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

// Checkpoint 表示复制检查点
type Checkpoint struct {
	ReplicationID string         `json:"replication_id"`
	SessionID     string         `json:"session_id"`
	NumFlows      int            `json:"num_flows"`
	FlowLSNs      map[int]uint64 `json:"flow_lsns"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Version       int            `json:"version"`
}

// Manager 管理检查点的读写
type Manager struct {
	filePath string
	mu       sync.Mutex
}

// NewManager 创建检查点管理器
func NewManager(filePath string) *Manager {
	return &Manager{
		filePath: filePath,
	}
}

// Load 加载检查点
func (m *Manager) Load() (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查文件是否存在
	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return nil, nil // 文件不存在，返回 nil
	}

	// 读取文件
	data, err := ioutil.ReadFile(m.filePath)
	if err != nil {
		return nil, fmt.Errorf("读取检查点文件失败: %w", err)
	}

	// 解析 JSON
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("解析检查点 JSON 失败: %w", err)
	}

	return &cp, nil
}

// Save 保存检查点
func (m *Manager) Save(cp *Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 设置更新时间和版本
	cp.UpdatedAt = time.Now()
	if cp.Version == 0 {
		cp.Version = 1
	}

	// 序列化为 JSON
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化检查点 JSON 失败: %w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 原子写入：先写临时文件，再重命名
	tmpFile := m.filePath + ".tmp"
	if err := ioutil.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tmpFile, m.filePath); err != nil {
		os.Remove(tmpFile) // 清理临时文件
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	return nil
}

// Delete 删除检查点文件
func (m *Manager) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Remove(m.filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除检查点文件失败: %w", err)
	}

	return nil
}
