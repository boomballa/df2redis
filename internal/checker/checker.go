package checker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckMode 定义校验模式
type CheckMode string

const (
	// ModeFullValue 全量值对比（完整对比所有字段和值）
	ModeFullValue CheckMode = "full"
	// ModeKeyOutline 键轮廓对比（对比 key 存在性、类型、TTL、长度等元信息）
	ModeKeyOutline CheckMode = "outline"
	// ModeValueLength 值长度对比（只对比值的长度）
	ModeValueLength CheckMode = "length"
	// ModeSmartBigKey 智能对比（遇到大 key 时只对比长度，否则全量对比）
	ModeSmartBigKey CheckMode = "smart"
)

// Config 校验配置
type Config struct {
	// 源端 Redis 地址
	SourceAddr string
	// 源端 Redis 密码
	SourcePassword string
	// 目标端 Redis 地址
	TargetAddr string
	// 目标端 Redis 密码
	TargetPassword string
	// 校验模式
	Mode CheckMode
	// QPS 限制（0 表示不限制）
	QPS int
	// 并发度
	Parallel int
	// 结果输出目录
	ResultDir string
	// redis-full-check 二进制文件路径
	BinaryPath string
	// 批量大小
	BatchSize int
	// 超时时间（秒）
	Timeout int
	// Key 过滤列表（支持前缀匹配，例如："user:*|session:*|cache:product:*"）
	FilterList string
	// 对比轮次（默认 3 轮，多轮对比可减少误报）
	CompareTimes int
	// 每轮对比的时间间隔（秒）
	Interval int
	// 大 key 阈值（字节数，仅在 smart 模式下生效）
	BigKeyThreshold int
	// 日志文件路径
	LogFile string
	// 日志级别（debug/info/warn/error）
	LogLevel string
}

// Result 校验结果
type Result struct {
	// 总 key 数量
	TotalKeys int64
	// 一致的 key 数量
	ConsistentKeys int64
	// 不一致的 key 数量
	InconsistentKeys int64
	// 源端独有的 key 数量
	SourceOnlyKeys int64
	// 目标端独有的 key 数量
	TargetOnlyKeys int64
	// 校验耗时
	Duration time.Duration
	// 结果文件路径
	ResultFile string
	// 不一致的 key 列表（最多前 100 个）
	InconsistentSamples []string
}

// Checker redis-full-check 封装
type Checker struct {
	config Config
}

// NewChecker 创建 Checker 实例
func NewChecker(config Config) *Checker {
	// 设置默认值
	if config.Mode == "" {
		config.Mode = ModeKeyOutline
	}
	if config.QPS <= 0 {
		config.QPS = 500
	}
	if config.Parallel <= 0 {
		config.Parallel = 4
	}
	if config.ResultDir == "" {
		config.ResultDir = "./check-results"
	}
	if config.BinaryPath == "" {
		config.BinaryPath = "redis-full-check"
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 256 // redis-full-check 默认值
	}
	if config.Timeout <= 0 {
		config.Timeout = 3600 // 默认 1 小时
	}
	if config.CompareTimes <= 0 {
		config.CompareTimes = 3 // 默认 3 轮对比
	}
	if config.Interval <= 0 {
		config.Interval = 5 // 默认间隔 5 秒
	}
	if config.BigKeyThreshold <= 0 {
		config.BigKeyThreshold = 524288 // 默认 512KB
	}
	if config.LogLevel == "" {
		config.LogLevel = "info"
	}

	return &Checker{config: config}
}

// Run 执行数据一致性校验
func (c *Checker) Run(ctx context.Context) (*Result, error) {
	startTime := time.Now()

	// 确保结果目录存在
	if err := os.MkdirAll(c.config.ResultDir, 0755); err != nil {
		return nil, fmt.Errorf("创建结果目录失败: %w", err)
	}

	// 生成结果文件路径
	timestamp := time.Now().Format("20060102_150405")
	resultFile := filepath.Join(c.config.ResultDir, fmt.Sprintf("check_%s.json", timestamp))

	// 构建命令参数
	args := c.buildArgs(resultFile)

	// 打印执行信息
	fmt.Printf("🔍 开始数据一致性校验\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  • 校验模式: %s\n", c.getModeDescription())
	fmt.Printf("  • 源端地址: %s\n", c.maskAddr(c.config.SourceAddr))
	fmt.Printf("  • 目标地址: %s\n", c.maskAddr(c.config.TargetAddr))
	fmt.Printf("  • QPS 限制: %d\n", c.config.QPS)
	fmt.Printf("  • 并发度: %d\n", c.config.Parallel)
	fmt.Printf("  • 结果文件: %s\n", resultFile)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// 创建带超时的 context
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(c.config.Timeout)*time.Second)
	defer cancel()

	// 执行 redis-full-check
	cmd := exec.CommandContext(cmdCtx, c.config.BinaryPath, args...)

	// 捕获输出以显示进度
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout pipe 失败: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stderr pipe 失败: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 redis-full-check 失败: %w", err)
	}

	// 实时显示输出
	go c.streamOutput(stdout, "INFO")
	go c.streamOutput(stderr, "ERROR")

	// 等待命令完成
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("redis-full-check 执行失败: %w", err)
	}

	duration := time.Since(startTime)

	fmt.Printf("\n✓ 校验完成，耗时: %s\n\n", duration.Round(time.Second))

	// 解析结果文件
	result, err := c.parseResultFile(resultFile)
	if err != nil {
		return nil, fmt.Errorf("解析结果文件失败: %w", err)
	}

	result.Duration = duration
	result.ResultFile = resultFile

	return result, nil
}

// buildArgs 构建 redis-full-check 命令行参数
func (c *Checker) buildArgs(resultFile string) []string {
	args := []string{
		"-s", c.config.SourceAddr,
		"-t", c.config.TargetAddr,
		"-m", c.getCompareMode(),
		"--qps", fmt.Sprintf("%d", c.config.QPS),
		"--parallel", fmt.Sprintf("%d", c.config.Parallel),
		"--batchcount", fmt.Sprintf("%d", c.config.BatchSize), // 正确的参数名
		"--result", resultFile,
		"--comparetimes", fmt.Sprintf("%d", c.config.CompareTimes),
		"--interval", fmt.Sprintf("%d", c.config.Interval),
		"--loglevel", c.config.LogLevel,
	}

	// 添加源端密码
	if c.config.SourcePassword != "" {
		args = append(args, "-p", c.config.SourcePassword)
	}

	// 添加目标端密码（使用正确的参数名）
	if c.config.TargetPassword != "" {
		args = append(args, "-a", c.config.TargetPassword) // 正确：使用 -a
	}

	// 添加 key 过滤列表
	if c.config.FilterList != "" {
		args = append(args, "-f", c.config.FilterList)
	}

	// 添加大 key 阈值（仅在 smart 模式下）
	if c.config.Mode == ModeSmartBigKey && c.config.BigKeyThreshold > 0 {
		args = append(args, "--bigkeythreshold", fmt.Sprintf("%d", c.config.BigKeyThreshold))
	}

	// 添加日志文件
	if c.config.LogFile != "" {
		args = append(args, "--log", c.config.LogFile)
	}

	return args
}

// getCompareMode 获取 redis-full-check 的对比模式参数
func (c *Checker) getCompareMode() string {
	switch c.config.Mode {
	case ModeFullValue:
		return "1" // 全量值对比
	case ModeValueLength:
		return "2" // 值长度对比
	case ModeKeyOutline:
		return "3" // 键轮廓对比
	case ModeSmartBigKey:
		return "4" // 智能对比（遇到大 key 只对比长度）
	default:
		return "3" // 默认使用 outline 模式
	}
}

// getModeDescription 获取模式描述
func (c *Checker) getModeDescription() string {
	switch c.config.Mode {
	case ModeFullValue:
		return "全量值对比 (完整对比)"
	case ModeValueLength:
		return "值长度对比 (快速对比)"
	case ModeKeyOutline:
		return "键轮廓对比 (元信息对比)"
	case ModeSmartBigKey:
		return fmt.Sprintf("智能对比 (大key阈值: %dKB)", c.config.BigKeyThreshold/1024)
	default:
		return string(c.config.Mode)
	}
}

// maskAddr 脱敏地址信息
func (c *Checker) maskAddr(addr string) string {
	// 保留 IP 地址的前两段和端口
	parts := strings.Split(addr, ":")
	if len(parts) == 2 {
		ipParts := strings.Split(parts[0], ".")
		if len(ipParts) == 4 {
			return fmt.Sprintf("%s.%s.x.x:%s", ipParts[0], ipParts[1], parts[1])
		}
	}
	return addr
}

// streamOutput 流式输出日志
func (c *Checker) streamOutput(reader io.Reader, prefix string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		// 过滤掉一些冗余的输出
		if strings.Contains(line, "scan") ||
			strings.Contains(line, "compare") ||
			strings.Contains(line, "finish") {
			fmt.Printf("  [%s] %s\n", prefix, line)
		}
	}
}

// parseResultFile 解析 redis-full-check 的结果文件
func (c *Checker) parseResultFile(resultFile string) (*Result, error) {
	// redis-full-check 的结果是 JSON Lines 格式
	// 每一行是一个不一致的 key 的详细信息

	file, err := os.Open(resultFile)
	if err != nil {
		return nil, fmt.Errorf("打开结果文件失败: %w", err)
	}
	defer file.Close()

	result := &Result{
		InconsistentSamples: make([]string, 0),
	}

	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Bytes()

		// 跳过空行
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// 解析 JSON
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // 跳过无法解析的行
		}

		// 提取 key
		if key, ok := entry["key"].(string); ok && lineCount <= 100 {
			result.InconsistentSamples = append(result.InconsistentSamples, key)
		}

		result.InconsistentKeys++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取结果文件失败: %w", err)
	}

	return result, nil
}

// PrintResult 打印校验结果
func (c *Checker) PrintResult(result *Result) {
	fmt.Printf("📊 校验结果汇总\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  • 校验耗时: %s\n", result.Duration.Round(time.Second))
	fmt.Printf("  • 不一致 key 数量: %d\n", result.InconsistentKeys)

	if result.InconsistentKeys == 0 {
		fmt.Printf("\n✓ 数据完全一致！\n")
	} else {
		fmt.Printf("\n⚠ 发现数据不一致\n")
		fmt.Printf("  • 结果文件: %s\n", result.ResultFile)

		if len(result.InconsistentSamples) > 0 {
			fmt.Printf("\n  不一致的 key 样本（前 %d 个）:\n", len(result.InconsistentSamples))
			for i, key := range result.InconsistentSamples {
				if i >= 10 {
					fmt.Printf("    ... 更多 key 请查看结果文件\n")
					break
				}
				fmt.Printf("    %d. %s\n", i+1, key)
			}
		}
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}
