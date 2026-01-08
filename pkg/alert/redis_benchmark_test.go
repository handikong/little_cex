package alert

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkSubscribe 测试订阅性能
func BenchmarkSubscribe(b *testing.B) {
	manager := setupRedis(nil) // 复用 setupRedis，但不需要 *testing.T
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule := AlertRule{
			AlertID:   fmt.Sprintf("bench_%d", i),
			Symbol:    "BTC_USDT",
			Direction: "high",
			Price:     50000,
			Type:      AlertOnce,
		}
		manager.Subscribe(ctx, rule)
	}
}

// BenchmarkGetTriggeredAlerts 测试惊群效应下的获取性能
// 场景: 10,000 个用户都在 50000 价格设置了预警
func BenchmarkGetTriggeredAlerts(b *testing.B) {
	manager := setupRedis(nil)
	ctx := context.Background()

	// 预先准备好 10000 条规则对象 (内存中)
	var rules []AlertRule
	for i := 0; i < 10000; i++ {
		rules = append(rules, AlertRule{
			AlertID:   fmt.Sprintf("herd_%d", i),
			Symbol:    "BTC_USDT",
			Direction: "high",
			Price:     50000,
			Type:      AlertOnce,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 1. 暂停计时：准备环境
		b.StopTimer()
		manager.client.FlushDB(ctx) // 清空

		// 批量插入 (使用 Pipeline 加速准备过程，减少等待时间)
		// 注意：这里我们手动模拟 Subscribe 的逻辑，或者直接循环调用 Subscribe
		// 为了简单且复用逻辑，我们直接循环调用 Subscribe
		for _, r := range rules {
			_ = manager.Subscribe(ctx, r)
		}

		// 2. 开始计时：只测这一行核心逻辑
		b.StartTimer()
		_, _ = manager.GetTriggeredAlerts(ctx, "BTC_USDT", 51000, 49000)
	}
}
