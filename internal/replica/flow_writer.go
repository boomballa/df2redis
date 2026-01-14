package replica

import (
	"context"
	"df2redis/internal/redisx"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
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
	pipelineClient *redisx.Client

	// Cluster client for cluster mode
	clusterClient *redisx.ClusterClient
	isCluster     bool

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

	// Dynamic Throttling
	limiter   *rate.Limiter
	limiterMu sync.RWMutex

	// Async flush helper
	asyncFlush func([]*RDBEntry)
}

// NewFlowWriter creates a new async batch writer for a flow
func NewFlowWriter(flowID int, writeFn func(*RDBEntry) error, numFlows int, targetType string, pipelineClient *redisx.Client, clusterClient *redisx.ClusterClient) *FlowWriter {
	ctx, cancel := context.WithCancel(context.Background())

	log.Printf("  [FLOW-%d] [INIT] Starting FlowWriter initialization, targetType=%s", flowID, targetType)

	var isCluster bool
	if targetType == "redis-cluster" {
		isCluster = true
		if clusterClient == nil {
			log.Printf("  [FLOW-%d] [INIT] ✗ WARNING: ClusterClient is nil but targetType is redis-cluster!", flowID)
		}
	} else {
		// Standalone
		if pipelineClient == nil {
			log.Printf("  [FLOW-%d] [INIT] ✗ WARNING: PipelineClient (standalone) is nil!", flowID)
		} else {
			log.Printf("  [FLOW-%d] [INIT] ✓ Pipeline client provided", flowID)
		}
	}

	// Adaptive concurrency control based on target type and number of FLOWs:
	// - Standalone mode: pipeline batching = lower concurrency needed
	// - Cluster mode: slot-based parallel writes = higher concurrency needed
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

	// CRITICAL OPTIMIZATION FOR CLUSTER: Increase batch size dramatically for slot grouping
	// Solution: Use 20000 entries/batch so each slot accumulates ~1.2 entries on average
	batchSize := 20000
	if targetType == "redis-standalone" {
		batchSize = 2000 // Standalone doesn't need large batches
	}

	// DYNAMIC FLUSH INTERVAL: Different strategies for standalone vs cluster
	flushInterval := 50 // Default for standalone
	if targetType == "redis-cluster" {
		flushInterval = 5000 // 5 seconds for cluster to accumulate large batches
	}

	channelBuffer := 2000000 // Huge buffer to absorb full sync bursts

	fw := &FlowWriter{
		flowID:              flowID,
		entryChan:           make(chan *RDBEntry, channelBuffer),
		batchSize:           batchSize,
		flushInterval:       time.Duration(flushInterval) * time.Millisecond,
		writeFn:             writeFn,
		targetType:          targetType,
		pipelineClient:      pipelineClient,
		clusterClient:       clusterClient,
		isCluster:           isCluster,
		ctx:                 ctx,
		cancel:              cancel,
		channelCapacity:     channelBuffer,
		maxConcurrentWrites: maxConcurrent,
		writeSemaphore:      make(chan struct{}, maxConcurrent),
		lastMonitorTime:     time.Now(),
		monitorInterval:     5 * time.Second,
		limiter:             rate.NewLimiter(rate.Inf, 0), // Default to unlimited
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
	log.Printf("    • Concurrency: %d per FLOW", maxConcurrent)
	log.Printf("    • Batch size: %d entries", batchSize)

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

// UpdateConfig updates dynamic parameters thread-safely
func (fw *FlowWriter) UpdateConfig(qps int, batchSize int) {
	// Update QPS (Rate Limiter)
	fw.limiterMu.Lock()
	if qps <= 0 {
		fw.limiter.SetLimit(rate.Inf)
		fw.limiter.SetBurst(0)
		log.Printf("  [FLOW-%d] [CONFIG] Rate limit disabled (unlimited)", fw.flowID)
	} else {
		fw.limiter.SetLimit(rate.Limit(qps))
		fw.limiter.SetBurst(batchSize) // Burst should allow at least one batch
		log.Printf("  [FLOW-%d] [CONFIG] Rate limit set to %d QPS", fw.flowID, qps)
	}
	fw.limiterMu.Unlock()

	// Update Batch Size (Atomic-ish)
	if batchSize > 0 && batchSize != fw.batchSize {
		fw.batchSize = batchSize
		log.Printf("  [FLOW-%d] [CONFIG] Batch size updated to %d", fw.flowID, batchSize)
	}
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
// - Standalone: one big pipeline
// - Cluster: group by Master Node for parallel writes (1 pipeline per node)
func (fw *FlowWriter) flushBatch(batch []*RDBEntry) {
	if len(batch) == 0 {
		return
	}

	// Dynamic Rate Limiting
	// We wait for N tokens where N = batch items
	fw.limiterMu.RLock()
	limiter := fw.limiter
	fw.limiterMu.RUnlock()

	if limiter.Limit() != rate.Inf {
		// Wait for tokens. ctx ensures we can cancel while waiting.
		if err := limiter.WaitN(fw.ctx, len(batch)); err != nil {
			// Context cancelled or error
			return
		}
	}

	start := time.Now()
	batchSize := len(batch)

	// Log batch start
	log.Printf("  [FLOW-%d] [WRITER] ⏩ Flushing batch: %d entries", fw.flowID, batchSize)

	// Grouping Strategy
	var groups map[string][]*RDBEntry // Addr -> Entries

	if fw.targetType == "redis-standalone" {
		// Single group with empty address (uses pipelineClient)
		groups = map[string][]*RDBEntry{"": batch}
	} else {
		// Cluster mode: group by Master Node
		groups = fw.groupByNode(batch)
	}
	numGroups := len(groups)

	// Process groups in parallel using goroutines
	var wg sync.WaitGroup
	resultChan := make(chan writeResult, numGroups)

	for addr, group := range groups {
		wg.Add(1)
		go func(nodeAddr string, entries []*RDBEntry) {
			defer wg.Done()
			result := fw.writeNodeBatch(nodeAddr, entries)
			resultChan <- result
		}(addr, group)
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

	duration := time.Since(start)

	// Update statistics
	fw.stats.mu.Lock()
	fw.stats.totalWritten += int64(successCount)
	fw.stats.totalBatches++
	fw.stats.mu.Unlock()

	DebugTotalFlushed.Add(int64(successCount))

	// Log performance
	opsPerSec := float64(batchSize) / duration.Seconds()
	log.Printf("  [FLOW-%d] [WRITER] ✓ Batch complete: %d entries in %v (%.0f ops/sec, nodes=%d, success=%d, fail=%d)",
		fw.flowID, batchSize, duration, opsPerSec, numGroups, successCount, failCount)

	// Warn if slow
	if duration > time.Second {
		log.Printf("  [FLOW-%d] [WRITER] ⚠ Slow batch: %v for %d entries.", fw.flowID, duration, batchSize)
	}
}

// writeResult holds the result of writing a batch
type writeResult struct {
	success int
	failed  int
}

// groupByNode groups entries by target Master Node address
func (fw *FlowWriter) groupByNode(batch []*RDBEntry) map[string][]*RDBEntry {
	groups := make(map[string][]*RDBEntry)

	for _, entry := range batch {
		slot := redisx.Slot(entry.Key)
		addr := fw.clusterClient.MasterAddr(slot)
		if addr == "" {
			// Fallback or log error? Use empty addr which might fail later or use random?
			// Should strictly not happen if topology is known.
			// Log once per batch?
			continue
		}
		groups[addr] = append(groups[addr], entry)
	}
	return groups
}

// writeNodeBatch writes a batch of entries to a specific node (or standalone)
func (fw *FlowWriter) writeNodeBatch(addr string, entries []*RDBEntry) writeResult {
	var successCount, failCount int

	// Get Client
	var client *redisx.Client
	var err error

	if fw.targetType == "redis-standalone" {
		client = fw.pipelineClient
	} else {
		// Cluster mode: get client for node
		client, err = fw.clusterClient.GetNodeClient(addr)
		if err != nil {
			log.Printf("  [FLOW-%d] [WRITER] ✗ Failed to get client for node %s: %v", fw.flowID, addr, err)
			return writeResult{failed: len(entries)}
		}
	}

	if client == nil {
		log.Printf("  [FLOW-%d] [WRITER] ✗ Client is nil (addr=%s)", fw.flowID, addr)
		return writeResult{failed: len(entries)}
	}

	// ----------------------------------------------------------------------
	// Recursively split large batches (same logic as before)
	// ----------------------------------------------------------------------
	const maxPipelineSize = 500
	if len(entries) > maxPipelineSize {
		for i := 0; i < len(entries); i += maxPipelineSize {
			end := i + maxPipelineSize
			if end > len(entries) {
				end = len(entries)
			}
			chunk := entries[i:end]
			result := fw.writeNodeBatch(addr, chunk) // Recursive
			successCount += result.success
			failCount += result.failed
		}
		return writeResult{success: successCount, failed: failCount}
	}

	// ----------------------------------------------------------------------
	// Build Pipeline
	// ----------------------------------------------------------------------
	cmds := make([][]interface{}, 0, len(entries))
	for _, entry := range entries {
		entryCmds := fw.buildCommands(entry)
		if len(entryCmds) > 0 {
			cmds = append(cmds, entryCmds...)
		}
	}

	if len(cmds) == 0 {
		return writeResult{success: 0, failed: 0}
	}

	// Execute Pipeline
	// client.Pipeline handles serialization. In Cluster mode, 'client' is unique per Node,
	// so parallelism is achieved across nodes.
	results, err := client.Pipeline(cmds)
	if err != nil {
		log.Printf("  [FLOW-%d] [WRITER] ✗ Pipeline failed to %s: %v", fw.flowID, addr, err)
		// Fallback to sequential?
		// Given we want speed, maybe just fail?
		// Or try individual?
		// Use sequential fallback just in case.
		return fw.writeSequential(client, entries)
	}

	// Check results
	for _, result := range results {
		if result != nil {
			if errStr, ok := result.(string); ok && strings.HasPrefix(errStr, "ERR") {
				// log.Printf("  [FLOW-%d] [WRITER] ✗ Command failed: %v", fw.flowID, errStr)
				failCount++
			} else if errRes, ok := result.(error); ok {
				// Pipeline might return error in slice? No, usually values or error.
				// redisx uses specific return type.
				// Our redisx.Pipeline returns []interface{}.
				// If error, it returns err.
				// The result element itself is the RESP reply.
				// Error replies start with - (e.g. -MOVED).
				// We should check for MOVED?
				// If MOVED, we routed wrong. Recovery?
				// For now, count as fail.
				if strings.HasPrefix(fmt.Sprintf("%v", errRes), "MOVED") {
					log.Printf("  [FLOW-%d] [WRITER] ✗ MOVED error on %s", fw.flowID, addr)
					failCount++
				} else {
					successCount++
				}
			} else {
				successCount++
			}
		} else {
			successCount++
		}
	}

	return writeResult{success: successCount, failed: failCount}
}

// writeSequential falls back to writing entries one by one
func (fw *FlowWriter) writeSequential(client *redisx.Client, entries []*RDBEntry) writeResult {
	var success, failed int
	for _, entry := range entries {
		if err := fw.writeEntryWithClient(client, entry); err != nil {
			failed++
		} else {
			success++
		}
	}
	return writeResult{success: success, failed: failed}
}

// writeEntryWithClient writes a single entry using specific client
func (fw *FlowWriter) writeEntryWithClient(client *redisx.Client, entry *RDBEntry) error {
	cmds := fw.buildCommands(entry)
	if len(cmds) == 0 {
		return nil
	}

	// Execute all commands for this entry (e.g. SET + PEXPIREAT)
	for _, cmd := range cmds {
		if len(cmd) == 0 {
			continue
		}
		cmdName := fmt.Sprint(cmd[0])
		args := cmd[1:]
		if _, err := client.Do(cmdName, args...); err != nil {
			return err
		}
	}
	return nil
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
