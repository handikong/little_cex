// 文件: pkg/fund/publisher.go
// 冷资产模块 - 事件发布器
//
// 使用通用 kafka 包发送资产事件
// JournalEvent 实现 kafka.Message 接口

package fund

import (
	"encoding/json"
	"fmt"
	"time"

	"max.com/pkg/kafka"
)

// =============================================================================
// JournalEvent 实现 kafka.Message 接口
// =============================================================================

// Topic 返回 Kafka topic
func (e *JournalEvent) Topic() string {
	return TopicJournalEvents
}

// Key 返回分区 key (按 UserID 分区保证顺序)
func (e *JournalEvent) Key() string {
	return fmt.Sprintf("%d", e.UserID)
}

// Value 返回序列化后的消息体
func (e *JournalEvent) Value() ([]byte, error) {
	return json.Marshal(e)
}

// =============================================================================
// BalanceSnapshot 实现 kafka.Message 接口
// =============================================================================

// Topic 返回 Kafka topic
func (s *BalanceSnapshot) Topic() string {
	return TopicBalanceEvents
}

// Key 返回分区 key
func (s *BalanceSnapshot) Key() string {
	return fmt.Sprintf("%d", s.UserID)
}

// Value 返回序列化后的消息体
func (s *BalanceSnapshot) Value() ([]byte, error) {
	return json.Marshal(s)
}

// =============================================================================
// EventPublisher - 资产事件发布器
// =============================================================================

// EventPublisher 资产事件发布器
type EventPublisher struct {
	producer *kafka.Producer
}

// NewEventPublisher 创建事件发布器
func NewEventPublisher(brokers []string) (*EventPublisher, error) {
	cfg := kafka.DefaultProducerConfig(brokers)
	producer, err := kafka.NewProducer(cfg)
	if err != nil {
		return nil, err
	}

	return &EventPublisher{producer: producer}, nil
}

// PublishJournal 发布流水事件
func (p *EventPublisher) PublishJournal(event *JournalEvent) error {
	return p.producer.Send(event)
}

// PublishBalance 发布余额快照事件
func (p *EventPublisher) PublishBalance(snapshot *BalanceSnapshot) error {
	return p.producer.Send(snapshot)
}

// PublishJournalFromChange 从余额变更构建并发布事件
func (p *EventPublisher) PublishJournalFromChange(
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

	return p.producer.Send(event)
}

// Close 关闭发布器
func (p *EventPublisher) Close() error {
	return p.producer.Close()
}

// Stats 获取统计
func (p *EventPublisher) Stats() kafka.ProducerStats {
	return p.producer.Stats()
}
