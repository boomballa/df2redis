package replica

// RDB opcodes (per Redis RDB specification)
const (
	// Expiration encodings
	RDB_OPCODE_EXPIRETIME_MS = 0xFC // expire time in milliseconds (8 bytes)
	RDB_OPCODE_EXPIRETIME    = 0xFD // expire time in seconds (4 bytes)

	// Database selection
	RDB_OPCODE_SELECTDB = 0xFE // SELECT <db>

	// RDB terminator
	RDB_OPCODE_EOF = 0xFF // EOF + 8-byte checksum

	// Dragonfly custom opcodes
	RDB_OPCODE_JOURNAL_BLOB               = 0xD2 // Inline journal entry during RDB streaming
	RDB_OPCODE_JOURNAL_OFFSET             = 0xD3 // JOURNAL_OFFSET marker
	RDB_OPCODE_FULLSYNC_END               = 0xC8 // FULLSYNC_END marker
	RDB_OPCODE_COMPRESSED_ZSTD_BLOB_START = 201  // 0xC9 - ZSTD compressed blob start
	RDB_OPCODE_COMPRESSED_LZ4_BLOB_START  = 202  // 0xCA - LZ4 compressed blob start
	RDB_OPCODE_COMPRESSED_BLOB_END        = 203  // 0xCB - Compressed blob end

	// AUX field
	RDB_OPCODE_AUX = 0xFA // AUX field
)

// RDB data types (per Redis RDB spec)
const (
	RDB_TYPE_STRING = 0 // string
	RDB_TYPE_LIST   = 1 // legacy list
	RDB_TYPE_SET    = 2 // set
	RDB_TYPE_ZSET   = 3 // legacy zset
	RDB_TYPE_HASH   = 4 // hash

	// Zipmap (deprecated)
	RDB_TYPE_HASH_ZIPMAP = 9

	// Ziplist encodings (compressed formats)
	RDB_TYPE_LIST_ZIPLIST = 10
	RDB_TYPE_SET_INTSET   = 11
	RDB_TYPE_ZSET_ZIPLIST = 12
	RDB_TYPE_HASH_ZIPLIST = 13

	// Quicklist encodings
	RDB_TYPE_LIST_QUICKLIST   = 14
	RDB_TYPE_LIST_QUICKLIST_2 = 17 // Redis 7.0+ / Dragonfly

	// Stream encodings
	RDB_TYPE_STREAM_LISTPACKS   = 15
	RDB_TYPE_STREAM_LISTPACKS_2 = 19
	RDB_TYPE_STREAM_LISTPACKS_3 = 21

	// Dragonfly custom types (30-35)
	RDB_TYPE_JSON             = 30
	RDB_TYPE_HASH_WITH_EXPIRY = 31 // Hash with per-field TTL
	RDB_TYPE_SET_WITH_EXPIRY  = 32 // Set with per-member TTL
	RDB_TYPE_SBF              = 33 // Scalable Bloom Filter

	// ZSet 2.0
	RDB_TYPE_ZSET_2 = 5

	// Module types
	RDB_TYPE_MODULE   = 6
	RDB_TYPE_MODULE_2 = 7
)

// RDB string encodings
const (
	RDB_ENC_INT8  = 0 // 8-bit integer
	RDB_ENC_INT16 = 1 // 16-bit integer
	RDB_ENC_INT32 = 2 // 32-bit integer
	RDB_ENC_LZF   = 3 // LZF-compressed string
)

// Quicklist container types
const (
	QUICKLIST_NODE_CONTAINER_PLAIN  = 1 // plain container
	QUICKLIST_NODE_CONTAINER_PACKED = 2 // packed/listpack container
)

// RDBEntry represents a decoded key/value pair
type RDBEntry struct {
	Key      string      // key name
	Type     byte        // RDB type
	Value    interface{} // decoded value (type-dependent)
	ExpireMs int64       // absolute expiration timestamp in ms; 0 means no TTL
	DbIndex  int         // database index
}

// StringValue wraps a plain string
type StringValue struct {
	Value string
}

// HashValue contains hash fields
type HashValue struct {
	Fields map[string]string
}

// ListValue stores list elements
type ListValue struct {
	Elements []string
}

// SetValue stores unordered members
type SetValue struct {
	Members []string
}

// ZSetValue holds sorted set members
type ZSetValue struct {
	Members []ZSetMember
}

// ZSetMember is a sorted-set entry
type ZSetMember struct {
	Member string
	Score  float64
}

// StreamValue stores stream messages
type StreamValue struct {
	Messages []StreamMessage
	Length   uint64 // Total number of messages
	LastID   string // Last generated ID (ms-seq format)
}

// StreamMessage represents a single stream entry
type StreamMessage struct {
	ID     string            // Message ID (e.g., "1640995200000-0")
	Fields map[string]string // Field-value pairs
}

// IsExpired evaluates the TTL
func (e *RDBEntry) IsExpired() bool {
	if e.ExpireMs == 0 {
		return false
	}
	// Compare with current time
	return e.ExpireMs < getCurrentTimeMillis()
}
