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
	TaskName   string           `json:"taskName"` // optional task name used for log file naming
	Source     SourceConfig     `json:"source"`
	Target     TargetConfig     `json:"target"`
	Migrate    MigrateConfig    `json:"migrate"`
	Checkpoint CheckpointConfig `json:"checkpoint"`
	Conflict   ConflictConfig   `json:"conflict"`
	Log        LogConfig        `json:"log"`
	Dashboard  DashboardConfig  `json:"dashboard"`
	StateDir   string           `json:"stateDir"`
	StatusFile string           `json:"statusFile"`

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

// CheckpointConfig controls LSN checkpoint persistence
type CheckpointConfig struct {
	Enabled  bool   `json:"enabled"`         // enable checkpointing
	Interval int    `json:"intervalSeconds"` // auto-save interval in seconds
	Path     string `json:"path"`            // optional checkpoint path (default: stateDir/checkpoint.json)
}

// LogConfig configures logging
type LogConfig struct {
	Dir            string `json:"dir"`            // log directory (default: logs)
	Level          string `json:"level"`          // log level debug/info/warn/error (default: info)
	ConsoleEnabled *bool  `json:"consoleEnabled"` // show key info on console (default: true)
}

// ConsoleEnabledValue returns the effective console logging flag.
func (lc LogConfig) ConsoleEnabledValue() bool {
	if lc.ConsoleEnabled == nil {
		return true
	}
	return *lc.ConsoleEnabled
}

// ConflictConfig sets the key conflict policy
type ConflictConfig struct {
	Policy string `json:"policy"` // overwrite (default), panic (stop on duplicates), skip (ignore duplicates)
}

// DashboardConfig controls the embedded dashboard server.
type DashboardConfig struct {
	Addr string `json:"addr"` // e.g. ":8080"
}

// ValidationError collects configuration issues.
type ValidationError struct {
	Path   string
	Errors []string
}

func (e *ValidationError) Error() string {
	builder := strings.Builder{}
	builder.WriteString("Configuration validation failed:")
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
		return nil, fmt.Errorf("configuration file path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path: %w", err)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file %s: %w", absPath, err)
	}
	defer file.Close()

	raw, err := parseYAML(file)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to deserialize config: %w", err)
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
	// Checkpoint defaults
	if c.Checkpoint.Interval == 0 {
		c.Checkpoint.Interval = 10 // default 10 seconds
	}
	// Checkpoint.Enabled defaults to false and must be explicitly enabled
	// Checkpoint.Path defaults to empty and will fall back to stateDir/checkpoint.json

	// Log defaults
	if c.Log.Dir == "" {
		c.Log.Dir = "logs"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.ConsoleEnabled == nil {
		val := true
		c.Log.ConsoleEnabled = &val
	}

	// Conflict defaults
	if c.Conflict.Policy == "" {
		c.Conflict.Policy = "overwrite" // overwrite by default
	}
	if c.Dashboard.Addr == "" {
		c.Dashboard.Addr = ":8080"
	}
}

// Validate ensures config is usable.
func (c *Config) Validate() error {
	var errs []string

	if c.Source.Addr == "" {
		errs = append(errs, "source.addr is required")
	}
	if c.Target.Seed == "" {
		errs = append(errs, "target.seed is required")
	}
	if c.Migrate.SnapshotPath == "" {
		errs = append(errs, "migrate.snapshotPath is required (RDB file path)")
	}
	if c.Migrate.ShakeBinary == "" {
		errs = append(errs, "migrate.shakeBinary is required (redis-shake binary path)")
	}
	// When neither shakeArgs nor shakeConfigFile is provided a config file will be generated

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

// ResolveCheckpointPath returns the absolute checkpoint path
func (c *Config) ResolveCheckpointPath() string {
	if c.Checkpoint.Path != "" {
		// resolve custom path when provided
		return c.ResolvePath(c.Checkpoint.Path)
	}
	// default to stateDir/checkpoint.json
	return filepath.Join(c.stateDirPath, "checkpoint.json")
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
	mc.ShakeConfigFile = cleanValue(mc.ShakeConfigFile)
	if mc.ShakeConfigFile != "" {
		mc.ShakeConfigFile = c.ResolvePath(mc.ShakeConfigFile)
	}
	mc.ShakeArgs = cleanValue(mc.ShakeArgs)
	if mc.BgsaveTimeout <= 0 {
		mc.BgsaveTimeout = 300
	}
	return mc
}

func cleanValue(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.Trim(s, "\"'")
	return s
}
