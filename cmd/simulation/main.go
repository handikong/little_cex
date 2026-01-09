package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"max.com/pkg/liquidation"
	"max.com/pkg/mtrade"
	"max.com/pkg/risk"
)

// =============================================================================
// Mock 组件实现
// =============================================================================

// MockUserDataProvider 模拟用户数据
type MockUserDataProvider struct {
	mu           sync.RWMutex
	positions    map[int64][]risk.Position // UserID -> Positions
	balances     map[int64]float64         // UserID -> Balance
	currentPrice float64                   // 当前市场价格 (用于构建 RiskInput)
}

func NewMockUserDataProvider() *MockUserDataProvider {
	return &MockUserDataProvider{
		positions: make(map[int64][]risk.Position),
		balances:  make(map[int64]float64),
	}
}

func (p *MockUserDataProvider) GetAllUserIDs(ctx context.Context) ([]int64, error) {
	log.Println("[Provider] GetAllUserIDs called")
	p.mu.RLock()
	defer p.mu.RUnlock()

	ids := make([]int64, 0, len(p.positions))
	for id := range p.positions {
		ids = append(ids, id)
	}
	return ids, nil
}

func (p *MockUserDataProvider) GetUserRiskInput(ctx context.Context, userID int64) (risk.RiskInput, error) {
	log.Printf("[Provider] Getting risk input for user %d", userID)
	p.mu.RLock()
	defer p.mu.RUnlock()

	positions, ok := p.positions[userID]
	if !ok {
		return risk.RiskInput{}, fmt.Errorf("user %d not found", userID)
	}

	balance := p.balances[userID]

	// 构建 RiskInput
	input := risk.RiskInput{
		Account: risk.Account{
			Balance:        balance,
			InitMarginRate: 0.1, // 假设 10% IMR
		},
		Positions: positions,
		Prices: map[string]risk.PriceSnapshot{
			"BTC_USDT": {
				Price:       p.currentPrice,
				MarkPrice:   p.currentPrice,
				FundingRate: 0.0001,
			},
		},
	}

	return input, nil
}

func (p *MockUserDataProvider) UpdatePosition(userID int64, pos risk.Position) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// 简单覆盖
	p.positions[userID] = []risk.Position{pos}
}

func (p *MockUserDataProvider) UpdateBalance(userID int64, balance float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.balances[userID] = balance
}

func (p *MockUserDataProvider) SetCurrentPrice(price float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentPrice = price
}

// MockLiquidationExecutor 模拟强平执行器
type MockLiquidationExecutor struct {
	tradeEngine *mtrade.Engine
}

func (e *MockLiquidationExecutor) Execute(ctx context.Context, task liquidation.LiquidationTask) liquidation.LiquidationResult {
	log.Printf("[Liquidation] ⚡️ TRIGGERED for User %d | Symbol: %s | RiskRatio: %.2f",
		task.UserID, task.TriggerSymbol, task.RiskRatio)

	result := liquidation.LiquidationResult{
		UserID:     task.UserID,
		ExecutedAt: time.Now(),
	}

	// 构造强平订单 (市价全平)
	// 这里简化处理，假设是多头仓位，需要卖出平仓
	order := &mtrade.Order{
		UserID:    task.UserID,
		Symbol:    task.TriggerSymbol,
		Side:      mtrade.SideSell, // 假设平多
		Type:      mtrade.OrderTypeMarket,
		Qty:       10, // 假设平仓数量 (需要从 Task 或 Provider 获取，这里简化)
		CreatedAt: time.Now().UnixNano(),
	}

	log.Printf("[Liquidation] 🚀 Submitting Market Order to Engine: User %d, %s, Sell %d",
		order.UserID, order.Symbol, order.Qty)

	if ok := e.tradeEngine.SubmitOrder(order); !ok {
		log.Printf("[Liquidation] ❌ Failed to submit order")
		result.Success = false
		result.Error = fmt.Errorf("failed to submit order")
		return result
	}

	result.Success = true
	result.Details = liquidation.LiquidationDetails{
		ClosedPositions: 1,
	}
	return result
}

// =============================================================================
// 主程序
// =============================================================================

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("🚀 Starting Full System Simulation...")

	// 1. 初始化 撮合引擎 (Matching Engine)
	// -------------------------------------------------------------------------
	tradeConfig := mtrade.DefaultEngineConfig("BTC_USDT")
	tradeConfig.WALDir = "./wal_data" // 启用 WAL
	os.RemoveAll("./wal_data")        // 清理旧数据

	tradeEngine, err := mtrade.NewEngine(tradeConfig)
	if err != nil {
		log.Fatalf("Failed to create Trade Engine: %v", err)
	}

	// 订阅成交事件 (Mock Subscription)
	tradeEngine.OnEvent(func(e mtrade.Event) {
		switch e.Type {
		case mtrade.EventTrade:
			log.Printf("[Trade] 🤝 MATCHED: %s | Price: %d | Qty: %d | Maker: %d | Taker: %d",
				e.Trade.Symbol, e.Trade.Price, e.Trade.Qty, e.Trade.MakerID, e.Trade.TakerID)
		case mtrade.EventOrderCanceled:
			log.Printf("[Trade] 🗑 CANCELED: Order %d", e.Order.ID)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tradeEngine.Start(ctx)
	defer tradeEngine.Stop()
	log.Println("✅ Matching Engine Started")

	// 2. 初始化 强平引擎 (Liquidation Engine)
	// -------------------------------------------------------------------------
	userDataProvider := NewMockUserDataProvider()
	riskEngine := risk.NewEngine()

	liqExecutor := &MockLiquidationExecutor{tradeEngine: tradeEngine}

	// NewEngine(riskEngine, userProvider, executor)
	liqEngine := liquidation.NewEngine(riskEngine, userDataProvider, liqExecutor)

	// Start()
	if err := liqEngine.Start(); err != nil {
		log.Fatalf("Failed to start Liquidation Engine: %v", err)
	}
	defer liqEngine.Stop()
	log.Println("✅ Liquidation Engine Started")

	// 3. 模拟数据生成
	// -------------------------------------------------------------------------

	// 3.1 初始用户仓位 (高风险用户)
	highRiskUser := int64(888)
	userDataProvider.UpdateBalance(highRiskUser, 5000) // 余额 5000
	userDataProvider.UpdatePosition(highRiskUser, risk.Position{
		Instrument:            risk.InstrumentPerp,
		Symbol:                "BTC_USDT",
		Qty:                   10.0,  // 持仓 10 BTC (价值 500,000)
		EntryPrice:            50000, // 入场价 50000
		MaintenanceMarginRate: 0.005, // 0.5% MMR
	})
	userDataProvider.SetCurrentPrice(50000)

	// 3.2 启动行情模拟器 (Market Simulator)
	go func() {
		price := float64(50000)
		ticker := time.NewTicker(100 * time.Millisecond)
		startTime := time.Now()
		crashed := false

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !crashed {
					// 1. 随机波动价格
					change := (rand.Float64() - 0.5) * 100 // -50 ~ +50
					price += change

					// 强制暴跌 (2秒后)
					if time.Since(startTime) > 2*time.Second {
						price = 40000 // 暴跌到 40000
						crashed = true
						log.Printf("[Market] 📉 FORCED CRASH! Price dropped to %.2f (Sustained)", price)
					}
				} else {
					// 保持低价，轻微波动
					change := (rand.Float64() - 0.5) * 10
					price = 40000 + change
				}

				userDataProvider.SetCurrentPrice(price)

				// 2. 推送价格给强平引擎 (模拟 Scanner 行为或直接推送)
				// 注意：Engine 没有直接 OnPriceChange 公共方法，通常是通过 Scanner 或 PriceProvider
				// 但 Engine 有 HandleLevelChange，或者 Scanner 会定期扫。
				// 这里我们依赖 Scanner 的定期扫描 (默认 5s)
				// 为了演示效果，我们可能需要缩短扫描间隔，或者手动触发。
				// 由于 Scanner 是私有的，我们无法直接触发。
				// 但我们可以通过更新 UserDataProvider 的价格，等待 Scanner 扫到。

				// 3. 随机下单到撮合引擎 (制造流动性)
				// Maker
				intPrice := int64(price)
				tradeEngine.SubmitOrder(&mtrade.Order{
					UserID: rand.Int63n(1000),
					Symbol: "BTC_USDT",
					Side:   mtrade.SideBuy,
					Type:   mtrade.OrderTypeLimit,
					Price:  intPrice - rand.Int63n(50),
					Qty:    rand.Int63n(10) + 1,
				})
				tradeEngine.SubmitOrder(&mtrade.Order{
					UserID: rand.Int63n(1000),
					Symbol: "BTC_USDT",
					Side:   mtrade.SideSell,
					Type:   mtrade.OrderTypeLimit,
					Price:  intPrice + rand.Int63n(50),
					Qty:    rand.Int63n(10) + 1,
				})

				// Taker (偶尔吃单)
				if rand.Float32() < 0.3 {
					side := mtrade.SideBuy
					if rand.Float32() < 0.5 {
						side = mtrade.SideSell
					}
					tradeEngine.SubmitOrder(&mtrade.Order{
						UserID: rand.Int63n(1000),
						Symbol: "BTC_USDT",
						Side:   side,
						Type:   mtrade.OrderTypeMarket,
						Qty:    rand.Int63n(5) + 1,
					})
				}
			}
		}
	}()

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("🛑 Shutting down...")
}
