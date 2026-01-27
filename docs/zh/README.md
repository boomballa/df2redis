# df2redis 中文文档

> Dragonfly 到 Redis/Redis Cluster 复制工具文档

[English Documentation](../en/README.md) | [返回主目录](../README.md)

---

## 📚 文档分类

### 🏗 架构设计 (Architecture)

面向开发者和架构师，深入理解 df2redis 的技术实现。

- [架构概览 (Overview)](architecture/overview.md) - 系统整体架构与组件关系
- [复制协议 (Replication Protocol)](architecture/replication-protocol.md) - Dragonfly 原生复制协议实现
- [多流架构 (Multi-Flow)](architecture/multi-flow.md) - 并行 FLOW 架构与分片同步
- [数据流水线 (Data Pipeline)](architecture/data-pipeline.md) - 数据处理流水线设计
- [集群路由 (Cluster Routing)](architecture/cluster-routing.md) - Redis Cluster 命令路由机制

### 📖 使用指南 (User Guides)

面向用户和运维人员，快速上手使用 df2redis。

- [仪表盘 (Dashboard)](guides/dashboard.md) - Web 可视化界面设计
- [数据校验 (Data Validation)](guides/data-validation.md) - 数据一致性校验工具

### 🔧 故障排查 (Troubleshooting)

常见问题解决方案和配置指南。

- [redis-full-check 安装指南](troubleshooting/redis-full-check-setup.md) - 数据校验工具安装

### 🔬 深入研究 (Research)

高级用户和贡献者参考，源码分析与协议研究。

- [Dragonfly Replica 实现详解](research/dragonfly-replica-protocol.md) - Dragonfly 复制协议源码分析
- [Dragonfly RDB 格式详解](research/Dragonfly RDB 格式详细分析.md) - RDB 数据格式研究
- [Dragonfly Stream 类型同步](research/Dragonfly Stream 类型数据同步与主从一致性实现.md) - Stream 数据结构同步机制
- [Dragonfly 全量同步写入机制](research/Dragonfly 全量同步的高效写入机制详解.md) - 高性能批量导入实现

---

## 🚀 快速开始

### 新用户推荐阅读顺序

1. **了解项目** → [主 README](../../README.zh-CN.md)
2. **理解架构** → [架构概览](architecture/overview.md)
3. **部署使用** → [数据校验指南](guides/data-validation.md)
4. **深入学习** → [复制协议](architecture/replication-protocol.md)

### 常见任务快速链接

- **想了解复制原理？** → [复制协议](architecture/replication-protocol.md)
- **想校验数据一致性？** → [数据校验](guides/data-validation.md)
- **遇到问题需要排查？** → [故障排查](troubleshooting/)
- **想深入研究源码？** → [深入研究](research/)

---

## 📊 图表资源

所有架构图表的 Mermaid 源文件和生成脚本位于 [diagrams/](../diagrams/) 目录。

主要图表：
- [复制协议时序图 (中文)](../images/architecture/replication-protocol-zh.svg)
- [状态机图 (中文)](../images/architecture/state-machine-diagram-zh.svg)
- [集群路由图](../images/architecture/cluster-routing.svg)
- [数据流水线图](../images/architecture/data-pipeline.svg)

---

## 🔗 相关链接

- [项目 GitHub](https://github.com/boomballa/df2redis)
- [English Documentation](../en/README.md)
- [API 文档](../api/dashboard-api.md)
- [开发日志归档](../archive/development-logs/)
