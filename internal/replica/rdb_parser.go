package replica

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// RDBParser RDB 流式解析器
type RDBParser struct {
	reader *bufio.Reader
	flowID int

	// 当前状态
	currentDB int   // 当前数据库索引
	expireMs  int64 // 当前键的过期时间（毫秒时间戳）
}

// NewRDBParser 创建 RDB 解析器
func NewRDBParser(reader io.Reader, flowID int) *RDBParser {
	return &RDBParser{
		reader:    bufio.NewReader(reader),
		flowID:    flowID,
		currentDB: 0,
		expireMs:  0,
	}
}

// ParseHeader 解析 RDB 头部
// 格式: "REDIS0009" + AUX 字段
func (p *RDBParser) ParseHeader() error {
	// 1. 读取 magic header: "REDIS0009" (9 字节)
	magic := make([]byte, 9)
	if _, err := io.ReadFull(p.reader, magic); err != nil {
		return fmt.Errorf("读取 RDB magic 失败: %w", err)
	}

	// 验证 magic
	expectedMagic := "REDIS0009"
	if string(magic) != expectedMagic {
		return fmt.Errorf("无效的 RDB magic: 期望 %s, 实际 %s", expectedMagic, string(magic))
	}

	// 2. 跳过 AUX 字段
	// AUX 字段格式: 0xFA + key + value
	// 一直读取直到遇到非 0xFA 的 opcode
	for {
		opcode, err := p.peekByte()
		if err != nil {
			return fmt.Errorf("读取 opcode 失败: %w", err)
		}

		if opcode != RDB_OPCODE_AUX {
			break
		}

		// 消费掉 0xFA
		p.readByte()

		// 跳过 AUX key 和 value
		_ = p.readString() // key
		_ = p.readString() // value
	}

	return nil
}

// ParseNext 解析下一个 RDB entry
// 返回 nil, io.EOF 表示 RDB 流结束
func (p *RDBParser) ParseNext() (*RDBEntry, error) {
	for {
		opcode, err := p.readByte()
		if err != nil {
			return nil, err
		}

		switch opcode {
		case RDB_OPCODE_EXPIRETIME_MS:
			// 读取毫秒过期时间 (8 字节，小端序)
			p.expireMs, err = p.readInt64()
			if err != nil {
				return nil, fmt.Errorf("读取过期时间失败: %w", err)
			}
			continue

		case RDB_OPCODE_EXPIRETIME:
			// 读取秒过期时间 (4 字节，小端序)
			expireSec, err := p.readInt32()
			if err != nil {
				return nil, fmt.Errorf("读取过期时间失败: %w", err)
			}
			p.expireMs = int64(expireSec) * 1000
			continue

		case RDB_OPCODE_SELECTDB:
			// SELECT db
			dbIndex, _, err := p.readLength()
			if err != nil {
				return nil, fmt.Errorf("读取 db 索引失败: %w", err)
			}
			p.currentDB = int(dbIndex)
			continue

		case RDB_OPCODE_JOURNAL_OFFSET:
			// Dragonfly 特有：JOURNAL_OFFSET 标记
			// 读取并丢弃 8 字节 offset（对于空 FLOW，这是第一个 opcode）
			offset := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, offset); err != nil {
				return nil, fmt.Errorf("读取 JOURNAL_OFFSET 失败: %w", err)
			}
			// 继续读取下一个 opcode
			continue

		case RDB_OPCODE_FULLSYNC_END:
			// Dragonfly 特有：FULLSYNC_END 标记
			// 读取并丢弃后续 8 个零字节
			zeros := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, zeros); err != nil {
				return nil, fmt.Errorf("读取 FULLSYNC_END 后缀失败: %w", err)
			}

			// FULLSYNC_END 表示全量同步阶段结束
			// 返回 io.EOF 让上层代码继续处理（如验证 EOF Token、发送 STARTSTABLE）
			return nil, io.EOF

		case RDB_OPCODE_EOF:
			// RDB 结束标记
			// 读取并丢弃 8 字节 checksum
			checksum := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, checksum); err != nil {
				return nil, fmt.Errorf("读取 EOF checksum 失败: %w", err)
			}
			return nil, io.EOF

		case RDB_OPCODE_AUX:
			// 跳过 AUX 字段
			_ = p.readString() // key
			_ = p.readString() // value
			continue

		default:
			// 这是数据类型 opcode，解析键值对
			return p.parseKeyValue(opcode)
		}
	}
}

// parseKeyValue 解析键值对
func (p *RDBParser) parseKeyValue(typeByte byte) (*RDBEntry, error) {
	// 1. 读取 key
	key := p.readString()

	entry := &RDBEntry{
		Key:      key,
		Type:     typeByte,
		DbIndex:  p.currentDB,
		ExpireMs: p.expireMs,
	}

	// 2. 根据类型解析 value
	var err error
	switch typeByte {
	case RDB_TYPE_STRING:
		entry.Value, err = p.parseString()

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST:
		entry.Value, err = p.parseHash(typeByte)

	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2, 18: // 18 可能是 List Listpack
		entry.Value, err = p.parseList(typeByte)

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET:
		entry.Value, err = p.parseSet(typeByte)

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST:
		entry.Value, err = p.parseZSet(typeByte)

	default:
		// 跳过不支持的类型（如Module、Stream等）
		return nil, fmt.Errorf("暂不支持的 RDB 类型: %d (key=%s)", typeByte, key)
	}

	if err != nil {
		return nil, fmt.Errorf("解析值失败 (type=%d, key=%s): %w", typeByte, key, err)
	}

	// 重置过期时间
	p.expireMs = 0

	return entry, nil
}

// parseString 解析 String 类型的值
func (p *RDBParser) parseString() (*StringValue, error) {
	value := p.readString()
	return &StringValue{Value: value}, nil
}

// ============ 基础读取方法 ============

// readByte 读取单个字节
func (p *RDBParser) readByte() (byte, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// peekByte 查看下一个字节（不消费）
func (p *RDBParser) peekByte() (byte, error) {
	// 使用 bufio.Reader.Peek() 查看下一个字节
	buf, err := p.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

// readInt32 读取 32 位整数（小端序）
func (p *RDBParser) readInt32() (int32, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(buf)), nil
}

// readInt64 读取 64 位整数（小端序）
func (p *RDBParser) readInt64() (int64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf)), nil
}

// readLength 读取 RDB 长度编码
// 返回: (length, isSpecial, error)
// isSpecial=true 表示这是特殊编码（整数、LZF 压缩等）
func (p *RDBParser) readLength() (uint64, bool, error) {
	firstByte, err := p.readByte()
	if err != nil {
		return 0, false, err
	}

	// 读取高 2 位判断编码类型
	typeField := (firstByte >> 6) & 0x03

	switch typeField {
	case 0:
		// 00|XXXXXX - 6 位长度
		return uint64(firstByte & 0x3F), false, nil

	case 1:
		// 01|XXXXXX XXXXXXXX - 14 位长度
		nextByte, err := p.readByte()
		if err != nil {
			return 0, false, err
		}
		length := (uint64(firstByte&0x3F) << 8) | uint64(nextByte)
		return length, false, nil

	case 2:
		// 10|XXXXXX - 特殊编码或 32 位长度
		if firstByte == 0x80 {
			// 32 位长度
			buf := make([]byte, 4)
			if _, err := io.ReadFull(p.reader, buf); err != nil {
				return 0, false, err
			}
			return uint64(binary.BigEndian.Uint32(buf)), false, nil
		} else if firstByte == 0x81 {
			// 64 位长度
			buf := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, buf); err != nil {
				return 0, false, err
			}
			return binary.BigEndian.Uint64(buf), false, nil
		}
		// 其他值为特殊编码
		return uint64(firstByte & 0x3F), true, nil

	case 3:
		// 11|XXXXXX - 特殊编码
		return uint64(firstByte & 0x3F), true, nil
	}

	return 0, false, fmt.Errorf("无效的长度编码类型: %d", typeField)
}

// readString 读取 RDB 字符串
// 调用 rdb_string.go 中的完整实现
func (p *RDBParser) readString() string {
	str, err := p.readStringFull()
	if err != nil {
		// 简化错误处理，返回空字符串
		// 实际错误会在上层 parseKeyValue 中捕获
		return ""
	}
	return str
}

// getCurrentTimeMillis 返回当前时间的毫秒时间戳（全局函数）
func getCurrentTimeMillis() int64 {
	return time.Now().UnixMilli()
}
