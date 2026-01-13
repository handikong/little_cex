// 文件: pkg/order/mysql_repo.go
package order

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type MySQLOrderRepository struct {
	db *gorm.DB
}

func NewMySQLOrderRepository(db *gorm.DB) *MySQLOrderRepository {
	return &MySQLOrderRepository{db: db}
}

func (r *MySQLOrderRepository) Create(ctx context.Context, order *Order) error {
	return r.db.WithContext(ctx).Create(order).Error
}

func (r *MySQLOrderRepository) GetByOrderID(ctx context.Context, orderID int64) (*Order, error) {
	var order Order
	err := r.db.WithContext(ctx).Where("order_id = ?", orderID).First(&order).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *MySQLOrderRepository) GetActiveByUser(ctx context.Context, userID int64) ([]*Order, error) {
	var orders []*Order
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND status IN ?", userID, []OrderStatus{StatusNew, StatusPartiallyFilled}).
		Order("created_at DESC").
		Find(&orders).Error
	return orders, err
}

func (r *MySQLOrderRepository) GetByUserAndSymbol(ctx context.Context, userID int64, symbol string, limit int) ([]*Order, error) {
	var orders []*Order
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND symbol = ?", userID, symbol).
		Order("created_at DESC").
		Limit(limit).
		Find(&orders).Error
	return orders, err
}

func (r *MySQLOrderRepository) UpdateFill(ctx context.Context, orderID int64, filledQty, avgPrice int64, status OrderStatus) error {
	return r.db.WithContext(ctx).
		Model(&Order{}).
		Where("order_id = ?", orderID).
		Updates(map[string]any{
			"filled_qty": filledQty,
			"avg_price":  avgPrice,
			"status":     status,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}

func (r *MySQLOrderRepository) UpdateStatus(ctx context.Context, orderID int64, status OrderStatus) error {
	return r.db.WithContext(ctx).
		Model(&Order{}).
		Where("order_id = ?", orderID).
		Updates(map[string]any{
			"status":     status,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}
