// Package pubsub provides a Broker that manages Redis Pub/Sub subscriptions
// and message publishing with its own lifecycle, replacing the global
// Register/Subscriptions pattern and the Listener in internal/pkg/redis.
package pubsub

import (
	"context"
	"encoding/json"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Event 是 Redis Pub/Sub 消息的信封结构体，包含事件类型与原始 JSON 负载。
// 所有消息通过 bus:events 单 channel 发布，由事件类型区分路由目标。
type Event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// busEventsChannel 是 Broker 使用的唯一 Redis Pub/Sub channel。
// 所有事件都通过此 channel 发布，由事件信封中的 Type 字段区分路由目标。
const busEventsChannel = "bus:events"

// Handler is a message handler function. It receives the channel name and raw
// JSON payload bytes. Return an error to log a warning; the broker continues
// to the next message.
type Handler func(ctx context.Context, eventType string, payload []byte) error

// Broker manages Redis Pub/Sub subscriptions and message publishing.
// It replaces the global Register()/Subscriptions() pattern and the
// Listener in internal/pkg/redis/ with its own lifecycle.
type Broker struct {
	client      goredis.UniversalClient
	logger      logger.LoggerInterface
	mu          sync.RWMutex
	handlers    map[string]Handler
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopOnce    sync.Once
	readyOnce   sync.Once
	readyCh     chan struct{}
	dispatchSem chan struct{}
	dispatchWG  sync.WaitGroup // in-flight dispatch goroutine 追踪
}

// NewBroker creates a new Broker. The client must be a connected Redis client.
func NewBroker(client goredis.UniversalClient, logger logger.LoggerInterface, lc lifecycle.ManagerInterface) *Broker {
	broker := &Broker{
		client:      client,
		logger:      logger,
		handlers:    make(map[string]Handler),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		readyCh:     make(chan struct{}),
		dispatchSem: make(chan struct{}, maxDispatchConcurrency),
	}

	lc.RegisterTo("cleanup", "pubsub-broker", func(ctx context.Context) error {
		broker.Stop()
		return nil
	})
	go broker.listen(context.Background())
	return broker
}

// Subscribe registers a handler for the given event type. Safe for concurrent use.
func (b *Broker) Subscribe(eventType string, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = handler
}

// Unsubscribe removes the handler for the given event type. Safe for concurrent use.
// Unsubscribing an event type that is not subscribed is a no-op.
func (b *Broker) Unsubscribe(eventType string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers, eventType)
}

// Ready 返回一个在订阅建立(SUBSCRIBE 成功下发)后关闭的 channel。
// 调用方若需在 Broker 构造后立即发布消息,应先等待该 channel,
// 否则消息可能因订阅尚未建立而丢失。select 超时由调用方自行决定。
func (b *Broker) Ready() <-chan struct{} { return b.readyCh }

// Publish 将 payload 序列化为 JSON 后包装在 Event 信封中，发布到 bus:events channel。
func (b *Broker) Publish(ctx context.Context, eventType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := Event{Type: eventType, Payload: raw}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, busEventsChannel, data).Err()
}

// listen 订阅 bus:events 单 channel，在单层循环中处理消息。
// 阻塞直到 Stop() 被调用或 ctx 被取消。
// 连接断开时使用指数退避自动重连。
func (b *Broker) listen(ctx context.Context) error {
	defer close(b.doneCh)

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		pubsub := b.client.Subscribe(ctx, busEventsChannel)
		ch := pubsub.Channel()

		b.logger.Info("pubsub broker: subscribed to bus:events")
		// SUBSCRIBE 已下发成功才关闭 readyCh。此前没有任何就绪信号,
		// 调用方(尤其是测试)在 NewBroker 之后立刻 Publish 时,
		// SUBSCRIBE 可能尚未到达 redis-server —— 消息被投递给零个订阅者
		// 后静默丢失,表现为间歇性"handler 未收到消息"。
		b.readyOnce.Do(func() { close(b.readyCh) })

	innerLoop:
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					_ = pubsub.Close()
					b.logger.Warn("pubsub broker: channel closed, reconnecting...")
					break innerLoop
				}
				b.dispatchAsync(ctx, msg)
				// 成功接收消息表示连接健康，重置退避
				backoff = time.Second

			case <-b.stopCh:
				_ = pubsub.Close()
				b.logger.Info("pubsub broker: stopped")
				return nil

			case <-ctx.Done():
				_ = pubsub.Close()
				return ctx.Err()
			}
		}

		select {
		case <-b.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// maxDispatchConcurrency 限制同时在跑的 handler 数。Pub/Sub 只是"通知"
// 通道,丢失或延迟均可由各方自己的兜底机制(如 scheduler 的 DB resync)
// 校正 —— 宁可丢弃积压也不无限开 goroutine。
const maxDispatchConcurrency = 64

// dispatchAsync 在独立 goroutine 中处理消息,消除接收循环的队头阻塞:
// 此前 dispatch 同步执行,一个慢 handler(如 job.changed 要查一次 DB)
// 会卡住整条消息管道,拖慢所有事件类型,channel 缓冲(100)耗尽后
// go-redis 直接丢消息。
//
// 槽位获取是非阻塞的:信号量饱和时直接丢弃本条消息并告警,而不是
// 让 listen 循环等待槽位——后者会在饱和时重新引入队头阻塞。Pub/Sub
// 只是"通知"通道,丢失的消息由各消费方自己的兜底机制(如 scheduler
// 的 DB resync)校正,因此宁可丢弃积压也不阻塞接收。
func (b *Broker) dispatchAsync(ctx context.Context, msg *goredis.Message) {
	select {
	case b.dispatchSem <- struct{}{}:
	default:
		b.logger.Warn("pubsub broker: dispatch saturated, message dropped",
			zap.String("channel", msg.Channel))
		return
	}
	b.dispatchWG.Add(1)
	go func() {
		defer func() {
			<-b.dispatchSem
			b.dispatchWG.Done()
		}()
		b.dispatch(ctx, msg)
	}()
}

// dispatch 解析 Event 信封并按事件类型路由到对应的 handler。
// 无 handler 注册的事件类型静默丢弃。
func (b *Broker) dispatch(ctx context.Context, msg *goredis.Message) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("pubsub broker: handler panic recovered",
				zap.Any("panic", r))
		}
	}()

	var event Event
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		b.logger.Warn("pubsub broker: failed to parse event envelope", zap.Error(err))
		return
	}

	b.mu.RLock()
	handler, ok := b.handlers[event.Type]
	b.mu.RUnlock()

	if !ok {
		return
	}

	if err := handler(ctx, event.Type, []byte(event.Payload)); err != nil {
		b.logger.Warn("pubsub broker: handler error",
			zap.String("eventType", event.Type), zap.Error(err))
	}
}

// Stop initiates graceful shutdown. It is idempotent — safe to call multiple
// times. It signals listen() to exit, waits for it AND for in-flight dispatch
// goroutines:不等 dispatch 时,随后的 redis.Close() 会令它们带错退出。
func (b *Broker) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
	<-b.doneCh
	b.dispatchWG.Wait()
}
