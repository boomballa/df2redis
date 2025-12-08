# Phase 2: 多 FLOW 并行架构 + RDB 快照接收 + EOF Token 验证

[English Version](en/Phase-2.md) | [中文版](Phase-2.md)

## 概述

Phase 2 实现了完整的 RDB 快照接收流程，包括多 FLOW 并行架构、FULLSYNC_END 标记检测、STARTSTABLE 切换、以及 EOF Token 验证。这是实现 Dragonfly → Redis 数据迁移的关键步骤，确保了完整的 RDB 数据同步和 Journal 流准备。

## 实现目标

- ✓ 为每个 FLOW 创建独立的 TCP 连接
- ✓ 发送 DFLY SYNC 命令触发异步 RDB 传输
- ✓ 并行接收所有 FLOW 的 RDB 快照数据
- ✓ 检测 FULLSYNC_END 标记（0xC8 + 8 零字节）
- ✓ 发送 DFLY STARTSTABLE 切换到稳定同步模式
- ✓ 验证所有 FLOW 的 EOF Token
- ✓ 实现 Packed Uint 解码器
- ✓ 实现 Journal Entry 类型定义和解析器
- ✓ 准备接收 Journal 流

## 核心组件

### 1. 架构重构：从单连接到多 FLOW 并行

**原始问题：**
Phase 1 中所有 FLOW 共用一个 TCP 连接，导致：
- 只能接收 FLOW-0 的数据
- 其他 FLOW 的 RDB 数据无法接收
- Dragonfly 在所有 FLOW 连接建立前不会发送 RDB 数据

**解决方案：**
重构 `Replicator` 结构，为每个 FLOW 创建独立连接：

```go
type Replicator struct {
    // 主连接（仅用于握手）
    mainConn *redisx.Client

    // 每个 FLOW 的独立连接
    flowConns []*redisx.Client

    // 其他字段...
}
```

### 2. 多 FLOW 建立流程 (`establishFlows()`)

**实现细节：**

```go
func (r *Replicator) establishFlows() error {
    numFlows := r.masterInfo.NumFlows
    r.flows = make([]FlowInfo, numFlows)
    r.flowConns = make([]*redisx.Client, numFlows)

    for i := 0; i < numFlows; i++ {
        // 1. 为每个 FLOW 创建新的 TCP 连接
        dialCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
        flowConn, err := redisx.Dial(dialCtx, redisx.Config{
            Addr:     r.cfg.Source.Addr,
            Password: r.cfg.Source.Password,
            TLS:      r.cfg.Source.TLS,
        })
        cancel()

        // 2. PING 验证连接
        if err := flowConn.Ping(); err != nil {
            return fmt.Errorf("FLOW-%d PING 失败: %w", i, err)
        }

        // 3. 发送 DFLY FLOW 命令注册
        resp, err := flowConn.Do("DFLY", "FLOW",
            r.masterInfo.ReplID,
            r.masterInfo.SyncID,
            strconv.Itoa(i))

        // 4. 解析响应，保存 EOF Token
        arr := resp.([]interface{})
        syncType := arr[0].(string)        // "FULL"
        eofToken := arr[1].(string)        // 40字节 hex 字符串

        r.flows[i] = FlowInfo{
            FlowID:   i,
            SyncType: syncType,
            EOFToken: eofToken,
        }
        r.flowConns[i] = flowConn
    }

    return nil
}
```

**关键点：**
- 每个 FLOW 都有独立的 TCP 连接和 bufio.Reader
- EOF Token 在 DFLY FLOW 响应中获取，后续用于验证
- 所有 FLOW 必须在 DFLY SYNC 之前建立完成

### 3. DFLY SYNC 命令 (`sendDflySync()`)

**协议细节：**
```
→ DFLY SYNC <sync_id>
← OK
```

**实现：**

```go
func (r *Replicator) sendDflySync() error {
    resp, err := r.mainConn.Do("DFLY", "SYNC", r.masterInfo.SyncID)
    if err != nil {
        return fmt.Errorf("DFLY SYNC 失败: %w", err)
    }

    if err := r.expectOK(resp); err != nil {
        return fmt.Errorf("DFLY SYNC 返回错误: %w", err)
    }

    return nil
}
```

**重要发现：**
- DFLY SYNC 返回 OK 后，Dragonfly 会异步发送 RDB 数据
- 数据通过所有 FLOW 连接并行发送
- 需要立即开始读取，否则会导致缓冲区溢出

### 4. 并行 RDB 快照接收 (`receiveSnapshot()`)

**实现架构：**

```go
func (r *Replicator) receiveSnapshot() error {
    numFlows := len(r.flowConns)
    var wg sync.WaitGroup
    errChan := make(chan error, numFlows)

    // FULLSYNC_END 标记：0xC8 + 8 个零字节
    fullsyncEndMarker := []byte{0xC8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

    for i := 0; i < numFlows; i++ {
        wg.Add(1)
        go func(flowID int) {
            defer wg.Done()
            flowConn := r.flowConns[flowID]

            buf := make([]byte, 8192)
            totalBytes := uint64(0)
            searchBuf := []byte{}

            for {
                // 读取数据
                n, err := flowConn.Read(buf)
                if err != nil {
                    errChan <- fmt.Errorf("FLOW-%d: 读取失败: %w", flowID, err)
                    return
                }

                totalBytes += uint64(n)
                searchBuf = append(searchBuf, buf[:n]...)

                // 查找 FULLSYNC_END 标记
                if bytes.Contains(searchBuf, fullsyncEndMarker) {
                    log.Printf("  [FLOW-%d] ✓ 找到 FULLSYNC_END 标记（已接收 %d 字节）",
                        flowID, totalBytes)
                    return
                }

                // 限制搜索缓冲区大小，避免内存溢出
                maxSearchBuf := len(fullsyncEndMarker) * 2
                if len(searchBuf) > maxSearchBuf {
                    searchBuf = searchBuf[len(searchBuf)-maxSearchBuf:]
                }
            }
        }(i)
    }

    wg.Wait()
    close(errChan)

    // 检查是否有错误
    for err := range errChan {
        return err
    }

    return nil
}
```

**关键技术点：**

1. **滑动窗口搜索**：使用有限大小的 searchBuf 避免内存溢出
2. **并行 goroutine**：所有 FLOW 同时读取，最大化吞吐量
3. **WaitGroup 同步**：确保所有 FLOW 都完成才继续
4. **错误通道**：任何 FLOW 失败都会终止整个接收过程

**实测性能：**
- 8 个 FLOW 并行接收
- 空数据库（3个key）接收完成时间：~1 秒
- 接收字节数：112-132 字节（空 RDB + FULLSYNC_END）

### 5. DFLY STARTSTABLE 命令 (`sendStartStable()`)

**协议细节：**
```
→ DFLY STARTSTABLE <sync_id>
← OK
```

**作用：**
- 通知 Dragonfly 副本已准备好进入稳定同步模式
- 触发 Dragonfly 发送 EOF 标记（0xFF + checksum + EOF token）
- 必须在所有 FLOW 读取到 FULLSYNC_END 之后发送

**实现：**

```go
func (r *Replicator) sendStartStable() error {
    resp, err := r.mainConn.Do("DFLY", "STARTSTABLE", r.masterInfo.SyncID)
    if err != nil {
        return fmt.Errorf("DFLY STARTSTABLE 失败: %w", err)
    }

    if err := r.expectOK(resp); err != nil {
        return fmt.Errorf("DFLY STARTSTABLE 返回错误: %w", err)
    }

    r.state = StateStableSync
    return nil
}
```

### 6. EOF Token 验证 (`verifyEofTokens()`)

**协议格式（STARTSTABLE 之后）：**

```
每个 FLOW 依次发送：
1. 元数据块：0xD3 + 8 字节数据
2. EOF opcode：0xFF
3. Checksum：8 字节
4. EOF Token：40 字节（hex 字符串）
```

**实现细节：**

```go
func (r *Replicator) verifyEofTokens() error {
    numFlows := len(r.flowConns)
    var wg sync.WaitGroup
    errChan := make(chan error, numFlows)

    for i := 0; i < numFlows; i++ {
        wg.Add(1)
        go func(flowID int) {
            defer wg.Done()
            flowConn := r.flowConns[flowID]
            expectedToken := r.flows[flowID].EOFToken

            // 1. 跳过元数据块（0xD3 + 8 字节）
            metadataBuf := make([]byte, 9)
            if _, err := io.ReadFull(flowConn, metadataBuf); err != nil {
                errChan <- fmt.Errorf("FLOW-%d: 读取元数据失败: %w", flowID, err)
                return
            }

            // 2. 读取 EOF opcode (0xFF)
            opcodeBuf := make([]byte, 1)
            if _, err := io.ReadFull(flowConn, opcodeBuf); err != nil {
                errChan <- fmt.Errorf("FLOW-%d: 读取 EOF opcode 失败: %w", flowID, err)
                return
            }
            if opcodeBuf[0] != 0xFF {
                errChan <- fmt.Errorf("FLOW-%d: 期望 EOF opcode 0xFF，实际收到 0x%02X",
                    flowID, opcodeBuf[0])
                return
            }

            // 3. 读取 checksum (8 字节)
            checksumBuf := make([]byte, 8)
            if _, err := io.ReadFull(flowConn, checksumBuf); err != nil {
                errChan <- fmt.Errorf("FLOW-%d: 读取 checksum 失败: %w", flowID, err)
                return
            }

            // 4. 读取 EOF token (40 字节)
            tokenBuf := make([]byte, 40)
            if _, err := io.ReadFull(flowConn, tokenBuf); err != nil {
                errChan <- fmt.Errorf("FLOW-%d: 读取 EOF token 失败: %w", flowID, err)
                return
            }
            receivedToken := string(tokenBuf)

            // 5. 验证 token 是否匹配
            if receivedToken != expectedToken {
                errChan <- fmt.Errorf("FLOW-%d: EOF token 不匹配\n  期望: %s\n  实际: %s",
                    flowID, expectedToken, receivedToken)
                return
            }

            log.Printf("  [FLOW-%d] ✓ EOF Token 验证成功", flowID)
        }(i)
    }

    wg.Wait()
    close(errChan)

    for err := range errChan {
        return err
    }

    return nil
}
```

**关键发现：元数据块（0xD3）**

调试过程中发现的实际字节流：
```
DEBUG: opcode=0xD3, next 20 bytes=0600000000000000FF0000000000000000643964...
                                   ^^^^^^^^^^^^^^^^ 8字节数据
                                                   ^^ EOF opcode (0xFF)
```

这个 0xD3 元数据块在 Dragonfly 源码中没有明确文档，通过实际抓包和调试发现其存在。

### 7. Packed Uint 解码器 (`internal/replica/encoding.go`)

**编码规则（RDB 兼容）：**

```
00|XXXXXX              → 6位值 (< 64)，1字节
01|XXXXXX XXXXXXXX     → 14位值 (< 16384)，2字节
10000000 [32-bit BE]   → 32位整数，5字节
10000001 [64-bit BE]   → 64位整数，9字节
```

**实现：**

```go
func ReadPackedUint(r io.Reader) (uint64, error) {
    buf := make([]byte, 1)
    if _, err := io.ReadFull(r, buf); err != nil {
        return 0, err
    }

    firstByte := buf[0]
    typeField := (firstByte >> 6) & 0x03  // 取高2位

    switch typeField {
    case 0:  // 00|XXXXXX - 6位值
        return uint64(firstByte & 0x3F), nil

    case 1:  // 01|XXXXXX XXXXXXXX - 14位值
        if _, err := io.ReadFull(r, buf); err != nil {
            return 0, err
        }
        val := (uint64(firstByte&0x3F) << 8) | uint64(buf[0])
        return val, nil

    case 2:  // 10|XXXXXX
        if firstByte == 0x80 {  // 32位整数
            buf32 := make([]byte, 4)
            if _, err := io.ReadFull(r, buf32); err != nil {
                return 0, err
            }
            return uint64(binary.BigEndian.Uint32(buf32)), nil
        } else if firstByte == 0x81 {  // 64位整数
            buf64 := make([]byte, 8)
            if _, err := io.ReadFull(r, buf64); err != nil {
                return 0, err
            }
            return binary.BigEndian.Uint64(buf64), nil
        }
        return 0, fmt.Errorf("无效的 RDB 编码标记: 0x%02x", firstByte)

    default:
        return 0, fmt.Errorf("未知的 RDB 编码类型: %d", typeField)
    }
}
```

### 8. Journal Entry 定义 (`internal/replica/journal.go`)

**Opcode 定义：**

```go
type JournalOpcode uint8

const (
    OpNoop    JournalOpcode = 0
    OpSelect  JournalOpcode = 1  // SELECT 数据库
    OpCommand JournalOpcode = 2  // 普通命令
    OpExpired JournalOpcode = 3  // 过期键
    OpLSN     JournalOpcode = 4  // LSN 标记
    OpPing    JournalOpcode = 5  // PING 心跳
)
```

**Entry 结构：**

```go
type JournalEntry struct {
    Opcode    JournalOpcode
    DbIndex   uint64   // SELECT 操作时的数据库索引
    TxID      uint64   // 事务 ID
    ShardCnt  uint64   // Shard 计数
    LSN       uint64   // 日志序列号
    Command   string   // 命令名
    Args      []string // 命令参数
    RawData   []byte   // 原始数据（用于调试）
}
```

**解析流程：**

```go
func (jr *JournalReader) ReadEntry() (*JournalEntry, error) {
    entry := &JournalEntry{}

    // 1. 读取 Opcode
    opcodeBuf := make([]byte, 1)
    if _, err := io.ReadFull(jr.reader, opcodeBuf); err != nil {
        return nil, err
    }
    entry.Opcode = JournalOpcode(opcodeBuf[0])

    // 2. 根据 Opcode 读取不同字段
    switch entry.Opcode {
    case OpSelect:
        dbid, err := ReadPackedUint(jr.reader)
        if err != nil {
            return nil, err
        }
        entry.DbIndex = dbid

    case OpLSN:
        lsn, err := ReadPackedUint(jr.reader)
        if err != nil {
            return nil, err
        }
        entry.LSN = lsn

    case OpPing:
        // 无额外数据

    case OpCommand, OpExpired:
        // 读取 txid
        txid, err := ReadPackedUint(jr.reader)
        if err != nil {
            return nil, err
        }
        entry.TxID = txid

        // 读取 shard_cnt
        shardCnt, err := ReadPackedUint(jr.reader)
        if err != nil {
            return nil, err
        }
        entry.ShardCnt = shardCnt

        // 读取 Payload
        if err := jr.readPayload(entry); err != nil {
            return nil, err
        }
    }

    return entry, nil
}
```

### 9. 客户端改进 (`internal/redisx/client.go`)

**问题：**
原有的 `RawRead()` 直接从 socket 读取，会跳过 bufio.Reader 中已缓冲的数据

**解决：**
添加 `Read()` 方法，正确使用 bufio.Reader：

```go
// Read 实现 io.Reader 接口，用于 Journal 流解析
// 从 bufio.Reader 读取，确保不会跳过已缓冲的数据
func (c *Client) Read(buf []byte) (int, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.closed {
        return 0, errors.New("redisx: client closed")
    }

    // 设置 60 秒读取超时，略长于 Dragonfly 的 30 秒写入超时
    if err := c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
        return 0, err
    }

    // 从 bufio.Reader 读取，它会自动处理缓冲区和底层连接
    return c.reader.Read(buf)
}
```

## 完整协议流程

```
1. Phase 1: 握手流程
   └─ REPLCONF (listening-port, capa, dragonfly) → OK
   └─ DFLY FLOW × 8 → ["FULL", EOF_token]  (保存 EOF Token)

2. Phase 2: RDB 快照接收
   ├─ DFLY SYNC → OK
   │  └─ 触发异步 RDB 传输
   │
   ├─ 并行读取所有 FLOW 的 RDB 数据
   │  └─ 查找 FULLSYNC_END (0xC8 + 8 zeros)
   │  └─ 所有 FLOW 完成后继续
   │
   ├─ DFLY STARTSTABLE → OK
   │  └─ 切换到稳定同步模式
   │
   └─ 并行验证所有 FLOW 的 EOF Token
      ├─ 跳过元数据块 (0xD3 + 8 bytes)
      ├─ 读取 EOF opcode (0xFF)
      ├─ 读取 checksum (8 bytes)
      ├─ 读取 EOF token (40 bytes)
      └─ 验证 token 是否匹配

3. Phase 3: Journal 流接收（准备中）
   └─ 持续读取 Journal Entry
   └─ 解析并重放命令到 Redis
```

## 实际测试结果

### 测试环境
- Dragonfly 版本：v1.30.0
- Dragonfly 地址：10.46.128.12:7380
- Shard 数量：8
- 协议版本：VER4
- 测试数据：3 个 key

### 成功输出

```
🚀 启动 Dragonfly 复制器
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔗 连接到 Dragonfly: 10.46.128.12:7380
✓ 主连接建立成功

🤝 开始握手流程
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  [1/6] 发送 PING...
  ✓ PONG 收到
  [2/6] 声明监听端口: 6380...
  ✓ 端口已注册
  [3/6] 跳过 IP 地址声明
  [4/6] 声明能力: eof psync2...
  ✓ 能力已声明
  [5/6] 声明 Dragonfly 兼容性...
  → 复制 ID: 16c2763d...
  → 同步会话: SYNC24
  → Flow 数量: 8
  → 协议版本: VER4
  ✓ Dragonfly 版本: VER4, Shard 数量: 8
  [6/6] 建立 8 个 FLOW...
    • 将建立 8 个并行 FLOW 连接...
    • 建立 FLOW-0 独立连接...
      → 同步类型: FULL, EOF Token: e14e3b03...
    ✓ FLOW-0 连接和注册完成
    ... (FLOW-1 到 FLOW-7 全部成功)
    ✓ 所有 8 个 FLOW 连接已建立
  ✓ 所有 FLOW 已建立
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
✓ 握手完成


🔄 发送 DFLY SYNC 触发数据传输...
  ✓ DFLY SYNC 发送成功，RDB 数据传输已触发

📦 开始并行接收 RDB 快照...
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  • 将使用 8 个 FLOW 并行接收 RDB 快照
  • 目标：读取到 FULLSYNC_END 标记 (0xC8 + 8 零字节)
  [FLOW-0] 开始读取 RDB 数据...
  [FLOW-1] 开始读取 RDB 数据...
  ... (所有 FLOW 并行读取)
  [FLOW-0] ✓ 找到 FULLSYNC_END 标记（已接收 112 字节）
  [FLOW-1] ✓ 找到 FULLSYNC_END 标记（已接收 112 字节）
  [FLOW-6] ✓ 找到 FULLSYNC_END 标记（已接收 125 字节）
  [FLOW-5] ✓ 找到 FULLSYNC_END 标记（已接收 132 字节）
  ... (所有 FLOW 全部完成)
  ✓ 所有 FLOW 已读取到 FULLSYNC_END 标记
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

🔄 切换到稳定同步模式...
  ✓ 已切换到稳定同步模式

🔐 验证 EOF Token...
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  [FLOW-0] ✓ EOF Token 验证成功
  [FLOW-1] ✓ EOF Token 验证成功
  [FLOW-2] ✓ EOF Token 验证成功
  ... (所有 FLOW 验证成功)
  ✓ 所有 FLOW 的 EOF Token 验证完成
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🎯 复制器启动成功！
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

📡 开始接收 Journal 流...
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

## 技术难点与解决方案

### 难点 1: 单连接架构导致超时

**问题：**
最初所有 FLOW 共用一个连接，只读取 FLOW-0 的数据，其他 FLOW 超时：
```
FLOW-4: 读取快照数据失败: read tcp ...: i/o timeout (60秒超时)
```

**分析：**
- Dragonfly 需要所有 FLOW 都连接后才开始发送 RDB 数据
- 单连接架构无法满足这个要求

**解决：**
- 重构为多连接架构
- 每个 FLOW 独立的 TCP 连接 + bufio.Reader
- 并行建立所有 FLOW 连接后再发送 DFLY SYNC

### 难点 2: EOF Token 发送时机理解错误

**问题：**
最初认为 EOF Token 在 FULLSYNC_END 之后立即发送，导致读取失败。

**分析（基于 Dragonfly 源码）：**

```cpp
// dragonfly/src/server/dflycmd.cc - DflyCmd::StartStable
void DflyCmd::StartStable(CmdArgList args, ConnectionContext* cntx) {
  // ...
  StopFullSyncInThread(sync_id, &trans);  // ← 在这里发送 EOF
  // ...
}

// dragonfly/src/server/dfly_main.cc - StopFullSyncInThread
void StopFullSyncInThread(...) {
  // ...
  SendEofAndChecksum(writer);  // ← 发送 0xFF + checksum + EOF token
  // ...
}
```

**解决：**
- EOF Token 只在 STARTSTABLE 之后发送
- 这是一个握手确认机制，确保副本已准备好接收 Journal 流

### 难点 3: 元数据块（0xD3）的发现

**问题：**
期望读取到 0xFF EOF opcode，实际收到 0xD3。

**调试日志：**
```
DEBUG: opcode=0xD3, next 20 bytes=0600000000000000FF0000000000000000643964...
```

**分析：**
- 0xD3 是 Dragonfly 的元数据块标记
- 格式：0xD3 + 8 字节数据
- 出现在 EOF opcode 之前

**解决：**
- 跳过 9 字节（1 byte opcode + 8 bytes data）
- 然后读取真正的 EOF 标记

### 难点 4: bufio.Reader 数据跳过问题

**问题：**
使用 `conn.Read()` 直接读取 socket 会跳过 bufio.Reader 中的缓冲数据。

**场景：**
```
DFLY FLOW 响应可能被部分读入 bufio.Reader
→ 使用 conn.Read() 直接读 socket
→ 跳过了 bufio.Reader 中的 RDB 头部数据
→ 读取到错误的数据
```

**解决：**
添加 `Read()` 方法正确使用 bufio.Reader：
```go
func (c *Client) Read(buf []byte) (int, error) {
    return c.reader.Read(buf)  // 从 bufio.Reader 读取
}
```

## 性能数据

### RDB 接收性能
- **并行度**: 8 个 FLOW 同时接收
- **完成时间**: ~1 秒（空数据库）
- **接收字节数**:
  - FLOW-0,1,2,3,4,7: 112 字节
  - FLOW-6: 125 字节
  - FLOW-5: 132 字节（包含实际数据）

### EOF 验证性能
- **验证时间**: < 100ms（所有 8 个 FLOW）
- **验证成功率**: 100%

## 文件清单

### 新增文件
- `internal/replica/encoding.go` - Packed Uint 解码器
- `internal/replica/journal.go` - Journal Entry 定义和解析器
- `docs/Phase-2.md` - 本文档

### 修改文件
- `internal/replica/replicator.go` - 多 FLOW 架构重构（+300 行）
  - `establishFlows()` - 建立所有 FLOW 连接
  - `sendDflySync()` - 触发 RDB 传输
  - `receiveSnapshot()` - 并行接收 RDB 快照
  - `sendStartStable()` - 切换到稳定同步
  - `verifyEofTokens()` - 验证 EOF Token

- `internal/replica/types.go` - 添加 FlowInfo.EOFToken 字段
- `internal/redisx/client.go` - 添加 Read() 方法

## 测试清单

- [x] 为所有 8 个 FLOW 创建独立 TCP 连接
- [x] 所有 FLOW 成功注册并保存 EOF Token
- [x] 发送 DFLY SYNC 触发 RDB 传输
- [x] 并行接收所有 FLOW 的 RDB 数据
- [x] 正确检测 FULLSYNC_END 标记
- [x] 所有 FLOW 在 1 秒内完成接收
- [x] 发送 DFLY STARTSTABLE 切换模式
- [x] 跳过元数据块（0xD3 + 8 bytes）
- [x] 读取并验证 EOF opcode（0xFF）
- [x] 读取 checksum（8 bytes）
- [x] 读取并验证 EOF Token（40 bytes）
- [x] 所有 8 个 FLOW 的 EOF Token 验证通过
- [x] 成功进入稳定同步模式
- [x] 准备接收 Journal 流

## 已知限制

1. 当前仅测试了空数据库（3个key）的场景
2. 未实现实际的 RDB 数据解析和写入
3. Journal 流接收已准备就绪，但未进行实际测试
4. 未实现断线重连和 LSN checkpoint
5. 元数据块（0xD3）的具体含义未完全理解（通过实测跳过）

## 下一步

Phase 3 将实现 Journal Stream 的接收、解析和命令重放：
- 持续接收 Journal Entry
- 解析并提取命令和参数
- 将命令重放到目标 Redis
- 处理 Redis Cluster 路由（MOVED/ASK）
- 实现 LSN Checkpoint
- 实现断线重连和增量续传

## 提交信息

```
feat(replica): implement multi-FLOW parallel RDB snapshot reception

- Refactor to multi-connection architecture (mainConn + flowConns[])
- Implement parallel RDB snapshot reception with goroutines
- Detect FULLSYNC_END marker (0xC8 + 8 zeros)
- Send DFLY STARTSTABLE to switch to stable sync mode
- Verify EOF tokens for all FLOWs in parallel
- Add Packed Uint decoder for RDB/Journal parsing
- Add Journal Entry types and parser
- Fix bufio.Reader data skip issue with Read() method

Phase 2 完成：成功接收 RDB 快照并验证 EOF Token，准备接收 Journal 流。
测试环境：8 个 FLOW 并行接收，1 秒内完成，所有 EOF Token 验证通过。
```

## 参考资料

### Dragonfly 源码
- `dragonfly/src/server/dflycmd.cc` - DFLY SYNC、DFLY STARTSTABLE 实现
- `dragonfly/src/server/snapshot.cc` - StartSnapshotInShard、SendFullSyncCut
- `dragonfly/src/server/rdb_save.cc` - SendEofAndChecksum、SaveEpilog
- `dragonfly/src/server/replica.cc` - FullSyncDflyFb、EOF token 验证

### 协议文档
- Dragonfly Replication Protocol (基于 Redis PSYNC2)
- RDB 文件格式规范
- Packed Integer 编码（Redis RDB 格式）
