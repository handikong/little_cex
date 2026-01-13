// 文件: pkg/futures/position.go
// 合约持仓数据结构
//
// 【存储策略】
// - 主存储: MySQL (持久化)
// - 缓存: Redis (查询加速)
// - 强平引擎: 通过事件通知更新内存索引

package futures

import "time"

// =============================================================================
// 持仓方向
// =============================================================================

type Side int8

const (
	SideLong  Side = 1  // 多头
	SideShort Side = -1 // 空头
)

func (s Side) String() string {
	if s == SideLong {
		return "LONG"
	}
	return "SHORT"
}

// =============================================================================
// Position - 用户持仓
// =============================================================================

// Position 用户在某合约上的持仓
//
// 【关键概念区分】
// - 未实现盈亏 (uPnL): 随价格实时变化，用方法 UnrealizedPnL() 计算，不存DB
// - 已实现盈亏 (RealizedPnL): 只有平仓/减仓时才产生，存DB累计
type Position struct {
	ID     uint   `gorm:"primaryKey;autoIncrement"`
	UserID int64  `gorm:"column:user_id;index"`
	Symbol string `gorm:"column:symbol;type:varchar(32);index"`

	// ===== 持仓状态 =====
	// Size > 0: 多头持仓
	// Size < 0: 空头持仓
	// Size = 0: 已平仓
	Size       int64 `gorm:"column:size"`
	EntryPrice int64 `gorm:"column:entry_price"` // 开仓均价
	Margin     int64 `gorm:"column:margin"`      // 占用保证金
	Leverage   int   `gorm:"column:leverage"`    // 杠杆倍数

	// ===== 已实现盈亏 =====
	// 【注意】只有平仓/减仓时才更新此字段
	// 例: 开仓1BTC@50000, 平仓0.5BTC@52000
	//     → RealizedPnL += (52000-50000)*0.5 = 1000 USDT
	// 未实现盈亏 (uPnL) 不存这里，实时用 UnrealizedPnL(markPrice) 计算
	RealizedPnL int64 `gorm:"column:realized_pnl"`

	CreatedAt int64 `gorm:"column:created_at"`
	UpdatedAt int64 `gorm:"column:updated_at"`
}

func (Position) TableName() string {
	return "positions"
}

// Side 获取方向
func (p *Position) Side() Side {
	if p.Size > 0 {
		return SideLong
	}
	return SideShort
}

// AbsSize 绝对值
func (p *Position) AbsSize() int64 {
	if p.Size < 0 {
		return -p.Size
	}
	return p.Size
}

// IsEmpty 是否无持仓
func (p *Position) IsEmpty() bool {
	return p.Size == 0
}

// UnrealizedPnL 未实现盈亏
func (p *Position) UnrealizedPnL(markPrice int64) int64 {
	return (markPrice - p.EntryPrice) * p.Size / Precision
}

// PositionValue 仓位价值
func (p *Position) PositionValue(markPrice int64) int64 {
	return p.AbsSize() * markPrice / Precision
}

// =============================================================================
// 持仓变更事件 (通知强平引擎)
// =============================================================================

type PositionChangeType int8

const (
	PositionOpen   PositionChangeType = iota // 新开仓
	PositionAdd                              // 加仓
	PositionReduce                           // 减仓
	PositionClose                            // 平仓
)

func (t PositionChangeType) String() string {
	switch t {
	case PositionOpen:
		return "OPEN"
	case PositionAdd:
		return "ADD"
	case PositionReduce:
		return "REDUCE"
	case PositionClose:
		return "CLOSE"
	}
	return "UNKNOWN"
}

// PositionChangedEvent 持仓变更事件
// 用于通知强平引擎更新内存索引
type PositionChangedEvent struct {
	UserID     int64
	Symbol     string
	ChangeType PositionChangeType

	// 变更后状态
	Size       int64
	EntryPrice int64
	Margin     int64
	Leverage   int

	Timestamp int64
}

// NewPositionChangedEvent 从 Position 创建事件
func NewPositionChangedEvent(pos *Position, changeType PositionChangeType) *PositionChangedEvent {
	return &PositionChangedEvent{
		UserID:     pos.UserID,
		Symbol:     pos.Symbol,
		ChangeType: changeType,
		Size:       pos.Size,
		EntryPrice: pos.EntryPrice,
		Margin:     pos.Margin,
		Leverage:   pos.Leverage,
		Timestamp:  time.Now().UnixMilli(),
	}
}
