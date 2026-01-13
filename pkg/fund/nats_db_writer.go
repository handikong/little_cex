// 文件: pkg/fund/nats_db_writer.go
// 冷资产模块 - NATS 数据库写入器
//
// 监听 NATS 事件，写入 MySQL 冷存储:
// - trades: 更新余额
// - order.canceled: 解冻余额

package fund

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"max.com/pkg/nats"
)

// =============================================================================
// NatsDBWriter - NATS 数据库写入器
// =============================================================================

// TradeEvent 成交事件 (包含完整信息)
type TradeEvent struct {
	TradeID        int64  `json:"trade_id"`
	TakerOrderID   int64  `json:"taker_order_id"`
	MakerOrderID   int64  `json:"maker_order_id"`
	TakerUserID    int64  `json:"taker_user_id"`
	MakerUserID    int64  `json:"maker_user_id"`
	TakerMargin    int64  `json:"taker_margin"`
	MakerMargin    int64  `json:"maker_margin"`
	Symbol         string `json:"symbol"`
	SettleCurrency string `json:"settle_currency"`
	Price          int64  `json:"price"`
	Qty            int64  `json:"qty"`
	Timestamp      int64  `json:"timestamp"`
}

// NatsDBWriter NATS 数据库写入器
type NatsDBWriter struct {
	repo       *BalanceRepo
	subscriber *nats.Subscriber

	// 统计
	stats struct {
		TradesReceived  int64
		CancelsReceived int64
		WrittenCount    int64
		ErrorCount      int64
	}
	mu sync.Mutex
}

// NewNatsDBWriter 创建 NATS 数据库写入器
func NewNatsDBWriter(repo *BalanceRepo, natsURL string) (*NatsDBWriter, error) {
	w := &NatsDBWriter{repo: repo}

	subscriber, err := nats.NewSubscriber(natsURL, w.handleMessage)
	if err != nil {
		return nil, err
	}
	w.subscriber = subscriber

	return w, nil
}

// Start 启动监听
func (w *NatsDBWriter) Start() error {
	// 订阅成交事件
	if err := w.subscriber.SubscribeQueue("trades", "db-writer"); err != nil {
		return err
	}
	// 订阅撤单事件
	if err := w.subscriber.SubscribeQueue("order.canceled", "db-writer"); err != nil {
		return err
	}
	return nil
}

// Stop 停止
func (w *NatsDBWriter) Stop() error {
	return w.subscriber.Close()
}

// handleMessage 处理消息
func (w *NatsDBWriter) handleMessage(subject string, data []byte) error {
	switch subject {
	case "trades":
		return w.handleTrade(data)
	case "order.canceled":
		return w.handleCancel(data)
	}
	return nil
}

// handleTrade 处理成交事件 -> 更新冷存储余额
func (w *NatsDBWriter) handleTrade(data []byte) error {
	var event TradeEvent
	if err := json.Unmarshal(data, &event); err != nil {
		w.mu.Lock()
		w.stats.ErrorCount++
		w.mu.Unlock()
		return err
	}

	w.mu.Lock()
	w.stats.TradesReceived++
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	currency := event.SettleCurrency
	if currency == "" {
		currency = "USDT" // 默认
	}

	// 扣除 Taker 的冻结 (保证金已用于持仓)
	if event.TakerUserID > 0 && event.TakerMargin > 0 {
		if err := w.repo.DeductLocked(ctx, event.TakerUserID, currency, event.TakerMargin); err != nil {
			fmt.Printf("[NatsDBWriter] deduct taker locked failed: %v\n", err)
		}
		// 记录流水
		w.repo.InsertJournal(ctx, &JournalEvent{
			EventID:    fmt.Sprintf("trade_taker_%d", event.TradeID),
			UserID:     event.TakerUserID,
			Symbol:     currency,
			ChangeType: ChangeTypeTransfer,
			Amount:     event.TakerMargin,
			BizType:    BizTypeTrade,
			BizID:      fmt.Sprintf("%d", event.TradeID),
			CreatedAt:  time.Now(),
		})
	}

	// 扣除 Maker 的冻结
	if event.MakerUserID > 0 && event.MakerMargin > 0 {
		if err := w.repo.DeductLocked(ctx, event.MakerUserID, currency, event.MakerMargin); err != nil {
			fmt.Printf("[NatsDBWriter] deduct maker locked failed: %v\n", err)
		}
		// 记录流水
		w.repo.InsertJournal(ctx, &JournalEvent{
			EventID:    fmt.Sprintf("trade_maker_%d", event.TradeID),
			UserID:     event.MakerUserID,
			Symbol:     currency,
			ChangeType: ChangeTypeTransfer,
			Amount:     event.MakerMargin,
			BizType:    BizTypeTrade,
			BizID:      fmt.Sprintf("%d", event.TradeID),
			CreatedAt:  time.Now(),
		})
	}

	w.mu.Lock()
	w.stats.WrittenCount++
	w.mu.Unlock()

	return nil
}

// handleCancel 处理撤单事件
func (w *NatsDBWriter) handleCancel(data []byte) error {
	var event struct {
		OrderID   int64  `json:"order_id"`
		Reason    string `json:"reason"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}

	w.mu.Lock()
	w.stats.CancelsReceived++
	w.mu.Unlock()

	// 记录撤单流水
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	journal := &JournalEvent{
		EventID:    fmt.Sprintf("cancel_%d", event.OrderID),
		UserID:     0, // TODO: 需要从订单获取 UserID
		ChangeType: ChangeTypeRelease,
		Amount:     0, // TODO: 需要从订单获取解冻金额
		BizType:    BizTypeOrder,
		BizID:      fmt.Sprintf("%d", event.OrderID),
		CreatedAt:  time.Now(),
	}

	w.repo.InsertJournal(ctx, journal)

	return nil
}

// Stats 获取统计
func (w *NatsDBWriter) Stats() map[string]int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]int64{
		"trades_received":  w.stats.TradesReceived,
		"cancels_received": w.stats.CancelsReceived,
		"written_count":    w.stats.WrittenCount,
		"error_count":      w.stats.ErrorCount,
	}
}
