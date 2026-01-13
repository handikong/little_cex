// 文件: pkg/order/model.go
// 统一订单模型，支持现货/合约/期权

package order

import "time"

// =============================================================================
// 订单状态
// =============================================================================

type OrderStatus int8

const (
	StatusNew             OrderStatus = iota // 新建
	StatusPartiallyFilled                    // 部分成交
	StatusFilled                             // 完全成交
	StatusCanceled                           // 已撤销
	StatusRejected                           // 已拒绝
)

func (s OrderStatus) String() string {
	switch s {
	case StatusNew:
		return "NEW"
	case StatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	case StatusFilled:
		return "FILLED"
	case StatusCanceled:
		return "CANCELED"
	case StatusRejected:
		return "REJECTED"
	}
	return "UNKNOWN"
}

// =============================================================================
// 产品类型
// =============================================================================

type ProductType string

const (
	ProductSpot    ProductType = "SPOT"
	ProductFutures ProductType = "FUTURES"
	ProductOptions ProductType = "OPTIONS"
)

// =============================================================================
// 订单类型
// =============================================================================

type OrderType int8

const (
	OrderTypeLimit  OrderType = iota + 1 // 限价
	OrderTypeMarket                      // 市价
)

// =============================================================================
// 订单方向
// =============================================================================

type OrderSide int8

const (
	SideBuy  OrderSide = 1
	SideSell OrderSide = 2
)

// =============================================================================
// Order - 统一订单结构
// =============================================================================

type Order struct {
	ID      uint  `gorm:"primaryKey;autoIncrement"`
	OrderID int64 `gorm:"column:order_id;uniqueIndex"` // 雪花ID

	UserID      int64       `gorm:"column:user_id;index"`
	Symbol      string      `gorm:"column:symbol;type:varchar(32)"`
	ProductType ProductType `gorm:"column:product_type;type:varchar(16)"` // SPOT/FUTURES/OPTIONS

	// 订单参数
	Side      OrderSide `gorm:"column:side"`
	OrderType OrderType `gorm:"column:order_type"`
	Price     int64     `gorm:"column:price"`
	Qty       int64     `gorm:"column:qty"`

	// 成交状态
	FilledQty int64       `gorm:"column:filled_qty"`
	AvgPrice  int64       `gorm:"column:avg_price"`
	Status    OrderStatus `gorm:"column:status;index"`

	// 扩展字段 (JSON，不同产品不同)
	// 合约: {"leverage": 10, "margin": 5000}
	// 期权: {"strike": 50000, "expiry": 1234567890}
	Extra string `gorm:"column:extra;type:json"`

	// 时间
	CreatedAt int64 `gorm:"column:created_at;index"`
	UpdatedAt int64 `gorm:"column:updated_at"`
}

func (Order) TableName() string {
	return "orders"
}

// =============================================================================
// 便捷方法
// =============================================================================

func (o *Order) IsActive() bool {
	return o.Status == StatusNew || o.Status == StatusPartiallyFilled
}

func (o *Order) RemainingQty() int64 {
	return o.Qty - o.FilledQty
}

// NewOrder 创建新订单
func NewOrder(orderID, userID int64, symbol string, productType ProductType, side OrderSide, orderType OrderType, price, qty int64) *Order {
	now := time.Now().UnixMilli()
	return &Order{
		OrderID:     orderID,
		UserID:      userID,
		Symbol:      symbol,
		ProductType: productType,
		Side:        side,
		OrderType:   orderType,
		Price:       price,
		Qty:         qty,
		Status:      StatusNew,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}
