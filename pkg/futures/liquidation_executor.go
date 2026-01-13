// 文件: pkg/futures/liquidation_executor.go
// 强平执行器 - 连接强平引擎和撮合引擎
//
// 【职责】
// 1. 接收强平任务
// 2. 发送强平单到撮合引擎
// 3. 处理强平成功/失败
// 4. 调用保险基金兜底穿仓
//
// 【强平流程】
// 强平引擎 → LiquidationExecutor → 撮合引擎 → 成交回调 → 保险基金

package futures

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"max.com/pkg/fund"
	"max.com/pkg/liquidation"
	"max.com/pkg/mtrade"
	"max.com/pkg/order"
)

// =============================================================================
// LiquidationExecutor - 强平执行器
// =============================================================================

// LiquidationExecutor 强平执行器
//
// 实现 liquidation.LiquidationExecutor 接口
type LiquidationExecutor struct {
	contractManager  *ContractManager
	matchEngine      *mtrade.Engine
	positionRepo     PositionRepository
	balanceRepo      *fund.BalanceRepo
	markPriceService *MarkPriceService
	insuranceFund    *InsuranceFund
	orderService     *order.OrderService

	// 强平订单追踪
	// orderID -> LiquidationTask
	pendingTasks sync.Map
}

func NewLiquidationExecutor(
	contractManager *ContractManager,
	matchEngine *mtrade.Engine,
	positionRepo PositionRepository,
	balanceRepo *fund.BalanceRepo,
	markPriceService *MarkPriceService,
	insuranceFund *InsuranceFund,
	orderService *order.OrderService,
) *LiquidationExecutor {
	executor := &LiquidationExecutor{
		contractManager:  contractManager,
		matchEngine:      matchEngine,
		positionRepo:     positionRepo,
		balanceRepo:      balanceRepo,
		markPriceService: markPriceService,
		insuranceFund:    insuranceFund,
		orderService:     orderService,
	}

	// 注册成交回调
	matchEngine.OnEvent(executor.handleEvent)

	return executor
}

// =============================================================================
// 实现 liquidation.LiquidationExecutor 接口
// =============================================================================

// Execute 执行强平
//
// 【核心逻辑】
// 1. 获取用户持仓
// 2. 计算破产价格和强平价格
// 3. 发送强平单到撮合
// 4. 等待成交
func (e *LiquidationExecutor) Execute(
	ctx context.Context,
	task liquidation.LiquidationTask,
) liquidation.LiquidationResult {
	log.Printf("[Liquidation] Executing task for user %d, symbol %s",
		task.UserID, task.Symbol)

	// 1. 获取用户持仓
	pos, err := e.positionRepo.GetByUserAndSymbol(ctx, task.UserID, task.Symbol)
	if err != nil || pos == nil || pos.Size == 0 {
		return liquidation.LiquidationResult{
			UserID:  task.UserID,
			Success: false,
			Error:   errors.New("no position found"),
		}
	}

	// 2. 获取合约规格
	spec, err := e.contractManager.GetContract(ctx, task.Symbol)
	if err != nil {
		return liquidation.LiquidationResult{
			UserID:  task.UserID,
			Success: false,
			Error:   err,
		}

	}

	// 3. 获取当前标记价格
	markPrice := e.markPriceService.GetMarkPrice(task.Symbol)
	if markPrice <= 0 {
		return liquidation.LiquidationResult{
			UserID:  task.UserID,
			Success: false,
			Error:   errors.New("no mark price"),
		}

	}

	// 4. 计算破产价格 (用户亏光保证金的价格)
	// 多头: 破产价 = 开仓价 - 保证金 / 数量
	// 空头: 破产价 = 开仓价 + 保证金 / 数量
	bankruptPrice := e.calculateBankruptPrice(pos)

	// 5. 强平价格 = 破产价格 (简化处理)
	// 实际交易所会留一点缓冲给保险基金
	liquidationPrice := bankruptPrice

	// 6. 确定强平方向
	var liqSide mtrade.Side
	if pos.Size > 0 {
		liqSide = mtrade.SideSell // 多头 → 卖出平仓
	} else {
		liqSide = mtrade.SideBuy // 空头 → 买入平仓
	}

	// 7. 生成订单ID
	orderID := order.GenerateOrderID()

	// 8. 创建强平订单
	liqOrder := &mtrade.Order{
		ID:     orderID,
		UserID: task.UserID,
		Symbol: task.Symbol,
		Side:   liqSide,
		Type:   mtrade.OrderTypeLimit, // 限价单，价格为破产价
		Price:  liquidationPrice,
		Qty:    pos.AbsSize(),
	}

	// 9. 保存任务信息 (用于成交后处理)
	e.pendingTasks.Store(orderID, &PendingLiquidation{
		Task:           task,
		Position:       *pos,
		BankruptPrice:  bankruptPrice,
		SettleCurrency: spec.SettleCurrency,
		SubmittedAt:    time.Now().UnixMilli(),
	})

	// 10. 提交到撮合引擎
	// 【特殊处理】强平单可能需要优先成交
	// 部分交易所会让强平单优先于普通订单
	if !e.matchEngine.SubmitOrder(liqOrder) {
		e.pendingTasks.Delete(orderID)
		return liquidation.LiquidationResult{
			Success: false,
			Error:   errors.New("submit liquidation order failed"),
		}
	}

	log.Printf("[Liquidation] Order submitted: orderID=%d, user=%d, size=%d, price=%d",
		orderID, task.UserID, pos.AbsSize(), liquidationPrice)

	// 11. 返回结果 (实际成交在回调中处理)
	return liquidation.LiquidationResult{
		Success: true,
		Error:   nil,
	}
}

// PendingLiquidation 待处理的强平任务
type PendingLiquidation struct {
	Task           liquidation.LiquidationTask
	Position       Position
	BankruptPrice  int64
	SettleCurrency string
	SubmittedAt    int64
}

// =============================================================================
// 成交回调
// =============================================================================

// handleEvent 处理撮合事件
func (e *LiquidationExecutor) handleEvent(event mtrade.Event) {
	if event.Type != mtrade.EventTrade {
		return
	}

	trade := event.Trade

	// 检查是否是强平订单
	if pending, ok := e.pendingTasks.Load(trade.TakerID); ok {
		e.handleLiquidationFill(trade, pending.(*PendingLiquidation), true)
		e.pendingTasks.Delete(trade.TakerID)
	}
	if pending, ok := e.pendingTasks.Load(trade.MakerID); ok {
		e.handleLiquidationFill(trade, pending.(*PendingLiquidation), false)
		e.pendingTasks.Delete(trade.MakerID)
	}
}

// handleLiquidationFill 处理强平成交
//
// 【核心逻辑】
// 1. 计算强平盈亏
// 2. 如果成交价优于破产价 → 差额归保险基金
// 3. 如果成交价劣于破产价 → 从保险基金扣除
// 4. 清空用户持仓
func (e *LiquidationExecutor) handleLiquidationFill(
	trade *mtrade.Trade,
	pending *PendingLiquidation,
	isTaker bool,
) {
	ctx := context.Background()
	pos := &pending.Position

	log.Printf("[Liquidation] Fill received: user=%d, price=%d, qty=%d",
		pending.Task.UserID, trade.Price, trade.Qty)

	// 1. 计算强平盈亏
	// 多头: PnL = (成交价 - 开仓价) × 数量
	// 空头: PnL = (开仓价 - 成交价) × 数量
	var pnl int64
	if pos.Size > 0 {
		pnl = (trade.Price - pos.EntryPrice) * int64(trade.Qty) / Precision
	} else {
		pnl = (pos.EntryPrice - trade.Price) * int64(trade.Qty) / Precision
	}

	// 2. 计算剩余金额 = 保证金 + 盈亏
	remaining := pos.Margin + pnl

	// 3. 处理强平剩余/穿仓
	if remaining > 0 {
		// 【强平盈余】成交价格优于破产价格
		// 剩余金额归保险基金
		e.insuranceFund.AddFunds(
			ctx,
			pending.SettleCurrency,
			remaining,
			"LIQUIDATION_PROFIT",
			pending.Task.UserID,
			pending.Task.Symbol,
			"Liquidation surplus",
		)
		log.Printf("[Liquidation] Surplus %d goes to insurance fund", remaining)

	} else if remaining < 0 {
		// 【穿仓】成交价格劣于破产价格
		// 从保险基金扣除补足
		bankruptAmount := -remaining
		covered, err := e.insuranceFund.CoverBankruptcy(
			ctx,
			pending.SettleCurrency,
			bankruptAmount,
			pending.Task.UserID,
			pending.Task.Symbol,
		)

		if err != nil || covered < bankruptAmount {
			// 保险基金不足，需要触发 ADL
			log.Printf("[Liquidation] WARNING: Insurance fund insufficient, need ADL!")
			// TODO: 触发 ADL
		} else {
			log.Printf("[Liquidation] Bankruptcy %d covered by insurance fund", covered)
		}
	}

	// 4. 清空用户持仓
	pos.RealizedPnL += pnl
	pos.Size = 0
	pos.Margin = 0
	pos.EntryPrice = 0
	pos.UpdatedAt = time.Now().UnixMilli()

	e.positionRepo.Save(ctx, pos)

	log.Printf("[Liquidation] User %d position liquidated, PnL=%d", pending.Task.UserID, pnl)
}

// =============================================================================
// 辅助方法
// =============================================================================

// calculateBankruptPrice 计算破产价格
//
// 【公式】
// 多头: 破产价 = 开仓价 × (1 - 1/杠杆) = 开仓价 - 保证金/数量 × 精度
// 空头: 破产价 = 开仓价 × (1 + 1/杠杆) = 开仓价 + 保证金/数量 × 精度
//
// 【直观理解】
// 破产价就是"亏光保证金"的价格
// 100x 杠杆多仓: 价格跌 1% 就破产
func (e *LiquidationExecutor) calculateBankruptPrice(pos *Position) int64 {
	if pos.Size == 0 {
		return 0
	}

	// 保证金对应的价格变动 = Margin / |Size| * Precision
	marginPerUnit := pos.Margin * Precision / pos.AbsSize()

	if pos.Size > 0 {
		// 多头: 破产价 = 开仓价 - 保证金/数量
		return pos.EntryPrice - marginPerUnit
	} else {
		// 空头: 破产价 = 开仓价 + 保证金/数量
		return pos.EntryPrice + marginPerUnit
	}
}
