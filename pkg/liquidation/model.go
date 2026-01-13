package liquidation

import "time"

// =============================================================================
// 风险等级定义
// =============================================================================

// RiskLevel 风险等级枚举
//
// 交易所根据用户的风险率，将用户分入不同的等级：
// - 安全区：不需要特别关注
// - 预警区：需要定期检查
// - 危险区：需要更频繁检查
// - 临界区：随时可能爆仓，需要价格触发
// - 强平区：立即执行强平
type RiskLevel int

const (
	// RiskLevelSafe 安全区: 风险率 < 70%
	// 用户处于安全状态，不需要进入任何监控索引
	RiskLevelSafe RiskLevel = iota

	// RiskLevelWarning 预警区: 70% <= 风险率 < 80%
	// 用户风险偏高，需要进入 Level 1 索引，每 5 秒检查一次
	// 触发动作：推送预警通知
	RiskLevelWarning

	// RiskLevelDanger 危险区: 80% <= 风险率 < 90%
	// 用户风险较高，需要进入 Level 2 索引，每 2 秒检查一次
	// 触发动作：限制开仓，推送警告通知
	RiskLevelDanger

	// RiskLevelCritical 临界区: 90% <= 风险率 < 100%
	// 用户随时可能爆仓，需要进入 Level 3 索引（价格触发器）
	// 触发动作：注册价格触发器，行情变化时立即检查
	RiskLevelCritical

	// RiskLevelLiquidate 强平区: 风险率 >= 100%
	// 用户必须立即强平
	// 触发动作：进入强平执行队列
	RiskLevelLiquidate
)

// String 返回风险等级的字符串表示（用于日志打印）
func (l RiskLevel) String() string {
	switch l {
	case RiskLevelSafe:
		return "SAFE"
	case RiskLevelWarning:
		return "WARNING"
	case RiskLevelDanger:
		return "DANGER"
	case RiskLevelCritical:
		return "CRITICAL"
	case RiskLevelLiquidate:
		return "LIQUIDATE"
	default:
		return "UNKNOWN"
	}
}

// =============================================================================
// 风险阈值常量
// =============================================================================

const (
	// ThresholdWarning 预警阈值: 70%
	ThresholdWarning = 0.70

	// ThresholdDanger 危险阈值: 80%
	ThresholdDanger = 0.80

	// ThresholdCritical 临界阈值: 90%
	ThresholdCritical = 0.90

	// ThresholdLiquidate 强平阈值: 100%
	ThresholdLiquidate = 1.00
)

// =============================================================================
// 用户风险数据
// =============================================================================

// UserRiskData 用户风险数据
//
// 这是存储在各级索引中的核心数据结构。
// 包含了判断用户是否需要升降级、是否需要强平的所有信息。
//
// 设计原则：
// 1. 使用值类型而非指针，减少 GC 压力
// 2. 按内存对齐排列字段（float64/int64 在前，bool 在后）
// 3. 只包含必要字段，避免数据冗余
type UserRiskData struct {
	// ========== 身份信息 ==========

	// UserID 用户唯一标识
	UserID int64

	// ========== 风险指标 ==========

	// RiskRatio 当前风险率
	// 计算公式: 维持保证金 / 动态权益
	// 越高越危险，>= 1.0 触发强平
	RiskRatio float64

	// Equity 动态权益
	// 计算公式: 余额 + 未实现盈亏
	Equity float64

	// MaintMargin 维持保证金总额
	MaintMargin float64

	// ========== 价格触发信息（Level 3 使用）==========

	// LiquidationPrices 各交易对的强平价格
	// Key = Symbol (如 "BTC_USDT")
	// Value = 该交易对触发强平的价格
	//
	// 为什么是 Map？
	// 因为全仓模式下，用户可能持有多个交易对的仓位
	// 任意一个交易对触发都可能导致整个账户强平
	LiquidationPrices map[string]float64

	// ========== 元数据 ==========

	// Level 当前所处的风险等级
	Level RiskLevel

	// UpdatedAt 最后更新时间（Unix 纳秒时间戳）
	// 使用纳秒而非 time.Time，避免序列化开销
	UpdatedAt int64

	// ========== 持仓信息摘要 ==========

	// Symbols 用户持有的交易对列表
	// 用于：行情变化时，快速判断该用户是否受影响
	Symbols []string
}

// NewUserRiskData 创建新的用户风险数据
func NewUserRiskData(userID int64) UserRiskData {
	return UserRiskData{
		UserID:            userID,
		LiquidationPrices: make(map[string]float64),
		Symbols:           make([]string, 0),
		UpdatedAt:         time.Now().UnixNano(),
	}
}

// =============================================================================
// 强平执行相关
// =============================================================================

// LiquidationTask 强平任务
//
// 当用户进入强平区时，会创建一个强平任务。
// 任务会被放入队列，由 Worker Pool 处理。
type LiquidationTask struct {
	// UserID 要强平的用户
	UserID int64

	Symbol string

	// RiskRatio 触发时的风险率
	RiskRatio float64

	// TriggerPrice 触发时的价格（用于记录）
	TriggerPrice float64

	// TriggerSymbol 触发的交易对
	TriggerSymbol string

	// CreatedAt 任务创建时间
	CreatedAt time.Time

	// Priority 优先级（风险率越高，优先级越高）
	// 用于优先级队列排序
	Priority float64
}

// LiquidationResult 强平执行结果
type LiquidationResult struct {
	// UserID 被强平的用户
	UserID int64

	// Success 是否成功
	Success bool

	// Error 错误信息（如果失败）
	Error error

	// ExecutedAt 执行时间
	ExecutedAt time.Time

	// Details 详细信息
	Details LiquidationDetails
}

// LiquidationDetails 强平详情
type LiquidationDetails struct {
	// ClosedPositions 被关闭的仓位数量
	ClosedPositions int

	// TotalPnL 总盈亏
	TotalPnL float64

	// RemainingBalance 剩余余额
	RemainingBalance float64
}

// =============================================================================
// 辅助函数
// =============================================================================

// CalculateRiskLevel 根据风险率计算风险等级
//
// 参数:
//
//	riskRatio: 风险率 (0.0 ~ 无穷大)
//
// 返回:
//
//	对应的风险等级
func CalculateRiskLevel(riskRatio float64) RiskLevel {
	switch {
	case riskRatio >= ThresholdLiquidate:
		return RiskLevelLiquidate
	case riskRatio >= ThresholdCritical:
		return RiskLevelCritical
	case riskRatio >= ThresholdDanger:
		return RiskLevelDanger
	case riskRatio >= ThresholdWarning:
		return RiskLevelWarning
	default:
		return RiskLevelSafe
	}
}
