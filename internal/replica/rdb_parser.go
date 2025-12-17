package replica

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// RDBParser streams and decodes RDB payloads
type RDBParser struct {
	reader         *bufio.Reader // current active reader
	originalReader *bufio.Reader // original network stream
	flowID         int

	// State tracked during parsing
	currentDB       int   // current database index
	expireMs        int64 // current key expiration (absolute ms timestamp)
	lz4BlobCount    int   // number of LZ4 blobs processed
	zstdBlobCount   int   // number of ZSTD blobs processed
	journalBlobCount int  // number of journal blobs processed

	// Callback for applying inline journal entries during RDB phase
	onJournalEntry func(*JournalEntry) error
}

// NewRDBParser creates a parser bound to a reader
func NewRDBParser(reader io.Reader, flowID int) *RDBParser {
	bufReader := bufio.NewReader(reader)
	return &RDBParser{
		reader:         bufReader,
		originalReader: bufReader,
		flowID:         flowID,
		currentDB:      0,
		expireMs:       0,
	}
}

// ParseHeader validates the RDB header ("REDIS0009" + AUX fields)
func (p *RDBParser) ParseHeader() error {
	// 1. Read magic header "REDIS0009"
	magic := make([]byte, 9)
	if _, err := io.ReadFull(p.reader, magic); err != nil {
		return fmt.Errorf("failed to read RDB magic: %w", err)
	}

	// Verify magic string
	expectedMagic := "REDIS0009"
	if string(magic) != expectedMagic {
		return fmt.Errorf("invalid RDB magic: expect %s, got %s", expectedMagic, string(magic))
	}

	// 2. Skip AUX fields (0xFA + key + value) until we hit a non-0xFA opcode
	for {
		opcode, err := p.peekByte()
		if err != nil {
			return fmt.Errorf("failed to read opcode: %w", err)
		}

		if opcode != RDB_OPCODE_AUX {
			break
		}

		// Consume 0xFA
		p.readByte()

		// Discard AUX key/value
		_ = p.readString() // key
		_ = p.readString() // value
	}

	return nil
}

// ParseNext reads the next RDB entry. Returns (nil, io.EOF) when the stream ends.
func (p *RDBParser) ParseNext() (*RDBEntry, error) {
	for {
		opcode, err := p.readByte()
		if err != nil {
			return nil, err
		}

		// Debug: Log every opcode encountered (helps diagnose missing JOURNAL_BLOB)
		if opcode >= 0xC8 { // Log only special opcodes (not regular type codes)
			log.Printf("  [FLOW-%d] [DEBUG] Opcode encountered: 0x%02X", p.flowID, opcode)
		}

		switch opcode {
		case RDB_OPCODE_EXPIRETIME_MS:
			// TTL encoded as 8-byte little-endian milliseconds
			p.expireMs, err = p.readInt64()
			if err != nil {
				return nil, fmt.Errorf("failed to read expiration time: %w", err)
			}
			continue

		case RDB_OPCODE_EXPIRETIME:
			// TTL encoded as 4-byte little-endian seconds
			expireSec, err := p.readInt32()
			if err != nil {
				return nil, fmt.Errorf("failed to read expiration time: %w", err)
			}
			p.expireMs = int64(expireSec) * 1000
			continue

		case RDB_OPCODE_SELECTDB:
			// Switch database
			dbIndex, _, err := p.readLength()
			if err != nil {
				return nil, fmt.Errorf("failed to read db index: %w", err)
			}
			p.currentDB = int(dbIndex)
			continue

		case RDB_OPCODE_JOURNAL_BLOB:
			// Dragonfly inline journal entry during RDB streaming
			// Format per Dragonfly source: [0xD2][num_entries: packed_uint][journal_blob: RDB string]
			// These entries represent writes that occurred during RDB transmission
			// and must be applied inline to maintain consistency
			p.journalBlobCount++
			blobNum := p.journalBlobCount

			// Read number of entries (packed uint)
			numEntries, _, err := p.readLength()
			if err != nil {
				return nil, fmt.Errorf("failed to read JOURNAL_BLOB #%d num_entries: %w", blobNum, err)
			}

			// Read journal blob as RDB string
			journalBlob := p.readString()
			if len(journalBlob) == 0 {
				log.Printf("  [FLOW-%d] [JOURNAL-BLOB #%d] Empty blob (num_entries=%d)", p.flowID, blobNum, numEntries)
				continue
			}

			log.Printf("  [FLOW-%d] [JOURNAL-BLOB #%d] Processing %d entries (%d bytes)", p.flowID, blobNum, numEntries, len(journalBlob))

			// Parse and apply journal entries
			if err := p.processJournalBlob(journalBlob, numEntries); err != nil {
				return nil, fmt.Errorf("failed to process JOURNAL_BLOB #%d: %w", blobNum, err)
			}

			// Continue to the next opcode
			continue

		case RDB_OPCODE_JOURNAL_OFFSET:
			// Dragonfly-specific JOURNAL_OFFSET marker, discard 8-byte offset
			offset := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, offset); err != nil {
				return nil, fmt.Errorf("failed to read JOURNAL_OFFSET: %w", err)
			}
			// Continue to the next opcode
			continue

		case RDB_OPCODE_FULLSYNC_END:
			// Dragonfly FULLSYNC_END marker, followed by eight zero bytes
			zeros := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, zeros); err != nil {
				return nil, fmt.Errorf("failed to read FULLSYNC_END suffix: %w", err)
			}

			// Signal the caller that full sync is complete so it can verify EOF tokens, etc.
			return nil, io.EOF

		case RDB_OPCODE_EOF:
			// RDB terminator; drop 8-byte checksum
			checksum := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, checksum); err != nil {
				return nil, fmt.Errorf("failed to read EOF checksum: %w", err)
			}
			return nil, io.EOF

		case RDB_OPCODE_AUX:
			// Read AUX key
			auxKey, err := p.readStringFull()
			if err != nil {
				return nil, fmt.Errorf("failed to read AUX key: %w", err)
			}
			// Read AUX value
			auxValue, err := p.readStringFull()
			if err != nil {
				return nil, fmt.Errorf("failed to read AUX value for key '%s': %w", auxKey, err)
			}
			log.Printf("  [FLOW-%d] AUX: %s = %s", p.flowID, auxKey, auxValue)
			continue

	case RDB_OPCODE_COMPRESSED_ZSTD_BLOB_START:
		// Dragonfly ZSTD compressed blob start
		if err := p.handleZstdBlob(); err != nil {
			return nil, fmt.Errorf("failed to handle ZSTD compressed blob: %w", err)
		}
		continue

		case RDB_OPCODE_COMPRESSED_LZ4_BLOB_START:
			// Dragonfly LZ4 compressed blob start
			if err := p.handleLZ4Blob(); err != nil {
				return nil, fmt.Errorf("failed to handle LZ4 compressed blob: %w", err)
			}
			continue

		case RDB_OPCODE_COMPRESSED_BLOB_END:
			// Compressed blob end, switch back to original stream
			if err := p.handleLZ4BlobEnd(); err != nil {
				return nil, fmt.Errorf("failed to handle compressed blob end: %w", err)
			}
			continue

		default:
			// Data type opcode; parse the key/value pair
			// Check if this looks like a valid RDB type (< 30) or a misaligned read
			if opcode >= 30 && opcode < 0xC0 {
				log.Printf("  [FLOW-%d] ⚠ Warning: opcode 0x%02X (%d) is unusually high for an RDB type, possible stream corruption", p.flowID, opcode, opcode)
			}
			return p.parseKeyValue(opcode)
		}
	}
}

// parseKeyValue decodes one key/value pair
func (p *RDBParser) parseKeyValue(typeByte byte) (*RDBEntry, error) {
	// 1. Read key
	key := p.readString()

	entry := &RDBEntry{
		Key:      key,
		Type:     typeByte,
		DbIndex:  p.currentDB,
		ExpireMs: p.expireMs,
	}

	// 2. Parse value based on encoding
	var err error
	switch typeByte {
	case RDB_TYPE_STRING:
		entry.Value, err = p.parseString()

	case RDB_TYPE_HASH, RDB_TYPE_HASH_ZIPLIST, RDB_TYPE_HASH_LISTPACK:
		entry.Value, err = p.parseHash(typeByte)

	case RDB_TYPE_LIST_QUICKLIST, RDB_TYPE_LIST_QUICKLIST_2:
		entry.Value, err = p.parseList(typeByte)

	case RDB_TYPE_SET, RDB_TYPE_SET_INTSET, RDB_TYPE_SET_LISTPACK:
		entry.Value, err = p.parseSet(typeByte)

	case RDB_TYPE_ZSET_2, RDB_TYPE_ZSET_ZIPLIST, RDB_TYPE_ZSET_LISTPACK:
		entry.Value, err = p.parseZSet(typeByte)

	case RDB_TYPE_STREAM_LISTPACKS, RDB_TYPE_STREAM_LISTPACKS_2, RDB_TYPE_STREAM_LISTPACKS_3:
		entry.Value, err = p.parseStream(typeByte)

	default:
		// Unknown module/stream etc.
		return nil, fmt.Errorf("unsupported RDB type: %d (key=%s)", typeByte, key)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse value (type=%d, key=%s): %w", typeByte, key, err)
	}

	// Reset expiration tracking
	p.expireMs = 0

	return entry, nil
}

// parseString handles raw string values
func (p *RDBParser) parseString() (*StringValue, error) {
	value := p.readString()
	return &StringValue{Value: value}, nil
}

// ============ Primitive readers ============

// readByte reads a single byte
func (p *RDBParser) readByte() (byte, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// peekByte peeks at the next byte without consuming it
func (p *RDBParser) peekByte() (byte, error) {
	// Use bufio.Reader.Peek(1)
	buf, err := p.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

// readInt32 reads a little-endian int32
func (p *RDBParser) readInt32() (int32, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(buf)), nil
}

// readInt64 reads a little-endian int64
func (p *RDBParser) readInt64() (int64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf)), nil
}

// readPackedUint reads a Dragonfly packed uint (variable-length encoding)
// Format: if byte < 0x80, value = byte; else continue reading
func (p *RDBParser) readPackedUint() (uint64, error) {
	var result uint64
	var shift uint
	for {
		b, err := p.readByte()
		if err != nil {
			return 0, err
		}
		// Add lower 7 bits to result
		result |= uint64(b&0x7F) << shift
		// If high bit is not set, we're done
		if b < 0x80 {
			break
		}
		shift += 7
	}
	return result, nil
}

// readLength parses the RDB length encoding.
// Returns (length, isSpecial, error) where isSpecial denotes integer/LZF encodings.
func (p *RDBParser) readLength() (uint64, bool, error) {
	firstByte, err := p.readByte()
	if err != nil {
		return 0, false, err
	}

	// Top two bits denote encoding scheme
	typeField := (firstByte >> 6) & 0x03

	switch typeField {
	case 0:
		// 00|XXXXXX - 6-bit length
		return uint64(firstByte & 0x3F), false, nil

	case 1:
		// 01|XXXXXX XXXXXXXX - 14-bit length
		nextByte, err := p.readByte()
		if err != nil {
			return 0, false, err
		}
		length := (uint64(firstByte&0x3F) << 8) | uint64(nextByte)
		return length, false, nil

	case 2:
		// 10|XXXXXX - special encoding or 32/64-bit length
		if firstByte == 0x80 {
			// 32-bit length
			buf := make([]byte, 4)
			if _, err := io.ReadFull(p.reader, buf); err != nil {
				return 0, false, err
			}
			return uint64(binary.BigEndian.Uint32(buf)), false, nil
		} else if firstByte == 0x81 {
			// 64-bit length
			buf := make([]byte, 8)
			if _, err := io.ReadFull(p.reader, buf); err != nil {
				return 0, false, err
			}
			return binary.BigEndian.Uint64(buf), false, nil
		}
		// Otherwise treat as special encoding
		return uint64(firstByte & 0x3F), true, nil

	case 3:
		// 11|XXXXXX - special encoding
		return uint64(firstByte & 0x3F), true, nil
	}

	return 0, false, fmt.Errorf("invalid length encoding type: %d", typeField)
}

// handleZstdBlob handles ZSTD compressed blob (opcode 0xC9)
func (p *RDBParser) handleZstdBlob() error {
	// Read compressed data as a length-prefixed string
	compressedData, err := p.readStringFull()
	if err != nil {
		return fmt.Errorf("failed to read ZSTD compressed data: %w", err)
	}

	// Decompress using ZSTD
	// Dragonfly uses ZSTD frame format with embedded metadata
	decoder, err := zstd.NewReader(bytes.NewReader([]byte(compressedData)))
	if err != nil {
		return fmt.Errorf("failed to create ZSTD decoder: %w", err)
	}
	defer decoder.Close()

	decompressed, err := io.ReadAll(decoder)
	if err != nil {
		return fmt.Errorf("ZSTD decompression failed: %w", err)
	}

	// Append RDB_OPCODE_COMPRESSED_BLOB_END (0xCB) to the decompressed data
	// This matches Dragonfly's behavior: decompress.cc adds this opcode to membuf
	decompressedWithEnd := make([]byte, len(decompressed)+1)
	copy(decompressedWithEnd, decompressed)
	decompressedWithEnd[len(decompressed)] = RDB_OPCODE_COMPRESSED_BLOB_END

	// Switch to reading from decompressed buffer (including the end marker)
	p.reader = bufio.NewReader(bytes.NewReader(decompressedWithEnd))

	return nil
}

// handleLZ4Blob handles LZ4 compressed blob (opcode 0xCA)
func (p *RDBParser) handleLZ4Blob() error {
	p.lz4BlobCount++
	blobNum := p.lz4BlobCount

	// Read compressed data as a length-prefixed string
	compressedData, err := p.readStringFull()
	if err != nil {
		log.Printf("  [FLOW-%d] ✗ LZ4 blob #%d: failed to read compressed data: %v", p.flowID, blobNum, err)
		return fmt.Errorf("failed to read compressed data (blob #%d): %w", blobNum, err)
	}
	log.Printf("  [FLOW-%d] → LZ4 blob #%d: read %d bytes of compressed data", p.flowID, blobNum, len(compressedData))

	// Decompress using LZ4 Frame format (not Block format)
	// Dragonfly uses LZ4F_compressFrame which produces Frame format with embedded metadata
	decompressStart := time.Now()
	reader := lz4.NewReader(bytes.NewReader([]byte(compressedData)))
	decompressed, err := io.ReadAll(reader)
	decompressDuration := time.Since(decompressStart)
	if err != nil {
		return fmt.Errorf("LZ4 Frame decompression failed: %w", err)
	}

	// Log slow decompressions (>1 second)
	if decompressDuration > time.Second {
		log.Printf("  [FLOW-%d] ⚠ LZ4 blob #%d: decompression took %v (compressed: %d bytes → uncompressed: %d bytes)",
			p.flowID, blobNum, decompressDuration, len(compressedData), len(decompressed))
	}

	// Append RDB_OPCODE_COMPRESSED_BLOB_END (0xCB) to the decompressed data
	// This matches Dragonfly's behavior: decompress.cc adds this opcode to membuf
	decompressedWithEnd := make([]byte, len(decompressed)+1)
	copy(decompressedWithEnd, decompressed)
	decompressedWithEnd[len(decompressed)] = RDB_OPCODE_COMPRESSED_BLOB_END

	// Switch to reading from decompressed buffer (including the end marker)
	p.reader = bufio.NewReader(bytes.NewReader(decompressedWithEnd))

	return nil
}

// handleLZ4BlobEnd handles compressed blob end marker (opcode 0xCB)
func (p *RDBParser) handleLZ4BlobEnd() error {
	// Switch back to original network stream
	p.reader = p.originalReader
	return nil
}

// readString reads an RDB string by delegating to rdb_string.go
func (p *RDBParser) readString() string {
	str, err := p.readStringFull()
	if err != nil {
		// Simplified handling; parseKeyValue will surface the actual error
		return ""
	}
	return str
}

// getCurrentTimeMillis returns the current timestamp in milliseconds
func getCurrentTimeMillis() int64 {
	return time.Now().UnixMilli()
}

// processJournalBlob parses and applies inline journal entries
func (p *RDBParser) processJournalBlob(blobData string, numEntries uint64) error {
	// Create a journal reader from the blob data
	blobReader := bytes.NewReader([]byte(blobData))
	journalReader := NewJournalReader(blobReader)

	// Parse and process each entry
	var processed uint64
	for processed < numEntries {
		entry, err := journalReader.ReadEntry()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to parse journal entry %d/%d: %w", processed+1, numEntries, err)
		}

		// Apply the entry via callback if provided
		if p.onJournalEntry != nil {
			// Set current database for the entry
			if entry.Opcode == OpSelect {
				p.currentDB = int(entry.DbIndex)
				log.Printf("  [FLOW-%d] [INLINE-JOURNAL] SELECT DB %d", p.flowID, entry.DbIndex)
			} else if entry.Opcode == OpCommand || entry.Opcode == OpExpired {
				// Apply the command (detailed logging happens inside replayCommand)
				if err := p.onJournalEntry(entry); err != nil {
					// Error log already printed by replayCommand
					return fmt.Errorf("failed to apply inline journal entry: %w", err)
				}
				// Success log already printed by replayCommand, no need to duplicate
			} else if entry.Opcode == OpLSN {
				log.Printf("  [FLOW-%d] [INLINE-JOURNAL] LSN update: %d", p.flowID, entry.LSN)
			} else if entry.Opcode == OpPing {
				log.Printf("  [FLOW-%d] [INLINE-JOURNAL] PING", p.flowID)
			}
		} else {
			// No callback - this shouldn't happen but log it
			log.Printf("  [FLOW-%d] [INLINE-JOURNAL] ⚠ No callback registered, skipping entry", p.flowID)
		}

		processed++
	}

	if processed != numEntries {
		log.Printf("  [FLOW-%d] ⚠ JOURNAL_BLOB: expected %d entries, parsed %d", p.flowID, numEntries, processed)
	} else {
		log.Printf("  [FLOW-%d] Processed %d inline journal entries", p.flowID, processed)
	}

	return nil
}
