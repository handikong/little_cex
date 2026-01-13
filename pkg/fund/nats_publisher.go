// 文件: pkg/fund/nats_publisher.go
// 冷资产模块 - NATS 事件发布器 (轻量级替代 Kafka)

package fund

import (
	"encoding/json"
	"fmt"
	"time"

	"max.com/pkg/nats"
)

// =============================================================================
// NatsEventPublisher - NATS 事件发布器
// =============================================================================

// NatsEventPublisher NATS 事件发布器
type NatsEventPublisher struct {
	publisher *nats.Publisher
}

// NewNatsEventPublisher 创建 NATS 事件发布器
func NewNatsEventPublisher(natsURL string) (*NatsEventPublisher, error) {
	publisher, err := nats.NewPublisher(natsURL)
	if err != nil {
		return nil, err
	}

	return &NatsEventPublisher{publisher: publisher}, nil
}

// PublishJournal 发布流水事件
func (p *NatsEventPublisher) PublishJournal(event *JournalEvent) error {
	return p.publisher.Publish(TopicJournalEvents, event)
}

// PublishBalance 发布余额快照事件
func (p *NatsEventPublisher) PublishBalance(snapshot *BalanceSnapshot) error {
	return p.publisher.Publish(TopicBalanceEvents, snapshot)
}

// PublishJournalFromChange 从余额变更构建并发布事件
func (p *NatsEventPublisher) PublishJournalFromChange(
	seq uint64,
	changeType ChangeType,
	userID int64,
	symbol string,
	amount int64,
	availBefore, availAfter, lockBefore, lockAfter int64,
	bizType BizType,
	bizID string,
) error {
	event := &JournalEvent{
		EventID:         fmt.Sprintf("%s_%d_%d", changeType.String(), seq, userID),
		Seq:             seq,
		UserID:          userID,
		Symbol:          symbol,
		ChangeType:      changeType,
		Amount:          amount,
		AvailableBefore: availBefore,
		AvailableAfter:  availAfter,
		LockedBefore:    lockBefore,
		LockedAfter:     lockAfter,
		BizType:         bizType,
		BizID:           bizID,
		CreatedAt:       time.Now(),
	}

	return p.publisher.Publish(TopicJournalEvents, event)
}

// PublishTrade 发布成交事件 (用于订单服务消费)
func (p *NatsEventPublisher) PublishTrade(tradeID, takerOrderID, makerOrderID, price, qty int64) error {
	event := map[string]any{
		"trade_id":       tradeID,
		"taker_order_id": takerOrderID,
		"maker_order_id": makerOrderID,
		"price":          price,
		"qty":            qty,
		"timestamp":      time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(event)
	return p.publisher.PublishRaw("trades", data)
}

// PublishCancel 发布撤单事件
func (p *NatsEventPublisher) PublishCancel(orderID int64, reason string) error {
	event := map[string]any{
		"order_id":  orderID,
		"reason":    reason,
		"timestamp": time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(event)
	return p.publisher.PublishRaw("order.canceled", data)
}

// Close 关闭发布器
func (p *NatsEventPublisher) Close() error {
	p.publisher.Close()
	return nil
}
