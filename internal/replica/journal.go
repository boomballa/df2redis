package replica

import (
	"fmt"
	"io"
	"strings"
)

// JournalOpcode enumerates Dragonfly journal opcodes
type JournalOpcode uint8

const (
	OpNoop    JournalOpcode = 0  // NOOP
	OpSelect  JournalOpcode = 6  // SELECT database
	OpExpired JournalOpcode = 9  // expired key
	OpCommand JournalOpcode = 10 // regular command
	OpPing    JournalOpcode = 13 // heartbeat
	OpLSN     JournalOpcode = 15 // LSN marker
	OpFin     JournalOpcode = 99 // synthetic end-of-stream marker
)

func (op JournalOpcode) String() string {
	switch op {
	case OpNoop:
		return "NOOP"
	case OpSelect:
		return "SELECT"
	case OpCommand:
		return "COMMAND"
	case OpExpired:
		return "EXPIRED"
	case OpLSN:
		return "LSN"
	case OpPing:
		return "PING"
	case OpFin:
		return "FIN"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", op)
	}
}

// JournalEntry captures a parsed journal record
type JournalEntry struct {
	Opcode   JournalOpcode
	DbIndex  uint64   // database index for SELECT
	TxID     uint64   // transaction ID
	ShardCnt uint64   // shard count
	LSN      uint64   // log sequence number
	Command  string   // command name
	Args     []string // command arguments
	RawData  []byte   // raw payload (for debugging)
}

// String pretty-prints the entry
func (e *JournalEntry) String() string {
	switch e.Opcode {
	case OpSelect:
		return fmt.Sprintf("SELECT DB=%d", e.DbIndex)
	case OpLSN:
		return fmt.Sprintf("LSN %d", e.LSN)
	case OpPing:
		return "PING"
	case OpCommand, OpExpired:
		args := make([]string, len(e.Args))
		for i, arg := range e.Args {
			if len(arg) > 50 {
				args[i] = fmt.Sprintf("\"%s...\"", arg[:50])
			} else {
				args[i] = fmt.Sprintf("\"%s\"", arg)
			}
		}
		return fmt.Sprintf("%s txid=%d cmd=%s args=[%s]",
			e.Opcode, e.TxID, e.Command, strings.Join(args, ", "))
	case OpFin:
		return "FIN (stream ended)"
	default:
		return fmt.Sprintf("%s (raw_len=%d)", e.Opcode, len(e.RawData))
	}
}

// JournalReader decodes journal frames
type JournalReader struct {
	reader io.Reader
}

// NewJournalReader builds a reader over an io.Reader
func NewJournalReader(r io.Reader) *JournalReader {
	return &JournalReader{reader: r}
}

// ReadEntry parses the next entry
func (jr *JournalReader) ReadEntry() (*JournalEntry, error) {
	entry := &JournalEntry{}

	// 1. Opcode (1 byte)
	opcodeBuf := make([]byte, 1)
	if _, err := io.ReadFull(jr.reader, opcodeBuf); err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read opcode: %w", err)
	}
	entry.Opcode = JournalOpcode(opcodeBuf[0])

	// 2. Dispatch per opcode
	switch entry.Opcode {
	case OpSelect:
		// SELECT: only read dbid
		dbid, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read SELECT dbid: %w", err)
		}
		entry.DbIndex = dbid
		return entry, nil

	case OpLSN:
		// LSN marker
		lsn, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read LSN: %w", err)
		}
		entry.LSN = lsn
		return entry, nil

	case OpPing:
		// PING carries no payload
		return entry, nil

	case OpCommand, OpExpired:
		// COMMAND/EXPIRED: txid + shard count + payload
		txid, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read txid: %w", err)
		}
		entry.TxID = txid

		shardCnt, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read shard_cnt: %w", err)
		}
		entry.ShardCnt = shardCnt

		// Parse payload
		if err := jr.readPayload(entry); err != nil {
			return nil, fmt.Errorf("failed to read payload: %w", err)
		}

		return entry, nil

	default:
		return nil, fmt.Errorf("unknown opcode: %d", entry.Opcode)
	}
}

// readPayload parses the payload layout:
//   - number of elements (Packed Uint) = 1 + args
//   - total command size (Packed Uint)
//   - command name (Packed String)
//   - arguments (Packed String × n)
func (jr *JournalReader) readPayload(entry *JournalEntry) error {
	// 1. Element count (command + args)
	numElems, err := ReadPackedUint(jr.reader)
	if err != nil {
		return fmt.Errorf("failed to read argument count: %w", err)
	}

	if numElems == 0 {
		// Empty payload
		return nil
	}

	// 2. Total command size (unused placeholder for potential validation)
	totalSize, err := ReadPackedUint(jr.reader)
	if err != nil {
		return fmt.Errorf("failed to read total command size: %w", err)
	}
	_ = totalSize

	// 3. Command name
	cmd, err := ReadPackedString(jr.reader)
	if err != nil {
		return fmt.Errorf("failed to read command name: %w", err)
	}
	entry.Command = cmd

	// 4. Arguments (numElems - 1)
	if numElems > 1 {
		entry.Args = make([]string, numElems-1)
		for i := 0; i < int(numElems-1); i++ {
			arg, err := ReadPackedString(jr.reader)
			if err != nil {
				return fmt.Errorf("failed to read argument[%d]: %w", i, err)
			}
			entry.Args[i] = arg
		}
	}

	return nil
}
