package rdbcli

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

// Importer wraps redis-rdb-cli invocation.
type Importer struct {
	cfg config.MigrateConfig
}

// NewImporter builds an importer instance.
func NewImporter(cfg config.MigrateConfig) (*Importer, error) {
	if cfg.RdbToolBinary == "" {
		return nil, errors.New("rdbToolBinary 未配置")
	}
	return &Importer{cfg: cfg}, nil
}

// Run executes redis-rdb-cli with computed arguments.
func (i *Importer) Run(ctx context.Context, targetSeed string) error {
	args := i.buildArgs(targetSeed)
	cmd := exec.CommandContext(ctx, i.cfg.RdbToolBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(i.cfg.RdbToolBinary)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 redis-rdb-cli 失败: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("redis-rdb-cli 退出异常: %w", err)
	}
	_ = start
	return nil
}

func (i *Importer) buildArgs(targetSeed string) []string {
	args := []string{
		"-s", i.cfg.SnapshotPath,
		"-m", fmt.Sprintf("redis://%s", targetSeed),
		"-r",
	}
	if strings.TrimSpace(i.cfg.RdbToolArgs) != "" {
		args = append(args, strings.Fields(i.cfg.RdbToolArgs)...)
	}
	if !i.cfg.Resume {
		args = append(args, "--no-resume")
	}
	return args
}
