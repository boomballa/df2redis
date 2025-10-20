package camellia

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"df2redis/internal/config"
)

// Manager controls lifecycle of Camellia proxy process.
type Manager struct {
	cfg     config.ProxyConfig
	process *exec.Cmd
	mu      sync.Mutex
	cancel  context.CancelFunc
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
	m.startStatusWriter()
	return nil
}

// Stop terminates Camellia process gracefully.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process == nil {
		return nil
	}

	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
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
		if err != nil && !isIgnorableExit(err) {
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

func (m *Manager) startStatusWriter() {
	if m.cfg.WALStatusFile == "" {
		return
	}
	port := m.cfg.ConsolePort
	if port <= 0 {
		port = 16379
	}
	host := "127.0.0.1"
	if h, _, err := net.SplitHostPort(m.cfg.Endpoint); err == nil && h != "" {
		if h != "0.0.0.0" {
			host = h
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.statusWriterLoop(ctx, host, port)
}

func (m *Manager) statusWriterLoop(ctx context.Context, host string, port int) {
	url := fmt.Sprintf("http://%s:%d/metrics", host, port)
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	errorLogged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pending, err := fetchPendingMetrics(client, url)
			if err != nil {
				if !errorLogged {
					log.Printf("读取 Camellia metrics 失败: %v", err)
					errorLogged = true
				}
				continue
			}
			errorLogged = false
			if err := writeWalStatus(m.cfg.WALStatusFile, pending); err != nil && !os.IsPermission(err) {
				log.Printf("写入 wal status 失败: %v", err)
			}
		}
	}
}

func fetchPendingMetrics(client *http.Client, url string) (int64, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metrics 响应码异常: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return parsePendingMetric(string(body))
}

func parsePendingMetric(metrics string) (int64, error) {
	var pending int64
	found := false
	scanner := bufio.NewScanner(strings.NewReader(metrics))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "kv_write_buffer_stats") && strings.Contains(line, "metric_type=\"pending\"") {
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			valueStr := parts[len(parts)-1]
			val, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				continue
			}
			pending += int64(val)
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("未找到 pending 指标")
	}
	return pending, nil
}

func writeWalStatus(path string, pending int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("pending=%d\nupdated_at=%d\n", pending, time.Now().UnixMilli())
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func isIgnorableExit(err error) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if status.Exited() {
		return status.ExitStatus() == 0
	}
	if status.Signaled() {
		switch status.Signal() {
		case syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL:
			return true
		}
	}
	return false
}
