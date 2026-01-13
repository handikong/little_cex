// 文件: pkg/asset/engine.go
// 热钱包账户引擎 - 主引擎
//
// 核心职责:
// 1. 管理多个分片 (Shard)，按 UserID 路由
// 2. 处理跨分片操作 (如成交结算涉及两个用户)
// 3. 提供统一的对外接口
// 4. 管理快照存储
//
// 架构:
//
//   外部调用 (撮合引擎/强平引擎)
//          │
//          ▼
//   ┌──────────────────────┐
//   │   AccountEngine      │
//   │   - 路由分片         │
//   │   - 跨分片协调       │
//   └──────────────────────┘
//          │
//   ┌──────┼──────┬──────┬──────┐
//   ▼      ▼      ▼      ▼      ▼
// Shard0 Shard1 Shard2 ... Shard7
//   │      │      │            │
//   └──────┴──────┴────────────┘
//                 │
//          SnapshotStore (无锁读)
//                 │
//                 ▼
//          风控/强平引擎

package asset

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// =============================================================================
// 配置
// =============================================================================

// EngineConfig 引擎配置
type EngineConfig struct {
	// NumShards 分片数量 (默认 8)
	// 建议设置为 CPU 核数或其倍数
	NumShards int

	// CommandQueueLen 每个分片的命令队列长度
	// 队列满时会阻塞，设置过大会占用内存
	CommandQueueLen int

	// DefaultTimeout 默认操作超时时间
	DefaultTimeout time.Duration
	WALDir         string // WAL 目录，为空则不启用

}

// DefaultEngineConfig 返回默认配置
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		NumShards:       NumShards, // 使用 model.go 中定义的常量
		CommandQueueLen: 10000,
		DefaultTimeout:  time.Second,
	}
}

// =============================================================================
// AccountEngine - 主引擎
// =============================================================================

// AccountEngine 账户引擎
//
// 这是资金系统的统一入口：
// - 撮合引擎调用 Reserve/Release/ApplyFill
// - 强平引擎读取 GetSnapshot
// - 资金服务调用 Deposit/Withdraw
//
// 使用示例:
//
//	engine := asset.NewEngine(asset.DefaultEngineConfig())
//	engine.Start()
//	defer engine.Stop()
//
//	// 下单冻结
//	err := engine.Reserve(userID, "USDT", 10000, orderID)
//
//	// 成交结算
//	err := engine.ApplyFill(fill)
//
//	// 读取快照 (无锁)
//	snap := engine.GetSnapshot(userID)
type AccountEngine struct {
	config EngineConfig

	// ===== 分片 =====
	shards []*Shard

	// ===== 快照存储 =====
	snapshotStore *SnapshotStore

	// ===== 序列号生成 =====
	// 全局递增，用于 WAL 和幂等键生成
	sequence atomic.Uint64

	// ===== 生命周期 =====
	running atomic.Bool
	stopCh  chan struct{}
	mu      sync.Mutex
}

// NewEngine 创建账户引擎
func NewEngine(cfg EngineConfig) *AccountEngine {
	if cfg.NumShards <= 0 {
		cfg.NumShards = NumShards
	}
	if cfg.CommandQueueLen <= 0 {
		cfg.CommandQueueLen = 10000
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = time.Second
	}

	// 创建快照存储
	snapshotStore := NewSnapshotStore()

	// 创建分片
	shards := make([]*Shard, cfg.NumShards)
	for i := 0; i < cfg.NumShards; i++ {
		var wal *WAL
		if cfg.WALDir != "" {
			walDir := filepath.Join(cfg.WALDir, fmt.Sprintf("shard_%d", i))
			var err error
			wal, err = NewWAL(WALConfig{Dir: walDir})
			if err != nil {
				// 处理错误
			}
		}

		shards[i] = NewShard(ShardConfig{
			ID:              i,
			CommandQueueLen: cfg.CommandQueueLen,
			SnapshotStore:   snapshotStore,
			WAL:             wal, // 传入 WAL

		})
	}

	return &AccountEngine{
		config:        cfg,
		shards:        shards,
		snapshotStore: snapshotStore,
		stopCh:        make(chan struct{}),
	}
}

// =============================================================================
// 生命周期
// =============================================================================

// Start 启动引擎
func (e *AccountEngine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running.Load() {
		return nil
	}

	// 启动所有分片
	for _, shard := range e.shards {
		shard.Start()
	}

	e.running.Store(true)
	return nil
}

// Stop 停止引擎
func (e *AccountEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running.Load() {
		return
	}

	// 停止所有分片
	for _, shard := range e.shards {
		shard.Stop()
	}

	e.running.Store(false)
}

// =============================================================================
// 分片路由
// =============================================================================

// getShard 根据 UserID 获取对应分片
//
// 使用简单的取模运算:
// - 确保同一用户的所有操作都在同一分片
// - 分布均匀
func (e *AccountEngine) getShard(userID int64) *Shard {
	// 处理负数 UserID
	idx := userID % int64(len(e.shards))
	if idx < 0 {
		idx = -idx
	}
	return e.shards[idx]
}

// nextSequence 生成下一个序列号
func (e *AccountEngine) nextSequence() uint64 {
	return e.sequence.Add(1)
}

// RecoverAll 恢复所有分片
func (e *AccountEngine) RecoverAll() error {
	for _, shard := range e.shards {
		if _, err := shard.RecoverFromWAL(); err != nil {
			return fmt.Errorf("recover shard %d: %w", shard.id, err)
		}
	}
	return nil
}

// StartCheckpointLoop 启动定期检查点
func (e *AccountEngine) StartCheckpointLoop(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				e.CreateCheckpoint()
			case <-e.stopCh:
				return
			}
		}
	}()
}

// CreateCheckpoint 创建所有分片的检查点
func (e *AccountEngine) CreateCheckpoint() error {
	for _, shard := range e.shards {
		if err := shard.CreateCheckpoint(); err != nil {
			return err
		}
	}
	return nil
}

// =============================================================================
// 核心操作 - 对外接口
// =============================================================================

// Reserve 冻结资产 (下单前调用)
//
// 参数:
//   - userID: 用户ID
//   - symbol: 资产符号 (如 "USDT")
//   - amount: 冻结金额
//   - orderID: 订单ID (用于幂等性)
//
// 返回:
//   - error: 失败原因 (余额不足等)
//
// 使用场景:
//
//	用户下买单: Reserve(userID, "USDT", price*qty, orderID)
//	用户下卖单: Reserve(userID, "BTC", qty, orderID)
func (e *AccountEngine) Reserve(userID int64, symbol string, amount int64, orderID int64) error {
	shard := e.getShard(userID)

	cmd := Command{
		Type:   CmdReserve,
		CmdID:  fmt.Sprintf("reserve_%d", orderID),
		UserID: userID,
		Symbol: symbol,
		Amount: amount,
	}

	return shard.Submit(cmd, e.config.DefaultTimeout)
}

// Release 解冻资产 (撤单时调用)
//
// 参数:
//   - userID: 用户ID
//   - symbol: 资产符号
//   - amount: 解冻金额
//   - orderID: 订单ID
//
// 使用场景:
//
//	用户撤销买单: Release(userID, "USDT", remainingAmount, orderID)
//	订单部分成交后撤销剩余: Release(userID, "USDT", unfilledAmount, orderID)
func (e *AccountEngine) Release(userID int64, symbol string, amount int64, orderID int64) error {
	shard := e.getShard(userID)

	cmd := Command{
		Type:   CmdRelease,
		CmdID:  fmt.Sprintf("release_%d", orderID),
		UserID: userID,
		Symbol: symbol,
		Amount: amount,
	}

	return shard.Submit(cmd, e.config.DefaultTimeout)
}

// ApplyFill 应用成交 (撮合引擎回调)
//
// 这是最复杂的操作，需要处理:
// 1. 扣除卖方的冻结资产
// 2. 给买方增加资产
// 3. 扣除双方手续费
// 4. 可能涉及跨分片 (买卖方在不同分片)
//
// 参数:
//   - fill: 成交事件
//
// 成交事件示例 (现货 BTC/USDT):
//
//	ApplyFill(&FillEvent{
//	    TradeID:    12345,
//	    BuyerID:    100,
//	    SellerID:   200,
//	    BaseAsset:  "BTC",
//	    QuoteAsset: "USDT",
//	    Price:      50000_00000000,  // 50000 USDT
//	    Quantity:   1_00000000,      // 1 BTC
//	    BuyerFee:   25_00000000,     // 25 USDT
//	    SellerFee:  0_00050000,      // 0.0005 BTC
//	})
func (e *AccountEngine) ApplyFill(fill *FillEvent) error {
	// 计算金额
	// 注意: 避免溢出! 先除后乘
	// quoteAmount = Price * Quantity / Precision
	// 如果 Price 和 Quantity 都很大，先乘会溢出
	// 解决方案: (Price / Precision) * Quantity 或使用 big.Int
	// 这里假设 Price 已经是带精度的值 (如 50000_00000000 表示 50000)
	// Quantity 也是带精度的值 (如 1_00000000 表示 1)
	// quoteAmount = Price * Quantity / Precision
	// = (Price / Precision) * Quantity  (先除，可能丢失精度)
	// 更好的方式: 使用 uint128 或分步计算
	quoteAmount := (fill.Price / Precision) * fill.Quantity // 先除再乘，避免溢出
	baseAmount := fill.Quantity                             // 卖方支付的 BTC

	// ===== 处理卖方 =====
	// 卖方: 扣 BTC (Locked), 加 USDT (Available), 扣 BTC 手续费
	sellerShard := e.getShard(fill.SellerID)
	sellerCmd := Command{
		Type:     CmdTransfer,
		CmdID:    fmt.Sprintf("fill_seller_%d", fill.TradeID),
		UserID:   fill.SellerID,
		Symbol:   fill.BaseAsset, // 卖方扣 BTC
		Amount:   baseAmount,
		ToUserID: fill.SellerID,       // 收款人是自己
		ToSymbol: fill.QuoteAsset,     // 收 USDT
		ToAmount: quoteAmount,         // 收到的 USDT
		Fee:      fill.SellerFee,      // 手续费
		FeeAsset: fill.SellerFeeAsset, // 手续费资产
	}

	if err := sellerShard.Submit(sellerCmd, e.config.DefaultTimeout); err != nil {
		return fmt.Errorf("seller transfer failed: %w", err)
	}

	// ===== 处理买方 =====
	// 买方: 扣 USDT (Locked), 加 BTC (Available), 扣 USDT 手续费
	buyerShard := e.getShard(fill.BuyerID)
	buyerCmd := Command{
		Type:     CmdTransfer,
		CmdID:    fmt.Sprintf("fill_buyer_%d", fill.TradeID),
		UserID:   fill.BuyerID,
		Symbol:   fill.QuoteAsset, // 买方扣 USDT
		Amount:   quoteAmount,
		ToUserID: fill.BuyerID,       // 收款人是自己
		ToSymbol: fill.BaseAsset,     // 收 BTC
		ToAmount: baseAmount,         // 收到的 BTC
		Fee:      fill.BuyerFee,      // 手续费
		FeeAsset: fill.BuyerFeeAsset, // 手续费资产
	}

	if err := buyerShard.Submit(buyerCmd, e.config.DefaultTimeout); err != nil {
		return fmt.Errorf("buyer transfer failed: %w", err)
	}

	return nil
}

// =============================================================================
// 查询接口 (无锁)
// =============================================================================

// GetSnapshot 获取用户快照 (无锁读取)
//
// 这是风控/强平引擎的主要接口:
// - 无锁，性能极高
// - 返回的是快照副本，可能略微滞后于最新状态
// - 适合读多写少的场景
func (e *AccountEngine) GetSnapshot(userID int64) *Snapshot {
	return e.snapshotStore.Get(userID)
}

// GetAvailable 快速获取可用余额
//
// 先尝试从快照读取，如果没有则从分片读取
func (e *AccountEngine) GetAvailable(userID int64, symbol string) int64 {
	snap := e.snapshotStore.Get(userID)
	if snap != nil {
		if asset, ok := snap.Assets[symbol]; ok {
			return asset.Available
		}
	}
	return 0
}

// =============================================================================
// 统计接口
// =============================================================================

// EngineStats 引擎统计
type EngineStats struct {
	TotalShards   int
	TotalUsers    int
	TotalCommands uint64
	ShardStats    []ShardStats
}

// GetStats 获取引擎统计信息
func (e *AccountEngine) GetStats() EngineStats {
	stats := EngineStats{
		TotalShards: len(e.shards),
		ShardStats:  make([]ShardStats, len(e.shards)),
	}

	for i, shard := range e.shards {
		shardStats := shard.GetStats()
		stats.ShardStats[i] = shardStats
		stats.TotalUsers += shardStats.ActiveUserCount
		stats.TotalCommands += shardStats.TotalCommands
	}

	return stats
}

// =============================================================================
// 外部余额同步 (充值/提现事件)
// =============================================================================

// BalanceChangeEvent 余额变更事件
// 资金服务通过消息队列 (Kafka) 发送此事件通知热钱包更新余额
type BalanceChangeEvent struct {
	EventType string // "DEPOSIT" / "WITHDRAW"
	EventID   string // 幂等键 (如 deposit_id, withdraw_id)
	UserID    int64
	Symbol    string
	Amount    int64 // 金额 (正数)
	Timestamp int64
}

// ApplyBalanceChange 应用余额变更 (由事件监听器调用)
//
// 使用场景:
// 1. 资金服务监听到链上充值确认 -> 发送 DEPOSIT 事件
// 2. 热钱包监听消息 -> 调用 ApplyBalanceChange 更新内存余额
//
// 示例:
//
//	engine.ApplyBalanceChange(&BalanceChangeEvent{
//	    EventType: "DEPOSIT",
//	    EventID:   "deposit_12345",
//	    UserID:    100,
//	    Symbol:    "USDT",
//	    Amount:    10000_00000000,  // 10000 USDT
//	})
func (e *AccountEngine) ApplyBalanceChange(event *BalanceChangeEvent) error {
	shard := e.getShard(event.UserID)

	var cmdType CmdType
	if event.EventType == "DEPOSIT" {
		cmdType = CmdAddBalance
	} else {
		cmdType = CmdDeductBalance
	}

	cmd := Command{
		Type:   cmdType,
		CmdID:  event.EventID,
		UserID: event.UserID,
		Symbol: event.Symbol,
		Amount: event.Amount,
	}

	return shard.Submit(cmd, e.config.DefaultTimeout)
}

// =============================================================================
// 对账接口 (Reconciliation)
// =============================================================================

// GetAllSnapshots 导出所有用户快照 (供对账使用)
//
// 使用场景:
// - 每天凌晨调用，生成热账户快照
// - 与数据库 (冷账户) 进行比对
// - 发现差异则进入告警/调整流程
//
// 注意: 此方法会遍历所有分片，可能较慢，建议在低峰期调用
func (e *AccountEngine) GetAllSnapshots() map[int64]*Snapshot {
	result := make(map[int64]*Snapshot)

	for _, shard := range e.shards {
		// 注意: 这里直接访问 shard.users 存在竞态风险
		// 生产环境应该通过命令队列实现安全访问
		for userID := range shard.users {
			if snap := e.snapshotStore.Get(userID); snap != nil {
				result[userID] = snap
			}
		}
	}

	return result
}

// ReconcileResult 对账结果
type ReconcileResult struct {
	UserID      int64
	Symbol      string
	HotBalance  int64 // 热钱包余额
	ColdBalance int64 // 数据库余额
	Diff        int64 // 差异 (Hot - Cold)
}

// =============================================================================
// FillEvent - 成交事件 (从撮合引擎传入)
// =============================================================================

// FillEvent 成交事件
//
// 当撮合引擎产生成交时，调用 ApplyFill 传入此结构
type FillEvent struct {
	TradeID int64 // 成交ID (幂等键)
	OrderID int64 // 订单ID
	MatchID int64 // 撮合ID (可选)

	// ===== 买卖双方 =====
	BuyerID  int64 // 买方用户ID
	SellerID int64 // 卖方用户ID

	// ===== 交易对 =====
	BaseAsset  string // 基础资产 (如 "BTC")
	QuoteAsset string // 计价资产 (如 "USDT")

	// ===== 成交信息 =====
	Price    int64 // 成交价格 (精度 Precision)
	Quantity int64 // 成交数量 (精度 Precision)

	// ===== 手续费 =====
	BuyerFee       int64  // 买方手续费
	BuyerFeeAsset  string // 买方手续费资产 (通常是 BaseAsset)
	SellerFee      int64  // 卖方手续费
	SellerFeeAsset string // 卖方手续费资产 (通常是 QuoteAsset)

	// ===== 时间戳 =====
	Timestamp int64 // 成交时间
}
