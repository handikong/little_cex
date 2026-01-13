// 文件: pkg/asset/shard.go
// 热钱包账户引擎 - 单分片处理器
//
// 核心设计:
// 1. 单线程模型: 每个分片由一个 goroutine 独占处理，避免锁竞争
// 2. 命令队列: 所有操作通过 Channel 串行执行
// 3. 原子快照: 操作完成后发布快照供风控无锁读取
//
// 为什么用单线程?
// - 金融系统对一致性要求极高，锁容易出错
// - 单线程消除了竞态条件，代码更简单可靠
// - 通过分片 (8个) 实现并行，总吞吐量 = 8 * 单分片吞吐量

package asset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrInsufficientBalance = errors.New("insufficient available balance")
	ErrInsufficientLocked  = errors.New("insufficient locked balance")
	ErrUserNotFound        = errors.New("user not found in hot cache")
	ErrShardClosed         = errors.New("shard is closed")
	ErrCommandTimeout      = errors.New("command timeout")
	ErrDuplicateCommand    = errors.New("duplicate command (idempotency)")
)

// =============================================================================
// 命令类型
// =============================================================================

// CmdType 命令类型
type CmdType uint8

const (
	CmdReserve       CmdType = iota + 1 // 冻结 (下单)
	CmdRelease                          // 解冻 (撤单)
	CmdTransfer                         // 划转 (成交结算)
	CmdAddBalance                       // 增加余额 (充值确认后)
	CmdDeductBalance                    // 扣减余额 (提现确认后)
)

// Command 命令结构
//
// 所有资金操作都封装为 Command，通过 Channel 发送给分片处理
// 这是"命令模式"的应用，便于:
// 1. 序列化到 WAL
// 2. 幂等性检查 (通过 CmdID)
// 3. 异步执行 + 结果回传
type Command struct {
	Type   CmdType // 命令类型
	CmdID  string  // 幂等键 (如 order_id, trade_id)
	UserID int64   // 目标用户

	// 操作参数
	Symbol string // 资产符号 (如 "USDT")
	Amount int64  // 金额

	// Transfer 专用 (成交时需要两方)
	ToUserID int64  // 接收方用户
	ToSymbol string // 接收资产 (如 "BTC")
	ToAmount int64  // 接收金额
	Fee      int64  // 手续费
	FeeAsset string // 手续费资产

	// 结果回传
	Result chan error
}

// =============================================================================
// Shard - 单分片处理器
// =============================================================================

// Shard 单分片处理器
//
// 职责:
// 1. 管理该分片下所有用户的 UserState
// 2. 单线程处理所有命令 (Reserve/Release/Transfer...)
// 3. 维护幂等性检查 (已处理的 CmdID)
// 4. 发布快照供风控读取
//
// 内存结构:
// - users: 热用户状态 map
// - appliedCmds: 已应用命令 (用于幂等检查)
// - cmdCh: 命令队列
type Shard struct {
	id int // 分片编号 (0 ~ NumShards-1)

	// ===== 用户状态 =====
	users map[int64]*UserState // UserID -> State

	// ===== 幂等性 =====
	// 存储最近已处理的 CmdID，防止重复执行
	// 使用 LRU 或定时清理，避免无限增长
	appliedCmds map[string]struct{}

	// ===== 命令队列 =====
	cmdCh chan Command

	// ===== 快照存储 =====
	// 由 Engine 统一管理，分片只负责更新
	snapshotStore *SnapshotStore

	// ===== 生命周期 =====
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// ===== 统计 =====
	stats ShardStats
	// ===== WAL =====
	wal *WAL // 可选，启用时会先写 WAL

}

// ShardStats 分片统计信息 (监控用)
type ShardStats struct {
	TotalCommands   uint64 // 处理的命令总数
	ReserveCount    uint64 // 冻结次数
	ReleaseCount    uint64 // 解冻次数
	TransferCount   uint64 // 划转次数
	RejectCount     uint64 // 拒绝次数 (余额不足等)
	DuplicateCount  uint64 // 重复命令次数
	ActiveUserCount int    // 活跃用户数
}

// ShardConfig 分片配置
type ShardConfig struct {
	ID              int            // 分片编号
	CommandQueueLen int            // 命令队列长度
	SnapshotStore   *SnapshotStore // 快照存储 (共享)
	WAL             *WAL           // 可选

}

// =============================================================================
// 分片生命周期
// =============================================================================

// NewShard 创建分片
func NewShard(cfg ShardConfig) *Shard {
	ctx, cancel := context.WithCancel(context.Background())

	queueLen := cfg.CommandQueueLen
	if queueLen <= 0 {
		queueLen = 10000 // 默认队列长度
	}

	return &Shard{
		id:            cfg.ID,
		users:         make(map[int64]*UserState),
		appliedCmds:   make(map[string]struct{}),
		cmdCh:         make(chan Command, queueLen),
		snapshotStore: cfg.SnapshotStore,
		ctx:           ctx,
		cancel:        cancel,
		wal:           cfg.WAL, // 添加这行

	}
}

// Start 启动分片处理循环
func (s *Shard) Start() {
	s.wg.Add(1)
	go s.processLoop()
}

// Stop 停止分片
func (s *Shard) Stop() {
	s.cancel()
	s.wg.Wait()
}

// processLoop 命令处理主循环 (单线程)
//
// 这是分片的核心:
// - 从 cmdCh 取命令
// - 执行命令 (修改 UserState)
// - 返回结果
// - 更新快照
//
// 因为是单线程，所有操作都是原子的，无需加锁
func (s *Shard) processLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			// 优雅关闭：处理完队列中剩余命令
			s.drainQueue()
			return

		case cmd := <-s.cmdCh:
			s.handleCommand(cmd)
		}
	}
}

// drainQueue 关闭时处理剩余命令
func (s *Shard) drainQueue() {
	for {
		select {
		case cmd := <-s.cmdCh:
			s.handleCommand(cmd)
		default:
			return
		}
	}
}

// =============================================================================
// 命令处理
// =============================================================================

// handleCommand 处理单个命令
func (s *Shard) handleCommand(cmd Command) {
	s.stats.TotalCommands++

	// 1. 幂等性检查
	if cmd.CmdID != "" {
		if _, exists := s.appliedCmds[cmd.CmdID]; exists {
			s.stats.DuplicateCount++
			s.sendResult(cmd, ErrDuplicateCommand)
			return
		}
	}
	// 2. 【新增】先写 WAL
	if s.wal != nil {
		entry := s.cmdToWALEntry(cmd)
		if err := s.wal.Write(entry); err != nil {
			s.sendResult(cmd, fmt.Errorf("wal write: %w", err))
			return
		}
	}

	// 2. 执行命令
	var err error
	switch cmd.Type {
	case CmdReserve:
		err = s.doReserve(cmd)
		if err == nil {
			s.stats.ReserveCount++
		}
	case CmdRelease:
		err = s.doRelease(cmd)
		if err == nil {
			s.stats.ReleaseCount++
		}
	case CmdTransfer:
		err = s.doTransfer(cmd)
		if err == nil {
			s.stats.TransferCount++
		}
	case CmdAddBalance:
		err = s.doAddBalance(cmd)
	case CmdDeductBalance:
		err = s.doDeductBalance(cmd)
	}

	if err != nil {
		s.stats.RejectCount++
	}

	// 3. 记录幂等键
	if err == nil && cmd.CmdID != "" {
		s.appliedCmds[cmd.CmdID] = struct{}{}
	}

	// 4. 返回结果
	s.sendResult(cmd, err)

	// 5. 更新快照 (可选: 批量更新以提高性能)
	if err == nil {
		s.updateSnapshot(cmd.UserID)
	}
}

// cmdToWALEntry 将命令转换为 WAL 条目
func (s *Shard) cmdToWALEntry(cmd Command) *WALEntry {
	var entryType WALEntryType
	switch cmd.Type {
	case CmdReserve:
		entryType = WALReserve
	case CmdRelease:
		entryType = WALRelease
	case CmdTransfer:
		entryType = WALTransfer
	case CmdAddBalance:
		entryType = WALAddBalance
	case CmdDeductBalance:
		entryType = WALDeductBalance
	}

	return &WALEntry{
		Type:     entryType,
		CmdID:    cmd.CmdID,
		UserID:   cmd.UserID,
		Symbol:   cmd.Symbol,
		Amount:   cmd.Amount,
		ToUserID: cmd.ToUserID,
		ToSymbol: cmd.ToSymbol,
		ToAmount: cmd.ToAmount,
		Fee:      cmd.Fee,
		FeeAsset: cmd.FeeAsset,
	}
}

// sendResult 发送命令结果
func (s *Shard) sendResult(cmd Command, err error) {
	if cmd.Result != nil {
		select {
		case cmd.Result <- err:
		default:
			// 调用方未等待结果，丢弃
		}
	}
}

// updateSnapshot 更新用户快照
func (s *Shard) updateSnapshot(userID int64) {
	if s.snapshotStore == nil {
		return
	}
	user, ok := s.users[userID]
	if !ok {
		return
	}
	snap := user.CreateSnapshot()
	s.snapshotStore.Update(snap)
}

// RecoverFromWAL 从 WAL 恢复状态
func (s *Shard) RecoverFromWAL() (uint64, error) {
	if s.wal == nil {
		return 0, nil
	}

	return s.wal.Recover(func(entry *WALEntry) error {
		// 将 WAL 条目转换回命令并重放
		cmd := s.walEntryToCmd(entry)

		// 跳过幂等检查，直接执行
		var err error
		switch cmd.Type {
		case CmdReserve:
			err = s.doReserve(cmd)
		case CmdRelease:
			err = s.doRelease(cmd)
		case CmdTransfer:
			err = s.doTransfer(cmd)
		case CmdAddBalance:
			err = s.doAddBalance(cmd)
		case CmdDeductBalance:
			err = s.doDeductBalance(cmd)
		}

		// 记录幂等键
		if err == nil && cmd.CmdID != "" {
			s.appliedCmds[cmd.CmdID] = struct{}{}
		}

		return err
	})
}

// walEntryToCmd 将 WAL 条目转换回命令
func (s *Shard) walEntryToCmd(entry *WALEntry) Command {
	var cmdType CmdType
	switch entry.Type {
	case WALReserve:
		cmdType = CmdReserve
	case WALRelease:
		cmdType = CmdRelease
	case WALTransfer:
		cmdType = CmdTransfer
	case WALAddBalance:
		cmdType = CmdAddBalance
	case WALDeductBalance:
		cmdType = CmdDeductBalance
	}

	return Command{
		Type:     cmdType,
		CmdID:    entry.CmdID,
		UserID:   entry.UserID,
		Symbol:   entry.Symbol,
		Amount:   entry.Amount,
		ToUserID: entry.ToUserID,
		ToSymbol: entry.ToSymbol,
		ToAmount: entry.ToAmount,
		Fee:      entry.Fee,
		FeeAsset: entry.FeeAsset,
	}
}

// SerializeState 序列化分片状态 (用于检查点)
func (s *Shard) SerializeState() ([]byte, error) {
	// 简单实现: 使用 JSON
	// 生产环境可用 protobuf 或自定义二进制格式
	return json.Marshal(s.users)
}

// DeserializeState 反序列化分片状态
func (s *Shard) DeserializeState(data []byte) error {
	return json.Unmarshal(data, &s.users)
}

// CreateCheckpoint 创建检查点
func (s *Shard) CreateCheckpoint() error {
	if s.wal == nil {
		return nil
	}

	data, err := s.SerializeState()
	if err != nil {
		return err
	}

	return s.wal.Checkpoint(data, s.wal.GetSequence())
}

// RecoverFromCheckpoint 从检查点恢复
func (s *Shard) RecoverFromCheckpoint() error {
	if s.wal == nil {
		return nil
	}

	// 1. 加载快照
	data, checkpointSeq, err := s.wal.LoadSnapshot()
	if err != nil {
		return err
	}

	if data != nil {
		if err := s.DeserializeState(data); err != nil {
			return err
		}
	}

	// 2. 重放检查点之后的 WAL
	_, err = s.wal.Recover(func(entry *WALEntry) error {
		if entry.Seq <= checkpointSeq {
			return nil // 跳过已包含在快照中的条目
		}

		cmd := s.walEntryToCmd(entry)

		// 执行命令
		switch cmd.Type {
		case CmdReserve:
			return s.doReserve(cmd)
		case CmdRelease:
			return s.doRelease(cmd)
		case CmdTransfer:
			return s.doTransfer(cmd)
		case CmdAddBalance:
			return s.doAddBalance(cmd)
		case CmdDeductBalance:
			return s.doDeductBalance(cmd)
		}
		return nil
	})
	return err
}

// =============================================================================
// 核心操作实现
// =============================================================================

// doReserve 冻结操作 (下单时调用)
//
// 流程:
// 1. 检查用户是否存在
// 2. 检查可用余额是否充足
// 3. Available -= amount, Locked += amount
func (s *Shard) doReserve(cmd Command) error {
	user := s.getOrCreateUser(cmd.UserID)
	asset := user.GetAsset(cmd.Symbol)

	// 余额检查
	if asset.Available < cmd.Amount {
		return ErrInsufficientBalance
	}

	// 冻结
	asset.Available -= cmd.Amount
	asset.Locked += cmd.Amount

	// 更新挂单计数
	user.OpenOrderCount++
	user.LastActiveAt = time.Now().UnixNano()

	return nil
}

// doRelease 解冻操作 (撤单时调用)
//
// 流程:
// 1. 检查冻结余额是否充足
// 2. Locked -= amount, Available += amount
func (s *Shard) doRelease(cmd Command) error {
	user, ok := s.users[cmd.UserID]
	if !ok {
		return ErrUserNotFound
	}

	asset := user.GetAsset(cmd.Symbol)

	// 冻结余额检查
	if asset.Locked < cmd.Amount {
		return ErrInsufficientLocked
	}

	// 解冻
	asset.Locked -= cmd.Amount
	asset.Available += cmd.Amount

	// 更新挂单计数
	if user.OpenOrderCount > 0 {
		user.OpenOrderCount--
	}
	user.LastActiveAt = time.Now().UnixNano()

	return nil
}

// doTransfer 划转操作 (成交结算时调用)
//
// 现货成交场景:
// - 买方: 扣 USDT (Locked), 加 BTC (Available)
// - 卖方: 扣 BTC (Locked), 加 USDT (Available)
// - 双方各扣手续费
//
// 参数说明:
// - UserID/Symbol/Amount: 支付方 (扣款)
// - ToUserID/ToSymbol/ToAmount: 接收方 (加款)
// - Fee/FeeAsset: 手续费扣除
func (s *Shard) doTransfer(cmd Command) error {
	// 获取支付方
	payer, ok := s.users[cmd.UserID]
	if !ok {
		return ErrUserNotFound
	}

	payerAsset := payer.GetAsset(cmd.Symbol)

	// 检查支付方冻结余额
	if payerAsset.Locked < cmd.Amount {
		return ErrInsufficientLocked
	}

	// 扣除支付方
	payerAsset.Locked -= cmd.Amount

	// 扣除手续费 (从可用余额扣)
	if cmd.Fee > 0 && cmd.FeeAsset != "" {
		feeAsset := payer.GetAsset(cmd.FeeAsset)
		if feeAsset.Available >= cmd.Fee {
			feeAsset.Available -= cmd.Fee
		}
		// 手续费不足时不阻止交易，记录日志即可
	}

	// 给接收方加款 (注意: 接收方可能在不同分片!)
	// 如果在同一分片，直接操作
	// 如果在不同分片，需要通过 Engine 路由
	receiver := s.getOrCreateUser(cmd.ToUserID)
	receiverAsset := receiver.GetAsset(cmd.ToSymbol)
	receiverAsset.Available += cmd.ToAmount

	// 更新活跃时间
	payer.LastActiveAt = time.Now().UnixNano()
	receiver.LastActiveAt = time.Now().UnixNano()

	// 更新接收方快照
	s.updateSnapshot(cmd.ToUserID)

	return nil
}

// doAddBalance 增加余额 (充值确认后调用)
// 资金服务监听到链上充值确认后，通过消息通知热钱包更新余额
func (s *Shard) doAddBalance(cmd Command) error {
	user := s.getOrCreateUser(cmd.UserID)
	asset := user.GetAsset(cmd.Symbol)
	asset.Available += cmd.Amount
	user.LastActiveAt = time.Now().UnixNano()
	return nil
}

// doDeductBalance 扣减余额 (提现确认后调用)
// 资金服务执行提现后，通过消息通知热钱包更新余额
func (s *Shard) doDeductBalance(cmd Command) error {
	user, ok := s.users[cmd.UserID]
	if !ok {
		return ErrUserNotFound
	}

	asset := user.GetAsset(cmd.Symbol)
	if asset.Available < cmd.Amount {
		return ErrInsufficientBalance
	}

	asset.Available -= cmd.Amount
	user.LastActiveAt = time.Now().UnixNano()
	return nil
}

// =============================================================================
// 辅助方法
// =============================================================================

// getOrCreateUser 获取或创建用户 (懒加载)
func (s *Shard) getOrCreateUser(userID int64) *UserState {
	if user, ok := s.users[userID]; ok {
		return user
	}
	user := NewUserState(userID)
	s.users[userID] = user
	s.stats.ActiveUserCount++
	return user
}

// GetUser 获取用户状态 (只读，用于外部查询)
func (s *Shard) GetUser(userID int64) *UserState {
	return s.users[userID]
}

// GetStats 获取统计信息
func (s *Shard) GetStats() ShardStats {
	stats := s.stats
	stats.ActiveUserCount = len(s.users)
	return stats
}

// Submit 提交命令到队列
//
// 这是外部调用的入口:
// 1. 创建 Command
// 2. 发送到 cmdCh
// 3. 等待结果 (可选)
//
// 参数 timeout: 等待结果的超时时间，0 表示不等待
func (s *Shard) Submit(cmd Command, timeout time.Duration) error {
	// 创建结果通道
	if timeout > 0 {
		cmd.Result = make(chan error, 1)
	}

	// 发送命令
	select {
	case s.cmdCh <- cmd:
		// 发送成功
	case <-s.ctx.Done():
		return ErrShardClosed
	default:
		// 队列满，可以选择阻塞或拒绝
		// 这里选择阻塞等待
		select {
		case s.cmdCh <- cmd:
		case <-s.ctx.Done():
			return ErrShardClosed
		}
	}

	// 等待结果
	if timeout > 0 {
		select {
		case err := <-cmd.Result:
			return err
		case <-time.After(timeout):
			return ErrCommandTimeout
		case <-s.ctx.Done():
			return ErrShardClosed
		}
	}

	return nil
}
