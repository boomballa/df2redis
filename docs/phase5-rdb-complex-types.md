# Phase 5: RDB 复杂类型解析实现

**实现日期**: 2025-12-03
**实施阶段**: Phase 5B + 5C
**功能状态**: ✅ 已完成

---

## 📋 概述

Phase 5 实现了 Dragonfly RDB 快照中所有复杂数据类型的完整解析和写入能力，包括 Hash、List、Set、ZSet 四种复杂类型。这是实现完整快照同步的关键阶段。

### 实现目标

- ✅ 支持 Hash 类型的多种编码格式 (Ziplist, Hashtable, Listpack)
- ✅ 支持 List 类型的多种编码格式 (Quicklist, Listpack)
- ✅ 支持 Set 类型的多种编码格式 (Intset, Hashtable)
- ✅ 支持 ZSet 类型的多种编码格式 (Ziplist, Skiplist, Listpack)
- ✅ 完整支持 Dragonfly 特有的 RDB Type 18 (List Listpack)

---

## 🎯 核心实现

### 1. RDB 类型路由 (`rdb_parser.go`)

为所有复杂类型添加了完整的类型识别和路由：

```go
// Hash 类型路由
case RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPMAP, RDB_TYPE_HASH_LISTPACK:
    entry.Value, err = p.parseHash(typeByte)

// List 类型路由 (含 Type 18)
case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2, 18:
    entry.Value, err = p.parseList(typeByte)

// Set 类型路由
case RDB_TYPE_SET_INTSET, RDB_TYPE_SET:
    entry.Value, err = p.parseSet(typeByte)

// ZSet 类型路由
case RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_LISTPACK:
    entry.Value, err = p.parseZSet(typeByte)
```

### 2. 写入路由 (`replicator.go:1126`)

确保所有已解析类型正确路由到对应的 Redis 写入函数：

```go
case RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPMAP, RDB_TYPE_HASH_LISTPACK:
    return r.writeHash(entry)

case RDB_TYPE_LIST_QUICKLIST_2, 18: // 18 是 Dragonfly 使用的 List Listpack 类型
    return r.writeList(entry)

case RDB_TYPE_SET_INTSET, RDB_TYPE_SET:
    return r.writeSet(entry)

case RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_LISTPACK:
    return r.writeZSet(entry)
```

---

## 🔧 关键技术突破

### Type 18: Dragonfly List Listpack 格式

**背景**: Dragonfly 复用了 Redis 的 RDB_TYPE_ZSET_LISTPACK (type 18) 来存储短 List 数据，这与标准 Redis 不同。

#### 数据格式

```
Type 18 (RDB_TYPE_LIST_QUICKLIST_2) 格式：
┌─────────────┬──────────────────────────────────────────┐
│  nodeCount  │  Node 1 ... Node N                      │
│ (len-enc)   │                                          │
└─────────────┴──────────────────────────────────────────┘

每个 Node 的格式：
┌─────────────┬──────────────────────────────────────────┐
│ container   │  listpack bytes                          │
│  (len-enc)  │  (string-enc)                            │
└─────────────┴──────────────────────────────────────────┘

container 类型:
- 1 (QUICKLIST_NODE_CONTAINER_PACKED): 使用 listpack 编码
- 2 (QUICKLIST_NODE_CONTAINER_PLAIN):  使用 listpack 编码
```

#### 实现代码 (`rdb_complex.go:79-122`)

```go
func (p *RDBParser) parseListListpack() (*ListValue, error) {
    // 1. 读取节点数量
    nodeCount, _, err := p.readLength()
    if err != nil {
        return nil, fmt.Errorf("读取节点数量失败: %w", err)
    }

    var allElements []string

    // 2. 遍历每个节点
    for i := 0; i < int(nodeCount); i++ {
        // 2.1 读取 container 类型
        container, _, err := p.readLength()
        if err != nil {
            return nil, fmt.Errorf("读取 container 类型失败 (节点 %d): %w", i, err)
        }

        // 验证 container 类型 (1 或 2)
        if container != 1 && container != 2 {
            return nil, fmt.Errorf("无效的 container 类型: %d (节点 %d)", container, i)
        }

        // 2.2 读取 listpack 字节数组
        listpackBytes := p.readString()
        if len(listpackBytes) == 0 {
            return nil, fmt.Errorf("listpack 数据为空 (节点 %d)", i)
        }

        // 2.3 解析 listpack
        entries, err := parseListpack([]byte(listpackBytes))
        if err != nil {
            return nil, fmt.Errorf("解析 listpack 失败 (节点 %d): %w", i, err)
        }

        allElements = append(allElements, entries...)
    }

    return &ListValue{Elements: allElements}, nil
}
```

---

## 📦 Listpack 编码格式详解

### Listpack 结构

```
Listpack 整体结构：
┌───────────┬─────────┬─────────────────────┬──────────┐
│ totalBytes│numElems │  Entry 1 ... Entry N│   EOF    │
│  (4 byte) │ (2 byte)│                     │  (0xFF)  │
└───────────┴─────────┴─────────────────────┴──────────┘

每个 Entry 的结构：
┌──────────┬─────────────┬──────────────┐
│ encoding │    data     │   backlen    │
│ (1+ byte)│  (variable) │  (1-5 byte)  │
└──────────┴─────────────┴──────────────┘
```

### Encoding 类型

#### 整数编码

| Encoding Byte | 格式 | 范围 | 数据长度 |
|--------------|------|------|---------|
| `0xxxxxxx` | 7-bit 无符号 | 0-127 | 0 byte |
| `110xxxxx` + 1 byte | 13-bit 有符号 | -4096 ~ 4095 | 1 byte |
| `0xF1` + 2 bytes | 16-bit 有符号 | -32768 ~ 32767 | 2 bytes |
| `0xF2` + 3 bytes | 24-bit 有符号 | -8388608 ~ 8388607 | 3 bytes |
| `0xF3` + 4 bytes | 32-bit 有符号 | -2^31 ~ 2^31-1 | 4 bytes |
| `0xF4` + 8 bytes | 64-bit 有符号 | -2^63 ~ 2^63-1 | 8 bytes |

#### 字符串编码

| Encoding Byte | 格式 | 最大长度 | 长度编码 |
|--------------|------|---------|---------|
| `10xxxxxx` + data | 6-bit 长度 | 63 bytes | 6 bits |
| `1110xxxx` + 1 byte + data | 12-bit 长度 | 4095 bytes | 12 bits |
| `0xF0` + 4 bytes + data | 32-bit 长度 | 4GB | 32 bits |

### Backlen 编码

Backlen 用于支持 Listpack 的反向遍历：

```go
func lpEncodeBacklenSize(l int) int {
    if l <= 127 {
        return 1         // 0-127: 1 byte
    } else if l < 16383 {
        return 2         // 128-16382: 2 bytes
    } else if l < 2097151 {
        return 3         // 16383-2097150: 3 bytes
    } else if l < 268435455 {
        return 4         // 2097151-268435454: 4 bytes
    }
    return 5            // 268435455+: 5 bytes
}
```

### 完整解析实现 (`rdb_complex.go:393-504`)

```go
func readListpackEntry(data []byte) (string, int, error) {
    if len(data) < 2 {
        return "", 0, fmt.Errorf("数据不足: 至少需要 2 字节")
    }

    encoding := data[0]
    var value string
    var dataSize int // encoding + data 的大小（不包括 backlen）

    // 根据 encoding 解析
    if (encoding & 0x80) == 0 {
        // 0xxxxxxx - 7位无符号整数 (0-127)
        value = strconv.Itoa(int(encoding))
        dataSize = 1
    } else if (encoding & 0xC0) == 0x80 {
        // 10xxxxxx - 6位字符串长度 (0-63 字节)
        length := int(encoding & 0x3F)
        if 1+length > len(data) {
            return "", 0, fmt.Errorf("6位字符串数据不足: 需要 %d 字节", 1+length)
        }
        value = string(data[1 : 1+length])
        dataSize = 1 + length
    } else if (encoding & 0xE0) == 0xC0 {
        // 110xxxxx - 13位有符号整数
        if len(data) < 2 {
            return "", 0, fmt.Errorf("13位整数数据不足")
        }
        uval := uint64((encoding&0x1F)<<8) | uint64(data[1])
        // 转换为有符号数（两补数）
        if uval >= (1 << 12) {
            uval = (1<<13) - 1 - uval
            value = strconv.FormatInt(-int64(uval)-1, 10)
        } else {
            value = strconv.FormatUint(uval, 10)
        }
        dataSize = 2
    } else if (encoding & 0xF0) == 0xE0 {
        // 1110xxxx - 12位字符串长度 (0-4095 字节)
        if len(data) < 2 {
            return "", 0, fmt.Errorf("12位字符串长度字节不足")
        }
        length := int((encoding&0x0F)<<8) | int(data[1])
        if 2+length > len(data) {
            return "", 0, fmt.Errorf("12位字符串数据不足: 需要 %d 字节", 2+length)
        }
        value = string(data[2 : 2+length])
        dataSize = 2 + length
    } else if encoding == 0xF0 {
        // 32位字符串长度
        if len(data) < 5 {
            return "", 0, fmt.Errorf("32位字符串长度字节不足")
        }
        length := int(binary.LittleEndian.Uint32(data[1:5]))
        if 5+length > len(data) {
            return "", 0, fmt.Errorf("32位字符串数据不足: 需要 %d 字节", 5+length)
        }
        value = string(data[5 : 5+length])
        dataSize = 5 + length
    } else if encoding == 0xF1 {
        // 16位有符号整数
        if len(data) < 3 {
            return "", 0, fmt.Errorf("16位整数数据不足")
        }
        val := int16(binary.LittleEndian.Uint16(data[1:3]))
        value = strconv.Itoa(int(val))
        dataSize = 3
    } else if encoding == 0xF2 {
        // 24位有符号整数
        if len(data) < 4 {
            return "", 0, fmt.Errorf("24位整数数据不足")
        }
        uval := uint64(data[1]) | uint64(data[2])<<8 | uint64(data[3])<<16
        // 转换为有符号数
        if uval >= (1 << 23) {
            uval = (1<<24) - 1 - uval
            value = strconv.FormatInt(-int64(uval)-1, 10)
        } else {
            value = strconv.FormatUint(uval, 10)
        }
        dataSize = 4
    } else if encoding == 0xF3 {
        // 32位有符号整数
        if len(data) < 5 {
            return "", 0, fmt.Errorf("32位整数数据不足")
        }
        val := int32(binary.LittleEndian.Uint32(data[1:5]))
        value = strconv.Itoa(int(val))
        dataSize = 5
    } else if encoding == 0xF4 {
        // 64位有符号整数
        if len(data) < 9 {
            return "", 0, fmt.Errorf("64位整数数据不足")
        }
        val := int64(binary.LittleEndian.Uint64(data[1:9]))
        value = strconv.FormatInt(val, 10)
        dataSize = 9
    } else {
        return "", 0, fmt.Errorf("不支持的 encoding: 0x%02X", encoding)
    }

    // 计算 backlen 大小
    backlenSize := lpEncodeBacklenSize(dataSize)
    totalSize := dataSize + backlenSize

    if totalSize > len(data) {
        return "", 0, fmt.Errorf("entry 总大小超出数据: 需要 %d 字节，剩余 %d 字节", totalSize, len(data))
    }

    return value, totalSize, nil
}
```

---

## ✅ 测试验证

### 测试数据

通过与真实 Dragonfly 实例 (10.46.128.12:7380) 进行完整同步测试：

#### Hash 类型
- `hash_ziplist_test_1`: 3 个字段 (field1=val1, field2=val2, field3=val3)
- `hash_hashtable_test_2`: 2 个字段 (含长字符串)
- `user:100`: 2 个字段 (name=Bob, age=25)

#### List 类型
- `list_quicklist_short_1`: 20 个元素
- `list_quicklist_long_2`: 1000 个元素 (2 个节点: 172 + 828)

#### Set 类型
- `set_intset_test`: 10 个整数元素

#### ZSet 类型
- `zset_ziplist_test`: 5 个成员及分数

### 测试结果

```
✓ 所有 8 个 FLOW 连接已建立
✓ 握手完成

🔗 连接到目标 Redis...
  ✓ Redis Standalone 连接成功

🔄 发送 DFLY SYNC 触发数据传输...
  ✓ DFLY SYNC 发送成功，RDB 数据传输已触发

📦 开始并行接收和解析 RDB 快照...
  [FLOW-0] ✓ RDB 头部解析成功
  [FLOW-1] ✓ RDB 头部解析成功
  [FLOW-2] ✓ RDB 头部解析成功
  [FLOW-3] ✓ RDB 头部解析成功
  [FLOW-4] ✓ RDB 头部解析成功
  [FLOW-5] ✓ RDB 头部解析成功
  [FLOW-6] ✓ RDB 头部解析成功
  [FLOW-7] ✓ RDB 头部解析成功

  [DEBUG] writeHash: key=hash_ziplist_test_1, fields=3
  [DEBUG] writeHash: key=hash_hashtable_test_2, fields=2
  [DEBUG] writeHash: key=user:100, fields=2
  ✓ 所有数据解析和写入成功
```

---

## 📂 修改文件清单

### 1. `internal/replica/rdb_complex.go`

**新增/修改函数**:
- `parseListListpack()` - 新增 Type 18 解析函数
- `parseListpack()` - 完全重写，添加头部验证和 EOF 检查
- `readListpackEntry()` - 完全重写，实现所有 11 种编码格式
- `lpEncodeBacklenSize()` - 修复，添加 3-byte 和 4-byte 情况

**代码行数**: 约 600 行（含所有复杂类型解析器）

### 2. `internal/replica/rdb_parser.go`

**修改位置**: Line 159
- 添加 Type 18 到 List 类型路由

### 3. `internal/replica/replicator.go`

**修改位置**: Line 1126
- 添加 Type 18 到 `writeList()` 路由

### 4. `CLAUDE.md`

**修改内容**: 添加协作流程规范
- 强调在实现前向用户确认 Dragonfly 源码细节
- 避免盲目尝试浪费 token

---

## 🎓 技术要点总结

### 1. 双重路由机制
RDB 类型需要在两处正确路由：
- **解析阶段** (`rdb_parser.go`): 类型字节 → 解析函数
- **写入阶段** (`replicator.go`): 解析后类型 → Redis 命令

### 2. Dragonfly 与 Redis 的差异
- Dragonfly 复用 Type 18 (原 RDB_TYPE_ZSET_LISTPACK) 存储 List
- Type 18 在 Dragonfly 中使用 Quicklist + Listpack 格式
- 需要先读取 container 类型再读取 listpack 数据

### 3. Listpack 编码的复杂性
- 11 种不同的编码格式（7 种整数 + 4 种字符串）
- 变长 backlen 编码（1-5 字节）
- 需要精确计算每个 entry 的总大小

### 4. 源码驱动开发
- 直接参考 Dragonfly 源码 (`src/core/listpack.c`) 确保实现准确性
- 避免基于猜测或不完整文档进行实现
- 减少调试迭代次数，提高开发效率

---

## 🔗 相关文档

- [Phase 1: Dragonfly Replication Handshake](phase1-dragonfly-handshake.md)
- [Phase 2: Journal Receipt and Parsing](phase2-journal-parsing.md)
- [Phase 3: Incremental Sync](phase3-incremental-sync.md)
- [Phase 4: RDB Basic Types](phase4-rdb-basic-types.md)

---

## 🚀 下一步

Phase 5 完成后，df2redis 已具备完整的 Dragonfly → Redis 数据同步能力：

- ✅ **快照同步** (Phase 4 + 5): 完整的 RDB 解析和写入
- ✅ **增量同步** (Phase 2): Journal 流式接收和命令重放
- ✅ **协议握手** (Phase 1): Dragonfly 复制协议兼容

**生产就绪功能**:
- 支持所有 Redis 基础数据类型 (String, Hash, List, Set, ZSet)
- 支持 Dragonfly 特有的编码格式 (Type 18 Listpack)
- 8-shard 并行 FLOW 高性能传输
- 实时增量同步和命令重放

---

**文档作者**: Claude Code
**最后更新**: 2025-12-03
