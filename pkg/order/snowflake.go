// 文件: pkg/order/snowflake.go
// 雪花算法 ID 生成器
// 使用开源库: github.com/bwmarrin/snowflake

package order

import (
	"sync"

	"github.com/bwmarrin/snowflake"
)

var (
	node     *snowflake.Node
	initOnce sync.Once
)

// InitSnowflake 初始化雪花算法
// nodeID: 节点ID (0-1023)
func InitSnowflake(nodeID int64) error {
	var err error
	initOnce.Do(func() {
		node, err = snowflake.NewNode(nodeID)
	})
	return err
}

// GenerateOrderID 生成订单ID
func GenerateOrderID() int64 {
	if node == nil {
		// 未初始化则使用默认节点0
		InitSnowflake(0)
	}
	return node.Generate().Int64()
}
