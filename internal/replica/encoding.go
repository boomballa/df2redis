package replica

import (
	"encoding/binary"
	"fmt"
	"io"
)

// RDB encoding constants aligned with Dragonfly
const (
	RDB_6BITLEN  = 0    // 00|XXXXXX - 6-bit value
	RDB_14BITLEN = 1    // 01|XXXXXX XXXXXXXX - 14-bit value
	RDB_32BITLEN = 0x80 // 10000000 [32-bit big-endian]
	RDB_64BITLEN = 0x81 // 10000001 [64-bit big-endian]
	RDB_ENCVAL   = 3    // 11|XXXXXX - special encoded object
)

// ReadPackedUint reads a packed integer.
// Encoding:
//   - 00|XXXXXX: 6-bit value (<64), 1 byte
//   - 01|XXXXXX XXXXXXXX: 14-bit value (<16384), 2 bytes
//   - 10000000 + 32-bit big-endian: 32-bit integer, 5 bytes
//   - 10000001 + 64-bit big-endian: 64-bit integer, 9 bytes
func ReadPackedUint(r io.Reader) (uint64, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}

	firstByte := buf[0]
	typeField := (firstByte >> 6) & 0x03 // top two bits

	switch typeField {
	case RDB_6BITLEN:
		// 00|XXXXXX - 6-bit value
		return uint64(firstByte & 0x3F), nil

	case RDB_14BITLEN:
		// 01|XXXXXX XXXXXXXX - 14-bit value
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		val := (uint64(firstByte&0x3F) << 8) | uint64(buf[0])
		return val, nil

	case 2: // 10|XXXXXX
		// Inspect low bits to distinguish 32- vs 64-bit encodings
		if firstByte == RDB_32BITLEN {
			// 10000000 - 32-bit integer
			buf32 := make([]byte, 4)
			if _, err := io.ReadFull(r, buf32); err != nil {
				return 0, err
			}
			return uint64(binary.BigEndian.Uint32(buf32)), nil
		} else if firstByte == RDB_64BITLEN {
			// 10000001 - 64-bit integer
			buf64 := make([]byte, 8)
			if _, err := io.ReadFull(r, buf64); err != nil {
				return 0, err
			}
			return binary.BigEndian.Uint64(buf64), nil
		}
		return 0, fmt.Errorf("invalid RDB length encoding marker: 0x%02x", firstByte)

	case RDB_ENCVAL:
		// 11|XXXXXX - special encoding (unsupported)
		return 0, fmt.Errorf("unsupported RDB special encoding: 0x%02x", firstByte)

	default:
		return 0, fmt.Errorf("unknown RDB length encoding type: %d", typeField)
	}
}

// ReadPackedString reads a length-prefixed string (packed uint + bytes)
func ReadPackedString(r io.Reader) (string, error) {
	length, err := ReadPackedUint(r)
	if err != nil {
		return "", fmt.Errorf("failed to read string length: %w", err)
	}

	if length == 0 {
		return "", nil
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("failed to read string payload: %w", err)
	}

	return string(buf), nil
}
