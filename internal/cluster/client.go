package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"df2redis/internal/redisx"
)

// NodeInfo 表示 Redis Cluster 节点信息
type NodeInfo struct {
	ID      string
	Addr    string
	Flags   []string
	Master  string
	Slots   [][2]int // slot 范围 [start, end]
}

// IsMaster 判断节点是否为 Master
func (n *NodeInfo) IsMaster() bool {
	for _, flag := range n.Flags {
		if flag == "master" {
			return true
		}
	}
	return false
}

// ClusterClient 表示 Redis Cluster 客户端（兼容单机模式）
type ClusterClient struct {
	seedAddr  string
	password  string
	useTLS    bool

	// 拓扑信息
	mu       sync.RWMutex
	slotMap  map[int]string   // slot -> node addr
	nodes    map[string]*redisx.Client // addr -> client
	topology []*NodeInfo

	// 配置
	dialTimeout   time.Duration
	isCluster     bool             // 是否为 Cluster 模式
	standaloneClient *redisx.Client // 单机模式客户端
}

// NewClusterClient 创建 Redis Cluster 客户端
func NewClusterClient(seedAddr, password string, useTLS bool) *ClusterClient {
	return &ClusterClient{
		seedAddr:    seedAddr,
		password:    password,
		useTLS:      useTLS,
		slotMap:     make(map[int]string),
		nodes:       make(map[string]*redisx.Client),
		dialTimeout: 5 * time.Second,
	}
}

// Connect 连接到 Redis 并自动检测是否为 Cluster 模式
func (c *ClusterClient) Connect() error {
	// 1. 连接到 seed 节点
	seedClient, err := c.connectNode(c.seedAddr)
	if err != nil {
		return fmt.Errorf("连接 seed 节点失败: %w", err)
	}

	// 2. 尝试执行 CLUSTER NODES 检测是否为 Cluster 模式
	resp, err := seedClient.Do("CLUSTER", "NODES")
	if err != nil {
		// 如果失败，尝试判断是否为单机模式
		errStr := fmt.Sprintf("%v", err)
		if strings.Contains(errStr, "cluster support disabled") ||
		   strings.Contains(errStr, "ERR This instance has cluster support disabled") {
			// 单机模式
			c.mu.Lock()
			c.isCluster = false
			c.standaloneClient = seedClient
			c.mu.Unlock()
			return nil
		}
		return fmt.Errorf("执行 CLUSTER NODES 失败: %w", err)
	}

	// 3. Cluster 模式：解析拓扑信息
	nodesStr, err := redisx.ToString(resp)
	if err != nil {
		return fmt.Errorf("解析 CLUSTER NODES 响应失败: %w", err)
	}

	topology, err := parseClusterNodes(nodesStr)
	if err != nil {
		return fmt.Errorf("解析拓扑信息失败: %w", err)
	}

	// 4. 构建 slot 映射表
	c.mu.Lock()
	defer c.mu.Unlock()

	c.isCluster = true
	c.topology = topology
	c.nodes[c.seedAddr] = seedClient

	for _, node := range topology {
		if !node.IsMaster() {
			continue
		}

		// 为每个 slot 范围建立映射
		for _, slotRange := range node.Slots {
			for slot := slotRange[0]; slot <= slotRange[1]; slot++ {
				c.slotMap[slot] = node.Addr
			}
		}

		// 如果节点不是 seed，建立连接
		if node.Addr != c.seedAddr {
			client, err := c.connectNode(node.Addr)
			if err != nil {
				return fmt.Errorf("连接节点 %s 失败: %w", node.Addr, err)
			}
			c.nodes[node.Addr] = client
		}
	}

	return nil
}

// connectNode 连接到指定节点
func (c *ClusterClient) connectNode(addr string) (*redisx.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.dialTimeout)
	defer cancel()

	client, err := redisx.Dial(ctx, redisx.Config{
		Addr:     addr,
		Password: c.password,
		TLS:      c.useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("连接节点失败: %w", err)
	}

	return client, nil
}

// Do 执行命令（自动路由到正确的节点或单机）
func (c *ClusterClient) Do(cmd string, args ...string) (interface{}, error) {
	c.mu.RLock()
	isCluster := c.isCluster
	standaloneClient := c.standaloneClient
	c.mu.RUnlock()

	// 将 string 参数转换为 interface{} 参数
	interfaceArgs := make([]interface{}, len(args))
	for i, arg := range args {
		interfaceArgs[i] = arg
	}

	// 单机模式：直接执行
	if !isCluster {
		if standaloneClient == nil {
			return nil, fmt.Errorf("单机客户端未初始化")
		}
		return standaloneClient.Do(cmd, interfaceArgs...)
	}

	// Cluster 模式：计算 slot 并路由
	slot := c.calculateSlot(cmd, args)

	c.mu.RLock()
	addr, ok := c.slotMap[slot]
	client := c.nodes[addr]
	c.mu.RUnlock()

	if !ok || client == nil {
		return nil, fmt.Errorf("未找到 slot %d 对应的节点", slot)
	}

	// 执行命令
	return client.Do(cmd, interfaceArgs...)
}

// calculateSlot 计算命令的 slot
func (c *ClusterClient) calculateSlot(cmd string, args []string) int {
	if len(args) == 0 {
		return 0
	}

	// 获取第一个 key（大多数命令的第一个参数是 key）
	key := args[0]

	// 特殊命令处理
	switch strings.ToUpper(cmd) {
	case "MSET", "MGET", "DEL":
		// 多 key 命令，使用第一个 key
		key = args[0]
	case "HSET", "HGET", "HDEL", "HINCRBY", "HINCRBYFLOAT":
		// Hash 命令，第一个参数是 key
		key = args[0]
	}

	return CalculateSlot(key)
}

// Close 关闭所有连接
func (c *ClusterClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isCluster {
		// 单机模式
		if c.standaloneClient != nil {
			c.standaloneClient.Close()
			c.standaloneClient = nil
		}
		return nil
	}

	// Cluster 模式
	for _, client := range c.nodes {
		client.Close()
	}

	c.nodes = make(map[string]*redisx.Client)
	c.slotMap = make(map[int]string)

	return nil
}

// GetTopology 返回拓扑信息（用于调试）
func (c *ClusterClient) GetTopology() []*NodeInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*NodeInfo, len(c.topology))
	copy(result, c.topology)
	return result
}
