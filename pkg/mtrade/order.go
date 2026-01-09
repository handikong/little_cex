package mtrade

import (
	"fmt"
	"time"
)

// =============================================================================
// 常量定义
// =============================================================================

// 【面试高频】Side 买卖方向
// 问：为什么用 int8 而不是 string？
// 答：内存小、比较快、避免字符串分配
type Side int8

const (
	SideBuy  Side = 1  // 买入
	SideSell Side = -1 // 卖出，用 -1 方便计算对手盘
)

func (s Side) String() string {
	if s == SideBuy {
		return "BUY"
	}
	return "SELL"
}

// Opposite 返回对手方向
// 【面试】撮合时需要快速获取对手盘方向
func (s Side) Opposite() Side {
	return -s // Buy(1) -> Sell(-1), Sell(-1) -> Buy(1)
}

// =============================================================================
// 订单类型
// =============================================================================

// 【面试高频】OrderType 订单类型
// 问：解释 Limit、Market、IOC、FOK 的区别
type OrderType int8

const (
	OrderTypeLimit    OrderType = iota // 限价单：指定价格，可部分成交
	OrderTypeMarket                    // 市价单：吃掉对手盘，无法成交则取消
	OrderTypeIOC                       // Immediate or Cancel：立即成交，剩余取消
	OrderTypeFOK                       // Fill or Kill：全部成交或全部取消
	OrderTypePostOnly                  // 仅做 Maker，如会吃单则拒绝
)

func (t OrderType) String() string {
	switch t {
	case OrderTypeLimit:
		return "LIMIT"
	case OrderTypeMarket:
		return "MARKET"
	case OrderTypeIOC:
		return "IOC"
	case OrderTypeFOK:
		return "FOK"
	case OrderTypePostOnly:
		return "POST_ONLY"
	default:
		return "UNKNOWN"
	}
}

// =============================================================================
// 订单状态
// =============================================================================

// 【面试】订单状态机：New → PartiallyFilled → Filled/Canceled
type OrderStatus int8

const (
	OrderStatusNew             OrderStatus = iota // 新订单，等待撮合
	OrderStatusPartiallyFilled                    // 部分成交
	OrderStatusFilled                             // 完全成交
	OrderStatusCanceled                           // 已取消
	OrderStatusRejected                           // 被拒绝
)

func (s OrderStatus) String() string {
	switch s {
	case OrderStatusNew:
		return "NEW"
	case OrderStatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	case OrderStatusFilled:
		return "FILLED"
	case OrderStatusCanceled:
		return "CANCELED"
	case OrderStatusRejected:
		return "REJECTED"
	default:
		return "UNKNOWN"
	}
}

// =============================================================================
// 订单结构体
// =============================================================================

// 【面试高频】Order 订单结构
// 问：为什么 Price 和 Qty 用 int64 而不是 float64？
// 答：避免浮点精度问题，用定点数表示（如 price * 1e8）
//
// 【性能优化】字段按 8 字节对齐，减少内存填充
type Order struct {
	// ========== 64 位字段放前面（内存对齐）==========

	ID        int64 // 订单 ID（Snowflake 生成）
	UserID    int64 // 用户 ID
	Price     int64 // 价格（定点数，实际价格 = Price / 1e8）
	Qty       int64 // 数量（定点数）
	FilledQty int64 // 已成交数量
	CreatedAt int64 // 创建时间（Unix 纳秒）

	// ========== 小字段放后面 ==========

	Side   Side        // 买卖方向
	Type   OrderType   // 订单类型
	Status OrderStatus // 订单状态

	// Symbol 放最后（string 是 16 字节）
	Symbol string // 交易对，如 "BTC_USDT"
}

// RemainingQty 返回剩余未成交数量
// 【面试】撮合时需要频繁调用
func (o *Order) RemainingQty() int64 {
	return o.Qty - o.FilledQty
}

// IsFilled 是否完全成交
func (o *Order) IsFilled() bool {
	return o.FilledQty >= o.Qty
}

// CanMatch 是否可以与对手订单撮合
// 【面试】价格匹配逻辑
// 买单：买价 >= 卖价 才能成交
// 卖单：卖价 <= 买价 才能成交
func (o *Order) CanMatch(other *Order) bool {
	if o.Side == other.Side {
		return false // 同方向不能撮合
	}

	if o.Side == SideBuy {
		return o.Price >= other.Price // 买价 >= 卖价
	}
	return o.Price <= other.Price // 卖价 <= 买价
}

// String 格式化输出
func (o *Order) String() string {
	return fmt.Sprintf("Order{ID:%d, %s %s %d@%d, Filled:%d, Status:%s}",
		o.ID, o.Side, o.Symbol, o.Qty, o.Price, o.FilledQty, o.Status)
}

// =============================================================================
// 价格转换工具
// =============================================================================

const (
	// PriceMultiplier 价格乘数
	// 【面试】为什么是 1e8？对标比特币最小单位 satoshi
	PriceMultiplier = 1e8
)

// ToFixedPrice 将浮点价格转为定点数
func ToFixedPrice(price float64) int64 {
	return int64(price * PriceMultiplier)
}

// FromFixedPrice 将定点数转为浮点价格
func FromFixedPrice(price int64) float64 {
	return float64(price) / PriceMultiplier
}

// =============================================================================
// 订单 ID 生成器（简化版，生产用 Snowflake）
// =============================================================================

var orderIDSeq int64

// NextOrderID 生成下一个订单 ID
// 【面试】生产环境用 Snowflake ID
// 结构：时间戳(41位) + 机器ID(10位) + 序列号(12位)
func NextOrderID() int64 {
	orderIDSeq++
	// 简化版：时间戳左移 + 序列号
	return time.Now().UnixNano()/1000000<<20 | (orderIDSeq & 0xFFFFF)
}
