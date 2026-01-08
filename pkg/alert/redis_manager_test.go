package alert

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// setupRedis 初始化 Redis 连接并清空测试数据
func setupRedis(t *testing.T) *RedisSubscriptionManager {
	// 假设本地 Redis 运行在 localhost:6379
	addr := "localhost:6379"
	manager := NewRedisSubscriptionManager(addr)

	// Ping 测试连接
	if err := manager.client.Ping(context.Background()).Err(); err != nil {
		if t != nil {
			t.Skipf("skipping test; redis not available: %v", err)
		} else {
			panic(fmt.Sprintf("redis not available: %v", err))
		}
	}

	// 清空测试用的 Key
	manager.client.FlushDB(context.Background())

	return manager
}

func TestRedisSubscriptionManager_Subscribe_Unsubscribe(t *testing.T) {
	manager := setupRedis(t)
	ctx := context.Background()

	rule := AlertRule{
		AlertID:   "1001",
		Symbol:    "BTC_USDT",
		Direction: "high",
		Price:     50000,
		Type:      AlertOnce,
	}

	// 1. 测试 Subscribe
	err := manager.Subscribe(ctx, rule)
	require.NoError(t, err)

	// 验证 Detail Key
	detailKey := fmt.Sprintf("alert:detail:%s", rule.AlertID)
	exists, err := manager.client.Exists(ctx, detailKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), exists, "Detail key should exist after subscribe")

	// 验证 Index Key
	indexKey := fmt.Sprintf("alerts:%s:%s", rule.Symbol, rule.Direction)
	score, err := manager.client.ZScore(ctx, indexKey, "1001:once").Result() // Member format: ID:Type
	require.NoError(t, err)
	require.Equal(t, rule.Price, score)

	// 2. 测试 Unsubscribe
	err = manager.Unsubscribe(ctx, rule.AlertID)
	require.NoError(t, err)

	// 验证 Detail Key 被删除
	exists, err = manager.client.Exists(ctx, detailKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(0), exists, "Detail key should be deleted after unsubscribe")

	// 验证 Index Key 被删除
	count, err := manager.client.ZCard(ctx, indexKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(0), count)
}

func TestRedisSubscriptionManager_GetTriggeredAlerts_Direction(t *testing.T) {
	manager := setupRedis(t)
	ctx := context.Background()

	// 准备数据
	// High Alert: > 50000
	highRule := AlertRule{AlertID: "high_1", Symbol: "BTC_USDT", Direction: "high", Price: 50000, Type: AlertAlways}
	// Low Alert: < 40000
	lowRule := AlertRule{AlertID: "low_1", Symbol: "BTC_USDT", Direction: "low", Price: 40000, Type: AlertAlways}

	err := manager.Subscribe(ctx, highRule)
	require.NoError(t, err)
	err = manager.Subscribe(ctx, lowRule)
	require.NoError(t, err)

	// Debug: Check if data exists
	count, _ := manager.client.ZCard(ctx, "alerts:BTC_USDT:high").Result()
	t.Logf("ZCard high: %d", count)

	// 1. 价格上涨 (49000 -> 51000) -> 应该触发 High
	triggered, err := manager.GetTriggeredAlerts(ctx, "BTC_USDT", 51000, 49000)
	require.NoError(t, err)
	t.Logf("Triggered: %+v", triggered)
	require.Len(t, triggered, 1)
	require.Equal(t, "high_1", triggered[0].AlertID)

	// 2. 价格下跌 (41000 -> 39000) -> 应该触发 Low
	triggered, err = manager.GetTriggeredAlerts(ctx, "BTC_USDT", 39000, 41000)
	require.NoError(t, err)
	require.Len(t, triggered, 1)
	require.Equal(t, "low_1", triggered[0].AlertID)

	// 3. 价格没变 -> 不触发
	triggered, err = manager.GetTriggeredAlerts(ctx, "BTC_USDT", 51000, 51000)
	require.NoError(t, err)
	require.Len(t, triggered, 0)
}

func TestRedisSubscriptionManager_AlertOnce(t *testing.T) {
	manager := setupRedis(t)
	ctx := context.Background()

	rule := AlertRule{
		AlertID:   "once_1",
		Symbol:    "ETH_USDT",
		Direction: "high",
		Price:     3000,
		Type:      AlertOnce,
	}
	manager.Subscribe(ctx, rule)

	// Debug: Check if detail key exists before trigger
	detailKey := fmt.Sprintf("alert:detail:%s", rule.AlertID)
	exists, _ := manager.client.Exists(ctx, detailKey).Result()
	t.Logf("Before trigger, detail exists: %d", exists)

	// 第一次触发
	triggered, err := manager.GetTriggeredAlerts(ctx, "ETH_USDT", 31000, 29000)
	require.NoError(t, err)
	require.Len(t, triggered, 1)

	// 第二次触发 -> 应该不触发 (因为已经从 Index 删除了)
	triggered, err = manager.GetTriggeredAlerts(ctx, "ETH_USDT", 31000, 29000)
	require.NoError(t, err)
	require.Len(t, triggered, 0)

	// 验证 Detail Key 还在 (符合需求)
	exists, err = manager.client.Exists(ctx, detailKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), exists, "Detail key should persist for AlertOnce")
}

func TestRedisSubscriptionManager_AlertAlways_Cooldown(t *testing.T) {
	manager := setupRedis(t)
	ctx := context.Background()

	rule := AlertRule{
		AlertID:   "always_1",
		Symbol:    "SOL_USDT",
		Direction: "high",
		Price:     100,
		Type:      AlertAlways,
	}
	manager.Subscribe(ctx, rule)

	// Debug: Check cooldown key before trigger
	cooldownKey := fmt.Sprintf("alert:cooldown:%s", rule.AlertID)
	exists, _ := manager.client.Exists(ctx, cooldownKey).Result()
	t.Logf("Before trigger, cooldown exists: %d", exists)

	// 第一次触发
	triggered, err := manager.GetTriggeredAlerts(ctx, "SOL_USDT", 110, 90)
	require.NoError(t, err)
	t.Logf("First trigger result: %+v", triggered)
	require.Len(t, triggered, 1, "Should trigger first time")

	// Debug: Check cooldown key after trigger
	exists, _ = manager.client.Exists(ctx, cooldownKey).Result()
	t.Logf("After trigger, cooldown exists: %d", exists)

	// 立即第二次触发 (应该被冷却拦截)
	triggered, err = manager.GetTriggeredAlerts(ctx, "SOL_USDT", 110, 90)
	require.NoError(t, err)
	require.Len(t, triggered, 0, "Should be cooldown")
}
