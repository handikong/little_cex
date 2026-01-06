package options

import (
	"errors"
	"fmt"
	"math"
)

var (
	// 错误信息，针对无效输入
	ErrInvalidInputs = errors.New("invalid inputs")
)

/*
Greeks 是衡量期权价格对不同市场因素敏感度的指标。对于 欧式期权，我们常见的 Greeks 有：

Delta: 期权价格相对于标的资产价格变动的敏感度。即标的资产价格变动 1 单位时，期权价格的变动量。

Gamma: Delta 对标的资产价格的敏感度。即标的资产价格变动 1 单位时，Delta 的变动量。

Vega: 期权价格相对于标的资产波动率变动的敏感度。即波动率变动 1 单位时，期权价格的变动量。

Theta: 期权价格相对于时间流逝的敏感度。即期权剩余到期时间变动 1 单位时，期权价格的变动量。


*/

// PriceCallBS 计算欧式看涨期权（Call）的 Black-Scholes 价格（无分红）
// S: 当前标的资产的价格（现货价格）
// K: 期权执行价（strike price）
// r: 无风险利率（以年化为基础的连续复利利率）
// sigma: 标的资产的波动率（年化波动率）
// T: 期权剩余到期时间（以年为单位）
func PriceCallBS(S, K, r, sigma, T float64) (float64, error) {
	// 检查输入是否合法
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}

	// 如果 T=0（到期时），看涨期权的价格就是 max(S - K, 0)
	if T == 0 {
		return math.Max(S-K, 0), nil
	}

	// 如果波动率为0，价格是确定的
	if sigma == 0 {
		// Call 的价格是 max(S - K*e^{-rT}, 0)
		return math.Max(S-K*math.Exp(-r*T), 0), nil
	}

	// 计算 d1 和 d2，用于 Black-Scholes 公式
	d1 := calcD1(S, K, r, sigma, T)
	d2 := d1 - sigma*math.Sqrt(T)

	// Black-Scholes 公式计算 Call 期权的价格
	call := S*normCDF(d1) - K*math.Exp(-r*T)*normCDF(d2)
	return call, nil
}

// PricePutBS 计算欧式看跌期权（Put）的 Black-Scholes 价格（无分红）
func PricePutBS(S, K, r, sigma, T float64) (float64, error) {
	// 检查输入是否合法
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}

	// 如果 T=0（到期时），看跌期权的价格就是 max(K - S, 0)
	if T == 0 {
		return math.Max(K-S, 0), nil
	}

	// 如果波动率为0，价格是确定的
	if sigma == 0 {
		// Put 的价格是 max(K*e^{-rT} - S, 0)
		return math.Max(K*math.Exp(-r*T)-S, 0), nil
	}

	// 计算 d1 和 d2，用于 Black-Scholes 公式
	d1 := calcD1(S, K, r, sigma, T)
	d2 := d1 - sigma*math.Sqrt(T)

	// Black-Scholes 公式计算 Put 期权的价格
	put := K*math.Exp(-r*T)*normCDF(-d2) - S*normCDF(-d1)
	return put, nil
}

// ImpliedVolatility 通过期权市场价格反推隐含波动率
func ImpliedVolatility(S, K, r, marketPrice, T float64) (float64, error) {
	// 初始猜测波动率，通常从 20% 开始
	sigma := 0.2
	tolerance := 1e-6
	maxIterations := 100

	for i := 0; i < maxIterations; i++ {
		// 计算市场价格对应的期权价格（使用当前猜测的波动率）
		optionPrice, err := PriceCallBS(S, K, r, sigma, T)
		if err != nil {
			return 0, err
		}

		// 计算期权价格的 Vega（对波动率的敏感度）
		vega, err := Vega(S, K, r, sigma, T)
		if err != nil {
			return 0, err
		}

		// 计算误差（期权市场价格与计算出来的期权价格之间的差距）
		priceError := marketPrice - optionPrice

		// 如果误差足够小，则停止迭代
		if math.Abs(priceError) < tolerance {
			return sigma, nil
		}

		// 根据牛顿法公式更新波动率
		sigma = sigma + priceError/vega
	}

	// 如果迭代次数过多仍未收敛，则返回错误
	return 0, errors.New("failed to converge to implied volatility")
}

// PriceScenarioAnalysis 用于模拟不同标的资产价格和波动率情景下期权价格的变化
// S: 当前标的资产价格
// K: 执行价格
// r: 无风险利率
// sigma: 当前波动率
// T: 到期时间
// priceChange: 模拟价格的变化比例，例如 0.05 表示价格上升 5%，-0.05 表示下降 5%
// volChange: 模拟波动率的变化比例
func PriceScenarioAnalysis(S, K, r, sigma, T, priceChange, volChange float64) {
	// 模拟价格变化
	newPrice := S * (1 + priceChange)
	callPrice, err := PriceCallBS(newPrice, K, r, sigma, T)
	if err != nil {
		fmt.Printf("Error calculating call price for price change scenario: %v\n", err)
		return
	}
	putPrice, err := PricePutBS(newPrice, K, r, sigma, T)
	if err != nil {
		fmt.Printf("Error calculating put price for price change scenario: %v\n", err)
		return
	}
	fmt.Printf("New Asset Price: %.2f\n", newPrice)
	fmt.Printf("Call Price (Price Change Scenario): %.4f\n", callPrice)
	fmt.Printf("Put Price (Price Change Scenario): %.4f\n", putPrice)

	// 模拟波动率变化
	newSigma := sigma * (1 + volChange)
	callPriceVol, err := PriceCallBS(S, K, r, newSigma, T)
	if err != nil {
		fmt.Printf("Error calculating call price for volatility change scenario: %v\n", err)
		return
	}
	putPriceVol, err := PricePutBS(S, K, r, newSigma, T)
	if err != nil {
		fmt.Printf("Error calculating put price for volatility change scenario: %v\n", err)
		return
	}
	fmt.Printf("New Volatility: %.4f\n", newSigma)
	fmt.Printf("Call Price (Volatility Change Scenario): %.4f\n", callPriceVol)
	fmt.Printf("Put Price (Volatility Change Scenario): %.4f\n", putPriceVol)
}

// DeltaCall 计算欧式看涨期权的 Delta
func DeltaCall(S, K, r, sigma, T float64) (float64, error) {
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}
	d1 := calcD1(S, K, r, sigma, T)
	return normCDF(d1), nil
}

// Gamma 计算欧式期权的 Gamma
func Gamma(S, K, r, sigma, T float64) (float64, error) {
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}
	d1 := calcD1(S, K, r, sigma, T)
	return normPDF(d1) / (S * sigma * math.Sqrt(T)), nil
}

// Vega 计算欧式期权的 Vega
func Vega(S, K, r, sigma, T float64) (float64, error) {
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}
	d1 := calcD1(S, K, r, sigma, T)
	return S * math.Sqrt(T) * normPDF(d1), nil
}

// ThetaCall 计算欧式看涨期权的 Theta
func ThetaCall(S, K, r, sigma, T float64) (float64, error) {
	if err := validateBSInputs(S, K, sigma, T); err != nil {
		return 0, err
	}
	d1 := calcD1(S, K, r, sigma, T)
	d2 := d1 - sigma*math.Sqrt(T)

	theta := -S*normPDF(d1)*sigma/(2*math.Sqrt(T)) - r*K*math.Exp(-r*T)*normCDF(d2)
	return theta, nil
}

// validateBSInputs 检查 Black-Scholes 输入的有效性
func validateBSInputs(S, K, sigma, T float64) error {
	// 当前标的价格和执行价必须大于零
	if S <= 0 || K <= 0 {
		return ErrInvalidInputs
	}
	// 波动率和到期时间必须大于零
	if sigma < 0 || T < 0 {
		return ErrInvalidInputs
	}
	return nil
}

// calcD1 计算 Black-Scholes 公式中的 d1
// d1 = [ln(S/K) + (r + 0.5*sigma^2)T] / (sigma * sqrt(T))
func calcD1(S, K, r, sigma, T float64) float64 {
	return (math.Log(S/K) + (r+0.5*sigma*sigma)*T) / (sigma * math.Sqrt(T))
}

// normCDF 计算标准正态分布的 CDF（累计分布函数）
// N(x) = 0.5 * (1 + erf(x / sqrt(2)))
func normCDF(x float64) float64 {
	return 0.5 * (1.0 + math.Erf(x/math.Sqrt2))
}

// normPDF 计算标准正态分布的 PDF（概率密度函数）
func normPDF(x float64) float64 {
	return (1.0 / math.Sqrt(2*math.Pi)) * math.Exp(-0.5*x*x)
}
