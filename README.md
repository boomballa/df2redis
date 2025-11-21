# df2redis 🚀

Dragonfly → Redis 迁移工具的 Go 原型，目标是直接兼容 Dragonfly 复制协议完成全量+增量同步，不再依赖 Camellia 代理双写。

> 当前状态：仅完成 CLI 框架、配置解析、状态文件、基于 `redis-rdb-cli rmt` 的全量导入，以及仪表盘展示。Dragonfly journal 流的增量复制尚未实现，流水线会提示跳过该阶段。

## 现在能做什么
- 🧭 CLI：`prepare` / `migrate` / `status` / `rollback` / `dashboard`。
- 📦 全量导入：封装 `redis-rdb-cli rmt` 调用，按配置导入 Dragonfly 生成的 RDB。
- 📊 状态与仪表盘：`state/status.json` 记录阶段状态、指标、事件；可通过 `--show` / `dashboard` 查看。
- 🧹 清爽依赖：去掉 Camellia/JRE 预置与相关逻辑，保留 `redis-rdb-cli` 源码供构建使用；`dragonfly/` 仅作参考，不纳入版本控制。

待完成：
- Dragonfly 复制握手/DFLY FLOW/STARTSTABLE 接入。
- Journal 解析、命令重放、LSN 续传、多 shard 协调。
- Redis Cluster 路由与一致性校验。

## 目录速览
- `cmd/df2redis`: CLI 入口。
- `internal/cli`: 子命令解析。
- `internal/config`: 配置解析与默认值。
- `internal/pipeline`: 阶段化编排（预检、全量导入、增量占位）。
- `internal/executor/rdbcli`: `redis-rdb-cli rmt` 调用封装。
- `internal/state`: 状态快照存储。
- `internal/web`: 简易仪表盘。
- `docs/architecture.md`: 方向和技术要点，已更新为 Dragonfly 复制协议路线。
- `examples/migrate.sample.yaml`: 配置样例。
- `redis-rdb-cli/`: 上游工具源码，便于自行编译二进制。
- `camellia/`: 历史参考源码，当前未使用。
- `dragonfly/`: 上游 Dragonfly 源码（仅作比对参考，已 `.gitignore`）。

## 构建与运行
要求：
- Go 1.21+
- 可选：已编译好的 `redis-rdb-cli` 二进制（或使用仓库下源码自行构建）

```bash
# 构建
GOOS=linux GOARCH=amd64 go build -o bin/df2redis ./cmd/df2redis

# 查看帮助
./bin/df2redis --help

# 仅校验配置
./bin/df2redis migrate --config examples/migrate.sample.yaml --dry-run

# 执行全量导入（需提前准备 snapshot/rdbToolBinary）
./bin/df2redis migrate --config examples/migrate.sample.yaml

# 启动仪表盘
./bin/df2redis migrate --config examples/migrate.sample.yaml --show 8080
```

> 构建 redis-rdb-cli：进入 `redis-rdb-cli` 目录，按其文档编译生成 `bin/rmt`，并在配置里填入路径。

## 配置要点
详见 `examples/migrate.sample.yaml`，核心字段：
- `source.addr` / `target.seed`：源 Dragonfly、目标 Redis 地址。
- `migrate.snapshotPath`：Dragonfly 生成的 RDB 路径。
- `migrate.rdbToolBinary`：`redis-rdb-cli rmt` 可执行文件路径。
- `stateDir` / `statusFile`：状态文件输出位置。

## 路线图
1) Dragonfly 复制握手 + RDB 拉取（bgsave 或 PSYNC），替换外部导入为内置 loader。  
2) Journal 流解析器（packed uint + Op/LSN/SELECT/COMMAND），命令重放到 Redis/Redis Cluster。  
3) 断线重连与 LSN 续传、指标观测、回压与限流。  
4) 集群路由/slot 对齐、多 shard 协调与一致性校验。  

欢迎在 issue 中反馈需求与想法。
