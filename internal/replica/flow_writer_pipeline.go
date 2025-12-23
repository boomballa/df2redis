package replica

import (
	"fmt"
	"strconv"
)

// buildCommand constructs a Redis command from an RDB entry for pipeline execution
// Returns nil if the entry type is not supported for pipeline batching
func (fw *FlowWriter) buildCommand(entry *RDBEntry) []interface{} {
	// Handle expiration
	var expireCmd []interface{}
	if entry.ExpireMs > 0 {
		expireCmd = []interface{}{"PEXPIREAT", entry.Key, strconv.FormatInt(entry.ExpireMs, 10)}
	}

	// Build main command based on type
	var mainCmd []interface{}

	switch entry.Type {
	case RDB_TYPE_STRING:
		// SET key value
		if strVal, ok := entry.Value.(string); ok {
			mainCmd = []interface{}{"SET", entry.Key, strVal}
		} else if bytesVal, ok := entry.Value.([]byte); ok {
			mainCmd = []interface{}{"SET", entry.Key, string(bytesVal)}
		}

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH_LISTPACK:
		// HSET key field1 value1 field2 value2 ...
		if hashMap, ok := entry.Value.(map[string]string); ok {
			if len(hashMap) > 0 {
				args := make([]interface{}, 0, 2+len(hashMap)*2)
				args = append(args, "HSET", entry.Key)
				for field, value := range hashMap {
					args = append(args, field, value)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2:
		// RPUSH key element1 element2 ...
		if listItems, ok := entry.Value.([]string); ok {
			if len(listItems) > 0 {
				args := make([]interface{}, 0, 2+len(listItems))
				args = append(args, "RPUSH", entry.Key)
				for _, item := range listItems {
					args = append(args, item)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET, RDB_TYPE_SET_LISTPACK:
		// SADD key member1 member2 ...
		if setMembers, ok := entry.Value.([]string); ok {
			if len(setMembers) > 0 {
				args := make([]interface{}, 0, 2+len(setMembers))
				args = append(args, "SADD", entry.Key)
				for _, member := range setMembers {
					args = append(args, member)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_LISTPACK:
		// ZADD key score1 member1 score2 member2 ...
		if zsetMembers, ok := entry.Value.([]ZSetMember); ok {
			if len(zsetMembers) > 0 {
				args := make([]interface{}, 0, 2+len(zsetMembers)*2)
				args = append(args, "ZADD", entry.Key)
				for _, zm := range zsetMembers {
					args = append(args, fmt.Sprintf("%f", zm.Score), zm.Member)
				}
				mainCmd = args
			}
		}

	case RDB_TYPE_STREAM_LISTPACKS, RDB_TYPE_STREAM_LISTPACKS_2, RDB_TYPE_STREAM_LISTPACKS_3:
		// XADD key ID field1 value1 field2 value2 ...
		// For streams, we need to process each entry
		// This is complex, so we skip pipeline for now and fallback to sequential
		return nil

	default:
		// Unsupported type for pipeline, return nil to use fallback
		return nil
	}

	// For now, only return main command
	// TODO: support expiration by returning multiple commands or using a transaction
	_ = expireCmd
	return mainCmd
}
