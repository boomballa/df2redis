package runtime

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	cfgpkg "df2redis/internal/config"
)

const (
	runtimeVersion         = "0.1.1"
	camelliaJarName        = "camellia-redis-proxy-bootstrap.jar"
	camelliaConfigFileName = "camellia-proxy.toml"
	camelliaLuaFileName    = "meta_cas.lua"
)

// Assets describes located runtime assets.
type Assets struct {
	JavaBinary  string
	CamelliaJar string
	ConfigFile  string
	LuaScript   string
	WorkDir     string
}

// Ensure prepares runtime assets (JRE / Camellia jar / config / lua) for auto mode.
func Ensure(cfg *cfgpkg.Config) (*Assets, error) {
	baseDir, err := ensureRuntimeDir()
	if err != nil {
		return nil, err
	}

	jarPath, err := ensureCamelliaJar(cfg, baseDir)
	if err != nil {
		return nil, err
	}

	configPath, err := ensureCamelliaConfig(cfg, baseDir)
	if err != nil {
		return nil, err
	}

	luaPath, err := ensureLuaScript(cfg, baseDir)
	if err != nil {
		return nil, err
	}

	javaPath, err := ensureJavaRuntime(cfg, baseDir)
	if err != nil {
		return nil, err
	}

	return &Assets{
		JavaBinary:  javaPath,
		CamelliaJar: jarPath,
		ConfigFile:  configPath,
		LuaScript:   luaPath,
		WorkDir:     filepath.Dir(jarPath),
	}, nil
}

func ensureRuntimeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法获取用户目录: %w", err)
	}
	base := filepath.Join(home, ".df2redis", "runtime", runtimeVersion)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("创建运行目录失败: %w", err)
	}
	return base, nil
}

func ensureCamelliaJar(cfg *cfgpkg.Config, baseDir string) (string, error) {
	dest := filepath.Join(baseDir, camelliaJarName)
	if fileExists(dest) {
		return dest, nil
	}

	candidates := []string{}
	for _, base := range assetSearchBases(cfg) {
		candidates = append(candidates,
			filepath.Join(base, "camellia", "camellia-redis-proxy", "camellia-redis-proxy-bootstrap", "target"),
			filepath.Join(base, "assets", "camellia"),
		)
	}

	var source string
	for _, dir := range candidates {
		matches, _ := filepath.Glob(filepath.Join(dir, "camellia-redis-proxy-bootstrap-*.jar"))
		if len(matches) > 0 {
			source = matches[0]
			break
		}
		jar := filepath.Join(dir, camelliaJarName)
		if fileExists(jar) {
			source = jar
			break
		}
	}
	if source == "" {
		return "", errors.New("未找到 camellia-redis-proxy-bootstrap.jar，请先编译或放置到 assets/camellia/")
	}

	if err := copyFile(source, dest); err != nil {
		return "", fmt.Errorf("复制 Camellia Jar 失败: %w", err)
	}
	return dest, nil
}

func ensureCamelliaConfig(cfg *cfgpkg.Config, baseDir string) (string, error) {
	dest := filepath.Join(baseDir, camelliaConfigFileName)
	if cfg.Proxy.IsAutoConfigFile() {
		if err := generateCamelliaConfig(cfg, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	if fileExists(dest) {
		return dest, nil
	}
	path := cfg.ResolvePath(cfg.Proxy.ConfigFile)
	if !fileExists(path) {
		// 尝试从 assets 搜索
		for _, base := range assetSearchBases(cfg) {
			candidate := filepath.Join(base, "assets", "camellia", camelliaConfigFileName)
			if fileExists(candidate) {
				path = candidate
				break
			}
		}
	}
	if !fileExists(path) {
		return "", fmt.Errorf("未找到 Camellia 配置文件: %s", path)
	}
	if err := copyFile(path, dest); err != nil {
		return "", fmt.Errorf("复制Camellia配置失败: %w", err)
	}
	return dest, nil
}

func ensureLuaScript(cfg *cfgpkg.Config, baseDir string) (string, error) {
	dest := filepath.Join(baseDir, camelliaLuaFileName)
	candidates := []string{
		cfg.ResolvePath(cfg.Proxy.MetaHookScript),
	}
	for _, base := range assetSearchBases(cfg) {
		candidates = append(candidates,
			filepath.Join(base, "assets", "lua", camelliaLuaFileName),
			filepath.Join(base, "lua", camelliaLuaFileName),
		)
	}

	if cfg.Proxy.IsAutoMetaHookScript() {
		if err := copyFirstExisting(candidates[1:], dest); err != nil {
			return "", err
		}
		return dest, nil
	}

	if fileExists(dest) {
		return dest, nil
	}
	if err := copyFirstExisting([]string{candidates[0]}, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func ensureJavaRuntime(cfg *cfgpkg.Config, baseDir string) (string, error) {
	platform := PlatformString()
	destDir := filepath.Join(baseDir, "jre", platform)
	if javaBin, ok := findJavaBinary(destDir); ok {
		return javaBin, nil
	}
	archiveName := fmt.Sprintf("jre-%s.tar.gz", platform)
	if archive := locateRuntimeArchive(cfg.ConfigDir(), platform); archive != "" {
		if err := extractTarGz(archive, destDir); err != nil {
			return "", fmt.Errorf("解压内置 JRE 失败: %w", err)
		}
		if javaBin, ok := findJavaBinary(destDir); ok {
			return javaBin, nil
		}
	}
	if javaHome := os.Getenv("JAVA_HOME"); javaHome != "" {
		candidate := filepath.Join(javaHome, "bin", "java")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	if javaHome := os.Getenv("DF2REDIS_JAVA_HOME"); javaHome != "" {
		candidate := filepath.Join(javaHome, "bin", "java")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("java"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("未找到 Java 运行时。请配置 JAVA_HOME/DF2REDIS_JAVA_HOME 或在 assets/runtime/ 中提供与平台匹配的 JRE 压缩包(例如 %s)", archiveName)
}

func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// PlatformString returns goos-goarch (for future use).
func PlatformString() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func copyFirstExisting(paths []string, dest string) error {
	for _, p := range paths {
		if fileExists(p) {
			return copyFile(p, dest)
		}
	}
	return fmt.Errorf("未找到需要的文件，尝试路径: %v", paths)
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dest, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			out, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// ignore other types
		}
	}
	return nil
}

func generateCamelliaConfig(cfg *cfgpkg.Config, dest string) error {
	if dir := cfg.ResolvePath(cfg.Proxy.WALDir); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if status := cfg.ResolvePath(cfg.Proxy.WALStatusFile); status != "" {
		_ = os.MkdirAll(filepath.Dir(status), 0o755)
	}
	data := map[string]string{
		"PORT":            fmt.Sprintf("%d", parsePort(cfg.Proxy.Endpoint)),
		"SOURCE_URL":      buildRedisURL(cfg.Source.Addr, cfg.Source.Password, cfg.Source.TLS),
		"TARGET_URL":      buildRedisURL(cfg.Target.Seed, cfg.Target.Password, cfg.Target.TLS),
		"HOOK_INFO_FILE":  cfg.ResolvePath(cfg.Proxy.HookInfoFile),
		"WAL_DIR":         cfg.ResolvePath(cfg.Proxy.WALDir),
		"WAL_STATUS_FILE": cfg.ResolvePath(cfg.Proxy.WALStatusFile),
		"CONSOLE_PORT":    fmt.Sprintf("%d", cfg.Proxy.ConsolePort),
	}

	templateCandidates := []string{
		filepath.Join(cfg.ConfigDir(), "assets", "camellia", camelliaConfigFileName),
	}
	var tplContent []byte
	for _, c := range templateCandidates {
		if fileExists(c) {
			content, err := os.ReadFile(c)
			if err != nil {
				return err
			}
			tplContent = content
			break
		}
	}
	if tplContent == nil {
		tplContent = []byte(defaultCamelliaTemplate)
	}

	content := string(tplContent)
	for key, value := range data {
		placeholder := fmt.Sprintf("{{%s}}", strings.ToUpper(key))
		content = strings.ReplaceAll(content, placeholder, value)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return err
	}
	return nil
}

const defaultCamelliaTemplate = `# Auto generated Camellia config
[server]
port = {{PORT}}
consolePort = {{CONSOLE_PORT}}
monitorEnable = true

[[upstreams]]
name = "dragonfly"
type = "standalone"
url = "{{SOURCE_URL}}"

[[upstreams]]
name = "redis"
type = "standalone"
url = "{{TARGET_URL}}"

[metadata]
hookInfoFile = "{{HOOK_INFO_FILE}}"
walDir = "{{WAL_DIR}}"
walStatusFile = "{{WAL_STATUS_FILE}}"
`

func parsePort(endpoint string) int {
	parts := strings.Split(endpoint, ":")
	if len(parts) == 0 {
		return 6380
	}
	last := parts[len(parts)-1]
	var port int
	fmt.Sscanf(last, "%d", &port)
	if port <= 0 {
		return 6380
	}
	return port
}

func buildRedisURL(addr, password string, tls bool) string {
	scheme := "redis"
	if tls {
		scheme = "rediss"
	}
	u := &url.URL{Scheme: scheme, Host: addr}
	if password != "" {
		u.User = url.UserPassword("", password)
	}
	return u.String()
}

func locateRuntimeArchive(configDir, platform string) string {
	base := filepath.Join(configDir, "assets", "runtime")
	aliases := runtimeArchivePatterns(platform)
	for _, pattern := range aliases {
		matches, _ := filepath.Glob(filepath.Join(base, pattern))
		if len(matches) > 0 {
			return matches[0]
		}
	}
	matches, _ := filepath.Glob(filepath.Join(base, "*.tar.gz"))
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func runtimeArchivePatterns(platform string) []string {
	patterns := []string{
		fmt.Sprintf("jre-%s.tar.gz", platform),
		fmt.Sprintf("jre-%s.tgz", platform),
	}
	for _, alias := range platformAliases(platform) {
		patterns = append(patterns,
			fmt.Sprintf("*%s*.tar.gz", alias),
			fmt.Sprintf("*%s*.tgz", alias),
		)
	}
	return patterns
}

func platformAliases(platform string) []string {
	switch platform {
	case "darwin-arm64":
		return []string{"darwin-arm64", "mac-arm64", "mac_aarch64", "mac", "osx"}
	case "darwin-amd64":
		return []string{"darwin-amd64", "mac-x64", "mac_x64", "mac", "osx", "x64_mac"}
	case "linux-amd64":
		return []string{"linux-amd64", "linux-x64", "linux_x64", "linux"}
	case "linux-arm64":
		return []string{"linux-arm64", "linux-aarch64", "linux"}
	default:
		return []string{platform}
	}
}

func assetSearchBases(cfg *cfgpkg.Config) []string {
	bases := []string{}
	seen := map[string]struct{}{}
	add := func(path string) {
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		bases = append(bases, clean)
	}
	add(cfg.ConfigDir())
	add(filepath.Dir(cfg.ConfigDir()))
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	return bases
}

func findJavaBinary(root string) (string, bool) {
	candidate := filepath.Join(root, "bin", "java")
	if fileExists(candidate) {
		return candidate, true
	}
	var found string
	errDone := errors.New("found")
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "java" && strings.Contains(filepath.Dir(path), string(os.PathSeparator)+"bin") {
			found = path
			return errDone
		}
		return nil
	})
	if found != "" {
		return found, true
	}
	if walkErr != nil && !errors.Is(walkErr, errDone) {
		return "", false
	}
	return "", false
}
