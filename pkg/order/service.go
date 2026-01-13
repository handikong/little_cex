// 文件: pkg/order/service.go
// 统一订单服务，供现货/合约/期权共用

package order

import (
	"context"
	"encoding/json"
	"time"
)

type OrderService struct {
	repo OrderRepository
}

func NewOrderService(repo OrderRepository) *OrderService {
	return &OrderService{repo: repo}
}

// =============================================================================
// 同步操作 (Processor 调用)
// =============================================================================

// CreateOrder 创建订单 (下单时同步调用)
func (s *OrderService) CreateOrder(ctx context.Context, order *Order) error {
	order.Status = StatusNew
	order.CreatedAt = time.Now().UnixMilli()
	order.UpdatedAt = order.CreatedAt
	return s.repo.Create(ctx, order)
}

// CreateFuturesOrder 创建合约订单 (便捷方法)
func (s *OrderService) CreateFuturesOrder(ctx context.Context, orderID, userID int64, symbol string, side OrderSide, price, qty int64, leverage int, margin int64) error {
	extra, _ := json.Marshal(map[string]any{
		"leverage": leverage,
		"margin":   margin,
	})
	order := &Order{
		OrderID:     orderID,
		UserID:      userID,
		Symbol:      symbol,
		ProductType: ProductFutures,
		Side:        side,
		OrderType:   OrderTypeLimit,
		Price:       price,
		Qty:         qty,
		Extra:       string(extra),
	}
	return s.CreateOrder(ctx, order)
}

// =============================================================================
// 事件处理 (消费 Kafka 事件)
// =============================================================================

// OnTradeFill 成交事件处理
func (s *OrderService) OnTradeFill(ctx context.Context, orderID int64, fillQty, fillPrice int64) error {
	order, err := s.repo.GetByOrderID(ctx, orderID)
	if err != nil {
		return err
	}

	// 计算新的成交均价
	newFilledQty := order.FilledQty + fillQty
	newAvgPrice := (order.AvgPrice*order.FilledQty + fillPrice*fillQty) / newFilledQty

	// 判断状态
	var newStatus OrderStatus
	if newFilledQty >= order.Qty {
		newStatus = StatusFilled
	} else {
		newStatus = StatusPartiallyFilled
	}

	return s.repo.UpdateFill(ctx, orderID, newFilledQty, newAvgPrice, newStatus)
}

// OnOrderCanceled 撤单事件处理
func (s *OrderService) OnOrderCanceled(ctx context.Context, orderID int64) error {
	return s.repo.UpdateStatus(ctx, orderID, StatusCanceled)
}

// =============================================================================
// 查询
// =============================================================================

func (s *OrderService) GetOrder(ctx context.Context, orderID int64) (*Order, error) {
	return s.repo.GetByOrderID(ctx, orderID)
}

func (s *OrderService) GetActiveOrders(ctx context.Context, userID int64) ([]*Order, error) {
	return s.repo.GetActiveByUser(ctx, userID)
}

func (s *OrderService) GetOrderHistory(ctx context.Context, userID int64, symbol string, limit int) ([]*Order, error) {
	return s.repo.GetByUserAndSymbol(ctx, userID, symbol, limit)
}
