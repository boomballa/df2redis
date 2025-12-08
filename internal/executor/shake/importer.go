package shake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"df2redis/internal/config"
)

// Importer wraps redis-shake invocation for RDB restore.
type Importer struct {
	cfg    config.MigrateConfig
	target config.TargetConfig
}

// NewImporter builds an importer instance.
func NewImporter(cfg config.MigrateConfig, target config.TargetConfig) (*Importer, error) {
	if cfg.ShakeBinary == "" {
		return nil, errors.New("shakeBinary 未配置")
	}
	return &Importer{cfg: cfg, target: target}, nil
}

// SetConfigFile updates the shake config file path (for generated configs).
func (i *Importer) SetConfigFile(path string) {
	i.cfg.ShakeConfigFile = path
}

// Run executes redis-shake with provided arguments.
func (i *Importer) Run(ctx context.Context) error {
	args, err := i.buildArgs()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, i.cfg.ShakeBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(i.cfg.ShakeBinary)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 redis-shake 失败: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("redis-shake 退出异常: %w", err)
	}
	_ = start
	return nil
}

func (i *Importer) buildArgs() ([]string, error) {
	// If user provides full args, respect as-is.
	rawArgs := strings.TrimSpace(i.cfg.ShakeArgs)
	if rawArgs == "" && i.cfg.ShakeConfigFile == "" {
		return nil, errors.New("shakeArgs 或 shakeConfigFile 必须提供其一，以描述导入参数")
	}
	args := []string{}
	if i.cfg.ShakeConfigFile != "" {
		// redis-shake v4 accepts the config path as the first argument or via -conf.
		if rawArgs == "" {
			args = append(args, i.cfg.ShakeConfigFile)
		} else {
			args = append(args, "-conf", i.cfg.ShakeConfigFile)
		}
	}
	if rawArgs != "" {
		args = append(args, strings.Fields(rawArgs)...)
	}
	return args, nil
}
