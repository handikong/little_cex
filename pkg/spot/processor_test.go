// 文件: pkg/spot/processor_test.go
// 现货交易处理器 - 集成测试
//
// 测试策略:
// 1. 完整下单-成交流程
// 2. 部分成交场景
// 3. 撤单场景
// 4. 余额验证 (资金守恒)

package spot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"max.com/pkg/asset"
	"max.com/pkg/mtrade"
)

// =============================================================================
// 测试辅助函数
// =============================================================================

// setupTestEnv 创建测试环境
func setupTestEnv(t *testing.T) (*SpotProcessor, *asset.AccountEngine, *mtrade.Engine, func()) {
	// 创建资产引擎
	assetEngine := asset.NewEngine(asset.DefaultEngineConfig())
	assetEngine.Start()

	// 创建撮合引擎
	matchEngine, err := mtrade.NewEngine(mtrade.DefaultEngineConfig("BTC_USDT"))
	if err != nil {
		t.Fatalf("Failed to create match engine: %v", err)
	}
	matchEngine.Start(context.Background())

	// 创建处理器
	processor := NewSpotProcessor(ProcessorConfig{
		AssetEngine:  assetEngine,
		MatchEngine:  matchEngine,
		MakerFeeRate: 10, // 0.1%
		TakerFeeRate: 20, // 0.2%
	})

	cleanup := func() {
		matchEngine.Stop()
		assetEngine.Stop()
	}

	return processor, assetEngine, matchEngine, cleanup
}

// depositFunds 充值资金
func depositFunds(t *testing.T, engine *asset.AccountEngine, userID int64, symbol string, amount int64) {
	err := engine.ApplyBalanceChange(&asset.BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   fmt.Sprintf("deposit_%d_%s_%d", userID, symbol, time.Now().UnixNano()),
		UserID:    userID,
		Symbol:    symbol,
		Amount:    amount,
	})
	if err != nil {
		t.Fatalf("Deposit failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
}

// =============================================================================
// 测试用例
// =============================================================================

// TestSpotProcessor_PlaceOrder 测试下单
func TestSpotProcessor_PlaceOrder(t *testing.T) {
	processor, assetEngine, _, cleanup := setupTestEnv(t)
	defer cleanup()

	userID := int64(100)
	price := int64(50000 * asset.Precision) // 50000 USDT
	qty := int64(1 * asset.Precision)       // 1 BTC

	// 充值 USDT (需要包含手续费)
	depositFunds(t, assetEngine, userID, "USDT", 60000*asset.Precision)

	// 下买单
	order := &mtrade.Order{
		ID:     1001,
		UserID: userID,
		Symbol: "BTC_USDT",
		Side:   mtrade.SideBuy,
		Type:   mtrade.OrderTypeLimit,
		Price:  price,
		Qty:    qty,
	}

	err := processor.PlaceOrder(order)
	if err != nil {
		t.Fatalf("PlaceOrder failed: %v", err)
	}

	// 验证余额被冻结
	time.Sleep(20 * time.Millisecond)
	snap := assetEngine.GetSnapshot(userID)
	if snap == nil {
		t.Fatal("Snapshot not found")
	}

	// 应该冻结: 50000 USDT + 0.2% 手续费 = 50100 USDT
	expectedLocked := int64(50100 * asset.Precision)
	if snap.Assets["USDT"].Locked < expectedLocked-asset.Precision {
		t.Errorf("Expected locked >= %d, got %d", expectedLocked, snap.Assets["USDT"].Locked)
	}
}

// TestSpotProcessor_FullMatch 测试完整成交
func TestSpotProcessor_FullMatch(t *testing.T) {
	processor, assetEngine, _, cleanup := setupTestEnv(t)
	defer cleanup()

	buyerID := int64(100)
	sellerID := int64(200)
	price := int64(50000 * asset.Precision)
	qty := int64(1 * asset.Precision)

	// 买方充值 USDT
	depositFunds(t, assetEngine, buyerID, "USDT", 60000*asset.Precision)

	// 卖方充值 BTC
	depositFunds(t, assetEngine, sellerID, "BTC", 2*asset.Precision)

	// 1. 卖方先挂卖单 (成为 Maker)
	sellOrder := &mtrade.Order{
		ID:     2001,
		UserID: sellerID,
		Symbol: "BTC_USDT",
		Side:   mtrade.SideSell,
		Type:   mtrade.OrderTypeLimit,
		Price:  price,
		Qty:    qty,
	}
	if err := processor.PlaceOrder(sellOrder); err != nil {
		t.Fatalf("Sell order failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// 2. 买方下买单 (成为 Taker，立即成交)
	buyOrder := &mtrade.Order{
		ID:     1001,
		UserID: buyerID,
		Symbol: "BTC_USDT",
		Side:   mtrade.SideBuy,
		Type:   mtrade.OrderTypeLimit,
		Price:  price,
		Qty:    qty,
	}
	if err := processor.PlaceOrder(buyOrder); err != nil {
		t.Fatalf("Buy order failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 3. 验证买方余额: 应该有 BTC，USDT 减少
	buyerSnap := assetEngine.GetSnapshot(buyerID)
	if buyerSnap == nil {
		t.Fatal("Buyer snapshot not found")
	}

	// 买方应该获得约 1 BTC (扣手续费后)
	if buyerSnap.Assets["BTC"].Available < qty*99/100 {
		t.Errorf("Buyer should have BTC, got %d", buyerSnap.Assets["BTC"].Available)
	}

	// 4. 验证卖方余额: 应该有 USDT，BTC 减少
	sellerSnap := assetEngine.GetSnapshot(sellerID)
	if sellerSnap == nil {
		t.Fatal("Seller snapshot not found")
	}

	// 卖方应该获得约 50000 USDT (扣手续费后)
	expectedUSDT := (price / asset.Precision) * qty
	if sellerSnap.Assets["USDT"].Available < expectedUSDT*99/100 {
		t.Errorf("Seller should have USDT, got %d", sellerSnap.Assets["USDT"].Available)
	}
}

// TestSpotProcessor_Cancel 测试撤单
func TestSpotProcessor_Cancel(t *testing.T) {
	processor, assetEngine, _, cleanup := setupTestEnv(t)
	defer cleanup()

	userID := int64(100)
	price := int64(50000 * asset.Precision)
	qty := int64(1 * asset.Precision)

	// 充值
	depositFunds(t, assetEngine, userID, "USDT", 60000*asset.Precision)
	initialBalance := assetEngine.GetAvailable(userID, "USDT")

	// 下单
	order := &mtrade.Order{
		ID:     1001,
		UserID: userID,
		Symbol: "BTC_USDT",
		Side:   mtrade.SideBuy,
		Type:   mtrade.OrderTypeLimit,
		Price:  price,
		Qty:    qty,
	}
	processor.PlaceOrder(order)
	time.Sleep(50 * time.Millisecond)

	// 撤单
	processor.CancelOrder(1001)
	time.Sleep(50 * time.Millisecond)

	// 验证余额恢复
	finalBalance := assetEngine.GetAvailable(userID, "USDT")
	if finalBalance != initialBalance {
		t.Errorf("Balance should be restored, expected %d, got %d", initialBalance, finalBalance)
	}
}

// TestSpotProcessor_InsufficientBalance 测试余额不足
func TestSpotProcessor_InsufficientBalance(t *testing.T) {
	processor, assetEngine, _, cleanup := setupTestEnv(t)
	defer cleanup()

	userID := int64(100)
	price := int64(50000 * asset.Precision)
	qty := int64(1 * asset.Precision)

	// 充值不足的金额
	depositFunds(t, assetEngine, userID, "USDT", 10000*asset.Precision)

	// 下单应该失败
	order := &mtrade.Order{
		ID:     1001,
		UserID: userID,
		Symbol: "BTC_USDT",
		Side:   mtrade.SideBuy,
		Type:   mtrade.OrderTypeLimit,
		Price:  price,
		Qty:    qty,
	}
	err := processor.PlaceOrder(order)
	if err == nil {
		t.Error("PlaceOrder should fail with insufficient balance")
	}
}

// =============================================================================
// 压测
// =============================================================================

// BenchmarkSpotProcessor_PlaceOrder 压测下单
func BenchmarkSpotProcessor_PlaceOrder(b *testing.B) {
	assetEngine := asset.NewEngine(asset.DefaultEngineConfig())
	assetEngine.Start()
	defer assetEngine.Stop()

	matchEngine, _ := mtrade.NewEngine(mtrade.DefaultEngineConfig("BTC_USDT"))
	matchEngine.Start(context.Background())
	defer matchEngine.Stop()

	processor := NewSpotProcessor(ProcessorConfig{
		AssetEngine:  assetEngine,
		MatchEngine:  matchEngine,
		MakerFeeRate: 10,
		TakerFeeRate: 20,
	})

	// 预充值
	userID := int64(1)
	assetEngine.ApplyBalanceChange(&asset.BalanceChangeEvent{
		EventType: "DEPOSIT",
		EventID:   "init",
		UserID:    userID,
		Symbol:    "USDT",
		Amount:    int64(b.N) * 100000 * asset.Precision,
	})
	time.Sleep(10 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := &mtrade.Order{
			ID:     int64(i + 1),
			UserID: userID,
			Symbol: "BTC_USDT",
			Side:   mtrade.SideBuy,
			Type:   mtrade.OrderTypeLimit,
			Price:  50000 * asset.Precision,
			Qty:    asset.Precision,
		}
		processor.PlaceOrder(order)
	}
}
