package mtrade

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// =============================================================================
// 撮合引擎 (Matching Engine)
// =============================================================================
//
// 【面试高频】撮合引擎是订单处理的入口
//
// 架构：
//   订单输入 → 订单队列 → 撮合线程 → 事件发布
//
//   ┌─────────────┐
//   │  OrderInput │ ──► Channel ──► MatchingLoop ──► EventBus
//   └─────────────┘

// EngineConfig 引擎配置
type EngineConfig struct {
	Symbol         string // 交易对
	OrderQueueSize int    // 订单队列大小
	WALDir         string // WAL 文件目录（为空则不启用 WAL）
}

// DefaultEngineConfig 默认配置
func DefaultEngineConfig(symbol string) EngineConfig {
	return EngineConfig{
		Symbol:         symbol,
		OrderQueueSize: 10000,
		WALDir:         "", // 默认不启用 WAL
	}
}

// =============================================================================
// 事件定义
// =============================================================================

// EventType 事件类型
type EventType int

const (
	EventTrade         EventType = iota // 成交事件
	EventOrderAccepted                  // 订单接受
	EventOrderRejected                  // 订单拒绝
	EventOrderCanceled                  // 订单取消
)

// Event 事件
type Event struct {
	Type      EventType
	Timestamp int64
	Order     *Order       // 相关订单
	Trade     *Trade       // 成交记录（仅 EventTrade）
	Result    *MatchResult // 撮合结果
}

// EventHandler 事件处理器
type EventHandler func(Event)

// =============================================================================
// 撮合引擎
// =============================================================================

// Engine 撮合引擎
// 【Go最佳实践】不在 struct 中存储 context，而是通过参数传递
type Engine struct {
	config    EngineConfig
	orderBook *OrderBook
	matcher   *Matcher

	// WAL（可选）
	wal *WAL

	// 订单输入队列
	orderCh chan *Order

	// 取消订单队列
	cancelCh chan int64

	// 异步事件队列
	eventCh chan Event

	// 事件处理器
	handlers []EventHandler
	mu       sync.RWMutex

	// 生命周期
	stopCh chan struct{}
	wg     sync.WaitGroup

	// 统计
	stats EngineStats
}

// EngineStats 引擎统计
type EngineStats struct {
	OrdersReceived int64
	OrdersMatched  int64
	TradesExecuted int64
	OrdersCanceled int64
	EventsDropped  int64 // 事件队列满时丢弃的事件数
}

// NewEngine 创建撮合引擎
func NewEngine(config EngineConfig) (*Engine, error) {
	ob := NewOrderBook(config.Symbol)

	engine := &Engine{
		config:    config,
		orderBook: ob,
		matcher:   NewMatcher(ob),
		orderCh:   make(chan *Order, config.OrderQueueSize),
		cancelCh:  make(chan int64, 1000),
		eventCh:   make(chan Event, 10000),
		handlers:  make([]EventHandler, 0),
		stopCh:    make(chan struct{}),
	}

	// 初始化 WAL（如果配置了）
	if config.WALDir != "" {
		walConfig := WALConfig{
			Dir:      config.WALDir,
			SyncMode: SyncModeBatch, // 批量刷盘
		}
		wal, err := NewWAL(walConfig)
		if err != nil {
			return nil, err
		}
		engine.wal = wal

		// 执行恢复
		recovery := NewWALRecovery(wal)
		if err := recovery.Recover(engine); err != nil {
			return nil, fmt.Errorf("failed to recover from WAL: %v", err)
		}
	}

	return engine, nil
}

// =============================================================================
// 生命周期
// =============================================================================

// Start 启动撮合引擎
// 【Go最佳实践】ctx 作为第一个参数传入，而不是存储在 struct 中
func (e *Engine) Start(ctx context.Context) {
	e.wg.Add(2) // matchLoop + eventLoop
	go e.matchLoop(ctx)
	go e.eventLoop(ctx) // 独立的事件分发线程
	// log.Printf("[Engine] %s started", e.config.Symbol)
}

// Stop 停止撮合引擎
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()

	// 关闭 WAL
	if e.wal != nil {
		e.wal.Sync()
		e.wal.Close()
	}
}

// CreateCheckpoint 创建检查点
// 【运维】手动触发或定时触发
func (e *Engine) CreateCheckpoint() error {
	if e.wal == nil {
		return nil
	}

	// 获取所有订单（快照）
	// 注意：这里假设是在 matchLoop 中调用，或者是线程安全的
	// 如果是外部调用，需要通过 channel 发送指令到 matchLoop
	// 为了简化，这里暂时假设外部调用时引擎已暂停或无流量
	// 更好的做法是发送 CheckpointEvent 到 matchLoop

	orders := e.orderBook.GetAllOrders()
	seq := e.wal.GetSequence()

	// 创建 Checkpoint
	if err := e.wal.CreateCheckpoint(seq, orders); err != nil {
		return err
	}

	// 截断 WAL
	return e.wal.Truncate()
}

// matchLoop 撮合主循环
// 【面试核心】单线程处理所有订单，保证顺序性
// 【Go最佳实践】ctx 作为参数传入
func (e *Engine) matchLoop(ctx context.Context) {
	defer e.wg.Done()

	for {
		select {
		case <-ctx.Done(): // 外部 context 取消
			return

		case <-e.stopCh: // 内部停止信号
			return

		case order := <-e.orderCh:
			e.processOrder(order)

		case orderID := <-e.cancelCh:
			e.processCancelOrder(orderID)
		}
	}
}

// =============================================================================
// 订单处理
// =============================================================================

// SubmitOrder 提交订单
// 【面试】异步提交，放入队列等待处理
func (e *Engine) SubmitOrder(order *Order) bool {
	select {
	case e.orderCh <- order:
		e.stats.OrdersReceived++
		return true
	default:
		// 队列满了
		return false
	}
}

// CancelOrder 取消订单
func (e *Engine) CancelOrder(orderID int64) bool {
	select {
	case e.cancelCh <- orderID:
		return true
	default:
		return false
	}
}

// processOrder 处理订单
func (e *Engine) processOrder(order *Order) {
	// 设置时间戳
	if order.CreatedAt == 0 {
		order.CreatedAt = time.Now().UnixNano()
	}

	// 生成订单 ID
	if order.ID == 0 {
		order.ID = NextOrderID()
	}

	// 【WAL】先写日志，再撮合
	if e.wal != nil {
		e.wal.WriteOrder(order)
	}

	// 撮合
	result := e.matcher.ProcessOrder(order)
	e.stats.OrdersMatched++

	// 发布事件
	e.publishOrderEvent(order, result)

	// 发布成交事件（关键事件，不可丢弃）
	for i := range result.Trades {
		e.stats.TradesExecuted++
		e.publishCriticalEvent(Event{
			Type:      EventTrade,
			Timestamp: result.Trades[i].Timestamp,
			Trade:     &result.Trades[i],
		})
	}

	// 更新快照（供外部无锁读取）
	e.orderBook.UpdateSnapshot()

	// 归还结果到对象池
	PutMatchResult(result)
}

// processCancelOrder 处理取消订单
func (e *Engine) processCancelOrder(orderID int64) {
	// 【WAL】先写日志
	if e.wal != nil {
		e.wal.WriteCancelOrder(orderID)
	}

	order := e.orderBook.CancelOrder(orderID)
	if order != nil {
		e.stats.OrdersCanceled++
		e.publishCriticalEvent(Event{
			Type:      EventOrderCanceled,
			Timestamp: time.Now().UnixNano(),
			Order:     order,
		})
	}
}

// =============================================================================
// 事件发布（分级策略）
// =============================================================================

// OnEvent 注册事件处理器
// 【支持多订阅者】可以注册多个 handler
func (e *Engine) OnEvent(handler EventHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers = append(e.handlers, handler)
}

// publishCriticalEvent 发布关键事件（阻塞，保证不丢）
// 【用于】Trade、OrderAccepted、OrderRejected、OrderCanceled
func (e *Engine) publishCriticalEvent(event Event) {
	// 监控队列长度
	queueLen := len(e.eventCh)
	if queueLen > cap(e.eventCh)*8/10 { // 超过 80%
		//log.Printf("[CRITICAL] event queue high watermark: %d/%d", queueLen, cap(e.eventCh))
	}

	// 阻塞发送，保证不丢失
	e.eventCh <- event
}

// publishEvent 发布普通事件（非阻塞，可丢弃）
// 【用于】Depth 更新等非关键事件
func (e *Engine) publishEvent(event Event) {
	select {
	case e.eventCh <- event:
		// 发送成功
	default:
		// 队列满了，丢弃
		e.stats.EventsDropped++
	}
}

// eventLoop 事件分发循环（独立 goroutine）
// 【异步】从 eventCh 读取事件，分发到所有 handler
func (e *Engine) eventLoop(ctx context.Context) {
	defer e.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return

		case <-e.stopCh:
			return

		case event := <-e.eventCh:
			e.dispatchEvent(event)
		}
	}
}

// dispatchEvent 分发事件到所有 handler
func (e *Engine) dispatchEvent(event Event) {
	e.mu.RLock()
	handlers := e.handlers
	e.mu.RUnlock()

	for _, h := range handlers {
		h(event)
	}
}

// publishOrderEvent 发布订单状态事件
func (e *Engine) publishOrderEvent(order *Order, result *MatchResult) {
	var eventType EventType
	switch order.Status {
	case OrderStatusRejected:
		eventType = EventOrderRejected
	default:
		eventType = EventOrderAccepted
	}

	e.publishCriticalEvent(Event{ // 订单状态变更是关键事件
		Type:      eventType,
		Timestamp: time.Now().UnixNano(),
		Order:     order,
		Result:    result,
	})
}

// =============================================================================
// 查询方法
// =============================================================================

// GetOrderBook 获取订单簿（用于查询）
func (e *Engine) GetOrderBook() *OrderBook {
	return e.orderBook
}

// GetStats 获取统计信息
func (e *Engine) GetStats() EngineStats {
	return e.stats
}

// GetDepth 获取深度
func (e *Engine) GetDepth(n int) (bids, asks []DepthLevel) {
	return e.orderBook.Depth(n)
}
