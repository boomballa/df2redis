package replica

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"df2redis/internal/cluster"
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

	// Cluster client for Redis Cluster pipeline operations (slot-based batching)
	// Type assertion needed to access PipelineForSlot method
	clusterClientRaw interface{} // Store original for debugging

	// Concurrency control
	maxConcurrentWrites int        // Maximum concurrent write goroutines
	writeSemaphore      chan struct{} // Semaphore to limit concurrency

	// Statistics
	stats struct {
		totalReceived int64
		totalWritten  int64
		totalFailed   int64 // CRITICAL: Track failed writes for data loss detection
		totalSkipped  int64 // Track skipped entries (e.g., unsupported types)
		totalBatches  int64
		mu            sync.Mutex
	}

	// Debug mode enables detailed failure logging
	debugMode bool

	// Backpressure and monitoring
	channelCapacity    int
	lastMonitorTime    time.Time
	monitorInterval    time.Duration
	highWatermarkCount int64 // Count of times channel usage exceeded 80%
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
	// - Cluster mode: node-based parallel writes = higher concurrency needed
	// - Physical machine (56 cores, 200G RAM) can handle high concurrency
	//
	// Strategy:
	//   - Standalone: 1 concurrent per FLOW (pipeline handles batching)
	//   - Cluster: adaptive concurrency based on FLOW count
	//
	// Examples:
	//   Standalone mode (any FLOWs): 1 concurrent per FLOW (pipeline does the work)
	//   Cluster mode - 2 FLOWs:  800/2  = 400 → 200 concurrent per FLOW
	//   Cluster mode - 8 FLOWs:  800/8  = 100 → 100 concurrent per FLOW
	//   Cluster mode - 16 FLOWs: 800/16 = 50  → 50 concurrent per FLOW
	//
	// TODO: Implement node-aware adaptive concurrency for large clusters
	// Current implementation uses fixed concurrency limits (50-200 per FLOW).
	// For large Redis Clusters (20+ nodes), we should:
	//   1. Query cluster node count via ClusterClient.GetNodeCount()
	//   2. Calculate optimal concurrency: nodeCount * K (e.g., K=3 concurrent per node)
	//   3. Cap at reasonable limits: min(nodeCount * 3, 50) to avoid excessive goroutines
	// Example: 20-node cluster → 60 concurrent per FLOW (vs current 50-200)
	// This ensures write throughput scales with cluster size while maintaining stability.

	var maxConcurrent int
	if targetType == "redis-standalone" {
		// Standalone mode: pipeline batching, only need 1 concurrent per FLOW
		maxConcurrent = 1
	} else {
		// Cluster mode: node-based parallelism, need moderate concurrency
		// TODO: Replace hardcoded limit with node-aware calculation
		//
		// CRITICAL FIX: Restore high concurrency to match RDB Parser speed
		//
		// Root cause analysis from actual measurements:
		//   - With 200 total concurrency: FlowWriter = 34k ops/sec (batch: 14ms)
		//   - With 800 total concurrency: FlowWriter = 178k ops/sec (batch: 2.8ms)
		//   - RDB Parser speed: ~200k ops/sec
		//
		// Problem with low concurrency (200):
		//   - Each FLOW only gets 25 concurrent goroutines (200 / 8)
		//   - Semaphore blocks frequently, causing pipeline delays
		//   - Batch execution time: 14ms (5x slower than 2.8ms)
		//   - FlowWriter speed: 34k (5x slower than needed)
		//   - Result: Channel still fills up → FLOW-6 drops
		//
		// Solution: Restore high concurrency (800)
		//   - Each FLOW gets 100 concurrent goroutines (800 / 8)
		//   - Less semaphore blocking, faster pipeline execution
		//   - Batch execution time: ~2.8ms (5x faster)
		//   - FlowWriter speed: ~178k (matches Parser speed)
		//   - Result: Channel won't fill up → no FLOW drops
		//
		// Note: High concurrency does NOT overwhelm Dragonfly because:
		//   1. Pipeline batching reduces network round trips
		//   2. FlowWriter processes incoming data, doesn't generate load
		//   3. Speed is limited by RDB Parser (~200k), not concurrency
		totalConcurrencyLimit := 800 // Restore high concurrency for speed
		maxConcurrent = totalConcurrencyLimit / numFlows
		if maxConcurrent < 50 {
			maxConcurrent = 50
		}
		if maxConcurrent > 200 {
			maxConcurrent = 200
		}
	}

	// CRITICAL FIX: Optimized batch size and flush interval
	//
	// Batch size: 500 entries per batch
	//   - Small enough to prevent memory spikes
	//   - Large enough for efficient pipeline batching
	//   - With 2.8ms execution time → ~178k entries/sec throughput
	//   - Matches RDB Parser speed (~200k/sec) to prevent channel backpressure
	//
	// Flush interval: 3000ms (3 seconds)
	//   - Allows batch to accumulate when Dragonfly sends slowly
	//   - Prevents premature small batch flushes
	//   - Trade-off: latency vs throughput (we choose throughput)
	batchSize := 500      // Process 500 entries per batch (balanced size)
	flushInterval := 3000 // Flush every 3 seconds (accumulation window)

	// CRITICAL FIX: Increased buffer to prevent channel backpressure
	// Root cause analysis: Channel 100% full caused ParseNext() blocking → socket read blocking
	// → Dragonfly Write() timeout (30s) → connection drop → incomplete FLOW transfer
	//
	// Previous analysis:
	//   - RDB Parser speed: ~200k entries/sec (pure memory operations)
	//   - FlowWriter speed with 10ms delay: ~39k entries/sec (5x slower!)
	//   - Channel fills up in 1 second: 200k buffer / (200k - 39k) = 1.24s
	//
	// Solution:
	//   - Remove 10ms inter-batch delay (see below)
	//   - Increase buffer from 200K → 500K for safety margin
	//   - FlowWriter speed without delay: ~178k entries/sec (matches Parser)
	//
	// Memory usage: 2M entries × ~1KB/entry = ~2GB per FLOW (8 FLOWs = 16GB total)
	// This is acceptable on modern servers (56 cores, 200G RAM)
	//
	// Buffer sizing calculation:
	//   - Measured FlowWriter speed: 40k entries/sec (slow due to Redis Cluster)
	//   - Measured Parser speed: 200k entries/sec
	//   - Speed difference: 160k entries/sec
	//   - Dragonfly timeout: 30 seconds
	//   - Required buffer: 160k × 30s = 4.8M entries
	//   - Use 2M as conservative value (gives ~12.5 seconds buffer time)
	channelBuffer := 2000000 // Huge buffer to survive Dragonfly 30s timeout (increased from 500K)

	// Check for debug mode via environment variable DF2REDIS_DEBUG
	debugMode := false
	if debugEnv := os.Getenv("DF2REDIS_DEBUG"); debugEnv == "1" || debugEnv == "true" {
		debugMode = true
		log.Printf("  [FLOW-%d] [INIT] ⚠ DEBUG MODE ENABLED - detailed failure logging active", flowID)
	}

	fw := &FlowWriter{
		flowID:              flowID,
		entryChan:           make(chan *RDBEntry, channelBuffer),             // Huge buffer for full sync bursts
		batchSize:           batchSize,                                       // Large batch for pipeline efficiency
		flushInterval:       time.Duration(flushInterval) * time.Millisecond, // Match batch size
		writeFn:             writeFn,
		targetType:          targetType,
		pipelineClient:      pipelineClient,
		isCluster:           isCluster,
		clusterClientRaw:    clusterClient,
		ctx:                 ctx,
		cancel:              cancel,
		channelCapacity:     channelBuffer,
		maxConcurrentWrites: maxConcurrent,
		writeSemaphore:      make(chan struct{}, maxConcurrent), // Semaphore for concurrency control
		debugMode:           debugMode,                          // Enable detailed failure logging
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
	log.Printf("    • Concurrency: %d per FLOW (%d total across %d FLOWs)", maxConcurrent, maxConcurrent*numFlows, numFlows)
	log.Printf("    • Batch size: %d entries", batchSize)
	log.Printf("    • Buffer size: %d entries (~%dMB)", channelBuffer, channelBuffer/1000)
	log.Printf("    • Flush interval: %v", time.Duration(flushInterval)*time.Millisecond)
	log.Printf("    • Inter-batch delay: REMOVED (prevents channel backpressure)")
	log.Printf("    • Expected throughput: ~178k entries/sec (matches RDB Parser speed)")

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

// Enqueue adds an entry to the write queue (blocks if channel is full - backpressure)
//
// CRITICAL PERFORMANCE: This function is called at very high frequency (200k/sec per FLOW).
// It MUST be fast and lock-free to avoid blocking RDB Parser and causing Dragonfly timeout.
func (fw *FlowWriter) Enqueue(entry *RDBEntry) error {
	select {
	case fw.entryChan <- entry:
		// CRITICAL: Use atomic increment instead of mutex lock
		// This is 10-100x faster than mutex in high-contention scenarios
		// With 8 FLOWs at 200k/sec each, mutex causes severe lock contention
		atomic.AddInt64(&fw.stats.totalReceived, 1)
		return nil
	case <-fw.ctx.Done():
		return fmt.Errorf("flow writer stopped")
	}
}

// GetStats returns current statistics (lock-free using atomic loads)
func (fw *FlowWriter) GetStats() (received, written, failed, skipped, batches int64) {
	return atomic.LoadInt64(&fw.stats.totalReceived),
		atomic.LoadInt64(&fw.stats.totalWritten),
		atomic.LoadInt64(&fw.stats.totalFailed),
		atomic.LoadInt64(&fw.stats.totalSkipped),
		atomic.LoadInt64(&fw.stats.totalBatches)
}

// batchWriteLoop is the main async write loop
func (fw *FlowWriter) batchWriteLoop() {
	defer fw.wg.Done()

	batch := make([]*RDBEntry, 0, fw.batchSize)
	ticker := time.NewTicker(fw.flushInterval)
	defer ticker.Stop()

	log.Printf("  [FLOW-%d] [WRITER] Async batch writer started (batch=%d, interval=%v)",
		fw.flowID, fw.batchSize, fw.flushInterval)

	for {
		select {
		case entry, ok := <-fw.entryChan:
			if !ok {
				// Channel closed, flush remaining batch and exit
				if len(batch) > 0 {
					fw.flushBatch(batch)
				}
				log.Printf("  [FLOW-%d] [WRITER] Channel closed, shutting down (total written: %d, batches: %d)",
					fw.flowID, fw.stats.totalWritten, fw.stats.totalBatches)
				return
			}

			batch = append(batch, entry)

			// Flush if batch size reached
			if len(batch) >= fw.batchSize {
				fw.flushBatch(batch)
				batch = batch[:0] // Reset batch
			}

		case <-ticker.C:
			// Flush on timer if batch not empty
			if len(batch) > 0 {
				fw.flushBatch(batch)
				batch = batch[:0]
			}

		case <-fw.ctx.Done():
			// Context cancelled, flush and exit
			if len(batch) > 0 {
				fw.flushBatch(batch)
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
	// - Cluster: group by node (NOT slot!) for better batching
	//
	// KEY INSIGHT: Grouping by slot (16384 slots) causes extreme fragmentation.
	// Even with 100 entries, they scatter across 50-100 different slots,
	// resulting in 1-2 entries per group. Pipeline has no benefit!
	//
	// Solution: Group by node (typically 3-6 master nodes). This concentrates
	// entries into fewer groups (30-50 entries each), maximizing pipeline efficiency.
	var nodeGroups map[string][]*RDBEntry
	var numGroups int

	if fw.targetType == "redis-standalone" {
		// Standalone mode: single group for all entries
		nodeGroups = map[string][]*RDBEntry{"standalone": batch}
		numGroups = 1
	} else {
		// Cluster mode: group entries by target node (NOT slot!)
		nodeGroups = fw.groupByNode(batch)
		numGroups = len(nodeGroups)
	}

	// Process groups in parallel using goroutines with concurrency limit
	var wg sync.WaitGroup
	resultChan := make(chan writeResult, numGroups)

	for nodeAddr, group := range nodeGroups {
		// Acquire semaphore slot (blocks if limit reached)
		fw.writeSemaphore <- struct{}{}

		wg.Add(1)
		go func(addr string, entries []*RDBEntry) {
			defer wg.Done()
			defer func() { <-fw.writeSemaphore }() // Release semaphore slot

			result := fw.writeNodeGroup(addr, entries)
			resultChan <- result
		}(nodeAddr, group)
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

	// Return all entries to pool after processing
	for _, entry := range batch {
		PutRDBEntry(entry)
	}

	duration := time.Since(start)

	// Update statistics
	fw.stats.mu.Lock()
	fw.stats.totalWritten += int64(successCount)
	fw.stats.totalFailed += int64(failCount)
	fw.stats.totalBatches++
	fw.stats.mu.Unlock()

	// Log performance
	opsPerSec := float64(batchSize) / duration.Seconds()
	log.Printf("  [FLOW-%d] [WRITER] ✓ Batch complete: %d entries in %v (%.0f ops/sec, groups=%d, success=%d, fail=%d)",
		fw.flowID, batchSize, duration, opsPerSec, numGroups, successCount, failCount)

	// CRITICAL: Warn if any failures detected
	if failCount > 0 {
		failRate := float64(failCount) / float64(batchSize) * 100
		log.Printf("  [FLOW-%d] [WRITER] ⚠ FAILURE DETECTED: %d/%d entries failed (%.1f%%) in batch",
			fw.flowID, failCount, batchSize, failRate)
	}

	// Warn if slow
	if duration > time.Second {
		log.Printf("  [FLOW-%d] [WRITER] ⚠ Slow batch detected: %v for %d entries",
			fw.flowID, duration, batchSize)
	}

	// CRITICAL FIX: Removed 10ms inter-batch delay (was causing channel backpressure)
	//
	// Previous implementation added 10ms delay to prevent overwhelming Dragonfly,
	// but this caused a different problem:
	//   - FlowWriter throughput: 500 entries / (2.8ms + 10ms) = 39k entries/sec
	//   - RDB Parser throughput: 200k entries/sec
	//   - Result: Channel fills up in 1 second → ParseNext() blocks → socket blocks
	//            → Dragonfly Write() timeout → connection drop
	//
	// New approach:
	//   - Remove delay, let FlowWriter run at full speed (~178k entries/sec)
	//   - Speed control via concurrency limits (200 total, 10-30 per FLOW)
	//   - Pipeline batching naturally prevents overwhelming Redis
	//   - Increased channel buffer (500K) provides safety margin
	//
	// This prevents channel backpressure while maintaining controlled Redis write speed.
}

// writeResult holds the result of writing a slot group
type writeResult struct {
	success int
	failed  int
}

// groupByNode groups entries by Redis Cluster node (NOT by slot!)
// This is critical for pipeline efficiency: grouping by node (3-6 groups)
// is far better than grouping by slot (50-100+ groups).
func (fw *FlowWriter) groupByNode(batch []*RDBEntry) map[string][]*RDBEntry {
	nodeMap := make(map[string][]*RDBEntry)

	// Try to get cluster client
	clusterClient, ok := fw.clusterClientRaw.(*cluster.ClusterClient)
	if !ok || clusterClient == nil {
		// Fallback: treat all as unknown node
		log.Printf("  [FLOW-%d] [WARNING] Cannot get cluster client, using fallback grouping", fw.flowID)
		nodeMap["unknown"] = batch
		return nodeMap
	}

	// Group entries by target node
	for _, entry := range batch {
		slot := calculateSlot(entry.Key)
		nodeAddr, ok := clusterClient.GetNodeForSlot(int(slot))
		if !ok {
			// Unknown slot, use fallback
			nodeAddr = "unknown"
		}
		nodeMap[nodeAddr] = append(nodeMap[nodeAddr], entry)
	}

	return nodeMap
}

// groupBySlot groups entries by Redis Cluster slot (DEPRECATED - causes fragmentation)
// Kept for reference but no longer used. Use groupByNode() instead.
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

// writeNodeGroup writes a group of entries to the same Redis Cluster node using pipeline
func (fw *FlowWriter) writeNodeGroup(nodeAddr string, entries []*RDBEntry) writeResult {
	var successCount, failCount int
	startTime := time.Now()

	// CRITICAL: All entries in this group belong to the same node
	// Use pipeline to batch them into a single network round trip
	log.Printf("  [FLOW-%d] [WRITER] writeNodeGroup called: entries=%d, node=%s, targetType=%s",
		fw.flowID, len(entries), nodeAddr, fw.targetType)

	// Standalone mode: use pipeline for batch writes
	if fw.targetType == "redis-standalone" && fw.pipelineClient != nil {
		log.Printf("  [FLOW-%d] [WRITER] ✓ Using PIPELINE mode for %d entries", fw.flowID, len(entries))

		// Build pipeline commands for all entries
		buildStart := time.Now()
		cmds := make([][]interface{}, 0, len(entries))
		skippedCount := 0
		for _, entry := range entries {
			cmd := fw.buildCommand(entry)
			if cmd != nil {
				cmds = append(cmds, cmd)
			} else {
				// CRITICAL: Track skipped entries (unsupported types like Stream, empty collections)
				skippedCount++
				if fw.debugMode {
					log.Printf("  [FLOW-%d] [WRITER] [DEBUG] Skipped key=%s type=%d (unsupported for pipeline)",
						fw.flowID, entry.Key, entry.Type)
				}
			}
		}
		buildDuration := time.Since(buildStart)

		// Update skip statistics
		if skippedCount > 0 {
			fw.stats.mu.Lock()
			fw.stats.totalSkipped += int64(skippedCount)
			fw.stats.mu.Unlock()
		}

		log.Printf("  [FLOW-%d] [WRITER] Built %d commands in %v (skipped=%d)", fw.flowID, len(cmds), buildDuration, skippedCount)

		// Execute pipeline batch
		if len(cmds) > 0 {
			pipelineStart := time.Now()
			results, err := fw.pipelineClient.Pipeline(cmds)
			pipelineDuration := time.Since(pipelineStart)

			if err != nil {
				log.Printf("  [FLOW-%d] [WRITER] ✗ Pipeline failed after %v: %v, fallback to sequential",
					fw.flowID, pipelineDuration, err)
				// Fallback to sequential writes on pipeline failure
				goto sequential
			}

			log.Printf("  [FLOW-%d] [WRITER] ✓ Pipeline executed in %v (%.0f ops/sec)",
				fw.flowID, pipelineDuration, float64(len(cmds))/pipelineDuration.Seconds())

			// Check results and track failures
			for i, result := range results {
				if result != nil {
					// Check if result is an error
					if errStr, ok := result.(string); ok && strings.HasPrefix(errStr, "ERR") {
						// CRITICAL: Record failure with detailed info in debug mode
						if fw.debugMode && i < len(entries) {
							log.Printf("  [FLOW-%d] [WRITER] [DEBUG] ✗ Command %d failed for key=%s: %v",
								fw.flowID, i, entries[i].Key, errStr)
						} else {
							log.Printf("  [FLOW-%d] [WRITER] ✗ Command %d failed: %v", fw.flowID, i, errStr)
						}
						failCount++
					} else {
						successCount++
					}
				} else {
					successCount++
				}
			}

			totalDuration := time.Since(startTime)
			log.Printf("  [FLOW-%d] [WRITER] ✓ Pipeline batch complete: %d success, %d failed in %v",
				fw.flowID, successCount, failCount, totalDuration)

			return writeResult{success: successCount, failed: failCount}
		} else {
			log.Printf("  [FLOW-%d] [WRITER] ⚠ No commands built, all entries skipped", fw.flowID)
		}
	}

	// CRITICAL OPTIMIZATION: Cluster mode pipeline support
	// Use PipelineForNode for batch writes to the same node (grouped by groupByNode())
	if fw.targetType == "redis-cluster" && len(entries) > 0 {
		// Try to cast cluster client
		if clusterClient, ok := fw.clusterClientRaw.(*cluster.ClusterClient); ok && clusterClient != nil {
			log.Printf("  [FLOW-%d] [WRITER] ✓ Using CLUSTER PIPELINE mode (node-based) for %d entries to node=%s",
				fw.flowID, len(entries), nodeAddr)

			// Build pipeline commands for all entries
			// Note: All entries in this group already belong to the same node (grouped by groupByNode())
			buildStart := time.Now()
			cmds := make([][]interface{}, 0, len(entries))
			skippedCount := 0
			for _, entry := range entries {
				cmd := fw.buildCommand(entry)
				if cmd != nil {
					cmds = append(cmds, cmd)
				} else {
					// CRITICAL: Track skipped entries (unsupported types like Stream, empty collections)
					skippedCount++
					if fw.debugMode {
						log.Printf("  [FLOW-%d] [WRITER] [DEBUG] Skipped key=%s type=%d (unsupported for pipeline)",
							fw.flowID, entry.Key, entry.Type)
					}
				}
			}
			buildDuration := time.Since(buildStart)

			// Update skip statistics
			if skippedCount > 0 {
				fw.stats.mu.Lock()
				fw.stats.totalSkipped += int64(skippedCount)
				fw.stats.mu.Unlock()
			}

			log.Printf("  [FLOW-%d] [WRITER] Built %d commands in %v for node %s (skipped=%d)",
				fw.flowID, len(cmds), buildDuration, nodeAddr, skippedCount)

			// Execute pipeline batch for this node
			if len(cmds) > 0 {
				pipelineStart := time.Now()
				results, err := clusterClient.PipelineForNode(nodeAddr, cmds)
				pipelineDuration := time.Since(pipelineStart)

				if err != nil {
					log.Printf("  [FLOW-%d] [WRITER] ✗ Node pipeline failed after %v: %v, fallback to sequential",
						fw.flowID, pipelineDuration, err)
					// Fallback to sequential writes on pipeline failure
					goto sequential
				}

				log.Printf("  [FLOW-%d] [WRITER] ✓ Node pipeline executed in %v (%.0f ops/sec)",
					fw.flowID, pipelineDuration, float64(len(cmds))/pipelineDuration.Seconds())

				// Check results and track failures
				for i, result := range results {
					if result != nil {
						// Check if result is an error
						if errStr, ok := result.(string); ok && strings.HasPrefix(errStr, "ERR") {
							// CRITICAL: Record failure with detailed info in debug mode
							if fw.debugMode && i < len(entries) {
								log.Printf("  [FLOW-%d] [WRITER] [DEBUG] ✗ Command %d failed for key=%s: %v",
									fw.flowID, i, entries[i].Key, errStr)
							} else {
								log.Printf("  [FLOW-%d] [WRITER] ✗ Command %d failed: %v", fw.flowID, i, errStr)
							}
							failCount++
						} else {
							successCount++
						}
					} else {
						successCount++
					}
				}

				totalDuration := time.Since(startTime)
				log.Printf("  [FLOW-%d] [WRITER] ✓ Node pipeline batch complete: %d success, %d failed in %v",
					fw.flowID, successCount, failCount, totalDuration)

				return writeResult{success: successCount, failed: failCount}
			} else {
				log.Printf("  [FLOW-%d] [WRITER] ⚠ No commands built, all entries skipped", fw.flowID)
			}
		} else {
			log.Printf("  [FLOW-%d] [WRITER] ⚠ Cluster client not available, fallback to sequential (type=%T)",
				fw.flowID, fw.clusterClientRaw)
		}
	}

sequential:
	// Fallback: write entries sequentially
	log.Printf("  [FLOW-%d] [WRITER] Sequential write started for %d entries", fw.flowID, len(entries))
	seqStart := time.Now()

	for _, entry := range entries {
		if err := fw.writeEntry(entry); err != nil {
			log.Printf("  [FLOW-%d] [WRITER] ✗ Failed to write key=%s: %v", fw.flowID, entry.Key, err)
			failCount++
		} else {
			successCount++
		}
	}

	seqDuration := time.Since(seqStart)
	log.Printf("  [FLOW-%d] [WRITER] Sequential write complete: %d success, %d failed in %v (%.0f ops/sec)",
		fw.flowID, successCount, failCount, seqDuration, float64(len(entries))/seqDuration.Seconds())

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

