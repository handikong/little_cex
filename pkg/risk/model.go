package risk

import "time"

type InstrumentType string

// InstrumentType 表示“仓位类型”。
// 我们未来要支持：
// - spot   现货仓位
// - perp   永续合约仓位
// - option 期权仓位
//

const (
	InstrumentSpot   InstrumentType = "spot"
	InstrumentPerp   InstrumentType = "perp"
	InstrumentOption InstrumentType = "option"
)

// Position 表示一条仓位（你在交易所持有的头寸）。
//
// 在交易所系统中，仓位通常是“状态”：由成交（fills）不断更新形成。
// 例如：你多次开仓 BTC 永续，系统会维护一个平均开仓价 entry_price。
type Position struct {
	// instrument：仓位类型（spot/perp/option）
	Instrument InstrumentType `json:"instrument"`

	// symbol：交易对或合约标识。
	// 这里我们用 BTC_USDT 这种形式。后续也可以扩展成包含 exchange/chain 的全局唯一 ID。
	Symbol string `json:"symbol"`

	// qty：数量（正负表示方向）
	// - qty > 0 代表多仓（long）
	// - qty < 0 代表空仓（short）
	//
	// 注意：不同交易所有不同的“合约张数/面值”定义，
	Qty float64 `json:"qty"`

	// entry_price：平均开仓价（仓位成本）。
	// 来源：撮合产生的成交明细（fills）做加权平均得到。
	EntryPrice float64 `json:"entry_price"`

	// MaintenanceMarginRate: 维持保证金率
	// 交易所通常是一个阶梯表（仓位越大，MMR越高），这里简化为直接输入。
	// 例如：0.005 表示 0.5% 的维持保证金率。
	MaintenanceMarginRate float64 `json:"maint_margin_rate"`
}

// PriceSnapshot 表示一个 symbol 的价格快照。
// Day1 我们先只保留一个 Price 字段，让数据输入简单。
// 后面会扩展为：IndexPrice / MarkPrice / LastPrice。
// - Index：现货指数价（防操纵）
// - Mark：标记价（用于强平/权益计算）
// - Last：最新成交价（更贴交易但可能被插针）
type PriceSnapshot struct {
	// price：当前价格（Day1 占位用）
	Price float64 `json:"price"`

	//  MarkPrice: 标记价格，用于计算 uPnL 和 强平。
	MarkPrice float64 `json:"mark_price"`

	//  FundingRate: 资金费率 (例如 0.0001 代表 0.01%)
	// 用于计算预期资金费支出（Day4 暂时只做展示，Day5 加入现金流计算）。
	FundingRate float64 `json:"funding_rate"`

	// ts：时间戳（可选）
	// 实盘里，价格时效性非常关键。Day1 先不校验时效，只保留字段以便未来扩展。
	Ts time.Time `json:"ts,omitempty"`
}

// Account 表示账户整体状态（我们先用最少字段）。
//
// 在交易所里，账户权益 Equity 通常包含：
// 余额 + 未实现盈亏(uPnL) - 手续费等 + 其他抵扣项。
// Day1 我们把它当作外部输入（你从交易所接口拿到），先不在本地复算。
type Account struct {
	// equity：账户权益（简化理解：你“净值”有多少）
	// Balance: 静态余额 (钱袋子里确定的钱)
	// Day1 的 Equity 其实是 Balance，Day4 我们区分开：
	// Equity(动态) = Balance(静态) + uPnL(浮动)
	Balance float64 `json:"balance"`

	// init_margin_rate：初始保证金率（Day1 占位参数）
	// 举例：0.1 表示名义价值的 10% 作为初始保证金需求。
	//
	// 真实交易所：不同产品、不同杠杆档位、不同风险限额，会有不同 IMR/MMR。
	// 我们 Day5 会引入组合保证金思想，逐步替换掉这个“固定比例”。
	InitMarginRate float64 `json:"init_margin_rate"`
}

// RiskInput 是“风险引擎”的统一输入。
// 风险引擎本质就是：输入（账户+仓位+价格+规则参数）→输出（保证金、风险率、预警）。
type RiskInput struct {
	Account Account `json:"account"`

	// positions：仓位列表
	Positions []Position `json:"positions"`

	// prices：symbol → 价格快照
	// 我们用 map 是为了查找方便：给一个仓位 symbol，能 O(1) 找到价格。
	Prices map[string]PriceSnapshot `json:"prices"`
}

// RiskOutput 是“风险引擎”的统一输出。
// Day1 我们先输出最核心的三项：
// 1) notional：名义价值（风险规模）
// 2) init_margin_req：初始保证金需求（占位）
// 3) risk_ratio：风险率（占位定义：equity / init_margin_req）
//
// 后面会扩展：维持保证金、爆仓价、uPnL、情景最坏损失等。
type RiskOutput struct {
	// notional：所有仓位名义价值总和
	Notional float64 `json:"notional"`

	// TotalUPnL: 总未实现盈亏
	TotalUPnL float64 `json:"total_upnl"`

	// Equity: 动态权益 = Balance + TotalUPnL [Day4 修正定义]
	Equity float64 `json:"equity"`
	
	// MaintMarginReq: 维持保证金需求 (低于这个线爆仓)
	MaintMarginReq float64 `json:"maint_margin_req"`

	// init_margin_req：初始保证金需求
	InitMarginReq float64 `json:"init_margin_req"`

	// RiskRatio: 风险率 = MaintMarginReq / Equity
	// 注意：不同交易所定义不同，有的用 Margin / Equity，有的倒过来。
	// 这里我们定义：占用率。越高越危险，> 100% 爆仓。
	RiskRatio float64 `json:"risk_ratio"`

	// warnings：提示信息（比如某些 instrument 还在占位计算）
	Warnings []string `json:"warnings,omitempty"`
}
