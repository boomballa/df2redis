package replica

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"df2redis/internal/checkpoint"
	"df2redis/internal/config"
	"df2redis/internal/redisx"
	"df2redis/internal/state"
)

// Replicator establishes the replication relationship with Dragonfly
type Replicator struct {
	cfg    *config.Config
	ctx    context.Context
	cancel context.CancelFunc

	// Primary connection (used for handshake)
	mainConn *redisx.Client

	// Dedicated connections for each FLOW
	flowConns []*redisx.Client

	// Redis Cluster client (replay commands)
	clusterClient *redisx.ClusterClient

	// Checkpoint manager
	checkpointMgr *checkpoint.Manager

	// Replication state
	state      ReplicaState
	masterInfo MasterInfo
	flows      []FlowInfo

	// Configuration
	listeningPort int
	announceIP    string

	// Replay statistics
	replayStats ReplayStats

	// RDB snapshot statistics
	rdbStats RDBStats

	// Automatic checkpoint saving
	checkpointInterval time.Duration
	lastCheckpointTime time.Time

	// Channel used to wait for Start() to finish
	done chan struct{}

	// State/metrics reporting
	store             *state.Store
	metrics           *metricsRecorder
	metricsMu         sync.Mutex
	flowKeyCounts     []int64
	flowLSNs          []uint64
	totalSyncedKeys   int64
	initialTargetKeys float64
}

// NewReplicator creates a new replicator
func NewReplicator(cfg *config.Config) *Replicator {
	ctx, cancel := context.WithCancel(context.Background())

	// Checkpoint file path: use configured path or the default path
	checkpointPath := cfg.ResolveCheckpointPath()

	// Checkpoint save interval: read from config (default 10 seconds)
	checkpointInterval := time.Duration(cfg.Checkpoint.Interval) * time.Second

	return &Replicator{
		cfg:                cfg,
		ctx:                ctx,
		cancel:             cancel,
		state:              StateDisconnected,
		listeningPort:      16379, // default port
		checkpointMgr:      checkpoint.NewManager(checkpointPath),
		checkpointInterval: checkpointInterval,
		done:               make(chan struct{}),
	}
}

// AttachStateStore wires a state store for dashboard metrics.
func (r *Replicator) AttachStateStore(store *state.Store) {
	r.store = store
	if store != nil && r.metrics == nil {
		r.metrics = newMetricsRecorder(store)
	}
}

// Start launches the replication workflow
func (r *Replicator) Start() error {
	defer close(r.done) // ensure Stop() gets notified when exiting
	if r.metrics != nil {
		defer r.metrics.Close()
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🚀 Starting Dragonfly replicator")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	r.recordPipelineStatus("handshake", "Connecting to Dragonfly")
	r.recordStage("replicator", "starting", "Starting replicator")

	// Connect to Dragonfly
	if err := r.connect(); err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Connection failed: %v", err))
		return fmt.Errorf("connection failed: %w", err)
	}

	// Perform handshake
	if err := r.handshake(); err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Handshake failed: %v", err))
		return fmt.Errorf("handshake failed: %w", err)
	}
	r.recordPipelineStatus("full_sync", "Receiving RDB snapshot")
	r.estimateSourceKeys()

	// Clear old FLOW stages from previous runs
	r.clearOldFlowStages()

	// Initialize Redis client (auto-detects cluster/standalone)
	log.Println("")
	log.Println("🔗 Connecting to target Redis...")

	seeds := r.cfg.Target.Cluster.Seeds
	if len(seeds) == 0 {
		seeds = []string{r.cfg.Target.Addr}
	}

	var err error
	r.clusterClient, err = redisx.DialCluster(r.ctx, seeds, r.cfg.Target.Password)
	if err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Failed to connect to target Redis: %v", err))
		return fmt.Errorf("failed to connect to target Redis: %w", err)
	}
	r.estimateTargetKeys()

	// Detect topology
	masterCount := r.clusterClient.MasterCount()
	if masterCount > 1 {
		log.Printf("  ✓ Connected to Redis Cluster (%d masters)", masterCount)
	} else {
		log.Println("  ✓ Connected to Redis (Single/Standalone)")
	}

	// Send DFLY SYNC to trigger the RDB transfer
	if err := r.sendDflySync(); err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Sending DFLY SYNC failed: %v", err))
		return fmt.Errorf("sending DFLY SYNC failed: %w", err)
	}

	// Receive snapshot in parallel
	r.state = StateFullSync
	if err := r.receiveSnapshot(); err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Snapshot reception failed: %v", err))
		return fmt.Errorf("snapshot reception failed: %w", err)
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🎯 Replicator started successfully!")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Receive and parse the journal stream
	// Note: Pipeline status will be updated to "incremental" when journal stream starts
	if err := r.receiveJournal(); err != nil {
		r.recordPipelineStatus("error", fmt.Sprintf("Journal stream reception failed: %v", err))
		return fmt.Errorf("journal stream reception failed: %w", err)
	}
	r.recordPipelineStatus("completed", "Journal stream finished")

	return nil
}

// Stop halts replication
func (r *Replicator) Stop() {
	log.Println("⏸  Stopping replicator...")

	// Cancel the context first
	r.cancel()

	// Close all connections immediately so blocking reads fail fast
	if r.mainConn != nil {
		r.mainConn.Close()
	}
	for i, conn := range r.flowConns {
		if conn != nil {
			log.Printf("  • Closing FLOW-%d connection", i)
			conn.Close()
		}
	}

	// Wait for Start() to finish (including checkpoint persistence)
	log.Println("  • Waiting for all goroutines to exit...")
	<-r.done

	r.state = StateStopped
	r.recordPipelineStatus("stopped", "Replicator stopped")
	log.Println("✓ Replicator stopped")
}

// connect creates the primary connection to Dragonfly for the handshake
func (r *Replicator) connect() error {
	r.state = StateConnecting
	log.Printf("🔗 Connecting to Dragonfly: %s", r.cfg.Source.Addr)

	dialCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()

	client, err := redisx.Dial(dialCtx, redisx.Config{
		Addr:     r.cfg.Source.Addr,
		Password: r.cfg.Source.Password,
		TLS:      r.cfg.Source.TLS,
	})

	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", r.cfg.Source.Addr, err)
	}

	r.mainConn = client
	log.Printf("✓ Primary connection established")

	return nil
}

// handshake performs the full handshake procedure
func (r *Replicator) handshake() error {
	r.state = StateHandshaking
	log.Println("")
	log.Println("🤝 Starting handshake")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Step 1: PING
	log.Println("  [1/6] Sending PING...")
	if err := r.sendPing(); err != nil {
		return err
	}
	log.Println("  ✓ PONG received")

	// Step 2: REPLCONF listening-port
	log.Printf("  [2/6] Declaring listening port: %d...", r.listeningPort)
	if err := r.sendListeningPort(); err != nil {
		return err
	}
	log.Println("  ✓ Listening port registered")

	// Step 3: REPLCONF ip-address (optional)
	if r.announceIP != "" {
		log.Printf("  [3/6] Declaring IP address: %s...", r.announceIP)
		if err := r.sendIPAddress(); err != nil {
			log.Printf("  ⚠ Failed to register IP address (primary may be older): %v", err)
		} else {
			log.Println("  ✓ IP address registered")
		}
	} else {
		log.Println("  [3/6] Skipping IP address declaration")
	}

	// Step 4: REPLCONF capa eof psync2
	log.Println("  [4/6] Declaring capabilities: eof psync2...")
	if err := r.sendCapaEOF(); err != nil {
		return err
	}
	log.Println("  ✓ Capabilities declared")

	// Step 5: REPLCONF capa dragonfly
	log.Println("  [5/6] Declaring Dragonfly compatibility...")
	if err := r.sendCapaDragonfly(); err != nil {
		return err
	}
	log.Printf("  ✓ Dragonfly version: %s, shards: %d", r.masterInfo.Version, r.masterInfo.NumFlows)

	// Step 6: establish FLOW connections
	log.Printf("  [6/6] Establishing %d FLOW connections...", r.masterInfo.NumFlows)
	if err := r.establishFlows(); err != nil {
		return err
	}
	log.Printf("  ✓ All FLOW connections established")

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("✓ Handshake complete")
	log.Println("")

	r.state = StatePreparation
	return nil
}

// sendPing issues a PING command over the main connection
func (r *Replicator) sendPing() error {
	resp, err := r.mainConn.Do("PING")
	if err != nil {
		return fmt.Errorf("PING failed: %w", err)
	}

	reply, err := redisx.ToString(resp)
	if err != nil || reply != "PONG" {
		return fmt.Errorf("expected PONG but received: %v", resp)
	}

	return nil
}

// sendListeningPort sends REPLCONF listening-port
func (r *Replicator) sendListeningPort() error {
	resp, err := r.mainConn.Do("REPLCONF", "listening-port", strconv.Itoa(r.listeningPort))
	if err != nil {
		return fmt.Errorf("REPLCONF listening-port failed: %w", err)
	}

	return r.expectOK(resp)
}

// sendIPAddress sends REPLCONF ip-address
func (r *Replicator) sendIPAddress() error {
	resp, err := r.mainConn.Do("REPLCONF", "ip-address", r.announceIP)
	if err != nil {
		return fmt.Errorf("REPLCONF ip-address failed: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaEOF sends REPLCONF capa eof/capa psync2
func (r *Replicator) sendCapaEOF() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "eof", "capa", "psync2")
	if err != nil {
		return fmt.Errorf("REPLCONF capa eof psync2 failed: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaDragonfly sends REPLCONF capa dragonfly and parses the response
func (r *Replicator) sendCapaDragonfly() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "dragonfly")
	if err != nil {
		return fmt.Errorf("REPLCONF capa dragonfly failed: %w", err)
	}

	// Parse response
	// Dragonfly response format (v1.30.0):
	// Array: [replication_id, sync_version, unknown_param, num_flows]
	// Example: ["16c2763d...", "SYNC5", 8, 4]

	arr, err := redisx.ToStringSlice(resp)
	if err != nil {
		// Not an array, try parsing as a simple string
		if str, err2 := redisx.ToString(resp); err2 == nil {
			// Check if it is OK (older versions or vanilla Redis)
			if str == "OK" {
				return fmt.Errorf("Target is Redis or an older Dragonfly (received simple OK)")
			}
			return fmt.Errorf("Target is not Dragonfly (unexpected response: %s)", str)
		}
		return fmt.Errorf("failed to parse capa dragonfly response: %w", err)
	}

	// Validate length
	if len(arr) < 4 {
		return fmt.Errorf("Malformed Dragonfly response (expected 4 elements): %v", arr)
	}

	// Response layout: [master_id, sync_id, flow_count, version]
	// e.g. ["16c2763d...", "SYNC11", 8, 4]

	// Element 0: replication ID (master_id)
	r.masterInfo.ReplID = arr[0]

	// Element 1: sync session ID (e.g. "SYNC11")
	r.masterInfo.SyncID = arr[1]

	// Element 2: number of flows
	numFlows, err := strconv.Atoi(arr[2])
	if err != nil {
		return fmt.Errorf("failed to parse flow count: %s", arr[2])
	}
	r.masterInfo.NumFlows = numFlows

	// Element 3: Dragonfly protocol version
	version, err := strconv.Atoi(arr[3])
	if err != nil {
		return fmt.Errorf("failed to parse protocol version: %s", arr[3])
	}
	r.masterInfo.Version = DflyVersion(version)

	log.Printf("  → Replication ID: %s", r.masterInfo.ReplID[:8]+"...")
	log.Printf("  → Sync session: %s", r.masterInfo.SyncID)
	log.Printf("  → Flow count: %d", r.masterInfo.NumFlows)
	log.Printf("  → Protocol version: %s", r.masterInfo.Version)

	return nil
}

// establishFlows creates dedicated FLOW connections for each shard
func (r *Replicator) establishFlows() error {
	numFlows := r.masterInfo.NumFlows
	log.Printf("    • Establishing %d parallel FLOW connections...", numFlows)

	r.flows = make([]FlowInfo, numFlows)
	r.flowConns = make([]*redisx.Client, numFlows)
	r.initFlowTracking(numFlows)

	// Create independent TCP connections for each FLOW
	for i := 0; i < numFlows; i++ {
		log.Printf("    • Establishing FLOW-%d dedicated connection...", i)
		r.recordFlowStage(i, "connecting", "Establishing FLOW connection")

		// 1. Create a new TCP connection
		dialCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
		flowConn, err := redisx.Dial(dialCtx, redisx.Config{
			Addr:     r.cfg.Source.Addr,
			Password: r.cfg.Source.Password,
			TLS:      r.cfg.Source.TLS,
		})
		cancel()

		if err != nil {
			return fmt.Errorf("FLOW-%d connection failed: %w", i, err)
		}

		r.flowConns[i] = flowConn

		// 2. Send PING (optional, ensures the connection is alive)
		if err := flowConn.Ping(); err != nil {
			return fmt.Errorf("FLOW-%d PING failed: %w", i, err)
		}

		// 3. Send DFLY FLOW to register this FLOW
		// Command: DFLY FLOW <master_id> <sync_id> <flow_id>
		resp, err := flowConn.Do("DFLY", "FLOW", r.masterInfo.ReplID, r.masterInfo.SyncID, strconv.Itoa(i))
		if err != nil {
			return fmt.Errorf("FLOW-%d registration failed: %w", i, err)
		}

		// 4. Parse response: ["FULL", <eof_token>] or ["PARTIAL", <eof_token>]
		arr, err := redisx.ToStringSlice(resp)
		if err != nil {
			// Could be a simple OK string
			if err := r.expectOK(resp); err != nil {
				return fmt.Errorf("FLOW-%d returned error: %w", i, err)
			}
			r.flows[i] = FlowInfo{
				FlowID:   i,
				State:    "established",
				SyncType: "OK",
				EOFToken: "",
			}
		} else {
			if len(arr) < 2 {
				return fmt.Errorf("FLOW-%d response malformed, expected 2 elements: %v", i, arr)
			}
			syncType := arr[0]
			eofToken := arr[1]

			r.flows[i] = FlowInfo{
				FlowID:   i,
				State:    "established",
				SyncType: syncType,
				EOFToken: eofToken,
			}

			log.Printf("      → Sync type: %s, EOF Token: %s...", syncType, eofToken[:min(8, len(eofToken))])
		}

		log.Printf("    ✓ FLOW-%d connection and registration complete", i)
		r.recordFlowStage(i, "established", fmt.Sprintf("%s FLOW established", r.flows[i].SyncType))
	}

	log.Printf("    ✓ All %d FLOW connections established", numFlows)
	return nil
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *Replicator) initFlowTracking(num int) {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	r.flowKeyCounts = make([]int64, num)
	r.flowLSNs = make([]uint64, num)
}

// sendDflySync issues DFLY SYNC to trigger the RDB transfer.
// Must be called only after every FLOW is established, otherwise Dragonfly will not send data.
func (r *Replicator) sendDflySync() error {
	log.Println("")
	log.Println("🔄 Sending DFLY SYNC to trigger data transfer...")

	// Send DFLY SYNC via the main connection
	// Command: DFLY SYNC <sync_id>
	resp, err := r.mainConn.Do("DFLY", "SYNC", r.masterInfo.SyncID)
	if err != nil {
		return fmt.Errorf("DFLY SYNC failed: %w", err)
	}

	// Expect OK
	if err := r.expectOK(resp); err != nil {
		return fmt.Errorf("DFLY SYNC returned error: %w", err)
	}

	log.Println("  ✓ DFLY SYNC sent, RDB transfer triggered")
	return nil
}

// expectOK validates that a Redis reply is the literal OK
func (r *Replicator) expectOK(resp interface{}) error {
	reply, err := redisx.ToString(resp)
	if err != nil {
		return fmt.Errorf("expected OK but received non-string response: %v", resp)
	}

	if reply != "OK" {
		return fmt.Errorf("expected OK but received: %s", reply)
	}

	return nil
}

// receiveSnapshot concurrently receives and parses RDB snapshots from all FLOW connections.
// Flow: use the RDB parser to decode data and write it into the target Redis.
// EOF tokens are validated after STARTSTABLE is issued.
func (r *Replicator) receiveSnapshot() error {
	log.Println("")
	log.Println("📦 Starting parallel RDB snapshot reception and parsing...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flows)
	if numFlows == 0 {
		return fmt.Errorf("no FLOW connection available")
	}

	log.Printf("  • Using %d FLOW connections to receive and parse the RDB snapshot", numFlows)

	// Wait for all goroutines
	var wg sync.WaitGroup
	errChan := make(chan error, numFlows)

	// Stats
	type FlowStats struct {
		KeyCount         int
		SkippedCount     int
		ErrorCount       int
		InlineJournalOps int
		mu               sync.Mutex
	}
	statsMap := make(map[int]*FlowStats)
	var statsMu sync.Mutex

	// Create async writers for each flow with adaptive concurrency
	// Create async writers for each flow with adaptive concurrency
	flowWriters := make([]*FlowWriter, numFlows)
	for i := 0; i < numFlows; i++ {
		var pipelineClient *redisx.Client
		if r.cfg.Target.Type == "redis-standalone" || r.cfg.Target.Type == "redis" {
			// Extract the single connection for pipeline usage
			// We use GetNodeClient with the configured address
			// Ignore error as connection was already established in connectTarget
			pipelineClient, _ = r.clusterClient.GetNodeClient(r.cfg.Target.Addr)
		}

		flowWriters[i] = NewFlowWriter(i, r.writeRDBEntry, numFlows, r.cfg.Target.Type, pipelineClient, r.clusterClient)
		flowWriters[i].Start()
	}

	// CRITICAL FIX: Global synchronization barrier matching Dragonfly's BlockingCounter design
	// rdbCompletionBarrier: Ensures all FLOWs finish RDB static snapshot before we send STARTSTABLE
	//
	// Dragonfly continues sending journal blobs AFTER FULLSYNC_END until we send STARTSTABLE.
	// We must keep reading continuously to avoid data loss.
	rdbCompletionBarrier := make(chan struct{})
	flowCompletionCount := &struct {
		count int
		mu    sync.Mutex
	}{}

	// Start a goroutine per FLOW to read and parse RDB data
	for i := 0; i < numFlows; i++ {
		statsMap[i] = &FlowStats{}
		wg.Add(1)
		go func(flowID int) {
			defer wg.Done()

			flowConn := r.flowConns[flowID]
			stats := statsMap[flowID]
			flowWriter := flowWriters[flowID]
			r.recordFlowStage(flowID, "rdb", "Receiving RDB snapshot")

			log.Printf("  [FLOW-%d] Starting to parse RDB data...", flowID)

			// Track whether this FLOW has completed RDB phase and synchronized via barrier
			// This ensures we only increment flowCompletionCount once, regardless of whether
			// we receive FULLSYNC_END marker or direct EOF from Dragonfly.
			// we receive FULLSYNC_END marker or direct EOF from Dragonfly.
			rdbCompleted := false

			parser := NewRDBParser(flowConn, flowID)

			// Set callback for inline journal entries during RDB phase
			parser.onJournalEntry = func(entry *JournalEntry) error {
				// Apply journal entry using existing replication logic
				if err := r.replayCommand(flowID, entry); err != nil {
					return fmt.Errorf("failed to apply inline journal entry: %w", err)
				}
				DebugTotalParsedJournal.Add(1) // DEBUG COUNTER
				// Update local flow stats
				stats.mu.Lock()
				stats.InlineJournalOps++
				stats.mu.Unlock()
				// Update global RDB stats
				r.rdbStats.mu.Lock()
				r.rdbStats.InlineJournalOps++
				r.rdbStats.mu.Unlock()
				return nil
			}

			// Set callback for FULLSYNC_END marker
			// When parser encounters 0xC8 (FULLSYNC_END), it calls this.
			parser.onFullSyncEnd = func() {
				if !rdbCompleted {
					rdbCompleted = true
					log.Printf("  [FLOW-%d] 🏁 Received FULLSYNC_END marker.", flowID)

					// Synchronization barrier
					flowCompletionCount.mu.Lock()
					flowCompletionCount.count++
					completedCount := flowCompletionCount.count
					flowCompletionCount.mu.Unlock()

					log.Printf("  [FLOW-%d] ⏸ Waiting for all FLOWs (%d/%d)...", flowID, completedCount, numFlows)

					if completedCount == numFlows {
						log.Printf("  [FLOW-%d] 🎯 All FLOWs completed RDB! Broadcasting signal...", flowID)
						close(rdbCompletionBarrier)
					}
				}
			}

			// 1. Parse header
			if err := parser.ParseHeader(); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: failed to parse RDB header: %w", flowID, err)
				r.recordFlowStage(flowID, "error", fmt.Sprintf("Failed to parse header: %v", err))
				return
			}
			log.Printf("  [FLOW-%d] ✓ RDB header parsed successfully", flowID)

			// 2. Parse entries
			for {
				// Observe cancellation
				select {
				case <-r.ctx.Done():
					errChan <- fmt.Errorf("FLOW-%d: snapshot reception cancelled", flowID)
					return
				default:
				}

				// Parse next entry
				entry, err := parser.ParseNext()
				if err != nil {
					// EOF: Dragonfly sent EOF (either after STARTSTABLE or directly)
					if err == io.EOF {
						stats.mu.Lock()
						inlineJournalOps := stats.InlineJournalOps
						stats.mu.Unlock()

						// CRITICAL FIX: If this FLOW never received FULLSYNC_END marker,
						// we still need to synchronize via barrier before exiting.
						// This handles the case where Dragonfly sends EOF directly without FULLSYNC_END.
						if !rdbCompleted {
							log.Printf("  [FLOW-%d] ✓ RDB stream terminated with EOF (no FULLSYNC_END received) (success=%d, skipped=%d, failed=%d, inline_journal=%d)",
								flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps)

							// Participate in barrier synchronization
							flowCompletionCount.mu.Lock()
							flowCompletionCount.count++
							completedCount := flowCompletionCount.count
							flowCompletionCount.mu.Unlock()

							log.Printf("  [FLOW-%d] ⏸ Waiting for all FLOWs to complete RDB (%d/%d done)...", flowID, completedCount, numFlows)

							// If this is the last FLOW to complete, broadcast signal
							if completedCount == numFlows {
								log.Printf("  [FLOW-%d] 🎯 All FLOWs completed! Broadcasting barrier signal...", flowID)
								close(rdbCompletionBarrier)
							}

							// Wait for barrier release
							<-rdbCompletionBarrier
							log.Printf("  [FLOW-%d] ✓ Barrier released, EOF FLOW exiting", flowID)
							rdbCompleted = true
						} else {
							// Normal case: EOF after STARTSTABLE (already participated in barrier)
							log.Printf("  [FLOW-%d] ✓ RDB stream terminated with EOF (after STARTSTABLE) (success=%d, skipped=%d, failed=%d, inline_journal=%d)",
								flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps)
						}

						r.recordFlowStage(flowID, "rdb_done",
							fmt.Sprintf("success=%d skipped=%d failed=%d inline_journal=%d", stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps))
						return
					}
					// Other errors: real parsing failure
					errChan <- fmt.Errorf("FLOW-%d: parsing failed: %w", flowID, err)
					r.recordFlowStage(flowID, "error", fmt.Sprintf("Parsing failed: %v", err))
					return
				}

				// CRITICAL: Check for FULLSYNC_END marker
				// This means "static RDB snapshot complete", but Dragonfly will CONTINUE
				// sending journal blobs until we send STARTSTABLE.
				// This logic is now handled by the parser.onFullSyncEnd callback.
				if entry.Type == RDB_TYPE_FULLSYNC_END_MARKER {
					stats.mu.Lock()
					inlineJournalOps := stats.InlineJournalOps
					stats.mu.Unlock()
					log.Printf("  [FLOW-%d] ✓ RDB parsing done (success=%d, skipped=%d, failed=%d, inline_journal=%d)",
						flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps)

					// CRITICAL: Continue ParseNext() loop to read journal blobs!
					// Do NOT return - Dragonfly will keep sending data until STARTSTABLE.
					// After main thread sends STARTSTABLE, Dragonfly will send EOF and we'll exit above.
					log.Printf("  [FLOW-%d] → Continuing to read journal blobs until STARTSTABLE triggers EOF...", flowID)
					continue
				}

				// Skip expired keys
				if entry.IsExpired() {
					statsMu.Lock()
					stats.SkippedCount++
					statsMu.Unlock()
					continue
				}

				// Write entry into Redis
				if err := flowWriter.Enqueue(entry); err != nil {
					log.Printf("  [FLOW-%d] ⚠ Write failed (key=%s): %v", flowID, entry.Key, err)
					statsMu.Lock()
					stats.ErrorCount++
					statsMu.Unlock()
					r.recordFlowStage(flowID, "error", fmt.Sprintf("Write failed key=%s", entry.Key))
				} else {
					DebugTotalEnqueued.Add(1) // DEBUG COUNTER
					statsMu.Lock()
					stats.KeyCount++
					statsMu.Unlock()
					r.onSnapshotKey(flowID)

					// Log progress every 100 keys
					if stats.KeyCount%100 == 0 {
						log.Printf("  [FLOW-%d] • Imported %d keys", flowID, stats.KeyCount)
					}
				}
			}
		}(i)
	}

	// CRITICAL FIX: Wait for RDB completion barrier BEFORE sending STARTSTABLE
	// This ensures all FLOWs have finished their static RDB snapshot.
	// After this point, goroutines continue reading journal blobs until we send STARTSTABLE.
	log.Println("")
	log.Println("⏸  Waiting for all FLOWs to complete RDB static snapshot...")
	<-rdbCompletionBarrier
	log.Println("  ✓ All FLOWs completed RDB static snapshot")

	// Intermediate stats - at this point RDB snapshot is complete but journal blobs may still be coming
	log.Println("")
	log.Println("📊 RDB Static Snapshot Complete - Intermediate Stats:")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	totalKeys := 0
	totalSkipped := 0
	totalErrors := 0
	totalInlineJournal := 0
	for flowID, stats := range statsMap {
		totalKeys += stats.KeyCount
		totalSkipped += stats.SkippedCount
		totalErrors += stats.ErrorCount
		stats.mu.Lock()
		totalInlineJournal += stats.InlineJournalOps
		inlineJournalOps := stats.InlineJournalOps
		stats.mu.Unlock()
		log.Printf("  [FLOW-%d] Stats: success=%d, skipped=%d, failed=%d, inline_journal=%d",
			flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps)
	}
	log.Printf("  ✓ RDB snapshot: total %d keys, skipped %d (expired), failed %d, inline_journal=%d",
		totalKeys, totalSkipped, totalErrors, totalInlineJournal)
	log.Printf("")

	// CRITICAL: Send STARTSTABLE immediately after barrier
	// This matches Dragonfly's design: after all FLOWs complete static snapshot,
	// send STARTSTABLE to trigger transition to stable sync.
	// Dragonfly will then send EOF to all FLOWs, allowing goroutines to exit naturally.
	if err := r.sendStartStable(); err != nil {
		return fmt.Errorf("Switching to stable sync failed: %w", err)
	}

	// CRITICAL: Now wait for goroutines to exit
	// After STARTSTABLE, Dragonfly sends EOF to all FLOWs, causing ParseNext() to return io.EOF
	// and goroutines to exit naturally.
	log.Println("")
	log.Println("⏸  Waiting for all FLOWs to receive EOF and terminate...")
	wg.Wait()
	close(errChan)

	// Drain errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}
	log.Println("  ✓ All FLOW goroutines terminated")

	// Stop all writers and wait for remaining batches to flush
	log.Println("")
	log.Println("⏸  Stopping async writers and flushing remaining batches...")
	for i, fw := range flowWriters {
		fw.Stop()
		received, written, batches := fw.GetStats()
		log.Printf("  [FLOW-%d] Writer stats: received=%d, written=%d, batches=%d",
			i, received, written, batches)
	}
	log.Println("  ✓ All writers stopped, all data flushed")

	// Final stats after all journal blobs processed
	log.Println("")
	log.Println("📊 Final Stats (including all journal blobs):")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	totalKeys = 0
	totalSkipped = 0
	totalErrors = 0
	totalInlineJournal = 0
	for flowID, stats := range statsMap {
		totalKeys += stats.KeyCount
		totalSkipped += stats.SkippedCount
		totalErrors += stats.ErrorCount
		stats.mu.Lock()
		totalInlineJournal += stats.InlineJournalOps
		inlineJournalOps := stats.InlineJournalOps
		stats.mu.Unlock()
		log.Printf("  [FLOW-%d] Stats: success=%d, skipped=%d, failed=%d, inline_journal=%d",
			flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount, inlineJournalOps)
	}
	log.Printf("  ✓ Total: %d keys, skipped %d (expired), failed %d, inline_journal=%d",
		totalKeys, totalSkipped, totalErrors, totalInlineJournal)
	log.Printf("")

	// Verify EOF tokens
	if err := r.verifyEofTokens(); err != nil {
		return fmt.Errorf("EOF token verification failed: %w", err)
	}
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

// sendStartStable issues DFLY STARTSTABLE on the main connection
func (r *Replicator) sendStartStable() error {
	log.Println("")
	log.Println("🔄 Switching to stable sync mode...")

	// STARTSTABLE may take longer than the default 5s timeout as it coordinates
	// multiple shards and prepares for stable sync transition. Use 180s timeout.
	log.Printf("  → Sending DFLY STARTSTABLE (sync_id=%s, timeout=180s)...", r.masterInfo.SyncID)
	log.Printf("  → Please wait, this may take a few minutes for Dragonfly to coordinate all shards...")
	resp, err := r.mainConn.DoWithTimeout(180*time.Second, "DFLY", "STARTSTABLE", r.masterInfo.SyncID)
	if err != nil {
		return fmt.Errorf("DFLY STARTSTABLE failed: %w", err)
	}

	if err := r.expectOK(resp); err != nil {
		return fmt.Errorf("DFLY STARTSTABLE returned error: %w", err)
	}

	log.Println("  ✓ Switched to stable sync mode")
	r.state = StateStableSync
	return nil
}

// verifyEofTokens validates EOF tokens emitted by each FLOW after STARTSTABLE.
// After STARTSTABLE each FLOW sends:
//  1. EOF opcode (0xFF) - 1 byte
//  2. Checksum - 8 bytes
//  3. EOF token - 40 bytes
func (r *Replicator) verifyEofTokens() error {
	log.Println("")
	log.Println("🔐 Verifying EOF token...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flowConns)
	var wg sync.WaitGroup
	errChan := make(chan error, numFlows)

	for i := 0; i < numFlows; i++ {
		wg.Add(1)
		go func(flowID int) {
			defer wg.Done()
			flowConn := r.flowConns[flowID]
			expectedToken := r.flows[flowID].EOFToken
			tokenLen := len(expectedToken)
			if tokenLen == 0 {
				errChan <- fmt.Errorf("FLOW-%d: EOF token missing", flowID)
				return
			}
			log.Printf("  [FLOW-%d] → Reading EOF token (%d bytes)...", flowID, tokenLen)

			// CRITICAL FIX: Even with FULLSYNC_END (0xC8), Dragonfly sends the 40-byte SHA1 EOF token (Checksum).
			// We MUST consume these 40 bytes so they are not misinterpreted as opcodes (e.g. 'd' = 100) by the journal parser next.
			if r.flows[flowID].SyncType == "FULL" {
				log.Printf("  [FLOW-%d] 🗑 Reading and discarding 40-byte EOF checksum (preventing 'unknown opcode' errors)...", flowID)

				// Create a temporary buffer to discard the data
				discardBuf := make([]byte, 40)
				// ReadFull ensures we get exactly 40 bytes or fail
				if _, err := io.ReadFull(flowConn, discardBuf); err != nil {
					log.Printf("  [FLOW-%d] ⚠ Failed to discard EOF checksum: %v (Journal parsing may fail next)", flowID, err)
					// We warn but don't error out, hoping for the best
				} else {
					log.Printf("  [FLOW-%d] ✓ EOF checksum discarded successfully.", flowID)
				}

				// We still return here, as we don't need to do legacy verification
				return
			}

			// Legacy EOF verification (only if FULLSYNC_END was NOT seen)
			log.Printf("  [FLOW-%d] 🔍 Verifying legacy EOF token...", flowID)
			parser := NewRDBParser(flowConn, flowID) // Create a parser to use PeekByte/ReadByte
			maxRetries := 100                        // Look ahead 100 bytes for EOF
			// expectedToken is already set to r.flows[flowID].EOFToken above

			for j := 0; j < maxRetries; j++ {
				opcodeByte, err := parser.peekByte()
				if err != nil {
					if err == io.EOF {
						break
					}
					errChan <- fmt.Errorf("FLOW-%d: error peeking for EOF: %w", flowID, err)
					return
				}

				switch opcodeByte {
				case 0xD2, 0xD3: // Journal blobs (unexpected here logic-wise if rdbCompleted, but safe to ignore if we were just scanning)
					// If we see journal ops, we consumed too far or are in mixed state.
					// But for legacy EOF search, we shouldn't see these unless we missed the transition.
					// We'll treat them as non-EOF.
					if _, err := parser.readByte(); err != nil { // consume
						errChan <- err
						return
					}
					continue

				case 0xFF: // RDB_OPCODE_EOF
					// Found EOF!
					log.Printf("  [FLOW-%d] ✓ Found legacy EOF opcode (0xFF)", flowID)
					// Consume the opcode
					parser.readByte()
					goto foundEOF

				default:
					// Consume and continue searching/skipping junk?
					// Strict mode: if we don't find it immediately, it's an error?
					// Let's consume and retry
					if _, err := parser.readByte(); err != nil {
						errChan <- err
						return
					}
				}
			}

			// If we reach here, we exceeded max retries
			errChan <- fmt.Errorf("FLOW-%d: exceeded max retries (%d) while searching for EOF", flowID, maxRetries)
			return

		foundEOF:
			// 2. Now we have EOF opcode, continue with checksum and token

			// 3. Read checksum (8 bytes)
			checksumBuf := make([]byte, 8)
			if _, err := io.ReadFull(flowConn, checksumBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: failed to read checksum: %w", flowID, err)
				return
			}

			// 4. Read EOF token (40 bytes)
			tokenBuf := make([]byte, 40)
			if _, err := io.ReadFull(flowConn, tokenBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: failed to read EOF token: %w", flowID, err)
				return
			}
			receivedToken := string(tokenBuf)

			// 5. Compare token
			if receivedToken != expectedToken {
				errChan <- fmt.Errorf("FLOW-%d: EOF token mismatch\n  expected: %s\n  actual: %s",
					flowID, expectedToken, receivedToken)
				return
			}

			log.Printf("  [FLOW-%d] ✓ EOF token verified", flowID)
		}(i)
	}

	log.Println("")
	log.Println("⏸  Waiting for all FLOWs to receive EOF and terminate...")
	wg.Wait()
	close(errChan)

	// PRINT FINAL DEBUG STATS
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("🛑 DEBUG STATS (Missing Key Investigation):")
	log.Printf("  • Total Parsed (RDB):    %d", DebugTotalParsedRDB.Load())
	log.Printf("  • Total Parsed (Journal): %d", DebugTotalParsedJournal.Load())
	log.Printf("  • Total Enqueued:        %d", DebugTotalEnqueued.Load())
	log.Printf("  • Total Flushed:         %d", DebugTotalFlushed.Load())
	log.Printf("  • Difference (Enqueue-Flush): %d", DebugTotalEnqueued.Load()-DebugTotalFlushed.Load())
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Surface the first error if any
	for err := range errChan {
		return err
	}

	log.Println("  ✓ EOF token verification finished for all FLOW connections")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

// FlowEntry represents a journal entry tagged with its FLOW ID
type FlowEntry struct {
	FlowID int
	Entry  *JournalEntry
	Error  error
}

// receiveJournal consumes journal streams from all FLOW connections in parallel
func (r *Replicator) receiveJournal() error {
	log.Println("")
	log.Println("📡 Starting to receive journal stream...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Update pipeline status to incremental now that journal streaming is starting
	r.recordPipelineStatus("incremental", "Replaying journal incrementally")
	r.recordStage("replicator", "journal", "Listening to journal stream")

	numFlows := len(r.flowConns)
	if numFlows == 0 {
		return fmt.Errorf("no FLOW connections available")
	}

	log.Printf("  • Listening to all %d FLOW connections in parallel", numFlows)

	// Channel for entries from all FLOWs
	entryChan := make(chan *FlowEntry, 100)

	// Launch a goroutine per FLOW
	var wg sync.WaitGroup
	for i := 0; i < numFlows; i++ {
		wg.Add(1)
		go r.readFlowJournal(i, entryChan, &wg)
	}

	// Close the channel once every FLOW goroutine exits
	go func() {
		wg.Wait()
		close(entryChan)
	}()

	// Main processing loop
	entriesCount := 0
	currentDB := uint64(0)
	flowStats := make(map[int]int) // entries per FLOW

	for flowEntry := range entryChan {
		// Handle errors
		if flowEntry.Error != nil {
			log.Printf("  ✗ FLOW-%d error: %v", flowEntry.FlowID, flowEntry.Error)
			continue
		}

		entriesCount++
		flowStats[flowEntry.FlowID]++
		entry := flowEntry.Entry

		// Track current database
		if entry.Opcode == OpSelect {
			currentDB = entry.DbIndex
		}

		// Display decoded command
		r.displayFlowEntry(flowEntry.FlowID, entry, currentDB, entriesCount)

		// Replay command to Redis Cluster
		r.replayStats.mu.Lock()
		r.replayStats.TotalCommands++
		r.replayStats.mu.Unlock()

		if err := r.replayCommand(flowEntry.FlowID, entry); err != nil {
			log.Printf("  ✗ Replay failed: %v", err)
		}

		// Attempt automatic checkpoint save
		r.tryAutoSaveCheckpoint()

		// Log statistics every 50 entries
		if entriesCount%50 == 0 {
			r.replayStats.mu.Lock()
			log.Printf("  📊 Stats: total=%d, success=%d, skipped=%d, failed=%d",
				r.replayStats.TotalCommands,
				r.replayStats.ReplayedOK,
				r.replayStats.Skipped,
				r.replayStats.Failed)

			// Report per-FLOW stats
			for fid, count := range flowStats {
				lsn := r.replayStats.FlowLSNs[fid]
				log.Printf("    FLOW-%d: %d entries, LSN=%d", fid, count, lsn)
			}
			r.replayStats.mu.Unlock()
		}
	}

	log.Println("  • Journal stream finished for all FLOW connections")

	// Persist final checkpoint if enabled
	if r.cfg.Checkpoint.Enabled {
		log.Println("  💾 Saving final checkpoint...")
		if err := r.saveCheckpoint(); err != nil {
			log.Printf("  ⚠ Failed to save final checkpoint: %v", err)
		} else {
			log.Println("  ✓ Checkpoint saved")
		}
	}

	return nil
}

// readFlowJournal reads the journal stream for a specific FLOW
func (r *Replicator) readFlowJournal(flowID int, entryChan chan<- *FlowEntry, wg *sync.WaitGroup) {
	defer wg.Done()

	reader := NewJournalReader(r.flowConns[flowID])
	log.Printf("  [FLOW-%d] Starting journal stream reception", flowID)
	r.recordFlowStage(flowID, "journal", "Listening to journal stream")

	for {
		// Observe cancellation
		select {
		case <-r.ctx.Done():
			log.Printf("  [FLOW-%d] Stop signal received", flowID)
			return
		default:
		}

		// Read entry
		entry, err := reader.ReadEntry()
		if err != nil {
			if err == io.EOF {
				log.Printf("  [FLOW-%d] Journal stream ended (EOF)", flowID)
				r.recordFlowStage(flowID, "journal_done", "Journal stream finished")
				return
			}
			// Send error to channel
			entryChan <- &FlowEntry{
				FlowID: flowID,
				Error:  fmt.Errorf("read failed: %w", err),
			}
			r.recordFlowStage(flowID, "error", fmt.Sprintf("Journal read failed: %v", err))
			return
		}

		// Forward entry
		entryChan <- &FlowEntry{
			FlowID: flowID,
			Entry:  entry,
		}
	}
}

// displayFlowEntry prints a FLOW-tagged journal entry
func (r *Replicator) displayFlowEntry(flowID int, entry *JournalEntry, currentDB uint64, count int) {
	// Format output based on opcode
	switch entry.Opcode {
	case OpSelect:
		log.Printf("  [%d] FLOW-%d: SELECT DB=%d", count, flowID, entry.DbIndex)

	case OpLSN:
		log.Printf("  [%d] FLOW-%d: LSN %d", count, flowID, entry.LSN)

	case OpPing:
		log.Printf("  [%d] FLOW-%d: PING", count, flowID)

	case OpCommand:
		// Format arguments
		args := make([]string, len(entry.Args))
		for i, arg := range entry.Args {
			if len(arg) > 50 {
				args[i] = fmt.Sprintf("\"%s...\"", arg[:50])
			} else {
				args[i] = fmt.Sprintf("\"%s\"", arg)
			}
		}
		log.Printf("  [%d] FLOW-%d: %s %s (txid=%d, shards=%d)",
			count, flowID, entry.Command, strings.Join(args, " "), entry.TxID, entry.ShardCnt)

	case OpExpired:
		log.Printf("  [%d] FLOW-%d: EXPIRED %s (txid=%d)",
			count, flowID, entry.Command, entry.TxID)

	default:
		log.Printf("  [%d] FLOW-%d: %s", count, flowID, entry.Opcode)
	}
}

// displayEntry prints a decoded journal entry without FLOW context
func (r *Replicator) displayEntry(entry *JournalEntry, currentDB uint64, count int) {
	// Format output based on opcode
	switch entry.Opcode {
	case OpSelect:
		log.Printf("  [%d] SELECT DB=%d", count, entry.DbIndex)

	case OpLSN:
		log.Printf("  [%d] LSN %d", count, entry.LSN)

	case OpPing:
		log.Printf("  [%d] PING", count)

	case OpCommand:
		// Format arguments
		args := make([]string, len(entry.Args))
		for i, arg := range entry.Args {
			if len(arg) > 50 {
				args[i] = fmt.Sprintf("\"%s...\"", arg[:50])
			} else {
				args[i] = fmt.Sprintf("\"%s\"", arg)
			}
		}

		log.Printf("  [%d] DB=%d COMMAND %s %s",
			count, currentDB, entry.Command, strings.Join(args, " "))

	case OpExpired:
		args := make([]string, len(entry.Args))
		for i, arg := range entry.Args {
			if len(arg) > 50 {
				args[i] = fmt.Sprintf("\"%s...\"", arg[:50])
			} else {
				args[i] = fmt.Sprintf("\"%s\"", arg)
			}
		}

		log.Printf("  [%d] DB=%d EXPIRED %s %s",
			count, currentDB, entry.Command, strings.Join(args, " "))

	default:
		log.Printf("  [%d] %s", count, entry.String())
	}
}

// GetState returns the current replicator state
func (r *Replicator) GetState() ReplicaState {
	return r.state
}

// GetMasterInfo returns master metadata collected during handshake
func (r *Replicator) GetMasterInfo() MasterInfo {
	return r.masterInfo
}

// GetFlows returns all FLOW descriptors
func (r *Replicator) GetFlows() []FlowInfo {
	return r.flows
}

// ReplayStats holds command replay statistics
type ReplayStats struct {
	mu             sync.Mutex
	TotalCommands  int64
	ReplayedOK     int64
	Skipped        int64
	Failed         int64
	FlowLSNs       map[int]uint64 // latest LSN per FLOW
	LastReplayTime time.Time
}

// RDBStats holds RDB snapshot import statistics
type RDBStats struct {
	mu               sync.Mutex
	Commands         int64 // Total Redis commands executed during RDB import (excludes inline journal)
	Keys             int64 // Total keys imported
	InlineJournalOps int64 // Inline journal operations applied during RDB phase
}

// replayCommand replays a single journal command into Redis Cluster
func (r *Replicator) replayCommand(flowID int, entry *JournalEntry) error {
	switch entry.Opcode {
	case OpSelect:
		// Redis Cluster only exposes DB 0, ignore SELECT
		log.Printf("  [FLOW-%d] ⊘ Skipped SELECT (reason: Redis Cluster only supports DB 0)", flowID)
		r.replayStats.mu.Lock()
		r.replayStats.Skipped++
		r.replayStats.mu.Unlock()
		return nil

	case OpPing:
		// Ignore heartbeat
		log.Printf("  [FLOW-%d] ⊘ Skipped PING (reason: heartbeat, no action needed)", flowID)
		r.replayStats.mu.Lock()
		r.replayStats.Skipped++
		r.replayStats.mu.Unlock()
		return nil

	case OpLSN:
		// Track LSN only
		r.replayStats.mu.Lock()
		if r.replayStats.FlowLSNs == nil {
			r.replayStats.FlowLSNs = make(map[int]uint64)
		}
		r.replayStats.FlowLSNs[flowID] = entry.LSN
		r.replayStats.mu.Unlock()
		r.recordFlowLSN(flowID, entry.LSN)
		return nil

	case OpExpired:
		// Handle expired key by re-applying TTL using PEXPIRE
		keyName := "unknown"
		if len(entry.Args) > 0 {
			keyName = entry.Args[0]
		}
		if err := r.handleExpiredKey(entry); err != nil {
			log.Printf("  [FLOW-%d] ✗ FAILED OpExpired key=%s, error: %v", flowID, keyName, err)
			r.replayStats.mu.Lock()
			r.replayStats.Failed++
			r.replayStats.mu.Unlock()
			return fmt.Errorf("Failed to process expired key: %w", err)
		}
		log.Printf("  [FLOW-%d] ✓ OpExpired applied: key=%s", flowID, keyName)
		r.replayStats.mu.Lock()
		r.replayStats.ReplayedOK++
		r.replayStats.LastReplayTime = time.Now()
		r.replayStats.mu.Unlock()
		return nil

	case OpCommand:
		// Check for global commands
		cmd := strings.ToUpper(entry.Command)
		keyName := "N/A"
		if len(entry.Args) > 0 {
			keyName = entry.Args[0]
		}

		if isGlobalCommand(cmd) {
			log.Printf("  [FLOW-%d] ⊘ Skipped global command: %s (reason: requires multi-shard coordination)", flowID, cmd)
			r.replayStats.mu.Lock()
			r.replayStats.Skipped++
			r.replayStats.mu.Unlock()
			return nil
		}

		// Execute regular command
		if err := r.executeCommand(entry); err != nil {
			log.Printf("  [FLOW-%d] ✗ FAILED command: %s key=%s args=%v, error: %v", flowID, entry.Command, keyName, entry.Args[1:], err)
			r.replayStats.mu.Lock()
			r.replayStats.Failed++
			r.replayStats.mu.Unlock()
			return fmt.Errorf("Command execution failed: %w", err)
		}

		log.Printf("  [FLOW-%d] ✓ Command applied: %s key=%s args=%v", flowID, entry.Command, keyName, entry.Args[1:])
		r.replayStats.mu.Lock()
		r.replayStats.ReplayedOK++
		r.replayStats.LastReplayTime = time.Now()
		r.replayStats.mu.Unlock()
		return nil

	default:
		return fmt.Errorf("Unknown opcode: %d", entry.Opcode)
	}
}

// handleExpiredKey sets TTL for expired key events
func (r *Replicator) handleExpiredKey(entry *JournalEntry) error {
	if len(entry.Args) == 0 {
		return fmt.Errorf("EXPIRED command missing key argument")
	}

	key := entry.Args[0]

	// Assume TTL is 1ms (key already expired). Can be refined if Dragonfly publishes TTL.
	ttlMs := int64(1)

	_, err := r.clusterClient.Do("PEXPIRE", key, ttlMs)
	if err != nil {
		return err
	}

	return nil
}

// executeCommand executes a journal command verbatim
func (r *Replicator) executeCommand(entry *JournalEntry) error {
	// Copy args
	// Copy args to interface slice
	args := make([]interface{}, len(entry.Args))
	for i, v := range entry.Args {
		args[i] = v
	}

	// Execute
	_, err := r.clusterClient.Do(entry.Command, args...)
	return err
}

// isGlobalCommand checks if a command needs cluster-wide coordination
func isGlobalCommand(cmd string) bool {
	globalCmds := map[string]bool{
		"FLUSHDB":                true,
		"FLUSHALL":               true,
		"DFLYCLUSTER FLUSHSLOTS": true,
	}
	return globalCmds[cmd]
}

// saveCheckpoint persists the current checkpoint state
func (r *Replicator) saveCheckpoint() error {
	r.replayStats.mu.Lock()
	defer r.replayStats.mu.Unlock()

	// Build checkpoint payload
	cp := &checkpoint.Checkpoint{
		ReplicationID: r.masterInfo.ReplID,
		SessionID:     r.masterInfo.SyncID,
		NumFlows:      len(r.flows),
		FlowLSNs:      make(map[int]uint64),
	}

	// Copy FlowLSNs
	for flowID, lsn := range r.replayStats.FlowLSNs {
		cp.FlowLSNs[flowID] = lsn
	}

	// Save to file
	if err := r.checkpointMgr.Save(cp); err != nil {
		return fmt.Errorf("Failed to save checkpoint: %w", err)
	}

	r.lastCheckpointTime = time.Now()
	if r.metrics != nil {
		r.metrics.Set(state.MetricCheckpointSavedAtUnix, float64(r.lastCheckpointTime.Unix()))
	}
	return nil
}

// tryAutoSaveCheckpoint periodically persists checkpoints
func (r *Replicator) tryAutoSaveCheckpoint() {
	// Skip when checkpointing is disabled
	if !r.cfg.Checkpoint.Enabled {
		return
	}

	if time.Since(r.lastCheckpointTime) >= r.checkpointInterval {
		if err := r.saveCheckpoint(); err != nil {
			log.Printf("  ⚠ Automatic checkpoint save failed: %v", err)
		}
	}
}

// checkKeyConflict validates whether an RDB entry should be written based on conflict policy.
// Returns (write, error).
func (r *Replicator) checkKeyConflict(key string) (bool, error) {
	policy := r.cfg.Conflict.Policy

	// overwrite: always write
	if policy == "overwrite" {
		return true, nil
	}

	// panic/skip: check if key exists
	reply, err := r.clusterClient.Do("EXISTS", key)
	if err != nil {
		return false, fmt.Errorf("Failed to check key existence: %w", err)
	}

	exists, ok := reply.(int64)
	if !ok {
		return false, fmt.Errorf("EXISTS command returned unexpected type")
	}

	if exists == 0 {
		return true, nil // key does not exist
	}

	// Key exists
	if policy == "panic" {
		log.Printf("  ⚠️ Duplicate key detected: %s (policy=panic, aborting)", key)
		return false, fmt.Errorf("duplicate key detected: %s", key)
	}

	// policy == skip
	log.Printf("  ⚠️ Skipping duplicate key: %s (policy=skip)", key)
	return false, nil
}

// writeRDBEntry writes an RDB entry into Redis
func (r *Replicator) writeRDBEntry(entry *RDBEntry) error {
	// Check conflicts
	shouldWrite, err := r.checkKeyConflict(entry.Key)
	if err != nil {
		return err // panic mode bubbles up
	}
	if !shouldWrite {
		return nil // skip mode simply ignores it
	}

	switch entry.Type {
	case RDB_TYPE_STRING:
		return r.writeString(entry)

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH_LISTPACK:
		return r.writeHash(entry)

	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2:
		return r.writeList(entry)

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET, RDB_TYPE_SET_LISTPACK:
		return r.writeSet(entry)

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_LISTPACK:
		return r.writeZSet(entry)

	case RDB_TYPE_STREAM_LISTPACKS, RDB_TYPE_STREAM_LISTPACKS_2, RDB_TYPE_STREAM_LISTPACKS_3:
		return r.writeStream(entry)

	default:
		return fmt.Errorf("unsupported RDB type: %d", entry.Type)
	}
}

// writeString handles string entries
func (r *Replicator) writeString(entry *RDBEntry) error {
	// Extract value
	strVal, ok := entry.Value.(*StringValue)
	if !ok {
		return fmt.Errorf("failed to convert string value")
	}

	// Write value
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()

	_, err := r.clusterClient.Do("SET", entry.Key, strVal.Value)
	if err != nil {
		return fmt.Errorf("SET command failed: %w", err)
	}

	// Apply TTL if needed
	if entry.ExpireMs > 0 {
		// Compute remaining TTL
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

// writeHash handles hash entries
func (r *Replicator) writeHash(entry *RDBEntry) error {
	// Extract value
	hashVal, ok := entry.Value.(*HashValue)
	if !ok {
		return fmt.Errorf("failed to convert hash value")
	}

	// Remove existing key to avoid stale fields
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Write all fields using HSET key field1 value1 ...
	log.Printf("  [DEBUG] writeHash: key=%s, fields=%d", entry.Key, len(hashVal.Fields))
	if len(hashVal.Fields) > 0 {
		args := make([]interface{}, 0, 1+len(hashVal.Fields)*2)
		args = append(args, entry.Key)
		for field, value := range hashVal.Fields {
			args = append(args, field, value)
			log.Printf("  [DEBUG]   field=%s, value=%s", field, value)
		}
		log.Printf("  [DEBUG] Executing HSET with %d arguments", len(args))

		r.rdbStats.mu.Lock()
		r.rdbStats.Commands++
		r.rdbStats.mu.Unlock()

		_, err := r.clusterClient.Do("HSET", args...)
		if err != nil {
			return fmt.Errorf("HSET command failed: %w", err)
		}
		log.Printf("  [DEBUG] HSET command succeeded")
	} else {
		log.Printf("  [DEBUG] Field empty, skipping write")
	}

	// Apply TTL if needed
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

// writeList handles list entries
func (r *Replicator) writeList(entry *RDBEntry) error {
	// Extract value
	listVal, ok := entry.Value.(*ListValue)
	if !ok {
		return fmt.Errorf("failed to convert list value")
	}

	// Remove existing key
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert elements with RPUSH
	if len(listVal.Elements) > 0 {
		args := make([]interface{}, 0, 1+len(listVal.Elements))
		args = append(args, entry.Key)
		for _, elem := range listVal.Elements {
			args = append(args, elem)
		}

		r.rdbStats.mu.Lock()
		r.rdbStats.Commands++
		r.rdbStats.mu.Unlock()

		_, err := r.clusterClient.Do("RPUSH", args...)
		if err != nil {
			return fmt.Errorf("RPUSH command failed: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

// writeSet handles set entries
func (r *Replicator) writeSet(entry *RDBEntry) error {
	// Extract value
	setVal, ok := entry.Value.(*SetValue)
	if !ok {
		return fmt.Errorf("failed to convert set value")
	}

	// Remove existing key
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert members via SADD
	if len(setVal.Members) > 0 {
		args := make([]interface{}, 0, 1+len(setVal.Members))
		args = append(args, entry.Key)
		for _, member := range setVal.Members {
			args = append(args, member)
		}

		r.rdbStats.mu.Lock()
		r.rdbStats.Commands++
		r.rdbStats.mu.Unlock()

		_, err := r.clusterClient.Do("SADD", args...)
		if err != nil {
			return fmt.Errorf("SADD command failed: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

// writeZSet handles sorted set entries
func (r *Replicator) writeZSet(entry *RDBEntry) error {
	// Extract value
	zsetVal, ok := entry.Value.(*ZSetValue)
	if !ok {
		return fmt.Errorf("failed to convert zset value")
	}

	// Remove existing key
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert members via ZADD key score member ...
	if len(zsetVal.Members) > 0 {
		args := make([]interface{}, 0, 1+len(zsetVal.Members)*2)
		args = append(args, entry.Key)
		for _, zm := range zsetVal.Members {
			args = append(args, fmt.Sprintf("%f", zm.Score), zm.Member)
		}

		r.rdbStats.mu.Lock()
		r.rdbStats.Commands++
		r.rdbStats.mu.Unlock()

		_, err := r.clusterClient.Do("ZADD", args...)
		if err != nil {
			return fmt.Errorf("ZADD command failed: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

// writeStream handles stream entries (XADD for each message)
func (r *Replicator) writeStream(entry *RDBEntry) error {
	// Extract value
	streamVal, ok := entry.Value.(*StreamValue)
	if !ok {
		return fmt.Errorf("failed to convert stream value")
	}

	// Remove existing key to avoid conflicts
	r.rdbStats.mu.Lock()
	r.rdbStats.Commands++
	r.rdbStats.mu.Unlock()
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert each message using XADD key ID field value ...
	for _, msg := range streamVal.Messages {
		args := make([]interface{}, 0, 2+len(msg.Fields)*2)
		args = append(args, entry.Key, msg.ID)

		// Add field-value pairs
		for field, value := range msg.Fields {
			args = append(args, field, value)
		}

		r.rdbStats.mu.Lock()
		r.rdbStats.Commands++
		r.rdbStats.mu.Unlock()

		_, err := r.clusterClient.Do("XADD", args...)
		if err != nil {
			return fmt.Errorf("XADD command failed for message %s: %w", msg.ID, err)
		}
	}

	// Apply TTL if needed
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			r.rdbStats.mu.Lock()
			r.rdbStats.Commands++
			r.rdbStats.mu.Unlock()

			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE command failed: %w", err)
			}
		}
	}

	r.rdbStats.mu.Lock()
	r.rdbStats.Keys++
	r.rdbStats.mu.Unlock()

	return nil
}

func (r *Replicator) recordPipelineStatus(status, message string) {
	if r.store == nil {
		return
	}
	if err := r.store.SetPipelineStatus(status, message); err != nil {
		log.Printf("[state] Failed to set pipeline status: %v", err)
	}
}

func (r *Replicator) recordStage(name, status, message string) {
	if r.store == nil {
		return
	}
	if err := r.store.UpdateStage(name, status, message); err != nil {
		log.Printf("[state] Failed to update stage %s: %v", name, err)
	}
}

func (r *Replicator) recordFlowStage(flowID int, status, message string) {
	r.recordStage(fmt.Sprintf("flow:%d", flowID), status, message)
}

// clearOldFlowStages removes all flow: stages from previous runs to avoid dashboard confusion
func (r *Replicator) clearOldFlowStages() {
	if r.store == nil {
		return
	}
	snap, err := r.store.Load()
	if err != nil {
		log.Printf("[state] Failed to load snapshot for flow cleanup: %v", err)
		return
	}

	// Remove all flow: stages
	for name := range snap.Stages {
		if strings.HasPrefix(name, "flow:") {
			delete(snap.Stages, name)
		}
	}

	// Clear flow-related and incremental metrics (reset for new run)
	for key := range snap.Metrics {
		if strings.Contains(key, "flow") || strings.Contains(key, "incremental") || strings.Contains(key, "rdb") {
			delete(snap.Metrics, key)
		}
	}

	if err := r.store.Write(snap); err != nil {
		log.Printf("[state] Failed to save cleaned snapshot: %v", err)
	}
}

func (r *Replicator) estimateSourceKeys() {
	if r.metrics == nil || r.mainConn == nil {
		return
	}
	reply, err := r.mainConn.Do("INFO", "keyspace")
	if err != nil {
		log.Printf("[state] Failed to fetch source key count: %v", err)
		return
	}
	info, err := redisx.ToString(reply)
	if err != nil {
		log.Printf("[state] Failed to parse source keyspace: %v", err)
		return
	}
	total := parseKeyspaceInfo(info)
	if total >= 0 {
		r.metrics.Set(state.MetricSourceKeysEstimated, total)
	}
}

func parseKeyspaceInfo(info string) float64 {
	var total float64
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !(strings.HasPrefix(line, "db") || strings.HasPrefix(line, "DB")) {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		stats := strings.Split(parts[1], ",")
		for _, stat := range stats {
			stat = strings.TrimSpace(stat)
			if strings.HasPrefix(stat, "keys=") {
				val := strings.TrimPrefix(stat, "keys=")
				if n, err := strconv.ParseFloat(val, 64); err == nil {
					total += n
				}
				break
			}
		}
	}
	return total
}

func (r *Replicator) estimateTargetKeys() {
	if r.metrics == nil {
		return
	}
	if r.clusterClient == nil {
		return
	}
	var total float64
	err := r.clusterClient.ForEachMaster(func(client *redisx.Client) error {
		reply, err := client.Do("DBSIZE")
		if err != nil {
			return err
		}
		count, err := redisx.ToInt64(reply)
		if err != nil {
			return err
		}
		total += float64(count)
		return nil
	})
	if err != nil {
		log.Printf("[state] Failed to fetch target key count: %v", err)
		return
	}
	r.metricsMu.Lock()
	r.initialTargetKeys = total
	r.totalSyncedKeys = 0
	r.metricsMu.Unlock()
	r.metrics.Set(state.MetricTargetKeysInitial, total)
	r.metrics.Set(state.MetricTargetKeysCurrent, total)
}

func (r *Replicator) onSnapshotKey(flowID int) {
	if r.metrics == nil {
		return
	}
	r.metricsMu.Lock()
	if flowID >= len(r.flowKeyCounts) {
		r.metricsMu.Unlock()
		return
	}
	r.flowKeyCounts[flowID]++
	flowCount := r.flowKeyCounts[flowID]
	r.totalSyncedKeys++
	total := r.totalSyncedKeys
	base := r.initialTargetKeys
	r.metricsMu.Unlock()

	if flowCount%500 == 0 {
		r.metrics.SetFlowImported(flowID, float64(flowCount))
	}
	if total%500 == 0 {
		r.metrics.Set(state.MetricSyncedKeys, float64(total))
		r.metrics.Set(state.MetricTargetKeysCurrent, base+float64(total))
	}
}

func (r *Replicator) recordFlowLSN(flowID int, lsn uint64) {
	if r.metrics == nil {
		return
	}
	r.metricsMu.Lock()
	if flowID >= len(r.flowLSNs) {
		r.metricsMu.Unlock()
		return
	}
	r.flowLSNs[flowID] = lsn
	var max uint64
	for _, val := range r.flowLSNs {
		if val > max {
			max = val
		}
	}
	r.metricsMu.Unlock()
	r.metrics.Set(state.MetricIncrementalLSNCurrent, float64(max))
	r.metrics.Set(state.MetricIncrementalLSNApplied, float64(max))
	r.metrics.Set(state.MetricIncrementalLagMs, 0)

	// Update operation statistics
	r.replayStats.mu.Lock()
	r.rdbStats.mu.Lock()

	// RDB phase metrics (snapshot import only)
	r.metrics.Set(state.MetricRdbOpsTotal, float64(r.rdbStats.Commands))
	r.metrics.Set(state.MetricRdbOpsSuccess, float64(r.rdbStats.Commands))
	r.metrics.Set(state.MetricRdbInlineJournalOps, float64(r.rdbStats.InlineJournalOps))

	// Incremental phase metrics (journal streaming only)
	r.metrics.Set(state.MetricIncrementalOpsTotal, float64(r.replayStats.TotalCommands))
	r.metrics.Set(state.MetricIncrementalOpsSuccess, float64(r.replayStats.ReplayedOK))
	r.metrics.Set(state.MetricIncrementalOpsSkipped, float64(r.replayStats.Skipped))
	r.metrics.Set(state.MetricIncrementalOpsFailed, float64(r.replayStats.Failed))

	r.rdbStats.mu.Unlock()
	r.replayStats.mu.Unlock()
}
