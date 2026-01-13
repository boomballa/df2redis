<div align="center">

# 🚀 df2redis

**High-Performance Dragonfly → Redis Replication Toolkit**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/yourusername/df2redis/pulls)

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
- 🧪 `df2redis check` – wrapper around `redis-full-check` with friendly defaults.
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

```
Dragonfly (source)
   ├─ Main connection (handshake, STARTSTABLE)
   ├─ FLOW-0 ... FLOW-n (RDB + journal)
   └─ Checkpoints persisted locally

Redis / Redis Cluster (target)
   ├─ Cluster client with slot routing
   └─ Conflict policy + TTL restoration
```

### Core Design Principles

1. **Zero-Downtime Migration** – Full sync (RDB snapshot) + incremental sync (journal streaming) with seamless transition via global synchronization barrier.

2. **High Performance** – Parallel FLOWs (count matches source shard count), intelligent batching (20K for cluster, 2K for standalone), and node-based cluster routing (100x performance improvement over naive slot-based grouping).

3. **Production-Ready** – LSN-based checkpointing for resume capability, configurable conflict policies, and built-in monitoring dashboard.

### Architecture Documentation

For detailed technical deep-dives, see the architecture documentation:

- **[System Overview](docs/en/architecture/overview.md)** – High-level architecture, design principles, and core innovations
- **[Replication Protocol](docs/en/architecture/replication-protocol.md)** – 5-phase protocol breakdown (handshake, FLOW registration, full sync, barrier, stable sync)
- **[Multi-FLOW Architecture](docs/en/architecture/multi-flow.md)** – Parallel FLOW design, global synchronization barrier, and concurrency control
- **[Cluster Routing Optimization](docs/en/architecture/cluster-routing.md)** – Node-based vs slot-based grouping (666x performance improvement)
- **[Data Pipeline & Backpressure](docs/en/architecture/data-pipeline.md)** – Buffering, batch accumulation, and flow control mechanisms

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
| `df2redis replicate --config <file> [--dashboard-addr :8080] [--task-name foo]` | Run replication (auto-starts dashboard unless addr is empty) |
| `df2redis cold-import --config <file> [--rdb path]` | One-off RDB restore via redis-shake (no incremental sync) |
| `df2redis check --config <file> [flags]` | Launch data consistency check (wrapper around `redis-full-check`) |
| `df2redis dashboard --config <file> [--addr :8080]` | Start the standalone dashboard service |
| `df2redis migrate/prepare/...` | Legacy redis-shake based pipeline helpers |

The CLI uses a shared logger (`internal/logger`) that truncates `<log.dir>/<task>_<command>.log` on each run. Detailed replication steps are written there, while the console only shows highlights (set `log.consoleEnabled: false` to silence it). Customize level/dir via the `log` block in the config file.

`cold-import` reuses the `migrate.*` configuration block (RDB path + redis-shake binary/args) to perform a one-time load without starting the Dragonfly replicator.

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

### Architecture Documentation

For detailed technical deep-dives, see:

- **[System Overview](docs/en/architecture/overview.md)** – High-level architecture, design principles, and core innovations
- **[Replication Protocol](docs/en/architecture/replication-protocol.md)** – 5-phase protocol breakdown (handshake, FLOW registration, full sync, barrier, stable sync)
- **[Multi-FLOW Architecture](docs/en/architecture/multi-flow.md)** – Parallel FLOW design, global synchronization barrier, and concurrency control
- **[Cluster Routing Optimization](docs/en/architecture/cluster-routing.md)** – Node-based vs slot-based grouping (666x performance improvement)
- **[Data Pipeline & Backpressure](docs/en/architecture/data-pipeline.md)** – Buffering, batch accumulation, and flow control mechanisms

### Research Notes

Technical research notes documenting Dragonfly protocol analysis and implementation challenges:

- **[Dragonfly Replication Protocol](docs/en/research/dragonfly-replica-protocol.md)** – Complete analysis of Dragonfly's replica replication protocol, state machine, and multi-FLOW handshake mechanism
- **[Stream RDB Format Analysis](docs/en/research/dragonfly-stream-rdb-format.md)** – Detailed breakdown of Stream RDB serialization format across V1/V2/V3 versions and PEL encoding
- **[Stream Sync Mechanism](docs/en/research/dragonfly-stream-sync.md)** – How Dragonfly ensures Stream replication consistency through journal rewriting and precise ID tracking
- **[Full Sync Performance](docs/en/research/dragonfly-fullsync-performance.md)** – Analysis of Dragonfly's high-performance full sync architecture and optimization recommendations for Redis writes

### Other Documentation

- [Chinese technical docs](docs/zh/) – deep dives for each replication phase, environment setup guides, etc.
- [Test scripts guide](scripts/README.md) – comprehensive testing documentation.
- [Dashboard API reference](docs/api/dashboard-api.md) – JSON endpoints consumed by the upcoming React UI.
- [Dashboard Design](docs/en/dashboard.md) – Material UI + Chart.js layout plan and implementation roadmap.

---

## 🤝 Contributing

1. Fork the repo and create a feature branch.
2. Run `go fmt ./...` and `go test ./...` (when applicable) before opening a PR.
3. Follow the existing logging style (emoji markers + concise explanations).
4. For protocol questions, read the Dragonfly sources under `dragonfly/src/server` or ping the maintainers.

MIT licensed. PRs and issues are always welcome!
