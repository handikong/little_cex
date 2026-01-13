// 文件: pkg/order/repository.go
package order

import "context"

type OrderRepository interface {
	// 创建
	Create(ctx context.Context, order *Order) error

	// 查询
	GetByOrderID(ctx context.Context, orderID int64) (*Order, error)
	GetActiveByUser(ctx context.Context, userID int64) ([]*Order, error)
	GetByUserAndSymbol(ctx context.Context, userID int64, symbol string, limit int) ([]*Order, error)

	// 更新
	UpdateFill(ctx context.Context, orderID int64, filledQty, avgPrice int64, status OrderStatus) error
	UpdateStatus(ctx context.Context, orderID int64, status OrderStatus) error
}
