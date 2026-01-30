package checker

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"df2redis/internal/redisx"
)

// CheckMode defines the validation mode
type CheckMode string

const (
	// ModeFullValue performs full value comparison
	ModeFullValue CheckMode = "full"
	// ModeKeyOutline performs key outline comparison (type, ttl, existence)
	ModeKeyOutline CheckMode = "outline"
	// ModeValueLength performs value length comparison
	ModeValueLength CheckMode = "length"
	// ModeSmartBigKey performs smart comparison (length-only for big keys)
	ModeSmartBigKey CheckMode = "smart"
)

// Config holds validation configuration
type Config struct {
	SourceAddr      string
	SourcePassword  string
	TargetAddr      string
	TargetPassword  string
	Mode            CheckMode
	QPS             int
	Parallel        int
	ResultDir       string
	BatchSize       int
	Timeout         int
	FilterList      string
	CompareTimes    int
	Interval        int
	BigKeyThreshold int
	LogFile         string
	LogLevel        string
	MaxKeys         int
	TaskName        string
}

// Result holds validation results
type Result struct {
	TotalKeys           int64
	ConsistentKeys      int64
	InconsistentKeys    int64
	MissingKeys         int64
	Duration            time.Duration
	ResultFile          string
	InconsistentSamples []string
}

// Progress indicates the current progress of the check
type Progress struct {
	Round            int
	TotalKeys        int64
	CheckedKeys      int64
	ConsistentKeys   int64
	InconsistentKeys int64
	MissingKeys      int64
	ErrorCount       int64
	Message          string
	Progress         float64 // 0.0 to 1.0
}

// Checker implements native data consistency check
type Checker struct {
	config Config
}

// NewChecker creates a new Checker instance
func NewChecker(config Config) *Checker {
	// Defaults
	if config.Mode == "" {
		config.Mode = ModeKeyOutline
	}
	if config.Parallel <= 0 {
		config.Parallel = 4
	}
	if config.ResultDir == "" {
		config.ResultDir = "./check-results"
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 1000
	}
	if config.Timeout <= 0 {
		config.Timeout = 3600
	}
	if config.BigKeyThreshold <= 0 {
		config.BigKeyThreshold = 5000
	}
	return &Checker{config: config}
}

// Run executes data consistency validation
func (c *Checker) Run(ctx context.Context, progressCh chan<- Progress) (*Result, error) {
	// Create result directory
	if err := os.MkdirAll(c.config.ResultDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create result directory: %w", err)
	}

	result := &Result{
		InconsistentSamples: make([]string, 0),
	}
	startTime := time.Now()

	log.Printf("🚀 Starting native check (Mode: %s, Parallel: %d)", c.config.Mode, c.config.Parallel)

	// Connect to Source and Target
	src, err := redisx.Dial(ctx, redisx.Config{Addr: c.config.SourceAddr, Password: c.config.SourcePassword})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to source: %w", err)
	}
	defer src.Close()

	tgt, err := redisx.Dial(ctx, redisx.Config{Addr: c.config.TargetAddr, Password: c.config.TargetPassword})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to target: %w", err)
	}
	defer tgt.Close()

	// Channels for pipeline
	keyChan := make(chan string, c.config.BatchSize*2)

	// Start Scanner
	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		defer close(keyChan)
		c.scanSource(ctx, src, keyChan)
	}()

	// Start Workers
	var workerWg sync.WaitGroup
	var inconsistenciesMutex sync.Mutex

	for i := 0; i < c.config.Parallel; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			// Create dedicated clients for workers if needed or reuse if client is thread-safe (redisx is likely thread-safe if it uses go-redis)
			// Assuming redisx.Client is a wrapper around go-redis which is thread safe.
			c.processKeys(ctx, src, tgt, keyChan, result, &inconsistenciesMutex, progressCh)
		}()
	}

	// Wait for completion
	workerWg.Wait()
	result.Duration = time.Since(startTime)

	c.PrintResult(result)
	return result, nil
}

func (c *Checker) scanSource(ctx context.Context, client *redisx.Client, out chan<- string) {
	cursor := "0"
	for {
		// key filter pattern
		pattern := "*"
		// TODO: Support filter list parsing if needed, complicates SCAN.
		// For now simple catch-all

		reply, err := client.Do("SCAN", cursor, "COUNT", c.config.BatchSize, "MATCH", pattern)
		if err != nil {
			log.Printf("SCAN failed: %v", err)
			return
		}

		arr, ok := reply.([]interface{})
		if !ok || len(arr) != 2 {
			log.Printf("SCAN returned unexpected format: %T", reply)
			return
		}

		// Cursor
		cursor, err = redisx.ToString(arr[0])
		if err != nil {
			log.Printf("SCAN cursor parse failed: %v", err)
			return
		}

		// Keys
		keys, err := redisx.ToStringSlice(arr[1])
		if err != nil {
			log.Printf("SCAN keys parse failed: %v", err)
			return
		}

		for _, k := range keys {
			out <- k
		}

		if cursor == "0" {
			break
		}
	}
}

func (c *Checker) processKeys(ctx context.Context, src, tgt *redisx.Client, keys <-chan string, res *Result, lock *sync.Mutex, progressCh chan<- Progress) {
	batchSize := 100
	batch := make([]string, 0, batchSize)

	for key := range keys {
		batch = append(batch, key)
		if len(batch) >= batchSize {
			c.processBatch(ctx, src, tgt, batch, res, lock, progressCh)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		c.processBatch(ctx, src, tgt, batch, res, lock, progressCh)
	}
}

func (c *Checker) processBatch(ctx context.Context, src, tgt *redisx.Client, keys []string, res *Result, lock *sync.Mutex, progressCh chan<- Progress) {
	if len(keys) == 0 {
		return
	}

	// 1. Pipeline TYPE for all keys
	typeCmds := make([][]interface{}, len(keys))
	for i, key := range keys {
		typeCmds[i] = []interface{}{"TYPE", key}
	}

	srcReplies, err := src.Pipeline(typeCmds)
	if err != nil {
		log.Printf("Source TYPE pipeline failed: %v", err)
		return
	}
	tgtReplies, err := tgt.Pipeline(typeCmds)
	if err != nil {
		log.Printf("Target TYPE pipeline failed: %v", err)
		return
	}

	// 2. Analyze Types and Group Strings
	stringKeys := make([]string, 0)
	otherKeys := make([]struct{ k, t string }, 0)

	for i, key := range keys {
		atomic.AddInt64(&res.TotalKeys, 1)

		srcType, err1 := redisx.ToString(srcReplies[i])
		tgtType, err2 := redisx.ToString(tgtReplies[i])

		if err1 != nil || err2 != nil {
			log.Printf("Failed to Parse TYPE for %s: %v %v", key, err1, err2)
			continue
		}

		consistent := true
		if tgtType == "none" {
			consistent = false
			atomic.AddInt64(&res.MissingKeys, 1)
		} else if srcType != tgtType {
			consistent = false
		}

		if !consistent {
			c.recordInconsistency(res, lock, key, srcType, tgtType)
			continue
		}

		// Types match. Check Value if needed.
		if c.config.Mode == ModeFullValue || c.config.Mode == ModeValueLength { // Todo: ModeValueLength handling
			if srcType == "string" && c.config.Mode == ModeFullValue {
				stringKeys = append(stringKeys, key)
			} else {
				otherKeys = append(otherKeys, struct{ k, t string }{key, srcType})
			}
		} else {
			// Outline mode, consistent
			atomic.AddInt64(&res.ConsistentKeys, 1)
		}
	}

	// 3. Batch Verify Strings
	if len(stringKeys) > 0 {
		c.batchVerifyStrings(src, tgt, stringKeys, res, lock)
	}

	// 4. Verify Others Iteratively
	for _, item := range otherKeys {
		isConsistent, err := c.verifyFullValue(src, tgt, item.k, item.t)
		if err != nil {
			log.Printf("Value check error for %s: %v", item.k, err)
			c.recordInconsistency(res, lock, item.k, fmt.Sprintf("%s(err)", item.t), "error")
		} else if !isConsistent {
			c.recordInconsistency(res, lock, item.k, item.t, item.t)
		} else {
			atomic.AddInt64(&res.ConsistentKeys, 1)
		}
	}

	// Progress Reporting
	c.reportProgress(res, progressCh)
}

func (c *Checker) batchVerifyStrings(src, tgt *redisx.Client, keys []string, res *Result, lock *sync.Mutex) {
	// Pipelining STRLEN first for SmartBigKey logic would be ideal if we want to be 100% robust.
	// But to save RTT, we can pipeline GET directly?
	// If Big Key, GET might be heavy.
	// If c.config.Mode == ModeSmartBigKey, we MUST check STRLEN first.
	// If Mode == FullValue, we can try GET. But if key is 500MB string, GET kills network.
	// Safe approach: Pipeline STRLEN first for *all* strings.

	// Pipeline STRLEN
	lenCmds := make([][]interface{}, len(keys))
	for i, key := range keys {
		lenCmds[i] = []interface{}{"STRLEN", key}
	}

	srcLens, err := src.Pipeline(lenCmds)
	if err != nil {
		log.Printf("Source STRLEN pipeline failed: %v", err)
		return
	}
	tgtLens, err := tgt.Pipeline(lenCmds)
	if err != nil {
		log.Printf("Target STRLEN pipeline failed: %v", err)
		return
	}

	getPipelineKeys := make([]string, 0)
	// Map index back to original keys slice

	for i, key := range keys {
		l1, err1 := redisx.ToInt64(srcLens[i])
		l2, err2 := redisx.ToInt64(tgtLens[i])
		if err1 != nil || err2 != nil {
			continue // skip error
		}

		if l1 != l2 {
			c.recordInconsistency(res, lock, key, fmt.Sprintf("len:%d", l1), fmt.Sprintf("len:%d", l2))
			continue
		}

		// If Smart Mode and big, skip GET
		if c.config.Mode == ModeSmartBigKey && int(l1) > c.config.BigKeyThreshold {
			atomic.AddInt64(&res.ConsistentKeys, 1)
			continue
		}

		getPipelineKeys = append(getPipelineKeys, key)
	}

	if len(getPipelineKeys) == 0 {
		return
	}

	// Pipeline GET
	getCmds := make([][]interface{}, len(getPipelineKeys))
	for i, k := range getPipelineKeys {
		getCmds[i] = []interface{}{"GET", k}
	}

	srcVals, err := src.Pipeline(getCmds)
	if err != nil {
		return
	}
	tgtVals, err := tgt.Pipeline(getCmds)
	if err != nil {
		return
	}

	for i, key := range getPipelineKeys {
		v1, err1 := redisx.ToString(srcVals[i])
		v2, err2 := redisx.ToString(tgtVals[i])

		if err1 != nil || err2 != nil || v1 != v2 {
			c.recordInconsistency(res, lock, key, "val_mismatch", "val_mismatch")
		} else {
			atomic.AddInt64(&res.ConsistentKeys, 1)
		}
	}
}

func (c *Checker) recordInconsistency(res *Result, lock *sync.Mutex, key, srcInfo, tgtInfo string) {
	atomic.AddInt64(&res.InconsistentKeys, 1)
	lock.Lock()
	if len(res.InconsistentSamples) < 100 {
		res.InconsistentSamples = append(res.InconsistentSamples, fmt.Sprintf("%s (src:%s, tgt:%s)", key, srcInfo, tgtInfo))
	}
	lock.Unlock()
}

func (c *Checker) reportProgress(res *Result, progressCh chan<- Progress) {
	total := atomic.LoadInt64(&res.TotalKeys)
	if progressCh != nil && total%1000 == 0 { // Reduce frequency
		select {
		case progressCh <- Progress{
			TotalKeys:        total,
			CheckedKeys:      total,
			InconsistentKeys: atomic.LoadInt64(&res.InconsistentKeys),
			MissingKeys:      atomic.LoadInt64(&res.MissingKeys),
			Message:          "Running native check...",
		}:
		default:
		}
	}
}

// Helper to ignore error for simple calls where we log/continue in outer scope
func must(val interface{}, err error) interface{} {
	if err != nil {
		return nil
	}
	return val
}

func (c *Checker) PrintResult(result *Result) {
	fmt.Printf("\n📊 Check Result: %d keys scanned, %d inconsistent\n", result.TotalKeys, result.InconsistentKeys)
}
