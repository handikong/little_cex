package mtrade

import (
	"testing"
	"time"
)

// =============================================================================
// 跳表测试
// =============================================================================

func TestSkipList_Basic(t *testing.T) {
	sl := NewSkipList(true) // 升序

	// 插入测试
	sl.Insert(100)
	sl.Insert(50)
	sl.Insert(150)

	if sl.Len() != 3 {
		t.Errorf("expected len 3, got %d", sl.Len())
	}

	// 第一个应该是 50（升序）
	first := sl.First()
	if first == nil || first.GetPrice() != 50 {
		t.Errorf("expected first price 50, got %v", first)
	}

	// 查找测试
	node := sl.Find(100)
	if node == nil || node.GetPrice() != 100 {
		t.Errorf("expected to find price 100")
	}

	// 删除测试
	sl.Delete(50)
	if sl.Len() != 2 {
		t.Errorf("expected len 2 after delete, got %d", sl.Len())
	}

	first = sl.First()
	if first == nil || first.GetPrice() != 100 {
		t.Errorf("expected first price 100 after delete, got %v", first)
	}
}

func TestSkipList_Descending(t *testing.T) {
	sl := NewSkipList(false) // 降序（买盘）

	sl.Insert(100)
	sl.Insert(50)
	sl.Insert(150)

	// 第一个应该是 150（降序）
	first := sl.First()
	if first == nil || first.GetPrice() != 150 {
		t.Errorf("expected first price 150 (descending), got %v", first)
	}
}

// =============================================================================
// 订单簿测试
// =============================================================================

func TestOrderBook_AddAndCancel(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")

	// 添加买单
	order := &Order{
		ID:     1,
		UserID: 100,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
	}
	ob.AddOrder(order)
	ob.UpdateSnapshot() // 更新快照

	// 检查最优买价
	bid, ok := ob.BestBid()
	if !ok || bid != 50000 {
		t.Errorf("expected best bid 50000, got %d", bid)
	}

	// 取消订单
	cancelled := ob.CancelOrder(1)
	if cancelled == nil {
		t.Error("expected to cancel order")
	}
	ob.UpdateSnapshot() // 更新快照

	// 检查订单簿为空
	_, ok = ob.BestBid()
	if ok {
		t.Error("expected no best bid after cancel")
	}
}

func TestOrderBook_Depth(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")

	// 添加多个价位的买单
	for i := int64(0); i < 5; i++ {
		ob.AddOrder(&Order{
			ID:     i + 1,
			Side:   SideBuy,
			Price:  50000 - i*100,
			Qty:    10,
			Symbol: "BTC_USDT",
		})
	}

	// 更新快照（无锁设计需要手动更新）
	ob.UpdateSnapshot()

	bids, _ := ob.Depth(3)
	if len(bids) != 3 {
		t.Errorf("expected 3 bid levels, got %d", len(bids))
	}

	// 买盘降序，第一个应该是最高价
	if bids[0].Price != 50000 {
		t.Errorf("expected first bid price 50000, got %d", bids[0].Price)
	}
}

// =============================================================================
// 撮合测试
// =============================================================================

func TestMatcher_SimpleTrade(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")
	matcher := NewMatcher(ob)

	// 先添加卖单（Maker）
	ob.AddOrder(&Order{
		ID:     1,
		UserID: 100,
		Side:   SideSell,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	})

	// 下买单（Taker）
	taker := &Order{
		ID:        2,
		UserID:    200,
		Side:      SideBuy,
		Price:     50000,
		Qty:       5,
		Symbol:    "BTC_USDT",
		Type:      OrderTypeLimit,
		CreatedAt: time.Now().UnixNano(),
	}

	result := matcher.Match(taker)

	// 检查成交
	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}

	trade := result.Trades[0]
	if trade.Qty != 5 {
		t.Errorf("expected trade qty 5, got %d", trade.Qty)
	}
	if trade.Price != 50000 {
		t.Errorf("expected trade price 50000, got %d", trade.Price)
	}

	// Taker 完全成交
	if !result.FullyFilled {
		t.Error("expected taker fully filled")
	}
}

func TestMatcher_PartialFill(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")
	matcher := NewMatcher(ob)

	// Maker 只有 5
	ob.AddOrder(&Order{
		ID:     1,
		Side:   SideSell,
		Price:  50000,
		Qty:    5,
		Symbol: "BTC_USDT",
	})

	// Taker 要买 10
	taker := &Order{
		ID:     2,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}

	result := matcher.Match(taker)

	// 部分成交
	if result.FullyFilled {
		t.Error("expected partial fill")
	}
	if result.FilledQty != 5 {
		t.Errorf("expected filled qty 5, got %d", result.FilledQty)
	}
	if result.RemainingQty != 5 {
		t.Errorf("expected remaining qty 5, got %d", result.RemainingQty)
	}
}

func TestMatcher_MultiplePrice(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")
	matcher := NewMatcher(ob)

	// 多个价位的卖单
	ob.AddOrder(&Order{ID: 1, Side: SideSell, Price: 50000, Qty: 5, Symbol: "BTC_USDT"})
	ob.AddOrder(&Order{ID: 2, Side: SideSell, Price: 50100, Qty: 5, Symbol: "BTC_USDT"})
	ob.AddOrder(&Order{ID: 3, Side: SideSell, Price: 50200, Qty: 5, Symbol: "BTC_USDT"})

	// 市价买入 12，会吃 3 个价位
	taker := &Order{
		ID:     4,
		Side:   SideBuy,
		Qty:    12,
		Symbol: "BTC_USDT",
		Type:   OrderTypeMarket,
	}

	result := matcher.Match(taker)

	// 应该有 3 笔成交
	if len(result.Trades) != 3 {
		t.Errorf("expected 3 trades, got %d", len(result.Trades))
	}

	// 成交 5+5+2 = 12
	if result.FilledQty != 12 {
		t.Errorf("expected filled qty 12, got %d", result.FilledQty)
	}
}

func TestMatcher_PriceNotMatch(t *testing.T) {
	ob := NewOrderBook("BTC_USDT")
	matcher := NewMatcher(ob)

	// 卖单 @ 50100
	ob.AddOrder(&Order{ID: 1, Side: SideSell, Price: 50100, Qty: 10, Symbol: "BTC_USDT"})

	// 买单 @ 50000（价格不匹配）
	taker := &Order{
		ID:     2,
		Side:   SideBuy,
		Price:  50000,
		Qty:    10,
		Symbol: "BTC_USDT",
		Type:   OrderTypeLimit,
	}

	result := matcher.Match(taker)

	// 不应该成交
	if len(result.Trades) != 0 {
		t.Errorf("expected no trades, got %d", len(result.Trades))
	}
}

// =============================================================================
// 基准测试
// =============================================================================

func BenchmarkSkipList_Insert(b *testing.B) {
	sl := NewSkipList(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Insert(int64(i))
	}
}

func BenchmarkSkipList_Find(b *testing.B) {
	sl := NewSkipList(true)

	// 预先插入数据
	for i := int64(0); i < 10000; i++ {
		sl.Insert(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Find(int64(i % 10000))
	}
}

func BenchmarkOrderBook_AddOrder(b *testing.B) {
	ob := NewOrderBook("BTC_USDT")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := &Order{
			ID:     int64(i),
			Side:   SideBuy,
			Price:  int64(50000 + i%100),
			Qty:    10,
			Symbol: "BTC_USDT",
		}
		ob.AddOrder(order)
	}
}

func BenchmarkMatcher_Match(b *testing.B) {
	ob := NewOrderBook("BTC_USDT")
	matcher := NewMatcher(ob)

	// 预先添加卖单（大量数量，避免被消耗完）
	for i := 0; i < 100; i++ {
		ob.AddOrder(&Order{
			ID:     int64(i),
			Side:   SideSell,
			Price:  int64(50000 + i),
			Qty:    1000000, // 大量，不会被消耗完
			Symbol: "BTC_USDT",
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		taker := &Order{
			ID:     int64(1000000 + i),
			Side:   SideBuy,
			Price:  50000, // 只匹配第一档
			Qty:    10,
			Symbol: "BTC_USDT",
			Type:   OrderTypeLimit,
		}
		result := matcher.Match(taker)
		PutMatchResult(result) // 归还对象池
	}
}

func BenchmarkMatcher_FullCycle(b *testing.B) {
	// 模拟完整撮合周期：下单 → 撮合 → 挂单
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ob := NewOrderBook("BTC_USDT")
		matcher := NewMatcher(ob)

		// 添加 100 个 Maker
		for j := 0; j < 100; j++ {
			ob.AddOrder(&Order{
				ID:     int64(j),
				Side:   SideSell,
				Price:  int64(50000 + j),
				Qty:    10,
				Symbol: "BTC_USDT",
			})
		}

		// 撮合 100 个 Taker
		for j := 0; j < 100; j++ {
			taker := &Order{
				ID:     int64(1000 + j),
				Side:   SideBuy,
				Price:  int64(50000 + j),
				Qty:    5,
				Symbol: "BTC_USDT",
				Type:   OrderTypeLimit,
			}
			matcher.ProcessOrder(taker)
		}
	}
}
