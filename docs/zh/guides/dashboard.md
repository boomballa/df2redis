# df2redis Dashboard 前端方案（草案）

[English Version](../../en/guides/dashboard.md) | [接口文档](../../api/dashboard-api.md)

## 目标

- 展示同步阶段（全量/增量）、进度百分比、FLOW 统计、LSN 延迟。
- 可视化数据校验结果，显示不一致样本。
- 为后续 React + MUI + Chart.js 前端打下基础，现阶段先明确 API 与页面布局。

## 技术栈选择

| 方案 | 说明 | 适用阶段 |
| --- | --- | --- |
| Go `html/template` + AdminLTE + Chart.js | 参考 RDR，可快速上线 | MVP / 简易部署 |
| **React + MUI + Chart.js（推荐）** | Google 风格、组件丰富、利于扩展 | 最终版本 |

建议直接建设 React 版本：

- **框架**：Vite + React + TypeScript。
- **UI**：Material UI（MUI）+ 自定义主题（Primary/Secondary 采用鲜艳色）。
- **图表**：`react-chartjs-2`（后续可替换为 `@mui/x-charts`）。
- **状态管理**：React Query/ SWR 轮询 `/api/*` 接口，未来可加入 WebSocket。

## 页面布局草图

1. **头部概览卡片**
   - 当前阶段（Full Sync / Incremental / Completed）。
   - 源端/目标端信息 + Checkpoint 最近保存时间。

2. **同步进度区**
   - 环形/线性进度条：`syncedKeys / sourceKeysEstimated`。
   - 集群键数统计（初始 / 当前）。
   - FLOW 列表：每个 FLOW 的状态、导入键数、更新时间。

3. **增量追平 & LSN**
   - 折线图显示 `LSNCurrent` vs `LSNApplied`。
   - 当前延迟（LSN 差值 / 毫秒）。

4. **数据校验面板**
   - 最近一次状态（一致/不一致/未运行）。
   - 样本表（key、源值、目标值）。
   - 按钮：触发新一轮校验（后端完成 API 后对接）。

5. **事件时间线 / 日志**
   - 展示 `/api/events` 返回的节点重连、checkpoint 等事件。

## 数据来源

- `/api/sync/summary`
- `/api/sync/progress`
- `/api/check/latest`
- `/api/events`

详见接口文档。

## 开发计划

1. **后端 API**：已在 `internal/web/server.go` 暴露 JSON 结构，未来 replicator/校验流程调用 `state.Store.RecordMetric / SaveCheckResult` 即可填充数据。
2. **前端工程**：
   - 在 `web/dashboard`（或 `ui/`）目录初始化 Vite + React 项目。
   - 定义 `SummaryCard`, `ProgressPanel`, `FlowList`, `CheckPanel`, `EventsTimeline` 等组件。
   - 使用 React Query 轮询 API（默认 3~5s）。
3. **集成**：将构建产物放入 `internal/web/static/dashboard`，由 `dashboard` 命令托管；或单独部署并通过 `--api-url` 访问后端。
4. **后续**：
   - 接入 WebSocket/SSE，提供实时推送。
   - 对接 `redis-full-check` 触发器，支持“校验历史”查看。
   - 增加鉴权与多租户配置。

## Todo

- React 项目脚手架。
- 自定义 Material 主题（鲜艳/Google 风）。
- 中英文文案、i18n。
