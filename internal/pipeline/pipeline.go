package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"df2redis/internal/config"
	"df2redis/internal/executor/camellia"
	"df2redis/internal/executor/rdbcli"
	"df2redis/internal/redisx"
	"df2redis/internal/state"
)

// Status indicates stage result.
type Status string

const (
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// Result represents outcome of a stage.
type Result struct {
	Status  Status
	Message string
}

// Stage defines pipeline stage behaviour.
type Stage interface {
	Name() string
	Run(ctx *Context) Result
}

// Context carries shared information across stages.
type Context struct {
	RunCtx      context.Context
	Config      *config.Config
	StateDir    string
	StageData   map[string]any
	State       *state.Store
	Camellia    *camellia.Manager
	Importer    *rdbcli.Importer
	SourceRedis *redisx.Client
	TargetRedis *redisx.Client
}

// NewContext builds a new context from configuration.
func NewContext(runCtx context.Context, cfg *config.Config, store *state.Store) (*Context, error) {
	proxyCfg := cfg.ResolvedProxyConfig()
	migrateCfg := cfg.ResolvedMigrateConfig()
	cfg.Proxy = proxyCfg
	cfg.Migrate = migrateCfg

	cam, err := camellia.NewManager(proxyCfg)
	if err != nil {
		return nil, err
	}
	importer, err := rdbcli.NewImporter(migrateCfg, cfg.Target)
	if err != nil {
		return nil, err
	}
	baseCtx := context.Background()
	if runCtx != nil {
		baseCtx = runCtx
	}
	dialCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()
	sourceClient, err := redisx.Dial(dialCtx, redisx.Config{
		Addr:     cfg.Source.Addr,
		Password: cfg.Source.Password,
		TLS:      cfg.Source.TLS,
	})
	if err != nil {
		cam.Stop(context.Background())
		return nil, fmt.Errorf("连接源库失败: %w", err)
	}

	dialCtx2, cancel2 := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel2()
	targetClient, err := redisx.Dial(dialCtx2, redisx.Config{
		Addr:     cfg.Target.Seed,
		Password: cfg.Target.Password,
		TLS:      cfg.Target.TLS,
	})
	if err != nil {
		sourceClient.Close()
		cam.Stop(context.Background())
		return nil, fmt.Errorf("连接目标库失败: %w", err)
	}

	return &Context{
		RunCtx:      runCtx,
		Config:      cfg,
		StateDir:    cfg.ResolveStateDir(),
		StageData:   make(map[string]any),
		State:       store,
		Camellia:    cam,
		Importer:    importer,
		SourceRedis: sourceClient,
		TargetRedis: targetClient,
	}, nil
}

// Close releases all external resources.
func (c *Context) Close() {
	if c.SourceRedis != nil {
		_ = c.SourceRedis.Close()
	}
	if c.TargetRedis != nil {
		_ = c.TargetRedis.Close()
	}
	if c.Camellia != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Camellia.Stop(stopCtx)
		cancel()
	}
}

// Pipeline executes stages sequentially.
type Pipeline struct {
	stages []Stage
}

// New creates an empty pipeline.
func New() *Pipeline {
	return &Pipeline{stages: make([]Stage, 0, 10)}
}

// Add appends stage into pipeline.
func (p *Pipeline) Add(stage Stage) *Pipeline {
	p.stages = append(p.stages, stage)
	return p
}

// Run executes pipeline. Returns false once any stage fails.
func (p *Pipeline) Run(ctx *Context) bool {
	if ctx.State != nil {
		_ = ctx.State.SetPipelineStatus("running", "迁移流程开始")
	}
	for _, stage := range p.stages {
		log.Printf("开始执行阶段: %s", stage.Name())
		if ctx.State != nil {
			_ = ctx.State.UpdateStage(stage.Name(), string(StatusRunning), "进行中")
		}
		result := stage.Run(ctx)
		log.Printf("阶段 %s 完成，状态=%s，备注=%s", stage.Name(), result.Status, result.Message)
		if ctx.State != nil {
			_ = ctx.State.UpdateStage(stage.Name(), string(result.Status), result.Message)
		}
		if result.Status == StatusFailed {
			if ctx.State != nil {
				_ = ctx.State.SetPipelineStatus("failed", fmt.Sprintf("阶段 %s 失败", stage.Name()))
			}
			return false
		}
	}
	if ctx.State != nil {
		_ = ctx.State.SetPipelineStatus("completed", "迁移流程完成")
	}
	return true
}
