// 文件: pkg/fund/model.go
// 冷资产模块 - 事件定义
//
// 定义从热账户传递到冷资产的事件结构
// 这些事件通过 Kafka 传输，由 DBWriter 消费写入 MySQL/ClickHouse

package fund

import (
	"encoding/json"
	"time"
)

// =============================================================================
// 常量定义
// =============================================================================

// 分片数量 (与热账户保持一致)
const NumShards = 128

// Kafka Topic
const (
	TopicBalanceEvents = "asset_balance_events" // 余额变更
	TopicJournalEvents = "asset_journal_events" // 流水事件
)

// ChangeType 变更类型
type ChangeType uint8

const (
	ChangeTypeReserve  ChangeType = 1 // 冻结 (下单)
	ChangeTypeRelease  ChangeType = 2 // 解冻 (撤单)
	ChangeTypeTransfer ChangeType = 3 // 划转 (成交)
	ChangeTypeDeposit  ChangeType = 4 // 充值
	ChangeTypeWithdraw ChangeType = 5 // 提现
	ChangeTypeFee      ChangeType = 6 // 手续费
)

func (t ChangeType) String() string {
	switch t {
	case ChangeTypeReserve:
		return "RESERVE"
	case ChangeTypeRelease:
		return "RELEASE"
	case ChangeTypeTransfer:
		return "TRANSFER"
	case ChangeTypeDeposit:
		return "DEPOSIT"
	case ChangeTypeWithdraw:
		return "WITHDRAW"
	case ChangeTypeFee:
		return "FEE"
	default:
		return "UNKNOWN"
	}
}

// BizType 业务类型
type BizType string

const (
	BizTypeOrder    BizType = "ORDER"    // 订单相关
	BizTypeTrade    BizType = "TRADE"    // 成交相关
	BizTypeDeposit  BizType = "DEPOSIT"  // 充值
	BizTypeWithdraw BizType = "WITHDRAW" // 提现
)

// =============================================================================
// 流水事件 (写入 MySQL + ClickHouse)
// =============================================================================

// JournalEvent 流水事件
// 每次余额变动都会产生一条流水
type JournalEvent struct {
	// ===== 唯一标识 =====
	EventID string `json:"event_id"` // 幂等键 (格式: {type}_{seq}_{user})
	Seq     uint64 `json:"seq"`      // WAL 序列号

	// ===== 用户信息 =====
	UserID int64  `json:"user_id"`
	Symbol string `json:"symbol"`

	// ===== 变更信息 =====
	ChangeType ChangeType `json:"change_type"`
	Amount     int64      `json:"amount"` // 变动金额 (正数)

	// ===== 变更前后余额 =====
	AvailableBefore int64 `json:"available_before"`
	AvailableAfter  int64 `json:"available_after"`
	LockedBefore    int64 `json:"locked_before"`
	LockedAfter     int64 `json:"locked_after"`

	// ===== 关联业务 =====
	BizType BizType `json:"biz_type"` // ORDER/TRADE/DEPOSIT/WITHDRAW
	BizID   string  `json:"biz_id"`   // 订单ID/成交ID/充值ID

	// ===== 时间 =====
	CreatedAt time.Time `json:"created_at"`
}

// ToJSON 序列化为 JSON (供 Kafka 发送)
func (e *JournalEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// FromJSON 从 JSON 反序列化
func (e *JournalEvent) FromJSON(data []byte) error {
	return json.Unmarshal(data, e)
}

// GetShard 获取分片编号 (按 UserID 路由)
func (e *JournalEvent) GetShard() int {
	return int(e.UserID % int64(NumShards))
}

// =============================================================================
// 余额快照事件 (写入 MySQL)
// =============================================================================

// BalanceSnapshot 余额快照
// 用于更新 MySQL 余额表
type BalanceSnapshot struct {
	EventID   string    `json:"event_id"` // 关联的流水 EventID
	UserID    int64     `json:"user_id"`
	Symbol    string    `json:"symbol"`
	Available int64     `json:"available"`
	Locked    int64     `json:"locked"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetShard 获取分片编号
func (s *BalanceSnapshot) GetShard() int {
	return int(s.UserID % int64(NumShards))
}

// =============================================================================
// 数据库模型
// =============================================================================

// BalanceRecord MySQL 余额表记录
type BalanceRecord struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Symbol    string    `db:"symbol"`
	Available int64     `db:"available"`
	Locked    int64     `db:"locked"`
	Version   int       `db:"version"` // 乐观锁
	UpdatedAt time.Time `db:"updated_at"`
}

// TableName 获取分片表名
func (r *BalanceRecord) TableName() string {
	shard := r.UserID % int64(NumShards)
	return "balance_" + shardSuffix(int(shard))
}

// JournalRecord MySQL 流水表记录
type JournalRecord struct {
	ID              int64      `db:"id"`
	EventID         string     `db:"event_id"`
	UserID          int64      `db:"user_id"`
	Symbol          string     `db:"symbol"`
	ChangeType      ChangeType `db:"change_type"`
	Amount          int64      `db:"amount"`
	AvailableBefore int64      `db:"available_before"`
	AvailableAfter  int64      `db:"available_after"`
	LockedBefore    int64      `db:"locked_before"`
	LockedAfter     int64      `db:"locked_after"`
	BizType         BizType    `db:"biz_type"`
	BizID           string     `db:"biz_id"`
	CreatedAt       time.Time  `db:"created_at"`
}

// TableName 获取分片表名
func (r *JournalRecord) TableName() string {
	shard := r.UserID % int64(NumShards)
	return "journal_" + shardSuffix(int(shard))
}

// =============================================================================
// 辅助函数
// =============================================================================

// shardSuffix 生成分片后缀 "000" ~ "127"
func shardSuffix(shard int) string {
	return string([]byte{
		'0' + byte(shard/100),
		'0' + byte((shard/10)%10),
		'0' + byte(shard%10),
	})
}

// GetTableName 获取分片表名
func GetTableName(baseName string, userID int64) string {
	shard := userID % int64(NumShards)
	return baseName + "_" + shardSuffix(int(shard))
}
