package alert

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisSubscriptionManager struct {
	client *redis.Client
}

func NewRedisSubscriptionManager(addr string) *RedisSubscriptionManager {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	return &RedisSubscriptionManager{client: rdb}
}

// luaSubscribe 订阅脚本
// KEYS[1]: detailKey (alert:detail:{id})
// KEYS[2]: indexKey (alerts:{symbol}:{direction})
// ARGV[1]: alertID
// ARGV[2]: score (price)
// ARGV[3]: ruleJSON
// ARGV[4]: alertType (int)
const luaSubscribe = `
	redis.call('SET', KEYS[1], ARGV[3])
	-- 拼装 Member: ID:Type (避免查询时反序列化)
	local member = ARGV[1] .. ":" .. ARGV[4]
	redis.call('ZADD', KEYS[2], ARGV[2], member)
	return 1
`

// Subscribe 订阅预警 (Redis Lua 实现)
func (m *RedisSubscriptionManager) Subscribe(ctx context.Context, rule AlertRule) error {
	data, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	// 使用字符串拼接代替 fmt.Sprintf
	detailKey := "alert:detail:" + rule.AlertID
	indexKey := "alerts:" + rule.Symbol + ":" + rule.Direction
	// 传入 Type 以便拼装 Member
	return m.client.Eval(ctx, luaSubscribe, []string{detailKey, indexKey},
		rule.AlertID, rule.Price, data, string(rule.Type)).Err()
}

// luaUnsubscribe 取消订阅脚本
// KEYS[1]: detailKey (alert:detail:{id})
// ARGV[1]: alertID
const luaUnsubscribe = `
	local data = redis.call('GET', KEYS[1])
	if not data then return 0 end

	local rule = cjson.decode(data)
	local symbol = rule["symbol"]
	local direction = rule["direction"]
	local type = rule["type"]
	
	local indexKey = string.format("alerts:%s:%s", symbol, direction)
	
	-- 重组 Member 以便删除
	local member = ARGV[1] .. ":" .. type
	
	redis.call('ZREM', indexKey, member)
	redis.call('DEL', KEYS[1])
	return 1
`

// Unsubscribe 取消订阅 (Redis Lua 实现)
func (m *RedisSubscriptionManager) Unsubscribe(ctx context.Context, alertID string) error {
	detailKey := "alert:detail:" + alertID
	return m.client.Eval(ctx, luaUnsubscribe, []string{detailKey}, alertID).Err()
}

// GetTriggeredAlerts 获取触发的预警 (高性能版: 无需 Unmarshal, 支持方向判断, 处理 AlertOnce)
func (m *RedisSubscriptionManager) GetTriggeredAlerts(ctx context.Context, symbol string, currentPrice, lastPrice float64) ([]AlertRule, error) {
	// 预分配切片容量，减少扩容次数
	triggered := make([]AlertRule, 0, 128)

	// 1. 根据价格走势判断方向
	var direction string
	var min, max string

	if currentPrice > lastPrice {
		// 上涨: 突破上方压力位 (High)
		// 查 Price <= currentPrice 的单子
		direction = "high"
		min = "-inf"
		// 使用 strconv.FormatFloat 代替 fmt.Sprintf，减少内存分配
		max = strconv.FormatFloat(currentPrice, 'f', -1, 64)
	} else if currentPrice < lastPrice {
		// 下跌: 跌破下方支撑位 (Low)
		// 查 Price >= currentPrice 的单子
		direction = "low"
		min = strconv.FormatFloat(currentPrice, 'f', -1, 64)
		max = "+inf"
	} else {
		// 价格没变，直接返回
		return nil, nil
	}
	indexKey := "alerts:" + symbol + ":" + direction

	// 2. 分页参数
	batchSize := 100
	offset := 0

	for {
		opt := &redis.ZRangeBy{
			Min:    min,
			Max:    max,
			Offset: int64(offset),
			Count:  int64(batchSize),
		}

		// 3. 查询 Member 列表 (格式: "ID:Type")
		members, err := m.client.ZRangeByScore(ctx, indexKey, opt).Result()
		if err != nil {
			return nil, err
		}

		if len(members) == 0 {
			break
		}

		// 预分配删除列表容量
		membersToRemove := make([]string, 0, len(members))

		for _, member := range members {
			// 4. 解析 Member (使用 strings.Cut 代替 strings.Split，减少切片分配)
			alertID, typeStr, found := strings.Cut(member, ":")
			if !found {
				continue
			}
			alertType := AlertType(typeStr)

			// 5. 处理 AlertAlways 的冷却时间 (防止打摆子)
			if alertType == AlertAlways {
				cooldownKey := "alert:cooldown:" + alertID
				// SetNX: 如果 Key 不存在则设置成功，存在则失败
				// 默认冷却 60 秒
				allowed, _ := m.client.SetNX(ctx, cooldownKey, "1", 60*time.Second).Result()
				if !allowed {
					continue // 冷却中，跳过
				}
			}

			// 6. 处理 AlertOnce (一次性预警)
			if alertType == AlertOnce {
				// 收集需要删除的 Member，稍后批量删除
				membersToRemove = append(membersToRemove, member)
			}

			// 7. 构造返回对象
			rule := AlertRule{
				AlertID: alertID,
				Type:    alertType,
				Symbol:  symbol,
			}
			triggered = append(triggered, rule)
		}

		// 8. 批量删除 AlertOnce 的索引
		if len(membersToRemove) > 0 {
			// 将 []string 转换为 []interface{} 以满足 ZRem 签名
			args := make([]interface{}, len(membersToRemove))
			for i, v := range membersToRemove {
				args[i] = v
			}
			m.client.ZRem(ctx, indexKey, args...)
		}

		// 9. 准备下一页
		offset += batchSize
	}

	return triggered, nil
}
