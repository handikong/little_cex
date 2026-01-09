package mtrade

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"time"
)

// =============================================================================
// WAL (Write-Ahead Log) 机制
// =============================================================================
//
// 【面试高频】WAL 保证数据持久化
//
// 核心原则：先写日志，再执行操作
//
// 流程：
//   订单到达 → WAL 写入(落盘) → 撮合(内存) → 返回结果
//
// 恢复流程：
//   启动 → 加载 Checkpoint → 重放 WAL → 继续服务

// =============================================================================
// WAL Entry 定义
// =============================================================================

// EntryType WAL 条目类型
type EntryType uint8

const (
	EntryPlaceOrder  EntryType = 1 // 下单
	EntryCancelOrder EntryType = 2 // 取消订单
	EntryCheckpoint  EntryType = 3 // 检查点
)

// WALEntry WAL 条目
// 【设计】每条 Entry 包含序列号、类型、数据和校验和
type WALEntry struct {
	Sequence  int64     // 序列号（单调递增）
	Timestamp int64     // 时间戳（Unix 纳秒）
	Type      EntryType // 操作类型
	Data      []byte    // 序列化的操作数据
	Checksum  uint32    // CRC32 校验和
}

// =============================================================================
// WAL Writer
// =============================================================================

// WAL Write-Ahead Log
// 【无锁设计】只由 matchLoop 单线程调用，无需加锁
type WAL struct {
	file     *os.File
	writer   *bufio.Writer
	sequence int64
	dir      string
	filename string

	// 【优化】可复用 buffer，避免每次分配
	buf []byte

	// 【优化】可复用 CRC32 对象
	crc32Hash hash.Hash32

	// 配置
	syncMode SyncMode
}

// SyncMode 同步模式
type SyncMode int

const (
	SyncModeAlways SyncMode = iota // 每条都刷盘（最安全）
	SyncModeBatch                  // 批量刷盘
	SyncModeAsync                  // 异步刷盘（最快但可能丢数据）
)

// WALConfig WAL 配置
type WALConfig struct {
	Dir      string   // WAL 文件目录
	SyncMode SyncMode // 同步模式
}

// DefaultWALConfig 默认配置
func DefaultWALConfig(dir string) WALConfig {
	return WALConfig{
		Dir:      dir,
		SyncMode: SyncModeBatch, // 默认批量刷盘
	}
}

// NewWAL 创建 WAL
func NewWAL(config WALConfig) (*WAL, error) {
	// 创建目录
	if err := os.MkdirAll(config.Dir, 0755); err != nil {
		return nil, err
	}

	// 打开 WAL 文件（追加模式）
	filename := filepath.Join(config.Dir, "wal.log")
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	wal := &WAL{
		file:      file,
		writer:    bufio.NewWriter(file),
		dir:       config.Dir,
		filename:  filename,
		buf:       make([]byte, 256), // 初始化可复用 buffer
		crc32Hash: crc32.NewIEEE(),   // 初始化 CRC32 对象
		syncMode:  config.SyncMode,
	}

	// 读取最后的序列号
	wal.sequence, _ = wal.getLastSequence()

	return wal, nil
}

// =============================================================================
// 写入操作
// =============================================================================

// WriteOrder 写入下单日志
// 【优化】使用二进制序列化 + 可复用 buffer
func (w *WAL) WriteOrder(order *Order) (int64, error) {
	// 二进制格式：ID(8) + UserID(8) + Price(8) + Qty(8) + FilledQty(8) + CreatedAt(8)
	//            + Side(1) + Type(1) + Status(1) + SymbolLen(2) + Symbol(n)
	symbolBytes := []byte(order.Symbol)
	dataLen := 8*6 + 3 + 2 + len(symbolBytes)

	// 使用可复用 buffer，按需扩容
	if cap(w.buf) < dataLen {
		w.buf = make([]byte, dataLen*2) // 2x 扩容
	}
	data := w.buf[:dataLen]

	offset := 0
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.ID))
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.UserID))
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.Price))
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.Qty))
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.FilledQty))
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], uint64(order.CreatedAt))
	offset += 8
	data[offset] = byte(order.Side)
	offset++
	data[offset] = byte(order.Type)
	offset++
	data[offset] = byte(order.Status)
	offset++
	binary.LittleEndian.PutUint16(data[offset:], uint16(len(symbolBytes)))
	offset += 2
	copy(data[offset:], symbolBytes)

	return w.write(EntryPlaceOrder, data)
}

// WriteCancelOrder 写入取消订单日志
func (w *WAL) WriteCancelOrder(orderID int64) (int64, error) {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, uint64(orderID))

	return w.write(EntryCancelOrder, data)
}

// WriteCheckpoint 写入检查点
func (w *WAL) WriteCheckpoint(data []byte) (int64, error) {
	return w.write(EntryCheckpoint, data)
}

// write 写入 WAL 条目
// 【无锁】仅由 matchLoop 单线程调用
func (w *WAL) write(entryType EntryType, data []byte) (int64, error) {
	w.sequence++
	entry := WALEntry{
		Sequence:  w.sequence,
		Timestamp: time.Now().UnixNano(),
		Type:      entryType,
		Data:      data,
	}

	// 计算校验和
	entry.Checksum = w.calculateChecksum(&entry)

	// 写入 Entry
	if err := w.writeEntry(&entry); err != nil {
		return 0, err
	}

	// 根据同步模式决定是否刷盘
	if w.syncMode == SyncModeAlways {
		if err := w.sync(); err != nil {
			return 0, err
		}
	}

	return entry.Sequence, nil
}

// writeEntry 写入单条 Entry
func (w *WAL) writeEntry(entry *WALEntry) error {
	// 写入 Sequence
	if err := binary.Write(w.writer, binary.LittleEndian, entry.Sequence); err != nil {
		return err
	}

	// 写入 Timestamp
	if err := binary.Write(w.writer, binary.LittleEndian, entry.Timestamp); err != nil {
		return err
	}

	// 写入 Type
	if err := w.writer.WriteByte(byte(entry.Type)); err != nil {
		return err
	}

	// 写入 Data Length
	if err := binary.Write(w.writer, binary.LittleEndian, uint32(len(entry.Data))); err != nil {
		return err
	}

	// 写入 Data
	if _, err := w.writer.Write(entry.Data); err != nil {
		return err
	}

	// 写入 Checksum
	if err := binary.Write(w.writer, binary.LittleEndian, entry.Checksum); err != nil {
		return err
	}

	return nil
}

// Sync 强制刷盘
func (w *WAL) Sync() error {
	return w.sync()
}

func (w *WAL) sync() error {
	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// Close 关闭 WAL
func (w *WAL) Close() error {
	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Close()
}

// =============================================================================
// 读取和恢复
// =============================================================================

// ReadAll 读取所有 WAL 条目
func (w *WAL) ReadAll() ([]WALEntry, error) {
	// 重新打开文件读取
	file, err := os.Open(w.filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var entries []WALEntry

	for {
		entry, err := w.readEntry(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return entries, err
		}

		// 验证校验和
		expectedChecksum := w.calculateChecksum(entry)
		if entry.Checksum != expectedChecksum {
			return entries, errors.New("WAL entry checksum mismatch")
		}

		entries = append(entries, *entry)
	}

	return entries, nil
}

// readEntry 读取单条 Entry
func (w *WAL) readEntry(reader *bufio.Reader) (*WALEntry, error) {
	entry := &WALEntry{}

	// 读取 Sequence
	if err := binary.Read(reader, binary.LittleEndian, &entry.Sequence); err != nil {
		return nil, err
	}

	// 读取 Timestamp
	if err := binary.Read(reader, binary.LittleEndian, &entry.Timestamp); err != nil {
		return nil, err
	}

	// 读取 Type
	typeByte, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	entry.Type = EntryType(typeByte)

	// 读取 Data Length
	var dataLen uint32
	if err := binary.Read(reader, binary.LittleEndian, &dataLen); err != nil {
		return nil, err
	}

	// 读取 Data
	entry.Data = make([]byte, dataLen)
	if _, err := io.ReadFull(reader, entry.Data); err != nil {
		return nil, err
	}

	// 读取 Checksum
	if err := binary.Read(reader, binary.LittleEndian, &entry.Checksum); err != nil {
		return nil, err
	}

	return entry, nil
}

// getLastSequence 获取最后的序列号
func (w *WAL) getLastSequence() (int64, error) {
	entries, err := w.ReadAll()
	if err != nil {
		return 0, err
	}

	if len(entries) == 0 {
		return 0, nil
	}

	return entries[len(entries)-1].Sequence, nil
}

// =============================================================================
// 辅助函数
// =============================================================================

// calculateChecksum 计算校验和
// calculateChecksum 计算校验和
// 【优化】复用 Hash 对象 + 零分配
func (w *WAL) calculateChecksum(entry *WALEntry) uint32 {
	w.crc32Hash.Reset()

	// 使用 buf 作为临时 buffer (复用前 17 字节)
	// Seq(8) + Time(8) + Type(1)
	if cap(w.buf) < 17 {
		w.buf = make([]byte, 256)
	}
	tmp := w.buf[:17]

	binary.LittleEndian.PutUint64(tmp[0:], uint64(entry.Sequence))
	binary.LittleEndian.PutUint64(tmp[8:], uint64(entry.Timestamp))
	tmp[16] = byte(entry.Type)

	w.crc32Hash.Write(tmp)
	w.crc32Hash.Write(entry.Data)
	return w.crc32Hash.Sum32()
}

// GetSequence 获取当前序列号
func (w *WAL) GetSequence() int64 {
	return w.sequence
}

// Truncate 截断 WAL（通常在 Checkpoint 后调用）
func (w *WAL) Truncate() error {
	// 关闭当前文件
	if err := w.writer.Flush(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}

	// 创建新文件
	file, err := os.Create(w.filename)
	if err != nil {
		return err
	}

	w.file = file
	w.writer = bufio.NewWriter(file)

	// 重置序列号（可选，取决于是否希望序列号连续）
	// w.sequence = 0

	return nil
}

// CreateCheckpoint 创建检查点
// 【优化】二进制格式存储：Header + Orders + Checksum
func (w *WAL) CreateCheckpoint(seq int64, orders []*Order) error {
	// 1. 创建临时文件
	tmpFile := filepath.Join(w.dir, fmt.Sprintf("checkpoint_%d.tmp", seq))
	f, err := os.Create(tmpFile)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)

	// 2. 写入 Header
	// Magic(4) + Version(1) + Seq(8) + OrderCount(8) = 21 bytes
	header := make([]byte, 21)
	binary.LittleEndian.PutUint32(header[0:], 0x43505431) // "CPT1"
	header[4] = 1                                         // Version 1
	binary.LittleEndian.PutUint64(header[5:], uint64(seq))
	binary.LittleEndian.PutUint64(header[13:], uint64(len(orders)))

	if _, err := writer.Write(header); err != nil {
		return err
	}

	// 3. 写入 Orders
	// 复用 buffer 进行序列化
	buf := make([]byte, 256)
	for _, order := range orders {
		// 序列化 Order
		// ID(8) + UserID(8) + Price(8) + Qty(8) + FilledQty(8) + CreatedAt(8) +
		// Side(1) + Type(1) + Status(1) + SymLen(2) + Symbol(n)
		// 固定长度 = 8*6 + 3 + 2 = 53 bytes

		symbolLen := len(order.Symbol)
		totalLen := 53 + symbolLen

		if cap(buf) < totalLen {
			buf = make([]byte, totalLen*2)
		}

		offset := 0
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.ID))
		offset += 8
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.UserID))
		offset += 8
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.Price))
		offset += 8
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.Qty))
		offset += 8
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.FilledQty))
		offset += 8
		binary.LittleEndian.PutUint64(buf[offset:], uint64(order.CreatedAt))
		offset += 8
		buf[offset] = byte(order.Side)
		offset++
		buf[offset] = byte(order.Type)
		offset++
		buf[offset] = byte(order.Status)
		offset++
		binary.LittleEndian.PutUint16(buf[offset:], uint16(symbolLen))
		offset += 2
		copy(buf[offset:], order.Symbol)

		if _, err := writer.Write(buf[:totalLen]); err != nil {
			return err
		}
	}

	// 4. 刷盘
	if err := writer.Flush(); err != nil {
		return err
	}

	// 5. 重命名为正式文件
	finalFile := filepath.Join(w.dir, fmt.Sprintf("checkpoint_%d.dat", seq))
	if err := os.Rename(tmpFile, finalFile); err != nil {
		return err
	}
	return nil
}

// LoadCheckpoint 加载最新的检查点
func (w *WAL) LoadCheckpoint() (int64, []*Order, error) {
	// 1. 查找最新的 checkpoint 文件
	files, err := filepath.Glob(filepath.Join(w.dir, "checkpoint_*.dat"))
	if err != nil {
		return 0, nil, err
	}
	if len(files) == 0 {
		return 0, nil, nil // 没有检查点
	}

	// 找到序列号最大的文件
	var maxSeq int64 = -1
	var latestFile string
	for _, file := range files {
		var seq int64
		_, err := fmt.Sscanf(filepath.Base(file), "checkpoint_%d.dat", &seq)
		if err == nil && seq > maxSeq {
			maxSeq = seq
			latestFile = file
		}
	}

	if latestFile == "" {
		return 0, nil, nil
	}

	// 2. 打开文件
	f, err := os.Open(latestFile)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	// 3. 读取 Header
	header := make([]byte, 21)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}

	// 验证 Magic
	if binary.LittleEndian.Uint32(header[0:]) != 0x43505431 {
		return 0, nil, errors.New("invalid checkpoint magic")
	}

	seq := int64(binary.LittleEndian.Uint64(header[5:]))
	count := int64(binary.LittleEndian.Uint64(header[13:]))

	// 4. 读取 Orders
	orders := make([]*Order, 0, count)
	for i := int64(0); i < count; i++ {
		// 读取固定长度部分 (53 bytes)
		buf := make([]byte, 53)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return 0, nil, err
		}

		// 解析 Symbol 长度
		symbolLen := binary.LittleEndian.Uint16(buf[51:])

		// 读取 Symbol
		symbolBuf := make([]byte, symbolLen)
		if _, err := io.ReadFull(reader, symbolBuf); err != nil {
			return 0, nil, err
		}

		// 拼接完整数据进行解码
		fullData := append(buf, symbolBuf...)
		order := decodeOrder(fullData)
		orders = append(orders, order)
	}

	return seq, orders, nil
}

// =============================================================================
// 二进制序列化辅助
// =============================================================================

// decodeOrder 从二进制数据解码 Order
func decodeOrder(data []byte) *Order {
	order := &Order{}
	offset := 0

	order.ID = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.UserID = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.Price = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.Qty = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.FilledQty = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.CreatedAt = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	order.Side = Side(data[offset])
	offset++
	order.Type = OrderType(data[offset])
	offset++
	order.Status = OrderStatus(data[offset])
	offset++
	symbolLen := binary.LittleEndian.Uint16(data[offset:])
	offset += 2
	order.Symbol = string(data[offset : offset+int(symbolLen)])

	return order
}

// =============================================================================
// 恢复器
// =============================================================================

// WALRecovery WAL 恢复器
type WALRecovery struct {
	wal *WAL
}

// NewWALRecovery 创建恢复器
func NewWALRecovery(wal *WAL) *WALRecovery {
	return &WALRecovery{wal: wal}
}

// Recover 恢复订单簿状态
// 【面试】重放 WAL 条目到订单簿
// Recover 恢复订单簿状态
// 【面试】重放 WAL 条目到订单簿
func (r *WALRecovery) Recover(engine *Engine) error {
	// 1. 加载 Checkpoint
	lastSeq, orders, err := r.wal.LoadCheckpoint()
	if err != nil {
		return fmt.Errorf("load checkpoint failed: %v", err)
	}

	// 恢复 Checkpoint 数据
	if len(orders) > 0 {
		for _, order := range orders {
			// 直接恢复到 OrderBook，不经过 Matcher 处理（因为已经是最终状态）
			// 但为了简单，这里还是通过 AddOrder 恢复，假设 Checkpoint 存的是 Active Orders
			engine.orderBook.AddOrder(order)
		}
		// 更新 WAL 序列号
		r.wal.sequence = lastSeq
	}

	// 2. 读取 WAL
	entries, err := r.wal.ReadAll()
	if err != nil {
		return err
	}

	// 3. 重放 WAL (仅重放 Sequence > lastSeq 的条目)
	for _, entry := range entries {
		if entry.Sequence <= lastSeq {
			continue
		}

		switch entry.Type {
		case EntryPlaceOrder:
			order := decodeOrder(entry.Data)
			// 直接添加到订单簿（绕过 WAL 避免重复写入）
			engine.matcher.ProcessOrder(order)

		case EntryCancelOrder:
			orderID := int64(binary.LittleEndian.Uint64(entry.Data))
			engine.orderBook.CancelOrder(orderID)
		}

		// 更新序列号
		if entry.Sequence > r.wal.sequence {
			r.wal.sequence = entry.Sequence
		}
	}

	// 恢复完成后更新快照
	engine.orderBook.UpdateSnapshot()

	return nil
}
