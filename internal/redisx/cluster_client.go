package redisx

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
)

// ClusterClient manages corrections to a Redis Cluster.
type ClusterClient struct {
	seeds    []string
	password string

	// Topology
	mu      sync.RWMutex
	slots   [16384]string      // Mapping slot -> master address
	clients map[string]*Client // Mapping address -> Client connection
	closed  bool
}

// DialCluster connects to a Redis Cluster using the provided seeds.
func DialCluster(ctx context.Context, seeds []string, password string) (*ClusterClient, error) {
	if len(seeds) == 0 {
		return nil, errors.New("redisx: no cluster seeds provided")
	}

	cc := &ClusterClient{
		seeds:    seeds,
		password: password,
		clients:  make(map[string]*Client),
	}

	// Initial topology discovery
	if err := cc.refreshSlots(ctx); err != nil {
		cc.Close()
		return nil, fmt.Errorf("failed to discover cluster topology: %w", err)
	}

	return cc, nil
}

// DialStandalone connects to a single Redis instance but returns a ClusterClient adapter.
// This allows the replicator to treat standalone and cluster targets uniformly.
func DialStandalone(ctx context.Context, addr string, password string) (*ClusterClient, error) {
	if addr == "" {
		return nil, errors.New("redisx: addr is empty")
	}

	cc := &ClusterClient{
		seeds:    []string{addr},
		password: password,
		clients:  make(map[string]*Client),
	}

	// Connect to the single node
	client, err := Dial(ctx, Config{Addr: addr, Password: password})
	if err != nil {
		return nil, err
	}
	cc.clients[addr] = client

	// Map all slots to this single node
	cc.mu.Lock()
	for i := 0; i < 16384; i++ {
		cc.slots[i] = addr
	}
	cc.mu.Unlock()

	return cc, nil
}

// Close closes all connections in the pool.
func (cc *ClusterClient) Close() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.closed {
		return nil
	}
	cc.closed = true

	var firstErr error
	for addr, client := range cc.clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(cc.clients, addr)
	}
	return firstErr
}

// Check if client is closed
func (cc *ClusterClient) isClosed() bool {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.closed
}

// MasterAddr returns the address of the master node for a given slot.
func (cc *ClusterClient) MasterAddr(slot uint16) string {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	if slot >= 16384 {
		return ""
	}
	return cc.slots[slot]
}

// GetNodeClient returns a client for the specific node address.
// If connection doesn't exist, it creates one.
func (cc *ClusterClient) GetNodeClient(addr string) (*Client, error) {
	if addr == "" {
		return nil, errors.New("redisx: empty address")
	}

	cc.mu.RLock()
	client, ok := cc.clients[addr]
	if ok && client.closed.Load() == 0 {
		cc.mu.RUnlock()
		return client, nil
	}
	cc.mu.RUnlock()

	// Need to create new connection
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Double check
	if client, ok := cc.clients[addr]; ok && client.closed.Load() == 0 {
		return client, nil
	}

	if cc.closed {
		return nil, errors.New("redisx: cluster client closed")
	}

	// Dial new connection
	cfg := Config{
		Addr:     addr,
		Password: cc.password,
	}
	newClient, err := Dial(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	cc.clients[addr] = newClient
	return newClient, nil
}

// refreshSlots updates the slot mapping by running CLUSTER SLOTS on random seed/known node.
func (cc *ClusterClient) refreshSlots(ctx context.Context) error {
	// Try known clients first, then seeds
	var addrs []string

	cc.mu.RLock()
	for addr := range cc.clients {
		addrs = append(addrs, addr)
	}
	cc.mu.RUnlock()

	// Shuffle addrs
	rand.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })

	// Append seeds
	addrs = append(addrs, cc.seeds...)

	var lastErr error
	for _, addr := range addrs {
		// Try to connect/use client to get slots
		nodes, err := cc.fetchSlots(ctx, addr)
		if err == nil {
			// Update topology
			cc.updateTopology(nodes)
			log.Printf("[Cluster] Topology refreshed from %s. Found %d master nodes.", addr, len(cc.clients))
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("all nodes failed topology refresh: %v", lastErr)
}

func (cc *ClusterClient) fetchSlots(ctx context.Context, addr string) ([]clusterSlotNode, error) {
	// Create temporary client if needed, or use existing
	var client *Client
	var err error

	// Check if we have an active client for this addr
	cc.mu.RLock()
	if existing, ok := cc.clients[addr]; ok && existing.closed.Load() == 0 {
		client = existing
	}
	cc.mu.RUnlock()

	if client == nil {
		client, err = Dial(ctx, Config{Addr: addr, Password: cc.password})
		if err != nil {
			return nil, err
		}
		defer client.Close() // Close temp client
	}

	// CLUSTER SLOTS
	reply, err := client.Do("CLUSTER", "SLOTS")
	if err != nil {
		return nil, err
	}

	return parseClusterSlots(reply)
}

type clusterSlotNode struct {
	start, end uint16
	masterAddr string
}

func parseClusterSlots(reply interface{}) ([]clusterSlotNode, error) {
	// Reply is array of arrays
	// [[start, end, [ip, port, id], ...], ...]

	infos, ok := reply.([]interface{})
	if !ok {
		return nil, fmt.Errorf("CLUSTER SLOTS reply not array")
	}

	var results []clusterSlotNode

	for _, info := range infos {
		slotInfo, ok := info.([]interface{})
		if !ok || len(slotInfo) < 3 {
			continue
		}

		start, _ := ToInt64(slotInfo[0])
		end, _ := ToInt64(slotInfo[1])

		masterInfo, ok := slotInfo[2].([]interface{})
		if !ok || len(masterInfo) < 2 {
			continue
		}

		ip, _ := ToString(masterInfo[0])
		port, _ := ToInt64(masterInfo[1])

		// If IP is empty (some Redis versions), handle it? Usually it returns valid IP.
		// If Docker, might be internal IP.

		addr := net.JoinHostPort(ip, strconv.FormatInt(port, 10))
		results = append(results, clusterSlotNode{
			start:      uint16(start),
			end:        uint16(end),
			masterAddr: addr,
		})
	}
	return results, nil
}

func (cc *ClusterClient) updateTopology(nodes []clusterSlotNode) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	for _, node := range nodes {
		for s := node.start; s <= node.end; s++ {
			cc.slots[s] = node.masterAddr
		}
	}
}

// MasterCount returns the number of unique master nodes.
func (cc *ClusterClient) MasterCount() int {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return len(cc.clients)
}

// Do executes a command on the appropriate node.
// It assumes the first argument in args is the Key.
// If args is empty, it executes on a random node.
func (cc *ClusterClient) Do(cmd string, args ...interface{}) (interface{}, error) {
	var client *Client
	var err error

	if len(args) > 0 {
		// Assume first arg is key
		key := fmt.Sprint(args[0])
		slot := Slot(key)
		addr := cc.MasterAddr(slot)
		if addr == "" {
			return nil, fmt.Errorf("no master found for slot %d (key %s)", slot, key)
		}
		client, err = cc.GetNodeClient(addr)
	} else {
		// Random node
		client, err = cc.getRandomClient()
	}

	if err != nil {
		return nil, err
	}

	// Retry on MOVED?
	// For now, simple execution.
	// Improvements: Handle MOVED/ASK recursion.
	return client.Do(cmd, args...)
}

func (cc *ClusterClient) getRandomClient() (*Client, error) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()

	for _, client := range cc.clients {
		if client.closed.Load() == 0 {
			return client, nil
		}
	}
	return nil, errors.New("no available clients")
}

// ForEachMaster executes a function for every connected master node.
func (cc *ClusterClient) ForEachMaster(fn func(client *Client) error) error {
	cc.mu.RLock()
	// Copy clients to avoid holding lock during callback
	clients := make([]*Client, 0, len(cc.clients))
	for _, c := range cc.clients {
		clients = append(clients, c)
	}
	cc.mu.RUnlock()

	for _, c := range clients {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}
