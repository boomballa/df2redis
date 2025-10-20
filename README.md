# df2redis

Dragonfly → Redis 迁移与回滚工具的 Go 实现原型。

## 当前能力
- CLI 子命令：`prepare` / `migrate` / `status` / `rollback`。
- Camellia 管控：`migrate` 流程中自动解压内置 Jar + 配置并启动代理，读取 WAL backlog，占位 meta hook。
- Meta Hook：自动加载 Lua 并生成 `hook.json` 给 Camellia 使用，实现 `meta:{key}` 双写回填。
- 全量导入：封装 `redis-rdb-cli rmt` 调用，自动拼装并发/pipeline/resume 参数。
- 状态文件：`state/status.json` 记录阶段状态、事件、指标；`status` 命令可观测。
- 配置解析：轻量 YAML（map-only）→ Go struct，带默认值、合法性校验。
- Pipeline 架构：阶段化执行，后续可扩展真实 Fence/Cutover 逻辑。

## 目录速览

- `cmd/df2redis`: CLI 入口。
- `internal/cli`: 子命令解析、状态查询、回滚标记。
- `internal/config`: 配置解析、默认值、校验、状态/工具路径处理。
- `internal/pipeline`: 阶段编排（预检、启动双写、基线、导入、Fence、清理等）。
- `internal/executor`: Camellia / redis-rdb-cli 封装。
- `internal/state`: 状态文件读写、指标/事件记录。
- `internal/redisx`: 轻量 RESP 客户端，与 Redis 源/目标交互。
- `docs/architecture.md`: 架构规划。
- `docs/camellia_hook.md`: Camellia Meta Hook 接入指引。
- `examples/migrate.sample.yaml`: 配置样例。
- `lua/`: 样例 meta hook Lua 脚本。
- `camellia/`, `redis-rdb-cli/`: 外部工具源码（后续集成）。

## 编译与示例

要求 Go 1.21+。

```bash
go build ./cmd/df2redis

# dry-run 仅校验配置
./df2redis migrate --config examples/migrate.sample.yaml --dry-run

# 正式执行（需准备 camellia、redis-rdb-cli、RDB 等）
./df2redis migrate --config examples/migrate.sample.yaml

# 带内置仪表盘运行
./df2redis migrate --config examples/migrate.sample.yaml --show 8080

# 查看状态文件
./df2redis status --config examples/migrate.sample.yaml
```

> 提示：默认配置下 `proxy.binary: auto`，第一次执行 `migrate` 时会自动在 `~/.df2redis/runtime/<version>/` 解压 Camellia Jar / 配置 / Lua，并优先使用 `assets/runtime/jre-<平台>.tar.gz` 内置 JRE（可按平台准备，如 `jre-darwin-arm64.tar.gz`、`jre-linux-amd64.tar.gz`）。若未提供内置 JRE，则会回退到系统 `java` 或 `JAVA_HOME`。Camellia Jar 会优先从 `assets/camellia/camellia-redis-proxy-bootstrap.jar` 复制，找不到则退回 `camellia/.../target/` 或提示补充文件。

### 打包运行时资产

为了实现“一站式”体验，请在发布前准备好：

- `assets/camellia/camellia-redis-proxy-bootstrap.jar`：从 `camellia-redis-proxy-bootstrap` 模块 `mvn package` 生成的 jar。
- `assets/runtime/jre-<平台>.tar.gz`：精简后的 JRE（例如 Adoptium/Temurin），解压后需包含 `bin/java`。文件名建议遵循 `jre-darwin-arm64.tar.gz`、`jre-linux-amd64.tar.gz` 等格式，或任何包含平台关键字（如 `linux`, `mac`, `darwin`, `arm64`, `x64`）的名字，工具会自动匹配。
- 如需自定义 Camellia 配置模板，可编辑 `assets/camellia/camellia-proxy.toml`，其中的 `{{SOURCE_URL}}`、`{{TARGET_URL}}`、`{{PORT}}` 等占位符会在运行时自动替换。

发布 tarball / 镜像时只需携带这些 asset，用户运行 `df2redis` 即会自动在本地缓存目录解压并使用，无需额外安装 Java 或手动摆放 Jar。

## 下一步
- 将 Camellia 双写侧代码落地（读取 `hook.json`、执行 Lua），并在生产侧验证。
- 灰度切读阶段接入实际流量控制（接入服务网关/流量调度 API）。
- 增强一致性校验：支持多类型 key、差异自动回写。
- 演练灰度切换、回滚剧本，补齐自动化测试与文档。

## 本地开发常用命令



## 本地开发常用命令

```bash
go build ./cmd/df2redis
./df2redis migrate --config examples/migrate.sample.yaml --show 8080
```
