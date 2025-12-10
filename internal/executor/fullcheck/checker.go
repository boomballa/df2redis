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

// CheckConfig validation configuration
type CheckConfig struct {
	Binary       string // redis-full-check binary path
	SourceAddr   string // Source address
	SourcePass   string // Source password
	TargetAddr   string // Target address
	TargetPass   string // Target password
	CompareMode  int    // Compare mode: 1=full value, 2=length, 3=key outline, 4=smart
	CompareTimes int    // Number of compare rounds
	QPS          int    // QPS limit
	BatchCount   int    // Batch size
	Parallel     int    // Parallel workers
	ResultDB     string // SQLite result file path
	ResultFile   string // Text result file path
}

// Progress validation progress information
type Progress struct {
	Round          int     // Current round
	CompareTimes   int     // Total rounds
	TotalKeys      int64   // Total keys
	CheckedKeys    int64   // Checked keys
	ConsistentKeys int64   // Consistent keys
	ConflictKeys   int64   // Conflict keys
	MissingKeys    int64   // Missing keys
	ErrorCount     int64   // Error count
	Progress       float64 // Progress percentage
	Message        string  // Status message
}

// Checker redis-full-check executor
type Checker struct {
	config     CheckConfig
	progressCh chan<- Progress
	mu         sync.Mutex
	stats      Progress
}

// NewChecker creates a new validation checker
func NewChecker(config CheckConfig, progressCh chan<- Progress) *Checker {
	return &Checker{
		config:     config,
		progressCh: progressCh,
	}
}

// Run executes validation
func (c *Checker) Run(ctx context.Context) error {
	args := c.buildArgs()

	log.Printf("[redis-full-check] Starting validation: %s %v", c.config.Binary, args)

	cmd := exec.CommandContext(ctx, c.config.Binary, args...)

	// Capture output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start redis-full-check: %w", err)
	}

	// Parse output in parallel
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

	// Wait for process to exit
	errChan := make(chan error, 1)
	go func() {
		errChan <- cmd.Wait()
	}()

	// Wait for output parsing to complete
	wg.Wait()

	// Check process exit status
	if err := <-errChan; err != nil {
		return fmt.Errorf("redis-full-check execution failed: %w", err)
	}

	log.Println("[redis-full-check] Validation completed")
	return nil
}

// buildArgs builds command line arguments
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
		"--metric", // Output metrics
	}

	// Add passwords if provided
	if c.config.SourcePass != "" {
		args = append(args, "--sourcepassword", c.config.SourcePass)
	}
	if c.config.TargetPass != "" {
		args = append(args, "--targetpassword", c.config.TargetPass)
	}

	return args
}

// parseOutput parses output and updates progress
func (c *Checker) parseOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)

	// Regular expressions - match various possible formats
	roundRE := regexp.MustCompile(`start (\d+)(?:th|st|nd|rd) time compare`)
	keyScanRE := regexp.MustCompile(`KeyScan[:\s]+(\d+)[/\s]+(\d+)`)
	scanProgressRE := regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)  // Generic progress format
	conflictRE := regexp.MustCompile(`(\d+)\s+key\(s\)\s+conflict`)
	missingRE := regexp.MustCompile(`(\d+)\s+key\(s\)\s+lack`)

	for scanner.Scan() {
		line := scanner.Text()

		// Output to log
		log.Printf("[redis-full-check] %s", line)

		// Parse round number
		if matches := roundRE.FindStringSubmatch(line); len(matches) > 1 {
			if round, err := strconv.Atoi(matches[1]); err == nil {
				log.Printf("[redis-full-check] Detected Round: %d", round)
				c.updateProgress(func(p *Progress) {
					p.Round = round
					p.Message = fmt.Sprintf("Round %d: Scanning keys...", round)
					// Reset progress when new round starts (but keep previous statistics)
					p.CheckedKeys = 0
					p.Progress = 0
				})
			}
		}

		// Parse scan progress - prioritize KeyScan format
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
			// Try generic progress format
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

		// Parse conflict statistics
		if matches := conflictRE.FindStringSubmatch(line); len(matches) > 1 {
			if conflicts, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				log.Printf("[redis-full-check] Conflicts: %d", conflicts)
				c.updateProgress(func(p *Progress) {
					p.ConflictKeys = conflicts
				})
			}
		}

		// Parse missing statistics
		if matches := missingRE.FindStringSubmatch(line); len(matches) > 1 {
			if missing, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				log.Printf("[redis-full-check] Missing: %d", missing)
				c.updateProgress(func(p *Progress) {
					p.MissingKeys = missing
				})
			}
		}

		// Detect completion (only detect explicit completion signal to avoid misjudging end of each round)
		if strings.Contains(line, "all finish") {
			log.Println("[redis-full-check] Detected completion: all finish")
			c.updateProgress(func(p *Progress) {
				p.Progress = 1.0
				p.Message = "Validation completed"
			})
		}
	}
}

// updateProgress thread-safe progress update
func (c *Checker) updateProgress(fn func(*Progress)) {
	c.mu.Lock()
	fn(&c.stats)
	// Ensure CompareTimes is always set
	c.stats.CompareTimes = c.config.CompareTimes
	stats := c.stats
	c.mu.Unlock()

	// Send progress update
	if c.progressCh != nil {
		select {
		case c.progressCh <- stats:
		default:
			// Non-blocking
		}
	}
}

// ParseResultFile parses text result file
func ParseResultFile(path string) (*CheckSummary, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	// TODO: Implement text file parsing
	// Format: db\tdiff-type\tkey\tfield

	return &CheckSummary{
		FilePath: absPath,
	}, nil
}

// CheckSummary validation summary
type CheckSummary struct {
	FilePath       string
	TotalConflicts int64
	TypeMismatch   int64
	ValueMismatch  int64
	MissingKeys    int64
}
