package replica

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"df2redis/internal/config"
	"df2redis/internal/redisx"
)

// Replicator 负责与 Dragonfly 建立复制关系
type Replicator struct {
	cfg    *config.Config
	ctx    context.Context
	cancel context.CancelFunc

	// 连接到 Dragonfly
	conn *redisx.Client

	// 复制状态
	state      ReplicaState
	masterInfo MasterInfo
	flows      []FlowInfo

	// 配置
	listeningPort int
	announceIP    string
}

// NewReplicator 创建一个新的复制器
func NewReplicator(cfg *config.Config) *Replicator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Replicator{
		cfg:           cfg,
		ctx:           ctx,
		cancel:        cancel,
		state:         StateDisconnected,
		listeningPort: 6380, // 默认端口
	}
}

// Start 启动复制流程
func (r *Replicator) Start() error {
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

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("🎯 复制器启动成功！")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

// Stop 停止复制
func (r *Replicator) Stop() {
	log.Println("⏸  停止复制器...")
	r.cancel()
	if r.conn != nil {
		r.conn.Close()
	}
	r.state = StateStopped
}

// connect 连接到 Dragonfly 主库
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

	r.conn = client
	log.Printf("✓ 连接成功")

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

// sendPing 发送 PING 命令
func (r *Replicator) sendPing() error {
	resp, err := r.conn.Do("PING")
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
	resp, err := r.conn.Do("REPLCONF", "listening-port", strconv.Itoa(r.listeningPort))
	if err != nil {
		return fmt.Errorf("REPLCONF listening-port 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendIPAddress 发送 REPLCONF ip-address
func (r *Replicator) sendIPAddress() error {
	resp, err := r.conn.Do("REPLCONF", "ip-address", r.announceIP)
	if err != nil {
		return fmt.Errorf("REPLCONF ip-address 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaEOF 发送 REPLCONF capa eof capa psync2
func (r *Replicator) sendCapaEOF() error {
	resp, err := r.conn.Do("REPLCONF", "capa", "eof", "capa", "psync2")
	if err != nil {
		return fmt.Errorf("REPLCONF capa eof psync2 失败: %w", err)
	}

	return r.expectOK(resp)
}

// sendCapaDragonfly 发送 REPLCONF capa dragonfly 并解析响应
func (r *Replicator) sendCapaDragonfly() error {
	resp, err := r.conn.Do("REPLCONF", "capa", "dragonfly")
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

// establishFlows 为每个 shard 建立 FLOW
func (r *Replicator) establishFlows() error {
	r.flows = make([]FlowInfo, r.masterInfo.NumFlows)

	for i := 0; i < r.masterInfo.NumFlows; i++ {
		log.Printf("    • 建立 FLOW-%d...", i)

		// DFLY FLOW 命令格式: DFLY FLOW <master_id> <sync_id> <flow_id>
		resp, err := r.conn.Do("DFLY", "FLOW", r.masterInfo.ReplID, r.masterInfo.SyncID, strconv.Itoa(i))
		if err != nil {
			return fmt.Errorf("建立 FLOW-%d 失败: %w", i, err)
		}

		// DFLY FLOW 返回格式：["FULL", <session_id>] 或可能是 "OK"
		// 我们需要检查响应
		arr, err := redisx.ToStringSlice(resp)
		if err != nil {
			// 可能是简单的 OK
			if err := r.expectOK(resp); err != nil {
				return fmt.Errorf("FLOW-%d 返回错误: %w", i, err)
			}
		} else {
			// 数组响应，第一个元素应该是 "FULL"
			if len(arr) >= 1 {
				log.Printf("      → 同步类型: %s", arr[0])
				if len(arr) >= 2 {
					log.Printf("      → 会话 ID: %s", arr[1][:8]+"...")
				}
			}
		}

		r.flows[i] = FlowInfo{
			FlowID: i,
			State:  "established",
		}

		log.Printf("    ✓ FLOW-%d 已建立", i)
	}

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
