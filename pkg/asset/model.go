// 文件: pkg/asset/model.go
// 热钱包账户引擎 - 核心数据模型
//
// 设计目标:
// 1. 零内存分配: 使用值类型和预分配，避免 GC 压力
// 2. 缓存友好: 字段按大小排列，减少 padding
// 3. 并发安全: 通过单线程分片 + 原子快照实现

package asset

import (
	"sync/atomic"
	"time"
)

// =============================================================================
// 精度常量
// =============================================================================

const (
	// Precision 精度因子
	// 所有金额存储为 int64，乘以 10^8 (类似比特币的 satoshi)
	// 1 BTC = 100,000,000 satoshi
	// 1 USDT = 100,000,000 微单位
	Precision = 100_000_000

	// NumShards 分片数量
	// 按 userID % NumShards 路由，每个分片单线程处理
	// 设置为 8，可根据 CPU 核数调整
	NumShards = 8
)

// =============================================================================
// Asset - 单个资产余额
// =============================================================================

// Asset 表示用户持有的某一种资产的余额状态
//
// 设计说明:
// - Available: 可用余额，可以下单、提现
// - Locked: 锁定余额，已被挂单冻结，不能重复使用
// - 总资产 = Available + Locked
//
// 内存布局: 16 bytes (2 * int64)，无 padding
type Asset struct {
	Available int64 // 可用余额 (单位: 最小精度，如 satoshi)
	Locked    int64 // 锁定余额 (挂单冻结)
}

// Total 返回总资产 (可用 + 锁定)
func (a *Asset) Total() int64 {
	return a.Available + a.Locked
}

// Clone 创建副本 (用于快照)
func (a *Asset) Clone() Asset {
	return Asset{
		Available: a.Available,
		Locked:    a.Locked,
	}
}

// =============================================================================
// Position - 合约持仓 (Phase 2)
// =============================================================================

// Position 表示用户在某个合约上的持仓
//
// 设计说明:
// - Size > 0: 多头 (Long)
// - Size < 0: 空头 (Short)
// - EntryPrice: 使用加权平均计算
//
// 内存布局: 40 bytes
type Position struct {
	Symbol     string // 合约标识 (如 "BTC-PERP")
	Size       int64  // 持仓数量 (正=多, 负=空)
	EntryPrice int64  // 开仓均价 (精度 Precision)
	Margin     int64  // 占用保证金
	UpdatedAt  int64  // 最后更新时间 (unix nano)
}

// Clone 创建副本
func (p *Position) Clone() Position {
	return Position{
		Symbol:     p.Symbol,
		Size:       p.Size,
		EntryPrice: p.EntryPrice,
		Margin:     p.Margin,
		UpdatedAt:  p.UpdatedAt,
	}
}

// =============================================================================
// OptionPosition - 期权持仓 (Phase 3)
// =============================================================================

// OptionType 期权类型
type OptionType int8

const (
	OptionCall OptionType = iota + 1 // 看涨期权
	OptionPut                        // 看跌期权
)

// OptionSide 期权持仓方向
type OptionSide int8

const (
	OptionLong  OptionSide = iota + 1 // 买方 (支付权利金，拥有权利)
	OptionShort                       // 卖方 (收取权利金，承担义务)
)

// OptionPosition 期权持仓
//
// 设计说明:
// - 期权标识格式: "BTC-20240315-50000-C" (标的-到期日-行权价-类型)
// - Long: 买入期权，支付权利金，最大亏损 = 权利金
// - Short: 卖出期权，收取权利金，需要锁定保证金
//
// 期权与合约的区别:
// - 合约: 双方都有义务
// - 期权: 买方有权利，卖方有义务
type OptionPosition struct {
	// ===== 标识 =====
	Symbol     string // 期权标识 (如 "BTC-20240315-50000-C")
	Underlying string // 标的资产 (如 "BTC")

	// ===== 持仓 =====
	Side OptionSide // LONG (买方) / SHORT (卖方)
	Size int64      // 持仓数量 (张数)

	// ===== 合约规格 =====
	OptionType OptionType // CALL / PUT
	Strike     int64      // 行权价 (精度 Precision)
	Expiry     int64      // 到期时间 (unix timestamp)

	// ===== 成本 =====
	Premium int64 // 权利金成本 (Long 为负，Short 为正)
	Margin  int64 // 卖方锁定的保证金 (仅 Short 有值)

	// ===== 元信息 =====
	UpdatedAt int64 // 最后更新时间
}

// Clone 创建副本
func (o *OptionPosition) Clone() OptionPosition {
	return OptionPosition{
		Symbol:     o.Symbol,
		Underlying: o.Underlying,
		Side:       o.Side,
		Size:       o.Size,
		OptionType: o.OptionType,
		Strike:     o.Strike,
		Expiry:     o.Expiry,
		Premium:    o.Premium,
		Margin:     o.Margin,
		UpdatedAt:  o.UpdatedAt,
	}
}

// IsExpired 判断期权是否已到期
func (o *OptionPosition) IsExpired(now int64) bool {
	return now >= o.Expiry
}

// =============================================================================
// UserState - 用户完整状态
// =============================================================================

// UserState 用户在热钱包中的完整状态
//
// 设计说明:
// 1. 这是热端的"真相"，所有交易操作都在这里完成
// 2. 每个用户的状态由其所在分片单线程管理，无锁
// 3. 快照通过深拷贝生成，供风控/强平无锁读取
//
// 生命周期:
// - 加载: 冷用户首次交易时从 DB 加载
// - 活跃: 有挂单/持仓时常驻内存
// - 驱逐: 无挂单且长时间不活跃时移出内存
type UserState struct {
	// ===== 身份 =====
	UserID int64

	// ===== 现货资产 =====
	// Key = 资产符号 (如 "BTC", "USDT")
	// Value = 余额状态
	Assets map[string]*Asset

	// ===== 合约持仓 (Phase 2) =====
	// Key = 合约符号 (如 "BTC-PERP")
	// Value = 持仓状态
	Positions map[string]*Position

	// ===== 期权持仓 (Phase 3) =====
	// Key = 期权标识 (如 "BTC-20240315-50000-C")
	// Value = 持仓状态
	Options map[string]*OptionPosition

	// ===== 元信息 =====
	LastSeq      uint64 // 最后应用的 WAL 序列号 (用于恢复/幂等)
	LastActiveAt int64  // 最后活跃时间 (unix nano)

	// ===== 状态标记 =====
	OpenOrderCount int32 // 当前挂单数量 (>0 时不可驱逐)
	Pinned         bool  // 是否固定在热端 (有持仓/借贷时为 true)
}

// NewUserState 创建新用户状态
func NewUserState(userID int64) *UserState {
	return &UserState{
		UserID:       userID,
		Assets:       make(map[string]*Asset),
		Positions:    make(map[string]*Position),
		Options:      make(map[string]*OptionPosition),
		LastActiveAt: time.Now().UnixNano(),
	}
}

// GetAsset 获取指定资产 (不存在则返回零值)
// 注意: 这是内部方法，调用者需确保在分片线程内
func (u *UserState) GetAsset(symbol string) *Asset {
	if asset, ok := u.Assets[symbol]; ok {
		return asset
	}
	// 懒初始化
	asset := &Asset{}
	u.Assets[symbol] = asset
	return asset
}

// GetAvailable 获取可用余额 (线程安全的快捷方法)
func (u *UserState) GetAvailable(symbol string) int64 {
	if asset, ok := u.Assets[symbol]; ok {
		return asset.Available
	}
	return 0
}

// CanBeEvicted 判断用户是否可以从热端驱逐
// 返回 false 的条件:
// 1. 有未完成的挂单
// 2. 有合约持仓
// 3. 有锁定资产
// 4. 被标记为 Pinned
func (u *UserState) CanBeEvicted() bool {
	if u.Pinned || u.OpenOrderCount > 0 || len(u.Positions) > 0 {
		return false
	}
	// 有期权持仓不能驱逐
	if u.Pinned || u.OpenOrderCount > 0 || len(u.Positions) > 0 || len(u.Options) > 0 {
		return false
	}

	// 检查是否有锁定资产
	for _, asset := range u.Assets {
		if asset.Locked > 0 {
			return false
		}
	}
	return true
}

// =============================================================================
// Snapshot - 原子快照 (供风控/强平无锁读)
// =============================================================================

// Snapshot 用户状态的只读快照
//
// 设计说明:
// 1. 使用值类型 (非指针)，确保快照不可变
// 2. 通过 atomic.Pointer 发布，风控可无锁读取
// 3. 生成快照时进行深拷贝，不影响热端操作
type Snapshot struct {
	UserID    int64
	Assets    map[string]Asset          // 值拷贝
	Positions map[string]Position       // 值拷贝
	Options   map[string]OptionPosition // 新增
	Seq       uint64                    // 快照对应的 WAL 序列号
	CreatedAt int64                     // 快照生成时间
}

// CreateSnapshot 从 UserState 生成只读快照
func (u *UserState) CreateSnapshot() *Snapshot {
	snap := &Snapshot{
		UserID:    u.UserID,
		Assets:    make(map[string]Asset, len(u.Assets)),
		Positions: make(map[string]Position, len(u.Positions)),
		Seq:       u.LastSeq,
		CreatedAt: time.Now().UnixNano(),
	}

	// 深拷贝资产
	for symbol, asset := range u.Assets {
		snap.Assets[symbol] = asset.Clone()
	}

	// 深拷贝合约
	for symbol, pos := range u.Positions {
		snap.Positions[symbol] = pos.Clone()
	}
	// 深拷贝期权
	for symbol, opt := range u.Options {
		snap.Options[symbol] = opt.Clone()
	}

	return snap
}

// =============================================================================
// SnapshotStore - 快照存储 (原子发布)
// =============================================================================

// SnapshotStore 存储所有用户快照，支持无锁读取
//
// 设计说明:
// 使用 atomic.Pointer 实现无锁读:
// - 写入时: 创建新 map，原子替换指针
// - 读取时: 直接读指针，无锁
type SnapshotStore struct {
	snapshots atomic.Pointer[map[int64]*Snapshot]
}

// NewSnapshotStore 创建快照存储
func NewSnapshotStore() *SnapshotStore {
	store := &SnapshotStore{}
	empty := make(map[int64]*Snapshot)
	store.snapshots.Store(&empty)
	return store
}

// Get 获取用户快照 (无锁)
func (s *SnapshotStore) Get(userID int64) *Snapshot {
	m := s.snapshots.Load()
	if m == nil {
		return nil
	}
	return (*m)[userID]
}

// Update 更新快照 (仅由分片线程调用)
// 使用 Copy-on-Write 策略
func (s *SnapshotStore) Update(snap *Snapshot) {
	for {
		old := s.snapshots.Load()
		// 创建新 map (浅拷贝 + 更新)
		newMap := make(map[int64]*Snapshot, len(*old)+1)
		for k, v := range *old {
			newMap[k] = v
		}
		newMap[snap.UserID] = snap

		// 原子替换
		if s.snapshots.CompareAndSwap(old, &newMap) {
			return
		}
	}
}
