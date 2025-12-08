package replica

import (
	"fmt"
)

// DflyVersion identifies the Dragonfly replication protocol version
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

// ReplicaState enumerates replication states
type ReplicaState int

const (
	StateDisconnected ReplicaState = iota // not connected
	StateConnecting                       // establishing connection
	StateHandshaking                      // performing handshake
	StatePreparation                      // preparing flow connections
	StateFullSync                         // full sync in progress
	StateStableSync                       // stable/incremental sync
	StateStopped                          // replication stopped
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

// MasterInfo describes the remote Dragonfly master
type MasterInfo struct {
	Version  DflyVersion // Dragonfly version
	NumFlows int         // number of shards/flows
	ReplID   string      // replication ID (master_id)
	SyncID   string      // sync session ID (e.g. "SYNC11")
	Offset   int64       // replication offset
}

// FlowInfo carries per-flow metadata
type FlowInfo struct {
	FlowID   int    // flow ID (aligned with shard ID)
	State    string // textual flow state
	EOFToken string // EOF marker from DFLY FLOW
	SyncType string // "FULL" or "PARTIAL"
}
