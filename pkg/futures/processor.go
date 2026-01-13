// 文件: pkg/futures/processor.go
// 合约交易处理器

package futures

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"max.com/pkg/fund"
	"max.com/pkg/mtrade"
	"max.com/pkg/nats"
	"max.com/pkg/order"
)

var (
	ErrInsufficientMargin = errors.New("insufficient margin")
	ErrInvalidLeverage    = errors.New("invalid leverage")
	ErrContractNotTrading = errors.New("contract not trading")
)

// =============================================================================
// FuturesProcessor - 合约交易处理器
// =============================================================================

// FuturesProcessor 合约交易处理器
//
// 【职责】
// 1. 开仓: 检查冷钱包余额 → 冻结冷钱包 → 提交撮合
// 2. 成交: 更新持仓 + 发布 NATS 事件
// 3. 撤单: 发布 NATS 事件
// 4. 风险计算: 实时计算 PnL、强平价格、风险等级
//
// 【架构说明】
// - 热钱包 (asset/mtrade/liquidation): 部署为独立服务，对外只提供 gRPC
// - 冷钱包 (fund): 本服务直接操作 MySQL
// - 风险模块 (risk/perp): 集成用于 PnL 和强平计算
type FuturesProcessor struct {
	contractManager  *ContractManager
	matchEngine      *mtrade.Engine // TODO: 生产环境改为 gRPC 客户端
	positionRepo     PositionRepository
	orderService     *order.OrderService
	balanceRepo      *fund.BalanceRepo // 冷钱包余额 (MySQL)
	riskCalculator   *RiskCalculator   // 风险计算器
	markPriceService *MarkPriceService // 标记价格服务
	publisher        *nats.Publisher   // NATS 事件发布器 (可选)

	// 订单元数据缓存
	orderMetas sync.Map
}

// ClosePositionRequest 平仓请求
type ClosePositionRequest struct {
	UserID int64
	Symbol string
	Qty    int64 // 平仓数量，0 表示全部平仓
	Price  int64 // 限价，0 表示市价
}

func NewFuturesProcessor(
	contractManager *ContractManager,
	matchEngine *mtrade.Engine,
	positionRepo PositionRepository,
	orderService *order.OrderService,
	balanceRepo *fund.BalanceRepo,
) *FuturesProcessor {
	p := &FuturesProcessor{
		contractManager:  contractManager,
		matchEngine:      matchEngine,
		positionRepo:     positionRepo,
		orderService:     orderService,
		balanceRepo:      balanceRepo,
		riskCalculator:   NewRiskCalculator(),
		markPriceService: NewMarkPriceService(),
	}
	matchEngine.OnEvent(p.handleEvent)
	return p
}

// SetPublisher 设置 NATS 发布器
func (p *FuturesProcessor) SetPublisher(publisher *nats.Publisher) {
	p.publisher = publisher
}

// GetRiskCalculator 获取风险计算器
func (p *FuturesProcessor) GetRiskCalculator() *RiskCalculator {
	return p.riskCalculator
}

// GetMarkPriceService 获取标记价格服务
func (p *FuturesProcessor) GetMarkPriceService() *MarkPriceService {
	return p.markPriceService
}

// UpdateMarkPrice 更新标记价格
func (p *FuturesProcessor) UpdateMarkPrice(symbol string, markPrice int64) {
	p.markPriceService.UpdateMarkPrice(symbol, markPrice)
}

// GetPositionWithRisk 获取带风险信息的持仓
func (p *FuturesProcessor) GetPositionWithRisk(ctx context.Context, userID int64, symbol string) (*PositionWithRisk, error) {
	pos, err := p.positionRepo.GetByUserAndSymbol(ctx, userID, symbol)
	if err != nil || pos == nil {
		return nil, err
	}

	// 获取标记价格
	markPrice := p.markPriceService.GetMarkPrice(symbol)
	if markPrice == 0 {
		markPrice = pos.EntryPrice // 无标记价格时用开仓价
	}

	// 获取用户余额
	balance, _ := p.balanceRepo.GetBalance(ctx, userID, "USDT")
	var balanceAmount int64
	if balance != nil {
		balanceAmount = balance.Available + balance.Locked
	}

	// 计算风险
	risk := p.riskCalculator.CalculatePositionRisk(pos, markPrice, balanceAmount)

	return &PositionWithRisk{
		Position:     pos,
		PositionRisk: risk,
	}, nil
}

// =============================================================================
// 开仓
// =============================================================================

type OpenPositionRequest struct {
	UserID   int64
	Symbol   string
	Side     Side
	Qty      int64
	Price    int64
	Leverage int
}

func (p *FuturesProcessor) OpenPosition(ctx context.Context, req *OpenPositionRequest) error {
	// 1. 获取合约规格
	spec, err := p.contractManager.GetContract(ctx, req.Symbol)
	if err != nil {
		return err
	}
	if !spec.IsTrading() {
		return ErrContractNotTrading
	}

	// 2. 验证杠杆
	if req.Leverage <= 0 || req.Leverage > spec.MaxLeverage {
		return ErrInvalidLeverage
	}

	// 3. 计算保证金
	positionValue := req.Qty * req.Price / Precision
	requiredMargin := positionValue / int64(req.Leverage)

	// 4. 冻结冷钱包余额 (MySQL)
	balance, err := p.balanceRepo.GetBalance(ctx, req.UserID, spec.SettleCurrency)
	if err != nil {
		return err
	}
	if balance == nil || balance.Available < requiredMargin {
		return ErrInsufficientMargin
	}
	if err := p.balanceRepo.FreezeBalance(ctx, req.UserID, spec.SettleCurrency, requiredMargin); err != nil {
		return ErrInsufficientMargin
	}

	// 5. 生成订单ID (雪花算法)
	orderID := order.GenerateOrderID()

	// 6. 创建订单记录 (同步写DB)
	err = p.orderService.CreateFuturesOrder(
		ctx,
		orderID,
		req.UserID,
		req.Symbol,
		toOrderSide(req.Side),
		req.Price,
		req.Qty,
		req.Leverage,
		requiredMargin,
	)
	if err != nil {
		// 回滚冷钱包冻结
		p.balanceRepo.UnfreezeBalance(ctx, req.UserID, spec.SettleCurrency, requiredMargin)
		return err
	}

	// 7. 构建撮合订单
	matchOrder := &mtrade.Order{
		ID:     orderID,
		UserID: req.UserID,
		Symbol: req.Symbol,
		Side:   toMtradeSide(req.Side),
		Type:   mtrade.OrderTypeLimit,
		Price:  req.Price,
		Qty:    req.Qty,
	}

	// 8. 提交撮合 (TODO: 生产环境改为 gRPC 调用)
	if !p.matchEngine.SubmitOrder(matchOrder) {
		// 回滚冷钱包冻结
		p.balanceRepo.UnfreezeBalance(ctx, req.UserID, spec.SettleCurrency, requiredMargin)
		// TODO: 更新订单状态为 REJECTED
		return errors.New("submit order failed")
	}

	// 9. 保存元数据 (用于成交回调)
	p.orderMetas.Store(orderID, &OrderMeta{
		UserID:   req.UserID,
		Symbol:   req.Symbol,
		Side:     req.Side,
		Qty:      req.Qty,
		Price:    req.Price,
		Leverage: req.Leverage,
		Margin:   requiredMargin,
	})

	return nil
}

// toOrderSide 转换为订单方向
func toOrderSide(side Side) order.OrderSide {
	if side == SideLong {
		return order.SideBuy
	}
	return order.SideSell
}

// =============================================================================
// 成交处理
// =============================================================================

func (p *FuturesProcessor) handleEvent(event mtrade.Event) {
	switch event.Type {
	case mtrade.EventTrade:
		p.handleTrade(event.Trade)
	case mtrade.EventOrderCanceled:
		p.handleCancel(event.Order)
	}
}

func (p *FuturesProcessor) handleTrade(trade *mtrade.Trade) {
	// 获取 Taker 和 Maker 的元数据
	var takerMeta, makerMeta *OrderMeta
	if val, ok := p.orderMetas.Load(trade.TakerID); ok {
		takerMeta = val.(*OrderMeta)
	}
	if val, ok := p.orderMetas.Load(trade.MakerID); ok {
		makerMeta = val.(*OrderMeta)
	}

	// Taker
	p.applyFill(trade.TakerID, trade)
	// Maker
	p.applyFill(trade.MakerID, trade)

	// 发布成交事件到 NATS (包含完整信息供冷钱包更新)
	if p.publisher != nil {
		event := map[string]any{
			"trade_id":       trade.ID,
			"taker_order_id": trade.TakerID,
			"maker_order_id": trade.MakerID,
			"price":          trade.Price,
			"qty":            trade.Qty,
			"timestamp":      trade.Timestamp,
		}
		// 添加 Taker 信息
		if takerMeta != nil {
			event["taker_user_id"] = takerMeta.UserID
			event["taker_margin"] = takerMeta.Margin
			event["symbol"] = takerMeta.Symbol
		}
		// 添加 Maker 信息
		if makerMeta != nil {
			event["maker_user_id"] = makerMeta.UserID
			event["maker_margin"] = makerMeta.Margin
		}
		// 结算货币
		if spec, err := p.contractManager.GetContract(context.Background(), takerMeta.Symbol); err == nil {
			event["settle_currency"] = spec.SettleCurrency
		}
		p.publisher.Publish("trades", event)
	}
}

func (p *FuturesProcessor) applyFill(orderID int64, trade *mtrade.Trade) {
	val, ok := p.orderMetas.Load(orderID)
	if !ok {
		return
	}
	meta := val.(*OrderMeta)
	ctx := context.Background()

	// 获取合约规格
	spec, _ := p.contractManager.GetContract(ctx, meta.Symbol)

	// ========== 平仓单处理 ==========
	if meta.IsClose {
		p.handleCloseFill(ctx, spec, meta, trade)
		p.orderMetas.Delete(orderID)
		return
	}

	// ========== 开仓单处理 (原有逻辑) ==========
	pos, _ := p.positionRepo.GetByUserAndSymbol(ctx, meta.UserID, meta.Symbol)
	isNewPosition := pos == nil

	if pos == nil {
		pos = &Position{
			UserID:    meta.UserID,
			Symbol:    meta.Symbol,
			CreatedAt: time.Now().UnixMilli(),
		}
	}

	fillQty := trade.Qty
	if meta.Side == SideShort {
		fillQty = -fillQty
	}

	p.updatePosition(pos, fillQty, trade.Price, meta.Margin, meta.Leverage, isNewPosition)
	p.positionRepo.Save(ctx, pos)
	p.orderMetas.Delete(orderID)

}

// handleCloseFill 处理平仓成交
//
// 【核心逻辑】
// 1. 计算已实现盈亏 (Realized PnL)
// 2. 释放保证金到可用余额
// 3. 结算盈亏到余额
// 4. 更新持仓 (减仓或清空)
func (p *FuturesProcessor) handleCloseFill(
	ctx context.Context,
	spec *ContractSpec,
	meta *OrderMeta,
	trade *mtrade.Trade,
) {
	// 1. 获取当前持仓
	pos, err := p.positionRepo.GetByUserAndSymbol(ctx, meta.UserID, meta.Symbol)
	if err != nil || pos == nil {
		log.Printf("[Futures] Close fill error: position not found for user %d", meta.UserID)
		return
	}

	// 2. 计算已实现盈亏
	// 多头: PnL = (平仓价 - 开仓价) × 平仓数量
	// 空头: PnL = (开仓价 - 平仓价) × 平仓数量
	// 统一公式: PnL = (trade.Price - pos.EntryPrice) × 平仓数量 × 方向
	//
	// 【面试】为什么用 meta.OriginalEntry 而不是 pos.EntryPrice?
	// 因为可能有多笔成交，第一笔成交后 pos.EntryPrice 会变
	var realizedPnL int64
	if meta.OriginalSize > 0 {
		// 原本是多头
		realizedPnL = (trade.Price - meta.OriginalEntry) * int64(trade.Qty) / Precision
	} else {
		// 原本是空头
		realizedPnL = (meta.OriginalEntry - trade.Price) * int64(trade.Qty) / Precision
	}

	log.Printf("[Futures] User %d close position: qty=%d, price=%d, entry=%d, PnL=%d",
		meta.UserID, trade.Qty, trade.Price, meta.OriginalEntry, realizedPnL)

	// 3. 结算到余额: 释放保证金 + 盈亏
	// 结算金额 = 释放的保证金 + 已实现盈亏
	settlementAmount := meta.Margin + realizedPnL

	// 穿仓保护: 最少返还 0
	if settlementAmount < 0 {
		log.Printf("[Futures] WARNING: User %d position bankrupt, loss exceeds margin", meta.UserID)
		// TODO: 从保险基金扣除
		settlementAmount = 0
	}

	if settlementAmount > 0 && spec != nil {
		p.balanceRepo.AddAvailable(ctx, meta.UserID, spec.SettleCurrency, settlementAmount)
	}

	// 4. 更新持仓
	// 多头平仓 → Size 减少
	// 空头平仓 → Size 增加 (绝对值减少)
	closeQty := int64(trade.Qty)
	if meta.OriginalSize > 0 {
		pos.Size -= closeQty
	} else {
		pos.Size += closeQty
	}

	// 5. 更新已实现盈亏累计
	pos.RealizedPnL += realizedPnL

	// 6. 按比例减少保证金
	pos.Margin -= meta.Margin

	// 7. 如果仓位清空
	if pos.Size == 0 {
		pos.Margin = 0
		pos.EntryPrice = 0
	}

	pos.UpdatedAt = time.Now().UnixMilli()

	// 8. 保存持仓
	p.positionRepo.Save(ctx, pos)

	// 9. 发布平仓事件
	if p.publisher != nil {
		event := map[string]any{
			"event_type":    "POSITION_CLOSED",
			"user_id":       meta.UserID,
			"symbol":        meta.Symbol,
			"close_qty":     trade.Qty,
			"close_price":   trade.Price,
			"realized_pnl":  realizedPnL,
			"remaining_pos": pos.Size,
			"timestamp":     time.Now().UnixMilli(),
		}
		p.publisher.Publish("position.closed", event)
	}
}

func (p *FuturesProcessor) updatePosition(pos *Position, deltaSize, price, margin int64, leverage int, isNew bool) PositionChangeType {
	if isNew || pos.Size == 0 {
		// 新开仓
		pos.Size = deltaSize
		pos.EntryPrice = price
		pos.Margin = margin
		pos.Leverage = leverage
		return PositionOpen
	}

	// 同向加仓
	if (pos.Size > 0 && deltaSize > 0) || (pos.Size < 0 && deltaSize < 0) {
		oldValue := pos.Size * pos.EntryPrice
		newValue := deltaSize * price
		pos.Size += deltaSize
		pos.EntryPrice = (oldValue + newValue) / pos.Size
		pos.Margin += margin
		return PositionAdd
	}

	// 反向: 减仓或反向开仓 (简化处理)
	pos.Size += deltaSize
	if pos.Size == 0 {
		return PositionClose
	}
	return PositionReduce
}

func (p *FuturesProcessor) handleCancel(order *mtrade.Order) {
	val, ok := p.orderMetas.Load(order.ID)
	if !ok {
		return
	}
	meta := val.(*OrderMeta)

	spec, _ := p.contractManager.GetContract(context.Background(), meta.Symbol)

	// 解冻冷钱包 (热钱包由撮合服务内部管理)
	if spec != nil && p.balanceRepo != nil {
		p.balanceRepo.UnfreezeBalance(context.Background(), meta.UserID, spec.SettleCurrency, meta.Margin)
	}
	p.orderMetas.Delete(order.ID)

	// 发布撤单事件到 NATS (包含完整信息)
	if p.publisher != nil {
		event := map[string]any{
			"order_id":        order.ID,
			"user_id":         meta.UserID,
			"margin":          meta.Margin,
			"settle_currency": spec.SettleCurrency,
			"reason":          "user_cancel",
			"timestamp":       time.Now().UnixMilli(),
		}
		p.publisher.Publish("order.canceled", event)
	}
}

// ClosePosition 平仓/减仓
//
// 【核心逻辑】
// 1. 获取用户持仓
// 2. 确定平仓数量 (全部 or 部分)
// 3. 计算应释放的保证金
// 4. 构建反向订单提交撮合
// 5. 成交后: 更新持仓 + 结算盈亏
//
// 【平仓 vs 减仓】
// - 平仓: Qty >= Position.Size，清空整个仓位
// - 减仓: Qty < Position.Size，部分平仓
//
// 【面试考点】
// Q: 平仓后保证金怎么处理？
// A: 释放保证金到可用余额 + 盈亏结算
func (p *FuturesProcessor) ClosePosition(ctx context.Context, req *ClosePositionRequest) error {
	// 1. 获取用户持仓
	pos, err := p.positionRepo.GetByUserAndSymbol(ctx, req.UserID, req.Symbol)
	if err != nil {
		return err
	}
	if pos == nil || pos.Size == 0 {
		return errors.New("no position to close")
	}

	// 2. 获取合约规格
	spec, err := p.contractManager.GetContract(ctx, req.Symbol)
	if err != nil {
		return err
	}
	if !spec.IsTrading() {
		return ErrContractNotTrading
	}

	// 3. 确定平仓数量
	closeQty := req.Qty
	if closeQty <= 0 || closeQty > pos.AbsSize() {
		closeQty = pos.AbsSize() // 全部平仓
	}

	// 4. 平仓方向与开仓相反
	// 多头持仓 (Size > 0) → 卖出平仓
	// 空头持仓 (Size < 0) → 买入平仓
	var closeSide Side
	if pos.Size > 0 {
		closeSide = SideShort // 卖出
	} else {
		closeSide = SideLong // 买入
	}

	// 5. 确定价格
	closePrice := req.Price
	if closePrice <= 0 {
		// 市价单：使用标记价格作为参考
		// 实际撮合时会使用订单簿最优价
		closePrice = p.markPriceService.GetMarkPrice(req.Symbol)
		if closePrice <= 0 {
			return errors.New("no market price available")
		}
	}

	// 6. 计算应释放的保证金 (按比例)
	// 如果平掉 50% 仓位，释放 50% 保证金
	marginToRelease := pos.Margin * closeQty / pos.AbsSize()

	// 7. 生成订单ID
	orderID := order.GenerateOrderID()

	// 8. 创建平仓订单记录
	err = p.orderService.CreateFuturesOrder(
		ctx,
		orderID,
		req.UserID,
		req.Symbol,
		toOrderSide(closeSide),
		closePrice,
		closeQty,
		pos.Leverage, // 沿用原杠杆
		0,            // 平仓不需要新增保证金
	)
	if err != nil {
		return err
	}

	// 9. 构建撮合订单
	matchOrder := &mtrade.Order{
		ID:     orderID,
		UserID: req.UserID,
		Symbol: req.Symbol,
		Side:   toMtradeSide(closeSide),
		Type:   mtrade.OrderTypeLimit,
		Price:  closePrice,
		Qty:    closeQty,
	}

	// 10. 提交撮合
	if !p.matchEngine.SubmitOrder(matchOrder) {
		return errors.New("submit close order failed")
	}

	// 11. 保存订单元数据 (用于成交回调)
	// 【重要】IsClose = true 标记这是平仓单
	p.orderMetas.Store(orderID, &OrderMeta{
		UserID:        req.UserID,
		Symbol:        req.Symbol,
		Side:          closeSide,
		Qty:           closeQty,
		Price:         closePrice,
		Leverage:      pos.Leverage,
		Margin:        marginToRelease,
		IsClose:       true, // 🔑 平仓标记
		OriginalSize:  pos.Size,
		OriginalEntry: pos.EntryPrice,
	})

	return nil
}

// =============================================================================
// 辅助
// =============================================================================

type OrderMeta struct {
	UserID   int64
	Symbol   string
	Side     Side
	Qty      int64
	Price    int64
	Leverage int
	Margin   int64

	// 平仓相关
	IsClose       bool  // 是否是平仓单
	OriginalSize  int64 // 平仓前的持仓量 (用于计算盈亏)
	OriginalEntry int64 // 平仓前的开仓均价

}

func toMtradeSide(side Side) mtrade.Side {
	if side == SideLong {
		return mtrade.SideBuy
	}
	return mtrade.SideSell
}
