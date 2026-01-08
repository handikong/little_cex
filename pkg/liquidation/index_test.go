package liquidation

import (
	"sync"
	"testing"
	"time"
)

// =============================================================================
// CowMap 单元测试
// =============================================================================

func TestCowMap_BasicOperations(t *testing.T) {
	m := NewCowMap()

	// 1. 测试空 Map
	if m.Len() != 0 {
		t.Errorf("new CowMap should be empty, got len=%d", m.Len())
	}

	// 2. 测试 Set
	data := UserRiskData{
		UserID:    1001,
		RiskRatio: 0.75,
		Level:     RiskLevelWarning,
		UpdatedAt: time.Now().UnixNano(),
	}
	m.Set(data)

	if m.Len() != 1 {
		t.Errorf("after Set, len should be 1, got %d", m.Len())
	}

	// 3. 测试 Get
	got, ok := m.Get(1001)
	if !ok {
		t.Error("Get should return ok=true for existing user")
	}
	if got.UserID != 1001 || got.RiskRatio != 0.75 {
		t.Errorf("Get returned wrong data: %+v", got)
	}

	// 4. 测试 Get 不存在的用户
	_, ok = m.Get(9999)
	if ok {
		t.Error("Get should return ok=false for non-existing user")
	}

	// 5. 测试 Contains
	if !m.Contains(1001) {
		t.Error("Contains should return true for existing user")
	}
	if m.Contains(9999) {
		t.Error("Contains should return false for non-existing user")
	}

	// 6. 测试 Remove
	m.Remove(1001)
	if m.Len() != 0 {
		t.Errorf("after Remove, len should be 0, got %d", m.Len())
	}
	if m.Contains(1001) {
		t.Error("after Remove, Contains should return false")
	}
}

func TestCowMap_GetAll(t *testing.T) {
	m := NewCowMap()

	// 添加多个用户
	users := []UserRiskData{
		{UserID: 1, RiskRatio: 0.70},
		{UserID: 2, RiskRatio: 0.80},
		{UserID: 3, RiskRatio: 0.90},
	}

	for _, u := range users {
		m.Set(u)
	}

	// GetAll 返回所有数据
	all := m.GetAll()
	if len(all) != 3 {
		t.Errorf("GetAll should return 3 users, got %d", len(all))
	}

	// 验证返回的数据包含所有用户
	found := make(map[int64]bool)
	for _, u := range all {
		found[u.UserID] = true
	}
	for _, u := range users {
		if !found[u.UserID] {
			t.Errorf("user %d not found in GetAll result", u.UserID)
		}
	}
}

func TestCowMap_BatchUpdate(t *testing.T) {
	m := NewCowMap()

	// 初始数据
	initial := []UserRiskData{
		{UserID: 1, RiskRatio: 0.70},
		{UserID: 2, RiskRatio: 0.75},
		{UserID: 3, RiskRatio: 0.80},
	}
	m.BatchUpdate(initial, nil)

	if m.Len() != 3 {
		t.Errorf("after initial BatchUpdate, len should be 3, got %d", m.Len())
	}

	// 批量更新：更新 2 个，删除 1 个
	updates := []UserRiskData{
		{UserID: 1, RiskRatio: 0.85}, // 更新
		{UserID: 4, RiskRatio: 0.90}, // 新增
	}
	removes := []int64{3} // 删除

	m.BatchUpdate(updates, removes)

	// 验证结果
	if m.Len() != 3 {
		t.Errorf("after second BatchUpdate, len should be 3, got %d", m.Len())
	}

	// user 1 应该被更新
	if data, ok := m.Get(1); !ok || data.RiskRatio != 0.85 {
		t.Errorf("user 1 should be updated to 0.85, got %+v", data)
	}

	// user 3 应该被删除
	if m.Contains(3) {
		t.Error("user 3 should be removed")
	}

	// user 4 应该被新增
	if !m.Contains(4) {
		t.Error("user 4 should be added")
	}
}

func TestCowMap_ConcurrentRead(t *testing.T) {
	m := NewCowMap()

	// 预填充数据
	for i := int64(1); i <= 100; i++ {
		m.Set(UserRiskData{UserID: i, RiskRatio: float64(i) / 100})
	}

	// 并发读取 (验证无锁读不会 panic)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				m.Get(id)
				m.GetAll()
				m.Len()
				m.Contains(id)
			}
		}(int64(i%100 + 1))
	}
	wg.Wait()
}

func TestCowMap_ConcurrentReadWrite(t *testing.T) {
	m := NewCowMap()

	// 预填充数据
	for i := int64(1); i <= 10; i++ {
		m.Set(UserRiskData{UserID: i, RiskRatio: 0.5})
	}

	var wg sync.WaitGroup

	// 读 Goroutines
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				m.GetAll()
			}
		}()
	}

	// 写 Goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Set(UserRiskData{UserID: id, RiskRatio: float64(j) / 100})
			}
		}(int64(i + 100))
	}

	wg.Wait()

	// 验证数据一致性：Len 应该返回合理的值
	if m.Len() < 10 {
		t.Errorf("after concurrent read/write, len should be >= 10, got %d", m.Len())
	}
}

// =============================================================================
// RiskLevelIndex 单元测试
// =============================================================================

func TestRiskLevelIndex_GetByLevel(t *testing.T) {
	idx := NewRiskLevelIndex()

	// 添加不同等级的用户
	warningUser := UserRiskData{UserID: 1, RiskRatio: 0.75, Level: RiskLevelWarning}
	dangerUser := UserRiskData{UserID: 2, RiskRatio: 0.85, Level: RiskLevelDanger}
	criticalUser := UserRiskData{UserID: 3, RiskRatio: 0.95, Level: RiskLevelCritical}

	idx.UpdateUser(warningUser)
	idx.UpdateUser(dangerUser)
	idx.UpdateUser(criticalUser)

	// 验证各等级
	warnings := idx.GetByLevel(RiskLevelWarning)
	if len(warnings) != 1 || warnings[0].UserID != 1 {
		t.Errorf("GetByLevel(Warning) wrong: %+v", warnings)
	}

	dangers := idx.GetByLevel(RiskLevelDanger)
	if len(dangers) != 1 || dangers[0].UserID != 2 {
		t.Errorf("GetByLevel(Danger) wrong: %+v", dangers)
	}

	criticals := idx.GetByLevel(RiskLevelCritical)
	if len(criticals) != 1 || criticals[0].UserID != 3 {
		t.Errorf("GetByLevel(Critical) wrong: %+v", criticals)
	}

	// Safe 和 Liquidate 不应该有数据
	safes := idx.GetByLevel(RiskLevelSafe)
	if len(safes) != 0 {
		t.Errorf("GetByLevel(Safe) should be empty, got %+v", safes)
	}
}

func TestRiskLevelIndex_UpdateUser_LevelChange(t *testing.T) {
	idx := NewRiskLevelIndex()

	// 用户初始在 Warning
	user := UserRiskData{UserID: 1, RiskRatio: 0.75}
	idx.UpdateUser(user)

	if len(idx.GetByLevel(RiskLevelWarning)) != 1 {
		t.Error("user should be in Warning level initially")
	}

	// 用户升级到 Danger
	user.RiskRatio = 0.85
	idx.UpdateUser(user)

	if len(idx.GetByLevel(RiskLevelWarning)) != 0 {
		t.Error("user should be removed from Warning level")
	}
	if len(idx.GetByLevel(RiskLevelDanger)) != 1 {
		t.Error("user should be in Danger level now")
	}

	// 用户降级到 Safe (应该从索引中移除)
	user.RiskRatio = 0.50
	idx.UpdateUser(user)

	if idx.TotalCount() != 0 {
		t.Errorf("user should be removed when Safe, total=%d", idx.TotalCount())
	}
}

func TestRiskLevelIndex_GetUser(t *testing.T) {
	idx := NewRiskLevelIndex()

	// 添加用户到不同等级
	idx.UpdateUser(UserRiskData{UserID: 1, RiskRatio: 0.75})
	idx.UpdateUser(UserRiskData{UserID: 2, RiskRatio: 0.85})
	idx.UpdateUser(UserRiskData{UserID: 3, RiskRatio: 0.95})

	// GetUser 应该能找到任意等级的用户
	if user, ok := idx.GetUser(1); !ok || user.UserID != 1 {
		t.Error("GetUser should find user 1")
	}
	if user, ok := idx.GetUser(2); !ok || user.UserID != 2 {
		t.Error("GetUser should find user 2")
	}
	if user, ok := idx.GetUser(3); !ok || user.UserID != 3 {
		t.Error("GetUser should find user 3")
	}

	// 不存在的用户
	if _, ok := idx.GetUser(999); ok {
		t.Error("GetUser should return false for non-existing user")
	}
}

func TestRiskLevelIndex_BatchUpdateLevel(t *testing.T) {
	idx := NewRiskLevelIndex()

	// 初始添加一些 Warning 用户
	idx.UpdateUser(UserRiskData{UserID: 1, RiskRatio: 0.71})
	idx.UpdateUser(UserRiskData{UserID: 2, RiskRatio: 0.72})
	idx.UpdateUser(UserRiskData{UserID: 3, RiskRatio: 0.73})

	// 批量替换 Warning 等级的所有数据
	newUsers := []UserRiskData{
		{UserID: 4, RiskRatio: 0.74, Level: RiskLevelWarning},
		{UserID: 5, RiskRatio: 0.75, Level: RiskLevelWarning},
	}
	idx.BatchUpdateLevel(RiskLevelWarning, newUsers)

	warnings := idx.GetByLevel(RiskLevelWarning)
	if len(warnings) != 2 {
		t.Errorf("after BatchUpdateLevel, should have 2 warning users, got %d", len(warnings))
	}

	// 原来的用户 1,2,3 应该被移除
	if _, ok := idx.GetUser(1); ok {
		t.Error("user 1 should be removed after BatchUpdateLevel")
	}
}

func TestRiskLevelIndex_SymbolIndex(t *testing.T) {
	idx := NewRiskLevelIndex()

	// 用户持有不同交易对
	user1 := UserRiskData{
		UserID:    1,
		RiskRatio: 0.75,
		Symbols:   []string{"BTC_USDT", "ETH_USDT"},
	}
	user2 := UserRiskData{
		UserID:    2,
		RiskRatio: 0.85,
		Symbols:   []string{"BTC_USDT"},
	}
	user3 := UserRiskData{
		UserID:    3,
		RiskRatio: 0.95,
		Symbols:   []string{"ETH_USDT", "SOL_USDT"},
	}

	// 更新 symbolToUsers 索引
	allUsers := []UserRiskData{user1, user2, user3}
	idx.UpdateSymbolIndex(allUsers)

	// 验证按交易对查询
	btcUsers := idx.GetUsersBySymbol("BTC_USDT")
	if len(btcUsers) != 2 {
		t.Errorf("BTC_USDT should have 2 users, got %d", len(btcUsers))
	}

	ethUsers := idx.GetUsersBySymbol("ETH_USDT")
	if len(ethUsers) != 2 {
		t.Errorf("ETH_USDT should have 2 users, got %d", len(ethUsers))
	}

	solUsers := idx.GetUsersBySymbol("SOL_USDT")
	if len(solUsers) != 1 {
		t.Errorf("SOL_USDT should have 1 user, got %d", len(solUsers))
	}

	// 不存在的交易对
	xrpUsers := idx.GetUsersBySymbol("XRP_USDT")
	if len(xrpUsers) != 0 {
		t.Errorf("XRP_USDT should have 0 users, got %d", len(xrpUsers))
	}
}

func TestRiskLevelIndex_TotalCount(t *testing.T) {
	idx := NewRiskLevelIndex()

	if idx.TotalCount() != 0 {
		t.Error("new index should have TotalCount 0")
	}

	idx.UpdateUser(UserRiskData{UserID: 1, RiskRatio: 0.75})
	idx.UpdateUser(UserRiskData{UserID: 2, RiskRatio: 0.85})
	idx.UpdateUser(UserRiskData{UserID: 3, RiskRatio: 0.95})

	if idx.TotalCount() != 3 {
		t.Errorf("TotalCount should be 3, got %d", idx.TotalCount())
	}
}
