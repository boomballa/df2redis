package replica

import (
	"encoding/binary"
	"fmt"
	"io"
)

// RDB 编码常量（与 Dragonfly 源码保持一致）
const (
	RDB_6BITLEN  = 0    // 00|XXXXXX - 6位值
	RDB_14BITLEN = 1    // 01|XXXXXX XXXXXXXX - 14位值
	RDB_32BITLEN = 0x80 // 10000000 [32-bit big-endian]
	RDB_64BITLEN = 0x81 // 10000001 [64-bit big-endian]
	RDB_ENCVAL   = 3    // 11|XXXXXX - 特殊编码对象
)

// ReadPackedUint 从 reader 读取一个 Packed Uint
// 编码规则：
//   - 00|XXXXXX: 6位值 (< 64)，1字节
//   - 01|XXXXXX XXXXXXXX: 14位值 (< 16384)，2字节
//   - 10000000 [32-bit big-endian]: 32位整数，5字节
//   - 10000001 [64-bit big-endian]: 64位整数，9字节
func ReadPackedUint(r io.Reader) (uint64, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}

	firstByte := buf[0]
	typeField := (firstByte >> 6) & 0x03 // 取高2位

	switch typeField {
	case RDB_6BITLEN:
		// 00|XXXXXX - 6位值
		return uint64(firstByte & 0x3F), nil

	case RDB_14BITLEN:
		// 01|XXXXXX XXXXXXXX - 14位值
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		val := (uint64(firstByte&0x3F) << 8) | uint64(buf[0])
		return val, nil

	case 2: // 10|XXXXXX
		// 检查低6位来判断是32位还是64位
		if firstByte == RDB_32BITLEN {
			// 10000000 - 32位整数
			buf32 := make([]byte, 4)
			if _, err := io.ReadFull(r, buf32); err != nil {
				return 0, err
			}
			return uint64(binary.BigEndian.Uint32(buf32)), nil
		} else if firstByte == RDB_64BITLEN {
			// 10000001 - 64位整数
			buf64 := make([]byte, 8)
			if _, err := io.ReadFull(r, buf64); err != nil {
				return 0, err
			}
			return binary.BigEndian.Uint64(buf64), nil
		}
		return 0, fmt.Errorf("无效的 RDB 编码标记: 0x%02x", firstByte)

	case RDB_ENCVAL:
		// 11|XXXXXX - 特殊编码对象（暂不支持）
		return 0, fmt.Errorf("不支持的 RDB 特殊编码: 0x%02x", firstByte)

	default:
		return 0, fmt.Errorf("未知的 RDB 编码类型: %d", typeField)
	}
}

// ReadPackedString 从 reader 读取一个 Packed String
// 格式：长度（Packed Uint）+ 字符串内容
func ReadPackedString(r io.Reader) (string, error) {
	length, err := ReadPackedUint(r)
	if err != nil {
		return "", fmt.Errorf("读取字符串长度失败: %w", err)
	}

	if length == 0 {
		return "", nil
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("读取字符串内容失败: %w", err)
	}

	return string(buf), nil
}
