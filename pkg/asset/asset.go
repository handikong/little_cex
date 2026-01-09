package asset

import (
	"errors"
	"sync"
)

// =============================================================================
// Errors
// =============================================================================

var (
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrInsufficientFrozen  = errors.New("insufficient frozen balance")
	ErrAccountNotFound     = errors.New("account not found")
	ErrInvalidAmount       = errors.New("invalid amount")
)

// =============================================================================
// Models
// =============================================================================

// Balance 表示单个资产的余额状态
type Balance struct {
	// Available 可用余额 (可用于下单、提现)
	Available float64

	// Frozen 冻结余额 (已下单未成交)
	Frozen float64
}

// Total 返回总资产 (可用 + 冻结)
func (b *Balance) Total() float64 {
	return b.Available + b.Frozen
}

// Account 表示一个用户的账户，包含多种资产
type Account struct {
	UserID   int64
	Balances map[string]*Balance // Symbol -> Balance (e.g., "BTC", "USDT")
	mu       sync.RWMutex
}

// NewAccount 创建新账户
func NewAccount(userID int64) *Account {
	return &Account{
		UserID:   userID,
		Balances: make(map[string]*Balance),
	}
}

// GetBalance 获取指定资产的余额 (线程安全)
func (a *Account) GetBalance(symbol string) (available, frozen float64) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if bal, ok := a.Balances[symbol]; ok {
		return bal.Available, bal.Frozen
	}
	return 0, 0
}

// =============================================================================
// Manager (Asset Manager)
// =============================================================================

// Manager 资产管理器
// 负责管理所有用户的资产，提供充提、冻结、解冻、结算等原子操作
type Manager struct {
	accounts map[int64]*Account
	mu       sync.RWMutex
}

// NewManager 创建资产管理器
func NewManager() *Manager {
	return &Manager{
		accounts: make(map[int64]*Account),
	}
}

// GetAccount 获取用户账户 (如果不存在则创建)
func (m *Manager) GetAccount(userID int64) *Account {
	m.mu.RLock()
	acc, ok := m.accounts[userID]
	m.mu.RUnlock()

	if ok {
		return acc
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double check
	if acc, ok = m.accounts[userID]; ok {
		return acc
	}
	acc = NewAccount(userID)
	m.accounts[userID] = acc
	return acc
}

// Deposit 充值 (增加可用余额)
func (m *Manager) Deposit(userID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	acc := m.GetAccount(userID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	if _, ok := acc.Balances[symbol]; !ok {
		acc.Balances[symbol] = &Balance{}
	}
	acc.Balances[symbol].Available += amount
	return nil
}

// Withdraw 提现 (扣减可用余额)
func (m *Manager) Withdraw(userID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	acc := m.GetAccount(userID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	bal, ok := acc.Balances[symbol]
	if !ok || bal.Available < amount {
		return ErrInsufficientBalance
	}

	bal.Available -= amount
	return nil
}

// Freeze 冻结资产 (可用 -> 冻结)
// 通常在下单前调用
func (m *Manager) Freeze(userID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	acc := m.GetAccount(userID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	bal, ok := acc.Balances[symbol]
	if !ok || bal.Available < amount {
		return ErrInsufficientBalance
	}

	bal.Available -= amount
	bal.Frozen += amount
	return nil
}

// Unfreeze 解冻资产 (冻结 -> 可用)
// 通常在撤单或部分成交后调用
func (m *Manager) Unfreeze(userID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	acc := m.GetAccount(userID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	bal, ok := acc.Balances[symbol]
	if !ok || bal.Frozen < amount {
		return ErrInsufficientFrozen
	}

	bal.Frozen -= amount
	bal.Available += amount
	return nil
}

// DeductFrozen 扣除冻结资产
// 通常在成交时调用 (卖方扣除资产)
func (m *Manager) DeductFrozen(userID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	acc := m.GetAccount(userID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	bal, ok := acc.Balances[symbol]
	if !ok || bal.Frozen < amount {
		return ErrInsufficientFrozen
	}

	bal.Frozen -= amount
	return nil
}

// Transfer 内部转账 (原子操作)
// fromUser -> toUser
func (m *Manager) Transfer(fromUserID, toUserID int64, symbol string, amount float64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	fromAcc := m.GetAccount(fromUserID)
	toAcc := m.GetAccount(toUserID)

	// 防止死锁：按 UserID 顺序加锁
	if fromUserID < toUserID {
		fromAcc.mu.Lock()
		toAcc.mu.Lock()
	} else {
		toAcc.mu.Lock()
		fromAcc.mu.Lock()
	}
	defer fromAcc.mu.Unlock()
	defer toAcc.mu.Unlock()

	// 检查余额
	fromBal, ok := fromAcc.Balances[symbol]
	if !ok || fromBal.Available < amount {
		return ErrInsufficientBalance
	}

	// 执行转账
	fromBal.Available -= amount

	if _, ok := toAcc.Balances[symbol]; !ok {
		toAcc.Balances[symbol] = &Balance{}
	}
	toAcc.Balances[symbol].Available += amount

	return nil
}
