package perp

import (
	"math"
	"testing"
)

// 基准测试：核心风险计算
func BenchmarkCalculateRisk(b *testing.B) {
	// 准备数据
	pos := Position{
		Qty:             1.5,
		EntryPrice:      50000.0,
		MarkPrice:       48000.0, // 亏损状态
		MaintenanceRate: 0.005,
		InitialRate:     0.1,
	}
	balance := 10000.0

	// 重置计时器，排除初始化时间
	b.ResetTimer()

	// 循环执行 N 次
	for i := 0; i < b.N; i++ {
		// 这里的 _ 是为了防止编译器优化掉这个函数调用
		_ = CalculateRisk(pos, balance)
	}
}

func TestCalculateLiquidationPrice(t *testing.T) {
	// 测试多仓
	t.Run("Long Position", func(t *testing.T) {
		qty := 1.0
		entryPrice := 50000.0
		balance := 1000.0
		mmr := 0.005

		liqPrice := CalculateLiquidationPrice(qty, entryPrice, balance, mmr)

		// 预期 ≈ 49246
		expected := 49246.23
		if math.Abs(liqPrice-expected) > 1 {
			t.Errorf("Expected ~%.2f, got %.2f", expected, liqPrice)
		}
		t.Logf("Long LiqPrice: %.2f", liqPrice)
	})

	// 测试空仓
	t.Run("Short Position", func(t *testing.T) {
		qty := -1.0
		entryPrice := 50000.0
		balance := 1000.0
		mmr := 0.005

		liqPrice := CalculateLiquidationPrice(qty, entryPrice, balance, mmr)

		// 预期 ≈ 50746
		expected := 50746.27
		if math.Abs(liqPrice-expected) > 1 {
			t.Errorf("Expected ~%.2f, got %.2f", expected, liqPrice)
		}
		t.Logf("Short LiqPrice: %.2f", liqPrice)
	})

	// 测试边界：无仓位
	t.Run("Zero Position", func(t *testing.T) {
		liqPrice := CalculateLiquidationPrice(0, 50000, 1000, 0.005)
		if liqPrice != 0 {
			t.Errorf("Expected 0, got %.2f", liqPrice)
		}
	})
}
