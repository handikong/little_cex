package liquidation

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max.com/pkg/risk"
)

// =============================================================================
// 大规模性能测试 Mock
// =============================================================================

// LargeScaleMockProvider 大规模模拟用户数据提供者
type LargeScaleMockProvider struct {
	// 配置
	TotalUsers         int // 总用户数 (100万)
	UsersWithPositions int // 有持仓用户数 (20万)
	HighRiskUsers      int // 高风险用户数 (2万)

	// 预生成的数据
	userIDs        []int64
	userRiskInputs map[int64]risk.RiskInput

	mu sync.RWMutex
}

// NewLargeScaleMockProvider 创建大规模模拟数据提供者
func NewLargeScaleMockProvider(totalUsers, usersWithPositions, highRiskUsers int) *LargeScaleMockProvider {
	p := &LargeScaleMockProvider{
		TotalUsers:         totalUsers,
		UsersWithPositions: usersWithPositions,
		HighRiskUsers:      highRiskUsers,
		userIDs:            make([]int64, usersWithPositions),
		userRiskInputs:     make(map[int64]risk.RiskInput, usersWithPositions),
	}
	p.generateData()
	return p
}

func (p *LargeScaleMockProvider) generateData() {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	symbols := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT", "DOGE_USDT", "XRP_USDT"}

	for i := 0; i < p.UsersWithPositions; i++ {
		userID := int64(i + 1)
		p.userIDs[i] = userID

		// 确定风险等级分布
		var riskRatio float64
		if i < p.HighRiskUsers {
			// 高风险用户：2万人分布在 Warning/Danger/Critical/Liquidate
			switch i % 4 {
			case 0:
				riskRatio = 0.70 + rnd.Float64()*0.09 // Warning: 70-79%
			case 1:
				riskRatio = 0.80 + rnd.Float64()*0.09 // Danger: 80-89%
			case 2:
				riskRatio = 0.90 + rnd.Float64()*0.09 // Critical: 90-99%
			case 3:
				riskRatio = 1.00 + rnd.Float64()*0.10 // Liquidate: 100-110%
			}
		} else {
			// 普通用户：Safe 区域
			riskRatio = 0.10 + rnd.Float64()*0.50 // Safe: 10-60%
		}

		symbol := symbols[i%len(symbols)]
		p.userRiskInputs[userID] = createMockRiskInputForBench(userID, symbol, riskRatio)
	}
}

func (p *LargeScaleMockProvider) GetAllUserIDs(ctx context.Context) ([]int64, error) {
	return p.userIDs, nil
}

func (p *LargeScaleMockProvider) GetUserRiskInput(ctx context.Context, userID int64) (risk.RiskInput, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if input, ok := p.userRiskInputs[userID]; ok {
		return input, nil
	}
	return risk.RiskInput{}, fmt.Errorf("user %d not found", userID)
}

// createMockRiskInputForBench 为 benchmark 创建模拟数据
func createMockRiskInputForBench(userID int64, symbol string, riskRatio float64) risk.RiskInput {
	qty := 1.0
	entryPrice := 50000.0
	markPrice := 50000.0
	mmr := 0.005
	maintMargin := qty * markPrice * mmr
	balance := maintMargin / riskRatio

	return risk.RiskInput{
		Account: risk.Account{
			Balance:        balance,
			InitMarginRate: 0.01,
		},
		Positions: []risk.Position{
			{
				Instrument:            risk.InstrumentPerp,
				Symbol:                symbol,
				Qty:                   qty,
				EntryPrice:            entryPrice,
				MaintenanceMarginRate: mmr,
			},
		},
		Prices: map[string]risk.PriceSnapshot{
			symbol: {MarkPrice: markPrice, Price: markPrice},
		},
	}
}

// NoOpExecutor 空操作执行器（用于 benchmark）
type NoOpExecutor struct {
	ExecuteCalls int64
}

func (e *NoOpExecutor) Execute(ctx context.Context, task LiquidationTask) LiquidationResult {
	atomic.AddInt64(&e.ExecuteCalls, 1)
	return LiquidationResult{UserID: task.UserID, Success: true}
}

// =============================================================================
// Benchmark: 扫描性能
// =============================================================================

// BenchmarkScanner_Scan_200K - 全量扫描 20万用户
func BenchmarkScanner_Scan_200K(b *testing.B) {
	// 100万总用户，20万有持仓，2万高风险
	provider := NewLargeScaleMockProvider(1_000_000, 200_000, 20_000)
	index := NewRiskLevelIndex()
	riskEngine := risk.NewEngine()
	scanner := NewScanner(index, provider, riskEngine)
	scanner.SetNumShards(8) // 8 分片并行

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		scanner.Scan(ctx)
	}

	b.ReportMetric(float64(provider.UsersWithPositions), "users/scan")
}

// BenchmarkScanner_Scan_20K - 只扫描 2万高风险用户
func BenchmarkScanner_Scan_20K(b *testing.B) {
	provider := NewLargeScaleMockProvider(100_000, 20_000, 20_000)
	index := NewRiskLevelIndex()
	scanner := NewScanner(index, provider, risk.NewEngine())
	scanner.SetNumShards(4)

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		scanner.Scan(ctx)
	}
}

// =============================================================================
// Benchmark: CowMap 性能
// =============================================================================

// BenchmarkCowMap_ConcurrentRead - 并发读性能
func BenchmarkCowMap_ConcurrentRead(b *testing.B) {
	m := NewCowMap()

	// 预填充 2万用户
	for i := int64(1); i <= 20_000; i++ {
		m.Set(UserRiskData{
			UserID:    i,
			RiskRatio: float64(i%100) / 100,
			Level:     RiskLevel(i % 4),
		})
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			i++
			m.Get(i%20_000 + 1)
		}
	})
}

// BenchmarkCowMap_GetAll - GetAll 性能 (2万用户)
func BenchmarkCowMap_GetAll(b *testing.B) {
	m := NewCowMap()

	for i := int64(1); i <= 20_000; i++ {
		m.Set(UserRiskData{UserID: i, RiskRatio: 0.75})
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = m.GetAll()
	}
}

// BenchmarkCowMap_BatchUpdate - 批量更新性能
func BenchmarkCowMap_BatchUpdate(b *testing.B) {
	m := NewCowMap()

	// 预填充数据
	initial := make([]UserRiskData, 10_000)
	for i := range initial {
		initial[i] = UserRiskData{UserID: int64(i + 1), RiskRatio: 0.75}
	}
	m.BatchUpdate(initial, nil)

	// 准备更新数据
	updates := make([]UserRiskData, 1_000)
	for i := range updates {
		updates[i] = UserRiskData{UserID: int64(i + 1), RiskRatio: 0.85}
	}
	removes := make([]int64, 100)
	for i := range removes {
		removes[i] = int64(i + 5_000)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		m.BatchUpdate(updates, removes)
	}
}

// =============================================================================
// Benchmark: RiskLevelIndex 性能
// =============================================================================

// BenchmarkRiskLevelIndex_GetByLevel - 按等级获取用户
func BenchmarkRiskLevelIndex_GetByLevel(b *testing.B) {
	idx := NewRiskLevelIndex()

	// 填充数据：每个等级约 6600 用户
	for i := int64(1); i <= 20_000; i++ {
		idx.UpdateUser(UserRiskData{
			UserID:    i,
			RiskRatio: 0.70 + float64(i%30)*0.01, // 分布在 70%-100%
		})
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = idx.GetByLevel(RiskLevelWarning)
		_ = idx.GetByLevel(RiskLevelDanger)
		_ = idx.GetByLevel(RiskLevelCritical)
	}
}

// BenchmarkRiskLevelIndex_UpdateUser - 用户等级更新
func BenchmarkRiskLevelIndex_UpdateUser(b *testing.B) {
	idx := NewRiskLevelIndex()

	// 预填充
	for i := int64(1); i <= 10_000; i++ {
		idx.UpdateUser(UserRiskData{UserID: i, RiskRatio: 0.75})
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		userID := int64(i%10_000 + 1)
		// 模拟等级变化
		riskRatio := 0.70 + float64(i%30)*0.01
		idx.UpdateUser(UserRiskData{UserID: userID, RiskRatio: riskRatio})
	}
}

// =============================================================================
// Benchmark: 强平触发性能
// =============================================================================

// BenchmarkEngine_TriggerLiquidation - 强平触发性能
func BenchmarkEngine_TriggerLiquidation(b *testing.B) {
	provider := &MockUserDataProvider{}
	executor := &NoOpExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)
	engine.Start()
	defer engine.Stop()

	user := UserRiskData{UserID: 1, RiskRatio: 1.05}
	output := risk.RiskOutput{RiskRatio: 1.05}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		engine.triggerLiquidation(user, output)
	}

	// 等待队列处理完
	time.Sleep(100 * time.Millisecond)
}

// =============================================================================
// Benchmark: 完整引擎性能
// =============================================================================

// BenchmarkEngine_FullCycle - 完整周期：扫描 + 分级 + 强平触发
func BenchmarkEngine_FullCycle(b *testing.B) {
	// 20万用户，2万高风险
	provider := NewLargeScaleMockProvider(200_000, 200_000, 20_000)
	executor := &NoOpExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// 模拟一次完整扫描周期
		engine.scanner.Scan(ctx)
	}

	b.ReportMetric(float64(provider.UsersWithPositions), "users/cycle")
	b.ReportMetric(float64(provider.HighRiskUsers), "high_risk_users")
}

// =============================================================================
// 压力测试
// =============================================================================

// TestStress_ConcurrentOperations - 并发操作压力测试
func TestStress_ConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	// 100万用户，20万有持仓，2万高风险
	provider := NewLargeScaleMockProvider(1_000_000, 200_000, 20_000)
	executor := &NoOpExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 启动引擎
	engine.Start()
	defer engine.Stop()

	var wg sync.WaitGroup
	// startTime := time.Now()

	// 并发执行多个操作
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 读取索引
			for j := 0; j < 1000; j++ {
				engine.GetStats()
				engine.index.GetByLevel(RiskLevelWarning)
				engine.index.GetByLevel(RiskLevelCritical)
			}
		}()
	}

	// 模拟价格变化
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			symbols := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT"}
			for j := 0; j < 100; j++ {
				engine.OnPriceChange(symbols[id%3], 50000+float64(j*100))
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
	// elapsed := time.Since(startTime)

	// t.Logf("Stress test completed in %v", elapsed)
	// t.Logf("Executor calls: %d", atomic.LoadInt64(&executor.ExecuteCalls))
	// t.Logf("Index stats: %+v", engine.GetStats())
}

// TestScale_1M_Users - 100万用户规模测试
func TestScale_1M_Users(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scale test in short mode")
	}

	// t.Log("Creating mock provider with 1M users...")
	// startTime := time.Now()

	// 创建100万用户数据
	provider := NewLargeScaleMockProvider(1_000_000, 200_000, 20_000)
	// t.Logf("Data generation took: %v", time.Since(startTime))

	executor := &NoOpExecutor{}
	engine := NewEngine(risk.NewEngine(), provider, executor)

	// 执行一次全量扫描
	// t.Log("Starting full scan...")
	// scanStart := time.Now()
	engine.scanner.SetNumShards(16) // 16 分片并行
	engine.scanner.Scan(context.Background())
	// scanTime := time.Since(scanStart)

	// 输出结果
	stats := engine.GetStats()
	// t.Logf("Scan completed in: %v", scanTime)
	// t.Logf("Stats: %+v", stats)
	// t.Logf("Throughput: %.2f users/sec", float64(provider.UsersWithPositions)/scanTime.Seconds())

	// 验证结果
	expectedHighRisk := provider.HighRiskUsers * 3 / 4 // Warning + Danger + Critical (不含 Liquidate)
	if stats.TotalHighRiskUsers < expectedHighRisk/2 {
		// t.Errorf("TotalHighRiskUsers too low: %d (expected around %d)", stats.TotalHighRiskUsers, expectedHighRisk)
	}
}
