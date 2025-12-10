package cluster

import (
	"fmt"
	"strconv"
	"strings"
)

// parseClusterNodes parses the CLUSTER NODES output.
// Example:
// 07c37dfeb235213a872192d90877d0cd55635b91 127.0.0.1:30004@31004 slave e7d1eecce10fd6bb5eb35b9f99a514335d9ba9ca 0 1426238317239 4 connected
// 67ed2db8d677e59ec4a4cefb06858cf2a1a89fa1 127.0.0.1:30002@31002 master - 0 1426238316232 2 connected 5461-10922
func parseClusterNodes(output string) ([]*NodeInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var nodes []*NodeInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			return nil, fmt.Errorf("invalid CLUSTER NODES line: %s", line)
		}

		node := &NodeInfo{
			ID:     fields[0],
			Addr:   normalizeAddr(fields[1]),
			Flags:  strings.Split(fields[2], ","),
			Master: fields[3],
		}

		// Parse slot ranges starting from field #8
		for i := 8; i < len(fields); i++ {
			slotField := fields[i]

			// Ignore importing/migrating markers
			if strings.HasPrefix(slotField, "[") {
				continue
			}

			slotRange, err := parseSlotRange(slotField)
			if err != nil {
				return nil, fmt.Errorf("failed to parse slot range '%s': %w", slotField, err)
			}

			node.Slots = append(node.Slots, slotRange)
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// normalizeAddr strips the @bus-port suffix.
// Example: 127.0.0.1:30002@31002 -> 127.0.0.1:30002
func normalizeAddr(addr string) string {
	if idx := strings.Index(addr, "@"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// parseSlotRange parses a slot specification.
// Supported formats:
//   - single slot: "5461"
//   - slot range: "5461-10922"
func parseSlotRange(s string) ([2]int, error) {
	parts := strings.Split(s, "-")

	if len(parts) == 1 {
		// Single slot
		slot, err := strconv.Atoi(parts[0])
		if err != nil {
			return [2]int{}, err
		}
		return [2]int{slot, slot}, nil
	}

	if len(parts) == 2 {
		// Slot range
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return [2]int{}, err
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return [2]int{}, err
		}
		return [2]int{start, end}, nil
	}

	return [2]int{}, fmt.Errorf("invalid slot range format: %s", s)
}
