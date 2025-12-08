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
	"df2redis/internal/cluster"
	"df2redis/internal/config"
	"df2redis/internal/redisx"
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
	clusterClient *cluster.ClusterClient

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

	// Automatic checkpoint saving
	checkpointInterval time.Duration
	lastCheckpointTime time.Time

	// Channel used to wait for Start() to finish
	done chan struct{}
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
		listeningPort:      6380, // default port
		checkpointMgr:      checkpoint.NewManager(checkpointPath),
		checkpointInterval: checkpointInterval,
		done:               make(chan struct{}),
	}
}

// Start launches the replication workflow
func (r *Replicator) Start() error {
	defer close(r.done) // ensure Stop() gets notified when exiting

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🚀 启动 Dragonfly 复制器")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Connect to Dragonfly
	if err := r.connect(); err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}

	// Perform handshake
	if err := r.handshake(); err != nil {
		return fmt.Errorf("握手失败: %w", err)
	}

	// Initialize Redis client (auto-detects cluster/standalone)
	log.Println("")
	log.Println("🔗 连接到目标 Redis...")
	r.clusterClient = cluster.NewClusterClient(
		r.cfg.Target.Seed,
		r.cfg.Target.Password,
		r.cfg.Target.TLS,
	)
	if err := r.clusterClient.Connect(); err != nil {
		return fmt.Errorf("连接目标 Redis 失败: %w", err)
	}

	// Detect topology
	topology := r.clusterClient.GetTopology()
	if len(topology) > 0 {
		log.Printf("  ✓ Redis Cluster 连接成功（%d 个主节点）", len(topology))
	} else {
		log.Println("  ✓ Redis Standalone 连接成功")
	}

	// Send DFLY SYNC to trigger the RDB transfer
	if err := r.sendDflySync(); err != nil {
		return fmt.Errorf("发送 DFLY SYNC 失败: %w", err)
	}

	// Receive snapshot in parallel
	r.state = StateFullSync
	if err := r.receiveSnapshot(); err != nil {
		return fmt.Errorf("接收快照失败: %w", err)
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🎯 复制器启动成功！")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Receive and parse the journal stream
	if err := r.receiveJournal(); err != nil {
		return fmt.Errorf("接收 Journal 流失败: %w", err)
	}

	return nil
}

// Stop halts replication
func (r *Replicator) Stop() {
	log.Println("⏸  停止复制器...")

	// Cancel the context first
	r.cancel()

	// Close all connections immediately so blocking reads fail fast
	if r.mainConn != nil {
		r.mainConn.Close()
	}
	for i, conn := range r.flowConns {
		if conn != nil {
			log.Printf("  • 关闭 FLOW-%d 连接", i)
			conn.Close()
		}
	}

	// Wait for Start() to finish (including checkpoint persistence)
	log.Println("  • 等待所有 goroutine 退出...")
	<-r.done

	r.state = StateStopped
	log.Println("✓ 复制器已停止")
}

// connect creates the primary connection to Dragonfly for the handshake
func (r *Replicator) connect() error {
	r.state = StateConnecting
	log.Printf("🔗 连接到 Dragonfly: %s", r.cfg.Source.Addr)

	dialCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()

	client, err := redisx.Dial(dialCtx, redisx.Config{
		Addr:     r.cfg.Source.Addr,
		Password: r.cfg.Source.Password,
		TLS:      r.cfg.Source.TLS,
	})

	if err != nil {
		return fmt.Errorf("无法连接到 %s: %w", r.cfg.Source.Addr, err)
	}

	r.mainConn = client
	log.Printf("✓ 主连接建立成功")

	return nil
}

// handshake performs the full handshake procedure
func (r *Replicator) handshake() error {
	r.state = StateHandshaking
	log.Println("")
	log.Println("🤝 开始握手流程")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Step 1: PING
	log.Println("  [1/6] 发送 PING...")
	if err := r.sendPing(); err != nil {
		return err
	}
	log.Println("  ✓ PONG 收到")

	// Step 2: REPLCONF listening-port
	log.Printf("  [2/6] 声明监听端口: %d...", r.listeningPort)
	if err := r.sendListeningPort(); err != nil {
		return err
	}
	log.Println("  ✓ 端口已注册")

	// Step 3: REPLCONF ip-address (optional)
	if r.announceIP != "" {
		log.Printf("  [3/6] 声明 IP 地址: %s...", r.announceIP)
		if err := r.sendIPAddress(); err != nil {
			log.Printf("  ⚠ IP 地址注册失败（主库可能是旧版本）: %v", err)
		} else {
			log.Println("  ✓ IP 地址已注册")
		}
	} else {
		log.Println("  [3/6] 跳过 IP 地址声明")
	}

	// Step 4: REPLCONF capa eof psync2
	log.Println("  [4/6] 声明能力: eof psync2...")
	if err := r.sendCapaEOF(); err != nil {
		return err
	}
	log.Println("  ✓ 能力已声明")

	// Step 5: REPLCONF capa dragonfly
	log.Println("  [5/6] 声明 Dragonfly 兼容性...")
	if err := r.sendCapaDragonfly(); err != nil {
		return err
	}
	log.Printf("  ✓ Dragonfly 版本: %s, Shard 数量: %d", r.masterInfo.Version, r.masterInfo.NumFlows)

	// Step 6: establish FLOW connections
	log.Printf("  [6/6] 建立 %d 个 FLOW...", r.masterInfo.NumFlows)
	if err := r.establishFlows(); err != nil {
		return err
	}
	log.Printf("  ✓ 所有 FLOW 已建立")

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("✓ 握手完成")
	log.Println("")

	r.state = StatePreparation
	return nil
}

// sendPing issues a PING command over the main connection
func (r *Replicator) sendPing() error {
	resp, err := r.mainConn.Do("PING")
	if err != nil {
		return fmt.Errorf("PING 失败: %w", err)
	}

	reply, err := redisx.ToString(resp)
	if err != nil || reply != "PONG" {
		return fmt.Errorf("期望 PONG，但收到: %v", resp)
	}

	return nil
}

// sendListeningPort sends REPLCONF listening-port
func (r *Replicator) sendListeningPort() error {
	resp, err := r.mainConn.Do("REPLCONF", "listening-port", strconv.Itoa(r.listeningPort))
	if err != nil {
		return fmt.Errorf("REPLCONF listening-port 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendIPAddress sends REPLCONF ip-address
func (r *Replicator) sendIPAddress() error {
	resp, err := r.mainConn.Do("REPLCONF", "ip-address", r.announceIP)
	if err != nil {
		return fmt.Errorf("REPLCONF ip-address 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaEOF sends REPLCONF capa eof/capa psync2
func (r *Replicator) sendCapaEOF() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "eof", "capa", "psync2")
	if err != nil {
		return fmt.Errorf("REPLCONF capa eof psync2 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaDragonfly sends REPLCONF capa dragonfly and parses the response
func (r *Replicator) sendCapaDragonfly() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "dragonfly")
	if err != nil {
		return fmt.Errorf("REPLCONF capa dragonfly 失败: %w", err)
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
				return fmt.Errorf("目标是 Redis 或旧版本 Dragonfly（收到简单 OK 响应）")
			}
			return fmt.Errorf("目标不是 Dragonfly（收到未知响应: %s）", str)
		}
		return fmt.Errorf("无法解析 capa dragonfly 响应: %w", err)
	}

	// Validate length
	if len(arr) < 4 {
		return fmt.Errorf("Dragonfly 响应格式错误（长度不足，期望 4 个元素）: %v", arr)
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
		return fmt.Errorf("无法解析 flow 数量: %s", arr[2])
	}
	r.masterInfo.NumFlows = numFlows

	// Element 3: Dragonfly protocol version
	version, err := strconv.Atoi(arr[3])
	if err != nil {
		return fmt.Errorf("无法解析协议版本: %s", arr[3])
	}
	r.masterInfo.Version = DflyVersion(version)

	log.Printf("  → 复制 ID: %s", r.masterInfo.ReplID[:8]+"...")
	log.Printf("  → 同步会话: %s", r.masterInfo.SyncID)
	log.Printf("  → Flow 数量: %d", r.masterInfo.NumFlows)
	log.Printf("  → 协议版本: %s", r.masterInfo.Version)

	return nil
}

// establishFlows creates dedicated FLOW connections for each shard
func (r *Replicator) establishFlows() error {
	numFlows := r.masterInfo.NumFlows
	log.Printf("    • 将建立 %d 个并行 FLOW 连接...", numFlows)

	r.flows = make([]FlowInfo, numFlows)
	r.flowConns = make([]*redisx.Client, numFlows)

	// Create independent TCP connections for each FLOW
	for i := 0; i < numFlows; i++ {
		log.Printf("    • 建立 FLOW-%d 独立连接...", i)

		// 1. Create a new TCP connection
		dialCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
		flowConn, err := redisx.Dial(dialCtx, redisx.Config{
			Addr:     r.cfg.Source.Addr,
			Password: r.cfg.Source.Password,
			TLS:      r.cfg.Source.TLS,
		})
		cancel()

		if err != nil {
			return fmt.Errorf("FLOW-%d 连接失败: %w", i, err)
		}

		r.flowConns[i] = flowConn

		// 2. Send PING (optional, ensures the connection is alive)
		if err := flowConn.Ping(); err != nil {
			return fmt.Errorf("FLOW-%d PING 失败: %w", i, err)
		}

		// 3. Send DFLY FLOW to register this FLOW
		// Command: DFLY FLOW <master_id> <sync_id> <flow_id>
		resp, err := flowConn.Do("DFLY", "FLOW", r.masterInfo.ReplID, r.masterInfo.SyncID, strconv.Itoa(i))
		if err != nil {
			return fmt.Errorf("FLOW-%d 注册失败: %w", i, err)
		}

		// 4. Parse response: ["FULL", <eof_token>] or ["PARTIAL", <eof_token>]
		arr, err := redisx.ToStringSlice(resp)
		if err != nil {
			// Could be a simple OK string
			if err := r.expectOK(resp); err != nil {
				return fmt.Errorf("FLOW-%d 返回错误: %w", i, err)
			}
			r.flows[i] = FlowInfo{
				FlowID:   i,
				State:    "established",
				SyncType: "OK",
				EOFToken: "",
			}
		} else {
			if len(arr) < 2 {
				return fmt.Errorf("FLOW-%d 响应格式错误，期望 2 个元素: %v", i, arr)
			}
			syncType := arr[0]
			eofToken := arr[1]

			r.flows[i] = FlowInfo{
				FlowID:   i,
				State:    "established",
				SyncType: syncType,
				EOFToken: eofToken,
			}

			log.Printf("      → 同步类型: %s, EOF Token: %s...", syncType, eofToken[:min(8, len(eofToken))])
		}

		log.Printf("    ✓ FLOW-%d 连接和注册完成", i)
	}

	log.Printf("    ✓ 所有 %d 个 FLOW 连接已建立", numFlows)
	return nil
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sendDflySync issues DFLY SYNC to trigger the RDB transfer.
// Must be called only after every FLOW is established, otherwise Dragonfly will not send data.
func (r *Replicator) sendDflySync() error {
	log.Println("")
	log.Println("🔄 发送 DFLY SYNC 触发数据传输...")

	// Send DFLY SYNC via the main connection
	// Command: DFLY SYNC <sync_id>
	resp, err := r.mainConn.Do("DFLY", "SYNC", r.masterInfo.SyncID)
	if err != nil {
		return fmt.Errorf("DFLY SYNC 失败: %w", err)
	}

	// Expect OK
	if err := r.expectOK(resp); err != nil {
		return fmt.Errorf("DFLY SYNC 返回错误: %w", err)
	}

	log.Println("  ✓ DFLY SYNC 发送成功，RDB 数据传输已触发")
	return nil
}

// expectOK validates that a Redis reply is the literal OK
func (r *Replicator) expectOK(resp interface{}) error {
	reply, err := redisx.ToString(resp)
	if err != nil {
		return fmt.Errorf("期望 OK，但收到非字符串响应: %v", resp)
	}

	if reply != "OK" {
		return fmt.Errorf("期望 OK，但收到: %s", reply)
	}

	return nil
}

// receiveSnapshot concurrently receives and parses RDB snapshots from all FLOW connections.
// Flow: use the RDB parser to decode data and write it into the target Redis.
// EOF tokens are validated after STARTSTABLE is issued.
func (r *Replicator) receiveSnapshot() error {
	log.Println("")
	log.Println("📦 开始并行接收和解析 RDB 快照...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flows)
	if numFlows == 0 {
		return fmt.Errorf("没有可用的 FLOW")
	}

	log.Printf("  • 将使用 %d 个 FLOW 并行接收和解析 RDB 快照", numFlows)

	// Wait for all goroutines
	var wg sync.WaitGroup
	errChan := make(chan error, numFlows)

	// Stats
	type FlowStats struct {
		KeyCount     int
		SkippedCount int
		ErrorCount   int
	}
	statsMap := make(map[int]*FlowStats)
	var statsMu sync.Mutex

	// Start a goroutine per FLOW to read and parse RDB data
	for i := 0; i < numFlows; i++ {
		statsMap[i] = &FlowStats{}
		wg.Add(1)
		go func(flowID int) {
			defer wg.Done()

			flowConn := r.flowConns[flowID]
			stats := statsMap[flowID]

			log.Printf("  [FLOW-%d] 开始解析 RDB 数据...", flowID)

			// Create RDB parser
			parser := NewRDBParser(flowConn, flowID)

			// 1. Parse header
			if err := parser.ParseHeader(); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 解析 RDB 头部失败: %w", flowID, err)
				return
			}
			log.Printf("  [FLOW-%d] ✓ RDB 头部解析成功", flowID)

			// 2. Parse entries
			for {
				// Observe cancellation
				select {
				case <-r.ctx.Done():
					errChan <- fmt.Errorf("FLOW-%d: 快照接收被取消", flowID)
					return
				default:
				}

				// Parse next entry
				entry, err := parser.ParseNext()
				if err != nil {
					if err == io.EOF {
						log.Printf("  [FLOW-%d] ✓ RDB 解析完成（成功=%d, 跳过=%d, 失败=%d）",
							flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount)
						// FULLSYNC_END received, snapshot done.
						// EOF tokens are read after STARTSTABLE.
						return
					}
					errChan <- fmt.Errorf("FLOW-%d: 解析失败: %w", flowID, err)
					return
				}

				// Skip expired keys
				if entry.IsExpired() {
					statsMu.Lock()
					stats.SkippedCount++
					statsMu.Unlock()
					continue
				}

				// Write entry into Redis
				if err := r.writeRDBEntry(entry); err != nil {
					log.Printf("  [FLOW-%d] ⚠ 写入失败 (key=%s): %v", flowID, entry.Key, err)
					statsMu.Lock()
					stats.ErrorCount++
					statsMu.Unlock()
				} else {
					statsMu.Lock()
					stats.KeyCount++
					statsMu.Unlock()

					// Log progress every 100 keys
					if stats.KeyCount%100 == 0 {
						log.Printf("  [FLOW-%d] • 已导入: %d 个键", flowID, stats.KeyCount)
					}
				}
			}
		}(i)
	}

	// Wait for goroutines
	wg.Wait()
	close(errChan)

	// Drain errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// Final stats
	totalKeys := 0
	totalSkipped := 0
	totalErrors := 0
	for flowID, stats := range statsMap {
		totalKeys += stats.KeyCount
		totalSkipped += stats.SkippedCount
		totalErrors += stats.ErrorCount
	log.Printf("  [FLOW-%d] 统计: 成功=%d, 跳过=%d, 失败=%d",
		flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount)
	}

	log.Printf("  ✓ RDB 全量导入完成: 总计 %d 个键, 跳过 %d 个（已过期）, 失败 %d 个",
		totalKeys, totalSkipped, totalErrors)

	// Dragonfly only sends EOF tokens after STARTSTABLE; reading before that causes a 60s timeout.
	if err := r.sendStartStable(); err != nil {
		return fmt.Errorf("切换稳定同步失败: %w", err)
	}

	if err := r.verifyEofTokens(); err != nil {
		return fmt.Errorf("验证 EOF Token 失败: %w", err)
	}
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

// sendStartStable issues DFLY STARTSTABLE on the main connection
func (r *Replicator) sendStartStable() error {
	log.Println("")
	log.Println("🔄 切换到稳定同步模式...")

	resp, err := r.mainConn.Do("DFLY", "STARTSTABLE", r.masterInfo.SyncID)
	if err != nil {
		return fmt.Errorf("DFLY STARTSTABLE 失败: %w", err)
	}

	if err := r.expectOK(resp); err != nil {
		return fmt.Errorf("DFLY STARTSTABLE 返回错误: %w", err)
	}

	log.Println("  ✓ 已切换到稳定同步模式")
	r.state = StateStableSync
	return nil
}

// verifyEofTokens validates EOF tokens emitted by each FLOW after STARTSTABLE.
// After STARTSTABLE each FLOW sends:
//   1. EOF opcode (0xFF) - 1 byte
//   2. Checksum - 8 bytes
//   3. EOF token - 40 bytes
func (r *Replicator) verifyEofTokens() error {
	log.Println("")
	log.Println("🔐 验证 EOF Token...")
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
				errChan <- fmt.Errorf("FLOW-%d: 未获取到 EOF Token", flowID)
				return
			}
			log.Printf("  [FLOW-%d] → 正在读取 EOF Token (%d 字节)...", flowID, tokenLen)

			// 1. Skip metadata block (0xD3 + 8 bytes). Dragonfly sends it before EOF.
			metadataBuf := make([]byte, 9) // 1 byte opcode + 8 bytes data
			if _, err := io.ReadFull(flowConn, metadataBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取元数据失败: %w", flowID, err)
				return
			}

			// 2. Read EOF opcode (0xFF)
			opcodeBuf := make([]byte, 1)
			if _, err := io.ReadFull(flowConn, opcodeBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 EOF opcode 失败: %w", flowID, err)
				return
			}
			if opcodeBuf[0] != 0xFF {
				errChan <- fmt.Errorf("FLOW-%d: 期望 EOF opcode 0xFF，实际收到 0x%02X", flowID, opcodeBuf[0])
				return
			}

			// 3. Read checksum (8 bytes)
			checksumBuf := make([]byte, 8)
			if _, err := io.ReadFull(flowConn, checksumBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 checksum 失败: %w", flowID, err)
				return
			}

			// 4. Read EOF token (40 bytes)
			tokenBuf := make([]byte, 40)
			if _, err := io.ReadFull(flowConn, tokenBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 EOF token 失败: %w", flowID, err)
				return
			}
			receivedToken := string(tokenBuf)

			// 5. Compare token
			if receivedToken != expectedToken {
				errChan <- fmt.Errorf("FLOW-%d: EOF token 不匹配\n  期望: %s\n  实际: %s",
					flowID, expectedToken, receivedToken)
				return
			}

			log.Printf("  [FLOW-%d] ✓ EOF Token 验证成功", flowID)
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Surface the first error if any
	for err := range errChan {
		return err
	}

	log.Println("  ✓ 所有 FLOW 的 EOF Token 验证完成")
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
	log.Println("📡 开始接收 Journal 流...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flowConns)
	if numFlows == 0 {
		return fmt.Errorf("没有可用的 FLOW 连接")
	}

	log.Printf("  • 并行监听所有 %d 个 FLOW", numFlows)

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
			log.Printf("  ✗ FLOW-%d 错误: %v", flowEntry.FlowID, flowEntry.Error)
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
			log.Printf("  ✗ 重放失败: %v", err)
		}

		// Attempt automatic checkpoint save
		r.tryAutoSaveCheckpoint()

		// Log statistics every 50 entries
		if entriesCount%50 == 0 {
			r.replayStats.mu.Lock()
			log.Printf("  📊 统计: 总计=%d, 成功=%d, 跳过=%d, 失败=%d",
				r.replayStats.TotalCommands,
				r.replayStats.ReplayedOK,
				r.replayStats.Skipped,
				r.replayStats.Failed)

			// Report per-FLOW stats
			for fid, count := range flowStats {
				lsn := r.replayStats.FlowLSNs[fid]
				log.Printf("    FLOW-%d: %d 条, LSN=%d", fid, count, lsn)
			}
			r.replayStats.mu.Unlock()
		}
	}

	log.Println("  • 所有 FLOW 的 Journal 流已结束")

	// Persist final checkpoint if enabled
	if r.cfg.Checkpoint.Enabled {
		log.Println("  💾 保存最终 checkpoint...")
		if err := r.saveCheckpoint(); err != nil {
			log.Printf("  ⚠ 保存最终 checkpoint 失败: %v", err)
		} else {
			log.Println("  ✓ Checkpoint 已保存")
		}
	}

	return nil
}

// readFlowJournal reads the journal stream for a specific FLOW
func (r *Replicator) readFlowJournal(flowID int, entryChan chan<- *FlowEntry, wg *sync.WaitGroup) {
	defer wg.Done()

	reader := NewJournalReader(r.flowConns[flowID])
	log.Printf("  [FLOW-%d] 开始接收 Journal 流", flowID)

	for {
		// Observe cancellation
		select {
		case <-r.ctx.Done():
			log.Printf("  [FLOW-%d] 收到停止信号", flowID)
			return
		default:
		}

		// Read entry
		entry, err := reader.ReadEntry()
		if err != nil {
			if err == io.EOF {
				log.Printf("  [FLOW-%d] Journal 流结束（EOF）", flowID)
				return
			}
			// Send error to channel
			entryChan <- &FlowEntry{
				FlowID: flowID,
				Error:  fmt.Errorf("读取失败: %w", err),
			}
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

// replayCommand replays a single journal command into Redis Cluster
func (r *Replicator) replayCommand(flowID int, entry *JournalEntry) error {
	switch entry.Opcode {
	case OpSelect:
		// Redis Cluster only exposes DB 0, ignore SELECT
		r.replayStats.mu.Lock()
		r.replayStats.Skipped++
		r.replayStats.mu.Unlock()
		return nil

	case OpPing:
		// Ignore heartbeat
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
		return nil

	case OpExpired:
		// Handle expired key by re-applying TTL using PEXPIRE
		if err := r.handleExpiredKey(entry); err != nil {
			r.replayStats.mu.Lock()
			r.replayStats.Failed++
			r.replayStats.mu.Unlock()
			return fmt.Errorf("处理过期键失败: %w", err)
		}
		r.replayStats.mu.Lock()
		r.replayStats.ReplayedOK++
		r.replayStats.LastReplayTime = time.Now()
		r.replayStats.mu.Unlock()
		return nil

	case OpCommand:
		// Check for global commands
		cmd := strings.ToUpper(entry.Command)
		if isGlobalCommand(cmd) {
			log.Printf("  ⚠ 跳过全局命令: %s（需要多分片协调）", cmd)
			r.replayStats.mu.Lock()
			r.replayStats.Skipped++
			r.replayStats.mu.Unlock()
			return nil
		}

		// Execute regular command
		if err := r.executeCommand(entry); err != nil {
			r.replayStats.mu.Lock()
			r.replayStats.Failed++
			r.replayStats.mu.Unlock()
			return fmt.Errorf("执行命令失败: %w", err)
		}

		r.replayStats.mu.Lock()
		r.replayStats.ReplayedOK++
		r.replayStats.LastReplayTime = time.Now()
		r.replayStats.mu.Unlock()
		return nil

	default:
		return fmt.Errorf("未知的 opcode: %d", entry.Opcode)
	}
}

// handleExpiredKey sets TTL for expired key events
func (r *Replicator) handleExpiredKey(entry *JournalEntry) error {
	if len(entry.Args) == 0 {
		return fmt.Errorf("EXPIRED 命令缺少 key 参数")
	}

	key := entry.Args[0]

	// Assume TTL is 1ms (key already expired). Can be refined if Dragonfly publishes TTL.
	ttlMs := int64(1)

	_, err := r.clusterClient.Do("PEXPIRE", key, fmt.Sprintf("%d", ttlMs))
	if err != nil {
		return err
	}

	return nil
}

// executeCommand executes a journal command verbatim
func (r *Replicator) executeCommand(entry *JournalEntry) error {
	// Copy args
	args := make([]string, len(entry.Args))
	copy(args, entry.Args)

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
		return fmt.Errorf("保存 checkpoint 失败: %w", err)
	}

	r.lastCheckpointTime = time.Now()
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
			log.Printf("  ⚠ 自动保存 checkpoint 失败: %v", err)
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
		return false, fmt.Errorf("检查键存在性失败: %w", err)
	}

	exists, ok := reply.(int64)
	if !ok {
		return false, fmt.Errorf("EXISTS 命令返回类型错误")
	}

	if exists == 0 {
		return true, nil // key does not exist
	}

	// Key exists
	if policy == "panic" {
		log.Printf("  ⚠️ 检测到重复键: %s (policy=panic，程序终止)", key)
		return false, fmt.Errorf("检测到重复键: %s", key)
	}

	// policy == skip
	log.Printf("  ⚠️ 跳过重复键: %s (policy=skip)", key)
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

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST:
		return r.writeHash(entry)

	case RDB_TYPE_LIST_QUICKLIST_2, 18: // 18 is Dragonfly listpack encoding for lists
		return r.writeList(entry)

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET:
		return r.writeSet(entry)

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST:
		return r.writeZSet(entry)

	default:
		return fmt.Errorf("暂不支持的 RDB 类型: %d", entry.Type)
	}
}

// writeString handles string entries
func (r *Replicator) writeString(entry *RDBEntry) error {
	// Extract value
	strVal, ok := entry.Value.(*StringValue)
	if !ok {
		return fmt.Errorf("String 类型值转换失败")
	}

	// Write value
	_, err := r.clusterClient.Do("SET", entry.Key, strVal.Value)
	if err != nil {
		return fmt.Errorf("SET 命令失败: %w", err)
	}

	// Apply TTL if needed
	if entry.ExpireMs > 0 {
		// Compute remaining TTL
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE 命令失败: %w", err)
			}
		}
	}

	return nil
}

// writeHash handles hash entries
func (r *Replicator) writeHash(entry *RDBEntry) error {
	// Extract value
	hashVal, ok := entry.Value.(*HashValue)
	if !ok {
		return fmt.Errorf("Hash 类型值转换失败")
	}

	// Remove existing key to avoid stale fields
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Write all fields using HSET key field1 value1 ...
	log.Printf("  [DEBUG] writeHash: key=%s, fields=%d", entry.Key, len(hashVal.Fields))
	if len(hashVal.Fields) > 0 {
		args := []string{entry.Key}
		for field, value := range hashVal.Fields {
			args = append(args, field, value)
			log.Printf("  [DEBUG]   field=%s, value=%s", field, value)
		}
		log.Printf("  [DEBUG] 执行 HSET 命令，参数数量=%d", len(args))
		_, err := r.clusterClient.Do("HSET", args...)
		if err != nil {
			return fmt.Errorf("HSET 命令失败: %w", err)
		}
		log.Printf("  [DEBUG] HSET 命令执行成功")
	} else {
		log.Printf("  [DEBUG] 字段为空，跳过写入")
	}

	// Apply TTL if needed
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE 命令失败: %w", err)
			}
		}
	}

	return nil
}

// writeList handles list entries
func (r *Replicator) writeList(entry *RDBEntry) error {
	// Extract value
	listVal, ok := entry.Value.(*ListValue)
	if !ok {
		return fmt.Errorf("List 类型值转换失败")
	}

	// Remove existing key
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert elements with RPUSH
	if len(listVal.Elements) > 0 {
		args := []string{entry.Key}
		for _, elem := range listVal.Elements {
			args = append(args, elem)
		}
		_, err := r.clusterClient.Do("RPUSH", args...)
		if err != nil {
			return fmt.Errorf("RPUSH 命令失败: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE 命令失败: %w", err)
			}
		}
	}

	return nil
}

// writeSet handles set entries
func (r *Replicator) writeSet(entry *RDBEntry) error {
	// Extract value
	setVal, ok := entry.Value.(*SetValue)
	if !ok {
		return fmt.Errorf("Set 类型值转换失败")
	}

	// Remove existing key
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert members via SADD
	if len(setVal.Members) > 0 {
		args := []string{entry.Key}
		for _, member := range setVal.Members {
			args = append(args, member)
		}
		_, err := r.clusterClient.Do("SADD", args...)
		if err != nil {
			return fmt.Errorf("SADD 命令失败: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE 命令失败: %w", err)
			}
		}
	}

	return nil
}

// writeZSet handles sorted set entries
func (r *Replicator) writeZSet(entry *RDBEntry) error {
	// Extract value
	zsetVal, ok := entry.Value.(*ZSetValue)
	if !ok {
		return fmt.Errorf("ZSet 类型值转换失败")
	}

	// Remove existing key
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// Insert members via ZADD key score member ...
	if len(zsetVal.Members) > 0 {
		args := []string{entry.Key}
		for _, zm := range zsetVal.Members {
			args = append(args, fmt.Sprintf("%f", zm.Score), zm.Member)
		}
		_, err := r.clusterClient.Do("ZADD", args...)
		if err != nil {
			return fmt.Errorf("ZADD 命令失败: %w", err)
		}
	}

	// Apply TTL
	if entry.ExpireMs > 0 {
		remainingMs := entry.ExpireMs - getCurrentTimeMillis()
		if remainingMs > 0 {
			_, err := r.clusterClient.Do("PEXPIRE", entry.Key, fmt.Sprintf("%d", remainingMs))
			if err != nil {
				return fmt.Errorf("PEXPIRE 命令失败: %w", err)
			}
		}
	}

	return nil
}
