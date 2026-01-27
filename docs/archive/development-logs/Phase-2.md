# Phase 2 – Parallel FLOW Ingest & RDB Snapshot (English Companion)

[中文文档](../Phase-2.md)

Phase 2 explains how df2redis receives the full snapshot from Dragonfly using multiple FLOW sockets in parallel.

## Highlights

- FLOW fan-out design: one goroutine per FLOW connection with shared stats and cancellation.
- QuickList 2.0 / listpack parsing pipeline plus conflict policies before writing into Redis.
- RDB progress logging (per 100 keys) and aggregate metrics printed once all FLOW goroutines complete.
- Enforcement of the correct ordering: receive RDB → send `DFLY STARTSTABLE` → verify EOF tokens after Dragonfly emits metadata blocks.

## Checklist

1. Launch worker goroutines, each creating an `RDBParser` bound to its FLOW connection.
2. Skip expired keys early and emit structured logs for errors.
3. After all FLOWs finish, compute totals, switch to stable sync, and verify EOF token integrity.
4. Surface errors through a buffered channel so the caller can abort if any FLOW fails.

See the Chinese document for diagrams, timing analysis, and corner cases.
