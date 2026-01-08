package risk

import (
	"math"
	"testing"
)

func TestComputeRisk_PerpPnL(t *testing.T) {
	e := NewEngine()

	// 场景：
	// 1. 账户里有 1000 U
	// 2. 开多 BTC (Qty=0.1)，开仓价 30000
	// 3. 现在 BTC 涨到了 35000 (MarkPrice)
	// 预期：
	// uPnL = 0.1 * (35000 - 30000) = 500
	// Equity = 1000 + 500 = 1500

	in := RiskInput{
		Account: Account{Balance: 1000, InitMarginRate: 0.1},
		Positions: []Position{
			{
				Instrument:            InstrumentPerp,
				Symbol:                "BTC_USDT",
				Qty:                   0.1,
				EntryPrice:            30000,
				MaintenanceMarginRate: 0.01, // 1% MMR
			},
		},
		Prices: map[string]PriceSnapshot{
			"BTC_USDT": {Price: 35000, MarkPrice: 35000}, // 最新价和标记价一致
		},
	}

	out, err := e.ComputeRisk(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 验证 uPnL
	if out.TotalUPnL != 500 {
		t.Errorf("expected uPnL 500, got %v", out.TotalUPnL)
	}

	// 验证 Equity
	if out.Equity != 1500 {
		t.Errorf("expected Equity 1500, got %v", out.Equity)
	}

	// 验证 Margin Requirement
	// Notional = 0.1 * 35000 = 3500
	// MaintMargin = 3500 * 0.01 = 35
	if out.MaintMarginReq != 35 {
		t.Errorf("expected MaintMargin 35, got %v", out.MaintMarginReq)
	}

	// 验证 Risk Ratio
	// Ratio = 35 / 1500 = 0.0233...
	expectedRatio := 35.0 / 1500.0
	if out.RiskRatio != expectedRatio {
		t.Errorf("expected RiskRatio %v, got %v", expectedRatio, out.RiskRatio)
	}
}

func TestComputeRisk_Liquidation(t *testing.T) {
	e := NewEngine()

	// 场景：
	// 1. 账户 100 U
	// 2. 开 100 倍杠杆做多 (假设)
	// 3. 价格下跌，导致 权益 < 维持保证金

	in := RiskInput{
		Account: Account{Balance: 100, InitMarginRate: 0.01}, // 1% IM
		Positions: []Position{
			{
				Instrument:            InstrumentPerp,
				Symbol:                "ETH_USDT",
				Qty:                   10,    // 10 个 ETH
				EntryPrice:            2000,  // 成本 20000
				MaintenanceMarginRate: 0.005, // 0.5% MMR = 100 U
			},
		},
		Prices: map[string]PriceSnapshot{
			// 价格跌到 1995
			// uPnL = 10 * (1995 - 2000) = -50
			// Equity = 100 - 50 = 50
			// Notional = 19950
			// MaintMargin = 19950 * 0.005 = 99.75
			// 因为 Equity (50) < MaintMargin (99.75)，这人应该炸了
			"ETH_USDT": {MarkPrice: 1995},
		},
	}

	out, err := e.ComputeRisk(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if out.RiskRatio <= 1.0 {
		t.Errorf("expected RiskRatio > 1 (liquidation), got %v", out.RiskRatio)
	}
}

// almostEqual 用于比较两个 float64 是否相等 (容忍微小误差)
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestEngine_Integration(t *testing.T) {
	e := NewEngine()

	// 基础设置：账户有 10,000 U 余额
	baseAccount := Account{
		Balance:        10000,
		InitMarginRate: 0.1, // 默认初始保证金率 10%
	}

	tests := []struct {
		name string
		in   RiskInput
		// 期望的输出检查函数
		check func(t *testing.T, out RiskOutput)
	}{
		{
			name: "场景1: 单边做多赚钱 (Happy Path)",
			in: RiskInput{
				Account: baseAccount,
				Positions: []Position{
					{
						Instrument:            InstrumentPerp,
						Symbol:                "BTC_USDT",
						Qty:                   1.0,     // 持仓 1 BTC
						EntryPrice:            50000.0, // 开仓价 50000
						MaintenanceMarginRate: 0.005,   // 维持保证金 0.5%
					},
				},
				Prices: map[string]PriceSnapshot{
					// 价格涨到了 55000
					"BTC_USDT": {MarkPrice: 55000, Price: 55000},
				},
			},
			check: func(t *testing.T, out RiskOutput) {
				// 1. 验证 uPnL
				// 公式: Qty * (Mark - Entry) = 1 * (55000 - 50000) = 5000
				if !almostEqual(out.TotalUPnL, 5000) {
					t.Errorf("uPnL wrong, got %v want 5000", out.TotalUPnL)
				}

				// 2. 验证 Equity (动态权益)
				// 公式: Balance + uPnL = 10000 + 5000 = 15000
				if !almostEqual(out.Equity, 15000) {
					t.Errorf("Equity wrong, got %v want 15000", out.Equity)
				}

				// 3. 验证维持保证金需求
				// 公式: Notional * MMR = (1 * 55000) * 0.005 = 275
				if !almostEqual(out.MaintMarginReq, 275) {
					t.Errorf("MaintMarginReq wrong, got %v want 275", out.MaintMarginReq)
				}

				// 4. 验证 Risk Ratio
				// 公式: MaintMargin / Equity = 275 / 15000 ≈ 0.01833...
				expectedRatio := 275.0 / 15000.0
				if !almostEqual(out.RiskRatio, expectedRatio) {
					t.Errorf("RiskRatio wrong, got %v want %v", out.RiskRatio, expectedRatio)
				}
			},
		},
		{
			name: "场景2: 多空双开 (Cross Margin 对冲)",
			in: RiskInput{
				Account: baseAccount,
				Positions: []Position{
					// 仓位 A: 做多 BTC (赚了)
					{
						Instrument:            InstrumentPerp,
						Symbol:                "BTC_USDT",
						Qty:                   1.0,
						EntryPrice:            50000.0,
						MaintenanceMarginRate: 0.005,
					},
					// 仓位 B: 做多 ETH (亏了 - 模拟开反了)
					// 这里演示如果 ETH 跌了，会抵消 BTC 的利润
					{
						Instrument:            InstrumentPerp,
						Symbol:                "ETH_USDT",
						Qty:                   10.0,   // 10 个 ETH
						EntryPrice:            3000.0, // 开仓价 3000
						MaintenanceMarginRate: 0.01,   // 维持保证金 1%
					},
				},
				Prices: map[string]PriceSnapshot{
					"BTC_USDT": {MarkPrice: 55000}, // BTC 涨 5000 -> 赚 5000
					"ETH_USDT": {MarkPrice: 2000},  // ETH 跌 1000 -> 亏 10 * 1000 = 10000
				},
			},
			check: func(t *testing.T, out RiskOutput) {
				// 1. 验证总 uPnL
				// BTC赚 5000 + ETH亏 10000 = 总亏 5000
				if !almostEqual(out.TotalUPnL, -5000) {
					t.Errorf("TotalUPnL wrong, got %v want -5000", out.TotalUPnL)
				}

				// 2. 验证 Equity
				// 10000 + (-5000) = 5000
				if !almostEqual(out.Equity, 5000) {
					t.Errorf("Equity wrong, got %v want 5000", out.Equity)
				}

				// 3. 验证风险率是否上升
				// BTC Notional = 55000, MMR = 275
				// ETH Notional = 20000, MMR = 200 (20000 * 0.01)
				// Total MMR = 475
				// RiskRatio = 475 / 5000 = 0.095
				if !almostEqual(out.MaintMarginReq, 475) {
					t.Errorf("MaintMarginReq wrong, got %v", out.MaintMarginReq)
				}
			},
		},
		{
			name: "场景3: 爆仓触发 (Liquidation)",
			in: RiskInput{
				// 这是一个只有 100 U 的穷鬼账户
				Account: Account{Balance: 100, InitMarginRate: 0.1},
				Positions: []Position{
					{
						Instrument:            InstrumentPerp,
						Symbol:                "BTC_USDT",
						Qty:                   0.1, // 持仓 0.1 BTC (价值约 5000 U)
						EntryPrice:            50000.0,
						MaintenanceMarginRate: 0.005, // MMR = 0.5%
					},
				},
				Prices: map[string]PriceSnapshot{
					// 价格稍微跌一点点: 50000 -> 49000
					// 亏损 = 0.1 * (49000 - 50000) = -100 U
					"BTC_USDT": {MarkPrice: 49000},
				},
			},
			check: func(t *testing.T, out RiskOutput) {
				// 1. 验证 Equity
				// Balance 100 + uPnL -100 = 0
				if !almostEqual(out.Equity, 0) {
					t.Errorf("Equity wrong, got %v want 0", out.Equity)
				}

				// 2. 验证风险率
				// MMR = 4900(名义价值) * 0.005 = 24.5
				// RiskRatio = 24.5 / 0 = Inf (无穷大)
				if out.RiskRatio <= 1.0 {
					t.Errorf("Expected liquidation (Ratio > 1), got %v", out.RiskRatio)
				}

				// 确认这是一个绝对会爆仓的状态
				if !math.IsInf(out.RiskRatio, 1) {
					// 如果你的逻辑处理了除0，可能返回 Inf，这正是我们想要的
					// 如果你返回了 0 或其他值，说明除0逻辑没处理好
					t.Logf("RiskRatio is %v (Expected Inf or >1)", out.RiskRatio)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := e.ComputeRisk(tt.in)
			if err != nil {
				t.Fatalf("ComputeRisk error: %v", err)
			}
			tt.check(t, out)
		})
	}
}
