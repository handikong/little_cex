// 文件: pkg/futures/spec.go
// 合约规格定义
//
// 设计目标:
// 1. 高性能: 字段按内存对齐排列，减少 padding
// 2. 零分配: 规格创建后不可变，安全共享
// 3. 面试点: 为什么用 int64 而不是 float64?
//    → 浮点数有精度问题，金融系统必须用定点数

package futures

// =============================================================================
// 精度常量
// =============================================================================

const (
	// Precision 价格/数量精度因子
	// 所有金额存储为 int64，乘以 10^8
	// 例: 1.5 BTC = 150_000_000
	Precision = 100_000_000

	// RatePrecision 费率精度 (万分比)
	// 例: 0.01% = 1, 0.1% = 10, 1% = 100
	RatePrecision = 10000
)

// =============================================================================
// 合约状态
// =============================================================================

// ContractStatus 合约状态
type ContractStatus int8

const (
	StatusPending  ContractStatus = iota // 待上线
	StatusTrading                        // 交易中
	StatusSettling                       // 结算中 (交割合约)
	StatusSettled                        // 已结算
	StatusDelisted                       // 已下架
)

func (s ContractStatus) String() string {
	switch s {
	case StatusPending:
		return "PENDING"
	case StatusTrading:
		return "TRADING"
	case StatusSettling:
		return "SETTLING"
	case StatusSettled:
		return "SETTLED"
	case StatusDelisted:
		return "DELISTED"
	default:
		return "UNKNOWN"
	}
}

// =============================================================================
// 合约类型
// =============================================================================

// ContractType 合约类型
type ContractType int8

const (
	TypePerpetual ContractType = iota // 永续合约
	TypeDelivery                      // 交割合约
)

func (t ContractType) String() string {
	if t == TypePerpetual {
		return "PERPETUAL"
	}
	return "DELIVERY"
}

// =============================================================================
// ContractSpec - 合约规格 (核心结构)
// =============================================================================

// ContractSpec 合约规格
//
// 【面试核心】这是合约交易的基础配置，决定了:
// - 用户能用多少杠杆
// - 什么时候触发强平
// - 资金费率如何计算
//
// 设计说明:
// 1. 字段按 8 字节对齐排列，减少内存 padding
// 2. 只读结构，创建后不可变
// 3. 使用 int64 存储金额，避免浮点精度问题
type ContractSpec struct {
	// ===== 主键 =====
	ID uint `gorm:"primaryKey;autoIncrement"`

	// ===== 标识 =====
	Symbol         string `gorm:"column:symbol;type:varchar(32);uniqueIndex"`
	BaseCurrency   string `gorm:"column:base_currency;type:varchar(16)"`
	QuoteCurrency  string `gorm:"column:quote_currency;type:varchar(16)"`
	SettleCurrency string `gorm:"column:settle_currency;type:varchar(16)"`

	// ===== 合约参数 =====
	ContractType   ContractType `gorm:"column:contract_type"`
	ContractSize   int64        `gorm:"column:contract_size"`
	TickSize       int64        `gorm:"column:tick_size"`
	MinOrderQty    int64        `gorm:"column:min_order_qty"`
	MaxOrderQty    int64        `gorm:"column:max_order_qty"`
	MaxPositionQty int64        `gorm:"column:max_position_qty"`

	// ===== 杠杆与保证金 =====
	MaxLeverage       int   `gorm:"column:max_leverage"`
	InitialMarginRate int64 `gorm:"column:initial_margin_rate"`
	MaintMarginRate   int64 `gorm:"column:maint_margin_rate"`

	// ===== 资金费率 (仅永续) =====
	FundingInterval int64 `gorm:"column:funding_interval"`
	MaxFundingRate  int64 `gorm:"column:max_funding_rate"`

	// ===== 指数价格 =====
	PriceSources []string `gorm:"column:price_sources;serializer:json"`

	// ===== 生命周期 =====
	Status    ContractStatus `gorm:"column:status;index"`
	ListedAt  int64          `gorm:"column:listed_at"`
	ExpiryAt  int64          `gorm:"column:expiry_at;index"`
	CreatedAt int64          `gorm:"column:created_at"`
	UpdatedAt int64          `gorm:"column:updated_at"`
}

// =============================================================================
// 便捷方法
// =============================================================================

// IsPerpetual 是否为永续合约
func (s *ContractSpec) IsPerpetual() bool {
	return s.ContractType == TypePerpetual
}

// IsTrading 是否可交易
func (s *ContractSpec) IsTrading() bool {
	return s.Status == StatusTrading
}

// IsExpired 是否已到期 (交割合约)
func (s *ContractSpec) IsExpired(now int64) bool {
	return s.ExpiryAt > 0 && now >= s.ExpiryAt
}

// CalcInitialMargin 计算开仓初始保证金
//
// 公式: 初始保证金 = 仓位价值 × 初始保证金率
// 或:   初始保证金 = 仓位价值 / 杠杆
//
// 参数:
//   - positionValue: 仓位价值 (价格 × 数量)
//   - leverage: 用户选择的杠杆
func (s *ContractSpec) CalcInitialMargin(positionValue int64, leverage int) int64 {
	if leverage <= 0 || leverage > s.MaxLeverage {
		leverage = s.MaxLeverage
	}
	// 初始保证金 = 仓位价值 / 杠杆
	return positionValue / int64(leverage)
}

// CalcMaintMargin 计算维持保证金
//
// 公式: 维持保证金 = 仓位价值 × 维持保证金率
//
// 【面试】维持保证金 < 初始保证金
// 当账户权益 < 维持保证金时触发强平
func (s *ContractSpec) CalcMaintMargin(positionValue int64) int64 {
	return positionValue * s.MaintMarginRate / RatePrecision
}

// ValidatePrice 验证价格是否符合 TickSize
func (s *ContractSpec) ValidatePrice(price int64) bool {
	return price > 0 && price%s.TickSize == 0
}

// ValidateQty 验证数量是否合法
func (s *ContractSpec) ValidateQty(qty int64) bool {
	return qty >= s.MinOrderQty && qty <= s.MaxOrderQty
}
