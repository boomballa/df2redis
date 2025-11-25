package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds migration configuration.
type Config struct {
	Source     SourceConfig  `json:"source"`
	Target     TargetConfig  `json:"target"`
	Migrate    MigrateConfig `json:"migrate"`
	StateDir   string        `json:"stateDir"`
	StatusFile string        `json:"statusFile"`

	path         string
	stateDirPath string
	statusPath   string
}

type SourceConfig struct {
	Type     string `json:"type"`
	Addr     string `json:"addr"`
	Password string `json:"password"`
	TLS      bool   `json:"tls"`
}

type TargetConfig struct {
	Type     string `json:"type"`
	Seed     string `json:"seed"`
	Password string `json:"password"`
	TLS      bool   `json:"tls"`
}

// Boolish accepts true/false or quoted "true"/"false" in JSON decoding.
type Boolish bool

// UnmarshalJSON allows bool values represented as strings.
func (b *Boolish) UnmarshalJSON(data []byte) error {
	// Try plain bool first.
	var bv bool
	if err := json.Unmarshal(data, &bv); err == nil {
		*b = Boolish(bv)
		return nil
	}
	// Try string "true"/"false".
	var sv string
	if err := json.Unmarshal(data, &sv); err == nil {
		switch strings.ToLower(strings.TrimSpace(sv)) {
		case "true":
			*b = Boolish(true)
			return nil
		case "false":
			*b = Boolish(false)
			return nil
		}
	}
	return fmt.Errorf("cannot decode %s as bool", string(data))
}

type MigrateConfig struct {
	SnapshotPath    string  `json:"snapshotPath"`
	ShakeBinary     string  `json:"shakeBinary"`
	ShakeArgs       string  `json:"shakeArgs"`
	ShakeConfigFile string  `json:"shakeConfigFile"`
	AutoBgsave      Boolish `json:"autoBgsave"`
	BgsaveTimeout   int     `json:"bgsaveTimeoutSeconds"`
}

// ValidationError collects configuration issues.
type ValidationError struct {
	Path   string
	Errors []string
}

func (e *ValidationError) Error() string {
	builder := strings.Builder{}
	builder.WriteString("配置校验失败:")
	if e.Path != "" {
		builder.WriteString(" ")
		builder.WriteString(e.Path)
	}
	for _, err := range e.Errors {
		builder.WriteString("\n - ")
		builder.WriteString(err)
	}
	return builder.String()
}

// Load reads configuration file.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("配置文件路径为空")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径失败: %w", err)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("无法打开配置文件 %s: %w", absPath, err)
	}
	defer file.Close()

	raw, err := parseYAML(file)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("序列化配置失败: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("反序列化配置失败: %w", err)
	}

	cfg.path = absPath
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.resolveStateDir()
	return &cfg, nil
}

// ApplyDefaults populates default values.
func (c *Config) ApplyDefaults() {
	if c.Source.Type == "" {
		c.Source.Type = "dragonfly"
	}
	if c.Target.Type == "" {
		c.Target.Type = "redis"
	}
	if c.StateDir == "" {
		c.StateDir = "state"
	}
	if c.StatusFile == "" {
		c.StatusFile = "state/status.json"
	}
	if c.Migrate.BgsaveTimeout == 0 {
		c.Migrate.BgsaveTimeout = 300
	}
}

// Validate ensures config is usable.
func (c *Config) Validate() error {
	var errs []string

	if c.Source.Addr == "" {
		errs = append(errs, "source.addr 必填")
	}
	if c.Target.Seed == "" {
		errs = append(errs, "target.seed 必填")
	}
	if c.Migrate.SnapshotPath == "" {
		errs = append(errs, "migrate.snapshotPath 必填 (RDB 文件路径)")
	}
	if c.Migrate.ShakeBinary == "" {
		errs = append(errs, "migrate.shakeBinary 必填 (redis-shake 可执行文件路径)")
	}
	// 如果既没有提供 shakeArgs 也没有提供 shakeConfigFile，将自动生成配置文件

	if len(errs) > 0 {
		return &ValidationError{Path: c.path, Errors: errs}
	}
	return nil
}

func (c *Config) resolveStateDir() {
	baseDir := filepath.Dir(c.path)
	dir := c.StateDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	c.stateDirPath = filepath.Clean(dir)

	status := c.StatusFile
	if !filepath.IsAbs(status) {
		status = filepath.Join(baseDir, status)
	}
	c.statusPath = filepath.Clean(status)
}

// ResolveStateDir returns absolute state directory.
func (c *Config) ResolveStateDir() string {
	return c.stateDirPath
}

// StatusFilePath returns absolute path to status file.
func (c *Config) StatusFilePath() string {
	return c.statusPath
}

// EnsureStateDir makes sure state directory exists.
func (c *Config) EnsureStateDir() error {
	if err := os.MkdirAll(c.stateDirPath, 0o755); err != nil {
		return err
	}
	statusDir := filepath.Dir(c.statusPath)
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		return err
	}
	return nil
}

// Summary returns concise overview.
func (c *Config) Summary() string {
	return fmt.Sprintf("source=%s@%s, target=%s@%s, migrate(snapshot=%s), stateDir=%s, statusFile=%s",
		c.Source.Type, c.Source.Addr,
		c.Target.Type, c.Target.Seed,
		c.Migrate.SnapshotPath,
		c.ResolveStateDir(), c.StatusFilePath())
}

// PrettySummary returns a multi-line summary with emojis.
func (c *Config) PrettySummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  🗄️ source    : %s @ %s\n", c.Source.Type, c.Source.Addr)
	fmt.Fprintf(&b, "  🎯 target    : %s @ %s\n", c.Target.Type, c.Target.Seed)
	fmt.Fprintf(&b, "  🚚 migrate   : snapshot=%s\n", c.Migrate.SnapshotPath)
	fmt.Fprintf(&b, "  📂 stateDir  : %s\n", c.ResolveStateDir())
	fmt.Fprintf(&b, "  📝 statusFile: %s", c.StatusFilePath())
	return b.String()
}

// ResolvePath returns absolute path based on config file location.
func (c *Config) ResolvePath(path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	base := filepath.Dir(c.path)
	return filepath.Clean(filepath.Join(base, path))
}

// ConfigDir returns directory of config file.
func (c *Config) ConfigDir() string {
	return filepath.Dir(c.path)
}

// ResolvedMigrateConfig returns migrate config with resolved paths.
func (c *Config) ResolvedMigrateConfig() MigrateConfig {
	mc := c.Migrate
	mc.SnapshotPath = c.ResolvePath(mc.SnapshotPath)
	mc.ShakeBinary = c.ResolvePath(mc.ShakeBinary)
	mc.ShakeConfigFile = cleanPath(mc.ShakeConfigFile)
	if mc.ShakeConfigFile != "" {
		mc.ShakeConfigFile = c.ResolvePath(mc.ShakeConfigFile)
	}
	if mc.BgsaveTimeout <= 0 {
		mc.BgsaveTimeout = 300
	}
	return mc
}

func cleanPath(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.Trim(s, "\"'")
	return s
}
