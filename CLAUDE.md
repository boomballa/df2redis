# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 工作偏好与规范

### 协作流程（重要！）

**IMPORTANT**: 在开始实现每个新 Phase 之前，必须先向用户确认关键的源码细节问题，避免浪费 token 做无效尝试。

**标准流程：**
1. **确定实现目标** - 明确本 Phase 要实现的功能
2. **提出关键问题** - 列出需要确认的 Dragonfly 协议细节、数据格式、编码规则等
3. **等待用户确认** - 用户会提供答案或指引查看源码位置
4. **开始编码实现** - 基于确认的信息进行准确实现
5. **测试验证** - 与真实 Dragonfly 实例测试
6. **文档总结** - 完成后创建 Phase 文档

**示例问题：**
- "DFLY FLOW 返回后，数据传输方式是什么？"
- "Packed Uint 的编码格式是什么？"
- "Journal Entry 的完整结构是什么？"
- "是否需要发送 DFLY STARTSTABLE 命令？"

**备选方案：**
如果用户也不确定，再去 `dragonfly/` 目录下查看源码（`src/server/dflycmd.cc`、`src/server/journal/`等）。

**重要原则：**
- **禁止盲目尝试和猜测**：遇到 Dragonfly 协议细节、数据格式、源码实现等不确定的问题时，**必须先向用户确认**，不要浪费 token 做无效尝试
- **主动寻求帮助**：用户可以帮忙查询 Dragonfly 源码、验证协议细节、确认数据格式等
- **高效协作**：通过提前确认关键细节，避免反复试错，提高开发效率

### 沟通语言
- **IMPORTANT**: 必须使用中文回答用户的所有问题和进行日常交流
- **代码注释必须使用英文**（Code comments MUST be in English）
- **日志消息必须使用英文**（Log messages MUST be in English）
- **错误信息必须使用英文**（Error messages MUST be in English）

### Git 提交规范
当完成重要的里程碑功能时，需要提供符合规范的英文 Git 提交信息。遵循 Conventional Commits 规范：

```
<type>(<scope>): <subject>

<body>

<footer>
```

**重要：**
- **不要在提交信息中添加 AI 生成标识**（如 "Generated with Claude Code"、"Co-Authored-By: Claude" 等）
- 保持提交信息简洁专业，只包含技术内容

**Type 类型：**
- `feat`: 新功能
- `fix`: 修复 bug
- `refactor`: 重构（既不是新增功能，也不是修复 bug）
- `docs`: 文档更新
- `style`: 代码格式调整（不影响代码运行）
- `perf`: 性能优化
- `test`: 添加或修改测试
- `chore`: 构建过程或辅助工具的变动

**示例：**
```bash
feat(pipeline): add incremental sync stage for Dragonfly journal streaming

Implement DFLY FLOW protocol and journal parser to support real-time
data synchronization from Dragonfly to Redis/Redis Cluster.

- Add journal stream reader with packed uint decoder
- Implement LSN checkpointing for resume capability
- Add command replay logic with Redis Cluster routing

Closes #123
```

### 代码质量检查
- 每次完成大量代码编辑后，**必须进行语法自查**
- 检查项目：
  - Go 语法错误（未使用的变量、导入、类型错误等）
  - 逻辑错误和边界情况
  - 潜在的空指针引用
  - 资源泄漏（未关闭的连接、文件等）
  - 并发安全问题（如果涉及 goroutine）
- 自查方式：
  - 运行 `go build` 确保编译通过
  - 检查是否有 `go vet` 警告
  - 审查代码逻辑完整性

### 终端输出规范
项目的终端输出必须具有良好的可读性和用户体验：

**要求：**
- 使用清晰的分隔符和格式化输出
- 适当添加 emoji 和符号增强可读性
- 使用不同级别的日志输出（INFO/WARN/ERROR）
- 关键步骤使用醒目的标记
- **成功/失败使用 ✓ 和 ✗ 符号（简洁专业）**

**推荐的符号和 emoji 使用：**
- ✓ 成功、通过、完成
- ✗ 失败、错误
- ⚠ 警告
- → 进行中、箭头指向
- ▸ 子项、详情
- • 列表项
- 🚀 启动或开始
- 📊 统计或指标
- 🔧 配置相关
- 🔍 检查或验证
- 📦 数据或导入
- 🔄 同步或处理中
- ⏱ 时间或耗时
- 🎯 目标或完成度
- 💾 存储或持久化

**输出格式示例：**
```
🚀 df2redis 迁移工具启动
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

📋 配置信息：
  • 源库: dragonfly@<SOURCE_HOST>:<SOURCE_PORT>
  • 目标: redis-cluster@<TARGET_HOST>:<TARGET_PORT>
  • 状态目录: ../out

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

🔍 [1/5] 执行预检查...
  ✓ RDB 文件存在
  ✓ redis-shake 可执行
  ✓ 源库连接正常
  ✓ 目标库连接正常

📦 [2/5] 开始全量导入...
  → 正在导入数据...
  ⏱ 导入耗时: 45.32s
  ✓ 导入完成

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🎯 迁移完成！总耗时: 1m 23s
```

**错误输出示例：**
```
🔍 [1/5] 执行预检查...
  ✓ RDB 文件存在
  ✓ redis-shake 可执行
  ✗ 源库连接失败: connection refused

⚠ 预检查未通过，请检查配置后重试
```

## Project Overview

**df2redis** is a Go-based migration tool for Dragonfly → Redis. It implements a direct replication protocol compatible with Dragonfly to enable full and incremental data synchronization without requiring external proxies or dual-write mechanisms.

**Current Status**: Prototype phase with CLI framework, configuration parsing, full-data import (via redis-shake), state management, and dashboard visualization. Incremental sync via Dragonfly journal streaming is planned but not yet implemented.

## Build Commands

```bash
# Build for Linux amd64
GOOS=linux GOARCH=amd64 go build -o bin/df2redis ./cmd/df2redis

# Build for macOS arm64 (with local cache)
GOCACHE=$PWD/.gocache GOOS=darwin GOARCH=arm64 go build -o bin/df2redis-mac ./cmd/df2redis

# View help
./bin/df2redis --help
```

## Running the Tool

```bash
# Validate configuration without execution
./bin/df2redis migrate --config examples/migrate.sample.yaml --dry-run

# Execute migration
./bin/df2redis migrate --config examples/migrate.sample.yaml

# Start migration with web dashboard on port 8080
./bin/df2redis migrate --config examples/migrate.sample.yaml --show 8080

# Check current status
./bin/df2redis status --config examples/migrate.sample.yaml

# Mark rollback state
./bin/df2redis rollback --config examples/migrate.sample.yaml

# Launch standalone dashboard
./bin/df2redis dashboard --config examples/migrate.sample.yaml
```

## Architecture

### Pipeline-Based Execution Model

The tool uses a stage-based pipeline architecture defined in `internal/pipeline/`. Each stage is executed sequentially, and failure at any stage halts the pipeline:

1. **precheck** (`stages.go:14-54`): Validates file existence, redis-shake binary availability, and database connectivity
2. **shake-config** (`stages.go:56-144`): Auto-generates redis-shake TOML config if not provided
3. **bgsave** (`stages.go:146-183`): Optionally triggers BGSAVE on source and waits for completion
4. **import** (`stages.go:185-207`): Invokes redis-shake to perform RDB import
5. **incremental-sync** (`stages.go:209-217`): Placeholder for future Dragonfly journal streaming (currently skipped)

### Key Components

**Context Management** (`pipeline.go:38-105`):
- `Context` carries shared state across all pipeline stages
- Holds Redis clients for both source and target
- Manages state persistence and metrics
- `NewContext()` establishes connections with 5-second timeout
- `Close()` ensures proper cleanup of Redis connections

**State Persistence** (`internal/state/state.go`):
- Maintains JSON snapshot at `state/status.json`
- Tracks per-stage status (running/success/skipped/failed)
- Records metrics (e.g., import duration) and event timeline
- Thread-safe with mutex protection
- Atomic writes via .tmp + rename pattern

**Redis Client** (`internal/redisx/client.go`):
- Lightweight RESP protocol implementation (no external deps)
- Supports Simple String, Error, Integer, Bulk String, and Array types
- Provides AUTH, PING, INFO, EVAL, EVALSHA commands
- Helper functions: `ToString()`, `ToInt64()`, `ToStringSlice()`, `IsMovedError()`

### Configuration Architecture

**YAML Parser** (`internal/config/parser.go`):
- Custom lightweight parser supporting 2-space indentation only
- Handles scalars, mappings, sequences, and comments
- Converts to JSON intermediate format for unmarshaling

**Config Validation** (`internal/config/config.go:272`):
- `Validate()` checks required fields (source.addr, target.seed, migrate.snapshotPath)
- `ResolvePath()` converts relative paths to absolute
- `ResolvedMigrateConfig()` fills defaults: state="state", statusFile="state/status.json", bgsaveTimeout=300s

### redis-shake Integration

**Executor** (`internal/executor/shake/importer.go`):
- Wraps redis-shake v4 subprocess execution
- `buildArgs()` constructs command-line arguments
- Supports either `-conf <file>` mode or direct file path
- Inherits stdout/stderr for real-time logging
- `Run()` uses `os/exec.CommandContext` for cancellation support

**Auto-Config Generation** (`stages.go:56-144`):
- If neither `shakeConfigFile` nor `shakeArgs` provided, generates TOML config
- Detects Redis Cluster vs standalone via `target.type` field
- Writes to `<stateDir>/shake.generated.toml`
- Injects source RDB path, target address/password, and logging paths

## Configuration Guide

See `examples/migrate.sample.yaml` for reference. Key fields:

### Source Configuration
```yaml
source:
  type: dragonfly          # Identifier (informational)
  addr: <host>:<port>      # Source address (e.g., localhost:6379)
  password: ""             # Optional password
  tls: false
```

### Target Configuration
```yaml
target:
  type: redis-cluster      # "redis-standalone" or "redis-cluster"
  seed: <host>:<port>      # Seed node address (e.g., localhost:6379)
  password: ""             # Optional password
  tls: false
```

### Migration Settings
```yaml
migrate:
  snapshotPath: ../data/backup/dragonfly7380-dump.rdb  # RDB file path
  shakeBinary: ../redis-shake-v4/redis-shake            # redis-shake executable
  shakeArgs: ""              # Optional: full CLI args (mutually exclusive with shakeConfigFile)
  shakeConfigFile: ""        # Optional: path to TOML config (mutually exclusive with shakeArgs)
  autoBgsave: false          # Auto-trigger BGSAVE on source
  bgsaveTimeoutSeconds: 300  # BGSAVE timeout
```

### State Management
```yaml
stateDir: ../out                  # State file output directory
statusFile: ../out/status.json    # Explicit status file path
```

## Code Organization

```
internal/
├── cli/                 # Command dispatch and main flow control
│   └── cli.go          # Parses subcommands, loads config, orchestrates pipeline
├── config/             # Configuration management
│   ├── config.go       # Config structs, validation, defaults
│   └── parser.go       # Lightweight YAML parser
├── pipeline/           # Stage-based orchestration
│   ├── pipeline.go     # Pipeline executor and context management
│   └── stages.go       # Concrete stage implementations
├── executor/shake/     # redis-shake wrapper
│   └── importer.go     # Subprocess invocation and arg building
├── state/              # State persistence
│   └── state.go        # JSON snapshot storage with mutex protection
├── redisx/             # Redis client
│   └── client.go       # RESP protocol implementation
├── consistency/        # Consistency validation (basic skeleton)
│   └── checker.go      # Key-value comparison utilities
└── web/                # Web dashboard
    ├── server.go       # HTTP server with /api/status endpoint
    ├── templates/      # HTML templates (layout.html, index.html)
    └── static/         # Bootstrap CSS + Chart.js
```

## Development Patterns

### Adding a New Pipeline Stage

1. Implement the `Stage` interface in `internal/pipeline/stages.go`:
```go
type Stage interface {
    Name() string
    Run(ctx *Context) Result
}
```

2. Use `StageFunc` helper for inline implementation:
```go
func NewMyStage() Stage {
    return StageFunc{
        name: "my-stage",
        run: func(ctx *Context) Result {
            // Implementation
            return Result{Status: StatusSuccess, Message: "done"}
        },
    }
}
```

3. Register in CLI (`internal/cli/cli.go`) by adding to the pipeline:
```go
pipeline.Add(NewMyStage())
```

### Accessing Redis Clients

Both source and target Redis clients are available in `Context`:
```go
// Ping source
if err := ctx.SourceRedis.Ping(); err != nil {
    return Result{Status: StatusFailed, Message: fmt.Sprintf("源库不可用: %v", err)}
}

// Execute command on target
resp, err := ctx.TargetRedis.Do("SET", "key", "value")
```

### Recording Metrics and Events

Use `State.RecordMetric()` and event logging:
```go
if ctx.State != nil {
    _ = ctx.State.RecordMetric("import.duration.seconds", duration.Seconds())
}
```

State updates happen automatically via `Pipeline.Run()` for each stage status change.

## Dependencies

- **Go 1.21+** (standard library only, zero external Go dependencies)
- **redis-shake v4** (external binary, runtime dependency)

## Future Roadmap

The following features are planned but not yet implemented:

1. **Dragonfly Replication Handshake**: `REPLCONF` negotiation, `DFLY FLOW` registration for per-shard channels
2. **Journal Stream Parsing**: Decode packed uint format, parse Op/LSN/DbId/TxId/args from Dragonfly journal
3. **Incremental Sync**: `DFLY STARTSTABLE` command, deterministic command replay to Redis/Redis Cluster
4. **LSN Checkpointing**: Resume from last consumed LSN after disconnect, fallback to full sync if needed
5. **Redis Cluster Routing**: Handle MOVED/ASK errors, track topology changes, per-slot command routing
6. **Consistency Validation**: Sampling-based key-value comparison between source and target

## Code Style Notes

- **Code comments MUST be in English** (IMPORTANT: all code comments, including struct field comments, function comments, and inline comments must be written in English)
- Log messages use Chinese format (project convention)
- Error messages use Chinese format: `fmt.Errorf("连接源库失败: %w", err)`
- Stage names use kebab-case: "precheck", "shake-config", "incremental-sync"
- Configuration fields use camelCase in YAML: `autoBgsave`, `bgsaveTimeoutSeconds`

---

## 内部开发参考 (Internal Development Reference)

**重要提醒：本部分包含实际测试环境的配置示例，仅供内部开发和测试使用。不要将具体的 IP 地址、端口、密码等敏感信息提交到公开文档（README、示例配置等）。**

### 文档敏感信息策略

1. **公开文档** (README.md, examples/*.yaml, scripts/README.md 等)
   - 只使用占位符：`<host>:<port>`, `<password>`, `localhost:6379`
   - 不包含任何真实的 IP 地址或密码
   - 使用通用示例：`192.0.2.1`, `203.0.113.1` (RFC 5737 保留地址)

2. **内部文档** (CLAUDE.md 本文件)
   - 可以包含实际测试环境配置
   - 仅用于开发和调试参考
   - 不应复制到公开文档

### 测试环境配置示例

#### 开发测试环境

```yaml
# Dragonfly Source (测试环境)
source:
  type: dragonfly
  addr: 192.168.1.100:6380
  password: ""
  tls: false

# Redis Target (测试环境)
target:
  type: redis-cluster
  seed: 192.168.2.200:6379
  password: "your_test_password"
  tls: false

# State & Checkpoint
stateDir: ./out
checkpoint:
  enabled: true
  intervalSeconds: 10
  path: "./out/checkpoint"

# Logging
log:
  dir: "logs"
  level: "info"
  consoleEnabled: true
```

#### Python 测试脚本配置

```python
# scripts/test_stream_replication.py
SOURCE_HOST = "192.168.1.100"
SOURCE_PORT = 6380
SOURCE_PASSWORD = ""

TARGET_HOST = "192.168.2.200"
TARGET_PORT = 6379
TARGET_PASSWORD = "your_test_password"
```

#### Bash 测试脚本配置

```bash
# scripts/manual_test_all_types.sh
SOURCE_HOST="192.168.1.100"
SOURCE_PORT="6380"
TARGET_HOST="192.168.2.200"
TARGET_PORT="6379"
TARGET_PASS="your_test_password"
```

### 常用测试命令

```bash
# 启动复制（使用内部测试配置）
./bin/df2redis replicate --config out/replicate.yaml

# 数据一致性检查
./bin/df2redis check --config out/replicate.yaml --mode outline

# 运行 Stream 类型测试
python3 scripts/test_stream_replication.py

# 运行全类型测试
bash scripts/manual_test_all_types.sh
```

### 环境变量敏感信息处理

对于需要在代码中使用敏感信息的场景，推荐使用环境变量：

```bash
# .env (不要提交到 git)
DRAGONFLY_HOST=192.168.1.100
DRAGONFLY_PORT=6380
DRAGONFLY_PASSWORD=

REDIS_HOST=192.168.2.200
REDIS_PORT=6379
REDIS_PASSWORD=your_test_password
```

```yaml
# 配置文件中引用环境变量
source:
  addr: ${DRAGONFLY_HOST}:${DRAGONFLY_PORT}
  password: ${DRAGONFLY_PASSWORD}

target:
  seed: ${REDIS_HOST}:${REDIS_PORT}
  password: ${REDIS_PASSWORD}
```
