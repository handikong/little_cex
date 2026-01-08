package liquidation

import (
	"sync"
	"sync/atomic"
)

// =============================================================================
// CowMap - Copy-on-Write Map
// =============================================================================

// CowMap Copy-on-Write Map
//
// 核心特性:
// 1. 读操作完全无锁 (Lock-Free Read)
// 2. 写操作会加锁，但不阻塞读操作
// 3. 适用于读多写少的场景
//
// 工作原理:
// - 内部维护一个指向 Map 的原子指针
// - 读取时，直接原子加载指针，读取 Map 内容
// - 写入时，先复制一份旧 Map，在副本上修改，然后原子替换指针
// - 旧 Map 在没有读者引用后，会被 GC 自动回收
//
// 为什么这样设计？
// - 传统读写锁 (RWMutex)：写操作会阻塞所有读操作
// - Copy-on-Write：写操作不影响正在进行的读操作
// - 在我们的场景中，读操作（检查器）每秒上千次，写操作（全量更新）只有几次
//
// 注意事项:
// - 写操作会复制整个 Map，内存开销较大
// - 适合 Map 较小 (< 10000 条) 的场景
// - 我们的高风险用户通常只有几百到几千，非常适合

type CowMap struct {
	// data: 原子指针，指向当前的 Map
	//
	// 使用 atomic.Pointer 而非 atomic.Value 的原因：
	// - atomic.Pointer 是泛型，类型更安全 (Go 1.19+)
	// - atomic.Value 需要类型断言，容易出错
	//  userId->UserRiskData
	data atomic.Pointer[map[int64]UserRiskData]

	// writeMu: 写锁
	//
	// 只保护写操作之间的互斥，不影响读操作
	// 防止多个 Goroutine 同时复制、同时替换，导致数据丢失
	writeMu sync.Mutex
}

// NewCowMap 创建新的 CowMap
func NewCowMap() *CowMap {
	m := &CowMap{}
	// 初始化一个空 Map
	emptyMap := make(map[int64]UserRiskData)
	m.data.Store(&emptyMap)
	return m
}

// =============================================================================
// 读操作 (无锁!)
// =============================================================================

// Get 获取指定用户的风险数据
//
// 参数:
//
//	userID: 用户ID
//
// 返回:
//
//	data: 用户风险数据
//	ok: 是否存在
//
// 特性:
//   - 完全无锁
//   - 可被多个 Goroutine 并发调用
//   - 读取的是调用时的快照，即使同时有写操作也不受影响
func (m *CowMap) Get(userID int64) (UserRiskData, bool) {
	// 原子加载当前 Map 的指针
	// 这个操作是原子的，返回的是一个稳定的指针
	currentMap := m.data.Load()

	// 从 Map 中读取
	// 注意：这里读取的 Map 在读取期间不会被修改
	// 因为写操作修改的是副本，不是这个 Map
	data, ok := (*currentMap)[userID]
	return data, ok
}

// GetAll 获取所有用户的风险数据
//
// 返回:
//
//	所有用户风险数据的切片
//
// 特性:
//   - 完全无锁
//   - 返回的是调用时的快照
func (m *CowMap) GetAll() []UserRiskData {
	currentMap := m.data.Load()

	// 预分配切片容量，避免多次扩容
	result := make([]UserRiskData, 0, len(*currentMap))
	for _, v := range *currentMap {
		result = append(result, v)
	}
	return result
}

// Len 获取 Map 的大小
//
// 返回:
//
//	当前 Map 中的用户数量
func (m *CowMap) Len() int {
	currentMap := m.data.Load()
	return len(*currentMap)
}

// Contains 检查用户是否存在
func (m *CowMap) Contains(userID int64) bool {
	currentMap := m.data.Load()
	_, ok := (*currentMap)[userID]
	return ok
}

// BatchUpdate 批量更新用户数据
//
// 参数:
//
//	updates: 要更新或新增的用户数据
//	removes: 要删除的用户ID列表
//
// 特性:
//   - 写操作之间互斥（通过 writeMu）
//   - 不阻塞读操作
//   - 原子替换，读者要么看到旧数据，要么看到新数据，不会看到中间状态
//
// 工作流程:
//  1. 加写锁
//  2. 加载当前 Map
//  3. 创建新 Map (深拷贝)
//  4. 在新 Map 上应用更新和删除
//  5. 原子替换指针
//  6. 释放写锁
//  7. 旧 Map 等待 GC 回收
func (m *CowMap) BatchUpdate(updates []UserRiskData, removes []int64) {
	// 1. 加写锁
	//   防止多个写操作同时进行
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	// 2. 加载当前 Map
	oldMap := m.data.Load()

	// 3. 创建新 Map (深拷贝)
	//    这是 Copy-on-Write 的核心步骤！
	//    新 Map 的容量 = 旧 Map 大小 + 更新数量（预估，避免扩容）
	newMap := make(map[int64]UserRiskData, len(*oldMap)+len(updates))

	// 复制旧数据到新 Map
	for k, v := range *oldMap {
		newMap[k] = v
	}

	// 4.1 应用删除
	//     先删除再更新，避免删除新增的数据
	for _, userID := range removes {
		delete(newMap, userID)
	}

	// 4.2 应用更新
	for _, data := range updates {
		newMap[data.UserID] = data
	}

	// 5. 原子替换指针
	//    这一步是瞬时完成的（纳秒级）
	//    - 正在进行的读操作仍然读取旧 Map，不受影响
	//    - 之后的读操作会读取新 Map
	m.data.Store(&newMap)

	// 6. 释放写锁（通过 defer）
	// 7. 旧 Map 会在没有读者引用后被 GC 回收
}

// Set 设置单个用户数据
//
// 注意：频繁调用此方法会产生大量复制，不推荐
// 推荐使用 BatchUpdate 批量操作
func (m *CowMap) Set(data UserRiskData) {
	m.BatchUpdate([]UserRiskData{data}, nil)
}

// Remove 删除单个用户
//
// 注意：同上，推荐 BatchUpdate
func (m *CowMap) Remove(userID int64) {
	m.BatchUpdate(nil, []int64{userID})
}

// =============================================================================
// RiskLevelIndex - 风险等级索引
// =============================================================================

// RiskLevelIndex 风险等级索引
//
// 管理所有风险等级的用户数据
// 每个等级使用独立的 CowMap，互不影响
//
// 结构:
//
//	levels[0] = Level 1 (Warning, 70%-80%)
//	levels[1] = Level 2 (Danger, 80%-90%)
//	levels[2] = Level 3 (Critical, 90%-100%)
//
// 为什么不存储 Safe 和 Liquidate？
//   - Safe: 用户数量太多（几万），不需要频繁检查
//   - Liquidate: 触发后立即进入强平队列，不需要存储
type RiskLevelIndex struct {
	// levels: 各等级的用户索引
	//   index 0 = Warning
	//   index 1 = Danger
	//   index 2 = Critical
	levels [3]*CowMap

	// symbolToUsers: 交易对 → 用户ID 列表
	// 用于：行情变化时，快速找到持有该交易对的高风险用户
	//
	// 例如：BTC 价格变化时，只需要检查持有 BTC 的用户
	// 而不是检查所有高风险用户
	symbolToUsers atomic.Pointer[map[string][]int64]

	// 新增：userId -> level 的快速查找索引
	userLevelIndex atomic.Pointer[map[int64]RiskLevel]

	// symbolMu: 保护 symbolToUsers 的更新
	symbolMu sync.Mutex
}

// NewRiskLevelIndex 创建新的风险等级索引
func NewRiskLevelIndex() *RiskLevelIndex {
	idx := &RiskLevelIndex{
		levels: [3]*CowMap{
			NewCowMap(), // Warning
			NewCowMap(), // Danger
			NewCowMap(), // Critical
		},
	}

	// 初始化 symbolToUsers
	emptySymbolMap := make(map[string][]int64)
	idx.symbolToUsers.Store(&emptySymbolMap)

	// 初始化 userLevelIndex
	emptyUserLevelMap := make(map[int64]RiskLevel)
	idx.userLevelIndex.Store(&emptyUserLevelMap)

	return idx
}

// levelToIndex 将 RiskLevel 转换为 levels 数组的索引
func levelToIndex(level RiskLevel) int {
	switch level {
	case RiskLevelWarning:
		return 0
	case RiskLevelDanger:
		return 1
	case RiskLevelCritical:
		return 2
	default:
		return -1 // Safe 或 Liquidate，不存储
	}
}

// GetByLevel 获取指定等级的所有用户
func (idx *RiskLevelIndex) GetByLevel(level RiskLevel) []UserRiskData {
	i := levelToIndex(level)
	if i < 0 {
		return nil
	}
	return idx.levels[i].GetAll()
}

// GetUser 获取指定用户（从所有等级中查找）
func (idx *RiskLevelIndex) GetUser(userID int64) (UserRiskData, bool) {
	levelMap := idx.userLevelIndex.Load()
	level, ok := (*levelMap)[userID]
	if !ok {
		return UserRiskData{}, false
	}

	// 2. 直接去对应 level 查找，O(1)
	i := levelToIndex(level)
	if i < 0 {
		return UserRiskData{}, false
	}
	return idx.levels[i].Get(userID)

}

// UpdateUser 更新用户数据（自动处理等级变化）
//
// 逻辑:
//  1. 根据新的风险率计算新等级
//  2. 如果等级变化，从旧等级移除，加入新等级
//  3. 如果等级不变，直接更新数据
func (idx *RiskLevelIndex) UpdateUser(data UserRiskData) {
	newLevel := CalculateRiskLevel(data.RiskRatio)
	newIndex := levelToIndex(newLevel)

	// 从所有等级中移除（如果存在）
	for i, level := range idx.levels {
		if level.Contains(data.UserID) {
			if i != newIndex {
				// 等级变化，需要移除
				level.Remove(data.UserID)
			}
		}
	}

	// 加入新等级（如果不是 Safe 或 Liquidate）
	if newIndex >= 0 {
		data.Level = newLevel
		// 同步更新 userLevelIndex
		idx.updateUserLevelIndex(data.UserID, newLevel)
		idx.levels[newIndex].Set(data)
	}

}

func (idx *RiskLevelIndex) updateUserLevelIndex(userID int64, level RiskLevel) {
	idx.symbolMu.Lock()
	defer idx.symbolMu.Unlock()

	oldMap := idx.userLevelIndex.Load()
	newMap := make(map[int64]RiskLevel, len(*oldMap)+1)
	for k, v := range *oldMap {
		newMap[k] = v
	}

	if level == RiskLevelSafe || level == RiskLevelLiquidate {
		delete(newMap, userID) // 安全或强平，从索引移除
	} else {
		newMap[userID] = level
	}

	idx.userLevelIndex.Store(&newMap)
}

// BatchUpdateLevel 批量更新指定等级的数据
//
// 用于全量扫描后的批量更新
// 直接替换整个等级的数据，而不是逐个更新
func (idx *RiskLevelIndex) BatchUpdateLevel(level RiskLevel, users []UserRiskData) {
	i := levelToIndex(level)
	if i < 0 {
		return
	}

	// 提取要删除的用户（现在在这个等级，但不在新数据中）
	currentUsers := idx.levels[i].GetAll()
	newUserSet := make(map[int64]struct{}, len(users))
	for _, u := range users {
		newUserSet[u.UserID] = struct{}{}
	}

	var removes []int64
	for _, u := range currentUsers {
		if _, exists := newUserSet[u.UserID]; !exists {
			removes = append(removes, u.UserID)
		}
	}

	// 批量更新
	idx.levels[i].BatchUpdate(users, removes)
}

// GetUsersBySymbol 获取持有指定交易对的高风险用户
//
// 用于：行情变化时，快速找到受影响的用户
func (idx *RiskLevelIndex) GetUsersBySymbol(symbol string) []int64 {
	symbolMap := idx.symbolToUsers.Load()
	if users, ok := (*symbolMap)[symbol]; ok {
		return users
	}
	return nil
}

// UpdateSymbolIndex 更新交易对索引
//
// 在全量扫描后调用，重建交易对 → 用户的映射
func (idx *RiskLevelIndex) UpdateSymbolIndex(allUsers []UserRiskData) {
	idx.symbolMu.Lock()
	defer idx.symbolMu.Unlock()

	// 构建新的映射
	newMap := make(map[string][]int64)
	for _, user := range allUsers {
		for _, symbol := range user.Symbols {
			newMap[symbol] = append(newMap[symbol], user.UserID)
		}
	}

	// 原子替换
	idx.symbolToUsers.Store(&newMap)
}

// TotalCount 获取所有等级的用户总数
func (idx *RiskLevelIndex) TotalCount() int {
	total := 0
	for _, level := range idx.levels {
		total += level.Len()
	}
	return total
}
