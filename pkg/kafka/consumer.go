// 文件: pkg/kafka/consumer.go
// 通用 Kafka 消费者
//
// 特点:
// - 消费者组支持
// - 自动提交/手动提交
// - 优雅关闭
// - 回调处理

package kafka

import (
	"context"
	"fmt"
	"sync"

	"github.com/IBM/sarama"
)

// =============================================================================
// Consumer 配置
// =============================================================================

// ConsumerConfig 消费者配置
type ConsumerConfig struct {
	Brokers       []string // Kafka broker 地址列表
	GroupID       string   // 消费者组 ID
	Topics        []string // 订阅的 topics
	OffsetInitial int64    // 初始 offset: -1=newest, -2=oldest
	AutoCommit    bool     // 是否自动提交 offset
}

// DefaultConsumerConfig 默认配置
func DefaultConsumerConfig(brokers []string, groupID string, topics []string) ConsumerConfig {
	return ConsumerConfig{
		Brokers:       brokers,
		GroupID:       groupID,
		Topics:        topics,
		OffsetInitial: sarama.OffsetNewest,
		AutoCommit:    true,
	}
}

// =============================================================================
// MessageHandler 消息处理器
// =============================================================================

// MessageHandler 消息处理函数
type MessageHandler func(topic string, partition int32, offset int64, key, value []byte) error

// =============================================================================
// Consumer 消费者
// =============================================================================

// Consumer 通用 Kafka 消费者
type Consumer struct {
	client  sarama.ConsumerGroup
	config  ConsumerConfig
	handler MessageHandler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewConsumer 创建消费者
func NewConsumer(cfg ConsumerConfig, handler MessageHandler) (*Consumer, error) {
	// 构建 Sarama 配置
	saramaConfig := sarama.NewConfig()
	saramaConfig.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRoundRobin()
	saramaConfig.Consumer.Offsets.Initial = cfg.OffsetInitial
	saramaConfig.Consumer.Offsets.AutoCommit.Enable = cfg.AutoCommit

	// 创建消费者组
	client, err := sarama.NewConsumerGroup(cfg.Brokers, cfg.GroupID, saramaConfig)
	if err != nil {
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Consumer{
		client:  client,
		config:  cfg,
		handler: handler,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// Start 启动消费
func (c *Consumer) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			// 加入消费者组
			handler := &consumerGroupHandler{handler: c.handler}
			err := c.client.Consume(c.ctx, c.config.Topics, handler)
			if err != nil {
				fmt.Printf("[Kafka] consume error: %v\n", err)
			}

			// 检查是否应该退出
			if c.ctx.Err() != nil {
				return
			}
		}
	}()
}

// Stop 停止消费
func (c *Consumer) Stop() error {
	c.cancel()
	c.wg.Wait()
	return c.client.Close()
}

// =============================================================================
// Sarama ConsumerGroupHandler 实现
// =============================================================================

type consumerGroupHandler struct {
	handler MessageHandler
}

func (h *consumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *consumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		// 调用用户处理器
		if err := h.handler(msg.Topic, msg.Partition, msg.Offset, msg.Key, msg.Value); err != nil {
			fmt.Printf("[Kafka] handle error: topic=%s, offset=%d, err=%v\n", msg.Topic, msg.Offset, err)
			// 继续处理下一条，不中断
		}

		// 标记已处理
		session.MarkMessage(msg, "")
	}
	return nil
}
