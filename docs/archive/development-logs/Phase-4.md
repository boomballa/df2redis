# Phase 4 – LSN Persistence & Checkpointing

[中文文档](../Phase-4.md)

Phase 4 introduces durable checkpoints so df2redis can resume incremental sync without replaying the entire snapshot.

## Highlights

- Definition of the checkpoint schema (`replication_id`, `session_id`, per-FLOW LSN map, versioning, timestamps).
- Atomic file writes (tmp file + rename) plus configurable save intervals.
- Integration with `Replicator.saveCheckpoint`, `tryAutoSaveCheckpoint`, and graceful shutdown flow.
- Operational guidance (SIGTERM handling, avoiding SIGKILL, verifying checkpoint files).

## Checklist

1. Initialize the checkpoint manager with a resolved path (`stateDir/checkpoint.json` by default).
2. Save checkpoints on the configured interval and when the process stops.
3. Ensure each FLOW goroutine updates `ReplayStats.FlowLSNs` so the checkpoint contains accurate offsets.
4. Document failure modes (missing files, path issues, process termination) and remedies.

See the Chinese document for full troubleshooting logs and performance measurements.
