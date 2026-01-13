// 文件: pkg/asset/engine_test.go
// 热钱包账户引擎 - 测试用例
//
// 测试策略:
// 1. 单元测试: 验证每个操作的正确性
// 2. 集成测试: 验证完整交易流程
// 3. 并发测试: 验证多线程安全性
// 4. 压测: 验证性能

package asset

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// 基础功能测试
// =============================================================================

// TestEngine_StartStop 测试引擎启动和停止
func TestEngine_StartStop(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())

	if err := engine.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 验证引擎正在运行
	if !engine.running.Load() {
		t.Error("Engine should be running")
	}

	engine.Stop()

	// 验证引擎已停止
	if engine.running.Load() {
		t.Error("Engine should be stopped")
	}
}

// TestEngine_Deposit 测试充值 (外部余额同步)
func TestEngine_Deposit(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	userID := int64(100)
	symbol := "USDT"
	amount := int64(10000 * Precision) // 10000 USDT

	// 充值
	err := engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_001",
		UserID:    userID,
		Symbol:    symbol,
		Amount:    amount,
	})
	if err != nil {
		t.Fatalf("Deposit failed: %v", err)
	}

	// 等待处理完成
	time.Sleep(10 * time.Millisecond)

	// 验证余额
	available := engine.GetAvailable(userID, symbol)
	if available != amount {
		t.Errorf("Expected balance %d, got %d", amount, available)
	}
}

// TestEngine_Reserve 测试冻结 (下单)
func TestEngine_Reserve(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	userID := int64(100)
	symbol := "USDT"
	depositAmount := int64(10000 * Precision)
	reserveAmount := int64(5000 * Precision)
	orderID := int64(1001)

	// 1. 先充值
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_001",
		UserID:    userID,
		Symbol:    symbol,
		Amount:    depositAmount,
	})
	time.Sleep(10 * time.Millisecond)

	// 2. 冻结
	err := engine.Reserve(userID, symbol, reserveAmount, orderID)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}

	// 3. 验证可用余额减少
	time.Sleep(10 * time.Millisecond)
	snap := engine.GetSnapshot(userID)
	if snap == nil {
		t.Fatal("Snapshot not found")
	}

	asset := snap.Assets[symbol]
	expectedAvailable := depositAmount - reserveAmount
	if asset.Available != expectedAvailable {
		t.Errorf("Expected available %d, got %d", expectedAvailable, asset.Available)
	}
	if asset.Locked != reserveAmount {
		t.Errorf("Expected locked %d, got %d", reserveAmount, asset.Locked)
	}
}

// TestEngine_Reserve_InsufficientBalance 测试余额不足
func TestEngine_Reserve_InsufficientBalance(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	userID := int64(100)
	symbol := "USDT"
	depositAmount := int64(1000 * Precision)
	reserveAmount := int64(5000 * Precision) // 超过余额
	orderID := int64(1001)

	// 充值 1000
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_001",
		UserID:    userID,
		Symbol:    symbol,
		Amount:    depositAmount,
	})
	time.Sleep(10 * time.Millisecond)

	// 尝试冻结 5000 (应该失败)
	err := engine.Reserve(userID, symbol, reserveAmount, orderID)
	if err != ErrInsufficientBalance {
		t.Errorf("Expected ErrInsufficientBalance, got %v", err)
	}
}

// TestEngine_Release 测试解冻 (撤单)
func TestEngine_Release(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	userID := int64(100)
	symbol := "USDT"
	depositAmount := int64(10000 * Precision)
	reserveAmount := int64(5000 * Precision)
	orderID := int64(1001)

	// 充值 + 冻结
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_001",
		UserID:    userID,
		Symbol:    symbol,
		Amount:    depositAmount,
	})
	time.Sleep(10 * time.Millisecond)
	engine.Reserve(userID, symbol, reserveAmount, orderID)
	time.Sleep(10 * time.Millisecond)

	// 解冻
	err := engine.Release(userID, symbol, reserveAmount, orderID)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// 验证余额恢复
	time.Sleep(10 * time.Millisecond)
	snap := engine.GetSnapshot(userID)
	if snap.Assets[symbol].Available != depositAmount {
		t.Errorf("Expected available %d, got %d", depositAmount, snap.Assets[symbol].Available)
	}
	if snap.Assets[symbol].Locked != 0 {
		t.Errorf("Expected locked 0, got %d", snap.Assets[symbol].Locked)
	}
}

// =============================================================================
// 成交结算测试
// =============================================================================

// TestEngine_ApplyFill 测试成交结算
func TestEngine_ApplyFill(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	buyerID := int64(100)
	sellerID := int64(200)
	price := int64(50000 * Precision) // 50000 USDT
	quantity := int64(1 * Precision)  // 1 BTC

	// 1. 买方充值 USDT
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_buyer",
		UserID:    buyerID,
		Symbol:    "USDT",
		Amount:    price,
	})

	// 2. 卖方充值 BTC
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_seller",
		UserID:    sellerID,
		Symbol:    "BTC",
		Amount:    quantity,
	})
	time.Sleep(10 * time.Millisecond)

	// 3. 买方下单冻结 USDT
	engine.Reserve(buyerID, "USDT", price, 1001)

	// 4. 卖方下单冻结 BTC
	engine.Reserve(sellerID, "BTC", quantity, 1002)
	time.Sleep(10 * time.Millisecond)

	// 5. 成交
	err := engine.ApplyFill(&FillEvent{
		TradeID:        12345,
		BuyerID:        buyerID,
		SellerID:       sellerID,
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Price:          price,
		Quantity:       quantity,
		BuyerFee:       0,
		BuyerFeeAsset:  "BTC",
		SellerFee:      0,
		SellerFeeAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("ApplyFill failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	// 6. 验证买方: 应该有 1 BTC，0 USDT
	buyerSnap := engine.GetSnapshot(buyerID)
	if buyerSnap == nil {
		t.Fatal("Buyer snapshot not found")
	}
	if buyerSnap.Assets["BTC"].Available != quantity {
		t.Errorf("Buyer BTC: expected %d, got %d", quantity, buyerSnap.Assets["BTC"].Available)
	}
	if buyerSnap.Assets["USDT"].Available != 0 && buyerSnap.Assets["USDT"].Locked != 0 {
		t.Errorf("Buyer USDT should be 0, got available=%d locked=%d",
			buyerSnap.Assets["USDT"].Available, buyerSnap.Assets["USDT"].Locked)
	}

	// 7. 验证卖方: 应该有 50000 USDT，0 BTC
	sellerSnap := engine.GetSnapshot(sellerID)
	if sellerSnap == nil {
		t.Fatal("Seller snapshot not found")
	}
	if sellerSnap.Assets["USDT"].Available != price {
		t.Errorf("Seller USDT: expected %d, got %d", price, sellerSnap.Assets["USDT"].Available)
	}
}

// =============================================================================
// 幂等性测试
// =============================================================================

// TestEngine_Idempotency 测试幂等性 (重复命令应被拒绝)
func TestEngine_Idempotency(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	userID := int64(100)
	symbol := "USDT"
	amount := int64(10000 * Precision)

	// 第一次充值
	err := engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_same_id",
		UserID:    userID,
		Symbol:    symbol,
		Amount:    amount,
	})
	if err != nil {
		t.Fatalf("First deposit failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	// 重复充值 (相同 EventID)
	err = engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "deposit_same_id", // 相同 ID
		UserID:    userID,
		Symbol:    symbol,
		Amount:    amount,
	})
	if err != ErrDuplicateCommand {
		t.Errorf("Expected ErrDuplicateCommand, got %v", err)
	}

	// 验证余额只增加一次
	time.Sleep(10 * time.Millisecond)
	available := engine.GetAvailable(userID, symbol)
	if available != amount {
		t.Errorf("Expected balance %d (not doubled), got %d", amount, available)
	}
}

// =============================================================================
// 并发测试
// =============================================================================

// TestEngine_Concurrent 测试并发操作
func TestEngine_Concurrent(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	numUsers := 100
	depositsPerUser := 10
	amountPerDeposit := int64(100 * Precision)

	var wg sync.WaitGroup

	// 并发充值
	for i := 0; i < numUsers; i++ {
		userID := int64(i)
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			for j := 0; j < depositsPerUser; j++ {
				engine.ApplyBalanceChange(&BalanceChangeEvent{
					EventType: "DEPOSIT",
					EventID:   fmt.Sprintf("deposit_%d_%d", uid, j),
					UserID:    uid,
					Symbol:    "USDT",
					Amount:    amountPerDeposit,
				})
			}
		}(userID)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // 等待所有命令处理完

	// 验证每个用户余额
	expectedBalance := amountPerDeposit * int64(depositsPerUser)
	for i := 0; i < numUsers; i++ {
		userID := int64(i)
		available := engine.GetAvailable(userID, "USDT")
		if available != expectedBalance {
			t.Errorf("User %d: expected %d, got %d", userID, expectedBalance, available)
		}
	}
}

// =============================================================================
// 对账测试
// =============================================================================

// TestEngine_GetAllSnapshots 测试对账快照
func TestEngine_GetAllSnapshots(t *testing.T) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	// 创建一些用户
	for i := 0; i < 10; i++ {
		engine.ApplyBalanceChange(&BalanceChangeEvent{
			EventType: "DEPOSIT",
			EventID:   fmt.Sprintf("deposit_%d", i),
			UserID:    int64(i),
			Symbol:    "USDT",
			Amount:    int64(1000 * Precision),
		})
	}
	time.Sleep(50 * time.Millisecond)

	// 获取所有快照
	snapshots := engine.GetAllSnapshots()

	if len(snapshots) != 10 {
		t.Errorf("Expected 10 snapshots, got %d", len(snapshots))
	}
}

// =============================================================================
// 性能压测
// =============================================================================

// BenchmarkEngine_Reserve 压测冻结操作
func BenchmarkEngine_Reserve(b *testing.B) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	// 预充值
	userID := int64(1)
	engine.ApplyBalanceChange(&BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "init",
		UserID:    userID,
		Symbol:    "USDT",
		Amount:    int64(b.N) * Precision * 100,
	})
	time.Sleep(10 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Reserve(userID, "USDT", 100*Precision, int64(i))
	}
}

// BenchmarkEngine_ApplyBalanceChange 压测余额同步
func BenchmarkEngine_ApplyBalanceChange(b *testing.B) {
	engine := NewEngine(DefaultEngineConfig())
	engine.Start()
	defer engine.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ApplyBalanceChange(&BalanceChangeEvent{
			EventType: "DEPOSIT",
			EventID:   fmt.Sprintf("d_%d", i),
			UserID:    int64(i % 1000),
			Symbol:    "USDT",
			Amount:    100 * Precision,
		})
	}
}
