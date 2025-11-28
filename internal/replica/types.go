package replica

import (
	"fmt"
)

// DflyVersion 表示 Dragonfly 协议版本
type DflyVersion int

const (
	DflyVersionUnknown DflyVersion = 0
	DflyVersion1       DflyVersion = 1
	DflyVersion2       DflyVersion = 2
	DflyVersion3       DflyVersion = 3
	DflyVersion4       DflyVersion = 4
)

func (v DflyVersion) String() string {
	return fmt.Sprintf("VER%d", v)
}

// ReplicaState 表示复制状态
type ReplicaState int

const (
	StateDisconnected ReplicaState = iota // 未连接
	StateConnecting                       // 连接中
	StateHandshaking                      // 握手中
	StatePreparation                      // 准备阶段
	StateFullSync                         // 全量同步
	StateStableSync                       // 增量同步
	StateStopped                          // 已停止
)

func (s ReplicaState) String() string {
	switch s {
	case StateDisconnected:
		return "DISCONNECTED"
	case StateConnecting:
		return "CONNECTING"
	case StateHandshaking:
		return "HANDSHAKING"
	case StatePreparation:
		return "PREPARATION"
	case StateFullSync:
		return "FULL_SYNC"
	case StateStableSync:
		return "STABLE_SYNC"
	case StateStopped:
		return "STOPPED"
	default:
		return "UNKNOWN"
	}
}

// MasterInfo 保存主库信息
type MasterInfo struct {
	Version  DflyVersion // Dragonfly 版本
	NumFlows int         // Shard 数量
	ReplID   string      // 复制 ID (master_id)
	SyncID   string      // 同步会话 ID (如 "SYNC11")
	Offset   int64       // 复制偏移量
}

// FlowInfo 保存单个 Flow 的信息
type FlowInfo struct {
	FlowID int    // Flow ID（对应 shard ID）
	State  string // Flow 状态
}
