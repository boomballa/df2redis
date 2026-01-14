<p align="center">
  <img src="docs/images/logo/df2redis.svg" width="100%" border="0" alt="df2redis logo">
</p>

# 🚀 df2redis

**高性能 Dragonfly 到 Redis 数据复制工具**

[English](README.md) | [中文](README.zh-CN.md)

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/yourusername/df2redis/pulls)

[功能特性](#-功能特性) • [快速开始](#-快速开始) • [架构设计](#-架构设计) • [文档](#-文档) • [贡献](#-贡献)

</div>

---

## 📖 概述

**df2redis** 是一个生产就绪的数据复制工具，实现了 Dragonfly 复制协议，能够实现从 **Dragonfly** 到 **Redis/Redis Cluster** 的无缝、高性能数据迁移。

与传统的基于代理的双写机制不同，df2redis 直接作为副本连接到 Dragonfly，同时执行**全量快照同步**和**实时增量同步**，确保零数据丢失和最小停机时间。

### 🎯 为什么选择 df2redis？

- **🔌 原生协议支持**：实现了 Dragonfly 复制协议（DFLY REPLICAOF、FLOW、Journal 流）
- **⚡ 高性能**：N 分片并行数据传输（N 为源端 Dragonfly 的 shard 数量），高效的 RDB 解析
- **🔄 实时同步**：通过 Journal 流处理实现持续增量复制
- **🛡️ 零数据丢失**：基于 LSN 的检查点机制，支持断点续传
- **🎨 零依赖**：纯 Go 实现，无外部运行时依赖
- **📊 可观测**：内置监控，提供详细的指标和进度跟踪

---

## ✨ 功能特性

### 核心能力

- ✅ **全量快照同步**
  - 完整的 RDB 解析，支持所有 Redis 数据类型（String、Hash、List、Set、ZSet）
  - 支持 Dragonfly 特有编码（Type 18 Listpack 格式）
  - N 分片并行数据传输（N 为源端 Dragonfly 的 shard 数量），实现最优吞吐量

- ✅ **增量同步**
  - 实时 Journal 流解析和命令重放
  - Packed uint 解码，实现高效数据传输
  - LSN（日志序列号）跟踪和持久化

- ✅ **复制协议**
  - 完整的 Dragonfly 握手实现（REPLCONF、DFLY REPLICAOF）
  - 多分片 FLOW 管理
  - EOF 令牌验证

- ✅ **可靠性**
  - LSN 检查点持久化，支持崩溃恢复
  - 自动重连，支持断点续传
  - 优雅关闭，保留状态

- ✅ **目标支持**
  - Redis 单机版
  - Redis Cluster，自动 Slot 路由
  - MOVED/ASK 错误处理

- ✅ **数据校验**
  - 集成 [redis-full-check](https://github.com/alibaba/RedisFullCheck)
  - 三种校验模式：完整/大纲/长度对比
  - 详细的不一致性报告，JSON 输出
  - 性能控制（QPS 限制、并行调优）

---

## 🚀 快速开始

### 前置要求

- **Go 1.21+**（从源码构建）
- **Dragonfly** 实例（源端）
- **Redis/Redis Cluster** 实例（目标端）

### 安装

#### 方式一：从源码构建

**在 Linux 上（CentOS 7 / Debian 11 / Ubuntu）：**

```bash
# 克隆仓库
git clone https://github.com/yourusername/df2redis.git
cd df2redis

# Linux (amd64) 构建 - 原生编译
go build -o bin/df2redis ./cmd/df2redis

# 或明确指定
GOOS=linux GOARCH=amd64 go build -o bin/df2redis ./cmd/df2redis

# 验证二进制文件
./bin/df2redis version
```

**在 macOS 上（用于 Linux 部署）：**

```bash
# 从 macOS 交叉编译到 Linux
GOOS=linux GOARCH=amd64 go build -o bin/df2redis ./cmd/df2redis

# macOS (ARM64 - M1/M2/M3) 构建
GOOS=darwin GOARCH=arm64 go build -o bin/df2redis-mac ./cmd/df2redis

# macOS (Intel) 构建
GOOS=darwin GOARCH=amd64 go build -o bin/df2redis-mac ./cmd/df2redis
```

**平台说明：**

| 平台 | 命令 | 输出二进制 | 说明 |
|----------|---------|---------------|-------|
| **CentOS 7** | `go build -o bin/df2redis ./cmd/df2redis` | `bin/df2redis` | 静态链接，无外部依赖 |
| **Debian 11** | `go build -o bin/df2redis ./cmd/df2redis` | `bin/df2redis` | 与 Ubuntu/Debian 二进制相同 |
| **Ubuntu 20.04+** | `go build -o bin/df2redis ./cmd/df2redis` | `bin/df2redis` | 与 CentOS/Debian 构建兼容 |
| **macOS (M1+)** | `GOOS=darwin GOARCH=arm64 go build` | `bin/df2redis-mac` | 用于本地测试 |
| **macOS (Intel)** | `GOOS=darwin GOARCH=amd64 go build` | `bin/df2redis-mac` | 用于本地测试 |

#### 方式二：下载预编译二进制

```bash
# 即将推出 - 请查看 releases 页面
```

---

## 🛠 命令参考

| 命令 | 说明 |
| --- | --- |
| `df2redis replicate --config <file>` | 启动完整复制（全量 RDB + 增量 Journal），持续运行。 |
| `df2redis migrate --config <file>` | 启动迁移（仅全量 RDB），完成后自动退出。使用高性能原生协议。 |
| `df2redis cold-import --config <file>` | 离线导入本地 RDB 文件（基于 `redis-shake`）。 |
| `df2redis check --config <file>` | 数据一致性校验（基于 `redis-full-check`）。 |
| `df2redis dashboard --config <file>` | 启动独立 Dashboard 服务。 |

---

## ⚡ 快速开始

#### 1. 创建配置文件

```bash
cp examples/replicate.sample.yaml config.yaml
```

编辑 `config.yaml`：

```yaml
source:
  addr: "192.168.1.100:16379"     # Dragonfly 地址
  password: ""                    # 可选密码
  tls: false

target:
  type: "redis-cluster"           # 或 "redis-standalone"
  addr: "192.168.2.200:6379"      # Redis 地址
  password: "your-password"
  tls: false

checkpoint:
  dir: "./checkpoint"             # LSN 检查点目录
  interval: 5                     # 检查点间隔（秒）
```

#### 2. 启动复制

```bash
# 试运行以验证配置
./bin/df2redis replicate --config config.yaml --dry-run

# 启动复制
./bin/df2redis replicate --config config.yaml

# 查看实时日志
tail -f logs/df2redis.log

# 冷态一次性导入 RDB（使用 redis-shake）
./bin/df2redis cold-import --config config.yaml --rdb ../tmp/latest.rdb
```

> `cold-import` 会直接调用 redis-shake，复用配置中的 `migrate.*` 字段（或 `--rdb` 覆盖）把 RDB 文件灌入目标 Redis，不会启动增量同步。

#### 3. 监控进度

工具会输出详细的进度信息：

```
🚀 启动 Dragonfly 复制器
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔗 连接到 Dragonfly: 192.168.1.100:16379
✓ 主连接建立成功

🤝 开始握手流程
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  [1/6] 发送 PING...
  ✓ PONG 收到
  [2/6] 声明监听端口: 16379...
  ✓ 端口已注册
  ...
  ✓ 所有 N 个 FLOW 连接已建立
✓ 握手完成

📦 开始并行接收和解析 RDB 快照...
  [FLOW-0] ✓ RDB 头部解析成功
  [FLOW-1] ✓ RDB 头部解析成功
  ...
  ✓ 快照同步完成

🔄 开始增量同步 (Journal 流式处理)
  → LSN: 1234567890
  → 已重放: 150,234 条命令
  → 延迟: 2.3ms
```

#### 4. 验证数据一致性

复制完成后，使用集成的检查命令验证数据一致性：

```bash
# 快速验证（键大纲模式 - 推荐）
./bin/df2redis check --config config.yaml --mode outline

# 完整验证（完整值对比）
./bin/df2redis check --config config.yaml --mode full --qps 200

# 查看详细结果
cat ./check-results/check_*.json | jq '.'
```

详细用法请参阅[数据校验指南](docs/data-validation.md)。

---

## 🏗️ 架构设计

df2redis 实现了完全并行的多 FLOW 架构，与 Dragonfly 的分片设计相匹配，以实现最大吞吐量。

### 高层设计

```
┌─────────────┐                    ┌──────────────┐
│             │   DFLY REPLICAOF  │              │
│  Dragonfly  │◄──────────────────│  df2redis    │
│   (Master)  │                    │  (Replica)   │
│             │                    │              │
│             │   Nx FLOW Streams  │              │
│             ├───────────────────►│              │
│             │   RDB + Journal    │              │
└─────────────┘                    └──────┬───────┘
                                          │
                                          │ Redis Protocol
                                          ▼
                                   ┌──────────────┐
                                   │    Redis     │
                                   │   Cluster    │
                                   └──────────────┘
```

### 核心设计原则

1. **零停机迁移** – 全量同步（RDB 快照）+ 增量同步（Journal 流）通过全局同步屏障实现无缝切换。

2. **高性能** – 并行 FLOW（数量与源端 shard 数相同）、智能批处理（集群模式 20K，单机模式 2K）、基于节点的集群路由（相比简单的 Slot 分组性能提升 100 倍）。

3. **生产就绪** – 基于 LSN 的 Checkpoint 机制支持断点续传、可配置的冲突策略、内置监控 Dashboard。

### 架构文档

详细的技术深度解析，请参阅架构文档：

- **[架构总览](docs/zh/architecture/overview.md)** – 高层架构、设计原则和核心创新
- **[复制协议深度解析](docs/zh/architecture/replication-protocol.md)** – 5 阶段协议详解（握手、FLOW 注册、全量同步、屏障、稳定同步）
- **[多 FLOW 并行架构](docs/zh/architecture/multi-flow.md)** – 并行 FLOW 设计、全局同步屏障、并发控制
- **[集群路由优化](docs/zh/architecture/cluster-routing.md)** – 基于节点 vs 基于 Slot 的分组（666 倍性能提升）
- **[数据流水线与背压控制](docs/zh/architecture/data-pipeline.md)** – 缓冲机制、批次累积、流量控制

### 复制流程

1. **握手阶段**
   - PING/PONG 交互
   - REPLCONF 协商（listening-port、capa、ip-address）
   - DFLY REPLICAOF 注册
   - 建立 N 个 FLOW 连接（N 由源端决定）

2. **快照阶段**
   - 通过多个并行 FLOW 接收 RDB 数据
   - 解析 RDB 条目（所有数据类型）
   - 根据正确的路由写入目标 Redis

3. **增量阶段**
   - 通过 FLOW 流接收 Journal 条目
   - 解码 Packed Uint 格式
   - 解析 Op/LSN/DbId/TxId/Command
   - 重放命令到目标 Redis
   - 持久化 LSN Checkpoint

### 关键组件

```
df2redis/
├── cmd/df2redis/           # CLI 入口点
├── internal/
│   ├── replica/            # 核心复制逻辑
│   │   ├── replicator.go   # 主复制器编排
│   │   ├── handshake.go    # Dragonfly 握手协议
│   │   ├── rdb_parser.go   # RDB 流解析器
│   │   ├── rdb_complex.go  # 复杂类型解析器（Hash/List/Set/ZSet）
│   │   ├── journal.go      # Journal 流处理器
│   │   └── checkpoint.go   # LSN 持久化
│   ├── checker/            # 数据校验（redis-full-check 包装）
│   ├── config/             # 配置管理
│   ├── redisx/             # Redis 客户端（RESP 协议）
│   └── util/               # 工具函数
├── docs/                   # 详细文档
└── examples/               # 配置示例
```

---

## 📚 文档

### 架构文档

深入的技术架构设计文档：

- **[系统概览](docs/zh/architecture/overview.md)** – 高层架构、设计原则和核心创新
- **[复制协议](docs/zh/architecture/replication-protocol.md)** – 5阶段协议分解（握手、FLOW注册、全量同步、屏障、稳定同步）
- **[多 FLOW 架构](docs/zh/architecture/multi-flow.md)** – 并行 FLOW 设计、全局同步屏障和并发控制
- **[集群路由优化](docs/zh/architecture/cluster-routing.md)** – 基于节点 vs 基于 Slot 的分组（666倍性能提升）
- **[数据流水线与背压控制](docs/zh/architecture/data-pipeline.md)** – 缓冲、批量累积和流量控制机制

### 技术研究笔记

记录 Dragonfly 协议分析和实现挑战的技术研究笔记：

- **[Dragonfly 复制协议](docs/zh/research/dragonfly-replica-protocol.md)** – Dragonfly Replica 复制协议、状态机和多 FLOW 握手机制的完整分析
- **[Stream RDB 格式分析](docs/zh/research/dragonfly-stream-rdb-format.md)** – Stream RDB 序列化格式在 V1/V2/V3 版本中的详细分解和 PEL 编码
- **[Stream 同步机制](docs/zh/research/dragonfly-stream-sync.md)** – Dragonfly 如何通过日志重写和精确 ID 跟踪确保 Stream 复制一致性
- **[全量同步性能](docs/zh/research/dragonfly-fullsync-performance.md)** – Dragonfly 高性能全量同步架构分析和 Redis 写入优化建议

### 详细指南

- [阶段 1：Dragonfly 复制握手](docs/Phase-1.md)
- [阶段 2：Journal 接收和解析](docs/Phase-2.md)
- [阶段 3：增量同步实现](docs/Phase-3.md)
- [阶段 4：LSN 持久化和检查点](docs/Phase-4.md)
- [阶段 5：RDB 复杂类型解析](docs/phase5-rdb-complex-types.md)
- [阶段 6：RDB 超时修复](docs/phase6-rdb-timeout-fix.md)
- [数据校验指南](docs/data-validation.md)
- [架构总览](docs/architecture.md)

### 其他文档

- [中文技术文档](docs/zh/) – 各复制阶段的深度解析、环境设置指南等
- [测试脚本指南](scripts/README.md) – 全面的测试文档
- [Dashboard API 参考](docs/api/dashboard-api.md) – 即将推出的 React UI 使用的 JSON 端点
- [前端设计草案](docs/zh/dashboard.md) – Material UI + Chart.js 布局方案和实现路线图

### 配置参考

<details>
<summary><strong>源端配置</strong></summary>

```yaml
source:
  addr: "192.168.1.100:16379" # Dragonfly 地址（必填）
  password: ""                # 认证密码（可选）
  tls: false                  # 启用 TLS（可选）
```
</details>

<details>
<summary><strong>目标端配置</strong></summary>

```yaml
target:
  type: "redis-cluster"       # "redis-standalone" 或 "redis-cluster"（必填）
  addr: "192.168.2.200:6379"  # Redis 地址（必填）
  password: "your_redis_password"  # 认证密码（可选）
  tls: false                  # 启用 TLS（可选）
```
</details>

<details>
<summary><strong>检查点配置</strong></summary>

```yaml
checkpoint:
  dir: "./checkpoint"         # 检查点目录（默认：./checkpoint）
  interval: 5                 # 检查点间隔（秒）（默认：5）
```
</details>

<details>
<summary><strong>冲突处理（RDB 快照阶段）</strong></summary>

```yaml
conflict:
  policy: "overwrite"         # 冲突处理策略（默认：overwrite）
                              # - overwrite: 直接覆盖重复键（性能最高）
                              # - panic: 检测到重复键时立即停止
                              # - skip: 跳过重复键并继续处理
```

**模式对比：**

| 模式 | 性能 | 使用场景 | 重复键行为 |
|------|-------------|----------|------------------------|
| **overwrite** | 最高（无 EXISTS 检查） | 生产迁移，替换目标数据 | 静默覆盖 |
| **panic** | 中等（EXISTS 检查） | 全新数据库迁移，验证 | 立即停止，记录键 |
| **skip** | 较低（EXISTS 检查） | 增量数据追加，部分同步 | 跳过并继续，记录键 |

**重要说明：**
- 冲突检查仅适用于 **RDB 快照阶段**，不适用于 Journal 流
- `panic` 和 `skip` 模式会记录重复键以便查看
- 大多数场景推荐使用 `overwrite`（零开销）

</details>

<details>
<summary><strong>高级选项</strong></summary>

```yaml
replica:
  listening_port: 16379        # 向主节点报告的监听端口（默认：16379）
  flow_timeout: 60            # FLOW 连接超时（秒）（默认：60）

logging:
  level: "info"               # 日志级别：debug/info/warn/error（默认：info）
  file: "logs/df2redis.log"   # 日志文件路径（可选）
```

> 日志说明：`log.dir` 相对配置文件所在目录解析，最终文件名为 `<任务名>_<命令>.log`。同名任务每次运行都会覆盖旧日志，详细步骤仅写入日志文件，终端只展示少量提示；如需完全静默，可将 `log.consoleEnabled` 设为 `false`。
</details>

---

## 🔧 高级使用

### 监控和指标

df2redis 提供详细的监控指标：

```bash
# 查看复制状态
./bin/df2redis status --config config.yaml

# 查看 LSN 检查点
cat checkpoint/lsn.json
```

检查点输出示例：

```json
{
  "lsn": 1234567890,
  "timestamp": "2025-12-04T02:15:30Z",
  "flow_status": {
    "0": {"lsn": 1234567890, "status": "streaming"},
    "1": {"lsn": 1234567888, "status": "streaming"},
    ...
  }
}
```

### 优雅关闭

df2redis 会优雅处理 SIGINT/SIGTERM 信号：

```bash
# 发送中断信号
kill -SIGTERM <pid>

# 或使用 Ctrl+C
^C
```

工具会：
1. 停止接收新的 Journal 条目
2. 将待处理命令刷新到 Redis
3. 保存最终的 LSN 检查点
4. 干净地关闭所有连接

### 从检查点恢复

重启后，df2redis 会自动从最后的检查点恢复：

```bash
# 重启复制 - 将从最后的 LSN 恢复
./bin/df2redis replicate --config config.yaml
```

---

## 🧪 测试

### 单元测试

```bash
# 运行所有测试
go test ./...

# 运行覆盖率测试
go test -cover ./...

# 运行特定包
go test ./internal/replica
```

### 集成测试

```bash
# 前提条件：运行中的 Dragonfly 和 Redis 实例
# 编辑测试配置
cp tests/integration.sample.yaml tests/integration.yaml

# 运行集成测试
go test -tags=integration ./tests/integration
```

---

## 📊 性能

### 基准测试结果

| 场景 | 数据大小 | 吞吐量 | 延迟 |
|----------|-----------|------------|---------|
| 全量同步 | 10GB | ~800 MB/s | N/A |
| 增量同步 | 10k ops/s | ~9.8k ops/s | <5ms |
| 并行分片 | 50GB | ~1.2 GB/s | N/A |

*测试环境：Dragonfly 1.x (8 分片配置)、Redis 7.x、网络：10Gbps、硬件：16 vCPU、32GB RAM*

### 优化建议

1. **增加 FLOW 并行度**：Dragonfly 的分片数决定 FLOW 数量
2. **调整检查点间隔**：在恢复时间和性能开销之间取得平衡
3. **使用 Redis 管道**：批量命令以实现更高吞吐量
4. **网络优化**：为复制流量使用专用网络

---

## 🛣️ 路线图

- [x] 阶段 1：Dragonfly 复制握手
- [x] 阶段 2：Journal 流处理
- [x] 阶段 3：增量同步
- [x] 阶段 4：LSN 检查点
- [x] 阶段 5：完整 RDB 类型支持
- [ ] 阶段 6：增强监控和指标
- [ ] 阶段 7：数据一致性验证
- [ ] 阶段 8：性能优化
- [ ] 阶段 9：生产加固

详细计划请参阅 [ROADMAP.md](ROADMAP.md)。

---

## 🤝 贡献

我们欢迎贡献！请参阅 [CONTRIBUTING.md](CONTRIBUTING.md) 了解指南。

### 开发环境设置

```bash
# Fork 并克隆仓库
git clone https://github.com/boomballa/df2redis.git
cd df2redis

# 安装依赖
go mod download

# 运行测试
go test ./...

# 构建
go build -o bin/df2redis ./cmd/df2redis
```

### 问题反馈

发现 bug 或有功能请求？请[提交 issue](https://github.com/boomballa/df2redis/issues)。

---

## 📄 许可证

本项目采用 MIT 许可证 - 详情请参阅 [LICENSE](LICENSE) 文件。

---

## 🙏 致谢

- [Dragonfly](https://github.com/dragonflydb/dragonfly) - 现代化的 Redis 替代方案
- [Redis](https://redis.io/) - 内存数据结构存储
- [Go 社区](https://go.dev/) - 优秀的工具和生态系统

---

## 📧 联系方式

- **邮箱**：boomballa0418@gmail.com
- **问题反馈**：[GitHub Issues](https://github.com/boomballa/df2redis/issues)

---

<div align="center">

**⭐ 如果你觉得 df2redis 有用，请给它一个星标！⭐**

用 ❤️ 由 df2redis 团队制作

</div>
