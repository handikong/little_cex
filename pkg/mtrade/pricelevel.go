package mtrade

// =============================================================================
// 环形队列优化版 PriceLevel
// =============================================================================
//
// 【面试进阶】Ring Buffer 实现 O(1) 头部删除
//
// 普通切片问题：
//   头部删除需要移动所有元素 O(n)
//   slice = slice[1:] 会导致内存泄漏
//
// 环形队列解决：
//   用 head/tail 指针标记有效区域
//   头部删除只需移动 head 指针 O(1)

const (
	// DefaultRingCapacity 默认环形缓冲区容量
	// 【性能】容量必须是 2 的幂，用位运算取模更快
	DefaultRingCapacity = 64
)

// RingPriceLevel 环形队列版价格档位
// 面试高频】手撕环形队列
type RingPriceLevel struct {
	Price    int64    // 价格
	TotalQty int64    // 总数量
	orders   []*Order // 环形缓冲区
	head     int      // 头指针（下一个出队位置）
	tail     int      // 尾指针（下一个入队位置）
	count    int      // 当前元素数量
	mask     int      // 容量掩码（用于取模）
}

// NewRingPriceLevel 创建环形队列价格档位
func NewRingPriceLevel(price int64) *RingPriceLevel {
	cap := DefaultRingCapacity
	return &RingPriceLevel{
		Price:  price,
		orders: make([]*Order, cap),
		mask:   cap - 1, // 容量是 2^n，mask = 2^n - 1
	}
}

// =============================================================================
// 订单操作
// =============================================================================
// AddOrder 添加订单到队尾
// 【面试】时间复杂度：O(1)，可能触发扩容 O(n)
func (pl *RingPriceLevel) AddOrder(order *Order) {
	// 检查是否需要扩容
	if pl.count == len(pl.orders) {
		pl.grow()
	}

	// 入队
	pl.orders[pl.tail] = order
	pl.tail = (pl.tail + 1) & pl.mask // 等价于 (tail + 1) % len
	pl.count++
	pl.TotalQty += order.RemainingQty()
}

// PopFront 弹出队首订单
// 【面试】时间复杂度：O(1) ← 这是关键优化！
func (pl *RingPriceLevel) PopFront() *Order {
	if pl.count == 0 {
		return nil
	}

	order := pl.orders[pl.head]
	pl.orders[pl.head] = nil // 帮助 GC
	pl.head = (pl.head + 1) & pl.mask
	pl.count--
	pl.TotalQty -= order.RemainingQty()

	return order
}

// Front 获取队首订单（不移除）
// 时间复杂度：O(1)
func (pl *RingPriceLevel) Front() *Order {
	if pl.count == 0 {
		return nil
	}
	return pl.orders[pl.head]
}

// RemoveOrder 从队列中移除指定订单
// 时间复杂度：O(n)，需要遍历查找
// 这比普通切片复杂，因为要处理环形结构
func (pl *RingPriceLevel) RemoveOrder(orderID int64) *Order {
	if pl.count == 0 {
		return nil
	}

	// 遍历查找
	for i := 0; i < pl.count; i++ {
		idx := (pl.head + i) & pl.mask
		if pl.orders[idx].ID == orderID {
			return pl.removeAt(i)
		}
	}
	return nil
}

// removeAt 移除指定位置的元素（相对于 head 的偏移）
func (pl *RingPriceLevel) removeAt(offset int) *Order {
	idx := (pl.head + offset) & pl.mask
	removed := pl.orders[idx]
	pl.TotalQty -= removed.RemainingQty()

	// 判断移动前半部分还是后半部分（选择移动少的）
	if offset < pl.count/2 {
		// 移动前半部分（向后移动，腾出头部）
		for i := offset; i > 0; i-- {
			curr := (pl.head + i) & pl.mask
			prev := (pl.head + i - 1) & pl.mask
			pl.orders[curr] = pl.orders[prev]
		}
		pl.orders[pl.head] = nil
		pl.head = (pl.head + 1) & pl.mask
	} else {
		// 移动后半部分（向前移动，腾出尾部）
		for i := offset; i < pl.count-1; i++ {
			curr := (pl.head + i) & pl.mask
			next := (pl.head + i + 1) & pl.mask
			pl.orders[curr] = pl.orders[next]
		}
		pl.tail = (pl.tail - 1 + len(pl.orders)) & pl.mask
		pl.orders[pl.tail] = nil
	}

	pl.count--
	return removed
}

// Len 返回订单数量
func (pl *RingPriceLevel) Len() int {
	return pl.count
}

// IsEmpty 是否为空
func (pl *RingPriceLevel) IsEmpty() bool {
	return pl.count == 0
}

// MatchQty 计算可成交数量
func (pl *RingPriceLevel) MatchQty(takerQty int64) int64 {
	if takerQty >= pl.TotalQty {
		return pl.TotalQty
	}
	return takerQty
}

// grow 扩容（容量翻倍）
// 【面试】扩容策略：翻倍扩容，保持 2 的幂
func (pl *RingPriceLevel) grow() {
	newCap := len(pl.orders) * 2
	newOrders := make([]*Order, newCap)

	// 复制元素到新数组（从 head 到 tail）
	for i := 0; i < pl.count; i++ {
		idx := (pl.head + i) & pl.mask
		newOrders[i] = pl.orders[idx]
	}

	pl.orders = newOrders
	pl.head = 0
	pl.tail = pl.count
	pl.mask = newCap - 1
}

// ForEach 遍历所有订单
// 【面试】如何正确遍历环形队列
func (pl *RingPriceLevel) ForEach(fn func(*Order)) {
	for i := 0; i < pl.count; i++ {
		idx := (pl.head + i) & pl.mask
		fn(pl.orders[idx])
	}
}

/*
Q: 价格档位用切片还是链表？

A（标准回答）：

我们选择切片，原因有三：

Cache 友好性：连续内存，遍历时 cache hit rate 高，撮合时遍历性能比链表快 10 倍
操作特征：撮合（遍历）远比取消（删除）频繁，应该优化高频操作
实际 n 较小：同一价格的订单数通常只有几十个，O(n) 删除开销可忽略
如果取消操作确实成为瓶颈，可以用惰性删除或双向链表 + 节点指针优化

*/

// =============================================================================
// 优化版本：使用环形缓冲区避免头部删除的 O(n) 开销
// =============================================================================
//
// 【面试进阶】生产环境可以用 Ring Buffer 实现 O(1) 头部删除
// type RingPriceLevel struct {
//     Price    int64
//     Orders   []Order  // 固定大小环形缓冲区
//     head     int      // 头指针
//     tail     int      // 尾指针
//     count    int      // 当前数量
// }
//
// 但增加了实现复杂度，我们先用简单切片版本
