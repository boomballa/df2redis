# Phase 6: RDB 读取超时问题修复

[English Version](en/Phase-6.md) | [中文版](Phase-6.md)

## 问题背景

在 Phase 5 完成 RDB 复杂类型解析后，测试 FLOW RDB 快照传输时遇到读取超时问题：

```
[FLOW-7] ✓ RDB 头部解析成功
...
read tcp 192.168.1.101:19622->192.168.1.100:16379: i/o timeout
```

测试环境为小数据量（仅几条记录），理论上应快速完成，但仍然超时。

## 问题分析

### 初始错误理解

最初认为超时是由于"Dragonfly 准备大量数据时间过长"，因此引入了可配置的 RDB 超时机制：

1. 添加 `ReplicationConfig` 配置结构
2. 提供 `rdbTimeoutSeconds` 配置项（默认 600 秒）
3. 通过 `SetRdbTimeout()` 方法设置超时

这个设计基于**错误的假设**：Dragonfly 会"先准备完所有数据再发送"。

### 源码分析揭示真相

通过分析 Dragonfly 源码（`rdb_save.cc`、`dflycmd.cc`、`streamer.cc`），发现：

#### 1. Dragonfly 采用流式传输（非批量传输）

```cpp
// rdb_save.cc:1207-1233
error_code RdbSaver::Impl::WriteRecord(io::Bytes src) {
  error_code ec;
  size_t start_size = src.size();
  last_write_time_ns_ = absl::GetCurrentTimeNanos();
  do {
    io::Bytes part = src.subspan(0, 8_MB);  // 8MB 分块
    src.remove_prefix(part.size());
    ec = sink_->Write(part);

    int64_t now = absl::GetCurrentTimeNanos();
    unsigned delta_ms = (now - last_write_time_ns_) / 1000'000;
    last_write_time_ns_ = now;

    LOG_IF(INFO, delta_ms > 1000) << "Channel write took " << delta_ms << " ms";
  } while (!src.empty());
  return ec;
}
```

**关键发现**：数据被切分为 **8MB 块**，边序列化边发送，不存在"准备阶段"。

#### 2. Master 侧的写入停滞检测

```cpp
// dflycmd.cc:690-725
void DflyCmd::BreakStalledFlowsInShard() {
  // ...
  int64_t last_write_ns = replica_ptr->flows[sid].saver->GetLastWriteTime();
  int64_t timeout_ns = int64_t(absl::GetFlag(FLAGS_replication_timeout)) * 1'000'000LL;
  int64_t now = absl::GetCurrentTimeNanos();
  if (last_write_ns > 0 && last_write_ns + timeout_ns < now) {
    LOG(INFO) << "Master detected replication timeout, breaking full sync";
    replica_ptr->Cancel();
  }
}
```

**关键发现**：Master 监控 `last_write_time_ns`，若 **30 秒**内无写入，主动断开连接。

#### 3. 默认超时配置

```cpp
// streamer.cc:18-19
ABSL_FLAG(uint32_t, replication_timeout, 30000,
          "Time in milliseconds to wait for the replication writes being stuck.");
```

**默认值**：30000 毫秒 = **30 秒**

### 正确理解

1. **超时应检测"数据传输间隔"而非"总传输时长"**
2. 即使 1TB 数据，也是连续的 8MB 块流式传输
3. 若 60 秒内无数据到达，说明发生了真实问题（网络故障、Dragonfly 停滞）
4. 超时值与数据总量**无关**，只与单块传输时间有关

## 解决方案

### 设计原则

- 使用**固定 60 秒超时**（2 倍于 Dragonfly 的 30 秒检测时间）
- 启用 **TCP Keepalive**（30 秒探测周期）
- 移除可配置超时（避免用户误配置）

### 代码修改

#### 1. 移除配置结构

删除 `internal/config/config.go` 中的：
```go
// 删除 ReplicationConfig 字段
type Config struct {
    Source     SourceConfig     `json:"source"`
    Target     TargetConfig     `json:"target"`
    Migrate    MigrateConfig    `json:"migrate"`
    Checkpoint CheckpointConfig `json:"checkpoint"`
    StateDir   string           `json:"stateDir"`
    StatusFile string           `json:"statusFile"`
    // Replication ReplicationConfig `json:"replication"` // 已删除
}

// 删除整个结构体定义
// type ReplicationConfig struct { ... }
```

#### 2. 启用 TCP Keepalive

在 `internal/redisx/client.go` 的 `Dial()` 函数中：
```go
// 启用 TCP Keepalive（与 Dragonfly 的 30 秒超时检测协调）
if tcpConn, ok := conn.(*net.TCPConn); ok {
    if err := tcpConn.SetKeepAlive(true); err != nil {
        fmt.Fprintf(os.Stderr, "警告: 无法启用 TCP KeepAlive: %v\n", err)
    } else if err := tcpConn.SetKeepAlivePeriod(30 * time.Second); err != nil {
        fmt.Fprintf(os.Stderr, "警告: 无法设置 KeepAlive 周期: %v\n", err)
    }
}

client := &Client{
    addr:       cfg.Addr,
    password:   cfg.Password,
    conn:       conn,
    reader:     bufio.NewReader(conn),
    timeout:    defaultTimeout,
    rdbTimeout: 60 * time.Second, // 固定 60 秒，适用于所有场景
}
```

#### 3. 移除动态超时设置

删除 `internal/redisx/client.go` 中的：
```go
// 删除此方法
// func (c *Client) SetRdbTimeout(timeout time.Duration) { ... }
```

删除 `internal/replica/replicator.go` 中的调用：
```go
// 删除这些代码
// rdbTimeout := time.Duration(r.cfg.Replication.RdbTimeoutSeconds) * time.Second
// flowConn.SetRdbTimeout(rdbTimeout)
```

#### 4. 更新配置示例

从 `examples/replicate.sample.yaml` 删除：
```yaml
# 删除整个 replication 配置段
# replication:
#   flowTimeoutSeconds: 300
#   rdbTimeoutSeconds: 600
#   maxRetries: 3
```

## 技术亮点

### 1. 通用性设计

该方案同时适用于：
- **测试环境**（少量数据）：60 秒远超实际需求
- **生产环境**（TB 级数据）：每 8MB 块传输不会超过 60 秒

### 2. 与 Dragonfly 对齐

- Dragonfly Master 30 秒检测周期
- Replica 60 秒读取超时
- TCP Keepalive 30 秒探测

三层保护机制，确保及时发现连接问题。

### 3. 简化配置

移除可配置项，避免用户：
- 误设置过短超时（导致误报）
- 误设置过长超时（延迟故障发现）

## 验证方法

### 编译测试

```bash
# 检查语法
go build -o bin/df2redis ./cmd/df2redis

# 编译两个平台
GOOS=darwin GOARCH=arm64 go build -o bin/df2redis-mac ./cmd/df2redis
GOOS=linux GOARCH=amd64 go build -o bin/df2redis ./cmd/df2redis
```

### 功能测试

```bash
# 测试小数据场景
./bin/df2redis-mac replicate --config examples/replicate.sample.yaml

# 观察是否还有超时错误
```

预期结果：
- ✓ 小数据快速完成，不会超时
- ✓ 大数据持续传输，8MB 块间隔不会超过 60 秒
- ✓ 网络故障时能在 60 秒内检测到

## 相关文件

### 修改的代码文件

- `internal/config/config.go` - 删除 ReplicationConfig
- `internal/redisx/client.go` - 删除 SetRdbTimeout()，添加 TCP Keepalive
- `internal/replica/replicator.go` - 删除 SetRdbTimeout() 调用
- `examples/replicate.sample.yaml` - 删除 replication 配置段

### Git 提交

```bash
git commit -m "fix(replica): remove configurable RDB timeout and use fixed 60s with TCP keepalive"
```

## 经验总结

### 1. 避免假设驱动设计

初始方案基于**未经验证的假设**（批量传输），导致：
- 引入不必要的配置复杂度
- 设计出与实际传输模式不符的超时机制

**正确做法**：先查看源码理解实际行为，再设计方案。

### 2. 超时设计原则

- **应测量间隔而非总时长**：流式传输中，超时应基于"数据间隔"
- **固定值优于可配置**：当有明确技术依据时（如 Dragonfly 的 30 秒检测）
- **对齐上游行为**：Replica 超时应略大于 Master 检测周期

### 3. 多层保护机制

- **应用层**：60 秒读取超时
- **传输层**：30 秒 TCP Keepalive
- **对端监控**：Dragonfly Master 30 秒写入停滞检测

三层机制互补，确保可靠性。

## Phase 6.2: EOF Token 读取修复

### 用户测试发现的新问题

在应用 Phase 6.1 的超时修复后,用户测试发现:

**现象:**
- ✅ 数据成功同步到目标端(12 个键)
- ❌ 60 秒后仍然出现超时错误
- ❌ 新增键未触发增量同步
- ❌ 程序卡在全量同步阶段,未进入 stable sync

**用户反馈:**
> "那证明我们的程序卡在全量阶段了,并没有进入stable sync阶段,所以并没有触发增量同步。"

### 根本原因分析

通过分析 `internal/replica/replicator.go:545-548` 发现:

```go
if err == io.EOF {
    log.Printf("  [FLOW-%d] ✓ RDB 解析完成...")
    return  // ❌ goroutine 立即退出,未读取 EOF Token!
}
```

**问题链:**
1. RDB 解析完成,返回 `io.EOF`
2. Goroutine 立即 `return`,退出
3. 40 字节 EOF Token 仍留在 socket 缓冲区
4. 连接变为空闲
5. 60 秒读取超时触发
6. 从未发送 `DFLY STARTSTABLE`
7. 从未进入 Journal 接收模式

### 数据流时序确认

根据用户提供的 Dragonfly 源码分析,数据流顺序为:

```
[RDB data]
[RDB_OPCODE_JOURNAL_OFFSET (0xD3) + offset]
[RDB_OPCODE_FULLSYNC_END (0xC8) + 8 zero bytes]
[RDB_OPCODE_EOF (0xFF) + 8 byte checksum]  ← RDB Parser 在此返回 io.EOF
[40-byte EOF Token]  ← 紧接着发送,中间无间隔
[Journal data stream...]  ← 持续流式传输
```

**关键发现:**
- EOF Token 在 RDB_OPCODE_EOF 之后**立即发送**
- 中间没有其他数据或元数据块
- Journal 流在 STARTSTABLE 之前就已开始传输

### 最终解决方案

在 `receiveSnapshotAndVerifyEOF()` 的 goroutine 中修改:

```go
if err == io.EOF {
    log.Printf("  [FLOW-%d] ✓ RDB 解析完成（成功=%d, 跳过=%d, 失败=%d）",
        flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount)

    // RDB 解析完成后，立即读取 EOF Token (40 字节)
    // 根据 Dragonfly 源码，EOF Token 紧跟在 RDB_OPCODE_EOF + checksum 之后
    expectedToken := r.flows[flowID].EOFToken
    eofTokenBuf := make([]byte, len(expectedToken))
    log.Printf("  [FLOW-%d] → 正在读取 EOF Token (%d 字节)...", flowID, len(expectedToken))

    if _, err := io.ReadFull(flowConn, eofTokenBuf); err != nil {
        errChan <- fmt.Errorf("FLOW-%d: 读取 EOF Token 失败: %w", flowID, err)
        return
    }

    // 验证 EOF Token
    actualToken := string(eofTokenBuf)
    if actualToken != expectedToken {
        errChan <- fmt.Errorf("FLOW-%d: EOF Token 不匹配 (期望前8字节=%s..., 实际前8字节=%s...)",
            flowID, expectedToken[:8], actualToken[:8])
        return
    }
    log.Printf("  [FLOW-%d] ✓ EOF Token 验证成功", flowID)
    return
}
```

### 正确的执行流程

```
Replica 侧(我们的代码):
1. 解析 RDB 数据(直到 RDB_OPCODE_EOF)
2. 读取并验证 EOF Token(40 字节)  ← 新增
3. 保存剩余缓冲区(可能包含 journal 数据)
4. 等待所有 FLOW 完成以上步骤
5. 发送 DFLY STARTSTABLE
6. 切换到 Journal 流接收模式
7. 开始增量同步 ✓
```

### 代码修改

**文件:** `internal/replica/replicator.go`
**位置:** 行 545-568
**修改:** 在 io.EOF 处理中添加 EOF Token 读取和验证逻辑

### Git 提交

```bash
git commit -m "fix(replica): read and verify EOF Token immediately after RDB parsing"
```

## 下一步工作

- [ ] **用户实际测试**: 在真实 Dragonfly 环境中验证修复
- [ ] 测试不同数据量下的传输稳定性
- [ ] 监控实际传输中的数据块间隔时间
- [ ] 验证增量同步是否正常工作
- [ ] 考虑添加传输速率统计（每秒 MB 数）
