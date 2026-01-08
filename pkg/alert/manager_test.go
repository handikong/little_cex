package alert

import (
	"testing"
)

func TestMockSubscriptionManager_GetTriggeredAlerts(t *testing.T) {
	manager := NewMockSubscriptionManager()

	// 1. 准备测试数据
	rules := []AlertRule{
		{
			AlertID:   "1",
			Symbol:    "BTC_USDT",
			Direction: "high",
			Price:     50000,
			Type:      AlertOnce,
		},
		{
			AlertID:   "2",
			Symbol:    "BTC_USDT",
			Direction: "low",
			Price:     40000,
			Type:      AlertAlways,
		},
		{
			AlertID:   "3",
			Symbol:    "ETH_USDT", // 不同的 Symbol
			Direction: "high",
			Price:     3000,
			Type:      AlertOnce,
		},
	}

	for _, r := range rules {
		manager.Subscribe(r)
	}

	// 2. 测试场景 A: BTC 价格涨到 51000
	// 预期: 触发 ID=1 (High 50000)
	// 不触发 ID=2 (Low 40000)
	// 不触发 ID=3 (Symbol 不匹配)
	triggered, err := manager.GetTriggeredAlerts("BTC_USDT", 51000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(triggered) != 1 {
		t.Errorf("expected 1 triggered alert, got %d", len(triggered))
	}
	if triggered[0].AlertID != "1" {
		t.Errorf("expected alert 1, got %s", triggered[0].AlertID)
	}

	// 3. 测试场景 B: 再次检查 (AlertOnce 应该被删除)
	triggered, _ = manager.GetTriggeredAlerts("BTC_USDT", 51000)
	if len(triggered) != 0 {
		t.Errorf("expected 0 alerts (AlertOnce should be deleted), got %d", len(triggered))
	}
}

func TestMockSubscriptionManager_DailyAlert(t *testing.T) {
	manager := NewMockSubscriptionManager()

	rule := AlertRule{
		AlertID:   "daily_1",
		Symbol:    "BTC_USDT",
		Direction: "high",
		Price:     50000,
		Type:      AlertDaily,
	}
	manager.Subscribe(rule)

	// 第一次触发
	triggered, _ := manager.GetTriggeredAlerts("BTC_USDT", 51000)
	if len(triggered) != 1 {
		t.Fatal("first trigger failed")
	}

	// 第二次触发 (同一天) -> 应该不触发
	triggered, _ = manager.GetTriggeredAlerts("BTC_USDT", 51000)
	if len(triggered) != 0 {
		t.Fatal("should not trigger twice in same day")
	}
}
