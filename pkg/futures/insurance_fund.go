// 文件: pkg/futures/insurance_fund.go
// 保险基金
//
// 【核心作用】
// 当用户穿仓时 (亏损 > 保证金)，由保险基金兜底
// 防止对手方 (盈利方) 无法足额获利
//
// 【资金来源】
// 1. 强平剩余: 强平价格优于破产价格时的差额
// 2. 交易手续费的一部分
// 3. 平台注资
//
// 【面试考点】
// Q: 保险基金不足怎么办？
// A: 触发 ADL (自动减仓)，强制减少盈利用户的仓位

package futures

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrInsufficientInsuranceFund = errors.New("insufficient insurance fund")
)

// =============================================================================
// InsuranceFund - 保险基金
// =============================================================================

// InsuranceFund 保险基金
//
// 【设计】
// 每个结算货币一个保险池，例如:
// - USDT 保险池: 给 USDT 结算的合约兜底
// - BTC 保险池: 给 BTC 结算的合约兜底
type InsuranceFund struct {
	db *gorm.DB

	// 内存缓存 (减少 DB 查询)
	// currency -> balance
	balanceCache sync.Map
}

func NewInsuranceFund(db *gorm.DB) *InsuranceFund {
	fund := &InsuranceFund{db: db}
	fund.loadAll()
	return fund
}

// =============================================================================
// 数据模型
// =============================================================================

// InsuranceFundBalance 保险基金余额
type InsuranceFundBalance struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	Currency  string `gorm:"column:currency;type:varchar(16);uniqueIndex"` // USDT, BTC
	Balance   int64  `gorm:"column:balance"`                               // 当前余额
	UpdatedAt int64  `gorm:"column:updated_at"`
}

func (InsuranceFundBalance) TableName() string {
	return "insurance_fund_balances"
}

// InsuranceFundLog 保险基金流水
type InsuranceFundLog struct {
	ID            uint   `gorm:"primaryKey;autoIncrement"`
	Currency      string `gorm:"column:currency;type:varchar(16);index"`
	ChangeType    string `gorm:"column:change_type"` // DEPOSIT / WITHDRAW / LIQUIDATION_PROFIT / BANKRUPT_COVER
	Amount        int64  `gorm:"column:amount"`      // 正=增加，负=减少
	BalanceAfter  int64  `gorm:"column:balance_after"`
	RelatedUserID int64  `gorm:"column:related_user_id"` // 关联用户 (强平/穿仓时)
	RelatedSymbol string `gorm:"column:related_symbol"`  // 关联合约
	Remark        string `gorm:"column:remark;type:text"`
	CreatedAt     int64  `gorm:"column:created_at;index"`
}

func (InsuranceFundLog) TableName() string {
	return "insurance_fund_logs"
}

// =============================================================================
// 核心操作
// =============================================================================

// GetBalance 获取保险基金余额
func (f *InsuranceFund) GetBalance(currency string) int64 {
	if v, ok := f.balanceCache.Load(currency); ok {
		return v.(int64)
	}
	return 0
}

// AddFunds 增加保险基金
//
// 【调用场景】
// 1. 强平剩余: 强平价格 > 破产价格，差额归保险基金
// 2. 手续费划转: 部分交易手续费归保险基金
// 3. 平台注资
func (f *InsuranceFund) AddFunds(
	ctx context.Context,
	currency string,
	amount int64,
	changeType string,
	userID int64,
	symbol string,
	remark string,
) error {
	if amount <= 0 {
		return errors.New("amount must be positive")
	}

	return f.db.Transaction(func(tx *gorm.DB) error {
		// 1. 查询或创建余额记录
		var balance InsuranceFundBalance
		err := tx.Where("currency = ?", currency).First(&balance).Error
		if err == gorm.ErrRecordNotFound {
			balance = InsuranceFundBalance{
				Currency:  currency,
				Balance:   0,
				UpdatedAt: time.Now().UnixMilli(),
			}
			tx.Create(&balance)
		} else if err != nil {
			return err
		}

		// 2. 增加余额
		newBalance := balance.Balance + amount
		err = tx.Model(&balance).Updates(map[string]any{
			"balance":    newBalance,
			"updated_at": time.Now().UnixMilli(),
		}).Error
		if err != nil {
			return err
		}

		// 3. 记录流水
		logEntry := &InsuranceFundLog{
			Currency:      currency,
			ChangeType:    changeType,
			Amount:        amount,
			BalanceAfter:  newBalance,
			RelatedUserID: userID,
			RelatedSymbol: symbol,
			Remark:        remark,
			CreatedAt:     time.Now().UnixMilli(),
		}
		if err := tx.Create(logEntry).Error; err != nil {
			return err
		}

		// 4. 更新缓存
		f.balanceCache.Store(currency, newBalance)

		log.Printf("[InsuranceFund] Added %d %s, new balance: %d, type: %s",
			amount, currency, newBalance, changeType)

		return nil
	})
}

// CoverBankruptcy 穿仓兜底
//
// 【调用场景】
// 用户亏损超过保证金，需要保险基金补足差额
//
// 【返回值】
// 实际扣除的金额 (可能小于请求金额，如果余额不足)
func (f *InsuranceFund) CoverBankruptcy(
	ctx context.Context,
	currency string,
	amount int64, // 需要兜底的金额 (正数)
	userID int64,
	symbol string,
) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}

	var coveredAmount int64

	err := f.db.Transaction(func(tx *gorm.DB) error {
		// 1. 获取当前余额
		var balance InsuranceFundBalance
		err := tx.Where("currency = ?", currency).First(&balance).Error
		if err != nil {
			return err
		}

		// 2. 计算实际可扣除金额
		coveredAmount = amount
		if coveredAmount > balance.Balance {
			coveredAmount = balance.Balance // 最多扣完
		}

		if coveredAmount <= 0 {
			return ErrInsufficientInsuranceFund
		}

		// 3. 扣除余额
		newBalance := balance.Balance - coveredAmount
		err = tx.Model(&balance).Updates(map[string]any{
			"balance":    newBalance,
			"updated_at": time.Now().UnixMilli(),
		}).Error
		if err != nil {
			return err
		}

		// 4. 记录流水
		logEntry := &InsuranceFundLog{
			Currency:      currency,
			ChangeType:    "BANKRUPT_COVER",
			Amount:        -coveredAmount, // 负数表示减少
			BalanceAfter:  newBalance,
			RelatedUserID: userID,
			RelatedSymbol: symbol,
			Remark:        "Cover user bankruptcy",
			CreatedAt:     time.Now().UnixMilli(),
		}
		if err := tx.Create(logEntry).Error; err != nil {
			return err
		}

		// 5. 更新缓存
		f.balanceCache.Store(currency, newBalance)

		log.Printf("[InsuranceFund] Covered bankruptcy %d %s for user %d, remaining: %d",
			coveredAmount, currency, userID, newBalance)

		return nil
	})

	return coveredAmount, err
}

// NeedsADL 是否需要触发 ADL
//
// 【规则】
// 当保险基金不足以覆盖穿仓损失时，返回 true
func (f *InsuranceFund) NeedsADL(currency string, bankruptAmount int64) bool {
	return f.GetBalance(currency) < bankruptAmount
}

// =============================================================================
// 辅助方法
// =============================================================================

// loadAll 启动时加载所有余额到缓存
func (f *InsuranceFund) loadAll() {
	var balances []InsuranceFundBalance
	f.db.Find(&balances)

	for _, b := range balances {
		f.balanceCache.Store(b.Currency, b.Balance)
	}

	log.Printf("[InsuranceFund] Loaded %d currency balances", len(balances))
}

// GetAllBalances 获取所有余额 (管理接口)
func (f *InsuranceFund) GetAllBalances() map[string]int64 {
	result := make(map[string]int64)
	f.balanceCache.Range(func(key, value any) bool {
		result[key.(string)] = value.(int64)
		return true
	})
	return result
}
