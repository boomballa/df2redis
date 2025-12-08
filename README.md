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

df2redis implements Dragonfly's replication protocol end-to-end so that Redis or Redis Cluster can act as a downstream target. The tool performs a full snapshot import followed by continuous journal replay, giving you a near-zero-downtime migration path without relying on double-write proxies or fragile scripts.

Key ideas:

- **Native protocol support** – handshake with Dragonfly via `DFLY FLOW`, detect FLOW topology, and request RDB + journal streams.
- **Parallel snapshot ingest** – multiple FLOW connections stream data concurrently, decoding Dragonfly-specific encodings (type-18 listpacks, QuickList 2.0, etc.).
- **Incremental catch-up** – Journal entries are parsed and routed to the correct Redis Cluster node, honoring transaction IDs, TTLs, and skip policies.
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

```
Dragonfly (source)
   ├─ Main connection (handshake, STARTSTABLE)
   ├─ FLOW-0 ... FLOW-n (RDB + journal)
   └─ Checkpoints persisted locally

Redis / Redis Cluster (target)
   ├─ Cluster client with slot routing
   └─ Conflict policy + TTL restoration
```

Key modules:

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
| `df2redis check --config <file> [flags]` | Launch data consistency check (wrapper around `redis-full-check`) |
| `df2redis dashboard --config <file> [--addr :8080]` | Start the standalone dashboard service |
| `df2redis migrate/prepare/...` | Legacy redis-shake based pipeline helpers |

The CLI shares logging helpers (`internal/logger`) that output both to the console and to rotating files. Use the `log` block in the config file to customize directories and levels.

The embedded dashboard listens on `config.dashboard.addr` (default `:8080`). Override it in the YAML or pass `--dashboard-addr` to `replicate`/`--addr` to `dashboard`.

---

## 📚 Documentation

- [Chinese technical docs](docs/) – deep dives for each replication phase, environment setup guides, etc.
- [English README (this file)](README.md) – concise overview.
- [Dashboard API reference](docs/api/dashboard-api.md) – JSON endpoints consumed by the upcoming React UI.
- [前端设计草案（ZH）](docs/zh/dashboard.md) – Material UI + Chart.js layout plan (English version WIP).

---

## 🤝 Contributing

1. Fork the repo and create a feature branch.
2. Run `go fmt ./...` and `go test ./...` (when applicable) before opening a PR.
3. Follow the existing logging style (emoji markers + concise explanations).
4. For protocol questions, read the Dragonfly sources under `dragonfly/src/server` or ping the maintainers.

MIT licensed. PRs and issues are always welcome!
