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

// CheckMode defines the validation mode
type CheckMode string

const (
	// ModeFullValue performs full value comparison (complete comparison of all fields and values)
	ModeFullValue CheckMode = "full"
	// ModeKeyOutline performs key outline comparison (compares key existence, type, TTL, length, etc.)
	ModeKeyOutline CheckMode = "outline"
	// ModeValueLength performs value length comparison (only compares value length)
	ModeValueLength CheckMode = "length"
	// ModeSmartBigKey performs smart comparison (length-only for big keys, full comparison otherwise)
	ModeSmartBigKey CheckMode = "smart"
)

// Config holds validation configuration
type Config struct {
	// Source Redis address
	SourceAddr string
	// Source Redis password
	SourcePassword string
	// Target Redis address
	TargetAddr string
	// Target Redis password
	TargetPassword string
	// Validation mode
	Mode CheckMode
	// QPS limit (0 = unlimited)
	QPS int
	// Parallelism level
	Parallel int
	// Result output directory
	ResultDir string
	// Path to redis-full-check binary
	BinaryPath string
	// Batch size
	BatchSize int
	// Timeout in seconds
	Timeout int
	// Key filter list (supports prefix matching, e.g., "user:*|session:*|cache:product:*")
	FilterList string
	// Number of comparison rounds (default: 3, multiple rounds reduce false positives)
	CompareTimes int
	// Interval between comparison rounds in seconds
	Interval int
	// Big key threshold in bytes (only effective in smart mode)
	BigKeyThreshold int
	// Log file path
	LogFile string
	// Log level (debug/info/warn/error)
	LogLevel string
	// Maximum number of keys to validate (0 = unlimited)
	MaxKeys int
	// Task name (used for result file naming)
	TaskName string
}

// Result holds validation results
type Result struct {
	// Total number of keys
	TotalKeys int64
	// Number of consistent keys
	ConsistentKeys int64
	// Number of inconsistent keys
	InconsistentKeys int64
	// Number of keys only present in source
	SourceOnlyKeys int64
	// Number of keys only present in target
	TargetOnlyKeys int64
	// Validation duration
	Duration time.Duration
	// Result file path
	ResultFile string
	// List of inconsistent keys (up to 100 samples)
	InconsistentSamples []string
}

// Checker wraps redis-full-check functionality
type Checker struct {
	config Config
}

// NewChecker creates a new Checker instance
func NewChecker(config Config) *Checker {
	// Set default values
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
		config.BatchSize = 256 // redis-full-check default value
	}
	if config.Timeout <= 0 {
		config.Timeout = 3600 // default: 1 hour
	}
	if config.CompareTimes <= 0 {
		config.CompareTimes = 3 // default: 3 rounds
	}
	if config.Interval <= 0 {
		config.Interval = 5 // default: 5 seconds
	}
	if config.BigKeyThreshold <= 0 {
		config.BigKeyThreshold = 524288 // default: 512KB
	}
	if config.LogLevel == "" {
		config.LogLevel = "info"
	}

	return &Checker{config: config}
}

// Run executes data consistency validation
func (c *Checker) Run(ctx context.Context) (*Result, error) {
	startTime := time.Now()

	// Ensure result directory exists
	if err := os.MkdirAll(c.config.ResultDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create result directory: %w", err)
	}

	// Generate result file paths (smart naming)
	timestamp := time.Now().Format("20060102_150405")
	filePrefix := c.generateFilePrefix()
	resultFile := filepath.Join(c.config.ResultDir, fmt.Sprintf("%s_check_%s.json", filePrefix, timestamp))
	summaryFile := filepath.Join(c.config.ResultDir, fmt.Sprintf("%s_check_%s_summary.txt", filePrefix, timestamp))

	// Build command arguments
	args := c.buildArgs(resultFile)

	// Print execution info
	fmt.Printf("🔍 Starting Data Consistency Check\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  • Check Mode: %s\n", c.getModeDescription())
	fmt.Printf("  • Source: %s\n", c.maskAddr(c.config.SourceAddr))
	fmt.Printf("  • Target: %s\n", c.maskAddr(c.config.TargetAddr))
	fmt.Printf("  • QPS Limit: %d\n", c.config.QPS)
	fmt.Printf("  • Parallelism: %d\n", c.config.Parallel)
	fmt.Printf("  • Result File: %s\n", resultFile)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(c.config.Timeout)*time.Second)
	defer cancel()

	// Execute redis-full-check
	cmd := exec.CommandContext(cmdCtx, c.config.BinaryPath, args...)

	// Capture output to display progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start redis-full-check: %w", err)
	}

	// Stream output in real-time
	go c.streamOutput(stdout, "INFO")
	go c.streamOutput(stderr, "ERROR")

	// Wait for command completion
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("redis-full-check execution failed: %w", err)
	}

	duration := time.Since(startTime)

	fmt.Printf("\n✓ Check completed, duration: %s\n\n", duration.Round(time.Second))

	// Parse result file
	result, err := c.parseResultFile(resultFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse result file: %w", err)
	}

	result.Duration = duration
	result.ResultFile = resultFile

	// Generate human-readable summary file
	if err := c.writeSummaryFile(summaryFile, result); err != nil {
		fmt.Printf("⚠️  Warning: Failed to generate summary file: %v\n", err)
		// Does not affect overall result, continue
	}

	return result, nil
}

// buildArgs builds redis-full-check command line arguments
func (c *Checker) buildArgs(resultFile string) []string {
	args := []string{
		"-s", c.config.SourceAddr,
		"-t", c.config.TargetAddr,
		"-m", c.getCompareMode(),
		"--qps", fmt.Sprintf("%d", c.config.QPS),
		"--parallel", fmt.Sprintf("%d", c.config.Parallel),
		"--batchcount", fmt.Sprintf("%d", c.config.BatchSize), // correct parameter name
		"--result", resultFile,
		"--comparetimes", fmt.Sprintf("%d", c.config.CompareTimes),
		"--interval", fmt.Sprintf("%d", c.config.Interval),
		"--loglevel", c.config.LogLevel,
	}

	// Add source password
	if c.config.SourcePassword != "" {
		args = append(args, "-p", c.config.SourcePassword)
	}

	// Add target password (using correct parameter name)
	if c.config.TargetPassword != "" {
		args = append(args, "-a", c.config.TargetPassword) // correct: use -a
	}

	// Add key filter list
	if c.config.FilterList != "" {
		args = append(args, "-f", c.config.FilterList)
	}

	// Add big key threshold (only in smart mode)
	if c.config.Mode == ModeSmartBigKey && c.config.BigKeyThreshold > 0 {
		args = append(args, "--bigkeythreshold", fmt.Sprintf("%d", c.config.BigKeyThreshold))
	}

	// Add log file
	if c.config.LogFile != "" {
		args = append(args, "--log", c.config.LogFile)
	}

	// Add maximum key count limit
	if c.config.MaxKeys > 0 {
		args = append(args, "--maxkeys", fmt.Sprintf("%d", c.config.MaxKeys))
	}

	return args
}

// getCompareMode returns redis-full-check comparison mode parameter
func (c *Checker) getCompareMode() string {
	switch c.config.Mode {
	case ModeFullValue:
		return "1" // full value comparison
	case ModeValueLength:
		return "2" // value length comparison
	case ModeKeyOutline:
		return "3" // key outline comparison
	case ModeSmartBigKey:
		return "4" // smart comparison (length-only for big keys)
	default:
		return "3" // default: outline mode
	}
}

// getModeDescription returns mode description
func (c *Checker) getModeDescription() string {
	switch c.config.Mode {
	case ModeFullValue:
		return "Full Value (complete comparison)"
	case ModeValueLength:
		return "Value Length (fast comparison)"
	case ModeKeyOutline:
		return "Key Outline (metadata comparison)"
	case ModeSmartBigKey:
		return fmt.Sprintf("Smart (big key threshold: %dKB)", c.config.BigKeyThreshold/1024)
	default:
		return string(c.config.Mode)
	}
}

// maskAddr masks sensitive address information
func (c *Checker) maskAddr(addr string) string {
	// Preserve first two segments of IP and port
	parts := strings.Split(addr, ":")
	if len(parts) == 2 {
		ipParts := strings.Split(parts[0], ".")
		if len(ipParts) == 4 {
			return fmt.Sprintf("%s.%s.x.x:%s", ipParts[0], ipParts[1], parts[1])
		}
	}
	return addr
}

// streamOutput streams log output in real-time
func (c *Checker) streamOutput(reader io.Reader, prefix string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		// Filter out redundant output
		if strings.Contains(line, "scan") ||
			strings.Contains(line, "compare") ||
			strings.Contains(line, "finish") {
			fmt.Printf("  [%s] %s\n", prefix, line)
		}
	}
}

// parseResultFile parses redis-full-check result file
func (c *Checker) parseResultFile(resultFile string) (*Result, error) {
	// redis-full-check outputs in JSON Lines format
	// Each line contains details of an inconsistent key

	file, err := os.Open(resultFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open result file: %w", err)
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

		// Skip empty lines
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// Parse JSON
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip unparseable lines
		}

		// Extract key
		if key, ok := entry["key"].(string); ok && lineCount <= 100 {
			result.InconsistentSamples = append(result.InconsistentSamples, key)
		}

		result.InconsistentKeys++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read result file: %w", err)
	}

	return result, nil
}

// PrintResult prints check results
func (c *Checker) PrintResult(result *Result) {
	fmt.Printf("📊 Check Result Summary\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  • Duration: %s\n", result.Duration.Round(time.Second))
	fmt.Printf("  • Inconsistent Keys: %d\n", result.InconsistentKeys)

	if result.InconsistentKeys == 0 {
		fmt.Printf("\n✓ Data is fully consistent!\n")
	} else {
		fmt.Printf("\n⚠  Data inconsistency detected\n")
		fmt.Printf("  • Result File: %s\n", result.ResultFile)

		if len(result.InconsistentSamples) > 0 {
			fmt.Printf("\n  Inconsistent Key Samples (first %d):\n", len(result.InconsistentSamples))
			for i, key := range result.InconsistentSamples {
				if i >= 10 {
					fmt.Printf("    ... see result file for more keys\n")
					break
				}
				fmt.Printf("    %d. %s\n", i+1, key)
			}
		}
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

// generateFilePrefix generates file name prefix
func (c *Checker) generateFilePrefix() string {
	// Use task name if specified
	if c.config.TaskName != "" {
		return c.config.TaskName
	}

	// Otherwise use source IP and port
	addr := c.config.SourceAddr
	// Replace ":" with "_" and "." with "_"
	prefix := strings.ReplaceAll(addr, ":", "_")
	prefix = strings.ReplaceAll(prefix, ".", "_")
	return prefix
}

// writeSummaryFile generates human-readable summary file
func (c *Checker) writeSummaryFile(summaryFile string, result *Result) error {
	file, err := os.Create(summaryFile)
	if err != nil {
		return fmt.Errorf("failed to create summary file: %w", err)
	}
	defer file.Close()

	// Write summary information
	fmt.Fprintf(file, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(file, "      Data Consistency Check Summary\n")
	fmt.Fprintf(file, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	fmt.Fprintf(file, "【Basic Information】\n")
	fmt.Fprintf(file, "  • Check Time: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "  • Check Mode: %s\n", c.getModeDescription())
	fmt.Fprintf(file, "  • Source: %s\n", c.config.SourceAddr)
	fmt.Fprintf(file, "  • Target: %s\n", c.config.TargetAddr)
	if c.config.MaxKeys > 0 {
		fmt.Fprintf(file, "  • Max Keys: %d keys\n", c.config.MaxKeys)
	}
	fmt.Fprintf(file, "\n")

	fmt.Fprintf(file, "【Check Configuration】\n")
	fmt.Fprintf(file, "  • QPS Limit: %d\n", c.config.QPS)
	fmt.Fprintf(file, "  • Parallelism: %d\n", c.config.Parallel)
	fmt.Fprintf(file, "  • Compare Times: %d rounds\n", c.config.CompareTimes)
	fmt.Fprintf(file, "  • Interval: %d seconds\n", c.config.Interval)
	if c.config.FilterList != "" {
		fmt.Fprintf(file, "  • Key Filter: %s\n", c.config.FilterList)
	}
	fmt.Fprintf(file, "\n")

	fmt.Fprintf(file, "【Check Results】\n")
	fmt.Fprintf(file, "  • Duration: %s\n", result.Duration.Round(time.Second))
	fmt.Fprintf(file, "  • Inconsistent Keys: %d\n", result.InconsistentKeys)
	fmt.Fprintf(file, "\n")

	if result.InconsistentKeys == 0 {
		fmt.Fprintf(file, "【Conclusion】\n")
		fmt.Fprintf(file, "  ✓ Data is fully consistent!\n\n")
	} else {
		fmt.Fprintf(file, "【Inconsistent Samples】\n")
		if len(result.InconsistentSamples) > 0 {
			sampleCount := len(result.InconsistentSamples)
			if sampleCount > 20 {
				sampleCount = 20
			}
			fmt.Fprintf(file, "  First %d inconsistent keys:\n\n", sampleCount)
			for i := 0; i < sampleCount; i++ {
				fmt.Fprintf(file, "    %d. %s\n", i+1, result.InconsistentSamples[i])
			}
			if len(result.InconsistentSamples) > 20 {
				fmt.Fprintf(file, "\n    ... see JSON result file for more keys\n")
			}
		}
		fmt.Fprintf(file, "\n")

		fmt.Fprintf(file, "【Conclusion】\n")
		fmt.Fprintf(file, "  ⚠️  Data inconsistency detected, please check detailed result file\n\n")
	}

	fmt.Fprintf(file, "【Detailed Result Files】\n")
	fmt.Fprintf(file, "  • JSON File: %s\n", result.ResultFile)
	fmt.Fprintf(file, "  • Summary File: %s\n", summaryFile)
	fmt.Fprintf(file, "\n")

	fmt.Fprintf(file, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return nil
}
