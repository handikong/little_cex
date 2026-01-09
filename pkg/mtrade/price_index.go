package mtrade

// =============================================================================
// 价格索引接口 (Price Index Interface)
// =============================================================================
//
// 【面试加分】基于接口编程，方便替换实现
//
// 目的：
//   - 当前实现：跳表 (SkipList)
//   - 未来可替换：红黑树 (RedBlackTree)
//   - 测试时可用：Mock 实现

// PriceLevelNode 价格档位节点接口
// 表示订单簿中的一个价格档位
type PriceLevelNode interface {
	// GetPrice 获取价格
	GetPrice() int64

	// GetLevel 获取价格档位（存储订单的队列）
	GetLevel() *RingPriceLevel
}

// PriceIndex 价格索引接口
// 【核心接口】订单簿使用这个接口管理价格档位
type PriceIndex interface {
	// Find 查找指定价格的节点
	// 返回 nil 表示不存在
	Find(price int64) PriceLevelNode

	// Insert 插入价格档位（如果不存在则创建）
	// 返回对应的节点
	Insert(price int64) PriceLevelNode

	// Delete 删除价格档位
	// 返回被删除的节点，不存在返回 nil
	Delete(price int64) PriceLevelNode

	// First 获取第一个节点（最优价格）
	// 买盘：最高价，卖盘：最低价
	First() PriceLevelNode

	// Len 返回价格档位数量
	Len() int

	// IsEmpty 是否为空
	IsEmpty() bool

	// ForEach 遍历所有节点
	// fn 返回 false 时停止遍历
	ForEach(fn func(PriceLevelNode) bool)

	// GetTopN 获取前 N 个价格档位
	GetTopN(n int) []PriceLevelNode
}
