package replica

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"strconv"
)

// ============ Hash parsing ============

// parseHash decodes the hash value based on encoding type
func (p *RDBParser) parseHash(typeByte byte) (*HashValue, error) {
	switch typeByte {
	case RDB_TYPE_HASH:
		return p.parseHashStandard()
	case RDB_TYPE_HASH_ZIPLIST:
		return p.parseHashZiplist()
	case RDB_TYPE_HASH_LISTPACK:
		return p.parseHashListpack()
	default:
		return nil, fmt.Errorf("unsupported hash encoding type: %d", typeByte)
	}
}

// parseHashStandard reads the plain hash encoding (RDB_TYPE_HASH = 4)
func (p *RDBParser) parseHashStandard() (*HashValue, error) {
	// Read field count
	size, _, err := p.readLength()
	if err != nil {
		return nil, err
	}

	fields := make(map[string]string, size)
	for i := uint64(0); i < size; i++ {
		field := p.readString()
		value := p.readString()
		fields[field] = value
	}

	return &HashValue{Fields: fields}, nil
}

// parseHashZiplist decodes the ziplist-encoded hash (RDB_TYPE_HASH_ZIPLIST = 13)
func (p *RDBParser) parseHashZiplist() (*HashValue, error) {
	// Read ziplist bytes
	ziplistBytes := p.readString()

	// Decode ziplist
	entries, err := parseZiplist([]byte(ziplistBytes))
	if err != nil {
		return nil, err
	}

	// Fields and values alternate in the ziplist
	fields := make(map[string]string)
	for i := 0; i < len(entries); i += 2 {
		if i+1 < len(entries) {
			fields[entries[i]] = entries[i+1]
		}
	}

	return &HashValue{Fields: fields}, nil
}

// parseHashListpack decodes the listpack-encoded hash (RDB_TYPE_HASH_LISTPACK = 16)
func (p *RDBParser) parseHashListpack() (*HashValue, error) {
	// Read listpack bytes
	listpackBytes := p.readString()

	// Decode listpack
	entries, err := parseListpack([]byte(listpackBytes))
	if err != nil {
		return nil, err
	}

	// Fields and values alternate in the listpack
	fields := make(map[string]string)
	for i := 0; i < len(entries); i += 2 {
		if i+1 < len(entries) {
			fields[entries[i]] = entries[i+1]
		}
	}

	return &HashValue{Fields: fields}, nil
}

// ============ List parsing ============

// parseList decodes list values depending on encoding
func (p *RDBParser) parseList(typeByte byte) (*ListValue, error) {
	switch typeByte {
	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2:
		return p.parseListQuicklist2()
	default:
		return nil, fmt.Errorf("unsupported list encoding type: %d", typeByte)
	}
}

// parseListQuicklist2 handles Quicklist 2.0 (RDB_TYPE_LIST_QUICKLIST_2 = 17)
func (p *RDBParser) parseListQuicklist2() (*ListValue, error) {
	// Number of quicklist nodes
	size, _, err := p.readLength()
	if err != nil {
		return nil, err
	}

	var elements []string
	for i := uint64(0); i < size; i++ {
		// Container type (1=plain, 2=packed/listpack)
		container, _, err := p.readLength()
		if err != nil {
			return nil, err
		}

		if container == QUICKLIST_NODE_CONTAINER_PACKED {
			// Packed container (listpack)
			listpackBytes := p.readString()
			entries, err := parseListpack([]byte(listpackBytes))
			if err != nil {
				return nil, err
			}
			elements = append(elements, entries...)
		} else {
			// Plain container
			value := p.readString()
			elements = append(elements, value)
		}
	}

	return &ListValue{Elements: elements}, nil
}

// ============ Set parsing ============

// parseSet decodes set encodings
func (p *RDBParser) parseSet(typeByte byte) (*SetValue, error) {
	switch typeByte {
	case RDB_TYPE_SET:
		return p.parseSetStandard()
	case RDB_TYPE_SET_INTSET:
		return p.parseSetIntset()
	case RDB_TYPE_SET_LISTPACK:
		return p.parseSetListpack()
	default:
		return nil, fmt.Errorf("unsupported set encoding type: %d", typeByte)
	}
}

// parseSetStandard reads the plain set encoding (RDB_TYPE_SET = 2)
func (p *RDBParser) parseSetStandard() (*SetValue, error) {
	// Member count
	size, _, err := p.readLength()
	if err != nil {
		return nil, err
	}

	members := make([]string, size)
	for i := uint64(0); i < size; i++ {
		members[i] = p.readString()
	}

	return &SetValue{Members: members}, nil
}

// parseSetIntset handles the intset encoding (RDB_TYPE_SET_INTSET = 11)
func (p *RDBParser) parseSetIntset() (*SetValue, error) {
	// Read intset bytes
	intsetBytes := p.readString()

	// Decode intset contents
	members, err := parseIntset([]byte(intsetBytes))
	if err != nil {
		return nil, err
	}

	return &SetValue{Members: members}, nil
}

// parseSetListpack handles the listpack encoding (RDB_TYPE_SET_LISTPACK = 20)
func (p *RDBParser) parseSetListpack() (*SetValue, error) {
	// Read listpack bytes
	listpackBytes := p.readString()

	// Decode listpack contents
	members, err := parseListpack([]byte(listpackBytes))
	if err != nil {
		return nil, err
	}

	return &SetValue{Members: members}, nil
}

// ============ ZSet parsing ============

// parseZSet decodes sorted sets
func (p *RDBParser) parseZSet(typeByte byte) (*ZSetValue, error) {
	switch typeByte {
	case RDB_TYPE_ZSET_2:
		return p.parseZSetStandard()
	case RDB_TYPE_ZSET_ZIPLIST:
		return p.parseZSetZiplist()
	case RDB_TYPE_ZSET_LISTPACK:
		return p.parseZSetListpack()
	default:
		return nil, fmt.Errorf("unsupported zset encoding type: %d", typeByte)
	}
}

// parseZSetStandard reads the standard encoding (RDB_TYPE_ZSET_2 = 5)
func (p *RDBParser) parseZSetStandard() (*ZSetValue, error) {
	// Member count
	size, _, err := p.readLength()
	if err != nil {
		return nil, err
	}

	members := make([]ZSetMember, size)
	for i := uint64(0); i < size; i++ {
		member := p.readString()
		score, err := p.readDouble()
		if err != nil {
			return nil, err
		}
		members[i] = ZSetMember{Member: member, Score: score}
	}

	return &ZSetValue{Members: members}, nil
}

// parseZSetZiplist handles the ziplist encoding (RDB_TYPE_ZSET_ZIPLIST = 12)
func (p *RDBParser) parseZSetZiplist() (*ZSetValue, error) {
	// Read ziplist payload
	ziplistBytes := p.readString()

	// Decode ziplist
	entries, err := parseZiplist([]byte(ziplistBytes))
	if err != nil {
		return nil, err
	}

	// Entries alternate between member and score
	var members []ZSetMember
	for i := 0; i < len(entries); i += 2 {
		if i+1 < len(entries) {
			member := entries[i]
			score, _ := strconv.ParseFloat(entries[i+1], 64)
			members = append(members, ZSetMember{Member: member, Score: score})
		}
	}

	return &ZSetValue{Members: members}, nil
}

// parseZSetListpack handles the listpack encoding (RDB_TYPE_ZSET_LISTPACK = 17)
func (p *RDBParser) parseZSetListpack() (*ZSetValue, error) {
	// Read listpack bytes
	listpackBytes := p.readString()

	// Decode listpack
	entries, err := parseListpack([]byte(listpackBytes))
	if err != nil {
		return nil, err
	}

	// Entries alternate between member and score
	var members []ZSetMember
	for i := 0; i < len(entries); i += 2 {
		if i+1 < len(entries) {
			member := entries[i]
			score, _ := strconv.ParseFloat(entries[i+1], 64)
			members = append(members, ZSetMember{Member: member, Score: score})
		}
	}

	return &ZSetValue{Members: members}, nil
}

// ============ Helper: double reader ============

// readDouble reads an 8-byte little-endian float64
func (p *RDBParser) readDouble() (float64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	bits := binary.LittleEndian.Uint64(buf)
	return math.Float64frombits(bits), nil
}

// ============ Ziplist parsing ============

// parseZiplist parses the layout [zlbytes][zltail][zllen][entries...][zlend=0xFF]
func parseZiplist(data []byte) ([]string, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("ziplist payload too short")
	}

	// Skip header (4+4+2 bytes)
	offset := 10
	var entries []string

	for offset < len(data) {
		if data[offset] == 0xFF {
			// zlend marker
			break
		}

		// Read entry
		entry, n, err := readZiplistEntry(data[offset:])
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		offset += n
	}

	return entries, nil
}

// readZiplistEntry decodes a single ziplist entry
func readZiplistEntry(data []byte) (string, int, error) {
	if len(data) < 1 {
		return "", 0, fmt.Errorf("ziplist entry does not have enough data")
	}

	offset := 0

	// 1. Skip prevlen (1 or 5 bytes)
	if data[offset] < 254 {
		offset++
	} else {
		offset += 5
	}

	if offset >= len(data) {
		return "", 0, fmt.Errorf("ziplist entry does not have enough data")
	}

	// 2. Encoding byte
	encoding := data[offset]
	offset++

	// 3. Interpret payload per encoding
	if (encoding & 0xC0) == 0 {
		// |00pppppp| - 6-bit length string
		length := int(encoding & 0x3F)
		value := string(data[offset : offset+length])
		return value, offset + length, nil
	} else if (encoding & 0xC0) == 0x40 {
		// |01pppppp|qqqqqqqq| - 14-bit length string
		length := int((encoding&0x3F)<<8 | data[offset])
		offset++
		value := string(data[offset : offset+length])
		return value, offset + length, nil
	} else if (encoding & 0xC0) == 0x80 {
		// |10______ qqqqqqqq rrrrrrrr ssssssss tttttttt| - 32-bit length string
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		value := string(data[offset : offset+length])
		return value, offset + length, nil
	} else if (encoding & 0xF0) == 0xC0 {
		// |1100____| - int16
		val := int16(binary.LittleEndian.Uint16(data[offset : offset+2]))
		return strconv.Itoa(int(val)), offset + 2, nil
	} else if (encoding & 0xF0) == 0xD0 {
		// |1101____| - int32
		val := int32(binary.LittleEndian.Uint32(data[offset : offset+4]))
		return strconv.Itoa(int(val)), offset + 4, nil
	} else if (encoding & 0xF0) == 0xE0 {
		// |1110____| - int64
		val := int64(binary.LittleEndian.Uint64(data[offset : offset+8]))
		return strconv.Itoa(int(val)), offset + 8, nil
	} else if (encoding & 0xFE) == 0xF0 {
		// |11110000| - 3-byte int
		val := int(data[offset]) | int(data[offset+1])<<8 | int(data[offset+2])<<16
		if val&0x800000 != 0 {
			val |= -1 << 24 // sign extension
		}
		return strconv.Itoa(val), offset + 3, nil
	} else if encoding == 0xFE {
		// |11111110| - 1-byte int
		return strconv.Itoa(int(int8(data[offset]))), offset + 1, nil
	} else if (encoding & 0xF0) == 0xF0 {
		// |1111xxxx| - 4-bit int (0-12)
		val := int(encoding & 0x0F)
		return strconv.Itoa(val - 1), offset, nil
	}

	return "", 0, fmt.Errorf("unsupported ziplist encoding: 0x%02X", encoding)
}

// ============ Listpack parsing ============

// parseListpack handles [total_bytes:4][num_elements:2][entries...][lpend:0xFF]
func parseListpack(data []byte) ([]string, error) {
	// Handle special case: empty listpack may be encoded as a single byte
	if len(data) == 0 {
		return []string{}, nil
	}

	// Debug: show hex dump for short listpacks
	if len(data) < 7 {
		hexDump := ""
		for i, b := range data {
			if i > 0 {
				hexDump += " "
			}
			hexDump += fmt.Sprintf("%02X", b)
		}
		return nil, fmt.Errorf("listpack payload too short: %d bytes, hex=[%s], possibly invalid or special encoding", len(data), hexDump)
	}

	// Parse header
	totalBytes := binary.LittleEndian.Uint32(data[0:4])
	numElements := binary.LittleEndian.Uint16(data[4:6])

	if int(totalBytes) != len(data) {
		return nil, fmt.Errorf("listpack length mismatch: expect %d bytes, got %d bytes", totalBytes, len(data))
	}

	// Skip header and process entries
	offset := 6
	var entries []string

	for i := 0; i < int(numElements); i++ {
		if offset >= len(data) {
			return nil, fmt.Errorf("listpack entry %d lacks enough data", i)
		}

		if data[offset] == 0xFF {
			return nil, fmt.Errorf("listpack entry %d encountered unexpected EOF marker", i)
		}

		// Read entry
		entry, entrySize, err := readListpackEntry(data[offset:])
		if err != nil {
			return nil, fmt.Errorf("listpack entry %d: %w", i, err)
		}

		entries = append(entries, entry)
		offset += entrySize
	}

	// Validate trailing EOF marker
	if offset >= len(data) || data[offset] != 0xFF {
		return nil, fmt.Errorf("listpack missing EOF marker")
	}

	return entries, nil
}

// readListpackEntry decodes one listpack entry and returns (value, total size incl. backlen).
func readListpackEntry(data []byte) (string, int, error) {
	if len(data) < 2 {
		return "", 0, fmt.Errorf("not enough data: need at least 2 bytes")
	}

	encoding := data[0]
	var value string
	var dataSize int // encoding + payload size (excluding backlen)

	// Dispatch on encoding
	if (encoding & 0x80) == 0 {
		// 0xxxxxxx - 7-bit unsigned integer (0-127)
		value = strconv.Itoa(int(encoding))
		dataSize = 1
	} else if (encoding & 0xC0) == 0x80 {
		// 10xxxxxx - 6-bit string length (0-63 bytes)
		length := int(encoding & 0x3F)
		if 1+length > len(data) {
			return "", 0, fmt.Errorf("6-bit string lacks enough data: need %d bytes", 1+length)
		}
		value = string(data[1 : 1+length])
		dataSize = 1 + length
	} else if (encoding & 0xE0) == 0xC0 {
		// 110xxxxx - 13-bit signed integer
		if len(data) < 2 {
			return "", 0, fmt.Errorf("13-bit integer lacks enough data")
		}
		uval := uint64((encoding&0x1F)<<8) | uint64(data[1])
		// Convert to signed integer (two's complement)
		if uval >= (1 << 12) {
			uval = (1 << 13) - 1 - uval
			value = strconv.FormatInt(-int64(uval)-1, 10)
		} else {
			value = strconv.FormatUint(uval, 10)
		}
		dataSize = 2
	} else if (encoding & 0xF0) == 0xE0 {
		// 1110xxxx - 12-bit string length (0-4095 bytes)
		if len(data) < 2 {
			return "", 0, fmt.Errorf("12-bit string length field lacks enough data")
		}
		length := int((encoding&0x0F)<<8) | int(data[1])
		if 2+length > len(data) {
			return "", 0, fmt.Errorf("12-bit string lacks enough data: need %d bytes", 2+length)
		}
		value = string(data[2 : 2+length])
		dataSize = 2 + length
	} else if encoding == 0xF0 {
		// 32-bit string length
		if len(data) < 5 {
			return "", 0, fmt.Errorf("32-bit string length field lacks enough data")
		}
		length := int(binary.LittleEndian.Uint32(data[1:5]))
		if 5+length > len(data) {
			return "", 0, fmt.Errorf("32-bit string lacks enough data: need %d bytes", 5+length)
		}
		value = string(data[5 : 5+length])
		dataSize = 5 + length
	} else if encoding == 0xF1 {
		// 16-bit signed integer
		if len(data) < 3 {
			return "", 0, fmt.Errorf("16-bit integer lacks enough data")
		}
		val := int16(binary.LittleEndian.Uint16(data[1:3]))
		value = strconv.Itoa(int(val))
		dataSize = 3
	} else if encoding == 0xF2 {
		// 24-bit signed integer
		if len(data) < 4 {
			return "", 0, fmt.Errorf("24-bit integer lacks enough data")
		}
		uval := uint64(data[1]) | uint64(data[2])<<8 | uint64(data[3])<<16
		// Convert to signed integer
		if uval >= (1 << 23) {
			uval = (1 << 24) - 1 - uval
			value = strconv.FormatInt(-int64(uval)-1, 10)
		} else {
			value = strconv.FormatUint(uval, 10)
		}
		dataSize = 4
	} else if encoding == 0xF3 {
		// 32-bit signed integer
		if len(data) < 5 {
			return "", 0, fmt.Errorf("32-bit integer lacks enough data")
		}
		val := int32(binary.LittleEndian.Uint32(data[1:5]))
		value = strconv.Itoa(int(val))
		dataSize = 5
	} else if encoding == 0xF4 {
		// 64-bit signed integer
		if len(data) < 9 {
			return "", 0, fmt.Errorf("64-bit integer lacks enough data")
		}
		val := int64(binary.LittleEndian.Uint64(data[1:9]))
		value = strconv.FormatInt(val, 10)
		dataSize = 9
	} else {
		return "", 0, fmt.Errorf("unsupported listpack encoding: 0x%02X", encoding)
	}

	// Calculate backlen size
	backlenSize := lpEncodeBacklenSize(dataSize)
	totalSize := dataSize + backlenSize

	if totalSize > len(data) {
		return "", 0, fmt.Errorf("entry total size exceeds available data: need %d bytes, have %d bytes", totalSize, len(data))
	}

	return value, totalSize, nil
}

// lpEncodeBacklenSize follows the Dragonfly/Redis listpack.c lpEncodeBacklen() rule
func lpEncodeBacklenSize(l int) int {
	if l <= 127 {
		return 1
	} else if l < 16383 {
		return 2
	} else if l < 2097151 {
		return 3
	} else if l < 268435455 {
		return 4
	}
	return 5
}

// ============ Intset parsing ============

// parseIntset decodes [encoding:4][length:4][contents...]
func parseIntset(data []byte) ([]string, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("intset payload too short")
	}

	encoding := binary.LittleEndian.Uint32(data[0:4])
	length := binary.LittleEndian.Uint32(data[4:8])

	var members []string
	offset := 8

	for i := uint32(0); i < length; i++ {
		var val int64
		switch encoding {
		case 2: // INTSET_ENC_INT16
			val = int64(int16(binary.LittleEndian.Uint16(data[offset : offset+2])))
			offset += 2
		case 4: // INTSET_ENC_INT32
			val = int64(int32(binary.LittleEndian.Uint32(data[offset : offset+4])))
			offset += 4
		case 8: // INTSET_ENC_INT64
			val = int64(binary.LittleEndian.Uint64(data[offset : offset+8]))
			offset += 8
		default:
			return nil, fmt.Errorf("unsupported intset encoding: %d", encoding)
		}
		members = append(members, strconv.FormatInt(val, 10))
	}

	return members, nil
}
// ============ Stream parsing ============

// parseStream handles stream types (RDB_TYPE_STREAM_LISTPACKS = 15, 19, 21)
// Format: length + last_id + listpacks + consumer_groups
func (p *RDBParser) parseStream(typeByte byte) (*StreamValue, error) {
	log.Printf("  [FLOW-%d] [STREAM-PARSE] Starting to parse stream key '%s', type=0x%02X",
		p.flowID, truncateKey(p.lastKeyName, 50), typeByte)

	// CRITICAL FIX: Dragonfly saves listpacks FIRST, not stream length!
	// Correct order: listpacks count → listpack nodes → stream length → last_id

	// 1. Read number of listpacks
	numListpacks, _, err := p.readLength()
	if err != nil {
		return nil, fmt.Errorf("failed to read num listpacks: %w", err)
	}
	log.Printf("  [FLOW-%d] [STREAM-PARSE] Number of listpacks: %d", p.flowID, numListpacks)

	var messages []StreamMessage

	// 2. Parse each listpack node
	for i := uint64(0); i < numListpacks; i++ {
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Processing listpack %d/%d", p.flowID, i+1, numListpacks)

		// Each listpack node: Stream ID (key) + listpack data (value)
		// Read the master entry ID (used as radix tree key)
		streamIDKey := p.readString()
		if len(streamIDKey) != 16 {
			return nil, fmt.Errorf("stream node key is not 16 bytes: got %d bytes", len(streamIDKey))
		}
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Listpack %d key size: %d bytes", p.flowID, i+1, len(streamIDKey))

		// Read listpack data
		listpackBytes := p.readString()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Listpack %d data size: %d bytes", p.flowID, i+1, len(listpackBytes))

		if len(listpackBytes) == 0 {
			return nil, fmt.Errorf("listpack %d is empty", i+1)
		}

		entries, err := parseListpack([]byte(listpackBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to parse listpack %d: %w", i+1, err)
		}
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Listpack %d parsed: %d entries", p.flowID, i+1, len(entries))

		// Extract master ID (ms and seq) from the 16-byte streamIDKey (big endian)
		var masterMs, masterSeq uint64
		if len(streamIDKey) == 16 {
			// Parse as big endian
			masterMs = binary.BigEndian.Uint64([]byte(streamIDKey[0:8]))
			masterSeq = binary.BigEndian.Uint64([]byte(streamIDKey[8:16]))
			log.Printf("  [FLOW-%d] [STREAM-PARSE] Listpack %d master ID: %d-%d", p.flowID, i+1, masterMs, masterSeq)
		}

		// Parse listpack entries into stream messages
		// Listpack format: count + master_fields + [flags + ms_diff + seq_diff + fields...]
		if len(entries) < 1 {
			continue
		}

		// First entry is the count
		idx := 1

		// Next entries are master fields (field names that apply to all messages in this listpack)
		var masterFields []string
		if idx < len(entries) {
			numMasterFields, _ := strconv.Atoi(entries[idx])
			idx++
			for j := 0; j < numMasterFields && idx < len(entries); j++ {
				masterFields = append(masterFields, entries[idx])
				idx++
			}
		}

		// Parse messages
		currentMs := masterMs
		currentSeq := masterSeq
		
		for idx < len(entries) {
			// Read flags (indicates how ID is stored)
			if idx >= len(entries) {
				break
			}
			flags, _ := strconv.Atoi(entries[idx])
			idx++

			// Read ID (ms and seq)
			if (flags & 0x01) == 0 {
				// ms is delta from previous
				if idx >= len(entries) {
					break
				}
				msDelta, _ := strconv.ParseUint(entries[idx], 10, 64)
				currentMs += msDelta
				idx++
			}
			
			if (flags & 0x02) == 0 {
				// seq is delta from previous
				if idx >= len(entries) {
					break
				}
				seqDelta, _ := strconv.ParseUint(entries[idx], 10, 64)
				currentSeq = seqDelta
				idx++
			} else {
				currentSeq++
			}

			messageID := fmt.Sprintf("%d-%d", currentMs, currentSeq)
			fields := make(map[string]string)

			// Read number of fields for this message
			if idx >= len(entries) {
				break
			}
			numFields, _ := strconv.Atoi(entries[idx])
			idx++

			// If numFields is negative, use master fields
			if numFields < 0 {
				numFields = -numFields
				// Use master field names
				for j := 0; j < numFields && j < len(masterFields) && idx < len(entries); j++ {
					fields[masterFields[j]] = entries[idx]
					idx++
				}
			} else {
				// Read field-value pairs
				for j := 0; j < numFields*2 && idx+1 < len(entries); j += 2 {
					fields[entries[idx]] = entries[idx+1]
					idx += 2
				}
			}

			if len(fields) > 0 {
				messages = append(messages, StreamMessage{
					ID:     messageID,
					Fields: fields,
				})
			}

			// Check if we should stop (lpend or out of data)
			if idx >= len(entries) {
				break
			}
		}
	}

	log.Printf("  [FLOW-%d] [STREAM-PARSE] Parsed %d messages from listpacks", p.flowID, len(messages))

	// 3. Read stream metadata (AFTER all listpacks)
	// Read stream length (total number of entries)
	length, _, err := p.readLength()
	if err != nil {
		return nil, fmt.Errorf("failed to read stream length: %w", err)
	}
	log.Printf("  [FLOW-%d] [STREAM-PARSE] Stream length: %d entries", p.flowID, length)

	// 4. Read last stream ID
	lastIDMs, _, err := p.readLength()
	if err != nil {
		return nil, fmt.Errorf("failed to read last ID ms: %w", err)
	}
	lastIDSeq, _, err := p.readLength()
	if err != nil {
		return nil, fmt.Errorf("failed to read last ID seq: %w", err)
	}
	lastID := fmt.Sprintf("%d-%d", lastIDMs, lastIDSeq)
	log.Printf("  [FLOW-%d] [STREAM-PARSE] Last ID: %s", p.flowID, lastID)

	// 5. Read V2+ fields (if applicable)
	if typeByte >= RDB_TYPE_STREAM_LISTPACKS_2 {
		// First ID
		firstIDMs, _, _ := p.readLength()
		firstIDSeq, _, _ := p.readLength()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] First ID: %d-%d", p.flowID, firstIDMs, firstIDSeq)

		// Max deleted entry ID
		maxDelMs, _, _ := p.readLength()
		maxDelSeq, _, _ := p.readLength()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Max deleted ID: %d-%d", p.flowID, maxDelMs, maxDelSeq)

		// Entries added
		entriesAdded, _, _ := p.readLength()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Entries added: %d", p.flowID, entriesAdded)
	}

	// 6. Read number of consumer groups
	numGroups, _, err := p.readLength()
	if err != nil {
		return nil, fmt.Errorf("failed to read num consumer groups: %w", err)
	}
	log.Printf("  [FLOW-%d] [STREAM-PARSE] Number of consumer groups: %d", p.flowID, numGroups)

	// Skip consumer group data
	for i := uint64(0); i < numGroups; i++ {
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Processing consumer group %d/%d", p.flowID, i+1, numGroups)

		// Group name
		groupName := p.readString()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d name: '%s'", p.flowID, i+1, groupName)

		// Last delivered ID (ms + seq)
		p.readLength()
		p.readLength()

		// V2+ fields
		if typeByte >= RDB_TYPE_STREAM_LISTPACKS_2 {
			// Entries read
			entriesRead, _, _ := p.readLength()
			log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d entries_read: %d", p.flowID, i+1, entriesRead)
		}

		// Global PEL (pending entry list)
		pelSize, _, _ := p.readLength()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d PEL size: %d", p.flowID, i+1, pelSize)

		for j := uint64(0); j < pelSize; j++ {
			// CRITICAL FIX: Stream ID is 16 bytes (sizeof(streamID)), not 8!
			// Read as raw bytes (big endian format)
			streamIDBytes := make([]byte, 16)
			for k := 0; k < 16; k++ {
				b, err := p.readByte()
				if err != nil {
					return nil, fmt.Errorf("failed to read PEL stream ID byte %d: %w", k, err)
				}
				streamIDBytes[k] = b
			}

			// Delivery time (8 bytes, little endian)
			deliveryTime, err := p.readInt64()
			if err != nil {
				return nil, fmt.Errorf("failed to read PEL delivery time: %w", err)
			}

			// Delivery count (length encoded)
			deliveryCount, _, err := p.readLength()
			if err != nil {
				return nil, fmt.Errorf("failed to read PEL delivery count: %w", err)
			}

			if j < 3 { // Log first 3 entries only
				log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d PEL[%d]: delivery_time=%d, count=%d",
					p.flowID, i+1, j, deliveryTime, deliveryCount)
			}
		}

		// Consumers
		numConsumers, _, _ := p.readLength()
		log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d has %d consumers", p.flowID, i+1, numConsumers)

		for j := uint64(0); j < numConsumers; j++ {
			// Consumer name
			consumerName := p.readString()
			log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d consumer %d: '%s'", p.flowID, i+1, j+1, consumerName)

			// Seen time (8 bytes, little endian)
			seenTime, err := p.readInt64()
			if err != nil {
				return nil, fmt.Errorf("failed to read consumer seen_time: %w", err)
			}
			log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d consumer %d seen_time: %d", p.flowID, i+1, j+1, seenTime)

			// Active time (V3+ only)
			if typeByte >= RDB_TYPE_STREAM_LISTPACKS_3 {
				activeTime, err := p.readInt64()
				if err != nil {
					return nil, fmt.Errorf("failed to read consumer active_time: %w", err)
				}
				log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d consumer %d active_time: %d", p.flowID, i+1, j+1, activeTime)
			}

			// Consumer PEL
			consumerPEL, _, _ := p.readLength()
			log.Printf("  [FLOW-%d] [STREAM-PARSE] Group %d consumer %d PEL size: %d", p.flowID, i+1, j+1, consumerPEL)

			for k := uint64(0); k < consumerPEL; k++ {
				// CRITICAL FIX: Consumer PEL only has Stream ID (16 bytes)
				// NO delivery_time, NO delivery_count!
				streamIDBytes := make([]byte, 16)
				for m := 0; m < 16; m++ {
					b, err := p.readByte()
					if err != nil {
						return nil, fmt.Errorf("failed to read consumer PEL stream ID byte %d: %w", m, err)
					}
					streamIDBytes[m] = b
				}
			}
		}
	}

	log.Printf("  [FLOW-%d] [STREAM-PARSE] ✓ Successfully parsed stream key '%s'", p.flowID, truncateKey(p.lastKeyName, 50))

	return &StreamValue{
		Messages: messages,
		Length:   length,
		LastID:   lastID,
	}, nil
}
