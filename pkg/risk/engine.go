package risk

import (
	"errors"
	"math"

	"gopherex.com/internal/risk/perp"
)

// Engine 是风险引擎对象。
// 你可以把它理解成“一个计算器”：
// 输入 RiskInput → 输出 RiskOutput。

type Engine struct{}

// NewEngine 创建一个默认 Engine。
// Day1 Engine 不包含任何依赖，后续会逐步添加。
func NewEngine() *Engine { return &Engine{} }

// ComputeRisk 是 Day1 的“占位版”风险计算。
// 注意：Day1 的目标不是“计算非常准确”，而是“把数据流跑通”。
//
// Day1 公式（占位）：
// 1) Notional = Σ |qty| * price
// 2) InitMarginReq = Notional * initMarginRate
// 3) RiskRatio = equity / InitMarginReq
//
// 为什么这么做？
// - Σ |qty|*price 是最朴素的风险规模指标
// - 固定比例保证金是最朴素的保证金模型
// 后面我们会逐步替换成更真实的模型。
func (e *Engine) ComputeRisk(in RiskInput) (RiskOutput, error) {
	if err := validateInput(in); err != nil {
		return RiskOutput{}, err
	}

	var (
		totalNotional  float64
		totalUPnL      float64
		totalMaintMrgn float64 // 总维持保证金
		totalInitMrgn  float64 // 总初始保证金
		warnings       []string
	)

	for _, p := range in.Positions {
		priceSnap, ok := in.Prices[p.Symbol]
		if !ok {
			return RiskOutput{}, errors.New("missing price for: " + p.Symbol)
		}

		// 优先使用 MarkPrice，如果没有则降级使用 Price，并给出警告
		calcPrice := priceSnap.MarkPrice
		if calcPrice == 0 {
			calcPrice = priceSnap.Price
			warnings = append(warnings, "using last_price as mark_price for "+p.Symbol)
		}
		if calcPrice <= 0 {
			return RiskOutput{}, errors.New("invalid price for: " + p.Symbol)
		}

		// 根据不同类型分发计算逻辑
		switch p.Instrument {
		case InstrumentPerp:
			// 使用 Day 4 新写的 perp 模块
			// 假设 imr 为账户默认值 (真实情况需查表)
			imr := in.Account.InitMarginRate
			// 如果仓位没有设置 MMR，给一个默认极小值防止 panic，或者报错
			mmr := p.MaintenanceMarginRate
			if mmr == 0 {
				mmr = 0.005 // 默认 0.5%
			}
			m := perp.CalculateMetrics(p.Qty, p.EntryPrice, calcPrice, mmr, imr)

			totalNotional += m.Notional
			totalUPnL += m.UnrealizedPnL
			totalMaintMrgn += m.MaintMarginReq
			totalInitMrgn += m.InitMarginReq

		case InstrumentSpot:
			// 现货逻辑：名义价值累计，无 uPnL (现货通常按余额算，这里简化处理), 无保证金概念(杠杆现货除外)
			notional := math.Abs(p.Qty) * calcPrice
			totalNotional += notional
			warnings = append(warnings, "spot risk simplified (no margin calc)")

		case InstrumentOption:
			// 期权逻辑 (Day 2/3 的内容，这里为了整合暂时简化)
			// 期权卖方才有保证金，买方只有权利金风险
			// 暂时占位
			notional := math.Abs(p.Qty) * calcPrice
			totalNotional += notional
			warnings = append(warnings, "option risk simplified (pending integration)")
		}
	}

	// 核心逻辑更新：计算动态权益
	// Equity = 静态余额 (Balance) + 总未实现盈亏 (TotalUPnL)
	equity := in.Account.Balance + totalUPnL

	// 核心逻辑更新：计算风险率
	// 这里我们用 MaintMargin / Equity 作为风险指标
	// 如果 > 100% (即 1.0)，说明权益不够支付维持保证金 -> 触发强平
	var riskRatio float64
	if equity > 0 {
		riskRatio = totalMaintMrgn / equity
	} else {
		// 权益已经 <= 0，也就是穿仓了，风险无穷大
		riskRatio = math.Inf(1)
	}

	return RiskOutput{
		Notional:       totalNotional,
		TotalUPnL:      totalUPnL,
		Equity:         equity,
		MaintMarginReq: totalMaintMrgn,
		InitMarginReq:  totalInitMrgn,
		RiskRatio:      riskRatio,
		Warnings:       dedup(warnings),
	}, nil
}

// validateInput 做最基本的输入校验。
// 真实交易所会校验更多：价格时效性、symbol 合法性、仓位数量限制等。
// Day1 先保留最小集合。
func validateInput(in RiskInput) error {
	// init_margin_rate 必须在 (0,1]，否则保证金需求没有意义
	if in.Account.InitMarginRate <= 0 || in.Account.InitMarginRate > 1 {
		return errors.New("init_margin_rate must be in (0,1]")
	}

	// 没有仓位就没必要算风险（你也可以改成返回全 0，但 Day1 先严格些）
	if len(in.Positions) == 0 {
		return errors.New("positions cannot be empty")
	}

	// prices 必须存在（map 为 nil 会导致查价格 panic 或永远缺价格）
	if in.Prices == nil {
		return errors.New("prices cannot be nil")
	}
	return nil
}

// dedup 对 warnings 去重，避免多个 perp 仓位重复提示多次。
func dedup(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
