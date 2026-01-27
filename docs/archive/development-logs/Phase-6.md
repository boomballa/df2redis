# Phase 6 – Fixing RDB Read Timeouts

[中文文档](../Phase-6.md)

Phase 6 focuses on eliminating snapshot read timeouts that interrupted FLOW connections.

## Highlights

- Added configurable RDB read deadlines (`Client.rdbTimeout`) separate from journal read loops.
- Enabled TCP keepalive to cooperate with Dragonfly's 30-second liveness detection.
- Documented tuning tips for slow disks or high-latency links.

## Checklist

1. Wrap raw RDB reads with explicit deadlines while still allowing journal reads to block for long periods.
2. Surface timeout errors in logs so operators can distinguish between Dragonfly pauses and network failures.
3. Verify keepalive settings across platforms to avoid silent connection drops.

Refer to the Chinese document for postmortem notes and waveform captures.
