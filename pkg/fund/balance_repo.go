// 文件: pkg/fund/balance_repo.go
// 冷资产模块 - 余额仓库 (GORM 实现)
//
// 使用 GORM 简化数据库操作:
// - 自动分片路由
// - 链式查询
// - 事务管理

package fund

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// =============================================================================
// BalanceRepo - 余额仓库
// =============================================================================

// BalanceRepo 余额仓库
type BalanceRepo struct {
	db             *gorm.DB
	useSingleTable bool // 开发模式用单表 balances，生产用分片表 balance_XXX
}

// NewBalanceRepo 创建余额仓库 (默认分片模式)
func NewBalanceRepo(db *gorm.DB) *BalanceRepo {
	return &BalanceRepo{db: db, useSingleTable: false}
}

// NewSingleTableBalanceRepo 创建单表余额仓库 (开发测试用)
func NewSingleTableBalanceRepo(db *gorm.DB) *BalanceRepo {
	return &BalanceRepo{db: db, useSingleTable: true}
}

// =============================================================================
// 分片表操作
// =============================================================================

// shardTable 获取分片表的 GORM Scope
func (r *BalanceRepo) balanceTable(userID int64) *gorm.DB {
	if r.useSingleTable {
		return r.db.Table("balances")
	}
	table := GetTableName("balance", userID)
	return r.db.Table(table)
}

func (r *BalanceRepo) journalTable(userID int64) *gorm.DB {
	if r.useSingleTable {
		return r.db.Table("journals")
	}
	table := GetTableName("journal", userID)
	return r.db.Table(table)
}

// =============================================================================
// 余额操作
// =============================================================================

// GetBalance 获取用户余额
func (r *BalanceRepo) GetBalance(ctx context.Context, userID int64, symbol string) (*BalanceRecord, error) {
	var record BalanceRecord
	err := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND symbol = ?", userID, symbol).
		First(&record).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetBalances 获取用户所有币种余额
func (r *BalanceRepo) GetBalances(ctx context.Context, userID int64) ([]*BalanceRecord, error) {
	var records []*BalanceRecord
	err := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ?", userID).
		Find(&records).Error

	return records, err
}

// UpsertBalance 更新或插入余额
func (r *BalanceRepo) UpsertBalance(ctx context.Context, snapshot *BalanceSnapshot) error {
	record := &BalanceRecord{
		UserID:    snapshot.UserID,
		Symbol:    snapshot.Symbol,
		Available: snapshot.Available,
		Locked:    snapshot.Locked,
		UpdatedAt: snapshot.UpdatedAt,
	}

	// 使用 GORM 的 Upsert 功能
	return r.balanceTable(snapshot.UserID).
		WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "symbol"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"available":  snapshot.Available,
				"locked":     snapshot.Locked,
				"version":    gorm.Expr("version + 1"),
				"updated_at": snapshot.UpdatedAt,
			}),
		}).
		Create(record).Error
}

// UpdateBalanceWithVersion 带版本号更新 (乐观锁)
func (r *BalanceRepo) UpdateBalanceWithVersion(
	ctx context.Context,
	userID int64,
	symbol string,
	available, locked int64,
	expectedVersion int,
) (bool, error) {
	result := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND symbol = ? AND version = ?", userID, symbol, expectedVersion).
		Updates(map[string]interface{}{
			"available":  available,
			"locked":     locked,
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		})

	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// FreezeBalance 冻结余额 (下单时调用)
// available -= amount, locked += amount
func (r *BalanceRepo) FreezeBalance(ctx context.Context, userID int64, symbol string, amount int64) error {
	result := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND symbol = ? AND available >= ?", userID, symbol, amount).
		Updates(map[string]interface{}{
			"available":  gorm.Expr("available - ?", amount),
			"locked":     gorm.Expr("locked + ?", amount),
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound // 余额不足或记录不存在
	}
	return nil
}

// UnfreezeBalance 解冻余额 (撤单时调用)
// available += amount, locked -= amount
func (r *BalanceRepo) UnfreezeBalance(ctx context.Context, userID int64, symbol string, amount int64) error {
	result := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND symbol = ? AND locked >= ?", userID, symbol, amount).
		Updates(map[string]interface{}{
			"available":  gorm.Expr("available + ?", amount),
			"locked":     gorm.Expr("locked - ?", amount),
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeductLocked 扣除冻结余额 (成交时调用)
// locked -= amount
func (r *BalanceRepo) DeductLocked(ctx context.Context, userID int64, symbol string, amount int64) error {
	result := r.balanceTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND symbol = ? AND locked >= ?", userID, symbol, amount).
		Updates(map[string]interface{}{
			"locked":     gorm.Expr("locked - ?", amount),
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		})

	if result.Error != nil {
		return result.Error
	}
	return nil
}

// AddAvailable 增加可用余额 (成交收款时调用)
func (r *BalanceRepo) AddAvailable(ctx context.Context, userID int64, symbol string, amount int64) error {
	// 如果记录不存在则创建
	record := &BalanceRecord{
		UserID:    userID,
		Symbol:    symbol,
		Available: amount,
		Locked:    0,
		UpdatedAt: time.Now(),
	}

	return r.balanceTable(userID).
		WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "symbol"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"available":  gorm.Expr("available + ?", amount),
				"version":    gorm.Expr("version + 1"),
				"updated_at": time.Now(),
			}),
		}).
		Create(record).Error
}

// =============================================================================
// 流水操作
// =============================================================================

// InsertJournal 插入流水 (幂等)
func (r *BalanceRepo) InsertJournal(ctx context.Context, event *JournalEvent) error {
	record := &JournalRecord{
		EventID:         event.EventID,
		UserID:          event.UserID,
		Symbol:          event.Symbol,
		ChangeType:      event.ChangeType,
		Amount:          event.Amount,
		AvailableBefore: event.AvailableBefore,
		AvailableAfter:  event.AvailableAfter,
		LockedBefore:    event.LockedBefore,
		LockedAfter:     event.LockedAfter,
		BizType:         event.BizType,
		BizID:           event.BizID,
		CreatedAt:       event.CreatedAt,
	}

	// INSERT IGNORE 效果
	return r.journalTable(event.UserID).
		WithContext(ctx).
		Clauses(clause.Insert{Modifier: "IGNORE"}).
		Create(record).Error
}

// GetJournalByEventID 根据 EventID 查询流水
func (r *BalanceRepo) GetJournalByEventID(ctx context.Context, userID int64, eventID string) (*JournalRecord, error) {
	var record JournalRecord
	err := r.journalTable(userID).
		WithContext(ctx).
		Where("event_id = ?", eventID).
		First(&record).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ListJournals 查询用户流水列表
func (r *BalanceRepo) ListJournals(
	ctx context.Context,
	userID int64,
	symbol string,
	limit, offset int,
) ([]*JournalRecord, error) {
	query := r.journalTable(userID).
		WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset)

	if symbol != "" {
		query = query.Where("symbol = ?", symbol)
	}

	var records []*JournalRecord
	err := query.Find(&records).Error
	return records, err
}

// ListJournalsByBiz 按业务查询流水
func (r *BalanceRepo) ListJournalsByBiz(
	ctx context.Context,
	userID int64,
	bizType BizType,
	bizID string,
) ([]*JournalRecord, error) {
	var records []*JournalRecord
	err := r.journalTable(userID).
		WithContext(ctx).
		Where("user_id = ? AND biz_type = ? AND biz_id = ?", userID, bizType, bizID).
		Order("created_at ASC").
		Find(&records).Error

	return records, err
}

// =============================================================================
// 批量操作
// =============================================================================

// BatchInsertJournals 批量插入流水
func (r *BalanceRepo) BatchInsertJournals(ctx context.Context, events []*JournalEvent) error {
	if len(events) == 0 {
		return nil
	}

	// 按分片分组
	shardEvents := make(map[int][]*JournalEvent)
	for _, e := range events {
		shard := e.GetShard()
		shardEvents[shard] = append(shardEvents[shard], e)
	}

	// 每个分片一个批量插入
	for shard, shardEvts := range shardEvents {
		if err := r.batchInsertToShard(ctx, shard, shardEvts); err != nil {
			return err
		}
	}

	return nil
}

func (r *BalanceRepo) batchInsertToShard(ctx context.Context, shard int, events []*JournalEvent) error {
	table := "journal_" + shardSuffix(shard)

	records := make([]*JournalRecord, 0, len(events))
	for _, e := range events {
		records = append(records, &JournalRecord{
			EventID:         e.EventID,
			UserID:          e.UserID,
			Symbol:          e.Symbol,
			ChangeType:      e.ChangeType,
			Amount:          e.Amount,
			AvailableBefore: e.AvailableBefore,
			AvailableAfter:  e.AvailableAfter,
			LockedBefore:    e.LockedBefore,
			LockedAfter:     e.LockedAfter,
			BizType:         e.BizType,
			BizID:           e.BizID,
			CreatedAt:       e.CreatedAt,
		})
	}

	return r.db.Table(table).
		WithContext(ctx).
		Clauses(clause.Insert{Modifier: "IGNORE"}).
		CreateInBatches(records, 100). // 每批 100 条
		Error
}

// =============================================================================
// 事务支持
// =============================================================================

// Transaction 执行事务
func (r *BalanceRepo) Transaction(ctx context.Context, fn func(tx *BalanceRepo) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &BalanceRepo{db: tx}
		return fn(txRepo)
	})
}

// SaveBalanceAndJournal 事务中同时保存余额和流水
func (r *BalanceRepo) SaveBalanceAndJournal(
	ctx context.Context,
	snapshot *BalanceSnapshot,
	event *JournalEvent,
) error {
	return r.Transaction(ctx, func(tx *BalanceRepo) error {
		if err := tx.InsertJournal(ctx, event); err != nil {
			return err
		}
		return tx.UpsertBalance(ctx, snapshot)
	})
}
