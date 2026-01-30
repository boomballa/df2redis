# df2redis Dashboard API

This document describes the JSON API currently exposed by the `df2redis dashboard` service, for integration with React/MUI frontends or other clients. All endpoints are served under the Dashboard HTTP service root path (default `http://127.0.0.1:8080`).

## Authentication

Currently, the API is designed for local/trusted environments and has no additional authentication mechanism. For production deployments, use firewall rules or reverse proxy to restrict access.

## Response Format

- Content-Type: `application/json`
- Time fields use RFC3339 format (UTC)
- Numeric fields are expressed as `float64`, 0 if no data available

## 1. `GET /api/sync/summary`

Returns overall synchronization information.

```json
{
  "stage": "full_sync",
  "stageMessage": "FLOW established",
  "stageUpdatedAt": "2025-03-21T10:33:11Z",
  "source": { "type": "dragonfly", "addr": "10.0.0.1:7380" },
  "target": { "type": "redis-cluster", "seed": "10.0.2.10:6379", "initialKeys": 1200 },
  "checkpoint": {
    "enabled": true,
    "file": "/opt/df2redis/state/checkpoint.json",
    "lastSavedAt": "2025-03-21T10:42:00Z"
  },
  "configPath": "/opt/df2redis/examples/replicate.yaml",
  "stateDir": "/opt/df2redis/state",
  "stageDetails": [
    {"name": "flow:0", "status": "running", "updatedAt": "2025-03-21T10:32:11Z"}
  ],
  "updatedAt": "2025-03-21T10:42:03Z"
}
```

## 2. `GET /api/sync/progress`

Synchronization progress data, including key counts, FLOW statistics, and incremental catch-up status.

```json
{
  "phase": "full_sync",
  "percent": 0.62,
  "sourceKeysEstimated": 500000,
  "targetKeysInitial": 20000,
  "targetKeysCurrent": 332000,
  "syncedKeys": 312000,
  "flowStats": [
    {"flowId": 0, "state": "rdb", "importedKeys": 78000},
    {"flowId": 1, "state": "rdb", "importedKeys": 82000}
  ],
  "incremental": {
    "lsnCurrent": 105600,
    "lsnApplied": 105550,
    "lagLsns": 50,
    "lagMillis": 200
  },
  "updatedAt": "2025-03-21T10:42:03Z"
}
```

### Data Sources

- `source.keys.estimated`
- `target.keys.initial`
- `target.keys.current`
- `sync.keys.applied`
- `flow.<id>.imported_keys`
- `sync.incremental.lsn.current`
- `sync.incremental.lsn.applied`
- `sync.incremental.lag.ms`

These values are written via `state.Store.RecordMetric`; if not provided, the API returns 0.

## 3. `GET /api/check/latest`

Data validation results (from the latest record saved by `state.Store.SaveCheckResult`).

```json
{
  "status": "inconsistent",
  "mode": "outline",
  "message": "5 keys detected as inconsistent",
  "startedAt": "2025-03-21T11:00:00Z",
  "finishedAt": "2025-03-21T11:03:12Z",
  "durationSeconds": 192,
  "inconsistentKeys": 5,
  "samples": [
    {"key": "user:42", "source": "v1", "target": "v2"}
  ],
  "resultFile": "/opt/df2redis/check-results/task_check_20250321.json",
  "summaryFile": "/opt/df2redis/check-results/task_check_20250321_summary.txt"
}
```

If no check has been run yet, returns `{ "status": "not_run" }`.

## 4. `GET /api/events`

Returns recently written events (from `Snapshot.Events` recorded by `state.Store.SetPipelineStatus` etc.).

```json
{
  "events": [
    {
      "timestamp": "2025-03-21T10:40:11Z",
      "type": "warn",
      "message": "FLOW-2 connection lost, retrying"
    }
  ]
}
```

## 5. Backward-Compatible `/api/status`

Still retains the original `state.Snapshot` serialization result for legacy scripts to continue using.

---

## Extension Plans

- Provide `POST /api/check/run` to trigger a redis-full-check run
- Provide WebSocket/SSE push channel for real-time updates
- Add authentication (Token / mTLS) to meet production environment requirements
