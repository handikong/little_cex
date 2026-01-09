package mtrade

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWAL_WriteAndRead(t *testing.T) {
	// 创建临时目录
	dir := filepath.Join(os.TempDir(), "wal_test")
	defer os.RemoveAll(dir)

	// 创建 WAL
	config := DefaultWALConfig(dir)
	wal, err := NewWAL(config)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	// 写入订单
	order := &Order{
		ID:     1,
		UserID: 100,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
	}

	seq, err := wal.WriteOrder(order)
	if err != nil {
		t.Fatalf("failed to write order: %v", err)
	}
	if seq != 1 {
		t.Errorf("expected sequence 1, got %d", seq)
	}

	// 写入取消
	seq, err = wal.WriteCancelOrder(12345)
	if err != nil {
		t.Fatalf("failed to write cancel: %v", err)
	}
	if seq != 2 {
		t.Errorf("expected sequence 2, got %d", seq)
	}

	// 刷盘
	if err := wal.Sync(); err != nil {
		t.Fatalf("failed to sync: %v", err)
	}

	// 读取所有条目
	entries, err := wal.ReadAll()
	if err != nil {
		t.Fatalf("failed to read entries: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	// 验证第一条
	if entries[0].Type != EntryPlaceOrder {
		t.Errorf("expected EntryPlaceOrder, got %d", entries[0].Type)
	}

	// 验证第二条
	if entries[1].Type != EntryCancelOrder {
		t.Errorf("expected EntryCancelOrder, got %d", entries[1].Type)
	}
}

func TestWAL_Checksum(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "wal_checksum_test")
	defer os.RemoveAll(dir)

	config := DefaultWALConfig(dir)
	wal, err := NewWAL(config)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}

	// 写入多条
	for i := 0; i < 10; i++ {
		_, err := wal.WriteOrder(&Order{
			ID:    int64(i),
			Price: int64(50000 + i),
			Qty:   10,
		})
		if err != nil {
			t.Fatalf("failed to write: %v", err)
		}
	}
	wal.Sync()
	wal.Close()

	// 重新打开读取
	wal2, err := NewWAL(config)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	entries, err := wal2.ReadAll()
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}

	if len(entries) != 10 {
		t.Errorf("expected 10 entries, got %d", len(entries))
	}

	// 验证序列号继续
	if wal2.GetSequence() != 10 {
		t.Errorf("expected sequence 10, got %d", wal2.GetSequence())
	}
}

func TestWAL_Truncate(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "wal_truncate_test")
	defer os.RemoveAll(dir)

	config := DefaultWALConfig(dir)
	wal, err := NewWAL(config)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	// 写入数据
	for i := 0; i < 5; i++ {
		wal.WriteOrder(&Order{ID: int64(i)})
	}
	wal.Sync()

	// 截断
	if err := wal.Truncate(); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}

	// 验证空了
	entries, err := wal.ReadAll()
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("expected 0 entries after truncate, got %d", len(entries))
	}
}

func TestWAL_Checkpoint(t *testing.T) {
	dir, err := os.MkdirTemp("", "wal_test_checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	config := DefaultWALConfig(dir)
	wal, err := NewWAL(config)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()

	// 准备订单数据
	orders := []*Order{
		{ID: 1, Symbol: "BTC_USDT", Price: 50000, Qty: 10},
		{ID: 2, Symbol: "ETH_USDT", Price: 3000, Qty: 20},
	}

	// 创建 Checkpoint
	seq := int64(100)
	if err := wal.CreateCheckpoint(seq, orders); err != nil {
		t.Fatalf("failed to create checkpoint: %v", err)
	}

	// 验证文件是否存在
	checkpointFile := filepath.Join(dir, fmt.Sprintf("checkpoint_%d.dat", seq))
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Errorf("checkpoint file not found: %s", checkpointFile)
	}

	// 验证文件内容（简单验证大小）
	info, _ := os.Stat(checkpointFile)
	// Header(21) + 2 * (53 + len("BTC_USDT")) = 21 + 2 * 61 = 143 bytes
	// ETH_USDT 也是 8 字节，所以长度一样
	expectedSize := int64(21 + 2*(53+8))
	if info.Size() != expectedSize {
		t.Errorf("expected file size %d, got %d", expectedSize, info.Size())
	}
}

func TestWAL_Recovery(t *testing.T) {
	dir, err := os.MkdirTemp("", "wal_test_recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// 1. 准备数据：写入 WAL -> Checkpoint -> 写入更多 WAL
	config := DefaultWALConfig(dir)
	wal, err := NewWAL(config)
	if err != nil {
		t.Fatal(err)
	}

	// 写入 10 个订单
	orders := make([]*Order, 0)
	for i := 1; i <= 10; i++ {
		order := &Order{ID: int64(i), Symbol: "BTC_USDT", Price: 50000, Qty: 10, Side: SideBuy, Type: OrderTypeLimit}
		wal.WriteOrder(order)
		orders = append(orders, order)
	}

	// 创建 Checkpoint (包含前 10 个)
	if err := wal.CreateCheckpoint(10, orders); err != nil {
		t.Fatal(err)
	}
	// 截断 WAL
	wal.Truncate()

	// 写入后续 5 个订单
	for i := 11; i <= 15; i++ {
		order := &Order{ID: int64(i), Symbol: "BTC_USDT", Price: 50000, Qty: 10, Side: SideBuy, Type: OrderTypeLimit}
		wal.WriteOrder(order)
	}

	wal.Close()

	// 2. 模拟重启恢复
	// 创建新 Engine (带 WAL 配置)
	engineConfig := DefaultEngineConfig("BTC_USDT")
	engineConfig.WALDir = dir

	engine, err := NewEngine(engineConfig)
	if err != nil {
		t.Fatalf("failed to create engine with recovery: %v", err)
	}
	defer engine.Stop()

	// 3. 验证状态
	// 应该有 15 个订单
	// stats := engine.GetStats()
	// 注意：Recover 是直接 ProcessOrder，会触发匹配
	// 但这里全是 Buy 单，不会成交，只会挂单
	// 所以 OrdersReceived 应该为 0 (因为直接调用的 ProcessOrder，没有经过 SubmitOrder 计数?)
	// 不对，ProcessOrder 内部没有计数 OrdersReceived，那是 SubmitOrder 做的。
	// 但 OrderBook 应该有 15 个订单。

	// 验证 OrderBook 深度
	bids, _ := engine.orderBook.Depth(100)
	totalOrders := 0
	for _, level := range bids {
		totalOrders += level.Orders
	}

	if totalOrders != 15 {
		t.Errorf("expected 15 orders in book, got %d", totalOrders)
	}

	// 验证 WAL 序列号是否正确恢复
	if engine.wal.GetSequence() != 15 {
		t.Errorf("expected wal sequence 15, got %d", engine.wal.GetSequence())
	}
}

// =============================================================================
// WAL 基准测试
// =============================================================================

func BenchmarkWAL_WriteOrder(b *testing.B) {
	dir := filepath.Join(os.TempDir(), "wal_bench")
	defer os.RemoveAll(dir)

	config := WALConfig{
		Dir:      dir,
		SyncMode: SyncModeAsync, // 异步模式最快
	}
	wal, err := NewWAL(config)
	if err != nil {
		b.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	order := &Order{
		ID:     1,
		UserID: 100,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order.ID = int64(i)
		wal.WriteOrder(order)
	}
}

func BenchmarkWAL_WriteOrderSync(b *testing.B) {
	dir := filepath.Join(os.TempDir(), "wal_bench_sync")
	defer os.RemoveAll(dir)

	config := WALConfig{
		Dir:      dir,
		SyncMode: SyncModeAlways, // 同步模式最安全
	}
	wal, err := NewWAL(config)
	if err != nil {
		b.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	order := &Order{
		ID:     1,
		UserID: 100,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order.ID = int64(i)
		wal.WriteOrder(order)
	}
}
