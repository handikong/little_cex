package alert

// AlertType 定义预警的生命周期类型
type AlertType string

const (
	AlertOnce   AlertType = "once"   // 触发一次后自动删除 (最常见)
	AlertDaily  AlertType = "daily"  // 每天触发一次 (如：每日收盘价提醒)
	AlertAlways AlertType = "always" // 只要满足条件就一直触发 (慎用，容易骚扰用户)
)

// AlertRule 定义具体的触发规则
// 对应 Redis Hash 中的详情数据
type AlertRule struct {
	AlertID   string    `json:"alert_id"`
	UserID    int64     `json:"user_id"`
	Symbol    string    `json:"symbol"`    // 交易对，如 "BTC_USDT"
	Direction string    `json:"direction"` // "high" (>= price) 或 "low" (<= price)
	Price     float64   `json:"price"`     // 触发价格
	Type      AlertType `json:"type"`      // 预警类型

	// 高级规则 (Day 6 进阶)
	StartHour int `json:"start_hour"` // 生效开始时间 (0-23)
	EndHour   int `json:"end_hour"`   // 生效结束时间 (0-23)

	// 状态字段
	LastTriggeredAt int64 `json:"last_triggered_at"` // 上次触发时间戳 (秒)
	CreatedAt       int64 `json:"created_at"`
}

// SubscriptionManager 定义订阅管理器的行为
// Day 6 先用内存 Mock 实现，Day 7 换成 Redis 实现
type SubscriptionManager interface {
	// Subscribe 创建一个新的预警订阅
	Subscribe(rule AlertRule) error

	// Unsubscribe 取消订阅
	Unsubscribe(alertID string) error

	// GetTriggeredAlerts 获取当前价格触发的所有预警
	// symbol: 交易对
	// currentPrice: 当前最新价
	// 返回: 触发的 AlertRule 列表
	GetTriggeredAlerts(symbol string, currentPrice float64) ([]AlertRule, error)
}
