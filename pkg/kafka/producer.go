// 文件: pkg/kafka/producer.go
// 通用 Kafka 生产者
//
// 特点:
// - 异步发送，高吞吐
// - 错误处理
// - 优雅关闭
// - 支持任意消息类型 (通过 Message 接口)

package kafka

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IBM/sarama"
)

// =============================================================================
// Message 接口 - 所有消息类型需实现
// =============================================================================

// Message 通用消息接口
type Message interface {
	Topic() string          // 目标 topic
	Key() string            // 分区 key (相同 key 保证顺序)
	Value() ([]byte, error) // 消息体 (序列化后的数据)
}

// =============================================================================
// Producer 配置
// =============================================================================

// ProducerConfig 生产者配置
type ProducerConfig struct {
	Brokers        []string      // Kafka broker 地址列表
	RequiredAcks   int           // 确认模式: 0=不等待, 1=leader确认, -1=全部确认
	Compression    string        // 压缩方式: none, gzip, snappy, lz4, zstd
	FlushFrequency time.Duration // 刷新间隔
	FlushMessages  int           // 批量消息数
	MaxRetries     int           // 最大重试次数
}

// DefaultProducerConfig 默认配置
func DefaultProducerConfig(brokers []string) ProducerConfig {
	return ProducerConfig{
		Brokers:        brokers,
		RequiredAcks:   1,
		Compression:    "snappy",
		FlushFrequency: 100 * time.Millisecond,
		FlushMessages:  100,
		MaxRetries:     3,
	}
}

// =============================================================================
// Producer 生产者
// =============================================================================

// Producer 通用 Kafka 生产者
type Producer struct {
	producer sarama.AsyncProducer
	config   ProducerConfig

	// 统计
	sentCount  atomic.Int64
	errorCount atomic.Int64

	// 生命周期
	closed atomic.Bool
	wg     sync.WaitGroup
}

// NewProducer 创建生产者
func NewProducer(cfg ProducerConfig) (*Producer, error) {
	// 构建 Sarama 配置
	saramaConfig := sarama.NewConfig()

	// 确认模式
	switch cfg.RequiredAcks {
	case 0:
		saramaConfig.Producer.RequiredAcks = sarama.NoResponse
	case 1:
		saramaConfig.Producer.RequiredAcks = sarama.WaitForLocal
	case -1:
		saramaConfig.Producer.RequiredAcks = sarama.WaitForAll
	default:
		saramaConfig.Producer.RequiredAcks = sarama.WaitForLocal
	}

	// 压缩方式
	switch cfg.Compression {
	case "gzip":
		saramaConfig.Producer.Compression = sarama.CompressionGZIP
	case "snappy":
		saramaConfig.Producer.Compression = sarama.CompressionSnappy
	case "lz4":
		saramaConfig.Producer.Compression = sarama.CompressionLZ4
	case "zstd":
		saramaConfig.Producer.Compression = sarama.CompressionZSTD
	default:
		saramaConfig.Producer.Compression = sarama.CompressionNone
	}

	// 批量设置
	saramaConfig.Producer.Flush.Frequency = cfg.FlushFrequency
	saramaConfig.Producer.Flush.Messages = cfg.FlushMessages
	saramaConfig.Producer.Retry.Max = cfg.MaxRetries

	// 异步模式
	saramaConfig.Producer.Return.Successes = false
	saramaConfig.Producer.Return.Errors = true

	// 创建生产者
	producer, err := sarama.NewAsyncProducer(cfg.Brokers, saramaConfig)
	if err != nil {
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}

	p := &Producer{
		producer: producer,
		config:   cfg,
	}

	// 启动错误处理
	p.wg.Add(1)
	go p.handleErrors()

	return p, nil
}

// =============================================================================
// 发送接口
// =============================================================================

// Send 发送消息 (异步)
func (p *Producer) Send(msg Message) error {
	if p.closed.Load() {
		return fmt.Errorf("producer is closed")
	}

	data, err := msg.Value()
	if err != nil {
		return fmt.Errorf("serialize message: %w", err)
	}

	m := &sarama.ProducerMessage{
		Topic: msg.Topic(),
		Key:   sarama.StringEncoder(msg.Key()),
		Value: sarama.ByteEncoder(data),
	}

	p.producer.Input() <- m
	p.sentCount.Add(1)

	return nil
}

// SendRaw 发送原始消息
func (p *Producer) SendRaw(topic, key string, value []byte) error {
	if p.closed.Load() {
		return fmt.Errorf("producer is closed")
	}

	m := &sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(key),
		Value: sarama.ByteEncoder(value),
	}

	p.producer.Input() <- m
	p.sentCount.Add(1)

	return nil
}

// =============================================================================
// 错误处理
// =============================================================================

func (p *Producer) handleErrors() {
	defer p.wg.Done()

	for err := range p.producer.Errors() {
		p.errorCount.Add(1)
		// TODO: 生产环境应该记录日志或发送告警
		fmt.Printf("[Kafka] send error: topic=%s, err=%v\n", err.Msg.Topic, err.Err)
	}
}

// =============================================================================
// 统计与生命周期
// =============================================================================

// ProducerStats 统计信息
type ProducerStats struct {
	SentCount  int64
	ErrorCount int64
}

// Stats 获取统计信息
func (p *Producer) Stats() ProducerStats {
	return ProducerStats{
		SentCount:  p.sentCount.Load(),
		ErrorCount: p.errorCount.Load(),
	}
}

// Close 关闭生产者
func (p *Producer) Close() error {
	if p.closed.Swap(true) {
		return nil // 已经关闭
	}

	err := p.producer.Close()
	p.wg.Wait() // 等待错误处理完成

	return err
}
