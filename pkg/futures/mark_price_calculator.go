// 文件: pkg/futures/mark_price_calculator.go
// 标记价格计算器 - 从多交易所现货价格计算标记价格

package futures

import (
	"sort"
	"sync"
	"time"
)

// =============================================================================
// MarkPriceCalculator - 标记价格计算器
// =============================================================================

// MarkPriceCalculator 标记价格计算器
//
// 【标记价格 vs 最新价格】
// - 最新价格: 本交易所最后成交价，容易被操控
// - 标记价格: 多交易所现货指数 + 基差平滑，用于计算强平
//
// 【计算公式 - 永续合约】
// MarkPrice = IndexPrice + EMA(Basis)
// Basis = ContractMidPrice - IndexPrice
//
// 【计算公式 - 交割合约】
// MarkPrice = IndexPrice × (1 + FairBasis)
// FairBasis = 考虑到期时间的合理溢价
type MarkPriceCalculator struct {
	mu sync.RWMutex

	// 各交易所现货价格 (用于计算指数)
	// symbol -> exchange -> price
	spotPrices map[string]map[string]*ExchangePrice

	// 交易所权重配置
	exchangeWeights map[string]float64

	// 基差移动平均
	// symbol -> basisHistory
	basisHistory map[string]*BasisHistory

	// 输出: 标记价格服务
	priceService *MarkPriceService

	// 配置
	config MarkPriceConfig
}

// ExchangePrice 交易所价格
type ExchangePrice struct {
	Exchange  string
	Price     int64 // 价格
	UpdatedAt int64 // 更新时间
}

// BasisHistory 基差历史 (用于 EMA 计算)
type BasisHistory struct {
	Values    []int64 // 历史基差值
	EMA       int64   // 当前 EMA
	LastIndex int     // 环形缓冲区索引
	Size      int     // 历史长度
}

// MarkPriceConfig 配置
type MarkPriceConfig struct {
	EMAWindow      int           // EMA 窗口大小 (默认 200)
	PriceTimeout   time.Duration // 价格超时 (默认 30s)
	UpdateInterval time.Duration // 更新间隔 (默认 1s)
}

// DefaultMarkPriceConfig 默认配置
func DefaultMarkPriceConfig() MarkPriceConfig {
	return MarkPriceConfig{
		EMAWindow:      200,
		PriceTimeout:   30 * time.Second,
		UpdateInterval: 1 * time.Second,
	}
}

// NewMarkPriceCalculator 创建标记价格计算器
func NewMarkPriceCalculator(priceService *MarkPriceService) *MarkPriceCalculator {
	return &MarkPriceCalculator{
		spotPrices:      make(map[string]map[string]*ExchangePrice),
		exchangeWeights: defaultExchangeWeights(),
		basisHistory:    make(map[string]*BasisHistory),
		priceService:    priceService,
		config:          DefaultMarkPriceConfig(),
	}
}

// 默认交易所权重
func defaultExchangeWeights() map[string]float64 {
	return map[string]float64{
		"binance": 0.35,
		"okx":     0.25,
		"huobi":   0.20,
		"bybit":   0.20,
	}
}

// =============================================================================
// 价格输入
// =============================================================================

// UpdateSpotPrice 更新某交易所的现货价格
func (c *MarkPriceCalculator) UpdateSpotPrice(symbol, exchange string, price int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.spotPrices[symbol] == nil {
		c.spotPrices[symbol] = make(map[string]*ExchangePrice)
	}

	c.spotPrices[symbol][exchange] = &ExchangePrice{
		Exchange:  exchange,
		Price:     price,
		UpdatedAt: time.Now().UnixMilli(),
	}
}

// UpdateContractPrice 更新合约最新价格 (用于计算基差)
// 返回计算后的标记价格
func (c *MarkPriceCalculator) UpdateContractPrice(symbol string, contractPrice int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 1. 计算指数价格
	indexPrice := c.calculateIndexPrice(symbol)
	if indexPrice == 0 {
		return contractPrice // 无指数价格，返回合约价格
	}

	// 2. 计算基差
	basis := contractPrice - indexPrice

	// 3. 更新基差 EMA
	emaBasis := c.updateBasisEMA(symbol, basis)

	// 4. 计算标记价格
	markPrice := indexPrice + emaBasis

	// 5. 更新到价格服务
	c.priceService.UpdatePriceInfo(&MarkPriceInfo{
		Symbol:     symbol,
		MarkPrice:  markPrice,
		IndexPrice: indexPrice,
		UpdatedAt:  time.Now().UnixMilli(),
	})

	return markPrice
}

// =============================================================================
// 核心计算
// =============================================================================

// calculateIndexPrice 计算现货指数价格 (加权平均)
func (c *MarkPriceCalculator) calculateIndexPrice(symbol string) int64 {
	exchanges, ok := c.spotPrices[symbol]
	if !ok || len(exchanges) == 0 {
		return 0
	}

	now := time.Now().UnixMilli()
	timeout := c.config.PriceTimeout.Milliseconds()

	// 收集有效价格
	type priceWeight struct {
		price  int64
		weight float64
	}
	var validPrices []priceWeight
	totalWeight := 0.0

	for exchange, ep := range exchanges {
		// 过滤超时价格
		if now-ep.UpdatedAt > timeout {
			continue
		}

		weight := c.exchangeWeights[exchange]
		if weight == 0 {
			weight = 0.1 // 未知交易所给小权重
		}

		validPrices = append(validPrices, priceWeight{
			price:  ep.Price,
			weight: weight,
		})
		totalWeight += weight
	}

	if len(validPrices) == 0 {
		return 0
	}

	// 计算加权平均
	var weightedSum float64
	for _, pw := range validPrices {
		weightedSum += float64(pw.price) * pw.weight / totalWeight
	}

	return int64(weightedSum)
}

// calculateIndexPriceMedian 计算现货指数价格 (中位数，防操控)
func (c *MarkPriceCalculator) calculateIndexPriceMedian(symbol string) int64 {
	exchanges, ok := c.spotPrices[symbol]
	if !ok || len(exchanges) == 0 {
		return 0
	}

	now := time.Now().UnixMilli()
	timeout := c.config.PriceTimeout.Milliseconds()

	// 收集有效价格
	var prices []int64
	for _, ep := range exchanges {
		if now-ep.UpdatedAt > timeout {
			continue
		}
		prices = append(prices, ep.Price)
	}

	if len(prices) == 0 {
		return 0
	}

	// 排序取中位数
	sort.Slice(prices, func(i, j int) bool { return prices[i] < prices[j] })
	mid := len(prices) / 2
	if len(prices)%2 == 0 {
		return (prices[mid-1] + prices[mid]) / 2
	}
	return prices[mid]
}

// updateBasisEMA 更新基差的指数移动平均
func (c *MarkPriceCalculator) updateBasisEMA(symbol string, basis int64) int64 {
	history, ok := c.basisHistory[symbol]
	if !ok {
		history = &BasisHistory{
			Values: make([]int64, c.config.EMAWindow),
			EMA:    basis, // 初始 EMA = 第一个基差值
			Size:   0,
		}
		c.basisHistory[symbol] = history
	}

	// 更新环形缓冲区
	history.Values[history.LastIndex] = basis
	history.LastIndex = (history.LastIndex + 1) % c.config.EMAWindow
	if history.Size < c.config.EMAWindow {
		history.Size++
	}

	// 计算 EMA
	// EMA = α * CurrentBasis + (1-α) * PreviousEMA
	// α = 2 / (N + 1)
	alpha := 2.0 / float64(c.config.EMAWindow+1)
	history.EMA = int64(alpha*float64(basis) + (1-alpha)*float64(history.EMA))

	return history.EMA
}

// =============================================================================
// 查询接口
// =============================================================================

// GetIndexPrice 获取指数价格
func (c *MarkPriceCalculator) GetIndexPrice(symbol string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.calculateIndexPrice(symbol)
}

// GetBasisEMA 获取基差 EMA
func (c *MarkPriceCalculator) GetBasisEMA(symbol string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if history, ok := c.basisHistory[symbol]; ok {
		return history.EMA
	}
	return 0
}

// SetExchangeWeight 设置交易所权重
func (c *MarkPriceCalculator) SetExchangeWeight(exchange string, weight float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exchangeWeights[exchange] = weight
}
