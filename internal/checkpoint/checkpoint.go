package checkpoint

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Checkpoint represents persisted replication state
type Checkpoint struct {
	ReplicationID string         `json:"replication_id"`
	SessionID     string         `json:"session_id"`
	NumFlows      int            `json:"num_flows"`
	FlowLSNs      map[int]uint64 `json:"flow_lsns"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Version       int            `json:"version"`
}

// Manager coordinates checkpoint reads/writes
type Manager struct {
	filePath string
	mu       sync.Mutex
}

// NewManager constructs a checkpoint manager for the provided path
func NewManager(filePath string) *Manager {
	return &Manager{
		filePath: filePath,
	}
}

// Load reads an existing checkpoint if present
func (m *Manager) Load() (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return nil when file is missing
	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return nil, nil
	}

	// Read file
	data, err := ioutil.ReadFile(m.filePath)
	if err != nil {
		return nil, fmt.Errorf("读取检查点文件失败: %w", err)
	}

	// Decode JSON
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("解析检查点 JSON 失败: %w", err)
	}

	return &cp, nil
}

// Save writes checkpoint data atomically
func (m *Manager) Save(cp *Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update metadata
	cp.UpdatedAt = time.Now()
	if cp.Version == 0 {
		cp.Version = 1
	}

	// Encode JSON
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化检查点 JSON 失败: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// Atomic write: temp file + rename
	tmpFile := m.filePath + ".tmp"
	if err := ioutil.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tmpFile, m.filePath); err != nil {
		os.Remove(tmpFile) // cleanup best effort
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	return nil
}

// Delete removes the checkpoint file if it exists
func (m *Manager) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Remove(m.filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除检查点文件失败: %w", err)
	}

	return nil
}
