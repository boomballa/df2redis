# Background Execution Design

## Problem Statement

When running long-lived processes over SSH (especially through jump servers), processes are often terminated when the SSH session disconnects due to:

1. **SIGHUP Signal**: When a controlling terminal closes, the shell sends SIGHUP to all child processes
2. **Timeout Policies**: Jump servers often have aggressive session timeouts (e.g., 30 minutes)
3. **Network Instability**: Temporary network issues can disconnect SSH sessions

Traditional solutions require external tools (`nohup`, `screen`, `tmux`) or process supervisors (`systemd`, `supervisor`).

## Design Goals

1. **Zero External Dependencies**: Work out-of-the-box without requiring `nohup` or other tools
2. **User-Friendly**: Simple `&` backgrounding should "just work"
3. **Production-Ready**: Support modern deployment patterns (systemd, Docker)
4. **Fail-Safe**: Continue running even when stdout/stderr become unavailable

## Implementation

### Layer 1: Signal Handling

**Location**: `internal/cli/cli.go:35`

```go
// Ignore SIGHUP to allow running in background without nohup
// When SSH session disconnects, the shell sends SIGHUP to all child processes.
// By ignoring SIGHUP, df2redis can continue running after session disconnect.
// This enables simple background execution: ./df2redis replicate &
signal.Ignore(syscall.SIGHUP)
```

**Rationale**:
- Placed in `Execute()` entry point, affects all subcommands
- Go's default behavior: terminate on SIGHUP
- `signal.Ignore()` is the standard Go approach (vs. catching and handling)
- No overhead, no complexity

**Trade-offs Considered**:
- **Catching vs. Ignoring**: Catching allows cleanup, but df2redis already handles SIGTERM/SIGINT for graceful shutdown. SIGHUP is specifically for terminal disconnect, which should not trigger cleanup.
- **Selective Ignoring**: Could ignore only for long-running commands (replicate), but simpler to apply globally. Short commands (status, check) complete quickly anyway.

### Layer 2: Dual Logging

**Location**: `internal/logger/logger.go`

```go
type Logger struct {
    fileLogger  *log.Logger // file output
    consoleLog  *log.Logger // console highlights
    // ...
}
```

**Features**:
1. **Simultaneous Output**: All logs written to both file and console
2. **Graceful Degradation**: Console write failures don't affect file logging
3. **Structured Storage**: File logs persist across restarts

**Write Failure Handling**:
```go
// Gracefully handle stdout write failures (e.g., when SSH session disconnects)
// If stdout is closed, the write will fail but won't crash the program
// Logs will continue to be written to file
_, _ = fmt.Fprintf(os.Stdout, "%s [df2redis] %s\n", timestamp, message)
```

**Rationale**:
- Explicitly discard write errors (`_, _`) to prevent panic
- File logging continues regardless of console state
- Similar to redis-shake's approach but more explicit

### Layer 3: TTY Auto-Detection

**Location**: `internal/cli/cli.go:797`

```go
// Smart console detection: disable console output if stdout is not a TTY
// This automatically handles background execution (nohup, systemd, Docker, etc.)
consoleEnabled := cfg.Log.ConsoleEnabledValue()
if consoleEnabled {
    // Check if stdout is a terminal
    if fileInfo, _ := os.Stdout.Stat(); (fileInfo.Mode() & os.ModeCharDevice) == 0 {
        // stdout is not a TTY (redirected to file or pipe)
        consoleEnabled = false
        log.Printf("[df2redis] Detected non-TTY stdout, disabling console output")
    }
}
```

**Benefits**:
1. **Performance**: Avoids unnecessary console writes when no terminal exists
2. **Automatic Adaptation**: Works correctly in systemd, Docker, cron, etc.
3. **User Override**: Config setting (`log.consoleEnabled`) still respected

**Detection Logic**:
- `os.Stdout.Stat()` returns file info
- `os.ModeCharDevice` flag indicates character device (terminal)
- If not a character device, stdout is redirected or piped

## Comparison with redis-shake

### redis-shake Approach

**Analysis of Source Code**:
- No SIGHUP handling (`main.go:292-294` only handles SIGINT/SIGTERM/SIGQUIT)
- Dual logging via zerolog MultiLevelWriter (`init.go:42-59`)
- No explicit TTY detection

**Why it "works"**:
- Go's default signal handling sometimes doesn't deliver SIGHUP (environment-dependent)
- Robust file logging means stdout failures are non-fatal
- Users typically use `nohup` anyway in production

### df2redis Approach

**Improvements**:
1. **Explicit SIGHUP Ignoring**: Guaranteed behavior across environments
2. **TTY Auto-Detection**: Optimizes performance, adapts to deployment context
3. **Documented Behavior**: Clear expectations for users

**Design Philosophy**:
- **Defense in Depth**: Multiple layers of protection
- **Fail-Safe Defaults**: Work correctly in worst-case scenarios
- **Observable Behavior**: Log messages explain what's happening

## Usage Patterns

### Interactive Execution

```bash
./bin/df2redis replicate --config config.yaml --task-name my-task
```
- Console output enabled (TTY detected)
- Color formatting, progress indicators
- Ctrl+C for graceful shutdown
- Config file and task name required

### Background Execution (Standard)

```bash
./bin/df2redis replicate --config config.yaml --task-name my-task &
```
- SIGHUP ignored, survives SSH disconnect
- Console + file logging
- Use `tail -f log/my-task_replicate.log` to monitor
- **Important**: Config file `--config` is mandatory
- **Recommended**: Specify `--task-name` for log identification

### Multiple Tasks

```bash
./bin/df2redis replicate --config config/task1.yaml --task-name task1 &
./bin/df2redis replicate --config config/task2.yaml --task-name task2 &
./bin/df2redis check --config config/task1.yaml --task-name check1 &
```

### With screen (Debugging)

```bash
screen -S df2redis-task1
./bin/df2redis replicate --config config.yaml --task-name task1
# Ctrl+A D to detach
# screen -r df2redis-task1 to reattach
```

## Testing Strategy

### Test Cases

1. **Normal Execution**:
   ```bash
   ./bin/df2redis replicate --config config.yaml --task-name test1
   ```
   → Both console and file logs

2. **Background Execution**:
   ```bash
   ./bin/df2redis replicate --config config.yaml --task-name test2 &
   ```
   → Continues after SSH disconnect (1800s timeout)

3. **Redirected Stdout**:
   ```bash
   ./bin/df2redis replicate --config config.yaml --task-name test3 >/dev/null 2>&1 &
   ```
   → No TTY detected, console disabled, file logs still work

4. **Multiple Tasks**:
   ```bash
   ./bin/df2redis replicate --config task1.yaml --task-name task1 &
   ./bin/df2redis replicate --config task2.yaml --task-name task2 &
   ```
   → Each task has separate log file

### Validation Commands

```bash
# Check if SIGHUP is ignored
kill -HUP <pid>  # Process should continue

# Check TTY detection
./bin/df2redis replicate --config config.yaml --task-name test </dev/null
# Should log: "Detected non-TTY stdout, disabling console output"

# Check file logging
tail -f log/test_replicate.log  # Should show all events

# Verify process survives SSH disconnect
# 1. Start: ./bin/df2redis replicate --config config.yaml --task-name survival &
# 2. Note PID
# 3. Disconnect SSH and wait > 1800s
# 4. Reconnect SSH
# 5. Check: ps aux | grep <PID>  # Should still be running
# 6. Check: tail log/survival_replicate.log  # Should have recent entries
```

## Known Limitations

1. **No Daemonization**: Process doesn't fully detach (no double-fork)
   - **Rationale**: Modern systems use systemd/Docker for daemonization
   - **Workaround**: Use `nohup`, `screen`, or `systemd`

2. **Stdout Redirection**: User can still redirect stdout, which may confuse TTY detection
   - **Impact**: Minimal - file logs always work
   - **Mitigation**: Documentation explains behavior

3. **Windows Compatibility**: SIGHUP is Unix-specific
   - **Impact**: Windows uses different process model (no terminals)
   - **Solution**: Signal handling is no-op on Windows, works correctly

## Future Considerations

### Potential Enhancements

1. **PID File**: Write PID to file for management scripts
   - Pro: Standard daemon feature
   - Con: Adds file management logic, users can do `echo $! > task.pid`

2. **Log Rotation**: Automatic log file rotation
   - Pro: Prevents unbounded disk usage
   - Con: Already supported by external tools (logrotate)

3. **Health Check Endpoint**: HTTP endpoint for monitoring
   - Pro: Easy integration with monitoring systems
   - Con: Adds HTTP server overhead

### Recommendations

- **Keep It Simple**: Current approach solves the core problem (SSH disconnect)
- **No Daemon Complexity**: Database sync tools should be simple and reliable
- **User Control**: Let users manage process lifecycle their way (`&`, `screen`, or scripts)

## Conclusion

**Design Principles**:
1. **User-Friendly**: Simple `&` backgrounding works (SSH disconnect survival)
2. **Defense in Depth**: Multiple failure protection layers
3. **Platform-Aware**: Adapts to deployment environment (TTY detection)
4. **Observable**: Clear logging of behavior
5. **No Parameter Changes**: All CLI arguments remain the same

**What Changed**:
- Internal: Added `signal.Ignore(syscall.SIGHUP)` (1 line)
- Internal: TTY auto-detection (5 lines)
- External: **Nothing** - same commands, same parameters

**What Didn't Change**:
- Config file `--config` still required
- Task name `--task-name` still recommended
- All subcommands work identically
- All parameters preserved

**Result**:
- Previous command: `./bin/df2redis replicate --config config.yaml --task-name task1 &`
- After upgrade: `./bin/df2redis replicate --config config.yaml --task-name task1 &`
- Same command, but now survives SSH disconnect

**Comparison**:
- redis-shake: No SIGHUP handling, relies on luck
- df2redis v1.x: Required nohup
- df2redis v2.x: Built-in protection, no external tools needed
