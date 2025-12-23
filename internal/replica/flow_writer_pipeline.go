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
		// CRITICAL FIX: entry.Value is *StringValue, not string!
		if strVal, ok := entry.Value.(*StringValue); ok && strVal != nil {
			mainCmd = []interface{}{"SET", entry.Key, strVal.Value}
		}

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH_LISTPACK:
		// HSET key field1 value1 field2 value2 ...
		// CRITICAL FIX: entry.Value is *HashValue, not map[string]string!
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
		// CRITICAL FIX: entry.Value is *ListValue, not []string!
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
		// CRITICAL FIX: entry.Value is *SetValue, not []string!
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
		// ZADD key score1 member1 score2 member2 ...
		// CRITICAL FIX: entry.Value is *ZSetValue, not []ZSetMember!
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
