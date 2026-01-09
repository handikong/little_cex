package mtrade

import (
	"sync/atomic"
	"unsafe"
)

// =============================================================================
// 订单簿 (Order Book) - 无锁设计
// =============================================================================
//
// 【面试高频】无锁撮合
//
// 设计原则：
//   1. 撮合线程独享 OrderBook，内部操作无锁
//   2. 外部查询通过快照机制，使用 atomic.Pointer
//   3. 完全避免锁竞争

// OrderBook 订单簿（无锁版本）
// 【核心设计】由单个 goroutine (matchLoop) 独占访问，无需锁
type OrderBook struct {
	Symbol string // 交易对

	bids PriceIndex // 买盘（价格降序）
	asks PriceIndex // 卖盘（价格升序）

	// 订单索引：OrderID → Order
	orderIndex map[int64]*Order

	// 快照（供外部查询，原子更新）
	snapshot atomic.Pointer[OrderBookSnapshot]
}

// OrderBookSnapshot 订单簿快照（只读）
// 【面试】外部查询使用快照，无锁读
type OrderBookSnapshot struct {
	BestBid   int64
	BestAsk   int64
	Spread    int64
	BidLevels int
	AskLevels int
	BidDepth  []DepthLevel
	AskDepth  []DepthLevel
}

// DepthLevel 深度档位
type DepthLevel struct {
	Price    int64
	Quantity int64
	Orders   int
}

// NewOrderBook 创建订单簿
func NewOrderBook(symbol string) *OrderBook {
	ob := &OrderBook{
		Symbol:     symbol,
		bids:       NewSkipList(false), // 降序
		asks:       NewSkipList(true),  // 升序
		orderIndex: make(map[int64]*Order),
	}
	// 初始化空快照
	ob.snapshot.Store(&OrderBookSnapshot{})
	return ob
}

// =============================================================================
// 订单操作（无锁，仅供 matchLoop 调用）
// =============================================================================

// AddOrder 添加订单到订单簿
// 【无锁】仅由 matchLoop 调用
func (ob *OrderBook) AddOrder(order *Order) bool {
	// 检查订单是否已存在
	if _, exists := ob.orderIndex[order.ID]; exists {
		return false
	}

	// 获取对应的价格索引
	priceIndex := ob.getSideIndex(order.Side)

	// 插入或获取价格档位
	node := priceIndex.Insert(order.Price)
	level := node.GetLevel()

	// 添加订单到价格档位
	level.AddOrder(order)

	// 添加到订单索引
	ob.orderIndex[order.ID] = order
	order.Status = OrderStatusNew

	return true
}

// CancelOrder 取消订单
// 【无锁】仅由 matchLoop 调用
func (ob *OrderBook) CancelOrder(orderID int64) *Order {
	// 1. 从索引中查找订单
	order, exists := ob.orderIndex[orderID]
	if !exists {
		return nil
	}

	// 2. 获取对应的价格索引
	priceIndex := ob.getSideIndex(order.Side)

	// 3. 找到价格档位
	node := priceIndex.Find(order.Price)
	if node == nil {
		return nil
	}

	// 4. 从价格档位中移除订单
	level := node.GetLevel()
	level.RemoveOrder(orderID)

	// 5. 如果价格档位空了，删除它
	if level.IsEmpty() {
		priceIndex.Delete(order.Price)
	}

	// 6. 从索引中移除
	delete(ob.orderIndex, orderID)
	order.Status = OrderStatusCanceled

	return order
}

// GetOrder 获取订单
// 【无锁】仅由 matchLoop 调用
func (ob *OrderBook) GetOrder(orderID int64) *Order {
	return ob.orderIndex[orderID]
}

// GetAllOrders 获取所有订单（用于 Checkpoint）
// 【无锁】仅由 matchLoop 调用
func (ob *OrderBook) GetAllOrders() []*Order {
	orders := make([]*Order, 0, len(ob.orderIndex))
	for _, order := range ob.orderIndex {
		orders = append(orders, order)
	}
	return orders
}

// =============================================================================
// 撮合辅助方法（无锁）
// =============================================================================

// GetOppositeIndex 获取对手盘索引
func (ob *OrderBook) GetOppositeIndex(side Side) PriceIndex {
	if side == SideBuy {
		return ob.asks
	}
	return ob.bids
}

// getSideIndex 获取对应方向的价格索引
func (ob *OrderBook) getSideIndex(side Side) PriceIndex {
	if side == SideBuy {
		return ob.bids
	}
	return ob.asks
}

// RemoveFromLevel 从价格档位移除订单
// 【无锁】仅由 matchLoop 调用
func (ob *OrderBook) RemoveFromLevel(order *Order) {
	priceIndex := ob.getSideIndex(order.Side)
	node := priceIndex.Find(order.Price)

	if node != nil {
		level := node.GetLevel()
		level.PopFront()

		if level.IsEmpty() {
			priceIndex.Delete(order.Price)
		}
	}

	delete(ob.orderIndex, order.ID)
}

// =============================================================================
// 快照机制（无锁读）
// =============================================================================

// UpdateSnapshot 更新快照
// 【无锁】仅由 matchLoop 调用，撮合后执行
func (ob *OrderBook) UpdateSnapshot() {
	snap := &OrderBookSnapshot{
		BidLevels: ob.bids.Len(),
		AskLevels: ob.asks.Len(),
		BidDepth:  ob.getDepth(ob.bids, 20),
		AskDepth:  ob.getDepth(ob.asks, 20),
	}

	if node := ob.bids.First(); node != nil {
		snap.BestBid = node.GetPrice()
	}
	if node := ob.asks.First(); node != nil {
		snap.BestAsk = node.GetPrice()
	}
	if snap.BestBid > 0 && snap.BestAsk > 0 {
		snap.Spread = snap.BestAsk - snap.BestBid
	}

	ob.snapshot.Store(snap)
}

// GetSnapshot 获取快照（无锁读）
// 【线程安全】可从任意 goroutine 调用
func (ob *OrderBook) GetSnapshot() *OrderBookSnapshot {
	return ob.snapshot.Load()
}

// getDepth 获取一侧的深度
func (ob *OrderBook) getDepth(index PriceIndex, n int) []DepthLevel {
	nodes := index.GetTopN(n)
	result := make([]DepthLevel, len(nodes))

	for i, node := range nodes {
		level := node.GetLevel()
		result[i] = DepthLevel{
			Price:    node.GetPrice(),
			Quantity: level.TotalQty,
			Orders:   level.Len(),
		}
	}

	return result
}

// =============================================================================
// 便捷查询方法（通过快照，无锁）
// =============================================================================

// BestBid 获取最优买价（从快照读取）
func (ob *OrderBook) BestBid() (int64, bool) {
	snap := ob.GetSnapshot()
	if snap.BestBid == 0 {
		return 0, false
	}
	return snap.BestBid, true
}

// BestAsk 获取最优卖价（从快照读取）
func (ob *OrderBook) BestAsk() (int64, bool) {
	snap := ob.GetSnapshot()
	if snap.BestAsk == 0 {
		return 0, false
	}
	return snap.BestAsk, true
}

// Spread 获取价差（从快照读取）
func (ob *OrderBook) Spread() (int64, bool) {
	snap := ob.GetSnapshot()
	if snap.Spread == 0 {
		return 0, false
	}
	return snap.Spread, true
}

// Depth 获取深度（从快照读取）
func (ob *OrderBook) Depth(n int) (bids, asks []DepthLevel) {
	snap := ob.GetSnapshot()

	// 返回快照中的前 n 档
	if n > len(snap.BidDepth) {
		n = len(snap.BidDepth)
	}
	bids = snap.BidDepth[:n]

	m := n
	if m > len(snap.AskDepth) {
		m = len(snap.AskDepth)
	}
	asks = snap.AskDepth[:m]

	return bids, asks
}

// GetStats 获取统计信息（从快照读取）
func (ob *OrderBook) GetStats() OrderBookStats {
	snap := ob.GetSnapshot()
	return OrderBookStats{
		BidLevels: snap.BidLevels,
		AskLevels: snap.AskLevels,
		BestBid:   snap.BestBid,
		BestAsk:   snap.BestAsk,
		Spread:    snap.Spread,
	}
}

// OrderBookStats 订单簿统计
type OrderBookStats struct {
	BidLevels   int
	AskLevels   int
	TotalOrders int
	BestBid     int64
	BestAsk     int64
	Spread      int64
}

// 确保 atomic.Pointer 可用（Go 1.19+）
var _ = unsafe.Pointer(nil)
