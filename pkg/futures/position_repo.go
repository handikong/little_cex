// 文件: pkg/futures/position_repo.go
// 持仓存储层 (Redis 缓存 + MySQL 持久化)

package futures

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// =============================================================================
// 接口定义
// =============================================================================

type PositionRepository interface {
	// 查询
	GetByUserAndSymbol(ctx context.Context, userID int64, symbol string) (*Position, error)
	GetByUser(ctx context.Context, userID int64) ([]*Position, error)

	// 保存 (写 DB + 更新 Redis)
	Save(ctx context.Context, pos *Position) error

	// 删除
	Delete(ctx context.Context, userID int64, symbol string) error
	ListBySymbol(ctx context.Context, symbol string, limit, offset int) ([]*Position, error)
}

// =============================================================================
// Redis Key
// =============================================================================

const (
	// position:{userID}:{symbol}
	positionKeyPattern = "position:%d:%s"
	// position:list:{userID}
	positionListKeyPattern = "position:list:%d"

	positionCacheTTL = 24 * time.Hour
)

func positionKey(userID int64, symbol string) string {
	return fmt.Sprintf(positionKeyPattern, userID, symbol)
}

func positionListKey(userID int64) string {
	return fmt.Sprintf(positionListKeyPattern, userID)
}

// =============================================================================
// 实现
// =============================================================================

type CachedPositionRepository struct {
	db    *gorm.DB
	redis *redis.Client
}

func NewCachedPositionRepository(db *gorm.DB, rds *redis.Client) *CachedPositionRepository {
	return &CachedPositionRepository{db: db, redis: rds}
}

// GetByUserAndSymbol 获取单个持仓
func (r *CachedPositionRepository) GetByUserAndSymbol(ctx context.Context, userID int64, symbol string) (*Position, error) {
	key := positionKey(userID, symbol)

	// 1. 查 Redis
	data, err := r.redis.Get(ctx, key).Bytes()
	if err == nil {
		var pos Position
		if json.Unmarshal(data, &pos) == nil {
			return &pos, nil
		}
	}

	// 2. 查 DB
	var pos Position
	err = r.db.WithContext(ctx).
		Where("user_id = ? AND symbol = ?", userID, symbol).
		First(&pos).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // 无持仓
		}
		return nil, err
	}

	// 3. 回填 Redis
	go r.cachePosition(context.Background(), &pos)

	return &pos, nil
}

// GetByUser 获取用户所有持仓
func (r *CachedPositionRepository) GetByUser(ctx context.Context, userID int64) ([]*Position, error) {
	var positions []*Position
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND size != 0", userID).
		Find(&positions).Error
	return positions, err
}

// Save 保存持仓 (DB + Redis)
func (r *CachedPositionRepository) Save(ctx context.Context, pos *Position) error {
	pos.UpdatedAt = time.Now().UnixMilli()

	// 1. 写 DB (upsert)
	err := r.db.WithContext(ctx).Save(pos).Error
	if err != nil {
		return err
	}

	// 2. 更新 Redis
	r.cachePosition(ctx, pos)

	// 3. 如果平仓 (size=0)，从缓存删除
	if pos.Size == 0 {
		r.redis.Del(ctx, positionKey(pos.UserID, pos.Symbol))
		r.redis.SRem(ctx, positionListKey(pos.UserID), pos.Symbol)
	} else {
		r.redis.SAdd(ctx, positionListKey(pos.UserID), pos.Symbol)
	}

	return nil
}

// Delete 删除持仓
func (r *CachedPositionRepository) Delete(ctx context.Context, userID int64, symbol string) error {
	// DB
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND symbol = ?", userID, symbol).
		Delete(&Position{}).Error
	if err != nil {
		return err
	}

	// Redis
	r.redis.Del(ctx, positionKey(userID, symbol))
	r.redis.SRem(ctx, positionListKey(userID), symbol)
	return nil
}

func (r *CachedPositionRepository) cachePosition(ctx context.Context, pos *Position) {
	key := positionKey(pos.UserID, pos.Symbol)
	data, _ := json.Marshal(pos)
	r.redis.Set(ctx, key, data, positionCacheTTL)
}

// ListBySymbol 按合约查询所有持仓 (交割用)
//
// 【分页设计】
// 因为一个合约可能有几万个持仓，必须分页查询
func (r *CachedPositionRepository) ListBySymbol(
	ctx context.Context,
	symbol string,
	limit, offset int,
) ([]*Position, error) {
	var positions []*Position
	err := r.db.WithContext(ctx).
		Where("symbol = ? AND size != 0", symbol).
		Limit(limit).
		Offset(offset).
		Find(&positions).Error

	return positions, err
}
