package mtrade

import (
	"sync"
	"time"
)

// =============================================================================
// 对象池优化
// =============================================================================

// 【性能优化】MatchResult 对象池，减少内存分配
var matchResultPool = sync.Pool{
	New: func() interface{} {
		return &MatchResult{
			Trades: make([]Trade, 0, 8), // 预分配容量
		}
	},
}

// getMatchResult 从对象池获取 MatchResult
func getMatchResult() *MatchResult {
	result := matchResultPool.Get().(*MatchResult)
	// 重置状态
	result.Trades = result.Trades[:0]
	result.TakerOrder = nil
	result.FilledQty = 0
	result.RemainingQty = 0
	result.FullyFilled = false
	return result
}

// PutMatchResult 归还 MatchResult 到对象池
// 【注意】调用者在使用完结果后应该调用此方法
func PutMatchResult(result *MatchResult) {
	if result != nil {
		matchResultPool.Put(result)
	}
}

// =============================================================================
// 成交事件 (Trade Event)
// =============================================================================

// Trade 成交记录
// 【面试】一次撮合可能产生多个 Trade（吃多个价位）
type Trade struct {
	ID        int64  // 成交 ID
	Symbol    string // 交易对
	Price     int64  // 成交价格
	Qty       int64  // 成交数量
	TakerID   int64  // Taker 订单 ID
	MakerID   int64  // Maker 订单 ID
	TakerSide Side   // Taker 方向
	Timestamp int64  // 成交时间
}

// =============================================================================
// 撮合结果
// =============================================================================

// MatchResult 撮合结果
type MatchResult struct {
	Trades       []Trade // 成交记录
	TakerOrder   *Order  // Taker 订单（更新后）
	FilledQty    int64   // 本次成交总量
	RemainingQty int64   // 剩余未成交量
	FullyFilled  bool    // 是否完全成交
}

// =============================================================================
// 撮合器 (Matcher)
// =============================================================================

// Matcher 撮合器
// 【面试核心】实现价格优先、时间优先的撮合算法
type Matcher struct {
	orderBook *OrderBook
	tradeSeq  int64 // 成交序列号
}

// NewMatcher 创建撮合器
func NewMatcher(ob *OrderBook) *Matcher {
	return &Matcher{
		orderBook: ob,
	}
}

// =============================================================================
// 核心撮合逻辑
// =============================================================================

// Match 撮合订单
// 【面试高频】这是最核心的函数
//
// 流程：
// 1. 获取对手盘
// 2. 按价格优先、时间优先遍历
// 3. 逐个价位撮合
// 4. 更新订单状态
// 5. 返回成交记录
func (m *Matcher) Match(taker *Order) *MatchResult {
	// 【优化】从对象池获取，避免每次分配
	result := getMatchResult()
	result.TakerOrder = taker

	// 获取对手盘
	oppositeIndex := m.orderBook.GetOppositeIndex(taker.Side)

	// 循环撮合，直到：
	// 1. Taker 订单完全成交
	// 2. 对手盘没有可成交的订单
	for taker.RemainingQty() > 0 {
		// 获取对手盘最优价格
		bestNode := oppositeIndex.First()
		if bestNode == nil {
			break // 对手盘空了
		}

		// 检查价格是否可以成交
		if !m.canMatch(taker, bestNode.GetPrice()) {
			break // 价格不匹配
		}

		// 撮合这个价位
		m.matchAtLevel(taker, bestNode.GetLevel(), result)

		// 如果价位空了，删除它
		if bestNode.GetLevel().IsEmpty() {
			oppositeIndex.Delete(bestNode.GetPrice())
		}
	}

	// 更新结果
	result.FilledQty = taker.FilledQty
	result.RemainingQty = taker.RemainingQty()
	result.FullyFilled = taker.IsFilled()

	// 更新 Taker 状态
	if taker.IsFilled() {
		taker.Status = OrderStatusFilled
	} else if taker.FilledQty > 0 {
		taker.Status = OrderStatusPartiallyFilled
	}

	return result
}

// canMatch 检查价格是否可以成交
// 【面试】买单：买价 >= 卖价；卖单：卖价 <= 买价
func (m *Matcher) canMatch(taker *Order, makerPrice int64) bool {
	if taker.Type == OrderTypeMarket {
		return true // 市价单总是可以成交
	}

	if taker.Side == SideBuy {
		return taker.Price >= makerPrice
	}
	return taker.Price <= makerPrice
}

// matchAtLevel 在一个价位上撮合
// 【面试】时间优先：FIFO 队列
func (m *Matcher) matchAtLevel(taker *Order, level *RingPriceLevel, result *MatchResult) {
	for taker.RemainingQty() > 0 && !level.IsEmpty() {
		// 获取队首订单（最早的 Maker）
		maker := level.Front()

		// 计算成交数量
		matchQty := min(taker.RemainingQty(), maker.RemainingQty())

		// 更新订单
		taker.FilledQty += matchQty
		maker.FilledQty += matchQty

		// 生成成交记录
		trade := Trade{
			ID:        m.nextTradeID(),
			Symbol:    taker.Symbol,
			Price:     maker.Price, // 成交价 = Maker 价格
			Qty:       matchQty,
			TakerID:   taker.ID,
			MakerID:   maker.ID,
			TakerSide: taker.Side,
			Timestamp: time.Now().UnixNano(),
		}
		result.Trades = append(result.Trades, trade)

		// 如果 Maker 完全成交，从队列移除
		if maker.IsFilled() {
			maker.Status = OrderStatusFilled
			level.PopFront()
			delete(m.orderBook.orderIndex, maker.ID)
		} else {
			maker.Status = OrderStatusPartiallyFilled
		}
	}
}

// nextTradeID 生成成交 ID
func (m *Matcher) nextTradeID() int64 {
	m.tradeSeq++
	return time.Now().UnixNano()/1000000<<20 | (m.tradeSeq & 0xFFFFF)
}

// =============================================================================
// 不同订单类型的处理
// =============================================================================

// ProcessOrder 处理订单（完整流程）
// 【面试】根据订单类型决定撮合后的行为
func (m *Matcher) ProcessOrder(order *Order) *MatchResult {
	// 1. 尝试撮合
	result := m.Match(order)

	// 2. 根据订单类型处理剩余
	switch order.Type {
	case OrderTypeLimit, OrderTypeGTC:
		// 限价单：剩余挂单
		if !result.FullyFilled {
			m.orderBook.AddOrder(order)
		}

	case OrderTypeMarket, OrderTypeIOC:
		// 市价单/IOC：剩余取消
		if !result.FullyFilled {
			order.Status = OrderStatusCanceled
		}

	case OrderTypeFOK:
		// FOK：如果不能完全成交，全部取消
		// 注意：需要在撮合前检查，这里简化处理
		if !result.FullyFilled {
			// 回滚已成交的（实际需要更复杂的处理）
			order.Status = OrderStatusCanceled
		}

	case OrderTypePostOnly:
		// Post Only：如果会立即成交，拒绝
		// 注意：需要在撮合前检查
		if len(result.Trades) > 0 {
			order.Status = OrderStatusRejected
		} else {
			m.orderBook.AddOrder(order)
		}
	}

	return result
}

// =============================================================================
// 辅助函数
// =============================================================================

// min 返回较小值
func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// OrderTypeGTC 添加到 order.go 中的常量
const OrderTypeGTC OrderType = 5
