<p align="center">
  <img src="docs/images/logo/df2redis.svg" width="100%" border="0" alt="df2redis logo">
</p>

# 🚀 df2redis

**High-Performance Dragonfly → Redis Replication Toolkit**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/boomballa/df2redis/pulls)

[English](README.md) | [中文](README.zh-CN.md)

[Features](#-features) • [Quick Start](#-quick-start) • [Architecture](#-architecture) • [CLI](#-cli-commands) • [Documentation](#-documentation) • [Contributing](#-contributing)

</div>

---

## 📖 Overview

df2redis implements Dragonfly's replication protocol end-to-end so that Redis or Redis Cluster can act as a downstream target. The tool performs a full snapshot import followed by continuous journal replay, giving you a near-zero-downtime migration path without relying on proxies or dual-write mechanisms.

Key ideas:

- **Native protocol support** – handshake with Dragonfly via `DFLY FLOW`, detect FLOW topology, and request RDB + journal streams.
- **Parallel snapshot ingest** – multiple FLOW connections stream data concurrently, decoding Dragonfly-specific encodings (type-18 listpacks, QuickList 2.0, LZ4 compression, etc.).
- **Incremental catch-up** – Journal entries are parsed and routed to the correct Redis Cluster node, honoring transaction IDs, TTLs, and skip policies.
- **Multi-FLOW synchronization** – Barrier-based coordination ensures all FLOWs complete RDB phase before stable sync, preventing data loss during the handoff window.
- **Checkpointing** – LSN/state checkpoints are persisted so you can resume stable sync after interruptions.

---

## ✨ Features

### Replication pipeline

- ✅ Full snapshot import for String/Hash/List/Set/ZSet, including Dragonfly-only encodings.
- ✅ Incremental sync using journal streams with FLOW-aware routing.
- ✅ Automatic checkpoint persistence (configurable interval, resumable).
- ✅ Smart big-key validation via `redis-full-check` integration.

### Operational tooling

- 🛠 `df2redis replicate` – connect to Dragonfly, establish FLOWs, receive RDB, and start journal replay.
- 🧪 `df2redis check` – Native parallel data consistency checker.
- 📊 Dashboard stub (HTTP) powered by the `dashboard` command and persisted state files.
- 📂 Rich documentation under `docs/` (Chinese originals) plus the English excerpts in this README.

### Safety & Observability

- Per-FLOW stats, human-friendly logging with emoji markers, and optional log files.
- Conflict policies (`overwrite`, `skip`, `panic`) applied during snapshot ingestion.
- Graceful shutdown path that saves a final checkpoint and closes FLOW streams.

### Reliability & Correctness

- **RDB completion barrier** – All FLOWs synchronize before STARTSTABLE is sent, handling both FULLSYNC_END marker and direct EOF cases to prevent data loss during phase transition.
- **Journal replay accuracy** – Supports inline journal entries (0xD2 opcode) during RDB phase, ensuring writes during snapshot are captured.
- **Stream type support** – Correctly replicates XADD/XTRIM operations with Dragonfly's rewritten commands (MINID/MAXLEN exact boundaries).
- **LZ4 decompression** – Full support for Dragonfly's LZ4-compressed RDB blobs (0xCA opcode).

---

## ⚡ Quick Start

```bash
# 1. Edit your replicate config (see examples/replicate.sample.yaml)
cp examples/replicate.sample.yaml out/replicate.yaml
vim out/replicate.yaml

# 2. Run the replicator
./df2redis replicate --config out/replicate.yaml

# 3. (Optional) validate consistency
./df2redis check --config out/replicate.yaml --mode outline
```

Helpful tips:

1. The replicator auto-detects whether the target is standalone or Redis Cluster and opens one client per master node.
2. FLOWS are created after the `REPLCONF capa dragonfly` negotiation; Dragonfly sends one FLOW per shard.
3. RDB import happens before `DFLY STARTSTABLE`. Only after STARTSTABLE does Dragonfly emit EOF tokens, so the code waits before reading them.
4. Journal replay respects PING/SELECT/EXPIRED opcodes and only replays user commands.

---

## 🧱 Architecture

df2redis implements a fully parallel, multi-FLOW architecture that matches Dragonfly's shard-based design for maximum throughput.

![System Architecture](docs/images/architecture/df2redis_handdrawn.png)

### Core Design Principles

1. **Zero-Downtime Migration** – Full sync (RDB snapshot) + incremental sync (journal streaming) with seamless transition via global synchronization barrier.

2. **High Performance** – Parallel FLOWs (count matches source shard count), intelligent batching (20K for cluster, 2K for standalone), and node-based cluster routing (100x performance improvement over naive slot-based grouping).

3. **Production-Ready** – LSN-based checkpointing for resume capability, configurable conflict policies, and built-in monitoring dashboard.

### Key Modules

| Package | Purpose |
| --- | --- |
| `internal/replica` | FLOW handshake, RDB/JOURNAL parser, replay logic |
| `internal/cluster` | Minimal Redis Cluster client + slot calculator |
| `internal/checker` | `redis-full-check` orchestration |
| `internal/pipeline` | Legacy migrate pipeline (shake + snapshot) |

---

## 🛠 CLI Commands

| Command | Description |
| --- | --- |
| `df2redis replicate --config <file>` | Run full replication (Snapshot + Incremental Journal). Keeps running. |
| `df2redis migrate --config <file>` | Run migration (Snapshot Only). Exits after RDB phase. High performance. |
| `df2redis check --config <file> [flags]` | Launch native data consistency check (parallel scan & diff) |
| `df2redis dashboard --config <file>` | Start the standalone dashboard service |

`replicate` and `migrate` both use the native Dragonfly replication protocol for high-performance data transfer.

The embedded dashboard listens on `config.dashboard.addr` (default `:8080`). Override it in the YAML or pass `--dashboard-addr` to `replicate`/`--addr` to `dashboard`.

---

## 🧪 Testing

### Test Scripts

The `scripts/` directory contains comprehensive test suites:

```bash
# Install test dependencies
pip3 install -r scripts/requirements.txt

# Test all data types during RDB phase
bash scripts/manual_test_all_types.sh

# Test Stream type replication
python3 scripts/test_stream_replication.py

# Test RDB phase data consistency
python3 scripts/test_rdb_phase_sync.py
```

See [scripts/README.md](scripts/README.md) for detailed documentation on each test.

---

## 📚 Documentation

### Architecture Deep-Dives

Technical documentation explaining system design and implementation:

- **[System Overview](docs/en/architecture/overview.md)** – High-level architecture, design principles, and core innovations
- **[Replication Protocol](docs/en/architecture/replication-protocol.md)** – 5-phase protocol breakdown (handshake, FLOW registration, full sync, barrier, stable sync)
- **[Multi-FLOW Architecture](docs/en/architecture/multi-flow.md)** – Parallel FLOW design, global synchronization barrier, and concurrency control
- **[Cluster Routing Optimization](docs/en/architecture/cluster-routing.md)** – Node-based vs slot-based grouping (666x performance improvement)
- **[Data Pipeline & Backpressure](docs/en/architecture/data-pipeline.md)** – Buffering, batch accumulation, and flow control mechanisms

### Research Notes

Protocol analysis and implementation research:

- **[Dragonfly Replication Protocol](docs/en/research/dragonfly-replica-protocol.md)** – Complete protocol analysis, state machine, and multi-FLOW handshake
- **[Stream RDB Format](docs/en/research/dragonfly-stream-rdb-format.md)** – Stream serialization format across V1/V2/V3 and PEL encoding
- **[Stream Sync Mechanism](docs/en/research/dragonfly-stream-sync.md)** – Journal rewriting and precise ID tracking for Stream consistency
- **[Full Sync Performance](docs/en/research/dragonfly-fullsync-performance.md)** – High-performance architecture and Redis write optimizations

### User Guides

Operational documentation for users and operators:

- **[Data Validation Guide](docs/en/guides/data-validation.md)** – Using `redis-full-check` for consistency verification
- **[Dashboard Design](docs/en/guides/dashboard.md)** – Material UI + Chart.js layout plan and implementation roadmap

### Additional Resources

- [Chinese Documentation](docs/zh/) – Comprehensive Chinese technical documentation
- [Test Scripts Guide](scripts/README.md) – Comprehensive testing documentation
- [Dashboard API Reference](docs/api/dashboard-api.en.md) – JSON endpoints for monitoring

---

## 🤝 Contributing

1. Fork the repo and create a feature branch.
2. Run `go fmt ./...` and `go test ./...` (when applicable) before opening a PR.
3. Follow the existing logging style (emoji markers + concise explanations).
4. For protocol questions, read the Dragonfly sources under `dragonfly/src/server` or ping the maintainers.

MIT licensed. PRs and issues are always welcome!

---

## 🙏 Acknowledgements

We would like to express our gratitude to the following outstanding open-source projects:

- [RedisFullCheck](https://github.com/tair-opensource/RedisFullCheck) - Our data consistency checker (`df2redis check`) is inspired by and references the design of RedisFullCheck.
- [RedisShake](https://github.com/tair-opensource/RedisShake) - An excellent tool for data migration. If you need to import RDB files, we recommend referring to RedisShake's approach.
    - *Note*: When generating RDB files from Dragonfly for use with tools like RedisShake, please ensure you use `DF_SNAPSHOT_FORMAT=RDB` (or the equivalent `BGSAVE RDB` command) to produce Redis-compatible RDB files.
- [Dragonfly](https://github.com/dragonflydb/dragonfly) - This tool's core design references the Dragonfly source code, and its existence is dedicated to adapting Dragonfly's high-performance replication protocol for Redis synchronization.

---
