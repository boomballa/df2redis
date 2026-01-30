# df2redis English Documentation

> Dragonfly to Redis/Redis Cluster replication tool documentation

[中文文档](../zh/README.md) | [Back to Main](../README.md)

---

## 📚 Documentation Categories

### 🏗 Architecture

For developers and architects to understand df2redis technical implementation.

- [Overview](architecture/overview.md) - System architecture and component relationships
- [Replication Protocol](architecture/replication-protocol.md) - Dragonfly native replication protocol implementation
- [Multi-Flow Architecture](architecture/multi-flow.md) - Parallel FLOW architecture and shard synchronization
- [Data Pipeline](architecture/data-pipeline.md) - Data processing pipeline design
- [Cluster Routing](architecture/cluster-routing.md) - Redis Cluster command routing mechanism

### 📖 User Guides

For users and operators to quickly get started with df2redis.

- [Dashboard](guides/dashboard.md) - Web visualization interface design
- [Data Validation](guides/data-validation.md) - Data consistency validation tool

### 🔧 Troubleshooting

Common problem solutions and configuration guides.

- [redis-full-check Setup Guide](troubleshooting/redis-full-check-setup.md) - Data validation tool installation

### 🔬 Research

For advanced users and contributors, source code analysis and protocol research.

- [Dragonfly Replica Protocol Analysis](research/dragonfly-replica-protocol.md) - Dragonfly replication protocol source code analysis
- [Dragonfly RDB Format Analysis](research/dragonfly-rdb-format.md) - RDB data format research
- [Dragonfly Stream Sync](research/dragonfly-stream-sync.md) - Stream data structure synchronization mechanism
- [Dragonfly FullSync Performance](research/fullsync-performance.md) - High-performance bulk import implementation

---

## 🚀 Quick Start

### Recommended Reading Order for New Users

1. **Learn about the project** → [Main README](../../README.md)
2. **Understand architecture** → [Architecture Overview](architecture/overview.md)
3. **Deploy and use** → [Data Validation Guide](guides/data-validation.md)
4. **Deep dive** → [Replication Protocol](architecture/replication-protocol.md)

### Quick Links for Common Tasks

- **Want to understand replication?** → [Replication Protocol](architecture/replication-protocol.md)
- **Want to validate data consistency?** → [Data Validation](guides/data-validation.md)
- **Encountering issues?** → [Troubleshooting](troubleshooting/)
- **Want to study source code?** → [Research](research/)

---

## 📊 Diagram Resources

All architecture diagram Mermaid source files and generation scripts are located in the [diagrams/](../diagrams/) directory.

Main diagrams:
- [Replication Protocol Sequence (English)](../images/architecture/replication-protocol-en.svg)
- [State Machine Diagram (English)](../images/architecture/state-machine-diagram-en.svg)
- [Cluster Routing Diagram](../images/architecture/cluster-routing.svg)
- [Data Pipeline Diagram](../images/architecture/data-pipeline.svg)

---

## 🔗 Related Links

- [Project GitHub](https://github.com/boomballa/df2redis)
- [中文文档](../zh/README.md)
- [API Documentation](../api/dashboard-api.en.md)
- [Development Logs Archive](../archive/development-logs/)
