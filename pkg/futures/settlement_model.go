// 文件: pkg/futures/settlement_model.go
// 交割相关数据结构

package futures

import "time"

// =============================================================================
// 交割记录
// =============================================================================

// SettlementRecord 交割记录 (存储到 MySQL)
//
// 每次交割产生一条主记录记录
type SettlementRecord struct {
	ID              uint   `gorm:"primaryKey;autoIncrement"`
	Symbol          string `gorm:"column:symbol;type:varchar(32);index"`
	SettlementPrice int64  `gorm:"column:settlement_price"` // 结算价
	TotalPositions  int    `gorm:"column:total_positions"`  // 结算的持仓数
	TotalPnL        int64  `gorm:"column:total_pnl"`        // 总盈亏
	Status          string `gorm:"column:status"`           // SUCCESS / FAILED
	StartedAt       int64  `gorm:"column:started_at"`
	FinishedAt      int64  `gorm:"column:finished_at"`
	ErrorMsg        string `gorm:"column:error_msg;type:text"`
}

func (SettlementRecord) TableName() string {
	return "settlement_records"
}

// =============================================================================
// 用户交割明细
// =============================================================================

// SettlementDetail 用户交割明细
//
// 每个用户的每个持仓产生一条明细
type SettlementDetail struct {
	ID               uint   `gorm:"primaryKey;autoIncrement"`
	SettlementID     uint   `gorm:"column:settlement_id;index"` // 关联主记录
	UserID           int64  `gorm:"column:user_id;index"`
	Symbol           string `gorm:"column:symbol;type:varchar(32)"`
	Side             Side   `gorm:"column:side"`              // 持仓方向
	Size             int64  `gorm:"column:size"`              // 持仓数量
	EntryPrice       int64  `gorm:"column:entry_price"`       // 开仓均价
	SettlementPrice  int64  `gorm:"column:settlement_price"`  // 结算价
	Margin           int64  `gorm:"column:margin"`            // 占用保证金
	PnL              int64  `gorm:"column:pnl"`               // 盈亏
	SettlementAmount int64  `gorm:"column:settlement_amount"` // 结算金额 (返还给用户)
	CreatedAt        int64  `gorm:"column:created_at"`
}

func (SettlementDetail) TableName() string {
	return "settlement_details"
}

// =============================================================================
// 交割事件 (发送到 NATS/Kafka)
// =============================================================================

// SettlementEvent 交割事件
type SettlementEvent struct {
	EventType       string `json:"event_type"` // SETTLEMENT_START / SETTLEMENT_COMPLETE
	Symbol          string `json:"symbol"`
	SettlementPrice int64  `json:"settlement_price,omitempty"`
	TotalPositions  int    `json:"total_positions,omitempty"`
	TotalPnL        int64  `json:"total_pnl,omitempty"`
	Timestamp       int64  `json:"timestamp"`
}

// UserSettlementEvent 用户交割通知事件
type UserSettlementEvent struct {
	EventType        string `json:"event_type"` // USER_SETTLEMENT
	UserID           int64  `json:"user_id"`
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Size             int64  `json:"size"`
	EntryPrice       int64  `json:"entry_price"`
	SettlementPrice  int64  `json:"settlement_price"`
	PnL              int64  `json:"pnl"`
	SettlementAmount int64  `json:"settlement_amount"`
	Timestamp        int64  `json:"timestamp"`
}

// =============================================================================
// 便捷构造函数
// =============================================================================

func NewSettlementEvent(eventType, symbol string, price int64) *SettlementEvent {
	return &SettlementEvent{
		EventType:       eventType,
		Symbol:          symbol,
		SettlementPrice: price,
		Timestamp:       time.Now().UnixMilli(),
	}
}

func NewUserSettlementEvent(pos *Position, settlementPrice, pnl, amount int64) *UserSettlementEvent {
	side := "LONG"
	if pos.Size < 0 {
		side = "SHORT"
	}
	return &UserSettlementEvent{
		EventType:        "USER_SETTLEMENT",
		UserID:           pos.UserID,
		Symbol:           pos.Symbol,
		Side:             side,
		Size:             pos.AbsSize(),
		EntryPrice:       pos.EntryPrice,
		SettlementPrice:  settlementPrice,
		PnL:              pnl,
		SettlementAmount: amount,
		Timestamp:        time.Now().UnixMilli(),
	}
}
