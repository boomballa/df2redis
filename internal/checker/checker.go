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

// Run executes data consistency validation, supporting multiple rounds via config.CompareTimes.
// Round 1 scans all keys. Subsequent rounds re-scan all keys after a configurable interval,
// stopping early when no inconsistencies remain.
func (c *Checker) Run(ctx context.Context, progressCh chan<- Progress) (*Result, error) {
	// Create result directory
	if err := os.MkdirAll(c.config.ResultDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create result directory: %w", err)
	}

	startTime := time.Now()
	compareTimes := c.config.CompareTimes
	if compareTimes <= 0 {
		compareTimes = 1
	}

	log.Printf("🚀 Starting native check (Mode: %s, Parallel: %d, Rounds: %d)", c.config.Mode, c.config.Parallel, compareTimes)

	// Connect once; reuse across rounds
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

	finalResult := &Result{InconsistentSamples: make([]string, 0)}

	for round := 1; round <= compareTimes; round++ {
		// Wait between rounds
		if round > 1 {
			interval := c.config.Interval
			if interval <= 0 {
				interval = 5
			}
			select {
			case <-ctx.Done():
				finalResult.Duration = time.Since(startTime)
				return finalResult, ctx.Err()
			case <-time.After(time.Duration(interval) * time.Second):
			}
		}

		// Notify round start
		if progressCh != nil {
			select {
			case progressCh <- Progress{
				Round:   round,
				Message: fmt.Sprintf("Round %d/%d: scanning...", round, compareTimes),
			}:
			default:
			}
		}

		roundResult := c.runOneRound(ctx, src, tgt, round, compareTimes, progressCh)

		// First round establishes the total key count baseline
		if round == 1 {
			finalResult.TotalKeys = roundResult.TotalKeys
		}
		finalResult.ConsistentKeys = roundResult.ConsistentKeys
		finalResult.InconsistentKeys = roundResult.InconsistentKeys
		finalResult.MissingKeys = roundResult.MissingKeys
		finalResult.InconsistentSamples = roundResult.InconsistentSamples

		if roundResult.InconsistentKeys == 0 {
			log.Printf("✓ Round %d/%d: all consistent, stopping early", round, compareTimes)
			break
		}
		log.Printf("⚠ Round %d/%d: %d inconsistent keys", round, compareTimes, roundResult.InconsistentKeys)
	}

	finalResult.Duration = time.Since(startTime)
	c.PrintResult(finalResult)
	return finalResult, nil
}

// runOneRound performs a single full scan-and-compare pass.
func (c *Checker) runOneRound(ctx context.Context, src, tgt *redisx.Client, round, totalRounds int, progressCh chan<- Progress) *Result {
	result := &Result{InconsistentSamples: make([]string, 0)}

	keyChan := make(chan string, c.config.BatchSize*2)

	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		defer close(keyChan)
		c.scanSource(ctx, src, keyChan)
	}()

	var workerWg sync.WaitGroup
	var inconsistenciesMutex sync.Mutex

	for i := 0; i < c.config.Parallel; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			c.processKeys(ctx, src, tgt, keyChan, result, &inconsistenciesMutex, progressCh, round, totalRounds)
		}()
	}

	workerWg.Wait()
	return result
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

func (c *Checker) processKeys(ctx context.Context, src, tgt *redisx.Client, keys <-chan string, res *Result, lock *sync.Mutex, progressCh chan<- Progress, round, totalRounds int) {
	batchSize := 100
	batch := make([]string, 0, batchSize)

	for key := range keys {
		batch = append(batch, key)
		if len(batch) >= batchSize {
			c.processBatch(ctx, src, tgt, batch, res, lock, progressCh, round, totalRounds)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		c.processBatch(ctx, src, tgt, batch, res, lock, progressCh, round, totalRounds)
	}
}

func (c *Checker) processBatch(ctx context.Context, src, tgt *redisx.Client, keys []string, res *Result, lock *sync.Mutex, progressCh chan<- Progress, round, totalRounds int) {
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

		// Types match. Check value if the mode requires it.
		switch c.config.Mode {
		case ModeFullValue, ModeSmartBigKey:
			// ModeFullValue: full value comparison for all types.
			// ModeSmartBigKey: full value for small keys, length-only for big keys (decided inside compare* funcs).
			if srcType == "string" {
				// Batch string keys together for pipelined STRLEN+GET.
				stringKeys = append(stringKeys, key)
			} else {
				otherKeys = append(otherKeys, struct{ k, t string }{key, srcType})
			}
		case ModeValueLength:
			// Length-only comparison: batched via pipeline below, not via verifyFullValue.
			otherKeys = append(otherKeys, struct{ k, t string }{key, srcType})
		default:
			// ModeKeyOutline: type match is sufficient, count as consistent.
			atomic.AddInt64(&res.ConsistentKeys, 1)
		}
	}

	// 3. Batch Verify Strings (ModeFullValue / ModeSmartBigKey)
	if len(stringKeys) > 0 {
		c.batchVerifyStrings(src, tgt, stringKeys, res, lock)
	}

	// 4. Verify others
	if c.config.Mode == ModeValueLength {
		// Pipeline all length commands in one round-trip per side instead of
		// issuing one STRLEN/LLEN/HLEN/… per key, which is the main reason
		// ModeValueLength used to take ~150 s for ~900 k keys.
		c.batchVerifyLengths(src, tgt, otherKeys, res, lock)
	} else {
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
	}

	// Progress Reporting
	c.reportProgress(res, progressCh, round, totalRounds)
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

// batchVerifyLengths pipelines the appropriate length command (STRLEN/LLEN/HLEN/SCARD/ZCARD/XLEN)
// for all items in a single round-trip to each side. This is the fast path for ModeValueLength,
// replacing the previous approach of calling verifyFullValue once per key (which issued two
// individual Do() calls per key and accounted for ~150 s on 900 k keys).
func (c *Checker) batchVerifyLengths(src, tgt *redisx.Client, items []struct{ k, t string }, res *Result, lock *sync.Mutex) {
	if len(items) == 0 {
		return
	}

	cmds := make([][]interface{}, len(items))
	for i, item := range items {
		cmds[i] = lengthCmd(item.t, item.k)
	}

	srcLens, err := src.Pipeline(cmds)
	if err != nil {
		log.Printf("Source length pipeline failed: %v", err)
		return
	}
	tgtLens, err := tgt.Pipeline(cmds)
	if err != nil {
		log.Printf("Target length pipeline failed: %v", err)
		return
	}

	for i, item := range items {
		l1, err1 := redisx.ToInt64(srcLens[i])
		l2, err2 := redisx.ToInt64(tgtLens[i])
		if err1 != nil || err2 != nil {
			continue
		}
		if l1 != l2 {
			c.recordInconsistency(res, lock, item.k, fmt.Sprintf("len:%d", l1), fmt.Sprintf("len:%d", l2))
		} else {
			atomic.AddInt64(&res.ConsistentKeys, 1)
		}
	}
}

// lengthCmd returns the appropriate Redis length command for a given key type.
func lengthCmd(keyType, key string) []interface{} {
	switch keyType {
	case "string":
		return []interface{}{"STRLEN", key}
	case "list":
		return []interface{}{"LLEN", key}
	case "set":
		return []interface{}{"SCARD", key}
	case "zset":
		return []interface{}{"ZCARD", key}
	case "hash":
		return []interface{}{"HLEN", key}
	case "stream":
		return []interface{}{"XLEN", key}
	default:
		return []interface{}{"TYPE", key}
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

func (c *Checker) reportProgress(res *Result, progressCh chan<- Progress, round, totalRounds int) {
	total := atomic.LoadInt64(&res.TotalKeys)
	if progressCh != nil && total%1000 == 0 { // Reduce frequency
		select {
		case progressCh <- Progress{
			Round:            round,
			TotalKeys:        total,
			CheckedKeys:      total,
			ConsistentKeys:   atomic.LoadInt64(&res.ConsistentKeys),
			InconsistentKeys: atomic.LoadInt64(&res.InconsistentKeys),
			MissingKeys:      atomic.LoadInt64(&res.MissingKeys),
			Message:          fmt.Sprintf("Round %d/%d: running check...", round, totalRounds),
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
