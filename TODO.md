### TODO

- 实现 Dragonfly 复制握手 + flow 注册（PREPARATION）。
- 接入 journal 解析/命令重放，完成增量同步（STABLE_SYNC）。
- Redis Cluster 路由与 MOVED/ASK 处理，跟踪拓扑变化。
- 增量阶段的 LSN 断点续传、重连与回压。
- 全量导入阶段考虑替换外部 rdb tool 为内置 loader。
