package shake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"df2redis/internal/config"
	"df2redis/internal/logger"
)

// Importer wraps redis-shake invocation for RDB restore.
type Importer struct {
	cfg    config.MigrateConfig
	target config.TargetConfig
}

// NewImporter builds an importer instance.
func NewImporter(cfg config.MigrateConfig, target config.TargetConfig) (*Importer, error) {
	if cfg.ShakeBinary == "" {
		return nil, errors.New("shakeBinary is not configured")
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
	// Pipe output to both console and the detailed log file
	logWriter := logger.Writer()
	cmd.Stdout = io.MultiWriter(os.Stdout, logWriter)
	cmd.Stderr = io.MultiWriter(os.Stderr, logWriter)
	cmd.Dir = filepath.Dir(i.cfg.ShakeBinary)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start redis-shake: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("redis-shake exited unexpectedly: %w", err)
	}
	_ = start
	return nil
}

func (i *Importer) buildArgs() ([]string, error) {
	// If user provides full args, respect as-is.
	rawArgs := strings.TrimSpace(i.cfg.ShakeArgs)
	if rawArgs == "" && i.cfg.ShakeConfigFile == "" {
		return nil, errors.New("either shakeArgs or shakeConfigFile must be provided to describe import parameters")
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
