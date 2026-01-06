package perp

import "math"

// Metrics 包含计算出的单条仓位风控指标
type Metrics struct {
	Notional       float64 // 名义价值
	UnrealizedPnL  float64 // 未实现盈亏
	MaintMarginReq float64 // 维持保证金需求
	InitMarginReq  float64 // 初始保证金需求
}

// CalculateMetrics 计算单条永续合约仓位的核心指标
// 适用于线性合约 (USDT-Margined)
//
// qty: 仓位数量 (+做多, -做空)
// entryPrice: 开仓均价
// markPrice: 当前标记价格
// mmr: 维持保证金率 (Maintenance Margin Rate)
// imr: 初始保证金率 (Initial Margin Rate)

func CalculateMetrics(qty, entryPrice, markPrice, mmr, imr float64) Metrics {
	// 1. 计算名义价值 (Notional)
	// 无论多空，名义价值都是正数： |Qty| * MarkPrice
	absQty := math.Abs(qty)
	notional := absQty * markPrice

	// 2. 计算未实现盈亏 (uPnL)
	// 线性合约公式: uPnL = Qty * (MarkPrice - EntryPrice)
	// 多仓(Qty>0): Mark > Entry => 赚
	// 空仓(Qty<0): Mark < Entry => (负数 * 负差值) = 正数 => 赚
	uPnL := qty * (markPrice - entryPrice)

	// 3. 计算保证金需求
	// 维持保证金 = 名义价值 * MMR
	maintMarginReq := notional * mmr
	// 初始保证金 = 名义价值 * IMR
	initMarginReq := notional * imr

	return Metrics{
		Notional:       notional,
		UnrealizedPnL:  uPnL,
		MaintMarginReq: maintMarginReq,
		InitMarginReq:  initMarginReq,
	}
}
