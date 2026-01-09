package liquidation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"max.com/pkg/risk"
)

// =============================================================================
// Mock UserDataProvider
// =============================================================================

// MockUserDataProvider 模拟用户数据提供者
type MockUserDataProvider struct {
	// 用户ID列表
	UserIDs []int64

	// 用户风控输入数据 (userID -> RiskInput)
	UserRiskInputs map[int64]risk.RiskInput

	// 模拟错误
	GetAllUserIDsErr    error
	GetUserRiskInputErr error

	// 调用计数
	GetAllUserIDsCalls    int32
	GetUserRiskInputCalls int32
}

func (m *MockUserDataProvider) GetAllUserIDs(ctx context.Context) ([]int64, error) {
	atomic.AddInt32(&m.GetAllUserIDsCalls, 1)
	if m.GetAllUserIDsErr != nil {
		return nil, m.GetAllUserIDsErr
	}
	return m.UserIDs, nil
}

func (m *MockUserDataProvider) GetUserRiskInput(ctx context.Context, userID int64) (risk.RiskInput, error) {
	atomic.AddInt32(&m.GetUserRiskInputCalls, 1)
	if m.GetUserRiskInputErr != nil {
		return risk.RiskInput{}, m.GetUserRiskInputErr
	}
	if input, ok := m.UserRiskInputs[userID]; ok {
		return input, nil
	}
	return risk.RiskInput{}, errors.New("user not found")
}

// =============================================================================
// 测试辅助函数
// =============================================================================

// createMockRiskInput 创建模拟的风控输入
// riskRatio 目标风险率，会反推出合适的 balance 和 position
func createMockRiskInput(userID int64, symbol string, riskRatio float64) risk.RiskInput {
	// 固定参数
	qty := 1.0
	entryPrice := 50000.0
	markPrice := 50000.0
	mmr := 0.005 // 0.5%

	// 计算需要的 balance 使得风险率 = riskRatio
	// riskRatio = MaintMargin / Equity
	// MaintMargin = qty * markPrice * mmr = 250
	// Equity = Balance + uPnL, 这里 uPnL = 0 (mark = entry)
	// 所以 Balance = MaintMargin / riskRatio
	maintMargin := qty * markPrice * mmr // 250
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

// =============================================================================
// Scanner 单元测试
// =============================================================================

func TestScanner_NewScanner(t *testing.T) {
	index := NewRiskLevelIndex()
	provider := &MockUserDataProvider{}
	riskEngine := risk.NewEngine()

	scanner := NewScanner(index, provider, riskEngine)

	if scanner == nil {
		t.Fatal("NewScanner should not return nil")
	}

	if scanner.numShards != DefaultNumShards {
		t.Errorf("numShards = %d, want %d", scanner.numShards, DefaultNumShards)
	}

	if scanner.scanInterval != DefaultScanInterval {
		t.Errorf("scanInterval = %v, want %v", scanner.scanInterval, DefaultScanInterval)
	}
}

func TestScanner_SetNumShards(t *testing.T) {
	scanner := NewScanner(NewRiskLevelIndex(), &MockUserDataProvider{}, risk.NewEngine())

	scanner.SetNumShards(8)
	if scanner.numShards != 8 {
		t.Errorf("after SetNumShards(8), numShards = %d", scanner.numShards)
	}

	// 无效值不应该改变
	scanner.SetNumShards(0)
	if scanner.numShards != 8 {
		t.Errorf("SetNumShards(0) should not change, got %d", scanner.numShards)
	}

	scanner.SetNumShards(-1)
	if scanner.numShards != 8 {
		t.Errorf("SetNumShards(-1) should not change, got %d", scanner.numShards)
	}
}

func TestScanner_SetScanInterval(t *testing.T) {
	scanner := NewScanner(NewRiskLevelIndex(), &MockUserDataProvider{}, risk.NewEngine())

	scanner.SetScanInterval(10 * time.Second)
	if scanner.scanInterval != 10*time.Second {
		t.Errorf("after SetScanInterval, interval = %v", scanner.scanInterval)
	}

	// 无效值不应该改变
	scanner.SetScanInterval(0)
	if scanner.scanInterval != 10*time.Second {
		t.Errorf("SetScanInterval(0) should not change, got %v", scanner.scanInterval)
	}
}

func TestScanner_ShardUsers(t *testing.T) {
	scanner := NewScanner(NewRiskLevelIndex(), &MockUserDataProvider{}, risk.NewEngine())
	scanner.SetNumShards(4)

	// 测试分片逻辑
	userIDs := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	shards := scanner.shardUsers(userIDs)

	if len(shards) != 4 {
		t.Errorf("should have 4 shards, got %d", len(shards))
	}

	// 验证所有用户都被分配了
	total := 0
	for _, shard := range shards {
		total += len(shard)
	}
	if total != len(userIDs) {
		t.Errorf("total users in shards = %d, want %d", total, len(userIDs))
	}

	// 验证分片一致性：相同 userID % numShards 应该在同一分片
	for _, userID := range userIDs {
		expectedShard := int(userID % 4)
		found := false
		for _, id := range shards[expectedShard] {
			if id == userID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("userID %d should be in shard %d", userID, expectedShard)
		}
	}
}

func TestScanner_Scan_Basic(t *testing.T) {
	index := NewRiskLevelIndex()
	riskEngine := risk.NewEngine()

	// 创建不同风险等级的用户
	provider := &MockUserDataProvider{
		UserIDs: []int64{1, 2, 3, 4, 5},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.50), // Safe
			2: createMockRiskInput(2, "BTC_USDT", 0.75), // Warning
			3: createMockRiskInput(3, "ETH_USDT", 0.85), // Danger
			4: createMockRiskInput(4, "ETH_USDT", 0.95), // Critical
			5: createMockRiskInput(5, "SOL_USDT", 1.10), // Liquidate
		},
	}

	scanner := NewScanner(index, provider, riskEngine)
	scanner.SetNumShards(2)

	// 执行扫描
	scanner.Scan(context.Background())

	// 验证索引结果
	if index.TotalCount() != 3 {
		// 应该有 3 个高风险用户 (Warning, Danger, Critical)
		// Safe 不进索引，Liquidate 会触发强平不存索引
		t.Errorf("TotalCount = %d, want 3", index.TotalCount())
	}

	warnings := index.GetByLevel(RiskLevelWarning)
	if len(warnings) != 1 {
		t.Errorf("Warning users = %d, want 1", len(warnings))
	}

	dangers := index.GetByLevel(RiskLevelDanger)
	if len(dangers) != 1 {
		t.Errorf("Danger users = %d, want 1", len(dangers))
	}

	criticals := index.GetByLevel(RiskLevelCritical)
	if len(criticals) != 1 {
		t.Errorf("Critical users = %d, want 1", len(criticals))
	}
}

func TestScanner_Scan_EmptyUsers(t *testing.T) {
	index := NewRiskLevelIndex()
	provider := &MockUserDataProvider{
		UserIDs:        []int64{},
		UserRiskInputs: map[int64]risk.RiskInput{},
	}

	scanner := NewScanner(index, provider, risk.NewEngine())
	scanner.Scan(context.Background())

	if index.TotalCount() != 0 {
		t.Errorf("TotalCount should be 0 for empty users, got %d", index.TotalCount())
	}
}

func TestScanner_Scan_GetAllUserIDsError(t *testing.T) {
	index := NewRiskLevelIndex()
	provider := &MockUserDataProvider{
		GetAllUserIDsErr: errors.New("database error"),
	}

	scanner := NewScanner(index, provider, risk.NewEngine())
	scanner.Scan(context.Background())

	// 出错时应该不更新索引
	if index.TotalCount() != 0 {
		t.Errorf("TotalCount should be 0 on error, got %d", index.TotalCount())
	}
}

func TestScanner_Scan_SymbolIndexUpdated(t *testing.T) {
	index := NewRiskLevelIndex()
	riskEngine := risk.NewEngine()

	provider := &MockUserDataProvider{
		UserIDs: []int64{1, 2},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.75),
			2: createMockRiskInput(2, "ETH_USDT", 0.85),
		},
	}

	scanner := NewScanner(index, provider, riskEngine)
	scanner.Scan(context.Background())

	// 验证 symbolToUsers 索引被更新
	btcUsers := index.GetUsersBySymbol("BTC_USDT")
	if len(btcUsers) != 1 {
		t.Errorf("BTC_USDT users = %d, want 1", len(btcUsers))
	}

	ethUsers := index.GetUsersBySymbol("ETH_USDT")
	if len(ethUsers) != 1 {
		t.Errorf("ETH_USDT users = %d, want 1", len(ethUsers))
	}
}

func TestScanner_StartStop(t *testing.T) {
	provider := &MockUserDataProvider{
		UserIDs: []int64{1},
		UserRiskInputs: map[int64]risk.RiskInput{
			1: createMockRiskInput(1, "BTC_USDT", 0.75),
		},
	}

	scanner := NewScanner(NewRiskLevelIndex(), provider, risk.NewEngine())
	scanner.SetScanInterval(50 * time.Millisecond) // 快速扫描用于测试

	// 启动
	scanner.Start()

	// 等待几个扫描周期
	time.Sleep(150 * time.Millisecond)

	// 停止
	scanner.Stop()

	// 验证至少调用了几次
	calls := atomic.LoadInt32(&provider.GetAllUserIDsCalls)
	if calls < 2 {
		t.Errorf("GetAllUserIDs should be called multiple times, got %d", calls)
	}

	// 再次启动/停止不应该 panic
	scanner.Start()
	scanner.Stop()
}

func TestScanner_ConvertToUserRiskData(t *testing.T) {
	scanner := NewScanner(NewRiskLevelIndex(), &MockUserDataProvider{}, risk.NewEngine())

	input := risk.RiskInput{
		Positions: []risk.Position{
			{Symbol: "BTC_USDT"},
			{Symbol: "ETH_USDT"},
		},
	}
	output := risk.RiskOutput{
		RiskRatio:      0.85,
		Equity:         10000,
		MaintMarginReq: 8500,
	}

	data := scanner.convertToUserRiskData(12345, input, output, time.Now().UnixNano())

	if data.UserID != 12345 {
		t.Errorf("UserID = %d, want 12345", data.UserID)
	}

	if data.RiskRatio != 0.85 {
		t.Errorf("RiskRatio = %v, want 0.85", data.RiskRatio)
	}

	if data.Level != RiskLevelDanger {
		t.Errorf("Level = %v, want Danger", data.Level)
	}

	if len(data.Symbols) != 2 {
		t.Errorf("Symbols length = %d, want 2", len(data.Symbols))
	}
}
