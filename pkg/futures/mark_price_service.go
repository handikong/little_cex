// 文件: pkg/futures/mark_price_service.go
// 标记价格服务 - 管理各合约的标记价格

package futures

import (
	"context"
	"sync"
	"time"
)

// =============================================================================
// MarkPriceService - 标记价格服务
// =============================================================================

// MarkPriceService 标记价格管理服务
//
// 【职责】
// 1. 存储各合约的当前标记价格
// 2. 提供价格查询接口
// 3. 发布价格更新事件 (用于强平引擎)
//
// 【标记价格来源】
// 实际生产中，标记价格来自多交易所现货指数 + 资金费率修正
// 这里简化为直接接收外部推送
type MarkPriceService struct {
	mu     sync.RWMutex
	prices map[string]*MarkPriceInfo

	// 价格更新回调
	onPriceUpdate func(symbol string, price *MarkPriceInfo)
}

// MarkPriceInfo 标记价格信息
type MarkPriceInfo struct {
	Symbol     string
	MarkPrice  int64 // 标记价格
	IndexPrice int64 // 指数价格 (现货加权)

	// 永续合约专用 (ExpiryTime = 0)
	FundingRate int64 // 资金费率 (每8小时结算)
	NextFunding int64 // 下次资金费结算时间

	// 交割合约专用 (ExpiryTime > 0)
	ExpiryTime int64 // 到期时间 (Unix毫秒), 0 表示永续合约

	UpdatedAt int64 // 更新时间
}

// IsPerpetual 是否为永续合约
func (m *MarkPriceInfo) IsPerpetual() bool {
	return m.ExpiryTime == 0
}

// IsDelivery 是否为交割合约
func (m *MarkPriceInfo) IsDelivery() bool {
	return m.ExpiryTime > 0
}

// TimeToExpiry 距离到期的时间 (毫秒), 永续返回 -1
func (m *MarkPriceInfo) TimeToExpiry() int64 {
	if m.ExpiryTime == 0 {
		return -1
	}
	return m.ExpiryTime - time.Now().UnixMilli()
}

// NewMarkPriceService 创建标记价格服务
func NewMarkPriceService() *MarkPriceService {
	return &MarkPriceService{
		prices: make(map[string]*MarkPriceInfo),
	}
}

// GetMarkPrice 获取标记价格
func (s *MarkPriceService) GetMarkPrice(symbol string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if info, ok := s.prices[symbol]; ok {
		return info.MarkPrice
	}
	return 0
}

// GetIndexPrice 获取指数价格 (现货加权价格)
// 用于资金费率计算
func (s *MarkPriceService) GetIndexPrice(symbol string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if info, ok := s.prices[symbol]; ok {
		return info.IndexPrice
	}
	return 0
}

// GetPriceInfo 获取完整价格信息
func (s *MarkPriceService) GetPriceInfo(symbol string) *MarkPriceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if info, ok := s.prices[symbol]; ok {
		// 返回副本
		copy := *info
		return &copy
	}
	return nil
}

// UpdateMarkPrice 更新标记价格
func (s *MarkPriceService) UpdateMarkPrice(symbol string, markPrice int64) {
	s.mu.Lock()
	info, ok := s.prices[symbol]
	if !ok {
		info = &MarkPriceInfo{Symbol: symbol}
		s.prices[symbol] = info
	}
	info.MarkPrice = markPrice
	info.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()

	// 触发回调
	if s.onPriceUpdate != nil {
		s.onPriceUpdate(symbol, info)
	}
}

// UpdatePriceInfo 更新完整价格信息
func (s *MarkPriceService) UpdatePriceInfo(info *MarkPriceInfo) {
	if info == nil {
		return
	}

	s.mu.Lock()
	info.UpdatedAt = time.Now().UnixMilli()
	s.prices[info.Symbol] = info
	s.mu.Unlock()

	// 触发回调
	if s.onPriceUpdate != nil {
		s.onPriceUpdate(info.Symbol, info)
	}
}

// OnPriceUpdate 设置价格更新回调
// 用于通知强平引擎检查风险
func (s *MarkPriceService) OnPriceUpdate(callback func(symbol string, price *MarkPriceInfo)) {
	s.onPriceUpdate = callback
}

// GetAllPrices 获取所有价格
func (s *MarkPriceService) GetAllPrices() map[string]*MarkPriceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*MarkPriceInfo, len(s.prices))
	for k, v := range s.prices {
		copy := *v
		result[k] = &copy
	}
	return result
}

// =============================================================================
// 获取带风险信息的持仓
// =============================================================================

// PositionWithRisk 带风险信息的持仓
type PositionWithRisk struct {
	*Position
	*PositionRisk
}

// GetPositionWithRisk 获取带风险计算的持仓
func GetPositionWithRisk(
	ctx context.Context,
	posRepo PositionRepository,
	priceService *MarkPriceService,
	riskCalc *RiskCalculator,
	userID int64,
	symbol string,
	balance int64,
) (*PositionWithRisk, error) {
	pos, err := posRepo.GetByUserAndSymbol(ctx, userID, symbol)
	if err != nil || pos == nil {
		return nil, err
	}

	markPrice := priceService.GetMarkPrice(symbol)
	if markPrice == 0 {
		// 无标记价格，用开仓价代替
		markPrice = pos.EntryPrice
	}

	risk := riskCalc.CalculatePositionRisk(pos, markPrice, balance)

	return &PositionWithRisk{
		Position:     pos,
		PositionRisk: risk,
	}, nil
}
