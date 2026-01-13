// 文件: pkg/futures/manager.go
// 合约规格管理器 - 业务逻辑层
//
// 【职责】
// 1. 参数验证
// 2. 业务规则校验
// 3. 调用 Repository 完成存储
// 4. 不关心底层是 MySQL 还是 Redis

package futures

import (
	"context"
	"errors"
	"time"
)

// =============================================================================
// 错误定义
// =============================================================================

var (
	ErrSymbolExists      = errors.New("contract symbol already exists")
	ErrSymbolNotFound    = errors.New("contract symbol not found")
	ErrInvalidSpec       = errors.New("invalid contract specification")
	ErrContractNotActive = errors.New("contract is not active for trading")
	ErrInvalidTransition = errors.New("invalid status transition")
)

// =============================================================================
// ContractManager - 合约管理器
// =============================================================================

// ContractManager 合约规格管理器
//
// 【设计】只依赖 ContractRepository 接口
// - 可以传入 MySQLContractRepository (无缓存)
// - 可以传入 CachedContractRepository (有缓存)
// - 单元测试时可以传入 MockRepository
type ContractManager struct {
	repo ContractRepository
}

// NewContractManager 创建合约管理器
func NewContractManager(repo ContractRepository) *ContractManager {
	return &ContractManager{repo: repo}
}

// =============================================================================
// 创建合约
// =============================================================================

// CreateContractRequest 创建合约请求
type CreateContractRequest struct {
	Symbol         string
	BaseCurrency   string
	QuoteCurrency  string
	SettleCurrency string

	ContractType   ContractType
	ContractSize   int64
	TickSize       int64
	MinOrderQty    int64
	MaxOrderQty    int64
	MaxPositionQty int64

	MaxLeverage       int
	InitialMarginRate int64 // 万分比
	MaintMarginRate   int64 // 万分比

	FundingInterval int64    // 秒
	MaxFundingRate  int64    // 万分比
	PriceSources    []string // 价格来源

	ExpiryAt int64 // 到期时间 (交割合约)
}

// CreateContract 创建新合约
func (m *ContractManager) CreateContract(ctx context.Context, req *CreateContractRequest) (*ContractSpec, error) {
	// 1. 参数验证
	if err := ValidateCreateRequest(req); err != nil {
		return nil, err
	}

	// 2. 构建 Spec
	now := time.Now().UnixMilli()
	spec := &ContractSpec{
		Symbol:            req.Symbol,
		BaseCurrency:      req.BaseCurrency,
		QuoteCurrency:     req.QuoteCurrency,
		SettleCurrency:    req.SettleCurrency,
		ContractType:      req.ContractType,
		ContractSize:      req.ContractSize,
		TickSize:          req.TickSize,
		MinOrderQty:       req.MinOrderQty,
		MaxOrderQty:       req.MaxOrderQty,
		MaxPositionQty:    req.MaxPositionQty,
		MaxLeverage:       req.MaxLeverage,
		InitialMarginRate: req.InitialMarginRate,
		MaintMarginRate:   req.MaintMarginRate,
		FundingInterval:   req.FundingInterval,
		MaxFundingRate:    req.MaxFundingRate,
		PriceSources:      req.PriceSources,
		Status:            StatusPending,
		ExpiryAt:          req.ExpiryAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	// 3. 保存
	if err := m.repo.Create(ctx, spec); err != nil {
		return nil, err
	}

	return spec, nil
}

// =============================================================================
// 查询合约
// =============================================================================

// GetContract 获取合约规格
func (m *ContractManager) GetContract(ctx context.Context, symbol string) (*ContractSpec, error) {
	return m.repo.GetBySymbol(ctx, symbol)
}

// GetTradingContracts 获取所有可交易合约
func (m *ContractManager) GetTradingContracts(ctx context.Context) ([]*ContractSpec, error) {
	return m.repo.ListByStatus(ctx, StatusTrading)
}

// GetAllContracts 获取所有合约
func (m *ContractManager) GetAllContracts(ctx context.Context) ([]*ContractSpec, error) {
	return m.repo.List(ctx)
}

// =============================================================================
// 生命周期管理
// =============================================================================

// ListContract 上线合约 (PENDING -> TRADING)
func (m *ContractManager) ListContract(ctx context.Context, symbol string) error {
	return m.repo.UpdateStatus(ctx, symbol, StatusPending, StatusTrading)
}

// DelistContract 下架合约 (TRADING -> DELISTED)
func (m *ContractManager) DelistContract(ctx context.Context, symbol string) error {
	return m.repo.UpdateStatus(ctx, symbol, StatusTrading, StatusDelisted)
}

// StartSettlement 开始交割 (TRADING -> SETTLING)
func (m *ContractManager) StartSettlement(ctx context.Context, symbol string) error {
	return m.repo.UpdateStatus(ctx, symbol, StatusTrading, StatusSettling)
}

// FinishSettlement 完成交割 (SETTLING -> SETTLED)
func (m *ContractManager) FinishSettlement(ctx context.Context, symbol string) error {
	return m.repo.UpdateStatus(ctx, symbol, StatusSettling, StatusSettled)
}

// =============================================================================
// 更新合约参数
// =============================================================================

// UpdateLeverage 更新最大杠杆
func (m *ContractManager) UpdateLeverage(ctx context.Context, symbol string, maxLeverage int) error {
	spec, err := m.repo.GetBySymbol(ctx, symbol)
	if err != nil {
		return err
	}

	if maxLeverage <= 0 || maxLeverage > 200 {
		return errors.New("invalid leverage: must be between 1 and 200")
	}

	spec.MaxLeverage = maxLeverage
	spec.InitialMarginRate = int64(RatePrecision / maxLeverage) // 1/杠杆

	return m.repo.Update(ctx, spec)
}
