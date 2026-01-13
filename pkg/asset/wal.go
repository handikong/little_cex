// 文件: pkg/asset/wal.go
// 资产模块 WAL (Write-Ahead Log)
//
// 核心原则:
// 1. 先写日志，再修改内存
// 2. 崩溃后通过重放日志恢复状态
// 3. 定期创建检查点减少恢复时间

package asset

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// =============================================================================
// WAL 条目格式
// =============================================================================

// WALEntryType 条目类型
type WALEntryType uint8

const (
	WALReserve       WALEntryType = iota + 1 // 冻结
	WALRelease                               // 解冻
	WALTransfer                              // 划转
	WALAddBalance                            // 增加余额
	WALDeductBalance                         // 扣减余额
	WALCheckpoint                            // 检查点
)

// WALEntry WAL 条目
type WALEntry struct {
	Seq       uint64       // 序列号 (递增)
	Type      WALEntryType // 条目类型
	Timestamp int64        // 时间戳
	CmdID     string       // 幂等键

	// 操作参数
	UserID   int64
	Symbol   string
	Amount   int64
	ToUserID int64
	ToSymbol string
	ToAmount int64
	Fee      int64
	FeeAsset string
}

// =============================================================================
// WAL 写入器
// =============================================================================

// WAL Write-Ahead Log
type WAL struct {
	dir    string   // 日志目录
	file   *os.File // 当前日志文件
	writer *bufio.Writer

	seq      uint64    // 当前序列号
	lastSync time.Time // 上次 fsync 时间

	mu  sync.Mutex // 仅用于外部调用
	buf []byte     // 复用缓冲区
}

// WALConfig WAL 配置
type WALConfig struct {
	Dir          string        // 日志目录
	SyncInterval time.Duration // fsync 间隔
}

// NewWAL 创建 WAL
func NewWAL(cfg WALConfig) (*WAL, error) {
	// 创建目录
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, fmt.Errorf("create wal dir: %w", err)
	}

	// 打开日志文件
	path := filepath.Join(cfg.Dir, "asset.wal")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open wal file: %w", err)
	}

	return &WAL{
		dir:    cfg.Dir,
		file:   file,
		writer: bufio.NewWriterSize(file, 64*1024), // 64KB 缓冲
		buf:    make([]byte, 512),
	}, nil
}

// Write 写入条目
func (w *WAL) Write(entry *WALEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 分配序列号
	w.seq++
	entry.Seq = w.seq
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixNano()
	}

	// 序列化
	data, err := w.encodeEntry(entry)
	if err != nil {
		return err
	}

	// 写入长度 + 数据 + CRC
	length := uint32(len(data))
	crc := crc32.ChecksumIEEE(data)

	// 写入: [长度 4B][数据][CRC 4B]
	if err := binary.Write(w.writer, binary.LittleEndian, length); err != nil {
		return err
	}
	if _, err := w.writer.Write(data); err != nil {
		return err
	}
	if err := binary.Write(w.writer, binary.LittleEndian, crc); err != nil {
		return err
	}

	return nil
}

// =============================================================================
// 检查点 (Checkpoint)
// =============================================================================

// Checkpoint 创建检查点
// 将当前状态快照保存到磁盘，并截断已处理的 WAL
func (w *WAL) Checkpoint(snapshotData []byte, upToSeq uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. 写入快照文件
	snapshotPath := filepath.Join(w.dir, "snapshot.bin")
	if err := os.WriteFile(snapshotPath, snapshotData, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	// 2. 记录检查点序列号
	metaPath := filepath.Join(w.dir, "checkpoint.meta")
	meta := fmt.Sprintf("%d", upToSeq)
	if err := os.WriteFile(metaPath, []byte(meta), 0644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// 3. 截断 WAL (可选: 创建新文件)
	// 这里简化处理：直接清空 WAL
	w.writer.Flush()
	w.file.Close()

	walPath := filepath.Join(w.dir, "asset.wal")
	w.file, _ = os.Create(walPath)
	w.writer = bufio.NewWriterSize(w.file, 64*1024)

	return nil
}

// LoadSnapshot 加载快照
func (w *WAL) LoadSnapshot() ([]byte, uint64, error) {
	// 读取检查点序列号
	metaPath := filepath.Join(w.dir, "checkpoint.meta")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil // 没有快照
		}
		return nil, 0, err
	}

	var seq uint64
	fmt.Sscanf(string(metaData), "%d", &seq)

	// 读取快照数据
	snapshotPath := filepath.Join(w.dir, "snapshot.bin")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return nil, 0, err
	}

	return data, seq, nil
}

// Sync 刷盘
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// Close 关闭
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.writer.Flush()
	return w.file.Close()
}

// GetSequence 获取当前序列号
func (w *WAL) GetSequence() uint64 {
	return w.seq
}

// =============================================================================
// 序列化
// =============================================================================

func (w *WAL) encodeEntry(e *WALEntry) ([]byte, error) {
	// 简单的二进制序列化
	// 格式: seq(8) + type(1) + ts(8) + cmdID_len(2) + cmdID + userID(8) + ...

	buf := w.buf[:0]

	// 固定字段
	buf = binary.LittleEndian.AppendUint64(buf, e.Seq)
	buf = append(buf, byte(e.Type))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.Timestamp))

	// CmdID (变长)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.CmdID)))
	buf = append(buf, e.CmdID...)

	// 操作参数
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.UserID))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.Symbol)))
	buf = append(buf, e.Symbol...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.Amount))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.ToUserID))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.ToSymbol)))
	buf = append(buf, e.ToSymbol...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.ToAmount))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.Fee))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.FeeAsset)))
	buf = append(buf, e.FeeAsset...)

	return buf, nil
}

// =============================================================================
// WAL 恢复
// =============================================================================

// Recover 恢复：读取 WAL 并重放
func (w *WAL) Recover(applyFn func(*WALEntry) error) (uint64, error) {
	// 回到文件开头
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(w.file)
	var lastSeq uint64
	var count int

	for {
		// 读取长度
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			if err == io.EOF {
				break
			}
			return lastSeq, fmt.Errorf("read length: %w", err)
		}

		// 读取数据
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return lastSeq, fmt.Errorf("read data: %w", err)
		}

		// 读取 CRC
		var crc uint32
		if err := binary.Read(reader, binary.LittleEndian, &crc); err != nil {
			return lastSeq, fmt.Errorf("read crc: %w", err)
		}

		// 校验 CRC
		if crc32.ChecksumIEEE(data) != crc {
			return lastSeq, errors.New("crc mismatch")
		}

		// 解码
		entry, err := w.decodeEntry(data)
		if err != nil {
			return lastSeq, fmt.Errorf("decode: %w", err)
		}

		// 重放
		if err := applyFn(entry); err != nil {
			return lastSeq, fmt.Errorf("apply: %w", err)
		}

		lastSeq = entry.Seq
		count++
	}

	w.seq = lastSeq
	return lastSeq, nil
}

func (w *WAL) decodeEntry(data []byte) (*WALEntry, error) {
	if len(data) < 17 {
		return nil, errors.New("data too short")
	}

	e := &WALEntry{}
	offset := 0

	// 固定字段
	e.Seq = binary.LittleEndian.Uint64(data[offset:])
	offset += 8
	e.Type = WALEntryType(data[offset])
	offset += 1
	e.Timestamp = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8

	// CmdID
	cmdIDLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	e.CmdID = string(data[offset : offset+cmdIDLen])
	offset += cmdIDLen

	// 操作参数
	e.UserID = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	symbolLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	e.Symbol = string(data[offset : offset+symbolLen])
	offset += symbolLen
	e.Amount = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	e.ToUserID = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	toSymbolLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	e.ToSymbol = string(data[offset : offset+toSymbolLen])
	offset += toSymbolLen
	e.ToAmount = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	e.Fee = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	feeAssetLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	e.FeeAsset = string(data[offset : offset+feeAssetLen])

	return e, nil
}
