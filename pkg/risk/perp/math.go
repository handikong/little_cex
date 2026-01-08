package perp

// Position 核心持仓结构
// 按照 Memory Alignment 优化顺序：从大到小排列字段
type Position struct {
	Qty        float64 // 持仓数量 (+多, -空)
	EntryPrice float64 // 开仓均价

	// 运行时动态字段（由风控引擎计算填充）
	MarkPrice        float64 // 当前标记价格
	LiquidationPrice float64 // 强平价格 (预计算)
	CumFundingFee    float64 // 累计已结算资金费 (从开仓到现在)

	// 配置字段
	MaintenanceRate float64 // 维持保证金率 (如 0.005)
	InitialRate     float64 // 初始保证金率 (如 0.1)
}

// RiskMetrics 计算后的风险指标
// 使用值传递，避免逃逸到堆内存 (Zero Allocation)
type RiskMetrics struct {
	Notional       float64 // 名义价值
	UnrealizedPnL  float64 // 未实现盈亏
	MaintMarginReq float64 // 维持保证金需求
	InitMarginReq  float64 // 初始保证金需求
	IsLiquidatable bool    // 是否应该强平
}

type RiskLevel int

const (
	RiskLevelSafe      RiskLevel = iota // 绿色：安全 (风险率 < 70%)
	RiskLevelWarning                    // 黄色：预警 (风险率 70% - 90%)
	RiskLevelDanger                     // 红色：危险 (风险率 90% - 100%)
	RiskLevelLiquidate                  // 强平：引擎接管 (风险率 >= 100%)
)

// 风险阈值配置
const (
	WarningThreshold   = 0.70 // 70% 触发预警
	DangerThreshold    = 0.90 // 90% 触发危险
	LiquidateThreshold = 1.00 // 100% 触发强平
)

// CalculateRisk 核心计算函数
//
// Input: 仓位快照 (值传递)
// Output: 风险指标 (值传递)
//
// 为什么不用指针？
// 对于这种小结构体，拷贝到栈上的速度比 GC 扫描堆指针的速度快得多。
// CPU 的寄存器可以直接装下这几个 float64。
func CalculateRisk(pos Position, balance float64) RiskMetrics {
	// 1. 计算名义价值
	// Notional = abs(Qty) * MarkPrice
	absQty := pos.Qty
	if absQty < 0 {
		absQty = -absQty
	}
	notional := absQty * pos.MarkPrice

	// 2. 计算未实现盈亏 uPnL
	// uPnL = Qty * (MarkPrice - EntryPrice)
	// Qty 为负时，(Mark < Entry) 结果为正，逻辑自洽
	uPnL := pos.Qty * (pos.MarkPrice - pos.EntryPrice)

	// 3. 计算保证金需求
	mmr := notional * pos.MaintenanceRate
	imr := notional * pos.InitialRate

	// 4. 判断强平
	// 权益 = 余额 + uPnL
	equity := balance + uPnL

	// 如果权益 <= 维持保证金，触发强平
	// 注意：这里没有除法，乘法比除法快，且避免除0错误
	isLiq := equity <= mmr

	return RiskMetrics{
		Notional:       notional,
		UnrealizedPnL:  uPnL,
		MaintMarginReq: mmr,
		InitMarginReq:  imr,
		IsLiquidatable: isLiq,
	}
}

// CalculateFundingFee 计算单次资金费
// rate: 资金费率 (如 0.0001)
// 返回: 需要支付的金额 (正数=付钱，负数=收钱)
func CalculateFundingFee(pos Position, rate float64) float64 {
	// 规则：多头支付资金费率，空头收取
	// Payment = Notional * Rate * sign(Qty)
	// 简化版：Payment = Qty * MarkPrice * Rate
	return pos.Qty * pos.MarkPrice * rate
}

// CalculateLiquidationPrice 计算永续合约的强平价格
//
// 【什么是强平价格？】
// 当行情价格达到这个价位时，用户的"动态权益"恰好等于"维持保证金"，
// 交易所会强制平仓以防止穿仓（亏损超过保证金）。
//
// 【核心公式推导】
// 强平条件: Equity <= Maintenance Margin Requirement (MMR)
//
// 其中:
//
//	Equity = Balance + uPnL
//	uPnL = Qty * (MarkPrice - EntryPrice)
//	MMR = |Qty| * MarkPrice * MaintenanceRate
//
// 设 P 为强平价格 (即 MarkPrice 达到 P 时触发强平):
//
//	Balance + Qty * (P - EntryPrice) = |Qty| * P * MaintenanceRate
//
// 解这个方程，得到 P：
//
// 【多仓 (Qty > 0)】
//
//	Balance + Qty * P - Qty * EntryPrice = Qty * P * MaintenanceRate
//	Balance - Qty * EntryPrice = Qty * P * (MaintenanceRate - 1)
//	P = (Balance - Qty * EntryPrice) / (Qty * (MaintenanceRate - 1))
//
//	由于 MaintenanceRate 通常很小 (如 0.5%)，(MaintenanceRate - 1) 是负数，
//	所以分母是负数。为了让公式更直观，我们取反：
//
//	P_long = (Qty * EntryPrice - Balance) / (Qty * (1 - MaintenanceRate))
//
// 【空仓 (Qty < 0)】
//
//	设 absQty = |Qty| = -Qty (因为 Qty 是负数)
//
//	Balance + Qty * (P - EntryPrice) = absQty * P * MaintenanceRate
//	Balance - absQty * (P - EntryPrice) = absQty * P * MaintenanceRate
//	Balance - absQty * P + absQty * EntryPrice = absQty * P * MaintenanceRate
//	Balance + absQty * EntryPrice = absQty * P * (1 + MaintenanceRate)
//	P = (Balance + absQty * EntryPrice) / (absQty * (1 + MaintenanceRate))
//
//	P_short = (Balance + |Qty| * EntryPrice) / (|Qty| * (1 + MaintenanceRate))
//
// 【参数说明】
//
//	qty:             仓位数量，正数=多仓，负数=空仓
//	entryPrice:      开仓均价
//	balance:         账户可用余额（或分配给该仓位的保证金）
//	maintenanceRate: 维持保证金率，如 0.005 表示 0.5%
//
// 【返回值】
//
//	强平价格 (float64)
//	如果仓位为 0 或计算异常，返回 0
func CalculateLiquidationPrice(qty, entryPrice, balance, maintenanceRate float64) float64 {
	// 边界检查：没有仓位，则不存在强平价格
	if qty == 0 {
		return 0
	}

	// 边界检查：保证金率不能为 100% 或以上（否则分母为 0 或负）
	if maintenanceRate >= 1 {
		return 0
	}

	var liqPrice float64

	if qty > 0 {
		// ========== 多仓 ==========
		// 公式: P = (Qty * EntryPrice - Balance) / (Qty * (1 - MMR))
		//
		// 分子: 开仓成本 - 可用余额
		//       如果余额越多，分子越小，强平价格越低（离得越远，越安全）
		//
		// 分母: Qty * (1 - MMR)
		//       MMR 通常很小（0.5%），所以 (1 - MMR) ≈ 0.995
		//       分母约等于 Qty，是一个正数
		//
		// 整体: 强平价格 < 开仓价（价格跌破这个值就爆仓）
		//
		numerator := qty*entryPrice - balance
		denominator := qty * (1 - maintenanceRate)
		liqPrice = numerator / denominator

	} else {
		// ========== 空仓 ==========
		// 公式: P = (Balance + |Qty| * EntryPrice) / (|Qty| * (1 + MMR))
		//
		// 分子: 余额 + 开仓成本
		//       余额越多，分子越大，强平价格越高（离得越远，越安全）
		//
		// 分母: |Qty| * (1 + MMR)
		//       约等于 |Qty|
		//
		// 整体: 强平价格 > 开仓价（价格涨破这个值就爆仓）
		//
		absQty := -qty // qty 是负数，取反得到正数
		numerator := balance + absQty*entryPrice
		denominator := absQty * (1 + maintenanceRate)
		liqPrice = numerator / denominator
	}

	// 边界检查：强平价格不能为负
	if liqPrice < 0 {
		return 0
	}

	return liqPrice
}

// CalculateWarningPrices 计算各级预警价格
// 返回: (预警价格, 危险价格, 强平价格)
func CalculateWarningPrices(qty, entryPrice, balance, mmr float64) (warnPrice, dangerPrice, liqPrice float64) {
	// 强平价格 (100% 风险率)
	liqPrice = CalculateLiquidationPrice(qty, entryPrice, balance, mmr)

	// 预警价格 (70% 风险率) - 比强平价格"远"一些
	// 简化计算：用线性插值，真实场景可能需要反推公式
	if qty > 0 {
		// 多仓：预警价格高于强平价格
		buffer := (entryPrice - liqPrice) * (1 - WarningThreshold)
		warnPrice = liqPrice + buffer
		dangerBuffer := (entryPrice - liqPrice) * (1 - DangerThreshold)
		dangerPrice = liqPrice + dangerBuffer
	} else {
		// 空仓：预警价格低于强平价格
		buffer := (liqPrice - entryPrice) * (1 - WarningThreshold)
		warnPrice = liqPrice - buffer
		dangerBuffer := (liqPrice - entryPrice) * (1 - DangerThreshold)
		dangerPrice = liqPrice - dangerBuffer
	}

	return warnPrice, dangerPrice, liqPrice
}
