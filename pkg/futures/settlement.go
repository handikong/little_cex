// 文件: pkg/futures/settlement.go
// 合约交割服务
//
// 【核心职责】
// 1. 定时扫描到期合约
// 2. 按结算价平掉所有持仓
// 3. 结算盈亏到用户账户
//
// 【交割流程】
// 1. 到期前 1 小时: 禁止开新仓 (只能平仓)
// 2. 到期时刻: 停止交易，状态 -> SETTLING
// 3. 获取结算价 (通常是最后 1 小时均价)
// 4. 遍历所有持仓，计算盈亏并结算
// 5. 状态 -> SETTLED
//
// 【面试考点】
// Q: 为什么交割要停止交易？
// A: 防止结算过程中价格波动导致计算混乱
//
// Q: 结算价为什么用均价而不是最新价？
// A: 防止操纵价格套利，均价更难被操控

package futures

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"max.com/pkg/fund"
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrContractNotExpired   = errors.New("contract has not expired yet")
	ErrContractNotSettling  = errors.New("contract is not in settling status")
	ErrNoPositionsToSettle  = errors.New("no positions to settle")
	ErrSettlementInProgress = errors.New("settlement already in progress")
)

// =============================================================================
// 交割配置
// =============================================================================

type SettlementConfig struct {
	// ScanInterval 扫描到期合约的间隔
	ScanInterval time.Duration

	// PreSettleWindow 提前多久禁止开仓 (只允许平仓)
	// 例: 1小时，即到期前1小时禁止开新仓
	PreSettleWindow time.Duration

	// SettlementTimeout 单次交割的超时时间
	SettlementTimeout time.Duration

	// BatchSize 每批处理的持仓数量
	// 大合约可能有几万个持仓，需要分批处理
	BatchSize int

	// WorkerCount 并行处理的 worker 数量
	WorkerCount int
}

func DefaultSettlementConfig() *SettlementConfig {
	return &SettlementConfig{
		ScanInterval:      time.Minute,      // 每分钟扫描
		PreSettleWindow:   time.Hour,        // 提前1小时禁止开仓
		SettlementTimeout: 30 * time.Minute, // 交割超时30分钟
		BatchSize:         1000,             // 每批1000个持仓
		WorkerCount:       4,                // 4个worker并行
	}
}

// =============================================================================
// SettlementEngine - 交割引擎
// =============================================================================

// SettlementEngine 交割引擎
//
// 【设计说明】
// 1. 单独的服务，不与撮合引擎混在一起
// 2. 定时扫描到期合约，自动触发交割
// 3. 支持手动触发交割 (运维用)
type SettlementEngine struct {
	config           *SettlementConfig
	contractManager  *ContractManager
	positionRepo     PositionRepository
	balanceRepo      *fund.BalanceRepo
	markPriceService *MarkPriceService

	// 状态
	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup

	// 防止同一合约并发交割
	settlingContracts sync.Map // symbol -> bool
}

func NewSettlementEngine(
	config *SettlementConfig,
	contractManager *ContractManager,
	positionRepo PositionRepository,
	balanceRepo *fund.BalanceRepo,
	markPriceService *MarkPriceService,
) *SettlementEngine {
	if config == nil {
		config = DefaultSettlementConfig()
	}
	return &SettlementEngine{
		config:           config,
		contractManager:  contractManager,
		positionRepo:     positionRepo,
		balanceRepo:      balanceRepo,
		markPriceService: markPriceService,
		stopChan:         make(chan struct{}),
	}
}

// =============================================================================
// 生命周期
// =============================================================================

// Start 启动交割引擎
func (e *SettlementEngine) Start() error {
	if e.running {
		return errors.New("settlement engine already running")
	}

	e.running = true
	e.wg.Add(1)
	go e.scanLoop()

	log.Println("[Settlement] Engine started")
	return nil
}

// Stop 停止交割引擎
func (e *SettlementEngine) Stop() {
	if !e.running {
		return
	}

	close(e.stopChan)
	e.wg.Wait()
	e.running = false

	log.Println("[Settlement] Engine stopped")
}

// =============================================================================
// 定时扫描
// =============================================================================

// scanLoop 扫描循环
//
// 【设计】
// 定时扫描所有交割合约，检查是否到期
// 到期的自动触发交割流程
func (e *SettlementEngine) scanLoop() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopChan:
			return
		case <-ticker.C:
			e.scanExpiredContracts()
		}
	}
}

// scanExpiredContracts 扫描到期合约
func (e *SettlementEngine) scanExpiredContracts() {
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// 获取所有交易中的合约
	contracts, err := e.contractManager.GetTradingContracts(ctx)
	if err != nil {
		log.Printf("[Settlement] Failed to get contracts: %v", err)
		return
	}

	for _, spec := range contracts {
		// 只处理交割合约
		if spec.ContractType != TypeDelivery {
			continue
		}

		// 检查是否到期
		if spec.IsExpired(now) {
			log.Printf("[Settlement] Contract %s expired, starting settlement", spec.Symbol)
			go e.settleContract(ctx, spec.Symbol)
		}
	}
}

// =============================================================================
// 交割执行
// =============================================================================

// SettleContract 手动触发合约交割 (公开方法)
func (e *SettlementEngine) SettleContract(ctx context.Context, symbol string) error {
	return e.settleContract(ctx, symbol)
}

// settleContract 执行合约交割
//
// 【核心流程】
// 1. 状态检查: 合约必须是 TRADING 且已到期
// 2. 锁定合约: 防止并发交割
// 3. 停止交易: 状态 -> SETTLING
// 4. 获取结算价
// 5. 分批处理持仓
// 6. 完成交割: 状态 -> SETTLED
func (e *SettlementEngine) settleContract(ctx context.Context, symbol string) error {
	// 1. 检查是否已在交割中
	if _, loaded := e.settlingContracts.LoadOrStore(symbol, true); loaded {
		return ErrSettlementInProgress
	}
	defer e.settlingContracts.Delete(symbol)

	// 2. 获取合约规格
	spec, err := e.contractManager.GetContract(ctx, symbol)
	if err != nil {
		return err
	}

	// 3. 检查合约状态
	now := time.Now().UnixMilli()
	if !spec.IsExpired(now) {
		return ErrContractNotExpired
	}

	// 4. 切换状态: TRADING -> SETTLING
	if spec.Status == StatusTrading {
		if err := e.contractManager.StartSettlement(ctx, symbol); err != nil {
			return err
		}
		log.Printf("[Settlement] %s status changed to SETTLING", symbol)
	} else if spec.Status != StatusSettling {
		return ErrContractNotSettling
	}

	// 5. 获取结算价
	// 【重要】结算价通常是到期前1小时的TWAP (Time-Weighted Average Price)
	// 这里简化为使用当前标记价格
	settlementPrice := e.getSettlementPrice(symbol)
	if settlementPrice <= 0 {
		log.Printf("[Settlement] %s: no settlement price available", symbol)
		return errors.New("no settlement price")
	}
	log.Printf("[Settlement] %s settlement price: %d", symbol, settlementPrice)

	// 6. 批量结算所有持仓
	if err := e.settleAllPositions(ctx, spec, settlementPrice); err != nil {
		log.Printf("[Settlement] %s failed: %v", symbol, err)
		return err
	}

	// 7. 切换状态: SETTLING -> SETTLED
	if err := e.contractManager.FinishSettlement(ctx, symbol); err != nil {
		return err
	}

	log.Printf("[Settlement] %s completed successfully", symbol)
	return nil
}

// getSettlementPrice 获取结算价
//
// 【生产环境】
// 应该使用 TWAP (时间加权平均价格):
//
//	settlementPrice = sum(price_i * time_i) / sum(time_i)
//
// 通常取到期前 1 小时内的所有成交价加权平均
// 这样可以防止结算时刻被操纵价格
func (e *SettlementEngine) getSettlementPrice(symbol string) int64 {
	// 简化实现: 使用当前标记价格
	// TODO: 实现 TWAP 计算
	return e.markPriceService.GetMarkPrice(symbol)
}

// =============================================================================
// 持仓结算
// =============================================================================

// settleAllPositions 结算所有持仓
//
// 【设计】分批处理，避免一次性加载太多数据
func (e *SettlementEngine) settleAllPositions(
	ctx context.Context,
	spec *ContractSpec,
	settlementPrice int64,
) error {
	var offset int
	totalSettled := 0

	for {
		// 分批获取持仓
		positions, err := e.positionRepo.ListBySymbol(ctx, spec.Symbol, e.config.BatchSize, offset)
		if err != nil {
			return err
		}

		if len(positions) == 0 {
			break
		}

		// 并行处理这一批
		settled, err := e.settleBatch(ctx, spec, positions, settlementPrice)
		if err != nil {
			return err
		}

		totalSettled += settled
		offset += len(positions)

		log.Printf("[Settlement] %s: settled %d positions, total %d",
			spec.Symbol, len(positions), totalSettled)
	}

	log.Printf("[Settlement] %s: all %d positions settled", spec.Symbol, totalSettled)
	return nil
}

// settleBatch 结算一批持仓
func (e *SettlementEngine) settleBatch(
	ctx context.Context,
	spec *ContractSpec,
	positions []*Position,
	settlementPrice int64,
) (int, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errors []error
	settled := 0

	// 使用信号量限制并发
	sem := make(chan struct{}, e.config.WorkerCount)

	for _, pos := range positions {
		if pos.Size == 0 {
			continue // 跳过空仓位
		}

		wg.Add(1)
		sem <- struct{}{} // 获取信号量

		go func(p *Position) {
			defer wg.Done()
			defer func() { <-sem }() // 释放信号量

			err := e.settlePosition(ctx, spec, p, settlementPrice)

			mu.Lock()
			if err != nil {
				errors = append(errors, err)
			} else {
				settled++
			}
			mu.Unlock()
		}(pos)
	}

	wg.Wait()

	if len(errors) > 0 {
		return settled, errors[0]
	}
	return settled, nil
}

// settlePosition 结算单个持仓
//
// 【核心逻辑】
// 1. 计算盈亏: PnL = (结算价 - 开仓价) × 持仓量 × 方向
// 2. 释放保证金: 返还到用户可用余额
// 3. 结算盈亏: 盈利加到余额，亏损从余额扣除
// 4. 清空持仓: Size = 0, Margin = 0
// 5. 记录交割流水
func (e *SettlementEngine) settlePosition(
	ctx context.Context,
	spec *ContractSpec,
	pos *Position,
	settlementPrice int64,
) error {
	// 1. 计算盈亏
	// 多头: PnL = (结算价 - 开仓价) × 数量
	// 空头: PnL = (开仓价 - 结算价) × 数量 = -(结算价 - 开仓价) × (-数量)
	// 统一公式: PnL = (结算价 - 开仓价) × Size / Precision
	pnl := (settlementPrice - pos.EntryPrice) * pos.Size / Precision

	// 2. 结算金额 = 保证金 + 盈亏
	// 如果亏损超过保证金，结算金额可能为负 (穿仓)
	settlementAmount := pos.Margin + pnl
	if settlementAmount < 0 {
		// 穿仓情况: 用户亏得比保证金还多
		// 生产环境应该从保险基金扣除
		log.Printf("[Settlement] WARNING: user %d position %s has negative settlement: %d (穿仓)",
			pos.UserID, spec.Symbol, settlementAmount)
		settlementAmount = 0 // 最多亏光保证金
	}

	// 3. 更新用户余额
	// 释放保证金 + 结算盈亏 = 直接增加可用余额
	if settlementAmount > 0 {
		err := e.balanceRepo.AddAvailable(ctx, pos.UserID, spec.SettleCurrency, settlementAmount)
		if err != nil {
			return err
		}
	}

	// 4. 更新持仓 (记录已实现盈亏，清空持仓)
	pos.RealizedPnL += pnl
	pos.Size = 0
	pos.Margin = 0
	pos.UpdatedAt = time.Now().UnixMilli()

	if err := e.positionRepo.Save(ctx, pos); err != nil {
		return err
	}

	log.Printf("[Settlement] User %d position %s settled: PnL=%d, Amount=%d",
		pos.UserID, spec.Symbol, pnl, settlementAmount)

	return nil
}

// =============================================================================
// 查询接口
// =============================================================================

// IsSettling 合约是否正在交割中
func (e *SettlementEngine) IsSettling(symbol string) bool {
	_, ok := e.settlingContracts.Load(symbol)
	return ok
}

// CanOpenPosition 是否允许开仓
//
// 【规则】
// 1. 永续合约: 随时可以开仓
// 2. 交割合约: 到期前 PreSettleWindow 内禁止开仓
func (e *SettlementEngine) CanOpenPosition(ctx context.Context, symbol string) (bool, error) {
	spec, err := e.contractManager.GetContract(ctx, symbol)
	if err != nil {
		return false, err
	}

	// 永续合约
	if spec.ContractType == TypePerpetual {
		return spec.IsTrading(), nil
	}

	// 交割合约: 检查是否在禁止开仓窗口
	if spec.ExpiryAt > 0 {
		now := time.Now().UnixMilli()
		preSettleTime := spec.ExpiryAt - e.config.PreSettleWindow.Milliseconds()

		if now >= preSettleTime {
			return false, nil // 禁止开仓
		}
	}

	return spec.IsTrading(), nil
}
