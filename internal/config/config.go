package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds migration configuration.
type Config struct {
	Source      SourceConfig      `json:"source"`
	Target      TargetConfig      `json:"target"`
	Proxy       ProxyConfig       `json:"proxy"`
	Migrate     MigrateConfig     `json:"migrate"`
	Consistency ConsistencyConfig `json:"consistency"`
	StateDir    string            `json:"stateDir"`
	StatusFile  string            `json:"statusFile"`

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

type ProxyConfig struct {
	Kind           string            `json:"kind"`
	Endpoint       string            `json:"endpoint"`
	WALDir         string            `json:"walDir"`
	PerKeyOrdering string            `json:"perKeyOrdering"`
	MirrorWrites   *bool             `json:"mirrorWrites"`
	Binary         string            `json:"binary"`
	ConfigFile     string            `json:"configFile"`
	WorkDir        string            `json:"workDir"`
	Args           string            `json:"args"`
	WALStatusFile  string            `json:"walStatusFile"`
	MetaHookScript string            `json:"metaHookScript"`
	Env            map[string]string `json:"env"`
	HookInfoFile   string            `json:"hookInfoFile"`
	ConsolePort    int               `json:"consolePort"`
}

func (p ProxyConfig) isAutoBinary() bool {
	return p.Binary == "" || strings.EqualFold(p.Binary, "auto")
}

func (p ProxyConfig) IsAutoConfigFile() bool {
	return p.ConfigFile == "" || strings.EqualFold(p.ConfigFile, "auto")
}

func (p ProxyConfig) IsAutoMetaHookScript() bool {
	return p.MetaHookScript == "" || strings.EqualFold(p.MetaHookScript, "auto")
}

type MigrateConfig struct {
	Mode          string          `json:"mode"`
	Concurrency   int             `json:"concurrency"`
	Pipeline      int             `json:"pipeline"`
	SlotBatch     int             `json:"slotBatch"`
	Throttle      *ThrottleConfig `json:"throttle"`
	SnapshotPath  string          `json:"snapshotPath"`
	RdbToolBinary string          `json:"rdbToolBinary"`
	RdbToolArgs   string          `json:"rdbToolArgs"`
	Resume        bool            `json:"resume"`
	Cutover       CutoverConfig   `json:"cutover"`
}

type ThrottleConfig struct {
	MaxOpsPerMaster *int  `json:"maxOpsPerMaster"`
	BigKeySlowpath  *bool `json:"bigKeySlowpath"`
}

type ConsistencyConfig struct {
	MetaKeyPattern string  `json:"metaKeyPattern"`
	FenceKey       string  `json:"fenceKey"`
	BaselineTsKey  string  `json:"baselineTsKey"`
	EnableCAS      *bool   `json:"enableCas"`
	SampleRate     float64 `json:"sampleRate"`
	LuaDir         string  `json:"luaDir"`
}

type CutoverConfig struct {
	Batches []CutoverBatch `json:"batches"`
}

type CutoverBatch struct {
	Label       string `json:"label"`
	Percentage  int    `json:"percentage"`
	Duration    string `json:"duration"`
	MaxMismatch int    `json:"maxMismatch"`
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
		c.Target.Type = "redis-cluster"
	}
	if c.Proxy.Kind == "" {
		c.Proxy.Kind = "camellia"
	}
	if c.Proxy.WALDir == "" {
		c.Proxy.WALDir = "state/wal"
	}
	if c.Proxy.PerKeyOrdering == "" {
		c.Proxy.PerKeyOrdering = "slot"
	}
	if c.Proxy.MirrorWrites == nil {
		defaultMirror := true
		c.Proxy.MirrorWrites = &defaultMirror
	}
	if c.Proxy.WorkDir == "" {
		c.Proxy.WorkDir = filepath.Dir(c.path)
	}
	if c.Proxy.WALStatusFile == "" && c.Proxy.WALDir != "" {
		c.Proxy.WALStatusFile = filepath.Join(c.Proxy.WALDir, "status.json")
	}
	if c.Proxy.HookInfoFile == "" && c.Proxy.WALDir != "" {
		c.Proxy.HookInfoFile = filepath.Join(c.Proxy.WALDir, "hook.json")
	}
	if c.Proxy.ConsolePort == 0 {
		c.Proxy.ConsolePort = 16379
	}
	if c.Proxy.ConfigFile == "" {
		c.Proxy.ConfigFile = "auto"
	}
	if c.Proxy.MetaHookScript == "" {
		c.Proxy.MetaHookScript = "auto"
	}
	if c.Migrate.Mode == "" {
		c.Migrate.Mode = "LIVE"
	} else {
		c.Migrate.Mode = strings.ToUpper(c.Migrate.Mode)
	}
	if c.Migrate.Concurrency <= 0 {
		c.Migrate.Concurrency = 8
	}
	if c.Migrate.Pipeline <= 0 {
		c.Migrate.Pipeline = 256
	}
	if c.Migrate.SlotBatch <= 0 {
		c.Migrate.SlotBatch = 1024
	}
	if c.Migrate.Throttle != nil && c.Migrate.Throttle.BigKeySlowpath == nil {
		defaultSlow := true
		c.Migrate.Throttle.BigKeySlowpath = &defaultSlow
	}
	if len(c.Migrate.Cutover.Batches) == 0 {
		c.Migrate.Cutover.Batches = []CutoverBatch{
			{Label: "phase-10", Percentage: 10, Duration: "5m", MaxMismatch: 0},
			{Label: "phase-50", Percentage: 50, Duration: "10m", MaxMismatch: 0},
			{Label: "phase-100", Percentage: 100, Duration: "15m", MaxMismatch: 0},
		}
	}
	if c.Migrate.RdbToolArgs == "" {
		c.Migrate.RdbToolArgs = ""
	}
	if c.Consistency.MetaKeyPattern == "" {
		c.Consistency.MetaKeyPattern = "meta:{%s}"
	}
	if c.Consistency.FenceKey == "" {
		c.Consistency.FenceKey = "MIGRATE_FENCE"
	}
	if c.Consistency.BaselineTsKey == "" {
		c.Consistency.BaselineTsKey = "MIGRATE_BASE_TS"
	}
	if c.Consistency.EnableCAS == nil {
		defaultCAS := true
		c.Consistency.EnableCAS = &defaultCAS
	}
	if c.Consistency.SampleRate <= 0 {
		c.Consistency.SampleRate = 0.001
	}
	if c.Consistency.LuaDir == "" {
		c.Consistency.LuaDir = filepath.Join(filepath.Dir(c.path), "lua")
	}
	if c.StateDir == "" {
		c.StateDir = "state"
	}
	if c.StatusFile == "" {
		c.StatusFile = "state/status.json"
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
	if c.Proxy.Endpoint == "" {
		errs = append(errs, "proxy.endpoint 必填")
	}
	if c.Proxy.WALDir == "" {
		errs = append(errs, "proxy.walDir 必填")
	}
	if c.Proxy.WALStatusFile == "" {
		errs = append(errs, "proxy.walStatusFile 必填")
	}
	if c.Proxy.ConsolePort <= 0 {
		errs = append(errs, "proxy.consolePort 必须 > 0")
	}
	if !c.Proxy.isAutoBinary() && c.Proxy.Binary == "" {
		errs = append(errs, "proxy.binary 必填或设置为 auto")
	}
	if c.Proxy.MetaHookScript == "" {
		errs = append(errs, "proxy.metaHookScript 必须指定，用于维护 meta 写入")
	}
	if c.Proxy.HookInfoFile == "" {
		errs = append(errs, "proxy.hookInfoFile 必填 (Camellia 注入 meta hook 信息)")
	}
	if c.Migrate.Mode != "LIVE" && c.Migrate.Mode != "COLD" {
		errs = append(errs, "migrate.mode 仅支持 LIVE 或 COLD")
	}
	if c.Migrate.Concurrency <= 0 {
		errs = append(errs, "migrate.concurrency 必须 > 0")
	}
	if c.Migrate.Pipeline <= 0 {
		errs = append(errs, "migrate.pipeline 必须 > 0")
	}
	if c.Migrate.SlotBatch <= 0 {
		errs = append(errs, "migrate.slotBatch 必须 > 0")
	}
	if c.Migrate.SnapshotPath == "" {
		errs = append(errs, "migrate.snapshotPath 必填 (RDB 文件路径)")
	}
	if c.Migrate.RdbToolBinary == "" {
		errs = append(errs, "migrate.rdbToolBinary 必填 (redis-rdb-cli rmt 路径)")
	}
	if c.Migrate.Throttle != nil && c.Migrate.Throttle.MaxOpsPerMaster != nil && *c.Migrate.Throttle.MaxOpsPerMaster <= 0 {
		errs = append(errs, "migrate.throttle.maxOpsPerMaster 必须 > 0")
	}
	if !strings.Contains(c.Consistency.MetaKeyPattern, "%s") {
		errs = append(errs, "consistency.metaKeyPattern 必须包含 %s")
	}
	if c.Consistency.SampleRate < 0 || c.Consistency.SampleRate > 1 {
		errs = append(errs, "consistency.sampleRate 必须在 0~1 之间")
	}
	if len(c.Migrate.Cutover.Batches) == 0 {
		errs = append(errs, "migrate.cutover.batches 至少一项")
	}
	for idx, batch := range c.Migrate.Cutover.Batches {
		if batch.Percentage <= 0 || batch.Percentage > 100 {
			errs = append(errs, fmt.Sprintf("migrate.cutover.batches[%d].percentage 需在 1-100 内", idx))
		}
		if batch.Label == "" {
			c.Migrate.Cutover.Batches[idx].Label = fmt.Sprintf("batch-%d", idx+1)
		}
		if batch.Duration != "" {
			if _, err := time.ParseDuration(batch.Duration); err != nil {
				errs = append(errs, fmt.Sprintf("migrate.cutover.batches[%d].duration 无法解析: %v", idx, err))
			}
		}
		if batch.MaxMismatch < 0 {
			errs = append(errs, fmt.Sprintf("migrate.cutover.batches[%d].maxMismatch 不能为负", idx))
		}
	}

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

// MirrorWritesEnabled reports whether double-write is on.
func (c *Config) MirrorWritesEnabled() bool {
	if c.Proxy.MirrorWrites == nil {
		return true
	}
	return *c.Proxy.MirrorWrites
}

// CASEnabled reports whether CAS protection is enabled.
func (c *Config) CASEnabled() bool {
	if c.Consistency.EnableCAS == nil {
		return true
	}
	return *c.Consistency.EnableCAS
}

// Summary returns concise overview.
func (c *Config) Summary() string {
	return fmt.Sprintf("source=%s@%s, target=%s@%s, proxy=%s@%s mirrorWrites=%t, migrate(mode=%s concurrency=%d pipeline=%d cutover=%d batches), consistency(metaPattern=%s enableCAS=%t sampleRate=%.4f), stateDir=%s, statusFile=%s",
		c.Source.Type, c.Source.Addr,
		c.Target.Type, c.Target.Seed,
		c.Proxy.Kind, c.Proxy.Endpoint, c.MirrorWritesEnabled(),
		c.Migrate.Mode, c.Migrate.Concurrency, c.Migrate.Pipeline, len(c.Migrate.Cutover.Batches),
		c.Consistency.MetaKeyPattern, c.CASEnabled(), c.Consistency.SampleRate,
		c.ResolveStateDir(), c.StatusFilePath())
}

// PrettySummary returns a multi-line summary with emojis.
func (c *Config) PrettySummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  🗄️ source    : %s @ %s\n", c.Source.Type, c.Source.Addr)
	fmt.Fprintf(&b, "  🎯 target    : %s @ %s\n", c.Target.Type, c.Target.Seed)
	fmt.Fprintf(&b, "  🔀 proxy     : %s @ %s (mirror=%t)\n", c.Proxy.Kind, c.Proxy.Endpoint, c.MirrorWritesEnabled())
	fmt.Fprintf(&b, "  🚚 migrate   : mode=%s concurrency=%d pipeline=%d\n", c.Migrate.Mode, c.Migrate.Concurrency, c.Migrate.Pipeline)
	fmt.Fprintf(&b, "                cutover batches=%d\n", len(c.Migrate.Cutover.Batches))
	fmt.Fprintf(&b, "  🔒 consistency: meta=%s enableCAS=%t sampleRate=%.4f\n", c.Consistency.MetaKeyPattern, c.CASEnabled(), c.Consistency.SampleRate)
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

// ResolvedProxyConfig returns proxy config with resolved paths.
func (c *Config) ResolvedProxyConfig() ProxyConfig {
	pc := c.Proxy
	if !pc.isAutoBinary() {
		pc.Binary = c.ResolvePath(pc.Binary)
	}
	if !pc.IsAutoConfigFile() {
		pc.ConfigFile = c.ResolvePath(pc.ConfigFile)
	}
	pc.WorkDir = c.ResolvePath(pc.WorkDir)
	pc.WALDir = c.ResolvePath(pc.WALDir)
	pc.WALStatusFile = c.ResolvePath(pc.WALStatusFile)
	if !pc.IsAutoMetaHookScript() {
		pc.MetaHookScript = c.ResolvePath(pc.MetaHookScript)
	}
	pc.HookInfoFile = c.ResolvePath(pc.HookInfoFile)
	return pc
}

// ResolvedMigrateConfig returns migrate config with resolved paths.
func (c *Config) ResolvedMigrateConfig() MigrateConfig {
	mc := c.Migrate
	mc.SnapshotPath = c.ResolvePath(mc.SnapshotPath)
	mc.RdbToolBinary = c.ResolvePath(mc.RdbToolBinary)
	return mc
}
