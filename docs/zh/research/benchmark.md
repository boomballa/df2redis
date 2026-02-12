# 性能基准测试报告 (Performance Benchmark)

本文档展示了 **df2redis** 在真实场景下的性能表现，特别是与传统迁移方案（如 RedisShake 离线导入）的对比。

## 1. 测试核心结论 🏆

> **TL;DR**: 在全量迁移场景下，df2redis 的**端到端迁移速度**比传统方案快 **X 倍**，且完全省去了中间存储开销。

| 方案 | 架构模式 | 全量迁移耗时 (10GB) | 吞吐量 (Avg) | 是否需要停机 |
| :--- | :--- | :--- | :--- | :--- |
| **df2redis** | **多路并行流式传输 (Multi-Flow Streaming)** | **[待填] 秒 (T1)** | **[待填] MB/s** | **否 (零停机)** |
| RedisShake | 串行文件导入 (Serial File Ingestion) | [待填] 秒 (T2) | [待填] MB/s | 是 (需 BGSAVE 窗口) |

*注：T2 = Dragonfly BGSAVE 时间 + RDB 传输时间 + RedisShake 导入时间*

---

## 2. 测试环境拓扑

```mermaid
graph LR
    subgraph Source_Zone
        DF[Dragonfly (8 Shards)]
        note1[数据量: 10GB<br>Key数量: 5000w]
    end

    subgraph Target_Zone
        Redis[Redis Cluster (3 Masters)]
    end

    DF -- "8x TCP Flows (df2redis)" --> Client[df2redis 运行节点]
    Client -- "Pipeline (Batch=20k)" --> Redis

    style DF fill:#ff9900,stroke:#333,stroke-width:2px
    style Redis fill:#dc382d,stroke:#333,stroke-width:2px
    style Client fill:#00add8,stroke:#333,stroke-width:2px
```

*   **硬件配置**:
    *   CPU: [例如: AWS c5.4xlarge (16 vCPU)]
    *   内存: [例如: 32GB]
    *   网络: [例如: 10Gbps 内网]
*   **软件版本**:
    *   Dragonfly: v1.x
    *   Redis: v7.0
    *   df2redis: v1.0.0

---

## 3. 详细测试场景

### 场景 A: 端到端全量迁移 (End-to-End Migration)

**测试目标**: 验证从“决定迁移”到“数据完全可用”的总耗时。

*   **df2redis 流程**:
    1.  启动 `df2redis replicate`。
    2.  并行建立 8 个 Flow，直接从内存拉取数据流。
    3.  实时解析并 Pipeline 写入目标 Redis。
    4.  **结果**: 仅需一次网络传输 RTT。

*   **传统方案流程**:
    1.  Dragonfly 执行 `BGSAVE` 落盘 (磁盘 IO 瓶颈)。
    2.  将 RDB 文件 scp 到迁移机 (网络 IO 瓶颈)。
    3.  RedisShake 读取文件并单线程写入 (解析 CPU 瓶颈)。

**[此处预留图表位置: 耗时对比柱状图]**
*(建议使用 Excel 生成图表后截图粘贴)*

### 场景 B: 并行扩展性 (Scalability)

**测试目标**: 验证 df2redis 是否能利用多核/多带宽优势。

| Dragonfly 分片数 | df2redis 并发配置 (Flows) | 吞吐量 (MB/s) |
| :---: | :---: | :---: |
| 4 | 4 | [待填] |
| 8 | 8 | [待填] |
| 16 | 16 | [待填] |

**[此处预留图表位置: 线性增长折线图]**

---

## 4. 分析与总结

1.  **流式架构优势**: df2redis 避免了昂贵的磁盘 IO 操作（BGSAVE），直接利用内存到网络的高速通道。
2.  **并行优势**: 传统的 RDB 解析器通常是单线程的，而 df2redis 为每个 Dragonfly 分片通过独立的 Goroutine 处理，能吃满客户端 CPU。
3.  **智能路由**: 针对 Redis Cluster 的 `MasterAddrForSlot` 预计算路由，减少了 Redis 端的 `MOVED` 跳转开销，进一步提升了写入 TPS。
