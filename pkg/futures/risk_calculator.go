// 文件: pkg/futures/risk_calculator.go
// 风险计算服务 - 集成 pkg/risk/perp 模块

package futures

import (
	"max.com/pkg/risk/perp"
)

// =============================================================================
// PositionRisk - 持仓风险信息
// =============================================================================

// PositionRisk 持仓的风险计算结果
type PositionRisk struct {
	// 价格
	MarkPrice        int64 // 标记价格
	LiquidationPrice int64 // 强平价格

	// 盈亏
	UnrealizedPnL int64 // 未实现盈亏
	RealizedPnL   int64 // 已实现盈亏 (从 Position 复制)

	// 保证金
	Notional       int64 // 名义价值
	MaintMarginReq int64 // 维持保证金需求
	InitMarginReq  int64 // 初始保证金需求

	// 风险等级
	RiskLevel RiskLevel
}

// RiskLevel 风险等级
type RiskLevel int8

const (
	RiskLevelSafe      RiskLevel = iota // 安全 (< 70%)
	RiskLevelWarning                    // 预警 (70% - 90%)
	RiskLevelDanger                     // 危险 (90% - 100%)
	RiskLevelLiquidate                  // 强平 (>= 100%)
)

func (r RiskLevel) String() string {
	switch r {
	case RiskLevelSafe:
		return "SAFE"
	case RiskLevelWarning:
		return "WARNING"
	case RiskLevelDanger:
		return "DANGER"
	case RiskLevelLiquidate:
		return "LIQUIDATE"
	}
	return "UNKNOWN"
}

// =============================================================================
// RiskCalculator - 风险计算器
// =============================================================================

// RiskCalculator 风险计算器
//
// 封装 pkg/risk/perp 的计算逻辑，提供简化的接口给 FuturesProcessor 使用
type RiskCalculator struct {
	maintenanceRate float64 // 维持保证金率 (如 0.005 = 0.5%)
	initialRate     float64 // 初始保证金率 (如 0.1 = 10%)
}

// NewRiskCalculator 创建风险计算器
func NewRiskCalculator() *RiskCalculator {
	return &RiskCalculator{
		maintenanceRate: 0.005, // 0.5% 维保率
		initialRate:     0.10,  // 10% 初始保证金 (10倍杠杆)
	}
}

// CalculatePositionRisk 计算单个持仓的风险指标
//
// 参数:
//   - pos: 持仓信息
//   - markPrice: 当前标记价格
//   - balance: 账户可用余额 (用于计算风险率)
//
// 返回: 风险计算结果
func (c *RiskCalculator) CalculatePositionRisk(pos *Position, markPrice int64, balance int64) *PositionRisk {
	if pos == nil || pos.Size == 0 {
		return nil
	}

	// 转换为 risk/perp 的类型 (float64)
	perpPos := perp.Position{
		Qty:             float64(pos.Size) / float64(Precision),
		EntryPrice:      float64(pos.EntryPrice) / float64(Precision),
		MarkPrice:       float64(markPrice) / float64(Precision),
		MaintenanceRate: c.maintenanceRate,
		InitialRate:     c.initialRate,
	}

	// 调用 risk/perp 计算
	balanceFloat := float64(balance) / float64(Precision)
	metrics := perp.CalculateRisk(perpPos, balanceFloat)

	// 计算强平价格
	liqPrice := perp.CalculateLiquidationPrice(
		perpPos.Qty,
		perpPos.EntryPrice,
		balanceFloat,
		c.maintenanceRate,
	)

	// 转换回 int64 精度
	result := &PositionRisk{
		MarkPrice:        markPrice,
		LiquidationPrice: int64(liqPrice * float64(Precision)),
		UnrealizedPnL:    int64(metrics.UnrealizedPnL * float64(Precision)),
		RealizedPnL:      pos.RealizedPnL,
		Notional:         int64(metrics.Notional * float64(Precision)),
		MaintMarginReq:   int64(metrics.MaintMarginReq * float64(Precision)),
		InitMarginReq:    int64(metrics.InitMarginReq * float64(Precision)),
	}

	// 设置风险等级
	result.RiskLevel = c.calculateRiskLevel(metrics, balanceFloat)

	return result
}

// calculateRiskLevel 根据计算结果判断风险等级
func (c *RiskCalculator) calculateRiskLevel(metrics perp.RiskMetrics, balance float64) RiskLevel {
	if metrics.IsLiquidatable {
		return RiskLevelLiquidate
	}

	// 计算风险率 = 维保需求 / 权益
	equity := balance + metrics.UnrealizedPnL
	if equity <= 0 {
		return RiskLevelLiquidate
	}

	riskRatio := metrics.MaintMarginReq / equity

	switch {
	case riskRatio >= perp.LiquidateThreshold:
		return RiskLevelLiquidate
	case riskRatio >= perp.DangerThreshold:
		return RiskLevelDanger
	case riskRatio >= perp.WarningThreshold:
		return RiskLevelWarning
	default:
		return RiskLevelSafe
	}
}

// SetMaintenanceRate 设置维持保证金率
func (c *RiskCalculator) SetMaintenanceRate(rate float64) {
	c.maintenanceRate = rate
}

// SetInitialRate 设置初始保证金率
func (c *RiskCalculator) SetInitialRate(rate float64) {
	c.initialRate = rate
}
