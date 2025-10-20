package pipeline

import (
	"context"
	"fmt"
	"os"
	"time"

	"df2redis/internal/consistency"
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
				{"Camellia binary", resolve(ctx.Config.Proxy.Binary)},
				{"Camellia config", resolve(ctx.Config.Proxy.ConfigFile)},
				{"Meta hook script", resolve(ctx.Config.Proxy.MetaHookScript)},
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

// NewMetaHookStage loads Lua script to Redis for meta updates.
func NewMetaHookStage() Stage {
	return StageFunc{
		name: "meta-hook",
		run: func(ctx *Context) Result {
			scriptPath := ctx.Config.ResolvePath(ctx.Config.Proxy.MetaHookScript)
			sha, err := ctx.TargetRedis.ScriptLoadFile(scriptPath)
			if err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("加载 Lua 失败: %v", err)}
			}
			ctx.StageData["metaScriptSHA"] = sha
			ctx.StageData["metaScriptPath"] = scriptPath
			if err := ctx.Camellia.RegisterMetaHook(sha, scriptPath, ctx.Config.Consistency.MetaKeyPattern, ctx.Config.Consistency.BaselineTsKey); err != nil {
				return Result{Status: StatusFailed, Message: fmt.Sprintf("注册 Camellia Meta hook 失败: %v", err)}
			}
			msg := fmt.Sprintf("脚本已加载，SHA=%s，并写入 hookInfo", sha)
			if ctx.State != nil {
				_ = ctx.State.RecordMetric("lua.meta.loaded", 1)
				_ = ctx.State.SetPipelineStatus("meta-ready", msg)
			}
			return Result{Status: StatusSuccess, Message: msg}
		},
	}
}

// NewStartProxyStage starts Camellia and captures WAL backlog.
func NewStartProxyStage() Stage {
	return StageFunc{
		name: "start-proxy",
		run: func(ctx *Context) Result {
			startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := ctx.Camellia.Start(startCtx); err != nil {
				return Result{Status: StatusFailed, Message: err.Error()}
			}

			if ctx.State != nil {
				if wal, err := ctx.Camellia.WALPending(); err == nil {
					_ = ctx.State.RecordMetric("camellia.wal.pending", float64(wal))
				}
			}
			return Result{Status: StatusSuccess, Message: "Camellia 已启动"}
		},
	}
}

// NewBaselineStage records baseline timestamp.
func NewBaselineStage() Stage {
	return StageFunc{
		name: "baseline",
		run: func(ctx *Context) Result {
			t0 := time.Now().UnixMilli()
			ctx.StageData["baselineT0"] = t0
			if ctx.State != nil {
				_ = ctx.State.RecordMetric("baseline.t0", float64(t0))
			}
			baselineKey := ctx.Config.Consistency.BaselineTsKey
			if baselineKey != "" {
				if err := setOnTarget(ctx, baselineKey, fmt.Sprintf("%d", t0)); err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("写入 %s 失败: %v", baselineKey, err)}
				}
				if ctx.SourceRedis != nil {
					_ = ctx.SourceRedis.Set(baselineKey, fmt.Sprintf("%d", t0))
				}
			}
			return Result{Status: StatusSuccess, Message: fmt.Sprintf("基线时间 T0=%d 已写入目标", t0)}
		},
	}
}

// NewImportStage triggers redis-rdb-cli import.
func NewImportStage() Stage {
	return StageFunc{
		name: "import",
		run: func(ctx *Context) Result {
			runCtx, cancel := context.WithCancel(context.Background())
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

// NewFenceStage records fence readiness placeholder.
func NewFenceStage() Stage {
	return StageFunc{
		name: "fence",
		run: func(ctx *Context) Result {
			fenceValue := time.Now().UnixMilli()
			ctx.StageData["fenceValue"] = fenceValue
			fenceKey := ctx.Config.Consistency.FenceKey
			if fenceKey != "" {
				if err := setOnTarget(ctx, fenceKey, fmt.Sprintf("%d", fenceValue)); err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("写入目标 %s 失败: %v", fenceKey, err)}
				}
				if ctx.SourceRedis != nil {
					if err := ctx.SourceRedis.Set(fenceKey, fmt.Sprintf("%d", fenceValue)); err != nil {
						return Result{Status: StatusFailed, Message: fmt.Sprintf("写入源 %s 失败: %v", fenceKey, err)}
					}
				}
				if val, err := getFromTarget(ctx, fenceKey); err == nil {
					ctx.StageData["fenceTargetValue"] = val
				}
			}

			message := fmt.Sprintf("Fence=%d 已写入，等待 WAL 清零", fenceValue)
			if ctx.State != nil {
				if wal, err := ctx.Camellia.WALPending(); err == nil {
					_ = ctx.State.RecordMetric("camellia.wal.pending", float64(wal))
					if wal == 0 {
						_ = ctx.State.SetPipelineStatus("fence-ready", fmt.Sprintf("Fence=%d，WAL=0", fenceValue))
						message = fmt.Sprintf("Fence=%d，WAL 已清零", fenceValue)
					} else {
						message = fmt.Sprintf("Fence=%d，WAL=%d", fenceValue, wal)
					}
				}
			}
			return Result{Status: StatusSuccess, Message: message}
		},
	}
}

// NewCutoverStage placeholder for traffic switch.
func NewCutoverStage() Stage {
	return StageFunc{
		name: "cutover",
		run: func(ctx *Context) Result {
			wal, err := ctx.Camellia.WALPending()
			if err == nil && wal > 0 {
				return Result{Status: StatusSkipped, Message: fmt.Sprintf("WAL 仍有积压: %d", wal)}
			}
			fenceKey := ctx.Config.Consistency.FenceKey
			var expected string
			if v, ok := ctx.StageData["fenceValue"].(int64); ok {
				expected = fmt.Sprintf("%d", v)
			}
			if fenceKey != "" {
				targetVal, err := ctx.TargetRedis.GetString(fenceKey)
				if err != nil {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("读取目标 %s 失败: %v", fenceKey, err)}
				}
				if expected != "" && targetVal != expected {
					return Result{Status: StatusFailed, Message: fmt.Sprintf("目标 Fence 与记录不一致: %s != %s", targetVal, expected)}
				}
			}

			sampleCount := 0
			sampleKeys := []string{}
			reply, err := ctx.TargetRedis.Do("SCAN", 0, "COUNT", 20)
			if err == nil {
				if arr, ok := reply.([]interface{}); ok && len(arr) == 2 {
					if keys, err := redisx.ToStringSlice(arr[1]); err == nil {
						sampleKeys = keys
						sampleCount = len(keys)
					}
				}
			}
			mismatches := 0
			mismatchKeys := []string{}
			if sampleCount > 0 {
				if result, err := consistency.CompareStringValues(ctx.SourceRedis, ctx.TargetRedis, sampleKeys); err == nil {
					mismatches = len(result)
					for i := 0; i < len(result) && i < 5; i++ {
						mismatchKeys = append(mismatchKeys, result[i].Key)
					}
					ctx.StageData["cutover_mismatch_samples"] = result
				}
			}
			if ctx.State != nil {
				_ = ctx.State.RecordMetric("cutover.sample.keys", float64(sampleCount))
				_ = ctx.State.RecordMetric("cutover.mismatch.count", float64(mismatches))
				statusMsg := fmt.Sprintf("样本=%d, mismatches=%d", sampleCount, mismatches)
				_ = ctx.State.SetPipelineStatus("cutover-ready", statusMsg)
				if mismatches > 0 {
					_ = ctx.State.RecordMetric("cutover.mismatch.flag", 1)
				}
			}
			msg := fmt.Sprintf("灰度切读前检查完成，样本=%d mismatch=%d", sampleCount, mismatches)
			if mismatches > 0 && len(mismatchKeys) > 0 {
				msg = fmt.Sprintf("%s (示例: %v)", msg, mismatchKeys)
			}
			return Result{Status: StatusSuccess, Message: msg}
		},
	}
}

// NewAutoCutoverStage builds plan for progressive traffic cutover.
func NewAutoCutoverStage() Stage {
	return StageFunc{
		name: "auto-cutover",
		run: func(ctx *Context) Result {
			plan := ctx.Config.Migrate.Cutover.Batches
			if len(plan) == 0 {
				return Result{Status: StatusSkipped, Message: "未配置 cutover 批次"}
			}
			mismatchCount := 0
			if samples, ok := ctx.StageData["cutover_mismatch_samples"].([]consistency.Mismatch); ok {
				mismatchCount = len(samples)
			}
			planSummary := make([]string, 0, len(plan))
			for _, batch := range plan {
				if mismatchCount > batch.MaxMismatch {
					msg := fmt.Sprintf("批次 %s 容忍 mismatch=%d，但当前=%d，阻断切流", batch.Label, batch.MaxMismatch, mismatchCount)
					if ctx.State != nil {
						_ = ctx.State.SetPipelineStatus("cutover-blocked", msg)
					}
					return Result{Status: StatusFailed, Message: msg}
				}
				step := fmt.Sprintf("推进到 %d%% 流量，观察 %s，允许 mismatch ≤ %d", batch.Percentage, batch.Duration, batch.MaxMismatch)
				planSummary = append(planSummary, step)
			}
			ctx.StageData["cutover_plan"] = planSummary
			if ctx.State != nil {
				_ = ctx.State.RecordMetric("cutover.plan.count", float64(len(plan)))
				if len(plan) > 0 {
					_ = ctx.State.RecordMetric("cutover.plan.first_percent", float64(plan[0].Percentage))
					_ = ctx.State.SetPipelineStatus("cutover-plan-ready", planSummary[0])
				}
			}
			return Result{Status: StatusSuccess, Message: fmt.Sprintf("自动切流计划就绪，共 %d 批次", len(plan))}
		},
	}
}

// NewCleanupStage stops Camellia.
func NewCleanupStage() Stage {
	return StageFunc{
		name: "cleanup",
		run: func(ctx *Context) Result {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := ctx.Camellia.Stop(stopCtx); err != nil {
				return Result{Status: StatusFailed, Message: err.Error()}
			}
			if ctx.SourceRedis != nil {
				_ = ctx.SourceRedis.Close()
				ctx.SourceRedis = nil
			}
			if ctx.TargetRedis != nil {
				_ = ctx.TargetRedis.Close()
				ctx.TargetRedis = nil
			}
			return Result{Status: StatusSuccess, Message: "Camellia 已停止"}
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

func setOnTarget(ctx *Context, key, value string) error {
	return executeWithCluster(ctx, func(client *redisx.Client) error {
		return client.Set(key, value)
	})
}

func getFromTarget(ctx *Context, key string) (string, error) {
	var result string
	err := executeWithCluster(ctx, func(client *redisx.Client) error {
		val, err := client.GetString(key)
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

func executeWithCluster(ctx *Context, fn func(*redisx.Client) error) error {
	if err := fn(ctx.TargetRedis); err != nil {
		return redirectCluster(ctx, err, fn)
	}
	return nil
}

func redirectCluster(ctx *Context, firstErr error, fn func(*redisx.Client) error) error {
	err := firstErr
	attempts := 0
	for redisx.IsMovedError(err) && attempts < 5 {
		addr, ok := redisx.ParseMovedAddr(err)
		if !ok {
			break
		}
		dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, dialErr := redisx.Dial(dialCtx, redisx.Config{
			Addr:     addr,
			Password: ctx.Config.Target.Password,
			TLS:      ctx.Config.Target.TLS,
		})
		cancel()
		if dialErr != nil {
			return fmt.Errorf("重定向到 %s 失败: %w", addr, dialErr)
		}
		err = fn(client)
		client.Close()
		attempts++
		if err == nil {
			return nil
		}
	}
	return err
}
