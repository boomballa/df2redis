# df2redis Dashboard API

本文档描述 `df2redis dashboard` 服务当前暴露的 JSON API，供 React/MUI 前端或其他集成调用。所有接口都在 Dashboard HTTP 服务根路径下（默认 `http://127.0.0.1:8080`）。

## 认证

目前接口仅面向本地/受信任环境开放，没有额外认证机制。生产部署时请使用防火墙或反向代理限制访问。

## 响应格式

- Content-Type: `application/json`
- 时间字段使用 RFC3339 格式（UTC）。
- 数值字段以 `float64` 表达，若无数据则为 0。

## 1. `GET /api/sync/summary`

返回同步总体信息。

```json
{
  "stage": "full_sync",
  "stageMessage": "FLOW 建立完成",
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

同步进度数据，包括键数量、FLOW 统计与增量追平情况。

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

### 数据来源

- `source.keys.estimated`
- `target.keys.initial`
- `target.keys.current`
- `sync.keys.applied`
- `flow.<id>.imported_keys`
- `sync.incremental.lsn.current`
- `sync.incremental.lsn.applied`
- `sync.incremental.lag.ms`

这些值通过 `state.Store.RecordMetric` 写入；若未提供，接口会以 0 返回。

## 3. `GET /api/check/latest`

数据校验结果（来自 `state.Store.SaveCheckResult` 保存的最新记录）。

```json
{
  "status": "inconsistent",
  "mode": "outline",
  "message": "检测到 5 个 key 不一致",
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

若尚未运行校验，返回 `{ "status": "not_run" }`。

## 4. `GET /api/events`

返回最近写入的事件（来自 `state.Store.SetPipelineStatus` 等记录的 `Snapshot.Events`）。

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

## 5. 向后兼容的 `/api/status`

仍保留原始的 `state.Snapshot` 序列化结果，便于旧脚本继续使用。

---

## 扩展计划

- 提供 `POST /api/check/run` 用于触发一次 redis-full-check。
- 提供 WebSocket/SSE 推送通道，实现实时刷新。
- 接入鉴权（Token / mTLS）以满足生产环境需求。
