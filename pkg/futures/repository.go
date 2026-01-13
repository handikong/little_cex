// 文件: pkg/futures/repository.go
// 合约规格存储接口
//
// 【设计模式】Repository Pattern
// - 定义存储操作的抽象接口
// - 业务层只依赖接口，不关心具体实现
// - 方便替换存储引擎、添加缓存层

package futures

import "context"

// ContractRepository 合约规格存储接口
//
// 【面试】为什么用接口而不是直接用 struct?
// 1. 可测试性: 单元测试时可以 mock
// 2. 可替换性: 换数据库只需换实现
// 3. 装饰器模式: 可以嵌套缓存层
type ContractRepository interface {
	// Create 创建合约
	// 如果 symbol 已存在，返回 ErrSymbolExists
	Create(ctx context.Context, spec *ContractSpec) error

	// GetBySymbol 根据 symbol 查询
	// 不存在返回 ErrSymbolNotFound
	GetBySymbol(ctx context.Context, symbol string) (*ContractSpec, error)

	// Update 更新合约 (根据 Symbol)
	Update(ctx context.Context, spec *ContractSpec) error

	// UpdateStatus 更新状态
	UpdateStatus(ctx context.Context, symbol string, from, to ContractStatus) error

	// List 列出所有合约
	List(ctx context.Context) ([]*ContractSpec, error)

	// ListByStatus 按状态查询
	ListByStatus(ctx context.Context, status ContractStatus) ([]*ContractSpec, error)

	// Delete 删除合约 (软删除)
	Delete(ctx context.Context, symbol string) error
}
