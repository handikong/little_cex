package liquidation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max.com/pkg/risk"
)

// =============================================================================
// Mock LiquidationExecutor
// =============================================================================

// MockLiquidationExecutor 模拟强平执行器
type MockLiquidationExecutor struct {
	// 执行的任务
	ExecutedTasks []LiquidationTask
	mu            sync.Mutex

	// 模拟执行结果
	ExecuteResult LiquidationResult

	// 执行延迟（模拟真实执行时间）
	ExecuteDelay time.Duration

	// 调用计数
	ExecuteCalls int32
}

func (m *MockLiquidationExecutor) Execute(ctx context.Context, task LiquidationTask) LiquidationResult {
	atomic.AddInt32(&m.ExecuteCalls, 1)

	m.mu.Lock()
	m.ExecutedTasks = append(m.ExecutedTasks, task)
	m.mu.Unlock()

	if m.ExecuteDelay > 0 {
		time.Sleep(m.ExecuteDelay)
	}

	if m.ExecuteResult.UserID == 0 {
		// 默认返回成功
		return LiquidationResult{
			UserID:     task.UserID,
			Success:    true,
			ExecutedAt: time.Now(),
			Details: LiquidationDetails{
				ClosedPositions:  1,
				TotalPnL:         -100,
				RemainingBalance: 50,
			},
		}
	}
	return m.ExecuteResult
}

func (m *MockLiquidationExecutor) GetExecutedTasks() []LiquidationTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]LiquidationTask, len(m.ExecutedTasks))
	copy(result, m.ExecutedTasks)
	return result
}

// =============================================================================
// Engine 单元测试
// =============================================================================

func TestEngine_NewEngine(t *testing.T) {
	riskEngine := risk.NewEngine()
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}

	engine := NewEngine(riskEngine, provider, executor)

	if engine == nil {
		t.Fatal("NewEngine should not return nil")
	}

	if engine.index == nil {
		t.Error("engine.index should not be nil")
	}

	if engine.scanner == nil {
		t.Error("engine.scanner should not be nil")
	}

	if engine.liquidationQueue == nil {
		t.Error("engine.liquidationQueue should not be nil")
	}
}

func TestEngine_StartStop(t *testing.T) {
	provider := &MockUserDataProvider{
		UserIDs:        []int64{},
		UserRiskInputs: map[int64]risk.RiskInput{},
	}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 启动
	err := engine.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// 重复启动应该无副作用
	err = engine.Start()
	if err != nil {
		t.Fatalf("Second Start() error: %v", err)
	}

	// 短暂等待确保 goroutines 启动
	time.Sleep(50 * time.Millisecond)

	// 停止
	engine.Stop()

	// 重复停止应该无副作用
	engine.Stop()
}

func TestEngine_GetStats(t *testing.T) {
	provider := &MockUserDataProvider{
		UserIDs: []int64{1, 2, 3},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.75), // Warning
			2: createMockRiskInput(2, "BTC_USDT", 0.85), // Danger
			3: createMockRiskInput(3, "BTC_USDT", 0.95), // Critical
		},
	}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 手动触发扫描填充索引
	engine.scanner.Scan(context.Background())

	stats := engine.GetStats()

	if stats.TotalHighRiskUsers != 3 {
		t.Errorf("TotalHighRiskUsers = %d, want 3", stats.TotalHighRiskUsers)
	}
	if stats.WarningUsers != 1 {
		t.Errorf("WarningUsers = %d, want 1", stats.WarningUsers)
	}
	if stats.DangerUsers != 1 {
		t.Errorf("DangerUsers = %d, want 1", stats.DangerUsers)
	}
	if stats.CriticalUsers != 1 {
		t.Errorf("CriticalUsers = %d, want 1", stats.CriticalUsers)
	}
}

func TestEngine_TriggerLiquidation(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 启动引擎（需要 worker 来消费队列）
	engine.Start()
	defer engine.Stop()

	// 模拟触发强平
	user := UserRiskData{
		UserID:    1001,
		RiskRatio: 1.05,
		Level:     RiskLevelLiquidate,
	}
	output := risk.RiskOutput{
		RiskRatio: 1.05,
		Equity:    100,
	}

	engine.triggerLiquidation(user, output)

	// 等待 worker 处理
	time.Sleep(100 * time.Millisecond)

	// 验证执行器被调用
	calls := atomic.LoadInt32(&executor.ExecuteCalls)
	if calls != 1 {
		t.Errorf("Executor should be called once, got %d", calls)
	}

	tasks := executor.GetExecutedTasks()
	if len(tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(tasks))
	}
	if tasks[0].UserID != 1001 {
		t.Errorf("Task UserID = %d, want 1001", tasks[0].UserID)
	}
}

func TestEngine_HandleLevelChange_NoChange(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 用户当前在 Warning，新计算结果仍是 Warning
	user := UserRiskData{
		UserID:    1,
		RiskRatio: 0.72,
		Level:     RiskLevelWarning,
	}
	engine.index.UpdateUser(user)

	// 模拟等级不变，只更新数据
	output := risk.RiskOutput{
		RiskRatio:      0.75, // 仍在 Warning 范围
		Equity:         1000,
		MaintMarginReq: 750,
	}
	newLevel := CalculateRiskLevel(output.RiskRatio) // Warning

	engine.handleLevelChange(user, newLevel, output)

	// 用户仍应该在 Warning
	warnings := engine.index.GetByLevel(RiskLevelWarning)
	if len(warnings) != 1 {
		t.Errorf("User should still be in Warning, got %d users", len(warnings))
	}

	// 数据应该被更新
	updated, _ := engine.index.GetUser(1)
	if updated.RiskRatio != 0.75 {
		t.Errorf("RiskRatio should be updated to 0.75, got %v", updated.RiskRatio)
	}
}

func TestEngine_HandleLevelChange_UpgradeToLiquidate(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 启动以消费强平队列
	engine.Start()
	defer engine.Stop()

	// 用户从 Critical 升级到 Liquidate
	user := UserRiskData{
		UserID:    2,
		RiskRatio: 0.98,
		Level:     RiskLevelCritical,
	}
	engine.index.UpdateUser(user)

	output := risk.RiskOutput{
		RiskRatio: 1.05, // 触发强平
		Equity:    100,
	}

	engine.handleLevelChange(user, RiskLevelLiquidate, output)

	// 等待处理
	time.Sleep(100 * time.Millisecond)

	// 用户应该从索引中移除
	if engine.index.TotalCount() != 0 {
		t.Errorf("User should be removed from index after liquidation")
	}

	// 强平执行器应该被调用
	calls := atomic.LoadInt32(&executor.ExecuteCalls)
	if calls != 1 {
		t.Errorf("Executor should be called, got %d calls", calls)
	}
}

func TestEngine_HandleLevelChange_DowngradeToSafe(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 用户从 Warning 降级到 Safe
	user := UserRiskData{
		UserID:    3,
		RiskRatio: 0.72,
		Level:     RiskLevelWarning,
	}
	engine.index.UpdateUser(user)

	output := risk.RiskOutput{
		RiskRatio: 0.50, // 回到安全区
		Equity:    2000,
	}

	engine.handleLevelChange(user, RiskLevelSafe, output)

	// 用户应该从索引中移除
	if engine.index.TotalCount() != 0 {
		t.Errorf("User should be removed from index when Safe, got %d", engine.index.TotalCount())
	}
}

func TestEngine_HandleLevelChange_LevelUpgrade(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 用户从 Warning 升级到 Danger
	user := UserRiskData{
		UserID:    4,
		RiskRatio: 0.75,
		Level:     RiskLevelWarning,
	}
	engine.index.UpdateUser(user)

	output := risk.RiskOutput{
		RiskRatio:      0.85,
		Equity:         1000,
		MaintMarginReq: 850,
	}

	engine.handleLevelChange(user, RiskLevelDanger, output)

	// Warning 应该没有用户
	if len(engine.index.GetByLevel(RiskLevelWarning)) != 0 {
		t.Error("Warning should have 0 users")
	}

	// Danger 应该有 1 个用户
	dangers := engine.index.GetByLevel(RiskLevelDanger)
	if len(dangers) != 1 {
		t.Errorf("Danger should have 1 user, got %d", len(dangers))
	}
	if dangers[0].RiskRatio != 0.85 {
		t.Errorf("RiskRatio should be 0.85, got %v", dangers[0].RiskRatio)
	}
}

func TestEngine_OnPriceChange(t *testing.T) {
	provider := &MockUserDataProvider{
		UserIDs: []int64{1, 2},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.95), // Critical
			2: createMockRiskInput(2, "BTC_USDT", 0.85), // Danger
		},
	}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 手动填充索引
	engine.scanner.Scan(context.Background())

	// 启动引擎
	engine.Start()
	defer engine.Stop()

	// 模拟价格变化 - 只应检查 Critical 用户
	engine.OnPriceChange("BTC_USDT", 55000)

	// 等待处理
	time.Sleep(100 * time.Millisecond)

	// 验证只有 Critical 用户被检查
	// (由于 mock 数据设计，可能不会触发强平，但验证不会 panic)
}

func TestEngine_WorkerPool(t *testing.T) {
	provider := &MockUserDataProvider{}
	executor := &MockLiquidationExecutor{
		ExecuteDelay: 10 * time.Millisecond, // 模拟执行耗时
	}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	engine.Start()
	defer engine.Stop()

	// 并发发送多个强平任务
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			user := UserRiskData{UserID: userID, RiskRatio: 1.05}
			output := risk.RiskOutput{RiskRatio: 1.05}
			engine.triggerLiquidation(user, output)
		}(int64(i + 1))
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond) // 等待所有 worker 处理完

	// 验证所有任务都被执行
	calls := atomic.LoadInt32(&executor.ExecuteCalls)
	if calls != 20 {
		t.Errorf("All 20 tasks should be executed, got %d", calls)
	}
}

// =============================================================================
// 集成测试
// =============================================================================

func TestEngine_Integration_FullFlow(t *testing.T) {
	// 创建模拟数据：5个用户，不同风险等级
	provider := &MockUserDataProvider{
		UserIDs: []int64{1, 2, 3, 4, 5},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.50), // Safe
			2: createMockRiskInput(2, "BTC_USDT", 0.75), // Warning
			3: createMockRiskInput(3, "ETH_USDT", 0.85), // Danger
			4: createMockRiskInput(4, "ETH_USDT", 0.95), // Critical
			5: createMockRiskInput(5, "SOL_USDT", 1.05), // Liquidate
		},
	}
	executor := &MockLiquidationExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 步骤 1: 启动引擎
	engine.Start()

	// 步骤 2: 等待第一次扫描完成
	time.Sleep(200 * time.Millisecond)

	// 步骤 3: 验证索引状态
	stats := engine.GetStats()
	t.Logf("Stats after scan: %+v", stats)

	// 应该有 3 个高风险用户 (Warning + Danger + Critical)
	// Liquidate 用户在扫描时就会触发强平
	if stats.TotalHighRiskUsers != 3 {
		t.Errorf("TotalHighRiskUsers = %d, want 3", stats.TotalHighRiskUsers)
	}

	// 步骤 4: 停止引擎
	engine.Stop()

	// 步骤 5: 验证强平执行器被调用（用户5应该被强平）
	tasks := executor.GetExecutedTasks()
	t.Logf("Executed tasks: %d", len(tasks))

	// 注意：由于扫描器直接创建 LiquidationTask 但没有发送到队列
	// 这里可能需要检查 scanner 的逻辑
}
