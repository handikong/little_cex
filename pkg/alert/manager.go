package alert

import (
	"errors"
	"sync"
	"time"
)

// MockSubscriptionManager 内存版预警管理器
// 用于 Day 6 快速验证逻辑，Day 7 会被 Redis 版本替换
type MockSubscriptionManager struct {
	mu    sync.RWMutex
	rules map[string]AlertRule // key: AlertID
}

func NewMockSubscriptionManager() *MockSubscriptionManager {
	return &MockSubscriptionManager{
		rules: make(map[string]AlertRule),
	}
}

// Subscribe 订阅预警
func (m *MockSubscriptionManager) Subscribe(rule AlertRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rule.AlertID == "" {
		return errors.New("alert_id is required")
	}
	// 模拟 Redis HSET
	m.rules[rule.AlertID] = rule
	return nil
}

// Unsubscribe 取消订阅
func (m *MockSubscriptionManager) Unsubscribe(alertID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 模拟 Redis DEL
	delete(m.rules, alertID)
	return nil
}

// GetTriggeredAlerts 获取触发的预警 (核心逻辑)
// 这个函数模拟了 Redis ZSet 的范围查询 + 业务过滤
func (m *MockSubscriptionManager) GetTriggeredAlerts(symbol string, currentPrice float64) ([]AlertRule, error) {
	m.mu.Lock() // 注意：这里涉及更新 LastTriggeredAt，所以用写锁
	defer m.mu.Unlock()

	var triggered []AlertRule
	now := time.Now()
	currentHour := now.Hour()

	// 遍历所有规则 (模拟 Redis ZRANGE)
	// 在真实 Redis 实现中，我们不会遍历全量，而是通过 ZSet 索引直接拿到候选集
	for id, rule := range m.rules {
		// 1. 基础过滤：Symbol 必须匹配
		if rule.Symbol != symbol {
			continue
		}

		// 2. 价格条件判断
		// High: 只有当 当前价 >= 目标价 时触发
		// Low:  只有当 当前价 <= 目标价 时触发
		isPriceMatch := false
		if rule.Direction == "high" {
			if currentPrice >= rule.Price {
				isPriceMatch = true
			}
		} else if rule.Direction == "low" {
			if currentPrice <= rule.Price {
				isPriceMatch = true
			}
		}

		if !isPriceMatch {
			continue
		}

		// 3. 高级规则过滤 (时间窗口)
		// 如果设置了 StartHour/EndHour，必须在区间内
		// 例如: 9 - 18
		if rule.StartHour != rule.EndHour {
			if currentHour < rule.StartHour || currentHour >= rule.EndHour {
				continue // 不在服务时间内
			}
		}

		// 4. 频率控制 (Type)
		shouldTrigger := false
		switch rule.Type {
		case AlertOnce:
			shouldTrigger = true
			// Once 类型触发后直接删除
			delete(m.rules, id)

		case AlertDaily:
			// 检查上次触发是不是今天
			last := time.Unix(rule.LastTriggeredAt, 0)
			// 如果上次触发不是今天，或者从未触发过(0)，则触发
			if rule.LastTriggeredAt == 0 || !isSameDay(last, now) {
				shouldTrigger = true
				// 更新触发时间
				rule.LastTriggeredAt = now.Unix()
				m.rules[id] = rule // 写回 map
			}

		case AlertAlways:
			shouldTrigger = true
			rule.LastTriggeredAt = now.Unix()
			m.rules[id] = rule
		}

		if shouldTrigger {
			triggered = append(triggered, rule)
		}
	}

	return triggered, nil
}

// isSameDay 判断两个时间是否是同一天
func isSameDay(t1, t2 time.Time) bool {
	y1, m1, d1 := t1.Date()
	y2, m2, d2 := t2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}
