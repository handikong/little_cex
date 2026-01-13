// 文件: pkg/nats/publisher.go
// NATS 消息发布者
// 轻量级替代 Kafka，适合本地开发

package nats

import (
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
)

// Publisher NATS 发布者
type Publisher struct {
	conn *nats.Conn
}

// NewPublisher 创建发布者
func NewPublisher(url string) (*Publisher, error) {
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}
	return &Publisher{conn: conn}, nil
}

// Publish 发布消息
func (p *Publisher) Publish(subject string, data any) error {
	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return p.conn.Publish(subject, bytes)
}

// PublishRaw 发布原始消息
func (p *Publisher) PublishRaw(subject string, data []byte) error {
	return p.conn.Publish(subject, data)
}

// Close 关闭连接
func (p *Publisher) Close() {
	p.conn.Close()
}
