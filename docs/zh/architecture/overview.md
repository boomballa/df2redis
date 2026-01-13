# 架构总览

df2redis 是一个高性能的复制工具，实现了 Dragonfly 的原生复制协议，支持实时数据迁移到 Redis/Redis Cluster。

## 设计原则

### 1. 零停机迁移
- **全量同步**：基于快照的批量数据传输（RDB 格式）
- **增量同步**：持续的 Journal 流重放实现实时更新
- **无缝切换**：全局同步屏障确保阶段切换时不丢失数据

### 2. 高性能
- **多 FLOW 并行**：并行数据流匹配 Dragonfly 的分片数量（N 为源端 Dragonfly 的 shard 数量）
- **智能批处理**：自适应批次大小（集群模式 20K，单机模式 2K）
- **集群路由优化**：基于主节点分组而非 slot 分组（100 倍性能提升）

### 3. 生产就绪
- **断点续传**：基于 LSN 的恢复机制
- **冲突处理**：可配置策略（overwrite/skip/panic）
- **监控**：内置 Dashboard 和指标导出

## 系统架构

![系统架构概览](../../images/architecture/RDB%2BJournal-data-flow.svg)

```
┌─────────────────────────────────────────────────────────────────┐
│                    Dragonfly Master (源端)                       │
│                      N 个 Shard (FLOW 0-N)                       │
└────────┬────────────────────────────────────────────────────────┘
         │
         │ RDB Stream + Journal Stream
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│                         df2redis                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  FLOW 层 (N 个 Goroutine)                                │  │
│  │    Reader 0-7：每个 FLOW 一个 TCP 连接                  │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Parser 层 (N 个 Goroutine)                              │  │
│  │    RDB Parser：Opcode → Entry                            │  │
│  │    Journal Parser：LSN, TxID, Command                    │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  全局同步屏障                                            │  │
│  │    等待所有 FLOW 完成 RDB 阶段                           │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Writer 层 (N 个 Goroutine)                              │  │
│  │    批次：20K 条目，缓冲区：2M 条目                       │  │
│  │    集群路由：基于节点分组                                │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────┬────────────────────────────────────────────────────────┘
         │
         │ Pipeline 命令
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│              Redis Cluster (目标端)                              │
│  Master 1 (Slots 0-5460)                                         │
│  Master 2 (Slots 5461-10922)                                     │
│  Master 3 (Slots 10923-16383)                                    │
└─────────────────────────────────────────────────────────────────┘
```

## 核心组件

| 组件 | 职责 | 关键文件 |
|------|-----|---------|
| **FLOW 管理器** | 建立和管理 N 个 FLOW 连接 | `internal/replica/replicator.go` |
| **RDB Parser** | 解码 Dragonfly RDB 流 | `internal/replica/rdb_parser.go` |
| **Journal Parser** | 解析 Journal 条目 | `internal/replica/journal_parser.go` |
| **集群路由** | 基于主节点的路由 | `internal/replica/flow_writer.go` |
| **Checkpoint 管理** | LSN 持久化 | `internal/state/checkpoint.go` |

## 数据流

```
握手 → FLOW 建立 → 全量同步 (RDB) → 屏障 → 稳定同步 (Journal)
                                       │
                                       └─→ 所有 FLOW 必须完成
```

## 关键创新

### 1. EOF Token 处理
**问题**：Dragonfly 在 `FULLSYNC_END` 后发送 40 字节 SHA1 校验和，可能导致流解析错位。

**解决方案**：显式读取并丢弃校验和，确保 Journal Parser 从正确位置开始。

```go
// 读取并丢弃 EOF Token，防止 "unknown opcode" 错误
eofToken := make([]byte, 40)
io.ReadFull(conn, eofToken)
```

### 2. 基于节点的集群路由
**问题**：Redis Cluster 有 16384 个 slot。按 slot 分组会导致 ~2000 个微型 Pipeline（每个只有 1 条命令），批次大小为 2000 时。

**解决方案**：按主节点分组而非 slot。

```go
// ✅ 正确：按主节点分组
func groupByNode(entries) {
    for entry := range entries {
        slot := crc16(entry.Key) % 16384
        masterAddr := clusterTopology[slot]  // 关键优化
        groups[masterAddr].append(entry)
    }
}

// 结果：3 个大 Pipeline（每个 ~666 条命令）
// 性能：10 ops/sec → 10,000+ ops/sec (1000 倍提升)
```

### 3. 全局同步屏障
**问题**：Dragonfly 要求所有 FLOW 完成 RDB 才能进入稳定同步。

**解决方案**：实现阻塞计数器模式。

```go
rdbCompletionBarrier := make(chan struct{})
var rdbCompleteCount atomic.Int32

// 每个 FLOW 完成时发出信号
if rdbCompleteCount.Add(1) == int32(numFlows) {
    close(rdbCompletionBarrier)  // 最后一个 FLOW 触发
}

<-rdbCompletionBarrier  // 主 Goroutine 等待
sendSTARTSTABLE()       // 进入稳定同步
```

## 性能特性

| 指标 | 数值 | 备注 |
|------|-----|------|
| **全量同步吞吐量** | 100,000+ ops/sec | 适当的批次大小 |
| **增量同步延迟** | <50ms | Journal 条目到 Redis |
| **内存使用** | ~16GB | N FLOWs × 2M buffer × 1KB/entry |
| **CPU 使用** | ~400% | N parser + N writer goroutines |

## 延伸阅读

- [复制协议深度解析](replication-protocol.md)
- [多 FLOW 架构](multi-flow.md)
- [集群路由优化](cluster-routing.md)
- [数据流水线与背压控制](data-pipeline.md)
