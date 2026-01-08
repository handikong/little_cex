// 文件路径: pkg/liquidation/engine.go

package liquidation

import (
	"context"
	"log"
	"sync"
	"time"

	"max.com/pkg/risk"
)

// =============================================================================
// 配置常量
// =============================================================================

const (
	// Level 检查间隔
	CheckIntervalWarning  = 5 * time.Second        // Level 1: 每 5 秒
	CheckIntervalDanger   = 2 * time.Second        // Level 2: 每 2 秒
	CheckIntervalCritical = 500 * time.Millisecond // Level 3: 每 500ms

	// 强平执行器配置
	LiquidationWorkers   = 10  // Worker 数量
	LiquidationQueueSize = 100 // 任务队列大小
)

// =============================================================================
// Engine 强平引擎
// =============================================================================

// Engine 强平引擎
//
// 这是整个强平系统的核心入口，负责:
// 1. 管理风险等级索引
// 2. 启动和协调各个检查器
// 3. 处理行情变化（针对 Level 3 用户）
// 4. 管理强平任务队列和 Worker Pool
//
// 架构:
//
//	┌─────────────────────────────────────────────────┐
//	│                    Engine                       │
//	│                                                 │
//	│  ┌─────────┐  ┌─────────┐  ┌─────────┐         │
//	│  │ Scanner │  │ Checkers│  │Executor │         │
//	│  └────┬────┘  └────┬────┘  └────┬────┘         │
//	│       │            │            │               │
//	│       └────────────┴────────────┘               │
//	│                    │                            │
//	│              RiskLevelIndex                     │
//	└─────────────────────────────────────────────────┘
type Engine struct {
	// ========== 核心组件 ==========

	// index: 风险等级索引
	index *RiskLevelIndex

	// scanner: 全量扫描器
	scanner *Scanner

	// riskEngine: 风控引擎（复用已有的）
	riskEngine *risk.Engine

	// userProvider: 用户数据提供者
	userProvider UserDataProvider

	// ========== 强平执行 ==========

	// liquidationQueue: 强平任务队列
	liquidationQueue chan LiquidationTask

	// executor: 强平执行器接口（由外部实现）
	executor LiquidationExecutor

	// ========== 生命周期 ==========

	// running: 是否正在运行
	running bool

	// stopCh: 停止信号
	stopCh chan struct{}

	// wg: 等待所有 Goroutine 完成
	wg sync.WaitGroup

	// mu: 保护 running 状态
	mu sync.Mutex
}

// LiquidationExecutor 强平执行器接口
//
// 由外部实现，负责真正执行强平操作
// Engine 不关心强平如何执行，只负责调度
type LiquidationExecutor interface {
	// Execute 执行强平
	//
	// 参数:
	//   ctx: 上下文
	//   task: 强平任务
	//
	// 返回:
	//   result: 执行结果
	Execute(ctx context.Context, task LiquidationTask) LiquidationResult
}

// =============================================================================
// 引擎生命周期
// =============================================================================

// NewEngine 创建强平引擎
func NewEngine(
	riskEngine *risk.Engine,
	userProvider UserDataProvider,
	executor LiquidationExecutor,
) *Engine {
	// 创建索引
	index := NewRiskLevelIndex()

	// 创建扫描器
	scanner := NewScanner(index, userProvider, riskEngine)

	return &Engine{
		index:            index,
		scanner:          scanner,
		riskEngine:       riskEngine,
		userProvider:     userProvider,
		liquidationQueue: make(chan LiquidationTask, LiquidationQueueSize),
		executor:         executor,
		stopCh:           make(chan struct{}),
	}
}

// Start 启动引擎
//
// 会启动以下组件:
// 1. 全量扫描器 (每 5 秒)
// 2. Level 1 检查器 (每 5 秒)
// 3. Level 2 检查器 (每 2 秒)
// 4. Level 3 检查器 (每 500ms)
// 5. 强平执行 Worker Pool
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		return nil
	}

	e.running = true
	e.stopCh = make(chan struct{})

	// 1. 启动扫描器
	e.scanner.Start()

	// 2. 启动各级别检查器
	e.startChecker(RiskLevelWarning, CheckIntervalWarning)
	e.startChecker(RiskLevelDanger, CheckIntervalDanger)
	e.startChecker(RiskLevelCritical, CheckIntervalCritical)

	// 3. 启动强平 Worker Pool
	e.startWorkers()

	log.Println("[Engine] Started")
	return nil
}

// Stop 停止引擎
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running {
		return
	}

	// 发送停止信号
	close(e.stopCh)

	// 停止扫描器
	e.scanner.Stop()

	// 关闭任务队列
	close(e.liquidationQueue)

	// 等待所有 Goroutine 完成
	e.wg.Wait()

	e.running = false
	log.Println("[Engine] Stopped")
}

// =============================================================================
// 检查器
// =============================================================================

// startChecker 启动指定等级的检查器
func (e *Engine) startChecker(level RiskLevel, interval time.Duration) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.runChecker(level, interval)
	}()
	log.Printf("[Engine] Checker started: level=%s, interval=%v", level, interval)
}

// runChecker 检查器主循环
//
// 定期检查指定等级的用户，判断是否需要升降级或强平
func (e *Engine) runChecker(level RiskLevel, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.checkLevel(level)
		}
	}
}

// checkLevel 检查指定等级的所有用户
func (e *Engine) checkLevel(level RiskLevel) {
	ctx := context.Background()

	// 获取该等级的所有用户
	users := e.index.GetByLevel(level)
	if len(users) == 0 {
		return
	}

	log.Printf("[Checker] Checking level=%s, users=%d", level, len(users))

	for _, user := range users {
		// 重新获取用户数据
		riskInput, err := e.userProvider.GetUserRiskInput(ctx, user.UserID)
		if err != nil {
			log.Printf("[Checker] Failed to get risk input for user %d: %v", user.UserID, err)
			continue
		}

		// 重新计算风险
		riskOutput, err := e.riskEngine.ComputeRisk(riskInput)
		if err != nil {
			log.Printf("[Checker] Failed to compute risk for user %d: %v", user.UserID, err)
			continue
		}

		// 判断新等级
		newLevel := CalculateRiskLevel(riskOutput.RiskRatio)

		// 处理等级变化
		e.handleLevelChange(user, newLevel, riskOutput)
	}
}

// handleLevelChange 处理用户等级变化
func (e *Engine) handleLevelChange(user UserRiskData, newLevel RiskLevel, output risk.RiskOutput) {
	oldLevel := user.Level

	if newLevel == oldLevel {
		// 等级没变，只更新数据
		user.RiskRatio = output.RiskRatio
		user.Equity = output.Equity
		user.MaintMargin = output.MaintMarginReq
		user.UpdatedAt = time.Now().UnixNano()
		e.index.UpdateUser(user)
		return
	}

	// 等级发生变化
	log.Printf("[Checker] User %d level changed: %s -> %s (riskRatio=%.4f)",
		user.UserID, oldLevel, newLevel, output.RiskRatio)

	if newLevel == RiskLevelLiquidate {
		// 需要强平！
		e.triggerLiquidation(user, output)
		// 从索引中移除
		e.index.UpdateUser(UserRiskData{UserID: user.UserID, Level: RiskLevelSafe})
	} else if newLevel == RiskLevelSafe {
		// 脱离危险，从索引中移除
		e.index.UpdateUser(UserRiskData{UserID: user.UserID, Level: RiskLevelSafe})
	} else {
		// 升级或降级到其他等级
		user.Level = newLevel
		user.RiskRatio = output.RiskRatio
		user.Equity = output.Equity
		user.MaintMargin = output.MaintMarginReq
		user.UpdatedAt = time.Now().UnixNano()
		e.index.UpdateUser(user)
	}
}

// =============================================================================
// 强平触发
// =============================================================================

// triggerLiquidation 触发强平
func (e *Engine) triggerLiquidation(user UserRiskData, output risk.RiskOutput) {
	task := LiquidationTask{
		UserID:    user.UserID,
		RiskRatio: output.RiskRatio,
		CreatedAt: time.Now(),
		Priority:  output.RiskRatio, // 风险率越高，优先级越高
	}

	// 非阻塞发送到队列
	select {
	case e.liquidationQueue <- task:
		log.Printf("[Engine] Liquidation task queued: user=%d, riskRatio=%.4f",
			user.UserID, output.RiskRatio)
	default:
		// 队列满了，记录日志（生产环境应该告警）
		log.Printf("[Engine] WARNING: Liquidation queue full, task dropped: user=%d",
			user.UserID)
	}
}

// =============================================================================
// 强平执行 Worker Pool
// =============================================================================

// startWorkers 启动 Worker Pool
func (e *Engine) startWorkers() {
	for i := 0; i < LiquidationWorkers; i++ {
		e.wg.Add(1)
		go func(workerID int) {
			defer e.wg.Done()
			e.runWorker(workerID)
		}(i)
	}
	log.Printf("[Engine] %d liquidation workers started", LiquidationWorkers)
}

// runWorker 单个 Worker 的主循环
func (e *Engine) runWorker(workerID int) {
	for task := range e.liquidationQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		log.Printf("[Worker-%d] Processing liquidation: user=%d", workerID, task.UserID)

		result := e.executor.Execute(ctx, task)

		if result.Success {
			log.Printf("[Worker-%d] Liquidation success: user=%d, pnl=%.2f",
				workerID, task.UserID, result.Details.TotalPnL)
		} else {
			log.Printf("[Worker-%d] Liquidation failed: user=%d, error=%v",
				workerID, task.UserID, result.Error)
			// TODO: 失败重试逻辑
		}

		cancel()
	}
}

// =============================================================================
// 行情事件处理 (用于 Level 3 的价格触发)
// =============================================================================

// OnPriceChange 价格变化事件处理
//
// 由行情系统调用，当价格变化时检查 Level 3 用户
// 这实现了 "毫秒级强平触发" 的需求
func (e *Engine) OnPriceChange(symbol string, price float64) {
	// 获取持有该交易对的高风险用户
	userIDs := e.index.GetUsersBySymbol(symbol)
	if len(userIDs) == 0 {
		return
	}

	ctx := context.Background()

	for _, userID := range userIDs {
		// 只检查 Level 3 (Critical) 用户
		user, ok := e.index.GetUser(userID)
		if !ok || user.Level != RiskLevelCritical {
			continue
		}

		// 重新计算风险
		riskInput, err := e.userProvider.GetUserRiskInput(ctx, userID)
		if err != nil {
			continue
		}

		riskOutput, err := e.riskEngine.ComputeRisk(riskInput)
		if err != nil {
			continue
		}

		// 检查是否需要强平
		if riskOutput.RiskRatio >= ThresholdLiquidate {
			log.Printf("[Engine] Price trigger liquidation: user=%d, symbol=%s, price=%.2f",
				userID, symbol, price)
			e.triggerLiquidation(user, riskOutput)
		}
	}
}

// =============================================================================
// 监控接口
// =============================================================================

// GetStats 获取引擎统计信息
func (e *Engine) GetStats() EngineStats {
	return EngineStats{
		TotalHighRiskUsers: e.index.TotalCount(),
		WarningUsers:       len(e.index.GetByLevel(RiskLevelWarning)),
		DangerUsers:        len(e.index.GetByLevel(RiskLevelDanger)),
		CriticalUsers:      len(e.index.GetByLevel(RiskLevelCritical)),
		QueuedTasks:        len(e.liquidationQueue),
	}
}

// EngineStats 引擎统计信息
type EngineStats struct {
	TotalHighRiskUsers int
	WarningUsers       int
	DangerUsers        int
	CriticalUsers      int
	QueuedTasks        int
}
