# Architecture Overview

df2redis is a high-performance replication toolkit that implements Dragonfly's native replication protocol to enable real-time data migration to Redis/Redis Cluster.

## Design Principles

### 1. Zero-Downtime Migration
- **Full Sync**: Snapshot-based bulk data transfer using RDB format
- **Incremental Sync**: Continuous journal stream replay for real-time updates
- **Seamless Transition**: Global synchronization barrier ensures no data loss during phase switch

### 2. High Performance
- **Multi-FLOW Parallelism**: Parallel data streams matching Dragonfly's shard count (N equals the source Dragonfly shard count)
- **Smart Batching**: Adaptive batch sizes (20K for cluster, 2K for standalone)
- **Cluster Routing Optimization**: Master node-based grouping instead of slot-based (100x performance gain)

### 3. Production-Ready
- **Checkpoint & Resume**: LSN-based recovery after interruptions
- **Conflict Handling**: Configurable policies (overwrite/skip/panic)
- **Monitoring**: Built-in dashboard and metrics export

## System Architecture

<!-- 🖼️ Architecture Diagram Placeholder -->
<!-- Replace with: docs/images/architecture/system-overview.png -->
![System Architecture](../../images/architecture/system-overview.png)

```
┌─────────────────────────────────────────────────────────────────┐
│                    Dragonfly Master (Source)                     │
│                      N Shards (FLOW 0-N)                         │
└────────┬────────────────────────────────────────────────────────┘
         │
         │ RDB Stream + Journal Stream
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│                         df2redis                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  FLOW Layer (N Goroutines)                               │  │
│  │    Reader 0-7: TCP connection per FLOW                   │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Parser Layer (N Goroutines)                             │  │
│  │    RDB Parser: Opcode → Entry                            │  │
│  │    Journal Parser: LSN, TxID, Command                    │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Global Synchronization Barrier                          │  │
│  │    Wait for all FLOWs to complete RDB phase              │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Writer Layer (N Goroutines)                             │  │
│  │    Batch: 20K entries, Buffer: 2M entries                │  │
│  │    Cluster Router: Node-based grouping                   │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────┬────────────────────────────────────────────────────────┘
         │
         │ Pipeline Commands
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│              Redis Cluster (Target)                              │
│  Master 1 (Slots 0-5460)                                         │
│  Master 2 (Slots 5461-10922)                                     │
│  Master 3 (Slots 10923-16383)                                    │
└─────────────────────────────────────────────────────────────────┘
```

## Core Components

| Component | Responsibility | Key Files |
|-----------|---------------|-----------|
| **FLOW Manager** | Establish and manage N FLOW connections | `internal/replica/replicator.go` |
| **RDB Parser** | Decode Dragonfly RDB stream | `internal/replica/rdb_parser.go` |
| **Journal Parser** | Parse journal entries | `internal/replica/journal_parser.go` |
| **Cluster Router** | Master node-based routing | `internal/replica/flow_writer.go` |
| **Checkpoint Manager** | LSN persistence | `internal/state/checkpoint.go` |

## Data Flow

```
Handshake → FLOW Setup → Full Sync (RDB) → Barrier → Stable Sync (Journal)
                                              │
                                              └─→ All FLOWs must complete
```

## Key Innovations

### 1. EOF Token Handling
**Problem**: Dragonfly sends a 40-byte SHA1 checksum after `FULLSYNC_END`, which can cause stream misalignment.

**Solution**: Explicitly read and discard the checksum to ensure journal parser starts at the correct position.

```go
// Read and discard EOF token to prevent "unknown opcode" errors
eofToken := make([]byte, 40)
io.ReadFull(conn, eofToken)
```

### 2. Node-Based Cluster Routing
**Problem**: Redis Cluster has 16384 slots. Grouping by slot results in ~2000 micro-pipelines (1 command each) for a 2000-entry batch.

**Solution**: Group by master node instead of slot.

```go
// ✅ Correct: Group by master node
func groupByNode(entries) {
    for entry := range entries {
        slot := crc16(entry.Key) % 16384
        masterAddr := clusterTopology[slot]  // Key optimization
        groups[masterAddr].append(entry)
    }
}

// Result: 3 large pipelines (~666 commands each)
// Performance: 10 ops/sec → 10,000+ ops/sec (1000x improvement)
```

### 3. Global Synchronization Barrier
**Problem**: Dragonfly requires all FLOWs to complete RDB before entering stable sync.

**Solution**: Implement a blocking counter pattern.

```go
rdbCompletionBarrier := make(chan struct{})
var rdbCompleteCount atomic.Int32

// Each FLOW signals completion
if rdbCompleteCount.Add(1) == int32(numFlows) {
    close(rdbCompletionBarrier)  // Last FLOW triggers
}

<-rdbCompletionBarrier  // Main goroutine waits
sendSTARTSTABLE()       // Enter stable sync
```

## Performance Characteristics

| Metric | Value | Notes |
|--------|-------|-------|
| **Full Sync Throughput** | 100,000+ ops/sec | With proper batch sizing |
| **Incremental Sync Latency** | <50ms | Journal entry to Redis |
| **Memory Usage** | ~16GB | N FLOWs × 2M buffer × 1KB/entry |
| **CPU Usage** | ~400% | N parser + N writer goroutines |

## Further Reading

- [Replication Protocol Deep Dive](replication-protocol.md)
- [Multi-FLOW Architecture](multi-flow.md)
- [Cluster Routing Optimization](cluster-routing.md)
- [Data Pipeline & Backpressure](data-pipeline.md)
