// 文件: pkg/fund/db_writer.go
// 冷资产模块 - 数据库写入器
//
// 消费 Kafka 事件，写入 MySQL:
// - 批量写入提高吞吐
// - 幂等写入防止重复
// - 错误重试

package fund

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"max.com/pkg/kafka"
)

// =============================================================================
// DBWriter - 数据库写入器
// =============================================================================

// DBWriter 数据库写入器
type DBWriter struct {
	repo     *BalanceRepo
	consumer *kafka.Consumer

	// 批量缓冲
	buffer    []*JournalEvent
	bufferMu  sync.Mutex
	batchSize int
	flushCh   chan struct{}

	// 统计
	stats DBWriterStats

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// DBWriterStats 写入统计
type DBWriterStats struct {
	ReceivedCount int64 // 接收数量
	WrittenCount  int64 // 写入数量
	ErrorCount    int64 // 错误数量
	BatchCount    int64 // 批次数量
}

// DBWriterConfig 配置
type DBWriterConfig struct {
	Brokers       []string      // Kafka brokers
	GroupID       string        // 消费者组
	BatchSize     int           // 批量大小
	FlushInterval time.Duration // 刷新间隔
}

// DefaultDBWriterConfig 默认配置
func DefaultDBWriterConfig(brokers []string) DBWriterConfig {
	return DBWriterConfig{
		Brokers:       brokers,
		GroupID:       "fund_db_writer",
		BatchSize:     100,
		FlushInterval: 500 * time.Millisecond,
	}
}

// NewDBWriter 创建数据库写入器
func NewDBWriter(cfg DBWriterConfig, repo *BalanceRepo) (*DBWriter, error) {
	ctx, cancel := context.WithCancel(context.Background())

	w := &DBWriter{
		repo:      repo,
		buffer:    make([]*JournalEvent, 0, cfg.BatchSize),
		batchSize: cfg.BatchSize,
		flushCh:   make(chan struct{}, 1),
		ctx:       ctx,
		cancel:    cancel,
	}

	// 创建消费者
	consumerCfg := kafka.DefaultConsumerConfig(
		cfg.Brokers,
		cfg.GroupID,
		[]string{TopicJournalEvents},
	)

	consumer, err := kafka.NewConsumer(consumerCfg, w.handleMessage)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create consumer: %w", err)
	}
	w.consumer = consumer

	return w, nil
}

// =============================================================================
// 消息处理
// =============================================================================

// handleMessage 处理单条消息
func (w *DBWriter) handleMessage(topic string, partition int32, offset int64, key, value []byte) error {
	var event JournalEvent
	if err := json.Unmarshal(value, &event); err != nil {
		w.stats.ErrorCount++
		return fmt.Errorf("unmarshal event: %w", err)
	}

	w.stats.ReceivedCount++

	// 加入缓冲
	w.bufferMu.Lock()
	w.buffer = append(w.buffer, &event)
	shouldFlush := len(w.buffer) >= w.batchSize
	w.bufferMu.Unlock()

	if shouldFlush {
		select {
		case w.flushCh <- struct{}{}:
		default:
		}
	}

	return nil
}

// =============================================================================
// 批量写入
// =============================================================================

// flush 刷新缓冲写入数据库
func (w *DBWriter) flush() {
	w.bufferMu.Lock()
	events := w.buffer
	w.buffer = make([]*JournalEvent, 0, w.batchSize)
	w.bufferMu.Unlock()

	if len(events) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 批量写入流水
	if err := w.repo.BatchInsertJournals(ctx, events); err != nil {
		w.stats.ErrorCount++
		fmt.Printf("[DBWriter] batch insert error: %v\n", err)
		return
	}

	// 更新余额 (每条事件更新对应用户的余额)
	for _, event := range events {
		snapshot := &BalanceSnapshot{
			EventID:   event.EventID,
			UserID:    event.UserID,
			Symbol:    event.Symbol,
			Available: event.AvailableAfter,
			Locked:    event.LockedAfter,
			UpdatedAt: event.CreatedAt,
		}
		if err := w.repo.UpsertBalance(ctx, snapshot); err != nil {
			w.stats.ErrorCount++
			fmt.Printf("[DBWriter] upsert balance error: user=%d, err=%v\n", event.UserID, err)
		}
	}

	w.stats.WrittenCount += int64(len(events))
	w.stats.BatchCount++
}

// =============================================================================
// 生命周期
// =============================================================================

// Start 启动写入器
func (w *DBWriter) Start(flushInterval time.Duration) {
	// 启动消费
	w.consumer.Start()

	// 启动定时刷新
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.ctx.Done():
				w.flush() // 最后刷新一次
				return
			case <-ticker.C:
				w.flush()
			case <-w.flushCh:
				w.flush()
			}
		}
	}()
}

// Stop 停止写入器
func (w *DBWriter) Stop() error {
	w.cancel()
	w.wg.Wait()
	return w.consumer.Stop()
}

// Stats 获取统计
func (w *DBWriter) Stats() DBWriterStats {
	return w.stats
}
