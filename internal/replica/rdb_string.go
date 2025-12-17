package replica

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"

	lzf "github.com/zhuyie/golzf"
)

// readStringFull implements full RDB string decoding (see readString stub in rdb_parser.go).
// Supports:
// 1. Plain strings
// 2. Integer encodings (INT8/INT16/INT32)
// 3. LZF-compressed strings
func (p *RDBParser) readStringFull() (string, error) {
	length, special, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("failed to read string length: %w", err)
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
	n, err := io.ReadFull(p.reader, buf)
	if err != nil {
		// Enhanced error logging for EOF issues
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", fmt.Errorf("failed to read string data: expected %d bytes, got %d bytes (stream ended prematurely, possible network issue or Dragonfly bug): %w", length, n, err)
		}
		return "", fmt.Errorf("failed to read string data: %w", err)
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
		// Check if this might be a Dragonfly-specific encoding
		if encoding >= 4 && encoding <= 15 {
			return "", fmt.Errorf("unsupported string encoding: %d (possible Dragonfly extension - please report this)", encoding)
		}
		return "", fmt.Errorf("unsupported string encoding: %d (invalid encoding value)", encoding)
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
		return "", fmt.Errorf("failed to read compressed length: %w", err)
	}

	// 2. Original length
	originalLen, _, err := p.readLength()
	if err != nil {
		return "", fmt.Errorf("failed to read original length: %w", err)
	}

	// 3. Compressed payload
	compressedData := make([]byte, compressedLen)
	if _, err := io.ReadFull(p.reader, compressedData); err != nil {
		return "", fmt.Errorf("failed to read compressed data: %w", err)
	}

	// 4. Decompress (requires an LZF implementation; unused if Dragonfly avoids LZF)
	decompressed, err := lzfDecompress(compressedData, int(originalLen))
	if err != nil {
		return "", fmt.Errorf("LZF decompression failed: %w (hint: ensure LZF library is available)", err)
	}

	return string(decompressed), nil
}

// lzfDecompress uses the golzf library for LZF decompression.
func lzfDecompress(src []byte, dstLen int) ([]byte, error) {
	dst := make([]byte, dstLen)
	n, err := lzf.Decompress(src, dst)
	if err != nil {
		return nil, fmt.Errorf("LZF decompression failed: %w", err)
	}
	if n != dstLen {
		return nil, fmt.Errorf("LZF decompressed length mismatch: expect %d, got %d", dstLen, n)
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
