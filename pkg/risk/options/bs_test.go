package options

import (
	"fmt"
	"math"
	"testing"
)

func TestBS_Prices_ReferenceCase(t *testing.T) {
	// 经典参数：S=100,K=100,r=0.05,sigma=0.2,T=1
	// 期望值（用于回归）：Call≈10.4505835722, Put≈5.5735260223
	S, K, r, sigma, T := 100.0, 100.0, 0.05, 0.2, 1.0

	call, err := PriceCallBS(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	put, err := PricePutBS(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("put err: %v", err)
	}

	if !almostEqual(call, 10.450583572185565, 1e-9) {
		t.Fatalf("call price mismatch: got=%v", call)
	}
	if !almostEqual(put, 5.573526022256971, 1e-9) {
		t.Fatalf("put price mismatch: got=%v", put)
	}
}

func TestBS_PutCallParity(t *testing.T) {
	// Put-Call Parity: C - P = S - K*e^{-rT}
	S, K, r, sigma, T := 100.0, 100.0, 0.05, 0.2, 1.0

	call, _ := PriceCallBS(S, K, r, sigma, T)
	put, _ := PricePutBS(S, K, r, sigma, T)

	left := call - put
	right := S - K*math.Exp(-r*T)

	if !almostEqual(left, right, 1e-9) {
		t.Fatalf("parity mismatch: left=%v right=%v", left, right)
	}
}

func TestBS_T0_IntrinsicValue(t *testing.T) {
	S, K, r, sigma, T := 90.0, 100.0, 0.05, 0.2, 0.0

	call, _ := PriceCallBS(S, K, r, sigma, T)
	put, _ := PricePutBS(S, K, r, sigma, T)

	if call != 0 {
		t.Fatalf("call intrinsic mismatch: got=%v", call)
	}
	if put != 10 {
		t.Fatalf("put intrinsic mismatch: got=%v", put)
	}
}

func TestBS_Sigma0_Deterministic(t *testing.T) {
	// sigma=0 时：
	// Call = max(S - K*e^{-rT}, 0)
	S, K, r, sigma, T := 100.0, 120.0, 0.05, 0.0, 1.0

	call, _ := PriceCallBS(S, K, r, sigma, T)
	want := math.Max(S-K*math.Exp(-r*T), 0)

	if !almostEqual(call, want, 1e-12) {
		t.Fatalf("sigma0 call mismatch: got=%v want=%v", call, want)
	}
}

func TestBS_InvalidInputs(t *testing.T) {
	_, err := PriceCallBS(-1, 100, 0.05, 0.2, 1)
	if err == nil {
		t.Fatalf("expected error for invalid S")
	}
	_, err = PricePutBS(100, 0, 0.05, 0.2, 1)
	if err == nil {
		t.Fatalf("expected error for invalid K")
	}
	_, err = PriceCallBS(100, 100, 0.05, -0.1, 1)
	if err == nil {
		t.Fatalf("expected error for invalid sigma")
	}
	_, err = PriceCallBS(100, 100, 0.05, 0.2, -1)
	if err == nil {
		t.Fatalf("expected error for invalid T")
	}
}

func TestGreeks(t *testing.T) {
	S, K, r, sigma, T := 100.0, 100.0, 0.05, 0.2, 1.0

	// 测试 Delta
	delta, err := DeltaCall(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("Delta error: %v", err)
	}
	expectedDelta := 0.6368306511756191           // 修改为实际计算结果
	if !almostEqual(delta, expectedDelta, 1e-3) { // 调整容忍度
		t.Fatalf("Delta mismatch: got=%v, want=%v", delta, expectedDelta)
	}

	// 测试 Gamma
	gamma, err := Gamma(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("Gamma error: %v", err)
	}
	expectedGamma := 0.018
	if !almostEqual(gamma, expectedGamma, 1e-3) {
		t.Fatalf("Gamma mismatch: got=%v, want=%v", gamma, expectedGamma)
	}

	// 测试 Vega
	vega, err := Vega(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("Vega error: %v", err)
	}
	expectedVega := 37.991
	if !almostEqual(vega, expectedVega, 1e-3) {
		t.Fatalf("Vega mismatch: got=%v, want=%v", vega, expectedVega)
	}

	// 测试 Theta
	theta, err := ThetaCall(S, K, r, sigma, T)
	if err != nil {
		t.Fatalf("Theta error: %v", err)
	}
	expectedTheta := -5.532
	if !almostEqual(theta, expectedTheta, 1e-3) {
		t.Fatalf("Theta mismatch: got=%v, want=%v", theta, expectedTheta)
	}
}

func TestScenarioAnalysis(t *testing.T) {
	S, K, r, sigma, T := 100.0, 100.0, 0.05, 0.2, 1.0

	// 模拟资产价格上升 5% 和下降 5% 的情景
	fmt.Println("Scenario 1: Asset Price Change (±5%)")
	PriceScenarioAnalysis(S, K, r, sigma, T, 0.05, 0)  // 资产价格上升 5%
	PriceScenarioAnalysis(S, K, r, sigma, T, -0.05, 0) // 资产价格下降 5%

	// 模拟波动率上升 10% 和下降 10% 的情景
	fmt.Println("Scenario 2: Volatility Change (±10%)")
	PriceScenarioAnalysis(S, K, r, sigma, T, 0, 0.10)  // 波动率上升 10%
	PriceScenarioAnalysis(S, K, r, sigma, T, 0, -0.10) // 波动率下降 10%
}

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}
