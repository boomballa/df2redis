# Phase 7 – Journal Timeout Fixes & Incremental Sync

[中文文档](../Phase-7.md)

Phase 7 describes stability fixes for journal streaming plus the final touches on incremental sync.

## Highlights

- Added long read deadlines (~24h) for journal readers while relying on TCP keepalive for failure detection.
- Improved goroutine coordination so Stop() cancels contexts, closes connections, and waits for `done` channel.
- Final checkpoint save logic when journal streams finish naturally.

## Checklist

1. Ensure each FLOW reader exits when `ctx.Done()` fires (after Stop or fatal error).
2. Close network connections proactively so blocked reads unblock immediately.
3. Test signal handling (SIGINT/SIGTERM) to confirm graceful shutdown and checkpoint persistence.

Details, logs, and test cases live in the Chinese companion.
