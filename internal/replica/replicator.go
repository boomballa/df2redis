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

// Replicator 负责与 Dragonfly 建立复制关系
type Replicator struct {
	cfg    *config.Config
	ctx    context.Context
	cancel context.CancelFunc

	// 主连接（用于握手）
	mainConn *redisx.Client

	// 每个 FLOW 的独立连接
	flowConns []*redisx.Client

	// Redis Cluster 客户端（用于命令重放）
	clusterClient *cluster.ClusterClient

	// Checkpoint 管理器
	checkpointMgr *checkpoint.Manager

	// 复制状态
	state      ReplicaState
	masterInfo MasterInfo
	flows      []FlowInfo

	// 配置
	listeningPort int
	announceIP    string

	// 统计信息
	replayStats ReplayStats

	// Checkpoint 自动保存
	checkpointInterval time.Duration
	lastCheckpointTime time.Time

	// 用于等待 Start() 完成的 channel
	done chan struct{}
}

// NewReplicator 创建一个新的复制器
func NewReplicator(cfg *config.Config) *Replicator {
	ctx, cancel := context.WithCancel(context.Background())

	// Checkpoint 文件路径：使用配置中的路径或默认路径
	checkpointPath := cfg.ResolveCheckpointPath()

	// Checkpoint 保存间隔：从配置读取（默认 10 秒）
	checkpointInterval := time.Duration(cfg.Checkpoint.Interval) * time.Second

	return &Replicator{
		cfg:                cfg,
		ctx:                ctx,
		cancel:             cancel,
		state:              StateDisconnected,
		listeningPort:      6380, // 默认端口
		checkpointMgr:      checkpoint.NewManager(checkpointPath),
		checkpointInterval: checkpointInterval,
		done:               make(chan struct{}),
	}
}

// Start 启动复制流程
func (r *Replicator) Start() error {
	defer close(r.done) // 确保退出时通知 Stop()

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🚀 启动 Dragonfly 复制器")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 连接到 Dragonfly
	if err := r.connect(); err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}

	// 执行握手
	if err := r.handshake(); err != nil {
		return fmt.Errorf("握手失败: %w", err)
	}

	// 初始化 Redis 客户端（自动检测 Cluster/Standalone）
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

	// 检测模式
	topology := r.clusterClient.GetTopology()
	if len(topology) > 0 {
		log.Printf("  ✓ Redis Cluster 连接成功（%d 个主节点）", len(topology))
	} else {
		log.Println("  ✓ Redis Standalone 连接成功")
	}

	// 发送 DFLY SYNC 触发 RDB 数据传输
	if err := r.sendDflySync(); err != nil {
		return fmt.Errorf("发送 DFLY SYNC 失败: %w", err)
	}

	// 接收 RDB 快照（并行）
	r.state = StateFullSync
	if err := r.receiveSnapshot(); err != nil {
		return fmt.Errorf("接收快照失败: %w", err)
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🎯 复制器启动成功！")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 接收并解析 Journal 流
	if err := r.receiveJournal(); err != nil {
		return fmt.Errorf("接收 Journal 流失败: %w", err)
	}

	return nil
}

// Stop 停止复制
func (r *Replicator) Stop() {
	log.Println("⏸  停止复制器...")

	// 先取消上下文
	r.cancel()

	// 立即关闭所有连接，强制阻塞的读取操作失败
	if r.mainConn != nil {
		r.mainConn.Close()
	}
	for i, conn := range r.flowConns {
		if conn != nil {
			log.Printf("  • 关闭 FLOW-%d 连接", i)
			conn.Close()
		}
	}

	// 等待 Start() 完成（包括 checkpoint 保存）
	log.Println("  • 等待所有 goroutine 退出...")
	<-r.done

	r.state = StateStopped
	log.Println("✓ 复制器已停止")
}

// connect 连接到 Dragonfly 主库（建立主连接用于握手）
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

// handshake 执行完整的握手流程
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

	// Step 3: REPLCONF ip-address (可选)
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

	// Step 6: 建立 FLOW
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

// sendPing 发送 PING 命令（使用主连接）
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

// sendListeningPort 发送 REPLCONF listening-port
func (r *Replicator) sendListeningPort() error {
	resp, err := r.mainConn.Do("REPLCONF", "listening-port", strconv.Itoa(r.listeningPort))
	if err != nil {
		return fmt.Errorf("REPLCONF listening-port 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendIPAddress 发送 REPLCONF ip-address
func (r *Replicator) sendIPAddress() error {
	resp, err := r.mainConn.Do("REPLCONF", "ip-address", r.announceIP)
	if err != nil {
		return fmt.Errorf("REPLCONF ip-address 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaEOF 发送 REPLCONF capa eof capa psync2
func (r *Replicator) sendCapaEOF() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "eof", "capa", "psync2")
	if err != nil {
		return fmt.Errorf("REPLCONF capa eof psync2 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaDragonfly 发送 REPLCONF capa dragonfly 并解析响应
func (r *Replicator) sendCapaDragonfly() error {
	resp, err := r.mainConn.Do("REPLCONF", "capa", "dragonfly")
	if err != nil {
		return fmt.Errorf("REPLCONF capa dragonfly 失败: %w", err)
	}

	// 解析响应
	// Dragonfly 实际响应格式（v1.30.0）：
	// 数组: [replication_id, sync_version, unknown_param, num_flows]
	// 例如: ["16c2763d...", "SYNC5", 8, 4]

	arr, err := redisx.ToStringSlice(resp)
	if err != nil {
		// 不是数组，尝试作为简单字符串解析
		if str, err2 := redisx.ToString(resp); err2 == nil {
			// 检查是否是 OK（旧版本或 Redis）
			if str == "OK" {
				return fmt.Errorf("目标是 Redis 或旧版本 Dragonfly（收到简单 OK 响应）")
			}
			return fmt.Errorf("目标不是 Dragonfly（收到未知响应: %s）", str)
		}
		return fmt.Errorf("无法解析 capa dragonfly 响应: %w", err)
	}

	// 验证数组长度
	if len(arr) < 4 {
		return fmt.Errorf("Dragonfly 响应格式错误（长度不足，期望 4 个元素）: %v", arr)
	}

	// 响应格式：[master_id, sync_id, flow_count, version]
	// 例如：["16c2763d...", "SYNC11", 8, 4]

	// 第一个元素：复制 ID (master_id)
	r.masterInfo.ReplID = arr[0]

	// 第二个元素：同步会话 ID (sync_id，如 "SYNC11")
	r.masterInfo.SyncID = arr[1]

	// 第三个元素：flow 数量
	numFlows, err := strconv.Atoi(arr[2])
	if err != nil {
		return fmt.Errorf("无法解析 flow 数量: %s", arr[2])
	}
	r.masterInfo.NumFlows = numFlows

	// 第四个元素：Dragonfly 协议版本
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

// establishFlows 为每个 shard 建立独立的 FLOW 连接
func (r *Replicator) establishFlows() error {
	numFlows := r.masterInfo.NumFlows
	log.Printf("    • 将建立 %d 个并行 FLOW 连接...", numFlows)

	r.flows = make([]FlowInfo, numFlows)
	r.flowConns = make([]*redisx.Client, numFlows)

	// 为每个 FLOW 建立独立的 TCP 连接
	for i := 0; i < numFlows; i++ {
		log.Printf("    • 建立 FLOW-%d 独立连接...", i)

		// 1. 创建新的 TCP 连接
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

		// 2. 在新连接上发送 PING（可选，确保连接可用）
		if err := flowConn.Ping(); err != nil {
			return fmt.Errorf("FLOW-%d PING 失败: %w", i, err)
		}

		// 4. 发送 DFLY FLOW 命令注册此 FLOW
		// 命令格式: DFLY FLOW <master_id> <sync_id> <flow_id>
		resp, err := flowConn.Do("DFLY", "FLOW", r.masterInfo.ReplID, r.masterInfo.SyncID, strconv.Itoa(i))
		if err != nil {
			return fmt.Errorf("FLOW-%d 注册失败: %w", i, err)
		}

		// 4. 解析响应：["FULL", <eof_token>] 或 ["PARTIAL", <eof_token>]
		arr, err := redisx.ToStringSlice(resp)
		if err != nil {
			// 可能是简单的 OK
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

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sendDflySync 发送 DFLY SYNC 命令触发 RDB 数据传输
// 必须在所有 FLOW 建立后调用，否则 Dragonfly 不会发送数据
func (r *Replicator) sendDflySync() error {
	log.Println("")
	log.Println("🔄 发送 DFLY SYNC 触发数据传输...")

	// 使用主连接发送 DFLY SYNC 命令
	// 命令格式: DFLY SYNC <sync_id>
	resp, err := r.mainConn.Do("DFLY", "SYNC", r.masterInfo.SyncID)
	if err != nil {
		return fmt.Errorf("DFLY SYNC 失败: %w", err)
	}

	// 期望返回 OK
	if err := r.expectOK(resp); err != nil {
		return fmt.Errorf("DFLY SYNC 返回错误: %w", err)
	}

	log.Println("  ✓ DFLY SYNC 发送成功，RDB 数据传输已触发")
	return nil
}

// expectOK 检查响应是否为 OK
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

// receiveSnapshot 并行接收和解析所有 FLOW 的 RDB 快照
// 流程：使用 RDB 解析器解析数据，写入目标 Redis
// EOF Token 将在发送 STARTSTABLE 后单独验证
func (r *Replicator) receiveSnapshot() error {
	log.Println("")
	log.Println("📦 开始并行接收和解析 RDB 快照...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flows)
	if numFlows == 0 {
		return fmt.Errorf("没有可用的 FLOW")
	}

	log.Printf("  • 将使用 %d 个 FLOW 并行接收和解析 RDB 快照", numFlows)

	// 使用 WaitGroup 等待所有 goroutine 完成
	var wg sync.WaitGroup
	errChan := make(chan error, numFlows)

	// 统计信息
	type FlowStats struct {
		KeyCount     int
		SkippedCount int
		ErrorCount   int
	}
	statsMap := make(map[int]*FlowStats)
	var statsMu sync.Mutex

	// 为每个 FLOW 启动一个 goroutine 接收和解析 RDB 数据
	for i := 0; i < numFlows; i++ {
		statsMap[i] = &FlowStats{}
		wg.Add(1)
		go func(flowID int) {
			defer wg.Done()

			flowConn := r.flowConns[flowID]
			stats := statsMap[flowID]

			log.Printf("  [FLOW-%d] 开始解析 RDB 数据...", flowID)

			// 创建 RDB 解析器
			parser := NewRDBParser(flowConn, flowID)

			// 1. 解析 RDB 头部
			if err := parser.ParseHeader(); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 解析 RDB 头部失败: %w", flowID, err)
				return
			}
			log.Printf("  [FLOW-%d] ✓ RDB 头部解析成功", flowID)

			// 2. 逐个解析键值对
			for {
				// 检查取消信号
				select {
				case <-r.ctx.Done():
					errChan <- fmt.Errorf("FLOW-%d: 快照接收被取消", flowID)
					return
				default:
				}

				// 解析下一个 entry
				entry, err := parser.ParseNext()
				if err != nil {
					if err == io.EOF {
						log.Printf("  [FLOW-%d] ✓ RDB 解析完成（成功=%d, 跳过=%d, 失败=%d）",
							flowID, stats.KeyCount, stats.SkippedCount, stats.ErrorCount)
						// FULLSYNC_END 已接收，RDB 解析完成
						// EOF Token 将在发送 STARTSTABLE 后读取
						return
					}
					errChan <- fmt.Errorf("FLOW-%d: 解析失败: %w", flowID, err)
					return
				}

				// 跳过已过期的键
				if entry.IsExpired() {
					statsMu.Lock()
					stats.SkippedCount++
					statsMu.Unlock()
					continue
				}

				// 写入 Redis
				if err := r.writeRDBEntry(entry); err != nil {
					log.Printf("  [FLOW-%d] ⚠ 写入失败 (key=%s): %v", flowID, entry.Key, err)
					statsMu.Lock()
					stats.ErrorCount++
					statsMu.Unlock()
				} else {
					statsMu.Lock()
					stats.KeyCount++
					statsMu.Unlock()

					// 每 100 个键打印一次进度
					if stats.KeyCount%100 == 0 {
						log.Printf("  [FLOW-%d] • 已导入: %d 个键", flowID, stats.KeyCount)
					}
				}
			}
		}(i)
	}

	// 等待所有 goroutine 完成
	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// 打印最终统计
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

	// Dragonfly 只会在收到 STARTSTABLE 之后发送 EOF Token；如果提前读取会导致 60s 超时。
	if err := r.sendStartStable(); err != nil {
		return fmt.Errorf("切换稳定同步失败: %w", err)
	}

	if err := r.verifyEofTokens(); err != nil {
		return fmt.Errorf("验证 EOF Token 失败: %w", err)
	}
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

// sendStartStable 发送 DFLY STARTSTABLE 命令（使用主连接）
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

// verifyEofTokens 验证所有 FLOW 的 EOF Token
// 在 STARTSTABLE 之后，每个 FLOW 会发送：
//   1. EOF opcode (0xFF) - 1 字节
//   2. Checksum - 8 字节
//   3. EOF Token - 40 字节
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

			// 1. 跳过元数据块（0xD3 + 8 字节）
			// Dragonfly 在 EOF 之前发送一个元数据块
			metadataBuf := make([]byte, 9) // 1 byte opcode + 8 bytes data
			if _, err := io.ReadFull(flowConn, metadataBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取元数据失败: %w", flowID, err)
				return
			}

			// 2. 读取 EOF opcode (0xFF)
			opcodeBuf := make([]byte, 1)
			if _, err := io.ReadFull(flowConn, opcodeBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 EOF opcode 失败: %w", flowID, err)
				return
			}
			if opcodeBuf[0] != 0xFF {
				errChan <- fmt.Errorf("FLOW-%d: 期望 EOF opcode 0xFF，实际收到 0x%02X", flowID, opcodeBuf[0])
				return
			}

			// 2. 读取 checksum (8 字节)
			checksumBuf := make([]byte, 8)
			if _, err := io.ReadFull(flowConn, checksumBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 checksum 失败: %w", flowID, err)
				return
			}

			// 3. 读取 EOF token (40 字节)
			tokenBuf := make([]byte, 40)
			if _, err := io.ReadFull(flowConn, tokenBuf); err != nil {
				errChan <- fmt.Errorf("FLOW-%d: 读取 EOF token 失败: %w", flowID, err)
				return
			}
			receivedToken := string(tokenBuf)

			// 4. 验证 token 是否匹配
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

	// 检查错误
	for err := range errChan {
		return err
	}

	log.Println("  ✓ 所有 FLOW 的 EOF Token 验证完成")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return nil
}

// FlowEntry 表示带有 FLOW ID 的 Journal Entry
type FlowEntry struct {
	FlowID int
	Entry  *JournalEntry
	Error  error
}

// receiveJournal 接收并解析 Journal 流（并行监听所有 FLOW）
func (r *Replicator) receiveJournal() error {
	log.Println("")
	log.Println("📡 开始接收 Journal 流...")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	numFlows := len(r.flowConns)
	if numFlows == 0 {
		return fmt.Errorf("没有可用的 FLOW 连接")
	}

	log.Printf("  • 并行监听所有 %d 个 FLOW", numFlows)

	// 创建 channel 接收所有 FLOW 的 Entry
	entryChan := make(chan *FlowEntry, 100)

	// 为每个 FLOW 启动一个 goroutine
	var wg sync.WaitGroup
	for i := 0; i < numFlows; i++ {
		wg.Add(1)
		go r.readFlowJournal(i, entryChan, &wg)
	}

	// 启动一个 goroutine 等待所有 FLOW 结束后关闭 channel
	go func() {
		wg.Wait()
		close(entryChan)
	}()

	// 主循环处理 Entry
	entriesCount := 0
	currentDB := uint64(0)
	flowStats := make(map[int]int) // 每个 FLOW 的 Entry 计数

	for flowEntry := range entryChan {
		// 检查错误
		if flowEntry.Error != nil {
			log.Printf("  ✗ FLOW-%d 错误: %v", flowEntry.FlowID, flowEntry.Error)
			continue
		}

		entriesCount++
		flowStats[flowEntry.FlowID]++
		entry := flowEntry.Entry

		// 更新当前数据库
		if entry.Opcode == OpSelect {
			currentDB = entry.DbIndex
		}

		// 显示解析的命令
		r.displayFlowEntry(flowEntry.FlowID, entry, currentDB, entriesCount)

		// 重放命令到 Redis Cluster
		r.replayStats.mu.Lock()
		r.replayStats.TotalCommands++
		r.replayStats.mu.Unlock()

		if err := r.replayCommand(flowEntry.FlowID, entry); err != nil {
			log.Printf("  ✗ 重放失败: %v", err)
		}

		// 尝试自动保存 checkpoint
		r.tryAutoSaveCheckpoint()

		// 每 50 条打印一次统计
		if entriesCount%50 == 0 {
			r.replayStats.mu.Lock()
			log.Printf("  📊 统计: 总计=%d, 成功=%d, 跳过=%d, 失败=%d",
				r.replayStats.TotalCommands,
				r.replayStats.ReplayedOK,
				r.replayStats.Skipped,
				r.replayStats.Failed)

			// 打印每个 FLOW 的统计
			for fid, count := range flowStats {
				lsn := r.replayStats.FlowLSNs[fid]
				log.Printf("    FLOW-%d: %d 条, LSN=%d", fid, count, lsn)
			}
			r.replayStats.mu.Unlock()
		}
	}

	log.Println("  • 所有 FLOW 的 Journal 流已结束")

	// 最终保存 checkpoint（如果启用）
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

// readFlowJournal 读取单个 FLOW 的 Journal 流
func (r *Replicator) readFlowJournal(flowID int, entryChan chan<- *FlowEntry, wg *sync.WaitGroup) {
	defer wg.Done()

	reader := NewJournalReader(r.flowConns[flowID])
	log.Printf("  [FLOW-%d] 开始接收 Journal 流", flowID)

	for {
		// 检查取消信号
		select {
		case <-r.ctx.Done():
			log.Printf("  [FLOW-%d] 收到停止信号", flowID)
			return
		default:
		}

		// 读取一条 Entry
		entry, err := reader.ReadEntry()
		if err != nil {
			if err == io.EOF {
				log.Printf("  [FLOW-%d] Journal 流结束（EOF）", flowID)
				return
			}
			// 发送错误到 channel
			entryChan <- &FlowEntry{
				FlowID: flowID,
				Error:  fmt.Errorf("读取失败: %w", err),
			}
			return
		}

		// 发送 Entry 到 channel
		entryChan <- &FlowEntry{
			FlowID: flowID,
			Entry:  entry,
		}
	}
}

// displayFlowEntry 显示带 FLOW ID 的 Journal Entry
func (r *Replicator) displayFlowEntry(flowID int, entry *JournalEntry, currentDB uint64, count int) {
	// 根据 opcode 不同显示不同格式
	switch entry.Opcode {
	case OpSelect:
		log.Printf("  [%d] FLOW-%d: SELECT DB=%d", count, flowID, entry.DbIndex)

	case OpLSN:
		log.Printf("  [%d] FLOW-%d: LSN %d", count, flowID, entry.LSN)

	case OpPing:
		log.Printf("  [%d] FLOW-%d: PING", count, flowID)

	case OpCommand:
		// 格式化参数
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

// displayEntry 显示解析的 Journal Entry
func (r *Replicator) displayEntry(entry *JournalEntry, currentDB uint64, count int) {
	// 根据 opcode 不同显示不同格式
	switch entry.Opcode {
	case OpSelect:
		log.Printf("  [%d] SELECT DB=%d", count, entry.DbIndex)

	case OpLSN:
		log.Printf("  [%d] LSN %d", count, entry.LSN)

	case OpPing:
		log.Printf("  [%d] PING", count)

	case OpCommand:
		// 格式化参数
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

// GetState 获取当前状态
func (r *Replicator) GetState() ReplicaState {
	return r.state
}

// GetMasterInfo 获取主库信息
func (r *Replicator) GetMasterInfo() MasterInfo {
	return r.masterInfo
}

// GetFlows 获取所有 Flow 信息
func (r *Replicator) GetFlows() []FlowInfo {
	return r.flows
}

// ReplayStats 记录命令重放统计
type ReplayStats struct {
	mu             sync.Mutex
	TotalCommands  int64
	ReplayedOK     int64
	Skipped        int64
	Failed         int64
	FlowLSNs       map[int]uint64 // 每个 FLOW 的最新 LSN
	LastReplayTime time.Time
}

// replayCommand 重放单条命令到 Redis Cluster
func (r *Replicator) replayCommand(flowID int, entry *JournalEntry) error {
	switch entry.Opcode {
	case OpSelect:
		// Redis Cluster 只有 DB 0，忽略 SELECT 命令
		r.replayStats.mu.Lock()
		r.replayStats.Skipped++
		r.replayStats.mu.Unlock()
		return nil

	case OpPing:
		// 忽略 PING 心跳
		r.replayStats.mu.Lock()
		r.replayStats.Skipped++
		r.replayStats.mu.Unlock()
		return nil

	case OpLSN:
		// 记录 LSN，不执行
		r.replayStats.mu.Lock()
		if r.replayStats.FlowLSNs == nil {
			r.replayStats.FlowLSNs = make(map[int]uint64)
		}
		r.replayStats.FlowLSNs[flowID] = entry.LSN
		r.replayStats.mu.Unlock()
		return nil

	case OpExpired:
		// 处理过期键：使用 PEXPIRE 设置剩余 TTL
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
		// 检查是否为全局命令
		cmd := strings.ToUpper(entry.Command)
		if isGlobalCommand(cmd) {
			log.Printf("  ⚠ 跳过全局命令: %s（需要多分片协调）", cmd)
			r.replayStats.mu.Lock()
			r.replayStats.Skipped++
			r.replayStats.mu.Unlock()
			return nil
		}

		// 执行普通命令
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

// handleExpiredKey 处理过期键
func (r *Replicator) handleExpiredKey(entry *JournalEntry) error {
	if len(entry.Args) == 0 {
		return fmt.Errorf("EXPIRED 命令缺少 key 参数")
	}

	key := entry.Args[0]

	// 假设 TTL 为 1ms（键已过期）
	// 实际实现中可以从 Args 中解析 TTL（如果 Dragonfly 提供）
	ttlMs := int64(1)

	_, err := r.clusterClient.Do("PEXPIRE", key, fmt.Sprintf("%d", ttlMs))
	if err != nil {
		return err
	}

	return nil
}

// executeCommand 执行普通命令
func (r *Replicator) executeCommand(entry *JournalEntry) error {
	// 构建完整的命令参数列表
	args := make([]string, len(entry.Args))
	copy(args, entry.Args)

	// 执行命令
	_, err := r.clusterClient.Do(entry.Command, args...)
	return err
}

// isGlobalCommand 检查是否为全局命令（需要多分片协调）
func isGlobalCommand(cmd string) bool {
	globalCmds := map[string]bool{
		"FLUSHDB":                true,
		"FLUSHALL":               true,
		"DFLYCLUSTER FLUSHSLOTS": true,
	}
	return globalCmds[cmd]
}

// saveCheckpoint 保存当前 checkpoint
func (r *Replicator) saveCheckpoint() error {
	r.replayStats.mu.Lock()
	defer r.replayStats.mu.Unlock()

	// 构建 checkpoint
	cp := &checkpoint.Checkpoint{
		ReplicationID: r.masterInfo.ReplID,
		SessionID:     r.masterInfo.SyncID,
		NumFlows:      len(r.flows),
		FlowLSNs:      make(map[int]uint64),
	}

	// 复制 FlowLSNs
	for flowID, lsn := range r.replayStats.FlowLSNs {
		cp.FlowLSNs[flowID] = lsn
	}

	// 保存到文件
	if err := r.checkpointMgr.Save(cp); err != nil {
		return fmt.Errorf("保存 checkpoint 失败: %w", err)
	}

	r.lastCheckpointTime = time.Now()
	return nil
}

// tryAutoSaveCheckpoint 尝试自动保存 checkpoint（如果时间到了）
func (r *Replicator) tryAutoSaveCheckpoint() {
	// 检查是否启用了 checkpoint
	if !r.cfg.Checkpoint.Enabled {
		return
	}

	if time.Since(r.lastCheckpointTime) >= r.checkpointInterval {
		if err := r.saveCheckpoint(); err != nil {
			log.Printf("  ⚠ 自动保存 checkpoint 失败: %v", err)
		}
	}
}

// writeRDBEntry 将 RDB entry 写入 Redis
func (r *Replicator) writeRDBEntry(entry *RDBEntry) error {
	switch entry.Type {
	case RDB_TYPE_STRING:
		return r.writeString(entry)

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST:
		return r.writeHash(entry)

	case RDB_TYPE_LIST_QUICKLIST_2, 18: // 18 是 Dragonfly 使用的 List Listpack 类型
		return r.writeList(entry)

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET:
		return r.writeSet(entry)

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST:
		return r.writeZSet(entry)

	default:
		return fmt.Errorf("暂不支持的 RDB 类型: %d", entry.Type)
	}
}

// writeString 写入 String 类型的键值对
func (r *Replicator) writeString(entry *RDBEntry) error {
	// 1. 提取值
	strVal, ok := entry.Value.(*StringValue)
	if !ok {
		return fmt.Errorf("String 类型值转换失败")
	}

	// 2. 写入键值
	_, err := r.clusterClient.Do("SET", entry.Key, strVal.Value)
	if err != nil {
		return fmt.Errorf("SET 命令失败: %w", err)
	}

	// 3. 设置 TTL（如果有）
	if entry.ExpireMs > 0 {
		// 计算剩余 TTL（毫秒）
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

// writeHash 写入 Hash 类型的键值对
func (r *Replicator) writeHash(entry *RDBEntry) error {
	// 1. 提取值
	hashVal, ok := entry.Value.(*HashValue)
	if !ok {
		return fmt.Errorf("Hash 类型值转换失败")
	}

	// 2. 删除旧键（避免残留字段）
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// 3. 写入所有字段（使用 HSET key field1 value1 field2 value2 ...）
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

	// 4. 设置 TTL（如果有）
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

// writeList 写入 List 类型的键值对
func (r *Replicator) writeList(entry *RDBEntry) error {
	// 1. 提取值
	listVal, ok := entry.Value.(*ListValue)
	if !ok {
		return fmt.Errorf("List 类型值转换失败")
	}

	// 2. 删除旧键
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// 3. 写入所有元素（使用 RPUSH key element1 element2 ...）
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

	// 4. 设置 TTL（如果有）
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

// writeSet 写入 Set 类型的键值对
func (r *Replicator) writeSet(entry *RDBEntry) error {
	// 1. 提取值
	setVal, ok := entry.Value.(*SetValue)
	if !ok {
		return fmt.Errorf("Set 类型值转换失败")
	}

	// 2. 删除旧键
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// 3. 写入所有成员（使用 SADD key member1 member2 ...）
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

	// 4. 设置 TTL（如果有）
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

// writeZSet 写入 ZSet 类型的键值对
func (r *Replicator) writeZSet(entry *RDBEntry) error {
	// 1. 提取值
	zsetVal, ok := entry.Value.(*ZSetValue)
	if !ok {
		return fmt.Errorf("ZSet 类型值转换失败")
	}

	// 2. 删除旧键
	_, _ = r.clusterClient.Do("DEL", entry.Key)

	// 3. 写入所有成员（使用 ZADD key score1 member1 score2 member2 ...）
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

	// 4. 设置 TTL（如果有）
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
