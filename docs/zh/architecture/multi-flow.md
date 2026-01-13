# 多 FLOW 并行架构

df2redis 实现了完全并行的多 FLOW 架构，与 Dragonfly 的分片设计相匹配，以实现最大吞吐量。

## 概览

<!-- 🖼️ 多 FLOW 架构图占位符 -->
<!-- 替换为：docs/images/architecture/multi-flow.png -->
![多 FLOW 架构](../../images/architecture/multi-flow.png)

```
Dragonfly Master (N 个 Shard)
    │
    ├─ Shard 0 ────► FLOW-0 ────► Parser-0 ────► Writer-0 ─┐
    ├─ Shard 1 ────► FLOW-1 ────► Parser-1 ────► Writer-1 ─┤
    ├─ Shard 2 ────► FLOW-2 ────► Parser-2 ────► Writer-2 ─┤
    ├─ Shard 3 ────► FLOW-3 ────► Parser-3 ────► Writer-3 ─┤
    ├─ Shard 4 ────► FLOW-4 ────► Parser-4 ────► Writer-4 ─┼─► Redis Cluster
    ├─ Shard 5 ────► FLOW-5 ────► Parser-5 ────► Writer-5 ─┤
    ├─ Shard 6 ────► FLOW-6 ────► Parser-6 ────► Writer-6 ─┤
    └─ Shard 7 ────► FLOW-7 ────► Parser-7 ────► Writer-7 ─┘
                         │             │              │
                         │             │              │
                    TCP Stream    RDB Decoder    Batch Writer
                                   Journal         Pipeline
                                   Decoder
```

## 设计原理

### 为什么需要多 FLOW？

1. **并行性**：Dragonfly 将数据分片到多个线程。单连接复制会造成瓶颈。

2. **顺序性**：每个分片维护自己的顺序保证。在一个流中混合多个分片的数据会使 LSN 跟踪复杂化。

3. **性能**：多个并行流可以充分利用网络带宽和多核 CPU。

4. **可扩展性**：FLOW 数量随 Dragonfly 的分片数量扩展（可配置，通常为 N）。

## 架构层次

### 层 1：FLOW 连接管理器

**职责**：建立并维护到 Dragonfly 的 TCP 连接。

```go
type FLOWConnection struct {
    ID        int
    Conn      net.Conn
    SessionID string
    Reader    *bufio.Reader
}

func (r *Replicator) setupFLOWs(numFlows int) ([]*FLOWConnection, error) {
    flows := make([]*FLOWConnection, numFlows)

    for i := 0; i < numFlows; i++ {
        conn, err := net.Dial("tcp", r.config.Source.Addr)
        if err != nil {
            return nil, fmt.Errorf("FLOW-%d: connection failed: %w", i, err)
        }

        // Set TCP parameters for high throughput
        if tcpConn, ok := conn.(*net.TCPConn); ok {
            tcpConn.SetReadBuffer(10 * 1024 * 1024)  // 10MB
            tcpConn.SetKeepAlive(true)
            tcpConn.SetKeepAlivePeriod(15 * time.Second)
        }

        // Send DFLY FLOW command
        resp, err := redisx.Do(conn, "DFLY", "FLOW", strconv.Itoa(i), "1.0")
        sessionID := parseSessionID(resp)

        flows[i] = &FLOWConnection{
            ID:        i,
            Conn:      conn,
            SessionID: sessionID,
            Reader:    bufio.NewReaderSize(conn, 1024*1024),
        }

        log.Infof("[FLOW-%d] Connected, session: %s", i, sessionID)
    }

    return flows, nil
}
```

### 层 2：RDB Parser（每个 FLOW）

**职责**：将 RDB 流解码为结构化条目。

```go
func (r *Replicator) parseRDBStream(flowID int, conn *FLOWConnection) error {
    parser := NewRDBParser(conn.Reader)

    for {
        opcode, err := parser.ReadByte()
        if err != nil {
            return fmt.Errorf("[FLOW-%d] read opcode failed: %w", flowID, err)
        }

        if opcode == RDB_OPCODE_FULLSYNC_END {
            log.Infof("[FLOW-%d] RDB phase complete", flowID)
            break
        }

        entry, err := parser.ParseEntry(opcode)
        if err != nil {
            return fmt.Errorf("[FLOW-%d] parse entry failed: %w", flowID, err)
        }

        // Send to writer
        r.writers[flowID].Enqueue(entry)
    }

    return nil
}
```

### 层 3：Writer（每个 FLOW）

**职责**：批量累积条目并写入 Redis。

```go
type FlowWriter struct {
    flowID      int
    entryChan   chan *RDBEntry
    batchSize   int
    clusterClient *cluster.Client
}

func (fw *FlowWriter) batchWriteLoop() {
    batch := make([]*RDBEntry, 0, fw.batchSize)
    ticker := time.NewTicker(5 * time.Second)

    for {
        select {
        case entry := <-fw.entryChan:
            batch = append(batch, entry)

            if len(batch) >= fw.batchSize {
                fw.flushBatch(batch)
                batch = make([]*RDBEntry, 0, fw.batchSize)
            }

        case <-ticker.C:
            if len(batch) > 0 {
                fw.flushBatch(batch)
                batch = make([]*RDBEntry, 0, fw.batchSize)
            }
        }
    }
}
```

## 全局同步屏障

### 问题

Dragonfly 要求所有 FLOW 在进入稳定同步之前完成 RDB 阶段。如果 `DFLY STARTSTABLE` 发送过早：
- 某些 FLOW 仍在接收 RDB 数据
- 这些 FLOW 会错过转换期间发生的写入
- 数据不一致

### 解决方案：阻塞计数器模式

灵感来自 Dragonfly 的 `BlockingCounter` 实现。

```go
// 全局屏障
rdbCompletionBarrier := make(chan struct{})
var rdbCompleteCount atomic.Int32

// 每个 FLOW goroutine
for i := 0; i < numFlows; i++ {
    go func(flowID int) {
        // Parse RDB stream
        err := r.parseRDBStream(flowID, flows[flowID])
        if err != nil {
            log.Errorf("[FLOW-%d] RDB parsing failed: %v", flowID, err)
            return
        }

        // Read EOF token
        err = r.consumeEOFToken(flows[flowID])
        if err != nil {
            log.Errorf("[FLOW-%d] EOF token failed: %v", flowID, err)
            return
        }

        // Signal completion
        completed := rdbCompleteCount.Add(1)
        log.Infof("[FLOW-%d] RDB phase complete (%d/%d)", flowID, completed, numFlows)

        // Last FLOW closes the barrier
        if completed == int32(numFlows) {
            log.Info("🚧 All FLOWs completed RDB, releasing barrier")
            close(rdbCompletionBarrier)
        }

        // Wait for barrier (synchronize all FLOWs)
        <-rdbCompletionBarrier
        log.Infof("[FLOW-%d] Barrier released, entering stable sync", flowID)

        // Parse journal stream
        r.parseJournalStream(flowID, flows[flowID])
    }(i)
}

// Main goroutine waits for all FLOWs to synchronize
<-rdbCompletionBarrier
log.Info("Sending DFLY STARTSTABLE")
r.masterConn.Do("DFLY", "STARTSTABLE", stableSessionID, "0")
```

### 可视化

```
Time ─────────────────────────────────────────────►

FLOW-0  [RDB Parsing.......] ┤ Wait ├ [Journal Stream...]
FLOW-1  [RDB Parsing..........] ┤ Wt ├ [Journal Stream...]
FLOW-2  [RDB Parsing.....] ┤ Wait   ├ [Journal Stream...]
FLOW-3  [RDB Parsing........] ┤ Wait ├ [Journal Stream...]
FLOW-4  [RDB Parsing......] ┤ Wait  ├ [Journal Stream...]
FLOW-5  [RDB Parsing...........] ┤ W ├ [Journal Stream...]
FLOW-6  [RDB Parsing.........] ┤ Wai├ [Journal Stream...]
FLOW-7  [RDB Parsing..............] ├ [Journal Stream...]
                                    │
                                    └─ Barrier releases here
                                       (when FLOW-7 completes)
```

## 并发控制

### Channel 缓冲

每个 Writer 有 2M 条目的缓冲区来吸收突发流量：

```go
entryChan := make(chan *RDBEntry, 2000000)
```

**容量推理**：
- 平均条目大小：~1KB
- 缓冲区容量：2M 条目 × 1KB = 每个 FLOW 2GB
- 总内存：N FLOWs × 2GB = 16GB

### 基于信号量的批次限制

防止过多并发写操作：

```go
type FlowWriter struct {
    writeSemaphore chan struct{}  // Max concurrent batches
}

func NewFlowWriter(maxConcurrent int) *FlowWriter {
    return &FlowWriter{
        writeSemaphore: make(chan struct{}, maxConcurrent),
    }
}

func (fw *FlowWriter) flushBatch(batch []*RDBEntry) {
    // Acquire semaphore slot
    fw.writeSemaphore <- struct{}{}

    go func() {
        defer func() { <-fw.writeSemaphore }()  // Release slot

        // Write batch to Redis
        fw.writeBatchToRedis(batch)
    }()
}
```

## 性能特性

### 吞吐量

**全量同步（RDB 阶段）**：
- 单个 FLOW：~12,000 ops/sec
- 总计（N FLOWs）：~96,000 ops/sec

**稳定同步（Journal 阶段）**：
- 单个 FLOW：~5,000 ops/sec（受源写入速率限制）
- 总计（N FLOWs）：~40,000 ops/sec

### 延迟

- **RDB 解析**：每个条目 <0.1ms
- **批次累积**：5000ms（可配置，集群模式）
- **Redis 写入（Pipeline）**：每批（500 条目）5-20ms
- **端到端**：从 Dragonfly 写入到 Redis 确认 <50ms

### 资源使用

| 资源 | 单个 FLOW | 总计（N FLOWs）|
|----------|----------|--------------------|
| 内存 | 2GB | 16GB |
| CPU | ~50% | ~400% |
| 网络 | 10-50 MB/s | 80-400 MB/s |
| Goroutines | 2（parser + writer）| 16 |

## 故障处理

### FLOW 级别故障

如果单个 FLOW 失败：
- 记录错误并标记 FLOW 为失败
- 继续处理其他 FLOW
- 仅当超过关键阈值（例如 >25% 失败）时整个复制才失败

```go
var failedFlows atomic.Int32

go func(flowID int) {
    if err := r.parseRDBStream(flowID, flows[flowID]); err != nil {
        log.Errorf("[FLOW-%d] Failed: %v", flowID, err)
        failedFlows.Add(1)

        if failedFlows.Load() > int32(numFlows/4) {
            log.Fatal("Too many FLOWs failed, aborting replication")
        }
        return
    }
}(i)
```

### 网络中断

- TCP keepalive（15 秒）检测死连接
- 使用指数退避的自动重连
- 从上次 Checkpoint LSN 恢复

### 屏障死锁预防

如果一个 FLOW 挂起，屏障永远不会关闭。预防措施：

```go
// 基于超时的屏障
select {
case <-rdbCompletionBarrier:
    log.Info("All FLOWs completed normally")
case <-time.After(10 * time.Minute):
    log.Fatal("RDB phase timeout (10m), some FLOWs may be stuck")
}
```

## 监控

### 单个 FLOW 指标

```go
type FLOWStats struct {
    TotalEntries  int64
    TotalBytes    int64
    ErrorCount    int64
    LastActivityTime time.Time
}

// Export to Prometheus
flowEntriesTotal.WithLabelValues(strconv.Itoa(flowID)).Add(float64(count))
flowBytesTotal.WithLabelValues(strconv.Itoa(flowID)).Add(float64(bytes))
```

### 健康检查

检测卡住的 FLOW：

```go
func (r *Replicator) monitorFLOWHealth() {
    ticker := time.NewTicker(30 * time.Second)
    for range ticker.C {
        for i, stats := range r.flowStats {
            if time.Since(stats.LastActivityTime) > 60*time.Second {
                log.Warnf("[FLOW-%d] No activity for 60s, may be stuck", i)
            }
        }
    }
}
```

## 最佳实践

1. **匹配 Dragonfly 的分片数量**：使用与 Dragonfly 分片相同数量的 FLOW。
2. **适当调整缓冲区大小**：对于 1-10M 键的数据集，每个 FLOW 2M 条目是一个良好的平衡。
3. **监控屏障时间**：如果屏障持续 >5 分钟，调查慢速 FLOW。
4. **使用结构化日志**：在所有日志消息中包含 FLOW-ID，便于调试。

## 延伸阅读

- [复制协议深度解析](replication-protocol.md)
- [数据流水线与背压控制](data-pipeline.md)
- [性能调优指南](../guides/performance-tuning.md)
