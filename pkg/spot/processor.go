// 文件: pkg/spot/processor.go
// 现货交易处理器 - 连接资产引擎和撮合引擎
//
// 核心职责:
// 1. 下单前: 调用资产引擎冻结资金
// 2. 成交后: 调用资产引擎进行结算
// 3. 撤单后: 调用资产引擎解冻资金
//
// 架构:
//
//   用户下单 → SpotProcessor → asset.Reserve()
//                    ↓
//              mtrade.SubmitOrder()
//                    ↓
//              撮合成交 (EventTrade)
//                    ↓
//              asset.ApplyFill()

package spot

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"max.com/pkg/asset"
	"max.com/pkg/fund"
	"max.com/pkg/mtrade"
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrInvalidSymbol    = errors.New("invalid symbol format, expected BASE_QUOTE")
	ErrOrderNotFound    = errors.New("order not found")
	ErrAssetReserveFail = errors.New("asset reserve failed")
	ErrSubmitOrderFail  = errors.New("submit order to matching engine failed")
)

// =============================================================================
// 订单元数据
// =============================================================================

// OrderMeta 订单元数据
// 用于跟踪订单的资产冻结信息，成交/撤单时需要
type OrderMeta struct {
	OrderID      int64  // 订单 ID
	UserID       int64  // 用户 ID
	Symbol       string // 交易对 "BTC_USDT"
	Side         mtrade.Side
	BaseAsset    string // 基础货币 "BTC"
	QuoteAsset   string // 报价货币 "USDT"
	ReserveAsset string // 冻结的资产
	ReserveAmt   int64  // 冻结金额 (本金)
	FeeReserve   int64  // 预估手续费冻结
	Price        int64  // 订单价格
	Qty          int64  // 订单数量
}

// =============================================================================
// SpotProcessor - 现货交易处理器
// =============================================================================

// SpotProcessor 现货交易处理器
//
// 设计说明:
// - 作为资产引擎和撮合引擎的中间层
// - 不修改撮合引擎的核心逻辑
// - 通过事件监听处理成交和撤单
type SpotProcessor struct {
	assetEngine *asset.AccountEngine
	matchEngine *mtrade.Engine

	// 订单索引: OrderID -> OrderMeta
	// 用于在成交事件中查找用户信息
	orderIndex map[int64]*OrderMeta
	mu         sync.RWMutex

	// 手续费率 (万分比)
	makerFeeRate int64 // Maker 手续费率，如 10 表示 0.1%
	takerFeeRate int64 // Taker 手续费率，如 20 表示 0.2%

	// Kafka 事件发布器 (可选)
	publisher *fund.EventPublisher
}

// ProcessorConfig 处理器配置
type ProcessorConfig struct {
	AssetEngine  *asset.AccountEngine
	MatchEngine  *mtrade.Engine
	MakerFeeRate int64                // 万分比，如 10 = 0.1%
	TakerFeeRate int64                // 万分比，如 20 = 0.2%
	Publisher    *fund.EventPublisher // 可选，不为 nil 则发送 Kafka 事件
}

// NewSpotProcessor 创建现货交易处理器
func NewSpotProcessor(cfg ProcessorConfig) *SpotProcessor {
	p := &SpotProcessor{
		assetEngine:  cfg.AssetEngine,
		matchEngine:  cfg.MatchEngine,
		orderIndex:   make(map[int64]*OrderMeta),
		makerFeeRate: cfg.MakerFeeRate,
		takerFeeRate: cfg.TakerFeeRate,
		publisher:    cfg.Publisher,
	}

	// 注册事件处理器
	p.matchEngine.OnEvent(p.handleEvent)

	return p
}

// =============================================================================
// 下单流程
// =============================================================================

// PlaceOrder 提交订单
//
// 流程:
// 1. 解析交易对 (BTC_USDT -> BTC, USDT)
// 2. 计算需要冻结的资产和金额
// 3. 调用资产引擎冻结
// 4. 提交到撮合引擎
//
// 参数:
// - order: 订单 (需要已填充 UserID, Symbol, Side, Price, Qty)
func (p *SpotProcessor) PlaceOrder(order *mtrade.Order) error {
	// 1. 解析交易对
	base, quote, err := parseSymbol(order.Symbol)
	if err != nil {
		return err
	}

	// 2. 计算冻结金额 (本金 + 预估手续费)
	// 手续费按 Taker 费率预估 (最高费率)，实际可能更低
	var reserveAsset string
	var reserveAmt int64
	var feeReserve int64

	if order.Side == mtrade.SideBuy {
		// 买单: 冻结报价资产 (USDT)
		reserveAsset = quote
		// 本金 = 价格 * 数量 / 精度
		principal := (order.Price / asset.Precision) * order.Qty
		// 预估手续费 (买方扣 BTC，但下单时锁 USDT，需要额外预留)
		// 这里简化处理: 直接在 USDT 中多锁一点
		feeReserve = principal * p.takerFeeRate / 10000
		reserveAmt = principal + feeReserve
	} else {
		// 卖单: 冻结基础资产 (BTC)
		reserveAsset = base
		// 预估手续费 (卖方扣 BTC)
		feeReserve = order.Qty * p.takerFeeRate / 10000
		reserveAmt = order.Qty + feeReserve
	}

	// 3. 冻结资产
	if err := p.assetEngine.Reserve(order.UserID, reserveAsset, reserveAmt, order.ID); err != nil {
		return errors.Join(ErrAssetReserveFail, err)
	}

	// 4. 记录订单元数据
	meta := &OrderMeta{
		OrderID:      order.ID,
		UserID:       order.UserID,
		Symbol:       order.Symbol,
		Side:         order.Side,
		BaseAsset:    base,
		QuoteAsset:   quote,
		ReserveAsset: reserveAsset,
		ReserveAmt:   reserveAmt - feeReserve, // 本金部分
		FeeReserve:   feeReserve,              // 手续费部分
		Price:        order.Price,
		Qty:          order.Qty,
	}

	p.mu.Lock()
	p.orderIndex[order.ID] = meta
	p.mu.Unlock()

	// 5. 提交到撮合引擎
	if !p.matchEngine.SubmitOrder(order) {
		// 撮合队列满，解冻资产
		p.assetEngine.Release(order.UserID, reserveAsset, reserveAmt, order.ID)
		p.mu.Lock()
		delete(p.orderIndex, order.ID)
		p.mu.Unlock()
		return ErrSubmitOrderFail
	}

	return nil
}

// CancelOrder 取消订单
func (p *SpotProcessor) CancelOrder(orderID int64) bool {
	return p.matchEngine.CancelOrder(orderID)
}

// =============================================================================
// 事件处理
// =============================================================================

// handleEvent 处理撮合引擎事件
func (p *SpotProcessor) handleEvent(event mtrade.Event) {
	switch event.Type {
	case mtrade.EventTrade:
		p.handleTrade(event)
	case mtrade.EventOrderCanceled:
		p.handleCancel(event)
	case mtrade.EventOrderAccepted:
		// 订单接受，无需处理
	case mtrade.EventOrderRejected:
		p.handleReject(event)
	}
}

// handleTrade 处理成交事件
//
// 成交时的资产变化:
// - Taker (吃单方): 扣除冻结资产，获得对手资产
// - Maker (挂单方): 扣除冻结资产，获得对手资产
func (p *SpotProcessor) handleTrade(event mtrade.Event) {
	trade := event.Trade
	if trade == nil {
		return
	}

	// 获取 Taker 和 Maker 的订单元数据
	p.mu.RLock()
	takerMeta := p.orderIndex[trade.TakerID]
	makerMeta := p.orderIndex[trade.MakerID]
	p.mu.RUnlock()

	if takerMeta == nil || makerMeta == nil {
		// 订单不存在，可能是恢复场景
		return
	}

	// 确定买卖方
	var buyerID, sellerID int64
	var buyerMeta, sellerMeta *OrderMeta

	if trade.TakerSide == mtrade.SideBuy {
		buyerID = takerMeta.UserID
		sellerID = makerMeta.UserID
		buyerMeta = takerMeta
		sellerMeta = makerMeta
	} else {
		buyerID = makerMeta.UserID
		sellerID = takerMeta.UserID
		buyerMeta = makerMeta
		sellerMeta = takerMeta
	}

	// 计算手续费
	// Taker 手续费率高于 Maker
	var buyerFee, sellerFee int64
	var buyerFeeAsset, sellerFeeAsset string

	if trade.TakerSide == mtrade.SideBuy {
		// Taker 是买方
		buyerFee = trade.Qty * p.takerFeeRate / 10000
		buyerFeeAsset = buyerMeta.BaseAsset // 买方手续费用 BTC 扣
		sellerFee = (trade.Price / asset.Precision) * trade.Qty * p.makerFeeRate / 10000
		sellerFeeAsset = sellerMeta.QuoteAsset // 卖方手续费用 USDT 扣
	} else {
		// Taker 是卖方
		buyerFee = trade.Qty * p.makerFeeRate / 10000
		buyerFeeAsset = buyerMeta.BaseAsset
		sellerFee = (trade.Price / asset.Precision) * trade.Qty * p.takerFeeRate / 10000
		sellerFeeAsset = sellerMeta.QuoteAsset
	}

	// 调用资产引擎结算
	p.assetEngine.ApplyFill(&asset.FillEvent{
		TradeID:        trade.ID,
		BuyerID:        buyerID,
		SellerID:       sellerID,
		BaseAsset:      takerMeta.BaseAsset,
		QuoteAsset:     takerMeta.QuoteAsset,
		Price:          trade.Price,
		Quantity:       trade.Qty,
		BuyerFee:       buyerFee,
		BuyerFeeAsset:  buyerFeeAsset,
		SellerFee:      sellerFee,
		SellerFeeAsset: sellerFeeAsset,
	})

	// 发送 Kafka 事件 (买方和卖方各一条流水)
	if p.publisher != nil {
		quoteAmount := (trade.Price / asset.Precision) * trade.Qty

		// 买方流水: 支付 USDT，获得 BTC
		p.publisher.PublishJournal(&fund.JournalEvent{
			EventID:    fmt.Sprintf("trade_%d_buyer", trade.ID),
			UserID:     buyerID,
			Symbol:     takerMeta.QuoteAsset,
			ChangeType: fund.ChangeTypeTransfer,
			Amount:     quoteAmount,
			BizType:    fund.BizTypeTrade,
			BizID:      fmt.Sprintf("%d", trade.ID),
			CreatedAt:  time.Now(),
		})

		// 卖方流水: 支付 BTC，获得 USDT
		p.publisher.PublishJournal(&fund.JournalEvent{
			EventID:    fmt.Sprintf("trade_%d_seller", trade.ID),
			UserID:     sellerID,
			Symbol:     takerMeta.BaseAsset,
			ChangeType: fund.ChangeTypeTransfer,
			Amount:     trade.Qty,
			BizType:    fund.BizTypeTrade,
			BizID:      fmt.Sprintf("%d", trade.ID),
			CreatedAt:  time.Now(),
		})
	}
}

// handleCancel 处理撤单事件
func (p *SpotProcessor) handleCancel(event mtrade.Event) {
	order := event.Order
	if order == nil {
		return
	}

	p.mu.RLock()
	meta := p.orderIndex[order.ID]
	p.mu.RUnlock()

	if meta == nil {
		return
	}

	// 计算剩余冻结金额 (本金 + 手续费预留)
	// 已成交部分不解冻，只解冻剩余部分
	remainingQty := order.Qty - order.FilledQty
	remainingRatio := remainingQty * 10000 / order.Qty // 万分比

	var releaseAmt int64

	if order.Side == mtrade.SideBuy {
		// 买单剩余: (价格 * 剩余数量) + 比例手续费
		principal := (meta.Price / asset.Precision) * remainingQty
		feeRelease := meta.FeeReserve * remainingRatio / 10000
		releaseAmt = principal + feeRelease
	} else {
		// 卖单剩余: 剩余数量 + 比例手续费
		feeRelease := meta.FeeReserve * remainingRatio / 10000
		releaseAmt = remainingQty + feeRelease
	}

	// 解冻资产
	if releaseAmt > 0 {
		p.assetEngine.Release(meta.UserID, meta.ReserveAsset, releaseAmt, order.ID)
	}

	// 清理元数据
	p.mu.Lock()
	delete(p.orderIndex, order.ID)
	p.mu.Unlock()
}

// handleReject 处理订单拒绝事件
func (p *SpotProcessor) handleReject(event mtrade.Event) {
	order := event.Order
	if order == nil {
		return
	}

	p.mu.RLock()
	meta := p.orderIndex[order.ID]
	p.mu.RUnlock()

	if meta == nil {
		return
	}

	// 全额解冻 (本金 + 手续费预留)
	totalReserve := meta.ReserveAmt + meta.FeeReserve
	p.assetEngine.Release(meta.UserID, meta.ReserveAsset, totalReserve, order.ID)

	p.mu.Lock()
	delete(p.orderIndex, order.ID)
	p.mu.Unlock()
}

// =============================================================================
// 辅助函数
// =============================================================================

// parseSymbol 解析交易对
// "BTC_USDT" -> "BTC", "USDT"
func parseSymbol(symbol string) (base, quote string, err error) {
	parts := strings.Split(symbol, "_")
	if len(parts) != 2 {
		return "", "", ErrInvalidSymbol
	}
	return parts[0], parts[1], nil
}

/*
下单流程
PlaceOrder()
    ↓
解析交易对 (BTC_USDT)
    ↓
计算冻结金额
    ↓
asset.Reserve() 冻结
    ↓
记录 OrderMeta
    ↓
mtrade.SubmitOrder()


成交事件

EventTrade
    ↓
查找 takerMeta, makerMeta
    ↓
确定 buyerID, sellerID
    ↓
计算手续费
    ↓
asset.ApplyFill() 结算


4. 手续费计算
Taker (吃单方): 费率较高，如 0.2%
Maker (挂单方): 费率较低，如 0.1%
买方手续费用 Base 资产扣（获得的 BTC）
卖方手续费用 Quote 资产扣（获得的 USDT）



*/
