package replica

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

// readStringFull implements full RDB string decoding (see readString stub in rdb_parser.go).
// Supports:
// 1. Plain strings
// 2. Integer encodings (INT8/INT16/INT32)
// 3. LZF-compressed strings
func (p *RDBParser) readStringFull() (string, error) {
	length, special, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取字符串长度失败: %w", err)
	}

	if special {
		// Special encodings
		return p.readStringEncoded(length)
	}

	// Plain string
	if length == 0 {
		return "", nil
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return "", fmt.Errorf("读取字符串数据失败: %w", err)
	}

	return string(buf), nil
}

// readStringEncoded handles integer/LZF encodings
func (p *RDBParser) readStringEncoded(encoding uint64) (string, error) {
	switch encoding {
	case RDB_ENC_INT8:
		// 8-bit integer
		val, err := p.readInt8()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_INT16:
		// 16-bit integer
		val, err := p.readInt16()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_INT32:
		// 32-bit integer
		val, err := p.readInt32()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(val)), nil

	case RDB_ENC_LZF:
		// LZF-compressed string
		return p.readLZFString()

	default:
		return "", fmt.Errorf("不支持的字符串编码: %d", encoding)
	}
}

// readInt8 reads an 8-bit integer
func (p *RDBParser) readInt8() (int8, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int8(buf[0]), nil
}

// readInt16 reads a little-endian 16-bit integer
func (p *RDBParser) readInt16() (int16, error) {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int16(binary.LittleEndian.Uint16(buf)), nil
}

// readLZFString handles the LZF format: [compressed_len][original_len][payload]
func (p *RDBParser) readLZFString() (string, error) {
	// 1. Compressed length
	compressedLen, _, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取压缩长度失败: %w", err)
	}

	// 2. Original length
	originalLen, _, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("读取原始长度失败: %w", err)
	}

	// 3. Compressed payload
	compressedData := make([]byte, compressedLen)
	if _, err := io.ReadFull(p.reader, compressedData); err != nil {
		return "", fmt.Errorf("读取压缩数据失败: %w", err)
	}

	// 4. Decompress (requires an LZF implementation; unused if Dragonfly avoids LZF)
	decompressed, err := lzfDecompress(compressedData, int(originalLen))
	if err != nil {
		return "", fmt.Errorf("LZF 解压缩失败: %w (提示: 可能需要安装 LZF 库)", err)
	}

	return string(decompressed), nil
}

// lzfDecompress provides a lightweight LZF decoder.
// For production use consider github.com/zhuyie/golzf or similar.
func lzfDecompress(src []byte, dstLen int) ([]byte, error) {
	dst := make([]byte, dstLen)
	srcIdx := 0
	dstIdx := 0

	for srcIdx < len(src) {
		ctrl := src[srcIdx]
		srcIdx++

		if ctrl < 32 {
			// Literal bytes
			count := int(ctrl) + 1
			if srcIdx+count > len(src) || dstIdx+count > dstLen {
				return nil, fmt.Errorf("LZF 数据损坏")
			}
			copy(dst[dstIdx:], src[srcIdx:srcIdx+count])
			srcIdx += count
			dstIdx += count
		} else {
			// Back reference
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

			// Copy referenced bytes
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

// readStringWithEncoding wires the full implementation into legacy callers
func (p *RDBParser) readStringWithEncoding() string {
	str, err := p.readStringFull()
	if err != nil {
		// Simplified handling; real errors bubble up elsewhere
		return ""
	}
	return str
}
