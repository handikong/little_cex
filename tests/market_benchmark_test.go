package tests

import (
	"testing"
	"time"

	"max.com/pkg/market"
	"max.com/pkg/risk"
)

// BenchmarkTicker 测试 Ticker 的生成性能
// 关注点：
// 1. ns/op (越低越好)
// 2. allocs/op (必须为 0)
func BenchmarkTicker(b *testing.B) {
	// 创建一个极高频的 Ticker (1微秒一次)
	ticker := market.NewTicker("BTC", 50000, 1*time.Microsecond)
	ch := ticker.Start()
	defer ticker.Stop()

	b.ResetTimer() // 重置计时器，排除初始化时间

	for i := 0; i < b.N; i++ {
		<-ch // 消费一个 tick
	}
}

// BenchmarkBroadcast 测试 Broadcaster 的分发性能
// 场景：1个生产者 -> 10个消费者
func BenchmarkBroadcast(b *testing.B) {
	broadcaster := market.NewBroadcaster()
	defer broadcaster.Close()

	// 模拟 10 个订阅者
	for i := 0; i < 10; i++ {
		ch := broadcaster.Subscribe()
		// 启动消费者 Goroutine，防止 Channel 满
		go func() {
			for range ch {
				// 模拟消费
			}
		}()
	}

	// 准备一条测试数据
	snap := risk.PriceSnapshot{Price: 50000, Ts: time.Now()}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		broadcaster.Broadcast(snap)
	}
}

// BenchmarkPipeline 测试完整链路性能
// Ticker -> Broadcaster -> 10 Consumers
func BenchmarkPipeline(b *testing.B) {
	ticker := market.NewTicker("BTC", 50000, 1*time.Microsecond)
	broadcaster := market.NewBroadcaster()

	// 10 个消费者
	for i := 0; i < 10; i++ {
		ch := broadcaster.Subscribe()
		go func() {
			for range ch {
			}
		}()
	}

	// 连接 Ticker 和 Broadcaster
	priceCh := ticker.Start()
	go func() {
		for p := range priceCh {
			broadcaster.Broadcast(p)
		}
	}()

	b.ResetTimer()

	// 这里的测试逻辑稍微不同，我们让它跑 1 秒，看能处理多少
	// b.N 在这里不太好控制，因为是异步的
	// 所以我们只跑固定时间，观察 CPU 占用
	time.Sleep(1 * time.Second)

	ticker.Stop()
	broadcaster.Close()
}
