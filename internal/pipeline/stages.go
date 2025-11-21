package pipeline

import (
	"context"
	"fmt"
	"os"
	"time"
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
				{"redis-rdb-cli binary", resolve(ctx.Config.Migrate.RdbToolBinary)},
			}
			for _, check := range checks {
				if check.path == "" {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("%s 未配置", check.desc)}
				}
				if _, err := os.Stat(check.path); err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("%s 不存在: %v", check.desc, err)}
				}
			}
			if err := ctx.SourceRedis.Ping(); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("源库不可用: %v", err)}
			}
			if err := ctx.TargetRedis.Ping(); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("目标库不可用: %v", err)}
			}
			return Result{Status: StatusSuccess, Message: "依赖校验通过"}
		},
	}
}

// NewImportStage triggers redis-rdb-cli import.
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
			return Result{Status: StatusSuccess, Message: fmt.Sprintf("全量导入完成，用时 %.2fs", duration.Seconds())}
		},
	}
}

// NewIncrementalPlaceholderStage marks the TODO for Dragonfly journal streaming.
func NewIncrementalPlaceholderStage() Stage {
	return StageFunc{
		name: "incremental-sync",
		run: func(ctx *Context) Result {
			return Result{Status: StatusSkipped, Message: "增量同步（Dragonfly journal 流）尚未实现"}
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
