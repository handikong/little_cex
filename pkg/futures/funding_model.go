// 文件: pkg/futures/funding_model.go
// 资金费率相关数据结构

package futures

import "time"

// =============================================================================
// 资金费支付记录
// =============================================================================

// FundingPayment 资金费支付记录
type FundingPayment struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	UserID       int64  `gorm:"column:user_id;index"`
	Symbol       string `gorm:"column:symbol;type:varchar(32);index"`
	PositionSize int64  `gorm:"column:position_size"`      // 结算时的持仓量
	MarkPrice    int64  `gorm:"column:mark_price"`         // 结算价格
	FundingRate  int64  `gorm:"column:funding_rate"`       // 资金费率 (万分比)
	Payment      int64  `gorm:"column:payment"`            // 资金费 (正=收入, 负=支出)
	FundingTime  int64  `gorm:"column:funding_time;index"` // 结算时间点
	CreatedAt    int64  `gorm:"column:created_at"`
}

func (FundingPayment) TableName() string {
	return "funding_payments"
}

// =============================================================================
// 资金费率历史记录
// =============================================================================

// FundingRateHistory 资金费率历史
type FundingRateHistory struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	Symbol      string `gorm:"column:symbol;type:varchar(32);index"`
	FundingRate int64  `gorm:"column:funding_rate"` // 万分比
	MarkPrice   int64  `gorm:"column:mark_price"`
	IndexPrice  int64  `gorm:"column:index_price"`
	FundingTime int64  `gorm:"column:funding_time;uniqueIndex:idx_symbol_time"`
	CreatedAt   int64  `gorm:"column:created_at"`
}

func (FundingRateHistory) TableName() string {
	return "funding_rate_history"
}

// =============================================================================
// 便捷构造
// =============================================================================

func NewFundingPayment(
	userID int64,
	symbol string,
	positionSize, markPrice, fundingRate, payment, fundingTime int64,
) *FundingPayment {
	return &FundingPayment{
		UserID:       userID,
		Symbol:       symbol,
		PositionSize: positionSize,
		MarkPrice:    markPrice,
		FundingRate:  fundingRate,
		Payment:      payment,
		FundingTime:  fundingTime,
		CreatedAt:    time.Now().UnixMilli(),
	}
}
