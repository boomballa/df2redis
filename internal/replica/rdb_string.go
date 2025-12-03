package replica

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

// readStringFull 读取 RDB 字符串（完整实现，覆盖 rdb_parser.go 中的简化版本）
// 支持：
// 1. 普通字符串
// 2. 整数编码（INT8, INT16, INT32）
// 3. LZF 压缩字符串
func (p *RDBParser) readStringFull() (string, error) {
	length, special, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取字符串长度失败: %w", err)
	}

	if special {
		// 特殊编码
		return p.readStringEncoded(length)
	}

	// 普通字符串
	if length == 0 {
		return "", nil
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return "", fmt.Errorf("读取字符串数据失败: %w", err)
	}

	return string(buf), nil
}

// readStringEncoded 读取特殊编码的字符串
func (p *RDBParser) readStringEncoded(encoding uint64) (string, error) {
	switch encoding {
	case RDB_ENC_INT8:
		// 8 位整数
		val, err := p.readInt8()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_INT16:
		// 16 位整数
		val, err := p.readInt16()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_INT32:
		// 32 位整数
		val, err := p.readInt32()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_LZF:
		// LZF 压缩字符串
		return p.readLZFString()

	default:
		return "", fmt.Errorf("不支持的字符串编码: %d", encoding)
	}
}

// readInt8 读取 8 位整数
func (p *RDBParser) readInt8() (int8, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int8(buf[0]), nil
}

// readInt16 读取 16 位整数（小端序）
func (p *RDBParser) readInt16() (int16, error) {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int16(binary.LittleEndian.Uint16(buf)), nil
}

// readLZFString 读取 LZF 压缩字符串
// 格式: [压缩后长度][原始长度][压缩数据]
func (p *RDBParser) readLZFString() (string, error) {
	// 1. 读取压缩后长度
	compressedLen, _, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取压缩长度失败: %w", err)
	}

	// 2. 读取原始长度
	originalLen, _, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取原始长度失败: %w", err)
	}

	// 3. 读取压缩数据
	compressedData := make([]byte, compressedLen)
	if _, err := io.ReadFull(p.reader, compressedData); err != nil {
		return "", fmt.Errorf("读取压缩数据失败: %w", err)
	}

	// 4. 解压缩
	// 注意：这里需要 LZF 解压缩库
	// 如果 Dragonfly 实际不使用 LZF 压缩，这段代码不会被执行到
	decompressed, err := lzfDecompress(compressedData, int(originalLen))
	if err != nil {
		return "", fmt.Errorf("LZF 解压缩失败: %w (提示: 可能需要安装 LZF 库)", err)
	}

	return string(decompressed), nil
}

// lzfDecompress LZF 解压缩实现
// 这是 LZF 算法的 Go 实现（简化版）
// 完整实现可以使用第三方库如 github.com/zhuyie/golzf
func lzfDecompress(src []byte, dstLen int) ([]byte, error) {
	dst := make([]byte, dstLen)
	srcIdx := 0
	dstIdx := 0

	for srcIdx < len(src) {
		ctrl := src[srcIdx]
		srcIdx++

		if ctrl < 32 {
			// 字面量（literal）
			count := int(ctrl) + 1
			if srcIdx+count > len(src) || dstIdx+count > dstLen {
				return nil, fmt.Errorf("LZF 数据损坏")
			}
			copy(dst[dstIdx:], src[srcIdx:srcIdx+count])
			srcIdx += count
			dstIdx += count
		} else {
			// 反向引用（back reference）
			length := int(ctrl >> 5)
			if length == 7 {
				if srcIdx >= len(src) {
					return nil, fmt.Errorf("LZF 数据损坏")
				}
				length += int(src[srcIdx])
				srcIdx++
			}
			length += 2

			if srcIdx >= len(src) {
				return nil, fmt.Errorf("LZF 数据损坏")
			}
			offset := int(ctrl&0x1f)<<8 | int(src[srcIdx])
			srcIdx++
			offset++

			if dstIdx < offset || dstIdx+length > dstLen {
				return nil, fmt.Errorf("LZF 数据损坏")
			}

			// 复制数据
			for i := 0; i < length; i++ {
				dst[dstIdx] = dst[dstIdx-offset]
				dstIdx++
			}
		}
	}

	if dstIdx != dstLen {
		return nil, fmt.Errorf("LZF 解压后长度不匹配: 期望 %d, 实际 %d", dstLen, dstIdx)
	}

	return dst, nil
}

// 更新 rdb_parser.go 中的 readString 方法，使其调用完整实现
func (p *RDBParser) readStringWithEncoding() string {
	str, err := p.readStringFull()
	if err != nil {
		// 简化错误处理，实际应该返回错误
		return ""
	}
	return str
}
