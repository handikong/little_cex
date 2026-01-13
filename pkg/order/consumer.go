// 文件: pkg/order/consumer.go
// 订单事件消费者 - 监听撮合引擎事件，更新订单状态
// 使用 NATS (轻量级替代 Kafka)

package order

import (
	"context"
	"encoding/json"
	"log"

	"max.com/pkg/nats"
)

// =============================================================================
// 事件结构
// =============================================================================

// TradeEvent 成交事件 (来自撮合引擎)
type TradeEvent struct {
	TradeID   int64 `json:"trade_id"`
	TakerID   int64 `json:"taker_order_id"`
	MakerID   int64 `json:"maker_order_id"`
	Price     int64 `json:"price"`
	Qty       int64 `json:"qty"`
	Timestamp int64 `json:"timestamp"`
}

// CancelEvent 撤单事件
type CancelEvent struct {
	OrderID   int64  `json:"order_id"`
	Reason    string `json:"reason"`
	Timestamp int64  `json:"timestamp"`
}

// =============================================================================
// OrderConsumer - 订单事件消费者
// =============================================================================

type OrderConsumer struct {
	service    *OrderService
	subscriber *nats.Subscriber
}

// NewOrderConsumer 创建订单消费者
func NewOrderConsumer(service *OrderService, natsURL string) (*OrderConsumer, error) {
	oc := &OrderConsumer{service: service}

	subscriber, err := nats.NewSubscriber(natsURL, oc.handleMessage)
	if err != nil {
		return nil, err
	}

	oc.subscriber = subscriber
	return oc, nil
}

// Start 启动消费 (队列订阅，支持多实例负载均衡)
func (c *OrderConsumer) Start() error {
	// 订阅成交事件
	if err := c.subscriber.SubscribeQueue("trades", "order-service"); err != nil {
		return err
	}
	// 订阅撤单事件
	if err := c.subscriber.SubscribeQueue("order.canceled", "order-service"); err != nil {
		return err
	}
	return nil
}

// Stop 停止消费
func (c *OrderConsumer) Stop() error {
	return c.subscriber.Close()
}

// handleMessage 处理消息
func (c *OrderConsumer) handleMessage(subject string, data []byte) error {
	ctx := context.Background()

	switch subject {
	case "trades":
		return c.handleTradeEvent(ctx, data)
	case "order.canceled":
		return c.handleCancelEvent(ctx, data)
	}
	return nil
}

// handleTradeEvent 处理成交事件
func (c *OrderConsumer) handleTradeEvent(ctx context.Context, data []byte) error {
	var event TradeEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("unmarshal trade event error: %v", err)
		return err
	}

	// 更新 Taker 订单
	if err := c.service.OnTradeFill(ctx, event.TakerID, event.Qty, event.Price); err != nil {
		log.Printf("update taker order error: %v", err)
	}

	// 更新 Maker 订单
	if err := c.service.OnTradeFill(ctx, event.MakerID, event.Qty, event.Price); err != nil {
		log.Printf("update maker order error: %v", err)
	}

	return nil
}

// handleCancelEvent 处理撤单事件
func (c *OrderConsumer) handleCancelEvent(ctx context.Context, data []byte) error {
	var event CancelEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("unmarshal cancel event error: %v", err)
		return err
	}

	return c.service.OnOrderCanceled(ctx, event.OrderID)
}
