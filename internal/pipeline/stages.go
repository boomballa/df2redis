package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df2redis/internal/redisx"
)

// NewPrecheckStage validates external dependencies.
func NewPrecheckStage() Stage {
	return StageFunc{
		name: "precheck",
		run: func(ctx *Context) Result {
			resolve := ctx.Config.ResolvePath
			checks := []struct {
				desc string
				path string
			}{
				{"RDB snapshot", resolve(ctx.Config.Migrate.SnapshotPath)},
				{"redis-shake binary", resolve(ctx.Config.Migrate.ShakeBinary)},
			}
			for _, check := range checks {
				if check.path == "" {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("%s is not configured", check.desc)}
				}
				if check.desc == "RDB snapshot" && bool(ctx.Config.Migrate.AutoBgsave) {
					continue
				}
				if _, err := os.Stat(check.path); err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("%s does not exist: %v", check.desc, err)}
				}
			}
			if cfg := strings.TrimSpace(ctx.Config.Migrate.ShakeConfigFile); cfg != "" {
				if _, err := os.Stat(cfg); err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("redis-shake config file not found: %v", err)}
				}
			} else if strings.TrimSpace(ctx.Config.Migrate.ShakeArgs) == "" {
				// Later stages will auto-generate a shake config
			}
			if err := ctx.SourceRedis.Ping(); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("Source endpoint unavailable: %v", err)}
			}
			if err := ctx.TargetRedis.Ping(); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("Target endpoint unavailable: %v", err)}
			}
			return Result{Status: StatusSuccess, Message: "Dependency check passed"}
		},
	}
}

// NewShakeConfigStage generates a redis-shake config file from YAML when missing.
func NewShakeConfigStage() Stage {
	return StageFunc{
		name: "shake-config",
		run: func(ctx *Context) Result {
			cfgPath := strings.TrimSpace(ctx.Config.Migrate.ShakeConfigFile)
			hasArgs := strings.TrimSpace(ctx.Config.Migrate.ShakeArgs) != ""
			generatedPath := filepath.Join(ctx.StateDir, "shake.generated.toml")

			// Skip generation when user provided custom settings that are not auto paths
			if hasArgs || (cfgPath != "" && cfgPath != generatedPath) {
				return Result{Status: StatusSkipped, Message: "shakeConfigFile or shakeArgs provided; skipping generation"}
			}

			path, err := GenerateShakeConfigFile(ctx.Config, ctx.StateDir)
			if err != nil {
				return Result{Status: StatusFailed, Message: err.Error()}
			}
			if ctx.Importer != nil {
				ctx.Importer.SetConfigFile(path)
			}
			return Result{Status: StatusSuccess, Message: fmt.Sprintf("shake config generated: %s", path)}
		},
	}
}

// NewBgsaveStage triggers BGSAVE on Dragonfly/Redis source when enabled.
func NewBgsaveStage() Stage {
	return StageFunc{
		name: "bgsave",
		run: func(ctx *Context) Result {
			if !bool(ctx.Config.Migrate.AutoBgsave) {
				return Result{Status: StatusSkipped, Message: "autoBgsave disabled; skipping"}
			}
			startVal, err := ctx.SourceRedis.Do("LASTSAVE")
			var last int64
			if err == nil {
				last, _ = redisx.ToInt64(startVal)
			}
			if _, err := ctx.SourceRedis.Do("BGSAVE"); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("Failed to trigger BGSAVE: %v", err)}
			}
			timeout := time.Duration(ctx.Config.Migrate.BgsaveTimeout) * time.Second
			deadline := time.Now().Add(timeout)
			for {
				if time.Now().After(deadline) {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("BGSAVE timed out (%ds)", ctx.Config.Migrate.BgsaveTimeout)}
				}
				time.Sleep(2 * time.Second)
				val, err := ctx.SourceRedis.Do("LASTSAVE")
				if err != nil {
					continue
				}
				ts, err := redisx.ToInt64(val)
				if err != nil {
					continue
				}
				if ts > last {
					return Result{Status: StatusSuccess, Message: fmt.Sprintf("BGSAVE completed, LASTSAVE=%d", ts)}
				}
			}
		},
	}
}

// NewImportStage triggers redis-shake import.
func NewImportStage() Stage {
	return StageFunc{
		name: "import",
		run: func(ctx *Context) Result {
			base := context.Background()
			if ctx.RunCtx != nil {
				base = ctx.RunCtx
			}
			runCtx, cancel := context.WithCancel(base)
			defer cancel()
			start := time.Now()
			if err := ctx.Importer.Run(runCtx); err != nil {
				return Result{Status: StatusFailed, Message: err.Error()}
			}
			duration := time.Since(start)
			if ctx.State != nil {
				_ = ctx.State.RecordMetric("import.duration.seconds", duration.Seconds())
			}
			return Result{Status: StatusSuccess, Message: fmt.Sprintf("Full import completed in %.2fs", duration.Seconds())}
		},
	}
}

// NewIncrementalPlaceholderStage marks the TODO for Dragonfly journal streaming.
func NewIncrementalPlaceholderStage() Stage {
	return StageFunc{
		name: "incremental-sync",
		run: func(ctx *Context) Result {
			return Result{Status: StatusSkipped, Message: "Incremental sync (Dragonfly journal stream) not implemented"}
		},
	}
}

// StageFunc helper to implement Stage interface inline.
type StageFunc struct {
	name string
	run  func(ctx *Context) Result
}

func (s StageFunc) Name() string { return s.name }

func (s StageFunc) Run(ctx *Context) Result {
	return s.run(ctx)
}
