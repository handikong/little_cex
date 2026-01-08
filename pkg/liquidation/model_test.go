package liquidation

import (
	"testing"
	"time"
)

// =============================================================================
// RiskLevel 测试
// =============================================================================

func TestRiskLevel_String(t *testing.T) {
	tests := []struct {
		level    RiskLevel
		expected string
	}{
		{RiskLevelSafe, "SAFE"},
		{RiskLevelWarning, "WARNING"},
		{RiskLevelDanger, "DANGER"},
		{RiskLevelCritical, "CRITICAL"},
		{RiskLevelLiquidate, "LIQUIDATE"},
		{RiskLevel(99), "UNKNOWN"}, // 未知等级
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.level.String()
			if got != tt.expected {
				t.Errorf("RiskLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// CalculateRiskLevel 测试
// =============================================================================

func TestCalculateRiskLevel(t *testing.T) {
	// 测试所有边界条件
	// 阈值:
	//   Safe:      < 0.70
	//   Warning:   0.70 ~ <0.80
	//   Danger:    0.80 ~ <0.90
	//   Critical:  0.90 ~ <1.00
	//   Liquidate: >= 1.00

	tests := []struct {
		name      string
		riskRatio float64
		expected  RiskLevel
	}{
		// Safe 区域
		{"Safe - 0%", 0.0, RiskLevelSafe},
		{"Safe - 50%", 0.50, RiskLevelSafe},
		{"Safe - 69%", 0.69, RiskLevelSafe},
		{"Safe - 69.9%", 0.699, RiskLevelSafe},

		// Warning 边界 (70%)
		{"Warning - exactly 70%", 0.70, RiskLevelWarning},
		{"Warning - 75%", 0.75, RiskLevelWarning},
		{"Warning - 79%", 0.79, RiskLevelWarning},
		{"Warning - 79.9%", 0.799, RiskLevelWarning},

		// Danger 边界 (80%)
		{"Danger - exactly 80%", 0.80, RiskLevelDanger},
		{"Danger - 85%", 0.85, RiskLevelDanger},
		{"Danger - 89%", 0.89, RiskLevelDanger},
		{"Danger - 89.9%", 0.899, RiskLevelDanger},

		// Critical 边界 (90%)
		{"Critical - exactly 90%", 0.90, RiskLevelCritical},
		{"Critical - 95%", 0.95, RiskLevelCritical},
		{"Critical - 99%", 0.99, RiskLevelCritical},
		{"Critical - 99.9%", 0.999, RiskLevelCritical},

		// Liquidate 边界 (100%)
		{"Liquidate - exactly 100%", 1.00, RiskLevelLiquidate},
		{"Liquidate - 110%", 1.10, RiskLevelLiquidate},
		{"Liquidate - 200%", 2.00, RiskLevelLiquidate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateRiskLevel(tt.riskRatio)
			if got != tt.expected {
				t.Errorf("CalculateRiskLevel(%v) = %v, want %v", tt.riskRatio, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// UserRiskData 测试
// =============================================================================

func TestNewUserRiskData(t *testing.T) {
	userID := int64(12345)
	beforeTime := time.Now().UnixNano()

	data := NewUserRiskData(userID)

	afterTime := time.Now().UnixNano()

	// 验证 UserID
	if data.UserID != userID {
		t.Errorf("UserID = %d, want %d", data.UserID, userID)
	}

	// 验证 LiquidationPrices 已初始化
	if data.LiquidationPrices == nil {
		t.Error("LiquidationPrices should be initialized, got nil")
	}

	// 验证 Symbols 已初始化
	if data.Symbols == nil {
		t.Error("Symbols should be initialized, got nil")
	}

	// 验证 UpdatedAt 在合理范围内
	if data.UpdatedAt < beforeTime || data.UpdatedAt > afterTime {
		t.Errorf("UpdatedAt = %d, should be between %d and %d",
			data.UpdatedAt, beforeTime, afterTime)
	}
}

func TestUserRiskData_Fields(t *testing.T) {
	// 测试结构体字段赋值和读取
	data := UserRiskData{
		UserID:      1001,
		RiskRatio:   0.85,
		Equity:      10000.0,
		MaintMargin: 8500.0,
		Level:       RiskLevelDanger,
		UpdatedAt:   time.Now().UnixNano(),
		Symbols:     []string{"BTC_USDT", "ETH_USDT"},
		LiquidationPrices: map[string]float64{
			"BTC_USDT": 45000.0,
			"ETH_USDT": 2800.0,
		},
	}

	if data.UserID != 1001 {
		t.Errorf("UserID = %d, want 1001", data.UserID)
	}

	if data.RiskRatio != 0.85 {
		t.Errorf("RiskRatio = %v, want 0.85", data.RiskRatio)
	}

	if data.Level != RiskLevelDanger {
		t.Errorf("Level = %v, want RiskLevelDanger", data.Level)
	}

	if len(data.Symbols) != 2 {
		t.Errorf("Symbols length = %d, want 2", len(data.Symbols))
	}

	if price, ok := data.LiquidationPrices["BTC_USDT"]; !ok || price != 45000.0 {
		t.Errorf("LiquidationPrices[BTC_USDT] = %v, want 45000.0", price)
	}
}

// =============================================================================
// LiquidationTask 测试
// =============================================================================

func TestLiquidationTask_Fields(t *testing.T) {
	now := time.Now()
	task := LiquidationTask{
		UserID:        2001,
		RiskRatio:     1.05,
		TriggerPrice:  50000.0,
		TriggerSymbol: "BTC_USDT",
		CreatedAt:     now,
		Priority:      1.05,
	}

	if task.UserID != 2001 {
		t.Errorf("UserID = %d, want 2001", task.UserID)
	}

	if task.RiskRatio != 1.05 {
		t.Errorf("RiskRatio = %v, want 1.05", task.RiskRatio)
	}

	if task.TriggerSymbol != "BTC_USDT" {
		t.Errorf("TriggerSymbol = %s, want BTC_USDT", task.TriggerSymbol)
	}

	if task.CreatedAt != now {
		t.Errorf("CreatedAt = %v, want %v", task.CreatedAt, now)
	}
}

// =============================================================================
// LiquidationResult 测试
// =============================================================================

func TestLiquidationResult_Fields(t *testing.T) {
	now := time.Now()
	result := LiquidationResult{
		UserID:     3001,
		Success:    true,
		Error:      nil,
		ExecutedAt: now,
		Details: LiquidationDetails{
			ClosedPositions:  3,
			TotalPnL:         -500.0,
			RemainingBalance: 200.0,
		},
	}

	if !result.Success {
		t.Error("Success should be true")
	}

	if result.Details.ClosedPositions != 3 {
		t.Errorf("ClosedPositions = %d, want 3", result.Details.ClosedPositions)
	}

	if result.Details.TotalPnL != -500.0 {
		t.Errorf("TotalPnL = %v, want -500.0", result.Details.TotalPnL)
	}
}
