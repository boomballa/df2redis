package rdbcli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"df2redis/internal/config"
)

// Importer wraps redis-rdb-cli invocation.
type Importer struct {
	cfg    config.MigrateConfig
	target config.TargetConfig
}

// NewImporter builds an importer instance.
func NewImporter(cfg config.MigrateConfig, target config.TargetConfig) (*Importer, error) {
	if cfg.RdbToolBinary == "" {
		return nil, errors.New("rdbToolBinary 未配置")
	}
	return &Importer{cfg: cfg, target: target}, nil
}

// SetNodesConf allows updating nodes.conf path for cluster imports.
func (i *Importer) SetNodesConf(path string) {
	i.cfg.NodesConf = path
}

// Run executes redis-rdb-cli with computed arguments.
func (i *Importer) Run(ctx context.Context) error {
	args, err := i.buildArgs()
	if err != nil {
		return err
	}
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

func (i *Importer) buildArgs() ([]string, error) {
	args := []string{"-s", i.cfg.SnapshotPath}

	useNodesConf := strings.TrimSpace(i.cfg.NodesConf) != ""
	if useNodesConf {
		args = append(args, "-c", i.cfg.NodesConf)
	} else {
		targetURL, err := i.buildTargetURL()
		if err != nil {
			return nil, err
		}
		args = append(args, "-m", targetURL)
	}
	args = append(args, "-r")
	if strings.TrimSpace(i.cfg.RdbToolArgs) != "" {
		args = append(args, strings.Fields(i.cfg.RdbToolArgs)...)
	}
	if !i.cfg.Resume {
		args = append(args, "--no-resume")
	}
	return args, nil
}

func (i *Importer) buildTargetURL() (string, error) {
	seed := strings.TrimSpace(i.target.Seed)
	if seed == "" {
		return "", errors.New("目标 seed 为空")
	}

	// If user already provides full URL, respect it (and inject password if missing).
	if strings.Contains(seed, "://") {
		u, err := url.Parse(seed)
		if err != nil {
			return "", fmt.Errorf("解析目标 seed 失败: %w", err)
		}
		if i.target.Password != "" && u.User == nil {
			u.User = url.UserPassword("", i.target.Password)
		}
		return u.String(), nil
	}

	scheme := "redis"
	if i.target.TLS {
		scheme = "rediss"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   seed,
	}
	if i.target.Password != "" {
		u.User = url.UserPassword("", i.target.Password)
	}
	return u.String(), nil
}
