# 架构草案：Dragonfly → Redis 复制工具

[English Version](en/architecture.md) | [中文版](architecture.md)

## 目标
- 以 Go 实现“伪 Dragonfly replica”，直接兼容 DFLY 复制协议。
- 支持 Dragonfly 主库到 Redis（含 Cluster）的全量 + 增量同步。
- 不依赖 Camellia 代理/双写；尽量减少外部运行环境依赖。

## 路线概览
1) **握手阶段**  
   - 连接 Dragonfly，发送 `REPLCONF`（capa eof/psync2/dragonfly）。  
   - 建立多 flow：`DFLY FLOW <id>`，准备 per-shard 通道。  

2) **全量阶段**  
   - 触发 `PSYNC` / `DFLY SYNC`，读取 RDB（disk-based 或 diskless）。  
   - 当前原型复用 `redis-rdb-cli rmt` 导入；后续可考虑内置 RDB loader 以减少依赖。  

3) **增量阶段（尚未实现）**  
   - 发送 `DFLY STARTSTABLE`，进入 journal streaming。  
   - 解析 Dragonfly journal 序列化（packed uint、Op/LSN/DbId/TxId/args）。  
   - 重放确定性命令到 Redis/Redis Cluster，维护 LSN 续传与断线重连。  
   - 多 shard 并发：每个 flow 独立 goroutine，跨 shard 顺序按收到的事件重放，单 shard 内保持顺序。  

4) **一致性与观测**  
   - 记录导入/重放延迟、落后量、错误计数。  
   - 抽样比对源/目标键值（后续补充），暴露 Prometheus 指标。  

## 模块规划
- `internal/cli`：解析子命令，加载配置，启动 Pipeline 和 Dashboard。  
- `internal/config`：轻量 YAML 解析、默认值与校验。  
- `internal/pipeline`：阶段化编排（预检、全量导入、增量占位）。  
- `internal/executor/rdbcli`：封装 `redis-rdb-cli rmt` 调用。  
- `internal/redisx`：轻量 RESP 客户端（后续可替换为更健壮的协议栈）。  
- `internal/state`：阶段状态、指标、事件持久化。  
- `internal/web`：状态、事件、指标的简易仪表盘。  
- 预留：`internal/replica`（待新增）实现握手、journal 解析与命令重放。  

## 关键技术点
- **Journal 解析**：实现 Dragonfly C++ 版 `JournalReader` 的 Go 等价物，支持 packed uint 解码、Op 分支处理（SELECT/COMMAND/EXPIRED/LSN/PING）。  
- **命令重写已确定性**：Dragonfly 在 journal 层已将部分非确定性命令改写（PEXPIREAT/RESTORE/SREM 等），可直接重放到 Redis。  
- **LSN/断点续传**：在增量阶段记录最后消费的 LSN，断线后重连继续；必要时自动回退到全量。  
- **Redis Cluster 路由**：根据 slot 发送到对应节点；处理 MOVED/ASK；必要时跟踪拓扑变化。  

## 待办
- [ ] 增量阶段：握手 + journal 解析 + 命令重放。  
- [ ] Redis Cluster 路由与错误恢复。  
- [ ] 一致性抽样/回填工具。  
- [ ] 完整的断点续传与重连策略。  

本文件会随实现推进持续更新。
