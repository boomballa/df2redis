package fullcheck

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// CheckConfig 校验配置
type CheckConfig struct {
	Binary       string // redis-full-check 二进制路径
	SourceAddr   string // 源地址
	SourcePass   string // 源密码
	TargetAddr   string // 目标地址
	TargetPass   string // 目标密码
	CompareMode  int    // 比较模式：1=全值, 2=长度, 3=Key轮廓, 4=智能
	CompareTimes int    // 比较轮次
	QPS          int    // QPS 限制
	BatchCount   int    // 批量大小
	Parallel     int    // 并发度
	ResultDB     string // SQLite 结果文件路径
	ResultFile   string // 文本结果文件路径
}

// Progress 进度信息
type Progress struct {
	Round          int     // 当前轮次
	TotalKeys      int64   // 总 key 数
	CheckedKeys    int64   // 已检查 key 数
	ConsistentKeys int64   // 一致 key 数
	ConflictKeys   int64   // 冲突 key 数
	MissingKeys    int64   // 缺失 key 数
	ErrorCount     int64   // 错误数
	Progress       float64 // 进度百分比
	Message        string  // 状态消息
}

// Checker redis-full-check 执行器
type Checker struct {
	config     CheckConfig
	progressCh chan<- Progress
	mu         sync.Mutex
	stats      Progress
}

// NewChecker 创建校验器
func NewChecker(config CheckConfig, progressCh chan<- Progress) *Checker {
	return &Checker{
		config:     config,
		progressCh: progressCh,
	}
}

// Run 执行校验
func (c *Checker) Run(ctx context.Context) error {
	args := c.buildArgs()

	log.Printf("[redis-full-check] 启动校验: %s %v", c.config.Binary, args)

	cmd := exec.CommandContext(ctx, c.config.Binary, args...)

	// 捕获输出
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	// 启动进程
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 redis-full-check 失败: %w", err)
	}

	// 并行解析输出
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		c.parseOutput(stdout)
	}()

	go func() {
		defer wg.Done()
		c.parseOutput(stderr)
	}()

	// 等待进程结束
	errChan := make(chan error, 1)
	go func() {
		errChan <- cmd.Wait()
	}()

	// 等待输出解析完成
	wg.Wait()

	// 检查进程退出状态
	if err := <-errChan; err != nil {
		return fmt.Errorf("redis-full-check 执行失败: %w", err)
	}

	log.Println("[redis-full-check] 校验完成")
	return nil
}

// buildArgs 构建命令行参数
func (c *Checker) buildArgs() []string {
	args := []string{
		"--source", c.config.SourceAddr,
		"--target", c.config.TargetAddr,
		"--comparemode", strconv.Itoa(c.config.CompareMode),
		"--comparetimes", strconv.Itoa(c.config.CompareTimes),
		"--qps", strconv.Itoa(c.config.QPS),
		"--batchcount", strconv.Itoa(c.config.BatchCount),
		"--parallel", strconv.Itoa(c.config.Parallel),
		"--db", c.config.ResultDB,
		"--result", c.config.ResultFile,
		"--metric", // 输出指标
	}

	// 添加密码（如果有）
	if c.config.SourcePass != "" {
		args = append(args, "--sourcepassword", c.config.SourcePass)
	}
	if c.config.TargetPass != "" {
		args = append(args, "--targetpassword", c.config.TargetPass)
	}

	return args
}

// parseOutput 解析输出并更新进度
func (c *Checker) parseOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)

	// 正则表达式 - 匹配多种可能的格式
	roundRE := regexp.MustCompile(`start (\d+)(?:th|st|nd|rd) time compare`)
	keyScanRE := regexp.MustCompile(`KeyScan[:\s]+(\d+)[/\s]+(\d+)`)
	scanProgressRE := regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)  // 通用进度格式
	conflictRE := regexp.MustCompile(`(\d+)\s+key\(s\)\s+conflict`)
	missingRE := regexp.MustCompile(`(\d+)\s+key\(s\)\s+lack`)

	for scanner.Scan() {
		line := scanner.Text()

		// 输出到日志
		log.Printf("[redis-full-check] %s", line)

		// 解析轮次
		if matches := roundRE.FindStringSubmatch(line); len(matches) > 1 {
			if round, err := strconv.Atoi(matches[1]); err == nil {
				log.Printf("[redis-full-check] Detected Round: %d", round)
				c.updateProgress(func(p *Progress) {
					p.Round = round
					p.Message = fmt.Sprintf("Round %d: Scanning keys...", round)
				})
			}
		}

		// 解析扫描进度 - 优先匹配 KeyScan 格式
		if matches := keyScanRE.FindStringSubmatch(line); len(matches) > 2 {
			checked, _ := strconv.ParseInt(matches[1], 10, 64)
			total, _ := strconv.ParseInt(matches[2], 10, 64)

			log.Printf("[redis-full-check] Progress: %d/%d", checked, total)
			c.updateProgress(func(p *Progress) {
				p.CheckedKeys = checked
				p.TotalKeys = total
				if total > 0 {
					p.Progress = float64(checked) / float64(total)
				}
				p.Message = fmt.Sprintf("Scanned %d/%d keys", checked, total)
			})
		} else if strings.Contains(line, "scan") || strings.Contains(line, "Scan") {
			// 尝试通用进度格式
			if matches := scanProgressRE.FindStringSubmatch(line); len(matches) > 2 {
				checked, _ := strconv.ParseInt(matches[1], 10, 64)
				total, _ := strconv.ParseInt(matches[2], 10, 64)

				if total > 0 && checked <= total {
					log.Printf("[redis-full-check] Generic progress: %d/%d", checked, total)
					c.updateProgress(func(p *Progress) {
						p.CheckedKeys = checked
						p.TotalKeys = total
						p.Progress = float64(checked) / float64(total)
						p.Message = fmt.Sprintf("Scanned %d/%d keys", checked, total)
					})
				}
			}
		}

		// 解析冲突统计
		if matches := conflictRE.FindStringSubmatch(line); len(matches) > 1 {
			if conflicts, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				log.Printf("[redis-full-check] Conflicts: %d", conflicts)
				c.updateProgress(func(p *Progress) {
					p.ConflictKeys = conflicts
				})
			}
		}

		// 解析缺失统计
		if matches := missingRE.FindStringSubmatch(line); len(matches) > 1 {
			if missing, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				log.Printf("[redis-full-check] Missing: %d", missing)
				c.updateProgress(func(p *Progress) {
					p.MissingKeys = missing
				})
			}
		}

		// 检测完成
		if strings.Contains(line, "all finish") || strings.Contains(line, "finish") {
			log.Println("[redis-full-check] Detected completion")
			c.updateProgress(func(p *Progress) {
				p.Progress = 1.0
				p.Message = "Validation completed"
			})
		}
	}
}

// updateProgress 线程安全地更新进度
func (c *Checker) updateProgress(fn func(*Progress)) {
	c.mu.Lock()
	fn(&c.stats)
	stats := c.stats
	c.mu.Unlock()

	// 发送进度更新
	if c.progressCh != nil {
		select {
		case c.progressCh <- stats:
		default:
			// 不阻塞
		}
	}
}

// ParseResultFile 解析文本结果文件
func ParseResultFile(path string) (*CheckSummary, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	// TODO: 实现文本文件解析
	// 格式：db\tdiff-type\tkey\tfield

	return &CheckSummary{
		FilePath: absPath,
	}, nil
}

// CheckSummary 校验摘要
type CheckSummary struct {
	FilePath       string
	TotalConflicts int64
	TypeMismatch   int64
	ValueMismatch  int64
	MissingKeys    int64
}
