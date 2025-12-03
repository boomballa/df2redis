package replica

// RDB Opcodes - 来自 Redis RDB 规范
const (
	// 过期时间
	RDB_OPCODE_EXPIRETIME_MS = 0xFC // 毫秒过期时间 (8 字节)
	RDB_OPCODE_EXPIRETIME    = 0xFD // 秒过期时间 (4 字节)

	// 数据库选择
	RDB_OPCODE_SELECTDB = 0xFE // SELECT db

	// RDB 结束
	RDB_OPCODE_EOF = 0xFF // EOF + 8 字节 checksum

	// Dragonfly 特有 opcodes
	RDB_OPCODE_FULLSYNC_END = 0xC8 // Dragonfly FULLSYNC_END 标记

	// AUX 字段
	RDB_OPCODE_AUX = 0xFA // AUX field
)

// RDB 数据类型 - 来自 Redis RDB 规范
const (
	RDB_TYPE_STRING = 0 // String 类型
	RDB_TYPE_LIST   = 1 // List 类型 (旧)
	RDB_TYPE_SET    = 2 // Set 类型
	RDB_TYPE_ZSET   = 3 // ZSet 类型 (旧)
	RDB_TYPE_HASH   = 4 // Hash 类型

	// Zipmap (已废弃)
	RDB_TYPE_HASH_ZIPMAP = 9

	// Ziplist 编码
	RDB_TYPE_LIST_ZIPLIST = 10
	RDB_TYPE_SET_INTSET   = 11
	RDB_TYPE_ZSET_ZIPLIST = 12
	RDB_TYPE_HASH_ZIPLIST = 13

	// Quicklist 编码
	RDB_TYPE_LIST_QUICKLIST   = 14
	RDB_TYPE_LIST_QUICKLIST_2 = 17 // Redis 7.0+ Quicklist 2.0

	// Stream 类型
	RDB_TYPE_STREAM_LISTPACKS   = 15
	RDB_TYPE_STREAM_LISTPACKS_2 = 19
	RDB_TYPE_STREAM_LISTPACKS_3 = 21

	// Listpack 编码 (Redis 7.0+)
	RDB_TYPE_HASH_ZIPLIST_EX = 16 // Hash ziplist with metadata
	RDB_TYPE_ZSET_LISTPACK   = 18 // ZSet listpack
	RDB_TYPE_HASH_LISTPACK   = 20 // Hash listpack
	RDB_TYPE_SET_LISTPACK    = 22 // Set listpack

	// ZSet 2.0
	RDB_TYPE_ZSET_2 = 5

	// Module 类型
	RDB_TYPE_MODULE   = 6
	RDB_TYPE_MODULE_2 = 7
)

// RDB 字符串编码
const (
	RDB_ENC_INT8 = 0 // 8 位整数
	RDB_ENC_INT16 = 1 // 16 位整数
	RDB_ENC_INT32 = 2 // 32 位整数
	RDB_ENC_LZF   = 3 // LZF 压缩字符串
)

// Quicklist 容器类型
const (
	QUICKLIST_NODE_CONTAINER_PLAIN  = 1 // Plain 容器
	QUICKLIST_NODE_CONTAINER_PACKED = 2 // Packed 容器 (Listpack)
)

// RDBEntry 表示解析出的一个键值对
type RDBEntry struct {
	Key      string      // 键名
	Type     byte        // RDB 类型
	Value    interface{} // 值（具体类型取决于 Type）
	ExpireMs int64       // 过期时间（绝对时间戳，毫秒），0 表示无过期
	DbIndex  int         // 数据库索引
}

// StringValue 表示 String 类型的值
type StringValue struct {
	Value string
}

// HashValue 表示 Hash 类型的值
type HashValue struct {
	Fields map[string]string
}

// ListValue 表示 List 类型的值
type ListValue struct {
	Elements []string
}

// SetValue 表示 Set 类型的值
type SetValue struct {
	Members []string
}

// ZSetValue 表示 ZSet 类型的值
type ZSetValue struct {
	Members []ZSetMember
}

// ZSetMember 表示 ZSet 的一个成员
type ZSetMember struct {
	Member string
	Score  float64
}

// IsExpired 检查键是否已过期
func (e *RDBEntry) IsExpired() bool {
	if e.ExpireMs == 0 {
		return false
	}
	// 使用 time 包获取当前时间
	return e.ExpireMs < getCurrentTimeMillis()
}
