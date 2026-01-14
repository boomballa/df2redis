# Phase 1: Dragonfly 复制握手流程

[English Version](en/Phase-1.md) | [中文版](Phase-1.md)

## 概述

Phase 1 实现了与 Dragonfly 主库建立复制连接的完整握手流程，这是实现 Dragonfly → Redis 数据迁移的第一步，也是最关键的基础。

## 实现目标

- ✓ 与 Dragonfly 主库建立 TCP 连接
- ✓ 执行完整的 6 步握手协议
- ✓ 解析 Dragonfly 服务器信息（版本、Shard 数量等）
- ✓ 为每个 Shard 建立独立的 FLOW 通道
- ✓ 验证握手成功并保持连接

## 核心组件

### 1. 类型定义 (`internal/replica/types.go`)

定义了复制流程中的核心数据结构：

```go
// DflyVersion - Dragonfly 协议版本
type DflyVersion int

// ReplicaState - 复制状态（未连接、连接中、握手中、准备阶段、全量同步、增量同步、已停止）
type ReplicaState int

// MasterInfo - 主库信息
type MasterInfo struct {
    Version  DflyVersion // 协议版本（VER1-VER4）
    NumFlows int         // Shard 数量
    ReplID   string      // 复制 ID (master_id)
    SyncID   string      // 同步会话 ID (如 "SYNC12")
    Offset   int64       // 复制偏移量
}

// FlowInfo - 单个 Flow 的信息
type FlowInfo struct {
    FlowID int    // Flow ID（对应 shard ID）
    State  string // Flow 状态
}
```

### 2. 复制器实现 (`internal/replica/replicator.go`)

实现了完整的握手逻辑，包括：

**核心方法：**
- `NewReplicator()` - 创建复制器实例
- `Start()` - 启动复制流程
- `connect()` - 建立 TCP 连接
- `handshake()` - 执行 6 步握手
- `Stop()` - 停止复制

**握手步骤：**

1. **PING** - 验证连通性
   ```
   → PING
   ← PONG
   ```

2. **REPLCONF listening-port** - 声明监听端口
   ```
   → REPLCONF listening-port 16379
   ← OK
   ```

3. **REPLCONF ip-address** - 声明 IP 地址（可选）
   ```
   → REPLCONF ip-address <ip>
   ← OK
   ```

4. **REPLCONF capa eof psync2** - 声明 EOF 和 PSYNC2 能力
   ```
   → REPLCONF capa eof capa psync2
   ← OK
   ```

5. **REPLCONF capa dragonfly** - 声明 Dragonfly 兼容性并获取服务器信息
   ```
   → REPLCONF capa dragonfly
   ← ["16c2763d...", "SYNC12", 8, 4]
      [master_id, sync_id, flow_count, version]
   ```

6. **DFLY FLOW** - 为每个 Shard 建立 FLOW 通道
   ```
   → DFLY FLOW <master_id> <sync_id> <flow_id>
   ← ["FULL", <session_id>]
   ```

### 3. CLI 命令 (`internal/cli/cli.go`)

新增 `replicate` 子命令用于测试握手流程：

```bash
./bin/df2redis replicate --config examples/replicate.sample.yaml
```

## 协议分析

### REPLCONF capa dragonfly 响应格式

Dragonfly 返回一个包含 4 个元素的数组：

```
[
  "16c2763d0e4cb8f214ded18e6d4e178b00775674",  // [0] master_id (复制 ID)
  "SYNC12",                                     // [1] sync_id (同步会话 ID)
  8,                                            // [2] flow_count (Shard/Flow 数量)
  4                                             // [3] version (协议版本)
]
```

**关键发现：**
- 数组第 3 个元素（arr[2]）是 flow_count（Shard 数量）
- 数组第 4 个元素（arr[3]）是协议版本号
- sync_id 每次握手都会变化（如 SYNC11、SYNC12）
- 这与 Redis 的 REPLCONF 响应格式完全不同

### DFLY FLOW 命令格式

正确的命令格式：

```
DFLY FLOW <master_id> <sync_id> <flow_id>
```

示例：
```
DFLY FLOW 16c2763d0e4cb8f214ded18e6d4e178b00775674 SYNC12 0
→ ["FULL", "cc5dd58c..."]
```

**重要参数：**
- `master_id` - 从 REPLCONF capa dragonfly 获取
- `sync_id` - 从 REPLCONF capa dragonfly 获取（每次握手唯一）
- `flow_id` - 0 到 (flow_count-1)

## 实际测试结果

### 测试环境
- Dragonfly 版本：v1.30.0
- Dragonfly 地址：192.168.1.100:16379
- Shard 数量：8
- 协议版本：VER4

### 握手成功输出

```
🚀 启动 Dragonfly 复制器
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔗 连接到 Dragonfly: 192.168.1.100:16379
✓ 连接成功

🤝 开始握手流程
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  [1/6] 发送 PING...
  ✓ PONG 收到
  [2/6] 声明监听端口: 16379...
  ✓ 端口已注册
  [3/6] 跳过 IP 地址声明
  [4/6] 声明能力: eof psync2...
  ✓ 能力已声明
  [5/6] 声明 Dragonfly 兼容性...
  → 复制 ID: 16c2763d...
  → 同步会话: SYNC12
  → Flow 数量: 8
  → 协议版本: VER4
  ✓ Dragonfly 版本: VER4, Shard 数量: 8
  [6/6] 建立 8 个 FLOW...
    • 建立 FLOW-0...
      → 同步类型: FULL
      → 会话 ID: cc5dd58c...
    ✓ FLOW-0 已建立
    • 建立 FLOW-1...
      → 同步类型: FULL
      → 会话 ID: 17aa2014...
    ✓ FLOW-1 已建立
    ... (FLOW-2 到 FLOW-7 全部成功)
  ✓ 所有 FLOW 已建立
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
✓ 握手完成

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🎯 复制器启动成功！
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

## 技术难点与解决方案

### 难点 1: REPLCONF capa dragonfly 响应解析

**问题：**
最初假设响应格式为 `[OK, VERx, num_flows]`，但实际响应是 `[master_id, sync_id, flow_count, version]`

**解决：**
- 通过 INFO server 命令分析实际响应
- 调整解析逻辑以匹配真实格式
- 正确提取并存储 master_id 和 sync_id

### 难点 2: DFLY FLOW 命令参数

**问题：**
多次尝试不同的参数组合都失败：
- `DFLY FLOW 0` → syntax error
- `DFLY FLOW 0 <repl_id>` → syntax error
- `DFLY FLOW 0 SYNC8` → syntax error
- `DFLY FLOW <repl_id> 8 0` → bad sync id

**解决：**
- 查阅 Dragonfly 源码 `dragonfly/src/server/dflycmd.cc`
- 发现正确格式：`DFLY FLOW <master_id> <sync_id> <flow_id>`
- 手动测试验证：`DFLY FLOW 16c2763d... SYNC11 0` → 成功返回 `["FULL", <session_id>]`

### 难点 3: 多 Shard 架构处理

**问题：**
Dragonfly 使用多 Shard 架构，每个 Shard 需要独立的 FLOW 通道

**解决：**
- 循环为每个 Shard（0 到 flow_count-1）建立 FLOW
- 每个 FLOW 返回独立的 session_id
- 为后续 Phase 2 的并行数据接收做准备

## 配置文件

`examples/replicate.sample.yaml`:

```yaml
source:
  type: dragonfly
  addr: 192.168.1.100:16379
  password: ""
  tls: false

target:
  type: redis-standalone
  addr: 192.168.2.200:6379
  password: "your_redis_password"
  tls: false

stateDir: ./out
statusFile: ./out/status.json

# 以下为占位符（仅为通过配置校验）
migrate:
  snapshotPath: /tmp/placeholder.rdb
  shakeBinary: /tmp/placeholder
```

## 状态管理

复制状态流转：

```
StateDisconnected (未连接)
    ↓
StateConnecting (连接中)
    ↓
StateHandshaking (握手中)
    ↓
StatePreparation (准备阶段) ← Phase 1 完成后的状态
    ↓
StateFullSync (全量同步) ← Phase 2 将实现
    ↓
StateStableSync (增量同步) ← Phase 3 将实现
```

## 后续 Phase 预览

### Phase 2: 全量同步 - Journal Stream 接收
- 接收每个 FLOW 的 Journal 流
- 解析 Packed Uint 编码
- 解析 Journal Entry 格式
- 显示解析后的命令到控制台

### Phase 3: 增量同步 - 命令重放到 Redis
- 将解析的命令写入目标 Redis
- 处理 Redis Cluster 路由（MOVED/ASK）
- LSN Checkpoint 保存与恢复
- 断线重连与增量续传

## 提交信息

```
feat(replica): implement Dragonfly replication handshake

- Add replica types (DflyVersion, ReplicaState, MasterInfo, FlowInfo)
- Implement 6-step handshake protocol (PING, REPLCONF, DFLY FLOW)
- Parse REPLCONF capa dragonfly response correctly
- Establish FLOW for each shard with proper parameters
- Add CLI replicate command for testing
- Create sample config for replication testing

Phase 1 完成：成功与 Dragonfly v1.30.0 建立复制连接并完成握手。
测试环境：8 个 Shard，协议版本 VER4，所有 FLOW 成功建立。
```

## 文件清单

**新增文件：**
- `internal/replica/types.go` - 类型定义
- `internal/replica/replicator.go` - 复制器实现
- `examples/replicate.sample.yaml` - 测试配置
- `docs/Phase-1.md` - 本文档

**修改文件：**
- `internal/cli/cli.go` - 新增 replicate 子命令

## 测试清单

- [x] 连接到 Dragonfly 主库
- [x] PING/PONG 验证
- [x] REPLCONF listening-port
- [x] REPLCONF capa eof psync2
- [x] REPLCONF capa dragonfly 响应解析
- [x] 正确提取 master_id、sync_id、flow_count、version
- [x] 为所有 Shard 建立 FLOW（测试了 8 个 Shard）
- [x] 每个 FLOW 返回 FULL 同步类型和 session_id
- [x] 握手成功后保持连接
- [x] 优雅停止（Ctrl+C）

## 已知限制

1. 当前仅完成握手，未接收数据流
2. 未实现断线重连
3. 未实现 LSN Checkpoint
4. 配置中 target 和 migrate 参数暂未使用（Phase 2/3 将使用）

## 下一步

Phase 2 将实现 Journal Stream 的接收和解析，包括：
- Packed Uint 解码器
- Journal Entry 解析
- 命令提取和显示
