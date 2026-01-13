// 文件: pkg/futures/mysql_repo.go
// 合约规格 MySQL 存储实现
//
// 【设计】
// - 使用 GORM 作为 ORM
// - ContractSpec 需要实现 GORM 的 TableName() 方法
// - 所有操作带 context 支持超时控制

package futures

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// 确保实现了接口
var _ ContractRepository = (*MySQLContractRepository)(nil)

// MySQLContractRepository MySQL 实现
type MySQLContractRepository struct {
	db *gorm.DB
}

// NewMySQLContractRepository 创建 MySQL 存储
func NewMySQLContractRepository(db *gorm.DB) *MySQLContractRepository {
	return &MySQLContractRepository{db: db}
}

// TableName GORM 表名
func (ContractSpec) TableName() string {
	return "contract_specs"
}

// =============================================================================
// 接口实现
// =============================================================================

// Create 创建合约
func (r *MySQLContractRepository) Create(ctx context.Context, spec *ContractSpec) error {
	now := time.Now().UnixMilli()
	spec.CreatedAt = now
	spec.UpdatedAt = now

	err := r.db.WithContext(ctx).Create(spec).Error
	if err != nil {
		if isDuplicateKeyError(err) {
			return ErrSymbolExists
		}
		return err
	}
	return nil
}

// GetBySymbol 根据 symbol 查询
func (r *MySQLContractRepository) GetBySymbol(ctx context.Context, symbol string) (*ContractSpec, error) {
	var spec ContractSpec
	err := r.db.WithContext(ctx).
		Where("symbol = ?", symbol).
		First(&spec).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSymbolNotFound
		}
		return nil, err
	}
	return &spec, nil
}

// Update 更新合约
func (r *MySQLContractRepository) Update(ctx context.Context, spec *ContractSpec) error {
	spec.UpdatedAt = time.Now().UnixMilli()

	result := r.db.WithContext(ctx).
		Model(&ContractSpec{}).
		Where("symbol = ?", spec.Symbol).
		Updates(spec)

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSymbolNotFound
	}
	return nil
}

// UpdateStatus 更新状态
func (r *MySQLContractRepository) UpdateStatus(ctx context.Context, symbol string, from, to ContractStatus) error {
	now := time.Now().UnixMilli()

	updates := map[string]interface{}{
		"status":     to,
		"updated_at": now,
	}
	// 首次上线时记录上线时间
	if to == StatusTrading {
		updates["listed_at"] = gorm.Expr("CASE WHEN listed_at = 0 THEN ? ELSE listed_at END", now)
	}

	result := r.db.WithContext(ctx).
		Model(&ContractSpec{}).
		Where("symbol = ? AND status = ?", symbol, from).
		Updates(updates)

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSymbolNotFound
	}
	return nil
}

// List 列出所有合约
func (r *MySQLContractRepository) List(ctx context.Context) ([]*ContractSpec, error) {
	var specs []*ContractSpec
	err := r.db.WithContext(ctx).
		Where("status != ?", StatusDelisted).
		Find(&specs).Error
	return specs, err
}

// ListByStatus 按状态查询
func (r *MySQLContractRepository) ListByStatus(ctx context.Context, status ContractStatus) ([]*ContractSpec, error) {
	var specs []*ContractSpec
	err := r.db.WithContext(ctx).
		Where("status = ?", status).
		Find(&specs).Error
	return specs, err
}

// Delete 软删除
func (r *MySQLContractRepository) Delete(ctx context.Context, symbol string) error {
	return r.UpdateStatus(ctx, symbol, StatusTrading, StatusDelisted)
}

// =============================================================================
// 辅助函数
// =============================================================================

// isDuplicateKeyError 判断是否为重复键错误
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// MySQL error code 1062 = Duplicate entry
	// 可以用 mysql.MySQLError 精确判断
	errStr := err.Error()
	return contains(errStr, "Duplicate entry") || contains(errStr, "1062")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsRune(s, substr))
}

func containsRune(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
