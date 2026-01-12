package replica

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"df2redis/internal/redisx"
)

// PipelineClient defines the interface for pipeline operations
type PipelineClient interface {
	Pipeline(cmds [][]interface{}) ([]interface{}, error)
}

// FlowWriter handles async batched writes for a single flow
type FlowWriter struct {
	flowID        int
	entryChan     chan *RDBEntry
	batchSize     int
	flushInterval time.Duration
	writeFn       func(*RDBEntry) error // Function to write an entry
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup

	// Target type determines write strategy
	targetType string // "redis-standalone" or "redis-cluster"

	// Pipeline client for standalone Redis batch writes
	// This is set only when targetType == "redis-standalone"
	pipelineClient PipelineClient
	isCluster      bool
	clusterClient  interface{} // Store original cluster client for debugging


	// Concurrency control
	maxConcurrentWrites int           // Maximum concurrent write goroutines
	writeSemaphore      chan struct{} // Semaphore to limit concurrency

	// Statistics
	stats struct {
		totalReceived int64
		totalWritten  int64
		totalBatches  int64
		mu            sync.Mutex
	}

	// Backpressure and monitoring
	channelCapacity    int
	lastMonitorTime    time.Time
	monitorInterval    time.Duration
	highWatermarkCount int64 // Count of times channel usage exceeded 80%

	// Async flush helper
	asyncFlush func([]*RDBEntry)
}

// NewFlowWriter creates a new async batch writer for a flow
// numFlows parameter enables adaptive concurrency tuning based on total FLOW count
// targetType determines the write strategy: "redis-standalone" uses pipeline, "redis-cluster" uses slot-based parallelism
// clusterClient provides access to standalone client for pipeline operations
func NewFlowWriter(flowID int, writeFn func(*RDBEntry) error, numFlows int, targetType string, clusterClient interface{}) *FlowWriter {
	ctx, cancel := context.WithCancel(context.Background())

	// Extract pipeline client if in standalone mode
	// CRITICAL FIX: Use concrete type assertion for *cluster.ClusterClient
	var pipelineClient PipelineClient
	var isCluster bool

	log.Printf("  [FLOW-%d] [INIT] Starting FlowWriter initialization, targetType=%s", flowID, targetType)

	// Try to extract cluster client using concrete type import
	// We use interface with method signatures to avoid circular dependency
	type ClusterClientInterface interface {
		IsCluster() bool
		GetStandaloneClient() *redisx.Client
	}

	if cc, ok := clusterClient.(ClusterClientInterface); ok {
		isCluster = cc.IsCluster()
		log.Printf("  [FLOW-%d] [INIT] ClusterClient interface extracted, isCluster=%v", flowID, isCluster)

		if !isCluster {
			// Get standalone client (*redisx.Client)
			standaloneClient := cc.GetStandaloneClient()
			log.Printf("  [FLOW-%d] [INIT] StandaloneClient extracted, type=%T", flowID, standaloneClient)

			// *redisx.Client should implement PipelineClient interface
			if standaloneClient != nil {
				// Direct assignment since *redisx.Client implements Pipeline()
				pipelineClient = standaloneClient
				log.Printf("  [FLOW-%d] [INIT] ✓ Pipeline client successfully extracted!", flowID)
			} else {
				log.Printf("  [FLOW-%d] [INIT] ✗ WARNING: StandaloneClient is nil!", flowID)
			}
		}
	} else {
		log.Printf("  [FLOW-%d] [INIT] ✗ WARNING: Failed to extract ClusterClient interface from %T", flowID, clusterClient)
	}

	// Adaptive concurrency control based on target type and number of FLOWs:
	// - Standalone mode: pipeline batching = lower concurrency needed
	// - Cluster mode: slot-based parallel writes = higher concurrency needed
	// - Physical machine (56 cores, 200G RAM) can handle high concurrency
	//
	// Strategy:
	//   - Async Batching Mode: Limit number of concurrent in-flight batches
	//   - maxConcurrent now means "Max In-Flight Batches"
	//   - 20-50 concurrent batches is plenty for high throughput
	var maxConcurrent int
	if targetType == "redis-standalone" {
		maxConcurrent = 50 // High concurrency for pipeline batches
	} else {
		// Cluster mode: limit total batches per flow to avoid exploding connection pool
		totalConcurrencyLimit := 400
		maxConcurrent = totalConcurrencyLimit / numFlows
		if maxConcurrent < 20 {
			maxConcurrent = 20
		}
		if maxConcurrent > 50 {
			maxConcurrent = 50
		}
	}

	// CRITICAL OPTIMIZATION: Increase batch size to reduce round trips
	// Old: 100 entries/batch → ~700 ops/sec
	// New: 2000 entries/batch → expected ~14000 ops/sec (20x reduction in round trips)
	batchSize := 2000 // Process 2000 entries per batch (increased from 100)

	// DYNAMIC FLUSH INTERVAL: Use shorter interval for incremental sync responsiveness
	// During full sync (high throughput), batches fill quickly and interval doesn't matter
	// During incremental sync (low throughput), we want fast response time
	flushInterval := 50 // Flush every 50ms for better incremental sync latency

	// CRITICAL: Even larger buffer to handle full sync bursts
	// During full sync, Dragonfly can send data extremely fast (600万keys in 3 seconds)
	// We need a huge buffer to absorb the burst while slowly writing to Redis
	//
	// Buffer sizing:
	//   - Increased buffer to prevent blocking parser while writing to Redis
	//   - 2M entries × ~1KB per entry = ~2GB memory per FLOW
	//   - With 8 FLOWs: 8 × 2GB = 16GB total (acceptable on 200GB machine)
	channelBuffer := 2000000 // Huge buffer to absorb full sync bursts

	fw := &FlowWriter{
		flowID:              flowID,
		entryChan:           make(chan *RDBEntry, channelBuffer),             // Huge buffer for full sync bursts
		batchSize:           batchSize,                                       // Large batch for pipeline efficiency
		flushInterval:       time.Duration(flushInterval) * time.Millisecond, // Fallback timer for tiny batches
		writeFn:             writeFn,
		targetType:          targetType,
		pipelineClient:      pipelineClient,
		isCluster:           isCluster,
		clusterClient:       clusterClient,
		ctx:                 ctx,
		cancel:              cancel,
		channelCapacity:     channelBuffer,
		maxConcurrentWrites: maxConcurrent,
		writeSemaphore:      make(chan struct{}, maxConcurrent), // Semaphore for concurrency control
		lastMonitorTime:     time.Now(),
		monitorInterval:     5 * time.Second, // Log channel usage every 5 seconds (more frequent)
	}

	// Log adaptive concurrency settings and mode for visibility
	mode := targetType
	pipelineStatus := "DISABLED"
	if targetType == "redis-standalone" {
		if pipelineClient != nil {
			mode = "standalone (pipeline ENABLED)"
			pipelineStatus = "ENABLED"
		} else {
			mode = "standalone (pipeline FAILED - will use sequential!)"
			pipelineStatus = "FAILED"
		}
	} else {
		mode = "cluster (slot-based parallel)"
	}
	log.Printf("  [FLOW-%d] [WRITER] Initialization complete:", flowID)
	log.Printf("    • Mode: %s", mode)
	log.Printf("    • Pipeline: %s", pipelineStatus)
	log.Printf("    • Concurrency: %d per FLOW (%d total)", maxConcurrent, maxConcurrent*numFlows)
	log.Printf("    • Batch size: %d entries", batchSize)
	log.Printf("    • Buffer size: %d entries (~%dMB)", channelBuffer, channelBuffer/1000)
	log.Printf("    • Flush interval: %v", time.Duration(flushInterval)*time.Millisecond)

	return fw
}

// Start launches the async write loop
func (fw *FlowWriter) Start() {
	fw.wg.Add(1)
	go fw.batchWriteLoop()
}

// Stop gracefully shuts down the writer, ensuring all queued entries are flushed
func (fw *FlowWriter) Stop() {
	// Close channel to signal no more entries
	close(fw.entryChan)

	// Wait for write loop to finish processing all remaining entries
	fw.wg.Wait()

	// Cancel context after all data is written
	fw.cancel()

	log.Printf("  [FLOW-%d] [WRITER] Shutdown complete, all data flushed", fw.flowID)
}

// Enqueue adds an entry to the write queue (blocking with 2M buffer)
// With 2M buffer, blocking is acceptable as it provides sufficient backpressure protection
// If channel somehow fills up (extreme case), we block Parser briefly
func (fw *FlowWriter) Enqueue(entry *RDBEntry) error {
	select {
	case fw.entryChan <- entry:
		// Successfully enqueued
		fw.stats.mu.Lock()
		fw.stats.totalReceived++
		fw.stats.mu.Unlock()
		return nil

	case <-fw.ctx.Done():
		return fmt.Errorf("flow writer stopped")
	}
}

// GetStats returns current statistics
func (fw *FlowWriter) GetStats() (received, written, batches int64) {
	fw.stats.mu.Lock()
	defer fw.stats.mu.Unlock()
	return fw.stats.totalReceived, fw.stats.totalWritten, fw.stats.totalBatches
}

// batchWriteLoop is the main async write loop
func (fw *FlowWriter) batchWriteLoop() {
	defer fw.wg.Done()

	batch := make([]*RDBEntry, 0, fw.batchSize)
	ticker := time.NewTicker(fw.flushInterval)
	defer ticker.Stop()

	log.Printf("  [FLOW-%d] [WRITER] Async batch writer started (batch=%d, interval=%v)",
		fw.flowID, fw.batchSize, fw.flushInterval)

	// Helper for async flushing
	fw.asyncFlush = func(batch []*RDBEntry) {
		// Acquire batch semaphore
		fw.writeSemaphore <- struct{}{}
		fw.wg.Add(1)
		go func(b []*RDBEntry) {
			defer fw.wg.Done()
			defer func() { <-fw.writeSemaphore }() // Release semaphore
			fw.flushBatch(b)
		}(batch)
	}

	for {
		select {
		case entry, ok := <-fw.entryChan:
			if !ok {
				// Channel closed, flush remaining batch and exit
				if len(batch) > 0 {
					fw.asyncFlush(batch)
				}
				log.Printf("  [FLOW-%d] [WRITER] Channel closed, shutting down (total written: %d, batches: %d)",
					fw.flowID, fw.stats.totalWritten, fw.stats.totalBatches)
				return
			}

			batch = append(batch, entry)

			// Flush if batch size reached
			if len(batch) >= fw.batchSize {
				fw.asyncFlush(batch)
				batch = make([]*RDBEntry, 0, fw.batchSize) // New batch
			}

		case <-ticker.C:
			// Flush on timer if batch not empty
			if len(batch) > 0 {
				fw.asyncFlush(batch)
				batch = make([]*RDBEntry, 0, fw.batchSize) // New batch
			}

		case <-fw.ctx.Done():
			// Context cancelled, flush and exit
			if len(batch) > 0 {
				fw.asyncFlush(batch)
			}
			log.Printf("  [FLOW-%d] [WRITER] Context cancelled, shutting down", fw.flowID)
			return
		}
	}
}

// flushBatch writes a batch of entries to Redis using smart batching:
// - Group entries by slot for parallel writes
// - Use pipelining within each slot group
func (fw *FlowWriter) flushBatch(batch []*RDBEntry) {
	if len(batch) == 0 {
		return
	}

	start := time.Now()
	batchSize := len(batch)

	// Log batch start
	log.Printf("  [FLOW-%d] [WRITER] ⏩ Flushing batch: %d entries", fw.flowID, batchSize)

	// CRITICAL: Strategy selection based on target type
	// - Standalone: treat batch as single group (enables pipeline batching)
	// - Cluster: group by slot for parallel writes
	var slotGroups [][]*RDBEntry
	if fw.targetType == "redis-standalone" {
		// Standalone mode: treat entire batch as one group (no slot partitioning)
		slotGroups = [][]*RDBEntry{batch}
	} else {
		// Cluster mode: group entries by slot for parallel processing
		slotGroups = fw.groupBySlot(batch)
	}
	numGroups := len(slotGroups)

	// Process groups in parallel using goroutines with concurrency limit
	var wg sync.WaitGroup
	resultChan := make(chan writeResult, numGroups)

	for _, group := range slotGroups {
		// NOTE: In async batch mode, we don't use the semaphore here (it's used for batch limiting).
		// We launch slot groups in parallel without further limiting, as the batch limit controls overall concurrency.
		wg.Add(1)
		go func(entries []*RDBEntry) {
			defer wg.Done()

			result := fw.writeSlotGroup(entries)
			resultChan <- result
		}(group)
	}

	// Wait for all groups to complete
	wg.Wait()
	close(resultChan)

	// Aggregate results
	var successCount, failCount int
	for result := range resultChan {
		successCount += result.success
		failCount += result.failed
	}

	// NOTE: Object pool removed due to race condition bug
	// Entries will be GC'd naturally

	duration := time.Since(start)

	// Update statistics
	fw.stats.mu.Lock()
	fw.stats.totalWritten += int64(successCount)
	fw.stats.totalBatches++
	fw.stats.mu.Unlock()

	DebugTotalFlushed.Add(int64(successCount)) // DEBUG COUNTER

	// Log performance
	opsPerSec := float64(batchSize) / duration.Seconds()
	log.Printf("  [FLOW-%d] [WRITER] ✓ Batch complete: %d entries in %v (%.0f ops/sec, groups=%d, success=%d, fail=%d)",
		fw.flowID, batchSize, duration, opsPerSec, numGroups, successCount, failCount)

	// Warn if slow
	// Warn if slow or empty key detected
	if duration > time.Second || (len(batch) > 0 && len(batch[0].Key) == 0) {
		// Calculate batch size in bytes
		var loopTotalBytes int
		for _, e := range batch {
			loopTotalBytes += len(e.Key)
			if e.Value != nil {
				switch v := e.Value.(type) {
				case []byte:
					loopTotalBytes += len(v)
				case string:
					loopTotalBytes += len(v)
				}
			}
		}

		firstKey := "N/A"
		firstType := -1
		valType := "nil"
		if len(batch) > 0 {
			firstKey = fmt.Sprintf("'%s' (len=%d)", batch[0].Key, len(batch[0].Key))
			firstType = int(batch[0].Type)
			if batch[0].Value != nil {
				valType = fmt.Sprintf("%T", batch[0].Value)
			}
		}

		log.Printf("  [FLOW-%d] [WRITER] ⚠ Slow/Empty batch: %v for %d entries. TotalBytes: %d. FirstEntry: [Type=%d, Key=%s, ValType=%s]",
			fw.flowID, duration, batchSize, loopTotalBytes, firstType, firstKey, valType)
	}
}

// writeResult holds the result of writing a slot group
type writeResult struct {
	success int
	failed  int
}

// groupBySlot groups entries by Redis Cluster slot
func (fw *FlowWriter) groupBySlot(batch []*RDBEntry) [][]*RDBEntry {
	// Simple slot calculation (CRC16 % 16384)
	slotMap := make(map[uint16][]*RDBEntry)

	for _, entry := range batch {
		slot := calculateSlot(entry.Key)
		slotMap[slot] = append(slotMap[slot], entry)
	}

	// Convert map to slice of groups
	groups := make([][]*RDBEntry, 0, len(slotMap))
	for _, group := range slotMap {
		groups = append(groups, group)
	}

	return groups
}

// writeSlotGroup writes a group of entries for the same slot
func (fw *FlowWriter) writeSlotGroup(entries []*RDBEntry) writeResult {
	var successCount, failCount int
	startTime := time.Now()

	// Declare variables at function start to avoid "goto jumps over declaration" error
	var buildStart time.Time
	var buildDuration time.Duration
	var cmds [][]interface{}

	// CRITICAL DEBUG: Log code path selection
	log.Printf("  [FLOW-%d] [WRITER] writeSlotGroup called: entries=%d, targetType=%s, pipelineClient=%v",
		fw.flowID, len(entries), fw.targetType, fw.pipelineClient != nil)

	// CRITICAL OPTIMIZATION: Always use pipeline for cluster mode
	// For standalone mode, skip pipeline for very small batches (< 3 entries)
	// Cluster mode benefits from pipeline even for 1-2 entries due to routing overhead
	pipelineThreshold := 1
	if fw.targetType == "redis-standalone" {
		pipelineThreshold = 3 // Standalone can skip tiny batches
	}
	if len(entries) < pipelineThreshold {
		log.Printf("  [FLOW-%d] [WRITER] Small batch (%d entries), using DIRECT WRITE mode", fw.flowID, len(entries))
		goto sequential
	}

	// CRITICAL FIX: Split large slot groups into smaller pipelines to avoid Redis timeout
	// Redis can handle large pipelines, but 1000+ commands can cause 10+ second delays
	// Split into chunks of 500 commands for better performance and responsiveness
	const maxPipelineSize = 500
	if len(entries) > maxPipelineSize {
		log.Printf("  [FLOW-%d] [WRITER] Large slot group (%d entries), splitting into chunks of %d",
			fw.flowID, len(entries), maxPipelineSize)

		for i := 0; i < len(entries); i += maxPipelineSize {
			end := i + maxPipelineSize
			if end > len(entries) {
				end = len(entries)
			}
			chunk := entries[i:end]
			result := fw.writeSlotGroup(chunk) // Recursive call with smaller chunk
			successCount += result.success
			failCount += result.failed
		}

		duration := time.Since(startTime)
		log.Printf("  [FLOW-%d] [WRITER] ✓ Large slot group complete: %d success, %d failed in %v (%.0f ops/sec)",
			fw.flowID, successCount, failCount, duration, float64(len(entries))/duration.Seconds())

		return writeResult{success: successCount, failed: failCount}
	}

	// Build pipeline commands for all entries (shared logic for both modes)
	buildStart = time.Now()
	cmds = make([][]interface{}, 0, len(entries))
	for _, entry := range entries {
		cmd := fw.buildCommand(entry)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	buildDuration = time.Since(buildStart)

	if len(cmds) == 0 {
		log.Printf("  [FLOW-%d] [WRITER] ⚠ No commands built, all entries skipped", fw.flowID)
		return writeResult{success: 0, failed: 0}
	}

	log.Printf("  [FLOW-%d] [WRITER] Built %d commands in %v", fw.flowID, len(cmds), buildDuration)

	// Standalone mode: use standalone pipeline
	if fw.targetType == "redis-standalone" && fw.pipelineClient != nil {
		log.Printf("  [FLOW-%d] [WRITER] ✓ Using STANDALONE PIPELINE mode for %d entries", fw.flowID, len(entries))

		pipelineStart := time.Now()
		results, err := fw.pipelineClient.Pipeline(cmds)
		pipelineDuration := time.Since(pipelineStart)

		if err != nil {
			log.Printf("  [FLOW-%d] [WRITER] ✗ Standalone pipeline failed after %v: %v, fallback to sequential",
				fw.flowID, pipelineDuration, err)
			goto sequential
		}

		log.Printf("  [FLOW-%d] [WRITER] ✓ Standalone pipeline executed in %v (%.0f ops/sec)",
			fw.flowID, pipelineDuration, float64(len(cmds))/pipelineDuration.Seconds())

		// Check results
		for i, result := range results {
			if result != nil {
				if errStr, ok := result.(string); ok && strings.HasPrefix(errStr, "ERR") {
					log.Printf("  [FLOW-%d] [WRITER] ✗ Command %d failed: %v", fw.flowID, i, errStr)
					failCount++
				} else {
					successCount++
				}
			} else {
				successCount++
			}
		}

		totalDuration := time.Since(startTime)
		log.Printf("  [FLOW-%d] [WRITER] ✓ Standalone pipeline batch complete: %d success, %d failed in %v",
			fw.flowID, successCount, failCount, totalDuration)

		return writeResult{success: successCount, failed: failCount}
	}

	// Cluster mode: use cluster pipeline (CRITICAL OPTIMIZATION)
	if fw.targetType == "redis-cluster" && fw.clusterClient != nil {
		log.Printf("  [FLOW-%d] [WRITER] ✓ Using CLUSTER PIPELINE mode for %d entries", fw.flowID, len(entries))

		// Extract ClusterClient interface for pipeline operations
		type ClusterPipelineInterface interface {
			PipelineForSlot(slot int, cmds [][]interface{}) ([]interface{}, error)
		}

		if clusterClient, ok := fw.clusterClient.(ClusterPipelineInterface); ok {
			// Calculate slot from first entry's key
			if len(entries) > 0 {
				slot := int(calculateSlot(entries[0].Key))
				log.Printf("  [FLOW-%d] [WRITER] Routing to slot %d for %d commands", fw.flowID, slot, len(cmds))

				pipelineStart := time.Now()
				results, err := clusterClient.PipelineForSlot(slot, cmds)
				pipelineDuration := time.Since(pipelineStart)

				if err != nil {
					log.Printf("  [FLOW-%d] [WRITER] ✗ Cluster pipeline failed after %v: %v, fallback to sequential",
						fw.flowID, pipelineDuration, err)
					goto sequential
				}

				log.Printf("  [FLOW-%d] [WRITER] ✓ Cluster pipeline executed in %v (%.0f ops/sec)",
					fw.flowID, pipelineDuration, float64(len(cmds))/pipelineDuration.Seconds())

				// Check results
				for i, result := range results {
					if result != nil {
						if errStr, ok := result.(string); ok && strings.HasPrefix(errStr, "ERR") {
							log.Printf("  [FLOW-%d] [WRITER] ✗ Command %d failed: %v", fw.flowID, i, errStr)
							failCount++
						} else {
							successCount++
						}
					} else {
						successCount++
					}
				}

				totalDuration := time.Since(startTime)
				log.Printf("  [FLOW-%d] [WRITER] ✓ Cluster pipeline batch complete: %d success, %d failed in %v",
					fw.flowID, successCount, failCount, totalDuration)

				return writeResult{success: successCount, failed: failCount}
			}
		} else {
			log.Printf("  [FLOW-%d] [WRITER] ✗ WARNING: ClusterClient does not support PipelineForSlot, fallback to sequential", fw.flowID)
		}
	}

	log.Printf("  [FLOW-%d] [WRITER] Using SEQUENTIAL mode (targetType=%s, pipelineClient=%v)",
		fw.flowID, fw.targetType, fw.pipelineClient != nil)

sequential:
	// Fallback: write entries sequentially (used for small batches or pipeline failures)
	seqStart := time.Now()

	// Use direct Do() instead of writeEntry() for better performance in cluster mode
	for _, entry := range entries {
		if err := fw.writeEntry(entry); err != nil {
			// Don't log every error in production, just count
			if len(entries) < 10 || failCount < 5 {
				log.Printf("  [FLOW-%d] [WRITER] ✗ Failed to write key=%s: %v", fw.flowID, entry.Key, err)
			}
			failCount++
		} else {
			successCount++
		}
	}

	seqDuration := time.Since(seqStart)

	// Only log if batch is large enough or took significant time
	if len(entries) >= 10 || seqDuration > 100*time.Millisecond {
		log.Printf("  [FLOW-%d] [WRITER] Direct write complete: %d success, %d failed in %v (%.0f ops/sec)",
			fw.flowID, successCount, failCount, seqDuration, float64(len(entries))/seqDuration.Seconds())
	}

	return writeResult{success: successCount, failed: failCount}
}

// calculateSlot calculates the Redis Cluster slot for a key
func calculateSlot(key string) uint16 {
	// Extract hash tag if present: {...}
	hashKey := key
	start := -1
	for i, c := range key {
		if c == '{' {
			start = i + 1
		} else if c == '}' && start >= 0 {
			hashKey = key[start:i]
			break
		}
	}

	// CRC16 checksum
	return crc16([]byte(hashKey)) % 16384
}

// crc16 implements CRC16-CCITT (polynomial 0x1021)
func crc16(data []byte) uint16 {
	var crc uint16 = 0
	for _, b := range data {
		crc = (crc << 8) ^ crc16Table[((crc>>8)^uint16(b))&0xFF]
	}
	return crc
}

// CRC16 lookup table
var crc16Table = [256]uint16{
	0x0000, 0x1021, 0x2042, 0x3063, 0x4084, 0x50A5, 0x60C6, 0x70E7,
	0x8108, 0x9129, 0xA14A, 0xB16B, 0xC18C, 0xD1AD, 0xE1CE, 0xF1EF,
	0x1231, 0x0210, 0x3273, 0x2252, 0x52B5, 0x4294, 0x72F7, 0x62D6,
	0x9339, 0x8318, 0xB37B, 0xA35A, 0xD3BD, 0xC39C, 0xF3FF, 0xE3DE,
	0x2462, 0x3443, 0x0420, 0x1401, 0x64E6, 0x74C7, 0x44A4, 0x5485,
	0xA56A, 0xB54B, 0x8528, 0x9509, 0xE5EE, 0xF5CF, 0xC5AC, 0xD58D,
	0x3653, 0x2672, 0x1611, 0x0630, 0x76D7, 0x66F6, 0x5695, 0x46B4,
	0xB75B, 0xA77A, 0x9719, 0x8738, 0xF7DF, 0xE7FE, 0xD79D, 0xC7BC,
	0x48C4, 0x58E5, 0x6886, 0x78A7, 0x0840, 0x1861, 0x2802, 0x3823,
	0xC9CC, 0xD9ED, 0xE98E, 0xF9AF, 0x8948, 0x9969, 0xA90A, 0xB92B,
	0x5AF5, 0x4AD4, 0x7AB7, 0x6A96, 0x1A71, 0x0A50, 0x3A33, 0x2A12,
	0xDBFD, 0xCBDC, 0xFBBF, 0xEB9E, 0x9B79, 0x8B58, 0xBB3B, 0xAB1A,
	0x6CA6, 0x7C87, 0x4CE4, 0x5CC5, 0x2C22, 0x3C03, 0x0C60, 0x1C41,
	0xEDAE, 0xFD8F, 0xCDEC, 0xDDCD, 0xAD2A, 0xBD0B, 0x8D68, 0x9D49,
	0x7E97, 0x6EB6, 0x5ED5, 0x4EF4, 0x3E13, 0x2E32, 0x1E51, 0x0E70,
	0xFF9F, 0xEFBE, 0xDFDD, 0xCFFC, 0xBF1B, 0xAF3A, 0x9F59, 0x8F78,
	0x9188, 0x81A9, 0xB1CA, 0xA1EB, 0xD10C, 0xC12D, 0xF14E, 0xE16F,
	0x1080, 0x00A1, 0x30C2, 0x20E3, 0x5004, 0x4025, 0x7046, 0x6067,
	0x83B9, 0x9398, 0xA3FB, 0xB3DA, 0xC33D, 0xD31C, 0xE37F, 0xF35E,
	0x02B1, 0x1290, 0x22F3, 0x32D2, 0x4235, 0x5214, 0x6277, 0x7256,
	0xB5EA, 0xA5CB, 0x95A8, 0x8589, 0xF56E, 0xE54F, 0xD52C, 0xC50D,
	0x34E2, 0x24C3, 0x14A0, 0x0481, 0x7466, 0x6447, 0x5424, 0x4405,
	0xA7DB, 0xB7FA, 0x8799, 0x97B8, 0xE75F, 0xF77E, 0xC71D, 0xD73C,
	0x26D3, 0x36F2, 0x0691, 0x16B0, 0x6657, 0x7676, 0x4615, 0x5634,
	0xD94C, 0xC96D, 0xF90E, 0xE92F, 0x99C8, 0x89E9, 0xB98A, 0xA9AB,
	0x5844, 0x4865, 0x7806, 0x6827, 0x18C0, 0x08E1, 0x3882, 0x28A3,
	0xCB7D, 0xDB5C, 0xEB3F, 0xFB1E, 0x8BF9, 0x9BD8, 0xABBB, 0xBB9A,
	0x4A75, 0x5A54, 0x6A37, 0x7A16, 0x0AF1, 0x1AD0, 0x2AB3, 0x3A92,
	0xFD2E, 0xED0F, 0xDD6C, 0xCD4D, 0xBDAA, 0xAD8B, 0x9DE8, 0x8DC9,
	0x7C26, 0x6C07, 0x5C64, 0x4C45, 0x3CA2, 0x2C83, 0x1CE0, 0x0CC1,
	0xEF1F, 0xFF3E, 0xCF5D, 0xDF7C, 0xAF9B, 0xBFBA, 0x8FD9, 0x9FF8,
	0x6E17, 0x7E36, 0x4E55, 0x5E74, 0x2E93, 0x3EB2, 0x0ED1, 0x1EF0,
}

// writeEntry writes a single RDB entry using the provided write function
func (fw *FlowWriter) writeEntry(entry *RDBEntry) error {
	return fw.writeFn(entry)
}
