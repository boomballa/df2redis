package replica

import (
	"fmt"
	"strconv"
)

// buildCommands constructs Redis commands from an RDB entry for pipeline execution
// Returns nil if the entry type is not supported for pipeline batching
func (fw *FlowWriter) buildCommands(entry *RDBEntry) [][]interface{} {
	var commands [][]interface{}

	// Build main command based on type
	var mainCmd []interface{}

	switch entry.Type {
	case RDB_TYPE_STRING:
		// SET key value
		if strVal, ok := entry.Value.(*StringValue); ok && strVal != nil {
			mainCmd = []interface{}{"SET", entry.Key, strVal.Value}
		}

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH_LISTPACK:
		// HSET key field1 value1 ...
		if hashVal, ok := entry.Value.(*HashValue); ok && hashVal != nil {
			if len(hashVal.Fields) > 0 {
				args := make([]interface{}, 0, 2+len(hashVal.Fields)*2)
				args = append(args, "HSET", entry.Key)
				for field, value := range hashVal.Fields {
					args = append(args, field, value)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2:
		// RPUSH key element1 element2 ...
		if listVal, ok := entry.Value.(*ListValue); ok && listVal != nil {
			if len(listVal.Elements) > 0 {
				args := make([]interface{}, 0, 2+len(listVal.Elements))
				args = append(args, "RPUSH", entry.Key)
				for _, item := range listVal.Elements {
					args = append(args, item)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET, RDB_TYPE_SET_LISTPACK:
		// SADD key member1 member2 ...
		if setVal, ok := entry.Value.(*SetValue); ok && setVal != nil {
			if len(setVal.Members) > 0 {
				args := make([]interface{}, 0, 2+len(setVal.Members))
				args = append(args, "SADD", entry.Key)
				for _, member := range setVal.Members {
					args = append(args, member)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_LISTPACK:
		// ZADD key score member ...
		if zsetVal, ok := entry.Value.(*ZSetValue); ok && zsetVal != nil {
			if len(zsetVal.Members) > 0 {
				args := make([]interface{}, 0, 2+len(zsetVal.Members)*2)
				args = append(args, "ZADD", entry.Key)
				for _, zm := range zsetVal.Members {
					args = append(args, fmt.Sprintf("%f", zm.Score), zm.Member)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_STREAM_LISTPACKS, RDB_TYPE_STREAM_LISTPACKS_2, RDB_TYPE_STREAM_LISTPACKS_3:
		return nil // Fallback to sequential for streams

	default:
		return nil
	}

	if mainCmd != nil {
		commands = append(commands, mainCmd)
		// Append expiration if needed
		if entry.ExpireMs > 0 {
			// Calculate remaining TTL
			// entry.ExpireMs is absolute timestamp in ms?
			// RDB usually stores absolute expiry.
			// PEXPIREAT takes absolute timestamp.
			// Wait, replicator.go was using PEXPIRE with (expiry - now).
			// If entry.ExpireMs is timestamp, we should use PEXPIREAT.
			// If entry.ExpireMs is TTL, we use PEXPIRE.
			// RDB parser usually returns ExpiryTime (absolute).
			// replicator.go line 1555: `remainingMs := entry.ExpireMs - getCurrentTimeMillis()`.
			// So `ExpireMs` IS absolute timestamp.
			// Therefore `PEXPIREAT` is correct and safer (idempotent).
			expireCmd := []interface{}{"PEXPIREAT", entry.Key, strconv.FormatInt(entry.ExpireMs, 10)}
			commands = append(commands, expireCmd)
		}
	}

	return commands
}
