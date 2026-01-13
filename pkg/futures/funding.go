// 文件: pkg/futures/funding.go
// 资金费率服务
//
// 【核心公式】
// 资金费率 = Clamp(溢价指数 + Clamp(利率 - 溢价指数, -0.05%, 0.05%), -0.75%, 0.75%)
//
// 简化版 (大多数交易所用这个):
// 资金费率 = Clamp((合约价格 - 现货价格) / 现货价格, -0.75%, 0.75%)
//
// 【结算周期】
// 每 8 小时结算一次: 00:00, 08:00, 16:00 UTC
//
// 【资金费公式】
// 资金费 = 持仓价值 × 资金费率
// 持仓价值 = 持仓数量 × 标记价格
//
// 多头付 = 资金费率 > 0
// 空头付 = 资金费率 < 0

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
// 常量
// =============================================================================

const (
	// FundingInterval 资金费结算间隔 (8小时)
	FundingInterval = 8 * time.Hour

	// DefaultInterestRate 默认利率 (0.03% 每日，即 0.01% 每8小时)
	// 这是借贷市场的无风险利率
	DefaultInterestRate = 10 // 万分之一 = 0.01%

	// MaxFundingRate 最大资金费率 (±0.75%)
	MaxFundingRate = 75 // 万分之75 = 0.75%

	// 精度
	FundingPrecision = 10000 // 万分比
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrFundingInProgress = errors.New("funding settlement in progress")
	ErrNoFundingDue      = errors.New("no funding settlement due")
)

// =============================================================================
// FundingService - 资金费率服务
// =============================================================================

// FundingService 资金费率服务
//
// 【职责】
// 1. 计算实时资金费率
// 2. 定时结算资金费
// 3. 记录资金费流水
type FundingService struct {
	contractManager  *ContractManager
	positionRepo     PositionRepository
	balanceRepo      *fund.BalanceRepo
	markPriceService *MarkPriceService

	// 当前资金费率缓存
	// symbol -> FundingRate (万分比)
	fundingRates sync.Map

	// 下次结算时间
	// symbol -> nextFundingTime (Unix毫秒)
	nextFundingTime sync.Map

	// 结算锁 (防止同一合约并发结算)
	settlingSymbols sync.Map

	// 配置
	batchSize   int
	workerCount int

	// 控制
	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

func NewFundingService(
	contractManager *ContractManager,
	positionRepo PositionRepository,
	balanceRepo *fund.BalanceRepo,
	markPriceService *MarkPriceService,
) *FundingService {
	return &FundingService{
		contractManager:  contractManager,
		positionRepo:     positionRepo,
		balanceRepo:      balanceRepo,
		markPriceService: markPriceService,
		batchSize:        1000,
		workerCount:      4,
		stopChan:         make(chan struct{}),
	}
}

// =============================================================================
// 生命周期
// =============================================================================

// Start 启动资金费率服务
func (s *FundingService) Start() error {
	if s.running {
		return errors.New("funding service already running")
	}

	s.running = true

	// 1. 初始化下次结算时间
	s.initNextFundingTimes()

	// 2. 启动定时结算循环
	s.wg.Add(1)
	go s.settlementLoop()

	// 3. 启动费率计算循环
	s.wg.Add(1)
	go s.rateCalculationLoop()

	log.Println("[Funding] Service started")
	return nil
}

// Stop 停止服务
func (s *FundingService) Stop() {
	if !s.running {
		return
	}
	close(s.stopChan)
	s.wg.Wait()
	s.running = false
	log.Println("[Funding] Service stopped")
}

// =============================================================================
// 资金费率计算
// =============================================================================

// GetFundingRate 获取当前资金费率 (万分比)
func (s *FundingService) GetFundingRate(symbol string) int64 {
	if v, ok := s.fundingRates.Load(symbol); ok {
		return v.(int64)
	}
	return 0
}

// GetNextFundingTime 获取下次结算时间
func (s *FundingService) GetNextFundingTime(symbol string) int64 {
	if v, ok := s.nextFundingTime.Load(symbol); ok {
		return v.(int64)
	}
	return 0
}

// CalculateFundingRate 计算资金费率
//
// 【公式】
// 溢价指数 = (合约价格 - 现货价格) / 现货价格
// 资金费率 = Clamp(溢价指数, -MaxRate, MaxRate)
//
// 【面试考点】
// Q: 为什么要 Clamp 限制范围？
// A: 防止极端行情下资金费过高，导致用户仓位被大量扣款
func (s *FundingService) CalculateFundingRate(symbol string) int64 {
	// 1. 获取合约价格 (使用标记价格或订单簿中间价)
	contractPrice := s.markPriceService.GetMarkPrice(symbol)
	if contractPrice <= 0 {
		return 0
	}

	// 2. 获取现货价格 (指数价格)
	indexPrice := s.markPriceService.GetIndexPrice(symbol)
	if indexPrice <= 0 {
		return 0
	}

	// 3. 计算溢价指数
	// premiumIndex = (contractPrice - indexPrice) / indexPrice
	// 转换为万分比: premiumIndex * 10000
	premiumIndex := (contractPrice - indexPrice) * FundingPrecision / indexPrice

	// 4. 加上利率基差 (通常很小，可以忽略)
	// fundingRate = premiumIndex + (interestRate - premiumIndex) * dampening
	// 简化: fundingRate ≈ premiumIndex
	fundingRate := premiumIndex

	// 5. Clamp 到合理范围
	fundingRate = clamp(fundingRate, -MaxFundingRate, MaxFundingRate)

	return fundingRate
}

// clamp 限制值在 [min, max] 范围内
func clamp(value, min, max int64) int64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// rateCalculationLoop 定期更新资金费率
func (s *FundingService) rateCalculationLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Minute) // 每分钟更新一次
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.updateAllFundingRates()
		}
	}
}

// updateAllFundingRates 更新所有永续合约的资金费率
func (s *FundingService) updateAllFundingRates() {
	ctx := context.Background()
	contracts, err := s.contractManager.GetTradingContracts(ctx)
	if err != nil {
		return
	}

	for _, spec := range contracts {
		if spec.ContractType != TypePerpetual {
			continue
		}

		rate := s.CalculateFundingRate(spec.Symbol)
		s.fundingRates.Store(spec.Symbol, rate)
	}
}

// =============================================================================
// 资金费结算
// =============================================================================

// settlementLoop 资金费结算循环
func (s *FundingService) settlementLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Second) // 每秒检查
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.checkAndSettle()
		}
	}
}

// checkAndSettle 检查并执行结算
func (s *FundingService) checkAndSettle() {
	ctx := context.Background()
	now := time.Now().UnixMilli()

	contracts, _ := s.contractManager.GetTradingContracts(ctx)

	for _, spec := range contracts {
		if spec.ContractType != TypePerpetual {
			continue
		}

		// 检查是否到达结算时间
		nextTime := s.GetNextFundingTime(spec.Symbol)
		if now >= nextTime {
			go s.settleFunding(ctx, spec.Symbol)
		}
	}
}

// SettleFunding 手动触发资金费结算 (公开方法)
func (s *FundingService) SettleFunding(ctx context.Context, symbol string) error {
	return s.settleFunding(ctx, symbol)
}

// settleFunding 执行资金费结算
//
// 【核心流程】
// 1. 获取当前资金费率
// 2. 遍历所有持仓
// 3. 计算每个用户的资金费
// 4. 多头付钱给空头 (或反过来)
// 5. 更新下次结算时间
func (s *FundingService) settleFunding(ctx context.Context, symbol string) error {
	// 1. 防止并发结算
	if _, loaded := s.settlingSymbols.LoadOrStore(symbol, true); loaded {
		return ErrFundingInProgress
	}
	defer s.settlingSymbols.Delete(symbol)

	// 2. 获取合约规格
	spec, err := s.contractManager.GetContract(ctx, symbol)
	if err != nil {
		return err
	}

	// 3. 获取资金费率
	fundingRate := s.GetFundingRate(symbol)
	if fundingRate == 0 {
		// 费率为 0，无需结算 (多空平衡)
		s.updateNextFundingTime(symbol)
		return nil
	}

	// 4. 获取标记价格 (用于计算持仓价值)
	markPrice := s.markPriceService.GetMarkPrice(symbol)

	log.Printf("[Funding] Starting settlement for %s, rate=%d/10000, markPrice=%d",
		symbol, fundingRate, markPrice)

	// 5. 分批处理所有持仓
	var offset int
	var totalPaid, totalReceived int64
	var paidCount, receivedCount int

	for {
		positions, err := s.positionRepo.ListBySymbol(ctx, spec.Symbol, s.batchSize, offset)
		if err != nil {
			return err
		}

		if len(positions) == 0 {
			break
		}

		for _, pos := range positions {
			if pos.Size == 0 {
				continue
			}

			// 6. 计算资金费
			payment := s.calculateFundingPayment(pos, fundingRate, markPrice)

			// 7. 执行资金转移
			if err := s.applyFundingPayment(ctx, spec, pos, payment); err != nil {
				log.Printf("[Funding] Failed to apply payment for user %d: %v", pos.UserID, err)
				continue
			}

			// 统计
			if payment > 0 {
				totalReceived += payment
				receivedCount++
			} else if payment < 0 {
				totalPaid += -payment
				paidCount++
			}
		}

		offset += len(positions)
	}

	// 8. 更新下次结算时间
	s.updateNextFundingTime(symbol)

	log.Printf("[Funding] Settlement complete for %s: %d paid (total=%d), %d received (total=%d)",
		symbol, paidCount, totalPaid, receivedCount, totalReceived)

	return nil
}

// calculateFundingPayment 计算资金费
//
// 【公式】
// 资金费 = 持仓价值 × 资金费率
// 持仓价值 = |持仓数量| × 标记价格
//
// 【方向规则】
// 资金费率 > 0 (多军付):
//   - 多头 (Size > 0): payment < 0 (付出)
//   - 空头 (Size < 0): payment > 0 (收入)
//
// 资金费率 < 0 (空军付):
//   - 多头 (Size > 0): payment > 0 (收入)
//   - 空头 (Size < 0): payment < 0 (付出)
//
// 统一公式: payment = -Size * markPrice * fundingRate / Precision / FundingPrecision
func (s *FundingService) calculateFundingPayment(pos *Position, fundingRate, markPrice int64) int64 {
	// 持仓价值 = Size * markPrice / Precision
	// 资金费 = -持仓价值 * fundingRate / FundingPrecision
	// 负号是因为: 做多且费率为正时，多头要付钱 (payment < 0)

	payment := -pos.Size * markPrice * fundingRate / Precision / FundingPrecision
	return payment
}

// applyFundingPayment 应用资金费
func (s *FundingService) applyFundingPayment(
	ctx context.Context,
	spec *ContractSpec,
	pos *Position,
	payment int64,
) error {
	if payment == 0 {
		return nil
	}

	// payment > 0: 用户收到资金费
	// payment < 0: 用户支付资金费
	if payment > 0 {
		// 增加余额
		return s.balanceRepo.AddAvailable(ctx, pos.UserID, spec.SettleCurrency, payment)
	} else {
		// 扣除余额 (从可用余额扣)
		// 如果余额不足，直接扣成 0 (不会变成负数)
		balance, err := s.balanceRepo.GetBalance(ctx, pos.UserID, spec.SettleCurrency)
		if err != nil {
			return err
		}
		deductAmount := -payment
		if balance != nil && balance.Available < deductAmount {
			deductAmount = balance.Available // 最多扣到 0
		}
		if deductAmount > 0 {
			// 使用事务确保原子性
			return s.balanceRepo.AddAvailable(ctx, pos.UserID, spec.SettleCurrency, -deductAmount)
		}
	}
	return nil
}

// =============================================================================
// 辅助方法
// =============================================================================

// initNextFundingTimes 初始化下次结算时间
func (s *FundingService) initNextFundingTimes() {
	ctx := context.Background()
	contracts, _ := s.contractManager.GetTradingContracts(ctx)

	for _, spec := range contracts {
		if spec.ContractType != TypePerpetual {
			continue
		}
		s.updateNextFundingTime(spec.Symbol)
	}
}

// updateNextFundingTime 更新下次结算时间
//
// 【规则】
// 结算时间固定在 00:00, 08:00, 16:00 UTC
func (s *FundingService) updateNextFundingTime(symbol string) {
	now := time.Now().UTC()

	// 计算下一个 8 小时整点
	hour := now.Hour()
	nextHour := ((hour / 8) + 1) * 8
	if nextHour >= 24 {
		nextHour = 0
		now = now.AddDate(0, 0, 1)
	}

	nextTime := time.Date(now.Year(), now.Month(), now.Day(), nextHour, 0, 0, 0, time.UTC)
	s.nextFundingTime.Store(symbol, nextTime.UnixMilli())

	log.Printf("[Funding] Next funding time for %s: %s", symbol, nextTime.Format(time.RFC3339))
}

// GetFundingInfo 获取资金费信息 (供 API 使用)
func (s *FundingService) GetFundingInfo(symbol string) *FundingInfo {
	return &FundingInfo{
		Symbol:          symbol,
		FundingRate:     s.GetFundingRate(symbol),
		NextFundingTime: s.GetNextFundingTime(symbol),
	}
}

// FundingInfo 资金费信息
type FundingInfo struct {
	Symbol          string `json:"symbol"`
	FundingRate     int64  `json:"funding_rate"`      // 万分比
	NextFundingTime int64  `json:"next_funding_time"` // Unix毫秒
}
