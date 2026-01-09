package mtrade

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Engine 测试
// =============================================================================

// 测试辅助函数
func mustNewEngine(t testing.TB, config EngineConfig) *Engine {
	engine, err := NewEngine(config)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	return engine
}

func TestEngine_StartStop(t *testing.T) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(t, config)

	ctx := context.Background()
	engine.Start(ctx)

	time.Sleep(10 * time.Millisecond)

	engine.Stop()
}

func TestEngine_SubmitOrder(t *testing.T) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(t, config)

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 提交订单
	order := &Order{
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}

	ok := engine.SubmitOrder(order)
	if !ok {
		t.Error("expected order submitted successfully")
	}

	// 等待处理
	time.Sleep(50 * time.Millisecond)

	stats := engine.GetStats()
	if stats.OrdersReceived != 1 {
		t.Errorf("expected 1 order received, got %d", stats.OrdersReceived)
	}
	if stats.OrdersMatched != 1 {
		t.Errorf("expected 1 order matched, got %d", stats.OrdersMatched)
	}
}

func TestEngine_MatchTrade(t *testing.T) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(t, config)

	// 记录事件
	var tradeCount int64
	engine.OnEvent(func(e Event) {
		if e.Type == EventTrade {
			atomic.AddInt64(&tradeCount, 1)
		}
	})

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 提交卖单（Maker）
	maker := &Order{
		Side:   SideSell,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}
	engine.SubmitOrder(maker)

	// 等待 Maker 挂单
	time.Sleep(20 * time.Millisecond)

	// 提交买单（Taker）
	taker := &Order{
		Side:   SideBuy,
		Price:  50000,
		Qty:    5,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}
	engine.SubmitOrder(taker)

	// 等待撮合
	time.Sleep(50 * time.Millisecond)

	stats := engine.GetStats()
	if stats.TradesExecuted != 1 {
		t.Errorf("expected 1 trade, got %d", stats.TradesExecuted)
	}

	if atomic.LoadInt64(&tradeCount) != 1 {
		t.Errorf("expected 1 trade event, got %d", tradeCount)
	}
}

func TestEngine_CancelOrder(t *testing.T) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(t, config)

	// 记录取消事件
	var cancelCount int64
	engine.OnEvent(func(e Event) {
		if e.Type == EventOrderCanceled {
			atomic.AddInt64(&cancelCount, 1)
		}
	})

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 提交订单
	order := &Order{
		ID:     12345,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}
	engine.SubmitOrder(order)

	// 等待挂单
	time.Sleep(20 * time.Millisecond)

	// 取消订单
	engine.CancelOrder(12345)

	// 等待处理
	time.Sleep(50 * time.Millisecond)

	stats := engine.GetStats()
	if stats.OrdersCanceled != 1 {
		t.Errorf("expected 1 cancel, got %d", stats.OrdersCanceled)
	}

	if atomic.LoadInt64(&cancelCount) != 1 {
		t.Errorf("expected 1 cancel event, got %d", cancelCount)
	}
}

func TestEngine_MultipleHandlers(t *testing.T) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(t, config)

	// 注册多个 handler
	var handler1Count, handler2Count int64
	engine.OnEvent(func(e Event) {
		atomic.AddInt64(&handler1Count, 1)
	})
	engine.OnEvent(func(e Event) {
		atomic.AddInt64(&handler2Count, 1)
	})

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 提交订单触发事件
	engine.SubmitOrder(&Order{
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	})

	// 等待处理
	time.Sleep(50 * time.Millisecond)

	// 两个 handler 都应该收到事件
	if atomic.LoadInt64(&handler1Count) == 0 {
		t.Error("handler1 did not receive event")
	}
	if atomic.LoadInt64(&handler2Count) == 0 {
		t.Error("handler2 did not receive event")
	}
}

// =============================================================================
// Engine 基准测试
// =============================================================================

func BenchmarkEngine_SubmitOrder(b *testing.B) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(b, config)

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 预热
	time.Sleep(10 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := &Order{
			ID:     int64(i),
			Side:   SideBuy,
			Price:  int64(50000 + i%100),
			Qty:    10,
			Symbol: "BTC_USDT",
			Type:   OrderTypeLimit,
		}
		engine.SubmitOrder(order)
	}
}

func BenchmarkEngine_MatchThroughput(b *testing.B) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(b, config)

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 预先添加 Maker 订单
	for i := 0; i < 100; i++ {
		engine.SubmitOrder(&Order{
			ID:     int64(i),
			Side:   SideSell,
			Price:  int64(50000 + i),
			Qty:    1000000,
			Symbol: "BTC_USDT",
			Type:   OrderTypeLimit,
		})
	}

	// 等待 Maker 挂单
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		taker := &Order{
			ID:     int64(1000000 + i),
			Side:   SideBuy,
			Price:  50000,
			Qty:    10,
			Symbol: "BTC_USDT",
			Type:   OrderTypeLimit,
		}
		engine.SubmitOrder(taker)
	}

	// 等待所有订单处理完成
	b.StopTimer()
	time.Sleep(100 * time.Millisecond)
}

func BenchmarkEngine_Concurrent(b *testing.B) {
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(b, config)

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	time.Sleep(10 * time.Millisecond)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			order := &Order{
				ID:     int64(time.Now().UnixNano() + int64(i)),
				Side:   SideBuy,
				Price:  int64(50000 + i%100),
				Qty:    10,
				Symbol: "BTC_USDT",
				Type:   OrderTypeLimit,
			}
			engine.SubmitOrder(order)
			i++
		}
	})
}

func BenchmarkEngine_FullPipeline(b *testing.B) {
	// 完整流程：提交订单 → 撮合 → 事件处理
	config := DefaultEngineConfig("BTC_USDT")
	engine := mustNewEngine(b, config)

	var eventCount int64
	engine.OnEvent(func(e Event) {
		atomic.AddInt64(&eventCount, 1)
	})

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// 预先添加 Maker
	for i := 0; i < 100; i++ {
		engine.SubmitOrder(&Order{
			ID:     int64(i),
			Side:   SideSell,
			Price:  int64(50000 + i),
			Qty:    1000000,
			Symbol: "BTC_USDT",
			Type:   OrderTypeLimit,
		})
	}
	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taker := &Order{
				ID:     int64(1000000 + idx),
				Side:   SideBuy,
				Price:  50000,
				Qty:    10,
				Symbol: "BTC_USDT",
				Type:   OrderTypeLimit,
			}
			engine.SubmitOrder(taker)
		}(i)
	}

	wg.Wait()
	b.StopTimer()
	time.Sleep(100 * time.Millisecond)
}
