package risk

import (
	"errors"
	"math"

	"max.com/pkg/risk/perp"
)

// Engine 是风险引擎对象。
// 你可以把它理解成“一个计算器”：
// 输入 RiskInput → 输出 RiskOutput。

type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// ComputeRisk 核心风控入口
// 这是一个 CPU 密集型函数，Day 4 优化后实现了 Zero Allocation (除 input 带来的开销外)
func (e *Engine) ComputeRisk(in RiskInput) (RiskOutput, error) {
	// 1. 基础校验
	if err := validateInput(in); err != nil {
		return RiskOutput{}, err
	}

	var (
		totalNotional  float64
		totalUPnL      float64
		totalMaintMrgn float64 // 总维持保证金需求
		totalInitMrgn  float64 // 总初始保证金需求
		warnings       []string
	)

	// 2. 遍历仓位 (The Loop)
	for _, p := range in.Positions {
		// 2.1 获取价格
		priceSnap, ok := in.Prices[p.Symbol]
		if !ok {
			return RiskOutput{}, errors.New("missing price for: " + p.Symbol)
		}

		// 优先使用 MarkPrice，降级使用 LastPrice
		calcPrice := priceSnap.MarkPrice
		if calcPrice == 0 {
			calcPrice = priceSnap.Price
			warnings = append(warnings, "using last_price as mark_price for "+p.Symbol)
		}
		if calcPrice <= 0 {
			return RiskOutput{}, errors.New("invalid price for: " + p.Symbol)
		}

		// 2.2 根据产品类型分发
		switch p.Instrument {
		case InstrumentPerp:
			// [Day 4 核心]：数据结构转换 (Model -> Perp)
			// 这里将外部宽泛的 JSON 结构，转为内部紧凑的计算结构
			internalPos := perp.Position{
				Qty:        p.Qty,
				EntryPrice: p.EntryPrice,
				MarkPrice:  calcPrice,
				// 假设 model 里没有这两个字段，暂时从 account 取或给默认值
				// 真实场景下，Position 结构体里应该包含这些
				MaintenanceRate: p.MaintenanceMarginRate,
				InitialRate:     in.Account.InitMarginRate,
			}

			// 如果 model 里没有设置 MMR，给一个默认兜底 (防止 panic)
			if internalPos.MaintenanceRate == 0 {
				internalPos.MaintenanceRate = 0.005 // 默认 0.5%
			}
			if internalPos.InitialRate == 0 {
				internalPos.InitialRate = 0.01 // 默认 1%
			}

			// [Day 4 核心]：调用高性能计算函数
			// 注意：这里传入 0 作为 balance，因为我们在循环里只算单个仓位的指标
			// 账户级的 Equity (余额+uPnL) 我们在循环外面统一算
			metrics := perp.CalculateRisk(internalPos, 0)

			// 2.3 聚合指标 (Aggregation)
			totalNotional += metrics.Notional
			totalUPnL += metrics.UnrealizedPnL
			totalMaintMrgn += metrics.MaintMarginReq
			totalInitMrgn += metrics.InitMarginReq

		case InstrumentSpot:
			// 现货简单处理
			notional := math.Abs(p.Qty) * calcPrice
			totalNotional += notional
		}
	}

	// 3. 账户级风控计算 (Cross Margin / 全仓模式)

	// 动态权益 = 静态余额 + 总未实现盈亏
	equity := in.Account.Balance + totalUPnL

	// 风险率 = 维持保证金 / 动态权益
	// Risk Ratio >= 1.0 意味着 权益 < 维持保证金 -> 爆仓
	var riskRatio float64
	if equity > 0 {
		riskRatio = totalMaintMrgn / equity
	} else {
		// 权益都负了，肯定是无穷大风险 (穿仓)
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

// validateInput 保持不变
func validateInput(in RiskInput) error {
	if len(in.Positions) == 0 {
		return errors.New("positions cannot be empty")
	}
	if in.Prices == nil {
		return errors.New("prices cannot be nil")
	}
	return nil
}

// dedup 保持不变
func dedup(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
