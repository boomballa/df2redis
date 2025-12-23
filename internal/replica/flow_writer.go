package replica

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

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

	// Concurrency control
	maxConcurrentWrites int        // Maximum concurrent write goroutines
	writeSemaphore      chan struct{} // Semaphore to limit concurrency

	// Statistics
	stats struct {
		totalReceived int64
		totalWritten  int64
		totalBatches  int64
		mu            sync.Mutex
	}

	// Backpressure
	channelCapacity int
}

// NewFlowWriter creates a new async batch writer for a flow
func NewFlowWriter(flowID int, writeFn func(*RDBEntry) error) *FlowWriter {
	ctx, cancel := context.WithCancel(context.Background())

	// Balance between throughput and Dragonfly stability:
	// - Too low: causes channel backpressure and Dragonfly heartbeat stalls
	// - Too high: overwhelms Dragonfly with concurrent requests
	// For multi-FLOW scenarios (8+ FLOWs), lower concurrency per flow to avoid
	// total system overload (e.g., 8 flows × 15 = 120 concurrent goroutines)
	maxConcurrent := 8   // Limit to 8 concurrent slot writes (balanced for multi-FLOW)
	batchSize := 100     // Process 100 entries per batch
	flushInterval := 50  // Flush every 50ms (keep responsive)

	channelBuffer := 2000 // Increase buffer to 2000 to handle LZ4 decompression bursts

	fw := &FlowWriter{
		flowID:              flowID,
		entryChan:           make(chan *RDBEntry, channelBuffer),           // Larger buffer for LZ4 workloads
		batchSize:           batchSize,                                     // Batch every 100 entries
		flushInterval:       time.Duration(flushInterval) * time.Millisecond, // Flush every 50ms
		writeFn:             writeFn,
		ctx:                 ctx,
		cancel:              cancel,
		channelCapacity:     channelBuffer,
		maxConcurrentWrites: maxConcurrent,
		writeSemaphore:      make(chan struct{}, maxConcurrent), // Semaphore for concurrency control
	}

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
func (fw *FlowWriter) Enqueue(entry *RDBEntry) error {
	select {
	case fw.entryChan <- entry:
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

	// Group entries by slot for parallel processing
	slotGroups := fw.groupBySlot(batch)
	numGroups := len(slotGroups)

	// Process groups in parallel using goroutines with concurrency limit
	var wg sync.WaitGroup
	resultChan := make(chan writeResult, numGroups)

	for _, group := range slotGroups {
		// Acquire semaphore slot (blocks if limit reached)
		fw.writeSemaphore <- struct{}{}

		wg.Add(1)
		go func(entries []*RDBEntry) {
			defer wg.Done()
			defer func() { <-fw.writeSemaphore }() // Release semaphore slot

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

	// Return all entries to pool after processing
	for _, entry := range batch {
		PutRDBEntry(entry)
	}

	duration := time.Since(start)

	// Update statistics
	fw.stats.mu.Lock()
	fw.stats.totalWritten += int64(successCount)
	fw.stats.totalBatches++
	fw.stats.mu.Unlock()

	// Log performance
	opsPerSec := float64(batchSize) / duration.Seconds()
	log.Printf("  [FLOW-%d] [WRITER] ✓ Batch complete: %d entries in %v (%.0f ops/sec, groups=%d, success=%d, fail=%d)",
		fw.flowID, batchSize, duration, opsPerSec, numGroups, successCount, failCount)

	// Warn if slow
	if duration > time.Second {
		log.Printf("  [FLOW-%d] [WRITER] ⚠ Slow batch detected: %v for %d entries",
			fw.flowID, duration, batchSize)
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

	// Write entries sequentially (can add pipelining here in future)
	for _, entry := range entries {
		if err := fw.writeEntry(entry); err != nil {
			log.Printf("  [FLOW-%d] [WRITER] ✗ Failed to write key=%s: %v", fw.flowID, entry.Key, err)
			failCount++
		} else {
			successCount++
		}
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

