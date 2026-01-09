package mtrade

import (
	"math/rand"
)

// =============================================================================
// 跳表 (Skip List) - 实现 PriceIndex 接口
// =============================================================================
//
// 【面试高频】手撕跳表，Redis 的 ZSet 就是用跳表实现的
//
// 跳表结构示意：
//
// Level 3:  Head ─────────────────────────────────► 50100 ────────────► nil
// Level 2:  Head ──────────────► 49950 ────────────► 50100 ────────────► nil
// Level 1:  Head ──────► 49900 ──► 49950 ──────────► 50100 ──► 50200 ──► nil
// Level 0:  Head ──► 49800 ──► 49900 ──► 49950 ──► 50000 ──► 50100 ──► 50200 ──► nil

const (
	// MaxLevel 跳表最大层数
	// 【面试】2^32 可以支持 40 亿个元素
	MaxLevel = 32

	// SkipListP 节点晋升概率
	// 【面试】1/4 概率晋升，期望层数 ≈ 1.33
	SkipListP = 0.25
)

// =============================================================================
// 节点定义 - 实现 PriceLevelNode 接口
// =============================================================================

// SkipListNode 跳表节点
type SkipListNode struct {
	price int64           // 价格（排序键）
	level *RingPriceLevel // 价格档位
	next  []*SkipListNode // 各层的下一个节点
}

// GetPrice 实现 PriceLevelNode 接口
func (n *SkipListNode) GetPrice() int64 {
	return n.price
}

// GetLevel 实现 PriceLevelNode 接口
func (n *SkipListNode) GetLevel() *RingPriceLevel {
	return n.level
}

// newNode 创建新节点
func newNode(price int64, height int) *SkipListNode {
	return &SkipListNode{
		price: price,
		level: NewRingPriceLevel(price),
		next:  make([]*SkipListNode, height),
	}
}

// =============================================================================
// 跳表结构 - 实现 PriceIndex 接口
// =============================================================================

// SkipList 跳表
type SkipList struct {
	head   *SkipListNode         // 头节点（哨兵）
	height int                   // 当前最大层数
	length int                   // 节点数量
	less   func(a, b int64) bool // 比较函数
}

// 编译时检查：确保 SkipList 实现了 PriceIndex 接口
var _ PriceIndex = (*SkipList)(nil)

// NewSkipList 创建跳表
// ascending: true 升序（卖盘），false 降序（买盘）
func NewSkipList(ascending bool) *SkipList {
	var less func(a, b int64) bool
	if ascending {
		less = func(a, b int64) bool { return a < b }
	} else {
		less = func(a, b int64) bool { return a > b }
	}

	return &SkipList{
		head:   newNode(0, MaxLevel),
		height: 1,
		less:   less,
	}
}

// =============================================================================
// 内部方法
// =============================================================================

// randomHeight 随机生成节点层数
func randomHeight() int {
	h := 1
	for rand.Float64() < SkipListP && h < MaxLevel {
		h++
	}
	return h
}

// findWithPath 查找节点，同时记录每层的前驱节点
// 返回：目标节点（可能为 nil），每层前驱节点数组
func (sl *SkipList) findWithPath(price int64) (*SkipListNode, [MaxLevel]*SkipListNode) {
	var path [MaxLevel]*SkipListNode
	curr := sl.head

	// 从最高层向下查找
	for i := sl.height - 1; i >= 0; i-- {
		for curr.next[i] != nil && sl.less(curr.next[i].price, price) {
			curr = curr.next[i]
		}
		path[i] = curr
	}

	// 目标节点
	target := curr.next[0]
	if target != nil && target.price == price {
		return target, path
	}
	return nil, path
}

// =============================================================================
// PriceIndex 接口实现
// =============================================================================

// Find 查找指定价格的节点
func (sl *SkipList) Find(price int64) PriceLevelNode {
	node, _ := sl.findWithPath(price)
	if node == nil {
		return nil
	}
	return node
}

// Insert 插入价格档位（如果不存在则创建）
func (sl *SkipList) Insert(price int64) PriceLevelNode {
	// 1. 查找并记录路径
	existing, path := sl.findWithPath(price)
	if existing != nil {
		return existing // 已存在
	}

	// 2. 生成随机高度
	h := randomHeight()

	// 3. 如果新高度超过当前高度，更新路径
	if h > sl.height {
		for i := sl.height; i < h; i++ {
			path[i] = sl.head
		}
		sl.height = h
	}

	// 4. 创建新节点
	node := newNode(price, h)

	// 5. 插入到每层
	for i := 0; i < h; i++ {
		node.next[i] = path[i].next[i]
		path[i].next[i] = node
	}

	sl.length++
	return node
}

// Delete 删除价格档位
func (sl *SkipList) Delete(price int64) PriceLevelNode {
	// 1. 查找并记录路径
	target, path := sl.findWithPath(price)
	if target == nil {
		return nil
	}

	// 2. 从每层删除
	for i := 0; i < sl.height; i++ {
		if path[i].next[i] != target {
			break
		}
		path[i].next[i] = target.next[i]
	}

	// 3. 降低高度
	for sl.height > 1 && sl.head.next[sl.height-1] == nil {
		sl.height--
	}

	sl.length--
	return target
}

// First 获取第一个节点（最优价格）
func (sl *SkipList) First() PriceLevelNode {
	if sl.head.next[0] == nil {
		return nil
	}
	return sl.head.next[0]
}

// Len 返回价格档位数量
func (sl *SkipList) Len() int {
	return sl.length
}

// IsEmpty 是否为空
func (sl *SkipList) IsEmpty() bool {
	return sl.length == 0
}

// ForEach 遍历所有节点
func (sl *SkipList) ForEach(fn func(PriceLevelNode) bool) {
	curr := sl.head.next[0]
	for curr != nil {
		if !fn(curr) {
			break
		}
		curr = curr.next[0]
	}
}

// GetTopN 获取前 N 个价格档位
func (sl *SkipList) GetTopN(n int) []PriceLevelNode {
	result := make([]PriceLevelNode, 0, n)
	curr := sl.head.next[0]

	for curr != nil && len(result) < n {
		result = append(result, curr)
		curr = curr.next[0]
	}

	return result
}
