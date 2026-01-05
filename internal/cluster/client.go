package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"df2redis/internal/redisx"
)

// NodeInfo describes a Redis Cluster node
type NodeInfo struct {
	ID     string
	Addr   string
	Flags  []string
	Master string
	Slots  [][2]int // slot ranges [start, end]
}

// IsMaster reports whether this node is a primary
func (n *NodeInfo) IsMaster() bool {
	for _, flag := range n.Flags {
		if flag == "master" {
			return true
		}
	}
	return false
}

// ClusterClient routes commands in Cluster mode with a Standalone fallback
type ClusterClient struct {
	seedAddr string
	password string
	useTLS   bool

	// Topology cache
	mu       sync.RWMutex
	slotMap  map[int]string            // slot -> node addr
	nodes    map[string]*redisx.Client // addr -> client
	topology []*NodeInfo

	// Configuration
	dialTimeout      time.Duration
	isCluster        bool           // true when cluster mode detected
	standaloneClient *redisx.Client // standalone client when not cluster
}

// NewClusterClient builds a cluster-aware client
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

// Connect establishes initial connections and autodetects cluster mode
func (c *ClusterClient) Connect() error {
	// 1. Connect to the seed node
	seedClient, err := c.connectNode(c.seedAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to seed node: %w", err)
	}

	// 2. Execute CLUSTER NODES to detect cluster mode
	resp, err := seedClient.Do("CLUSTER", "NODES")
	if err != nil {
		// Fallback: determine if server runs standalone
		errStr := fmt.Sprintf("%v", err)
		if strings.Contains(errStr, "cluster support disabled") ||
			strings.Contains(errStr, "ERR This instance has cluster support disabled") {
			// Standalone mode detected
			c.mu.Lock()
			c.isCluster = false
			c.standaloneClient = seedClient
			c.mu.Unlock()
			return nil
		}
		return fmt.Errorf("failed to execute CLUSTER NODES: %w", err)
	}

	// 3. Parse topology data
	nodesStr, err := redisx.ToString(resp)
	if err != nil {
		return fmt.Errorf("failed to parse CLUSTER NODES response: %w", err)
	}

	topology, err := parseClusterNodes(nodesStr)
	if err != nil {
		return fmt.Errorf("failed to parse cluster topology: %w", err)
	}

	// 4. Build the slot map
	c.mu.Lock()
	defer c.mu.Unlock()

	c.isCluster = true
	c.topology = topology
	c.nodes[c.seedAddr] = seedClient

	for _, node := range topology {
		if !node.IsMaster() {
			continue
		}

		// Map slots to this node
		for _, slotRange := range node.Slots {
			for slot := slotRange[0]; slot <= slotRange[1]; slot++ {
				c.slotMap[slot] = node.Addr
			}
		}

		// Connect to other primaries as needed
		if node.Addr != c.seedAddr {
			client, err := c.connectNode(node.Addr)
			if err != nil {
				return fmt.Errorf("failed to connect to node %s: %w", node.Addr, err)
			}
			c.nodes[node.Addr] = client
		}
	}

	return nil
}

// connectNode dials a single node with timeout
func (c *ClusterClient) connectNode(addr string) (*redisx.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.dialTimeout)
	defer cancel()

	client, err := redisx.Dial(ctx, redisx.Config{
		Addr:     addr,
		Password: c.password,
		TLS:      c.useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to node: %w", err)
	}

	return client, nil
}

// Do routes and executes a command with automatic MOVED/ASK redirection
func (c *ClusterClient) Do(cmd string, args ...string) (interface{}, error) {
	c.mu.RLock()
	isCluster := c.isCluster
	standaloneClient := c.standaloneClient
	c.mu.RUnlock()

	// Convert args to []interface{}
	interfaceArgs := make([]interface{}, len(args))
	for i, arg := range args {
		interfaceArgs[i] = arg
	}

	// Standalone execution
	if !isCluster {
		if standaloneClient == nil {
			return nil, fmt.Errorf("standalone client not initialized")
		}
		return standaloneClient.Do(cmd, interfaceArgs...)
	}

	// Cluster mode: compute slot and route with retry on MOVED/ASK
	maxRedirects := 5 // Prevent infinite redirect loops
	for attempt := 0; attempt < maxRedirects; attempt++ {
		slot := c.calculateSlot(cmd, args)

		c.mu.RLock()
		addr, ok := c.slotMap[slot]
		client := c.nodes[addr]
		c.mu.RUnlock()

		if !ok || client == nil {
			return nil, fmt.Errorf("no node found for slot %d", slot)
		}

		// Execute command
		resp, err := client.Do(cmd, interfaceArgs...)
		if err == nil {
			return resp, nil
		}

		// Check for MOVED or ASK error
		errStr := fmt.Sprintf("%v", err)

		// MOVED error format: "MOVED <slot> <host>:<port>"
		if strings.HasPrefix(errStr, "MOVED ") {
			parts := strings.Fields(errStr)
			if len(parts) >= 3 {
				newAddr := parts[2]

				// Update slot mapping
				c.mu.Lock()
				c.slotMap[slot] = newAddr

				// Connect to new node if not already connected
				if _, exists := c.nodes[newAddr]; !exists {
					newClient, connErr := c.connectNode(newAddr)
					if connErr != nil {
						c.mu.Unlock()
						return nil, fmt.Errorf("failed to connect to redirected node %s: %w", newAddr, connErr)
					}
					c.nodes[newAddr] = newClient
				}
				c.mu.Unlock()

				// Retry the command (loop continues)
				continue
			}
		}

		// ASK error format: "ASK <slot> <host>:<port>"
		if strings.HasPrefix(errStr, "ASK ") {
			parts := strings.Fields(errStr)
			if len(parts) >= 3 {
				askAddr := parts[2]

				// Connect to ASK target if needed
				c.mu.Lock()
				askClient, exists := c.nodes[askAddr]
				if !exists {
					var connErr error
					askClient, connErr = c.connectNode(askAddr)
					if connErr != nil {
						c.mu.Unlock()
						return nil, fmt.Errorf("failed to connect to ASK node %s: %w", askAddr, connErr)
					}
					c.nodes[askAddr] = askClient
				}
				c.mu.Unlock()

				// Send ASKING command first, then retry
				_, askErr := askClient.Do("ASKING")
				if askErr != nil {
					return nil, fmt.Errorf("ASKING command failed: %w", askErr)
				}

				// Retry on ASK target
				return askClient.Do(cmd, interfaceArgs...)
			}
		}

		// Not a redirect error, return the error
		return resp, err
	}

	return nil, fmt.Errorf("too many redirects (max %d) for command %s", maxRedirects, cmd)
}

// calculateSlot determines the key slot for a command
func (c *ClusterClient) calculateSlot(cmd string, args []string) int {
	if len(args) == 0 {
		return 0
	}

	// Default to the first argument as key
	key := args[0]

	// Special-case commands that accept multiple keys
	switch strings.ToUpper(cmd) {
	case "MSET", "MGET", "DEL":
		// still hash based on the first key
		key = args[0]
	case "HSET", "HGET", "HDEL", "HINCRBY", "HINCRBYFLOAT":
		// hash commands: first argument is key
		key = args[0]
	}

	return CalculateSlot(key)
}

// Close closes all active connections
func (c *ClusterClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isCluster {
		// Standalone
		if c.standaloneClient != nil {
			c.standaloneClient.Close()
			c.standaloneClient = nil
		}
		return nil
	}

	// Cluster mode cleanup
	for _, client := range c.nodes {
		client.Close()
	}

	c.nodes = make(map[string]*redisx.Client)
	c.slotMap = make(map[int]string)

	return nil
}

// GetTopology returns a copy of the cached topology (useful for debugging)
func (c *ClusterClient) GetTopology() []*NodeInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*NodeInfo, len(c.topology))
	copy(result, c.topology)
	return result
}

// ForEachMaster invokes fn for each master node (or standalone instance).
func (c *ClusterClient) ForEachMaster(fn func(addr string, client *redisx.Client) error) error {
	c.mu.RLock()
	isCluster := c.isCluster
	standaloneClient := c.standaloneClient
	seedAddr := c.seedAddr
	topology := make([]*NodeInfo, len(c.topology))
	copy(topology, c.topology)
	nodeClients := make(map[string]*redisx.Client, len(c.nodes))
	for addr, client := range c.nodes {
		nodeClients[addr] = client
	}
	c.mu.RUnlock()

	if !isCluster {
		if standaloneClient == nil {
			return fmt.Errorf("standalone client not initialized")
		}
		return fn(seedAddr, standaloneClient)
	}

	for _, node := range topology {
		if node == nil || !node.IsMaster() {
			continue
		}
		client := nodeClients[node.Addr]
		if client == nil {
			continue
		}
		if err := fn(node.Addr, client); err != nil {
			return err
		}
	}
	return nil
}

// IsCluster returns whether the target is a Redis Cluster or standalone instance
func (c *ClusterClient) IsCluster() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isCluster
}

// GetStandaloneClient returns the standalone client (nil if in cluster mode)
func (c *ClusterClient) GetStandaloneClient() *redisx.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.standaloneClient
}

// GetNodeForSlot returns the node address for a given slot
func (c *ClusterClient) GetNodeForSlot(slot int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	addr, ok := c.slotMap[slot]
	return addr, ok
}

// PipelineForNode executes a batch of commands on a specific node using pipeline.
// Commands can belong to different slots as long as they route to the same node.
// This significantly improves write performance in cluster mode by reducing network round trips.
//
// Returns results array corresponding to each command, or error if pipeline fails.
func (c *ClusterClient) PipelineForNode(nodeAddr string, cmds [][]interface{}) ([]interface{}, error) {
	if len(cmds) == 0 {
		return []interface{}{}, nil
	}

	c.mu.RLock()
	client, exists := c.nodes[nodeAddr]
	c.mu.RUnlock()

	if !exists || client == nil {
		return nil, fmt.Errorf("node %s not found", nodeAddr)
	}

	// Execute pipeline on the target node
	return client.Pipeline(cmds)
}

// PipelineForSlot executes a batch of commands for a specific slot using pipeline.
// All commands must belong to the same slot (same key or slot hash tag).
// This significantly improves write performance in cluster mode by reducing network round trips.
//
// Returns results array corresponding to each command, or error if pipeline fails.
func (c *ClusterClient) PipelineForSlot(slot int, cmds [][]interface{}) ([]interface{}, error) {
	if len(cmds) == 0 {
		return []interface{}{}, nil
	}

	c.mu.RLock()
	targetAddr, exists := c.slotMap[slot]
	c.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("slot %d not found in topology", slot)
	}

	c.mu.RLock()
	client, exists := c.nodes[targetAddr]
	c.mu.RUnlock()

	if !exists || client == nil {
		return nil, fmt.Errorf("node %s not found for slot %d", targetAddr, slot)
	}

	// Execute pipeline on the target node
	return client.Pipeline(cmds)
}
