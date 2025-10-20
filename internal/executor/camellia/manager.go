package camellia

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"df2redis/internal/config"
)

// Manager controls lifecycle of Camellia proxy process.
type Manager struct {
	cfg     config.ProxyConfig
	process *exec.Cmd
	mu      sync.Mutex
}

// NewManager returns a Camellia process manager.
func NewManager(cfg config.ProxyConfig) (*Manager, error) {
	if cfg.Binary == "" {
		return nil, errors.New("camellia binary not configured")
	}
	return &Manager{cfg: cfg}, nil
}

// Start launches Camellia proxy with provided context.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process != nil {
		return errors.New("camellia already running")
	}

	args := parseArgs(m.cfg.Args)
	if m.cfg.ConfigFile != "" && !containsConfigFlag(args) {
		args = append(args, "--config", m.cfg.ConfigFile)
	}

	cmd := exec.CommandContext(ctx, m.cfg.Binary, args...)
	workingDir := m.cfg.WorkDir
	if workingDir == "" {
		workingDir = filepath.Dir(m.cfg.Binary)
	}
	cmd.Dir = workingDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(m.cfg.Env) > 0 {
		env := os.Environ()
		for k, v := range m.cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Camellia 失败: %w", err)
	}
	m.process = cmd
	return nil
}

// Stop terminates Camellia process gracefully.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- m.process.Wait()
	}()

	if err := m.process.Process.Signal(os.Interrupt); err != nil {
		_ = m.process.Process.Kill()
	}

	select {
	case <-ctx.Done():
		_ = m.process.Process.Kill()
		<-done
	case <-time.After(5 * time.Second):
		_ = m.process.Process.Kill()
		<-done
	case err := <-done:
		if err != nil {
			return err
		}
	}
	m.process = nil
	return nil
}

// WALPending reads pending entry count from configured walStatusFile.
func (m *Manager) WALPending() (int64, error) {
	if m.cfg.WALStatusFile == "" {
		return -1, errors.New("walStatusFile 未配置")
	}
	file, err := os.Open(m.cfg.WALStatusFile)
	if err != nil {
		return -1, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// 支持简单格式，例如 "pending=123"
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if strings.TrimSpace(parts[0]) != "pending" {
				continue
			}
			line = parts[1]
		}
		value, err := strconv.ParseInt(line, 10, 64)
		if err == nil {
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return -1, err
	}
	return -1, errors.New("未从 walStatusFile 解析到 pending 值")
}

// MetaHookScript returns path for CAS/meta hook registration.
func (m *Manager) MetaHookScript() string {
	return m.cfg.MetaHookScript
}

// RegisterMetaHook writes metadata for Camellia sidecar to consume.
func (m *Manager) RegisterMetaHook(sha, scriptPath string, metaPattern string, baselineKey string) error {
	if m.cfg.HookInfoFile == "" {
		return errors.New("hookInfoFile 未配置")
	}
	if err := os.MkdirAll(filepath.Dir(m.cfg.HookInfoFile), 0o755); err != nil {
		return fmt.Errorf("创建 HookInfo 目录失败: %w", err)
	}
	payload := map[string]any{
		"script_sha":   sha,
		"script_path":  scriptPath,
		"meta_pattern": metaPattern,
		"baseline_key": baselineKey,
		"updated_at":   time.Now().Unix(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.cfg.HookInfoFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入 hookInfoFile 失败: %w", err)
	}
	if err := os.Rename(tmp, m.cfg.HookInfoFile); err != nil {
		return fmt.Errorf("重命名 hookInfoFile 失败: %w", err)
	}
	return nil
}

func containsConfigFlag(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--config") {
			return true
		}
	}
	return false
}

func parseArgs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Fields(raw)
}
