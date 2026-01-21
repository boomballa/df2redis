# Dragonfly v1.36.0 Bug Workaround

## 问题背景

### Dragonfly Bug 描述

Dragonfly v1.36.0 存在一个严重的 bug：当副本（replica）异常断开连接时，Dragonfly 主节点在清理副本连接时会发生**死锁或崩溃**。

**触发条件**：
- 副本在稳定同步（stable sync）阶段**异常断开**连接
- "异常断开"指：进程被杀、网络中断、突然关闭 socket（发送 TCP RST）

**Bug 根因**（来自 Dragonfly 源码分析）：

当副本断开时，Dragonfly 调用 `JournalStreamer::Cancel()` 清理资源：

```cpp
// streamer.cc:86-93 (v1.36.0)
void JournalStreamer::Cancel() {
  VLOG(1) << "JournalStreamer::Cancel";
  waker_.notifyAll();
  journal_->UnregisterOnChange(journal_cb_id_);
  // BUG: 无条件等待 inflight 写操作完成
  WaitForInflightToComplete();
}
```

`WaitForInflightToComplete()` 会无限期等待，直到所有 inflight 写操作完成：

```cpp
// streamer.cc:175-183 (v1.36.0)
void JournalStreamer::WaitForInflightToComplete() {
  while (in_flight_bytes_) {
    auto next = chrono::steady_clock::now() + 1s;
    std::cv_status status =
        waker_.await_until([this] { return this->in_flight_bytes_ == 0; }, next);
    LOG_IF(WARNING, status == std::cv_status::timeout)
        << "Waiting for inflight bytes " << in_flight_bytes_;
  }
}
```

**问题**：当连接已经断开（TCP RST），这些 inflight 写操作永远不会完成，导致：
- 每秒打印一次警告日志
- 线程永久阻塞
- 资源耗尽
- Dragonfly **崩溃**

### 官方修复状态

- **修复时间**：2025年2月20日
- **修复 commit**：a22daaf4
- **修复内容**：在等待前增加错误检查

```cpp
// streamer.cc:90-92 (修复后)
void JournalStreamer::Cancel() {
  VLOG(1) << "JournalStreamer::Cancel";
  waker_.notifyAll();
  journal_->UnregisterOnChange(journal_cb_id_);
  // 修复：只在连接正常时等待
  if (!cntx_->IsError()) {
    WaitForInflightToComplete();
  }
}
```

**问题**：
- 修复不在 v1.36.0 中
- 很多生产环境仍在使用 v1.36.0
- 升级 Dragonfly 有风险和成本

## df2redis 的 Workaround 方案

### 核心思想

**避免异常断开，实现优雅关闭**。

通过以下步骤确保 Dragonfly 有足够时间清理资源，不进入死锁路径：

1. **停止发送新的 REPLCONF ACK**
2. **等待 Dragonfly 处理 inflight 数据**
3. **发送 TCP FIN（而不是 RST）**
4. **等待 Dragonfly 确认 FIN**
5. **完全关闭连接**

### 具体实现

#### 1. 优雅关闭函数 (`Stop()`)

**位置**：`internal/replica/replicator.go:223-261`

**关键步骤**：

```go
func (r *Replicator) Stop() {
    // Step 1: 取消 context，停止心跳 goroutine
    // 这会阻止发送新的 REPLCONF ACK
    r.cancel()

    // Step 2: 等待 2 秒，让 Dragonfly 处理 inflight 的 ACK
    // 这是关键！确保 in_flight_bytes 降为 0
    time.Sleep(2 * time.Second)

    // Step 3: 半关闭 FLOW 连接（发送 FIN，不是 RST）
    // CloseWrite() 只关闭写端，保留读端
    for i, conn := range r.flowConns {
        conn.CloseWrite() // TCP FIN (graceful)
    }

    // Step 4: 再等待 1 秒，让 Dragonfly 确认 FIN
    time.Sleep(1 * time.Second)

    // Step 5: 完全关闭所有连接
    for i, conn := range r.flowConns {
        conn.Close()
    }
}
```

**为什么有效**：
- 2 秒等待确保 `in_flight_bytes` 降为 0
- `CloseWrite()` 发送 TCP FIN（优雅关闭）而不是 RST（强制关闭）
- Dragonfly 收到 FIN 后，知道副本主动断开，会优雅清理
- `if (!cntx_->IsError())` 检查通过，正常执行清理逻辑，不进入死锁

#### 2. CloseWrite() 方法

**位置**：`internal/redisx/client.go:137-155`

```go
func (c *Client) CloseWrite() error {
    // 获取底层 *net.TCPConn
    if tcpConn, ok := c.conn.(*net.TCPConn); ok {
        // 半关闭：只关闭写端，发送 FIN
        return tcpConn.CloseWrite()
    }
    // 非 TCP 连接，回退到完全关闭
    return c.Close()
}
```

**TCP 半关闭**：
- `CloseWrite()` 关闭写端 → 发送 TCP FIN
- 读端保持打开 → 可以接收 Dragonfly 的确认
- Dragonfly 看到 FIN → 知道是优雅关闭 → 不触发 bug

#### 3. Context 取消传播

心跳 goroutine 通过 context 取消停止：

```go
func (r *Replicator) startFlowHeartbeat(..., done chan struct{}) {
    ticker := time.NewTicker(1 * time.Second)
    for {
        select {
        case <-ticker.C:
            r.sendFlowACK(flowID, lsn)
        case <-done:
            return // 正常退出
        case <-r.ctx.Done():
            return // Context 取消，立即退出
        }
    }
}
```

当 `Stop()` 调用 `r.cancel()` 时，所有心跳 goroutine 立即退出。

## 测试验证

### 场景 1：用户主动停止（Ctrl+C）

**步骤**：
```bash
# 启动复制
./bin/df2redis replicate --config config.yaml --task-name test1 &

# 按 Ctrl+C 停止
# 预期：优雅关闭，Dragonfly 不崩溃
```

**日志输出**：
```
⏸  Stopping replicator gracefully...
  • Stopping heartbeat goroutines...
  • Waiting for Dragonfly to process inflight ACKs (2 seconds)...
  • Gracefully closing FLOW-0 connection
  • Gracefully closing FLOW-1 connection
  ...
  • Waiting for Dragonfly to acknowledge shutdown (1 second)...
  • Fully closing FLOW-0 connection
  • Fully closing FLOW-1 connection
  ...
  • Closing main connection
  • Waiting for all goroutines to exit...
✓ Replicator stopped gracefully (Dragonfly v1.36.0 bug workaround applied)
```

**验证 Dragonfly 状态**：
```bash
# Dragonfly 应该仍在运行，无错误日志
ps aux | grep dragonfly
tail -n 50 /path/to/dragonfly.log
```

### 场景 2：源端不可达（网络中断）

**步骤**：
```bash
# 启动复制
./bin/df2redis replicate --config config.yaml --task-name test2 &

# 模拟网络中断（防火墙阻断）
iptables -A INPUT -s <dragonfly-ip> -j DROP

# 预期：df2redis 检测到 EOF，优雅关闭
```

**预期行为**：
- df2redis 检测到读取错误（EOF）
- 调用 `Stop()` 优雅关闭
- Dragonfly 不崩溃

### 场景 3：长时间运行（跨越 SSH 超时）

**步骤**：
```bash
# 启动复制（不用 screen）
./bin/df2redis replicate --config config.yaml --task-name long-run &

# 等待 > 30 分钟（超过 SSH 超时）
# SSH 断开，重新连接

# 检查进程状态
ps aux | grep df2redis  # 应该还在运行
ps aux | grep dragonfly  # 应该还在运行
```

**验证**：
- df2redis 持续运行（SIGHUP 已忽略）
- Dragonfly 持续运行（没有被 df2redis 触发 bug）

## 对比：修复前 vs 修复后

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| 用户 Ctrl+C | Dragonfly **崩溃** ❌ | Dragonfly 正常 ✅ |
| 网络中断 | Dragonfly **崩溃** ❌ | Dragonfly 正常 ✅ |
| SSH 超时 | Dragonfly **崩溃** ❌ | Dragonfly 正常 ✅ |
| 进程被杀 | Dragonfly **崩溃** ❌ | 有一定概率正常 ⚠️ |

**注意**：
- 如果进程被 `kill -9` 强杀，无法执行优雅关闭，仍可能触发 bug
- 建议使用 `kill -TERM` 或 `Ctrl+C`，让进程有机会优雅退出

## 生产环境建议

### 1. 监控 Dragonfly 健康

```bash
#!/bin/bash
# monitor_dragonfly.sh

while true; do
    # 检查 Dragonfly 是否运行
    if ! pgrep -f dragonfly > /dev/null; then
        echo "[$(date)] Dragonfly down! Alerting..."
        # 发送告警
        send_alert "Dragonfly crashed"

        # 自动重启（可选）
        # ./restart_dragonfly.sh
    fi

    # 检查 Dragonfly 日志是否有 "Waiting for inflight bytes" 警告
    if tail -n 100 /path/to/dragonfly.log | grep -q "Waiting for inflight bytes"; then
        echo "[$(date)] Dragonfly deadlock detected! Alerting..."
        send_alert "Dragonfly deadlock (bug triggered)"
    fi

    sleep 30
done
```

### 2. 优先使用 systemd 管理

```ini
[Unit]
Description=df2redis Replication Service
After=network.target

[Service]
Type=simple
User=dba
WorkingDirectory=/home/dba/df2redis
ExecStart=/home/dba/df2redis/bin/df2redis replicate --config /home/dba/df2redis/config/prod.yaml --task-name prod

# 优雅关闭：发送 SIGTERM，等待 30 秒
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=30

# 故障重启
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
```

systemd 会发送 SIGTERM（不是 SIGKILL），给进程优雅退出的机会。

### 3. 使用信号处理脚本

```bash
#!/bin/bash
# graceful_stop.sh

PID=$(pgrep -f "df2redis replicate")

if [ -z "$PID" ]; then
    echo "df2redis not running"
    exit 0
fi

echo "Sending SIGTERM to df2redis (PID: $PID)..."
kill -TERM $PID

# 等待优雅退出
for i in {1..30}; do
    if ! ps -p $PID > /dev/null 2>&1; then
        echo "df2redis stopped gracefully"
        exit 0
    fi
    sleep 1
done

# 如果 30 秒后还没退出，强制杀
echo "Timeout, sending SIGKILL..."
kill -9 $PID
```

### 4. 定期健康检查

在 df2redis 中添加健康检查端点（未来功能）：

```bash
#!/bin/bash
# health_check.sh

while true; do
    # 检查 df2redis 健康状态
    if ! curl -f http://localhost:7777/health > /dev/null 2>&1; then
        echo "[$(date)] df2redis unhealthy, restarting..."
        systemctl restart df2redis
    fi
    sleep 60
done
```

## 技术细节

### TCP 连接状态转换

**正常关闭（FIN）**：
```
df2redis                 Dragonfly
   |                         |
   | -------- FIN --------> |  (CloseWrite)
   |                         |
   | <------- ACK --------- |  (确认 FIN)
   |                         |
   | <------- FIN --------- |  (Dragonfly 也关闭)
   |                         |
   | -------- ACK --------> |  (确认)
   |                         |
CLOSED                    CLOSED
```

**异常关闭（RST）**：
```
df2redis                 Dragonfly
   |                         |
   | -------- RST --------> |  (Close)
   |                         |
ERROR                     ERROR
                          (触发 bug)
```

### Dragonfly 清理流程

1. `OnClose()` 被调用（连接断开）
2. 调用 `ReplicaInfo::Cancel()`
3. 对每个分片，调用 `FlowInfo::cleanup()`
4. 调用 `JournalStreamer::Cancel()`
5. **关键**：检查 `cntx_->IsError()`
   - 如果连接优雅关闭（FIN），`IsError()` 返回 false
   - 调用 `WaitForInflightToComplete()`，但 inflight 已清空，立即返回
   - 如果连接异常关闭（RST），`IsError()` 返回 true（v1.36.0 没有检查）
   - v1.36.0 无条件调用 `WaitForInflightToComplete()` → **死锁**

## 总结

### 根本原因
- Dragonfly v1.36.0 的 `JournalStreamer::Cancel()` 缺少错误检查
- 异常断开连接触发无限等待死锁

### df2redis 的 Workaround
- 优雅关闭：停止心跳 → 等待清空 → 发送 FIN → 等待确认 → 完全关闭
- 避免触发 Dragonfly bug

### 适用版本
- **Dragonfly**: v1.36.0 及更早版本
- **df2redis**: v2.x（本文档对应版本）

### 后续升级建议
- 当 Dragonfly 升级到包含修复的版本（2025年2月20日后）时，本 workaround 仍然有效且无害
- 优雅关闭是最佳实践，即使 Dragonfly 修复了 bug，也应该保留

### 关键代码位置
- `internal/replica/replicator.go:223-261` - Stop() 优雅关闭逻辑
- `internal/redisx/client.go:137-155` - CloseWrite() 半关闭实现
- `internal/cli/cli.go:616-648` - 信号处理和 Stop() 调用
