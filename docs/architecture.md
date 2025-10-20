# df2redis 架构草案（Go 版）

## 目标
- 将 Dragonfly 的数据实时同步/回滚到 Redis（单实例或 Cluster）。
- 通过单一 Go 二进制完成预检、双写、全量回灌、栅栏切换、灰度与回滚。
- 封装 Camellia-redis-proxy 与 redis-rdb-cli，屏蔽复杂操作，降低环境依赖。

## 工具链依赖
- **Camellia-redis-proxy**：负责双写、WAL、同 key 串行；由 df2redis 控制启动/监控。
- **redis-rdb-cli**：负责全量回灌（`rmt` 或 `rct`+自研 loader）。
- **Golang 1.21+**：核心编排逻辑使用 Go 实现，静态编译成单一可执行文件。

## 模块规划
1. **CLI 层（`cmd/df2redis`）**
   - 子命令：`prepare`、`migrate`、`status`、`rollback`、`help`、`version`。
   - 使用标准库 `flag` 实现，支持 `--config`、`--dry-run` 等参数。
   - 所有命令复用同一份配置加载逻辑。

2. **配置模型（`internal/config`）**
   - 提供轻量级 YAML 解析器（仅支持本项目需要的 map 结构）。
   - 映射为 `Config` 结构体，包含 `source`、`target`、`proxy`、`migrate`、`consistency` 等段落。
   - 提供默认值填充、字段合法性校验、状态目录解析。

3. **阶段编排（`internal/pipeline`）**
   - 抽象 Stage 与 Pipeline，内置上下文 `Context`（持有配置、状态目录、阶段共享数据）。
   - 支持顺序执行、失败中断，后续将扩展为可恢复的状态机。

4. **外部工具适配（`internal/executor`）**
   - `CamelliaManager`：封装启动/停止、WAL backlog 读取、meta hook 路径约束。
   - `RdbImportManager`：封装 redis-rdb-cli 调用、并发/限速参数转换、进程日志。
   - `RedisAdminClient`：计划基于 go-redis 执行元数据写入、Fence、抽样校验（待实现）。
   - `docs/camellia_hook.md` 介绍 Camellia 侧如何读取 `hook.json` 并执行 Lua。
   - `internal/runtime`：负责自动解压内置 Camellia Jar / 模板配置 / Lua / JRE；`df2redis` CLI 会在 auto 模式下生成带源/目标地址的配置文件。

5. **一致性组件（`internal/consistency`，规划中）**
   - 维护 `meta:{key}`、`MIGRATE_BASE_TS`、`MIGRATE_FENCE` 等键。
   - 提供 CAS Lua 模板管理、抽样校验与修复接口。

6. **可观测性与日志**
   - 先使用标准库 `log` 输出（带前缀、时间戳），后续视需要接入 `zerolog/zap`。
   - 计划输出阶段进度、WAL backlog、错误计数等指标（未来可挂 Prometheus）。

## 目录结构（初版）
```
df2redis/
  docs/
    architecture.md
  cmd/
    df2redis/
      main.go
  internal/
    cli/
    config/
    pipeline/
    executor/
    runtime/
    state/
    consistency/       # TODO
  internal/web/
    templates/
    static/
    server.go
  assets/
    camellia/          # 预置 Camellia jar/template
    lua/
    runtime/           # 可选 JRE 压缩包（命名可含平台关键字，如 linux、mac、arm64、x64）
  examples/
    migrate.sample.yaml
  camellia/            # 外部依赖（源码）
  redis-rdb-cli/       # 外部依赖
```

## 管线阶段概览
1. **Precheck**：路径/依赖校验、源/目标 Redis PING。
2. **Meta Hook**：加载 Lua，向 Camellia 写入 `hook.json`，便于代理在双写时调用脚本更新 `meta:{key}`。
3. **Start Proxy**：启动 Camellia 双写、记录初始 WAL。
4. **Baseline**：记录基线时间 `T0`，写入 `MIGRATE_BASE_TS`。
5. **Full Import**：调用 redis-rdb-cli 回灌快照，记录用时。
6. **Fence**：写入 `MIGRATE_FENCE`，监控 WAL 清零。
7. **Cutover**：读取 Fence、抽样比对源/目标键值，记录 mismatch 并标记灰度准备。
8. **Auto Cutover**：根据 `cutover.batches` 生成灰度推进计划，若 mismatch 超阈值则阻断切流。
9. **Dashboard**（可选）：实时展示迁移指标、阶段状态、事件时间线。
10. **Cleanup**：关闭双写连接、释放资源。

## 当前进展
- CLI 框架、配置解析、状态持久化落地。
- Camellia 启停、WAL backlog 探测、meta 脚本检查填充。
- redis-rdb-cli 调度、导入时长指标、Stage 状态写入。
- `status` 命令输出阶段快照、指标、事件；`rollback` 做状态标记。

## 后续阶段
- 接入 Camellia/redis-rdb-cli 进程管理与监控。
- 实现 CAS Lua 脚本部署、Fence 判定、抽样校验。
- 设计状态持久化格式，实现中断恢复 & `status` 指标展示。
- 集成测试：Docker-compose / K8s 真机演练，加入自动化测试脚本。
