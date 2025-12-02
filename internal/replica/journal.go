package replica

import (
	"fmt"
	"io"
	"strings"
)

// JournalOpcode 表示 Journal 操作码
type JournalOpcode uint8

const (
	OpNoop    JournalOpcode = 0  // NOOP
	OpSelect  JournalOpcode = 6  // SELECT 数据库
	OpExpired JournalOpcode = 9  // 过期键
	OpCommand JournalOpcode = 10 // 普通命令
	OpPing    JournalOpcode = 13 // PING 心跳
	OpLSN     JournalOpcode = 15 // LSN 标记
	OpFin     JournalOpcode = 99 // 流结束标记（自定义）
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

// JournalEntry 表示一条 Journal 条目
type JournalEntry struct {
	Opcode    JournalOpcode
	DbIndex   uint64   // SELECT 操作时的数据库索引
	TxID      uint64   // 事务 ID
	ShardCnt  uint64   // Shard 计数
	LSN       uint64   // 日志序列号
	Command   string   // 命令名
	Args      []string // 命令参数
	RawData   []byte   // 原始数据（用于调试）
}

// String 返回 Entry 的可读表示
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

// JournalReader 读取并解析 Journal 流
type JournalReader struct {
	reader io.Reader
}

// NewJournalReader 创建 Journal 读取器
func NewJournalReader(r io.Reader) *JournalReader {
	return &JournalReader{reader: r}
}

// ReadEntry 读取一条 Journal Entry
func (jr *JournalReader) ReadEntry() (*JournalEntry, error) {
	entry := &JournalEntry{}

	// 1. 读取 Opcode (1字节)
	opcodeBuf := make([]byte, 1)
	if _, err := io.ReadFull(jr.reader, opcodeBuf); err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("读取 opcode 失败: %w", err)
	}
	entry.Opcode = JournalOpcode(opcodeBuf[0])

	// 2. 根据 Opcode 读取不同的字段
	switch entry.Opcode {
	case OpSelect:
		// SELECT: 仅读取 dbid
		dbid, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("读取 SELECT dbid 失败: %w", err)
		}
		entry.DbIndex = dbid
		return entry, nil

	case OpLSN:
		// LSN: 仅读取 lsn
		lsn, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("读取 LSN 失败: %w", err)
		}
		entry.LSN = lsn
		return entry, nil

	case OpPing:
		// PING: 无额外数据
		return entry, nil

	case OpCommand, OpExpired:
		// COMMAND/EXPIRED: txid + shard_cnt + payload
		txid, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("读取 txid 失败: %w", err)
		}
		entry.TxID = txid

		shardCnt, err := ReadPackedUint(jr.reader)
		if err != nil {
			return nil, fmt.Errorf("读取 shard_cnt 失败: %w", err)
		}
		entry.ShardCnt = shardCnt

		// 读取 Payload
		if err := jr.readPayload(entry); err != nil {
			return nil, fmt.Errorf("读取 payload 失败: %w", err)
		}

		return entry, nil

	default:
		return nil, fmt.Errorf("未知的 opcode: %d", entry.Opcode)
	}
}

// readPayload 读取 Payload 部分
// 格式：
//   - 参数数量 (Packed Uint) = 1 + args.size()
//   - 总命令大小 (Packed Uint)
//   - 命令名 (Packed String)
//   - 每个参数 (Packed String)
func (jr *JournalReader) readPayload(entry *JournalEntry) error {
	// 1. 读取参数数量（包含命令名）
	numElems, err := ReadPackedUint(jr.reader)
	if err != nil {
		return fmt.Errorf("读取参数数量失败: %w", err)
	}

	if numElems == 0 {
		// 空 payload
		return nil
	}

	// 2. 读取总命令大小（暂时读取但不使用，用于校验）
	totalSize, err := ReadPackedUint(jr.reader)
	if err != nil {
		return fmt.Errorf("读取总命令大小失败: %w", err)
	}
	_ = totalSize // 暂不使用

	// 3. 读取命令名
	cmd, err := ReadPackedString(jr.reader)
	if err != nil {
		return fmt.Errorf("读取命令名失败: %w", err)
	}
	entry.Command = cmd

	// 4. 读取参数（numElems - 1 个）
	if numElems > 1 {
		entry.Args = make([]string, numElems-1)
		for i := 0; i < int(numElems-1); i++ {
			arg, err := ReadPackedString(jr.reader)
			if err != nil {
				return fmt.Errorf("读取参数[%d]失败: %w", i, err)
			}
			entry.Args[i] = arg
		}
	}

	return nil
}
