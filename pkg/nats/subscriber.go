// 文件: pkg/nats/subscriber.go
// NATS 消息订阅者

package nats

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
)

// MessageHandler 消息处理函数
type MessageHandler func(subject string, data []byte) error

// Subscriber NATS 订阅者
type Subscriber struct {
	conn    *nats.Conn
	subs    []*nats.Subscription
	handler MessageHandler
}

// NewSubscriber 创建订阅者
func NewSubscriber(url string, handler MessageHandler) (*Subscriber, error) {
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}
	return &Subscriber{
		conn:    conn,
		handler: handler,
	}, nil
}

// Subscribe 订阅主题
func (s *Subscriber) Subscribe(subjects ...string) error {
	for _, subject := range subjects {
		sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
			if err := s.handler(msg.Subject, msg.Data); err != nil {
				log.Printf("[NATS] handle error: subject=%s, err=%v", msg.Subject, err)
			}
		})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", subject, err)
		}
		s.subs = append(s.subs, sub)
	}
	return nil
}

// SubscribeQueue 队列订阅 (负载均衡)
func (s *Subscriber) SubscribeQueue(subject, queue string) error {
	sub, err := s.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		if err := s.handler(msg.Subject, msg.Data); err != nil {
			log.Printf("[NATS] handle error: subject=%s, err=%v", msg.Subject, err)
		}
	})
	if err != nil {
		return err
	}
	s.subs = append(s.subs, sub)
	return nil
}

// Close 关闭
func (s *Subscriber) Close() error {
	for _, sub := range s.subs {
		sub.Unsubscribe()
	}
	s.conn.Close()
	return nil
}

// =============================================================================
// 便捷方法
// =============================================================================

// UnmarshalJSON 反序列化 JSON
func UnmarshalJSON[T any](data []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
