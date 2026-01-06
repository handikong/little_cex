package risk

import (
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
