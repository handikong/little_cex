package market

import (
	"sync"

	"max.com/pkg/risk"
)

// Broadcaster 行情广播器
// 设计模式：Fan-out（扇出）
// 核心职责：把一条消息分发给 N 个订阅者，且保证隔离性
//
// ========== Fan-out 模式图解 ==========
//
//	      Ticker (生产者)
//	            |
//	            v
//	     [Broadcaster]
//	       /    |    \
//	      v     v     v
//	 订阅者1  订阅者2  订阅者3
//	(风控)   (撮合)   (WebSocket)
//
// 关键特性：
// 1. 订阅者2 处理慢，不能影响订阅者1和3
// 2. 订阅/取消订阅 是并发安全的
// 3. Broadcast() 是 Hot Path，必须极快
type Broadcaster struct {
	// ========== 并发控制 ==========
	// 📝 笔记：为什么用 RWMutex 而不是 Mutex？
	//
	// 场景分析：
	// - Subscribe()：写操作，修改 subscribers 列表（少）
	// - Broadcast()：读操作，遍历 subscribers（多，每秒数万次）
	//
	// RWMutex 特性：
	// - 多个 Goroutine 可以同时持有读锁
	// - 只有写锁是排他的
	//
	// 性能对比：
	// - Mutex：所有操作都互斥，Broadcast 串行 → 慢
	// - RWMutex：多个 Broadcast 并发执行 → 快（提升 N 倍，N=CPU核数）
	//
	// 面试考点：
	// Q: "什么时候用 RWMutex？"
	// A: "读多写少的场景。比如配置中心、缓存、订阅者列表。"
	mu sync.RWMutex

	// ========== 订阅者列表 ==========
	// 为什么用 slice 而不是 map？
	//
	// 对比：
	// - slice：遍历快，删除慢（需要移动元素）
	// - map：删除快，遍历略慢（哈希表迭代）
	//
	// 选择 slice 的原因：
	// 1. Broadcast 是热路径，遍历频率极高
	// 2. 删除操作很少（订阅者长期存在）
	// 3. slice 内存布局连续，CPU Cache 友好
	//
	// 📝 笔记：为什么是 chan PriceSnapshot 而不是 *Subscriber？
	// 答：简单！直接给每个订阅者一个 Channel 即可，不需要复杂的对象
	subscribers []chan risk.PriceSnapshot
}

// NewBroadcaster 创建一个新的广播器
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subscribers: make([]chan risk.PriceSnapshot, 0),
	}
}

// Subscribe 订阅行情
// 返回一个只读 Channel，订阅者从中接收数据
//
// 📝 笔记：Buffer 大小为什么是 1024？
// 答：这个 Buffer 是为了保护订阅者自己的。
// 如果 Ticker 很快（10k TPS），而订阅者处理慢（比如写 DB），
// 1024 的 Buffer 可以缓冲约 100ms 的数据，给订阅者喘息时间。
func (b *Broadcaster) Subscribe() <-chan risk.PriceSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan risk.PriceSnapshot, 1024)
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// Broadcast 广播行情到所有订阅者（Hot Path）
//
// 📝 笔记：隔离性是如何保证的？
// 答：通过 select default。
// 如果某个订阅者的 Channel 满了（处理慢），我们直接跳过它（丢包），
// 绝不等待，从而保证不会影响其他健康的订阅者。
func (b *Broadcaster) Broadcast(p risk.PriceSnapshot) {
	// 使用读锁，允许多个 Broadcast 并发执行（虽然 Ticker 只有一个，但未来可能有多个源）
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- p:
			// 发送成功
		default:
			// Channel 满了，丢弃这条数据 (Drop Strategy)
			// 也可以选择记录日志：log.Warn("subscriber slow, dropping tick")
		}
	}
}

// Close 关闭广播器
// 释放所有资源，关闭所有订阅者的 Channel
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subscribers {
		close(ch)
	}
	// 清空列表，避免重复关闭
	b.subscribers = nil
}
