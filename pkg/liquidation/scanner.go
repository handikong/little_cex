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
	// DefaultScanInterval 默认全量扫描间隔
	DefaultScanInterval = 5 * time.Second

	// DefaultNumShards 默认分片数量
	// 根据 CPU 核数调整，通常设为核数的 1-2 倍
	DefaultNumShards = 4

	// DefaultBatchSize 每个分片的批处理大小
	DefaultBatchSize = 1000
)

// =============================================================================
// 接口定义
// =============================================================================

// UserDataProvider 用户数据提供者接口
//
// 由外部实现，负责提供用户持仓信息和账户余额
// 扫描器不关心数据从哪里来（数据库、缓存、内存等）
// UserDataProvider 用户数据提供者接口
type UserDataProvider interface {
	// GetAllUserIDs 获取所有持仓用户的ID列表
	GetAllUserIDs(ctx context.Context) ([]int64, error)

	// GetUserRiskInput 获取用户的风控输入数据
	// 直接返回 risk.RiskInput，与现有风控引擎对接
	GetUserRiskInput(ctx context.Context, userID int64) (risk.RiskInput, error)
}

// PriceProvider 价格提供者接口
// 由外部实现，负责提供最新行情价格
type PriceProvider interface {
	// GetPrice 获取指定交易对的最新价格
	GetPrice(symbol string) (float64, error)
}

// =============================================================================
// Scanner 扫描器
// =============================================================================

// Scanner 风险扫描器
//
// 职责:
// 1. 定期全量扫描所有持仓用户
// 2. 计算每个用户的风险率
// 3. 将用户分配到对应的风险等级索引
//
// 设计思想:
// - 使用分片并行处理，加速扫描
// - 全量扫描作为"兜底"，保证数据一致性
// - 增量更新由事件触发（在 engine.go 中实现）
type Scanner struct {
	index        *RiskLevelIndex
	userProvider UserDataProvider
	riskEngine   *risk.Engine // 使用已有的风控引擎
	numShards    int
	scanInterval time.Duration
	running      bool
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// NewScanner 创建新的扫描器
func NewScanner(
	index *RiskLevelIndex,
	userProvider UserDataProvider,
	riskEngine *risk.Engine, // 传入已有的风控引擎
) *Scanner {
	return &Scanner{
		index:        index,
		userProvider: userProvider,
		riskEngine:   riskEngine,
		numShards:    DefaultNumShards,
		scanInterval: DefaultScanInterval,
		stopCh:       make(chan struct{}),
	}
}

// SetNumShards 设置分片数量
func (s *Scanner) SetNumShards(n int) {
	if n > 0 {
		s.numShards = n
	}
}

// SetScanInterval 设置扫描间隔
func (s *Scanner) SetScanInterval(d time.Duration) {
	if d > 0 {
		s.scanInterval = d
	}
}

// =============================================================================
// 扫描器生命周期
// =============================================================================

// Start 启动扫描器
//
// 启动后会在后台定期执行全量扫描
func (s *Scanner) Start() {
	if s.running {
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runLoop()
	}()

	log.Printf("[Scanner] Started with interval=%v, shards=%d",
		s.scanInterval, s.numShards)
}

// Stop 停止扫描器
func (s *Scanner) Stop() {
	if !s.running {
		return
	}
	close(s.stopCh)
	s.wg.Wait()
	s.running = false
	log.Println("[Scanner] Stopped")
}

// runLoop 扫描主循环
func (s *Scanner) runLoop() {
	// 启动时立即执行一次扫描
	s.Scan(context.Background())

	ticker := time.NewTicker(s.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.Scan(context.Background())
		}
	}
}

// =============================================================================
// 核心扫描逻辑
// =============================================================================

// Scan 执行一次全量扫描
//
// 步骤:
// 1. 获取所有持仓用户ID
// 2. 将用户分片
// 3. 并行计算每个分片的风险数据
// 4. 合并结果，按等级分组
// 5. 批量更新索引
func (s *Scanner) Scan(ctx context.Context) {
	startTime := time.Now()

	// 1. 获取所有持仓用户ID
	userIDs, err := s.userProvider.GetAllUserIDs(ctx)
	if err != nil {
		log.Printf("[Scanner] Failed to get user IDs: %v", err)
		return
	}

	if len(userIDs) == 0 {
		log.Println("[Scanner] No users to scan")
		return
	}

	// 2. 将用户分片
	shards := s.shardUsers(userIDs)

	// 3. 并行计算每个分片的风险数据
	results := s.processShards(ctx, shards)

	// 4. 合并结果，按等级分组
	levelWarning := make([]UserRiskData, 0)
	levelDanger := make([]UserRiskData, 0)
	levelCritical := make([]UserRiskData, 0)
	var liquidateTasks []LiquidationTask

	for _, result := range results {
		for _, data := range result {
			switch data.Level {
			case RiskLevelWarning:
				levelWarning = append(levelWarning, data)
			case RiskLevelDanger:
				levelDanger = append(levelDanger, data)
			case RiskLevelCritical:
				levelCritical = append(levelCritical, data)
			case RiskLevelLiquidate:
				// 直接创建强平任务
				liquidateTasks = append(liquidateTasks, LiquidationTask{
					UserID:    data.UserID,
					RiskRatio: data.RiskRatio,
					CreatedAt: time.Now(),
					Priority:  data.RiskRatio,
				})
			}
		}
	}

	// 5. 批量更新索引
	s.index.BatchUpdateLevel(RiskLevelWarning, levelWarning)
	s.index.BatchUpdateLevel(RiskLevelDanger, levelDanger)
	s.index.BatchUpdateLevel(RiskLevelCritical, levelCritical)

	// 更新交易对索引
	allHighRiskUsers := make([]UserRiskData, 0,
		len(levelWarning)+len(levelDanger)+len(levelCritical))
	allHighRiskUsers = append(allHighRiskUsers, levelWarning...)
	allHighRiskUsers = append(allHighRiskUsers, levelDanger...)
	allHighRiskUsers = append(allHighRiskUsers, levelCritical...)
	s.index.UpdateSymbolIndex(allHighRiskUsers)

	// 记录日志
	elapsed := time.Since(startTime)
	log.Printf("[Scanner] Scan completed: users=%d, warning=%d, danger=%d, critical=%d, liquidate=%d, elapsed=%v",
		len(userIDs), len(levelWarning), len(levelDanger),
		len(levelCritical), len(liquidateTasks), elapsed)

	// TODO: 将 liquidateTasks 发送到强平执行器
	// 这部分在 engine.go 中实现
}

// shardUsers 将用户ID分片
//
// 使用取模方式分片，保证同一用户始终在同一分片
// 这样可以避免跨分片的数据一致性问题
func (s *Scanner) shardUsers(userIDs []int64) [][]int64 {
	shards := make([][]int64, s.numShards)
	for i := range shards {
		// 预估每个分片的大小
		shards[i] = make([]int64, 0, len(userIDs)/s.numShards+1)
	}

	for _, userID := range userIDs {
		// 使用 userID % numShards 分配到分片
		shardIdx := int(userID % int64(s.numShards))
		shards[shardIdx] = append(shards[shardIdx], userID)
	}

	return shards
}

// processShards 并行处理所有分片
//
// 每个分片由一个独立的 Goroutine 处理
// 使用 WaitGroup 等待所有分片完成
func (s *Scanner) processShards(ctx context.Context, shards [][]int64) []map[int64]UserRiskData {
	results := make([]map[int64]UserRiskData, s.numShards)
	var wg sync.WaitGroup

	for i, shard := range shards {
		wg.Add(1)
		go func(shardIdx int, userIDs []int64) {
			defer wg.Done()
			results[shardIdx] = s.processShard(ctx, userIDs)
		}(i, shard)
	}

	wg.Wait()
	return results
}

// processShard 处理单个分片
func (s *Scanner) processShard(ctx context.Context, userIDs []int64) map[int64]UserRiskData {
	result := make(map[int64]UserRiskData, len(userIDs))

	for _, userID := range userIDs {
		select {
		case <-ctx.Done():
			return result
		default:
		}

		// 获取用户的风控输入
		riskInput, err := s.userProvider.GetUserRiskInput(ctx, userID)
		if err != nil {
			log.Printf("[Scanner] Failed to get risk input for user %d: %v", userID, err)
			continue
		}

		// 调用已有的风控引擎计算
		riskOutput, err := s.riskEngine.ComputeRisk(riskInput)
		if err != nil {
			log.Printf("[Scanner] Failed to compute risk for user %d: %v", userID, err)
			continue
		}

		// 将 risk.RiskOutput 转换为 UserRiskData
		data := s.convertToUserRiskData(userID, riskInput, riskOutput)

		// 只存储有风险的用户
		if data.Level != RiskLevelSafe {
			result[userID] = data
		}
	}

	return result
}

// convertToUserRiskData 将风控输出转换为用户风险数据
func (s *Scanner) convertToUserRiskData(
	userID int64,
	input risk.RiskInput,
	output risk.RiskOutput,
) UserRiskData {
	// 计算风险等级
	level := CalculateRiskLevel(output.RiskRatio)

	// 提取用户持有的交易对
	symbols := make([]string, 0, len(input.Positions))
	for _, pos := range input.Positions {
		symbols = append(symbols, pos.Symbol)
	}

	return UserRiskData{
		UserID:            userID,
		RiskRatio:         output.RiskRatio,
		Equity:            output.Equity,
		MaintMargin:       output.MaintMarginReq,
		LiquidationPrices: make(map[string]float64), // TODO: 计算各仓位的强平价格
		Level:             level,
		UpdatedAt:         time.Now().UnixNano(),
		Symbols:           symbols,
	}
}
