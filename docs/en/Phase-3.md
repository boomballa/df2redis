# Phase 3 – Journal Stream Parsing & Replay

[中文文档](../Phase-3.md)

Phase 3 covers the incremental replication path: decoding Dragonfly journal entries and replaying them against Redis/Redis Cluster.

## Highlights

- Journal reader state machine (opcodes: SELECT, LSN, COMMAND, EXPIRED, PING, FIN).
- FLOW-aware fan-in channel that keeps per-flow stats and prints human-readable progress.
- Replay rules: skip SELECT/PING, convert EXPIRED records into `PEXPIRE`, route regular commands through the Cluster client with key conflict policies.
- Automatic checkpoint persistence triggered by timers as well as on graceful shutdown.

## Checklist

1. Run one reader goroutine per FLOW to transform journal bytes into `JournalEntry` structs.
2. Use a central dispatcher that handles errors, prints entries, and replays commands.
3. Track FLOW LSNs so checkpoints can resume exactly where Dragonfly expects.
4. Save a final checkpoint when the journal stream completes or when the process receives a termination signal.

Consult the Chinese write-up for byte diagrams, troubleshooting logs, and benchmark data.
