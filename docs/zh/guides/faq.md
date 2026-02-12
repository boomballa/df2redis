# 常见问题 (FAQ)

本文汇总了使用 df2redis 过程中可能遇到的典型问题及解决建议。

## 性能问题 🐢

### Q: 为什么增量同步的延迟 (Lag) 很高？

**现象**: 目标端 Redis 数据写入延迟超过 100ms，甚至数秒。

**可能原因**:
1.  **目标端性能瓶颈**: 目标 Redis (Cluster) 负载过高，Pipeline 写入被阻塞。
2.  **网络带宽不足**: 源端到目标端的网络带宽被跑满。
3.  **大 Key (BigKey) 阻塞**: 单个超大 Key (如几百 MB 的 Hash/List) 导致解析或写入变慢。

**解决建议**:
- 检查目标 Redis 的 CPU 和网络使用率。
- 调大并行度：`maxConcurrentBatches: 100`。
- 如果是 BigKey，建议使用 `df2redis check --mode smart` 定位大 Key。

### Q: 为什么全量迁移速度没有跑满带宽？

**可能原因**:
1.  **并发度不足**: 默认并发度可能偏保守。
2.  **目标端写入限流**: 云厂商 Redis 实例可能有 QPS/带宽限制。

**优化方案**:
- 增加 Flow 并发数（取决于 Dragonfly 分片数，无法手动增加，但可以确保 CPU 足够）。
- 调大 `batchSize` (建议 2000-5000)。
- 检查云 Redis 监控，确认是否触及写入上限。

---

## 连接问题 🔌

### Q: 报错 `connection reset by peer`

**现象**: 运行一段时间后，日志频繁出现连接断开。

**原因**:
- Dragonfly 默认 timeout (30s) 关闭了空闲连接。
- 你的 `df2redis` 运行在不稳定的网络环境（跨公网/跨机房）。

**解决建议**:
- 确保 `df2redis` 和数据库在同一 VPC/局域网。
- 开启 TCP KeepAlive（df2redis 默认已开启）。

### Q: 连不上 Dragonfly (`dial tcp: i/o timeout`)

**检查步骤**:
1.  **防火墙/安全组**: 检查 6379 (或 7380) 端口是否放行。
2.  **Bind 地址**: 确认 Dragonfly 启动时是否绑定了 `0.0.0.0` 或内网 IP (默认可能只绑了 `127.0.0.1`)。
3.  **密码**: 确认密码无误。

---

## 数据一致性 ⚖️

### Q: `check` 命令报 `inconsistent keys` 怎么办？

**排查步骤**:
1.  查看 check 生成的 JSON 报告，找到具体的 Key。
2.  在源端和目标端分别执行 `TTL <key>`, `TYPE <key>`, `DUMP <key>`。
3.  **常见不一致原因**:
    - **TTL 差异**: 源端 Key 在迁移过程中刚好过期。
    - **写入延迟**: 增量同步还在追数据，check 读取到了瞬时不一致 (建议开启 `--compare-times 3` 多轮复查)。
    - **Dragonfly 特性**: 使用了 Redis 不支持的特殊编码 (虽然 df2redis 会自动转换，但可能有边缘 Case)。

**处理**:
- 如果是极少数 Key 不一致，且是因为 TTL/动态写入导致的，通常可忽略。
- 如果大面积不一致，请提交 Issue 并附上 check 报告。

---

## 其他 🧩

### Q: 支持从 Redis 同步到 Dragonfly 吗？

**A**: 不支持。df2redis 专门设计用于 **Dragonfly -> Redis**。反向同步请直接使用 Dragonfly 自带的 `REPLICAOF` 命令（Dragonfly 原生支持作为 Redis 的副本）。

### Q: 是否支持断点续传？

**A**: 支持。df2redis 会定期保存 checkpoints (LSN)。如果你 Ctrl+C 停止进程，下次带上同样的配置启动，会自动从上次的 LSN 继续同步。如果想重新全量同步，请删除 `checkpoint.json` 文件。
