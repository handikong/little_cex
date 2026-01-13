// 文件: pkg/futures/cache_repo.go
// 合约规格 Redis 缓存层
//
// 【设计模式】装饰器模式 (Decorator Pattern)
// - 包装底层 Repository，透明添加缓存能力
// - 调用方无感知，只看到 ContractRepository 接口
//
// 【缓存策略】
// - 读: 先查 Redis，miss 则查 DB 并回填
// - 写: 先写 DB，成功后删除缓存 (Cache Aside)

package futures

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 确保实现了接口
var _ ContractRepository = (*CachedContractRepository)(nil)

// =============================================================================
// 缓存配置
// =============================================================================

const (
	// 缓存 Key 前缀
	cacheKeyPrefix = "futures:spec:"

	// 单个合约: futures:spec:symbol:{symbol}
	cacheKeySymbol = cacheKeyPrefix + "symbol:%s"

	// 可交易列表: futures:spec:trading
	cacheKeyTradingList = cacheKeyPrefix + "trading"

	// 所有列表: futures:spec:all
	cacheKeyAllList = cacheKeyPrefix + "all"

	// 缓存过期时间
	cacheTTL = 24 * time.Hour

	// 列表缓存过期时间 (较短，因为可能有状态变化)
	listCacheTTL = 5 * time.Minute
)

// =============================================================================
// CachedContractRepository - 带缓存的 Repository
// =============================================================================

// CachedContractRepository Redis 缓存装饰器
//
// 【面试】为什么用装饰器而不是在 MySQL Repo 里直接加缓存?
// 1. 单一职责: MySQL Repo 只管 DB，Cache Repo 只管缓存
// 2. 可组合: 可以选择用或不用缓存
// 3. 可替换: 换 Memcached 只需新建装饰器
type CachedContractRepository struct {
	repo  ContractRepository // 被装饰的底层 Repository
	redis *redis.Client
}

// NewCachedContractRepository 创建带缓存的 Repository
//
// 用法:
//
//	mysqlRepo := NewMySQLContractRepository(db)
//	cachedRepo := NewCachedContractRepository(mysqlRepo, redisClient)
//	manager := NewContractManager(cachedRepo)  // manager 用缓存版
func NewCachedContractRepository(repo ContractRepository, rds *redis.Client) *CachedContractRepository {
	return &CachedContractRepository{
		repo:  repo,
		redis: rds,
	}
}

// =============================================================================
// 读操作 (带缓存)
// =============================================================================

// GetBySymbol 根据 symbol 查询 (带缓存)
func (r *CachedContractRepository) GetBySymbol(ctx context.Context, symbol string) (*ContractSpec, error) {
	cacheKey := fmt.Sprintf(cacheKeySymbol, symbol)

	// 1. 查缓存
	data, err := r.redis.Get(ctx, cacheKey).Bytes()
	if err == nil {
		var spec ContractSpec
		if json.Unmarshal(data, &spec) == nil {
			return &spec, nil // Cache hit
		}
	}

	// 2. Cache miss, 查底层
	spec, err := r.repo.GetBySymbol(ctx, symbol)
	if err != nil {
		return nil, err
	}

	// 3. 回填缓存 (异步，不阻塞主流程)
	go r.setCache(context.Background(), cacheKey, spec, cacheTTL)

	return spec, nil
}

// ListByStatus 按状态查询 (带缓存)
func (r *CachedContractRepository) ListByStatus(ctx context.Context, status ContractStatus) ([]*ContractSpec, error) {
	// 只缓存 Trading 状态的列表
	if status == StatusTrading {
		return r.getTradingListCached(ctx)
	}
	// 其他状态不缓存，直接查 DB
	return r.repo.ListByStatus(ctx, status)
}

// getTradingListCached 获取可交易列表 (带缓存)
func (r *CachedContractRepository) getTradingListCached(ctx context.Context) ([]*ContractSpec, error) {
	// 1. 查缓存
	data, err := r.redis.Get(ctx, cacheKeyTradingList).Bytes()
	if err == nil {
		var specs []*ContractSpec
		if json.Unmarshal(data, &specs) == nil {
			return specs, nil
		}
	}

	// 2. 查底层
	specs, err := r.repo.ListByStatus(ctx, StatusTrading)
	if err != nil {
		return nil, err
	}

	// 3. 回填
	go r.setCacheList(context.Background(), cacheKeyTradingList, specs, listCacheTTL)

	return specs, nil
}

// List 列出所有合约
func (r *CachedContractRepository) List(ctx context.Context) ([]*ContractSpec, error) {
	// 列表查询不缓存，或使用较短 TTL
	return r.repo.List(ctx)
}

// =============================================================================
// 写操作 (写穿 + 删缓存)
// =============================================================================

// Create 创建合约
func (r *CachedContractRepository) Create(ctx context.Context, spec *ContractSpec) error {
	// 1. 写 DB
	if err := r.repo.Create(ctx, spec); err != nil {
		return err
	}

	// 2. 不需要主动缓存，下次读取时会自动缓存
	// 3. 删除列表缓存 (新增合约可能影响列表)
	r.invalidateListCache(ctx)

	return nil
}

// Update 更新合约
func (r *CachedContractRepository) Update(ctx context.Context, spec *ContractSpec) error {
	if err := r.repo.Update(ctx, spec); err != nil {
		return err
	}

	// 删除缓存
	r.invalidateCache(ctx, spec.Symbol)
	return nil
}

// UpdateStatus 更新状态
func (r *CachedContractRepository) UpdateStatus(ctx context.Context, symbol string, from, to ContractStatus) error {
	if err := r.repo.UpdateStatus(ctx, symbol, from, to); err != nil {
		return err
	}

	// 删除缓存
	r.invalidateCache(ctx, symbol)
	return nil
}

// Delete 删除合约
func (r *CachedContractRepository) Delete(ctx context.Context, symbol string) error {
	if err := r.repo.Delete(ctx, symbol); err != nil {
		return err
	}

	r.invalidateCache(ctx, symbol)
	return nil
}

// =============================================================================
// 缓存操作
// =============================================================================

// setCache 设置缓存
func (r *CachedContractRepository) setCache(ctx context.Context, key string, spec *ContractSpec, ttl time.Duration) {
	data, err := json.Marshal(spec)
	if err != nil {
		return
	}
	r.redis.Set(ctx, key, data, ttl)
}

// setCacheList 设置列表缓存
func (r *CachedContractRepository) setCacheList(ctx context.Context, key string, specs []*ContractSpec, ttl time.Duration) {
	data, err := json.Marshal(specs)
	if err != nil {
		return
	}
	r.redis.Set(ctx, key, data, ttl)
}

// invalidateCache 删除指定合约的缓存
func (r *CachedContractRepository) invalidateCache(ctx context.Context, symbol string) {
	// 删除单个缓存
	r.redis.Del(ctx, fmt.Sprintf(cacheKeySymbol, symbol))
	// 删除列表缓存
	r.invalidateListCache(ctx)
}

// invalidateListCache 删除列表缓存
func (r *CachedContractRepository) invalidateListCache(ctx context.Context) {
	r.redis.Del(ctx, cacheKeyTradingList)
	r.redis.Del(ctx, cacheKeyAllList)
}
