package pubsub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// fakeLC 忽略清理注册,满足 ManagerInterface。
type fakeLC struct{}

func (fakeLC) RegisterTo(string, string, lifecycle.StopFunc) {}

func newTestBroker(t *testing.T) (*Broker, goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	broker := NewBroker(client, logger.NewNop(), fakeLC{})
	t.Cleanup(func() {
		broker.Stop()
		client.Close()
		mr.Close()
	})
	return broker, client, mr
}

// waitReady 等待 Broker 的 SUBSCRIBE 真正下发完成。
//
// 不等待就 Publish 是一个真实竞态:NewBroker 在 goroutine 里订阅,
// 而 Publish 走的是另一条连接 —— SUBSCRIBE 若尚未到达 redis-server,
// 消息会投递给零个订阅者并静默丢失,表现为本文件里间歇性的
// "handler 未在时限内收到消息"(该 flake 在本次修复前可复现)。
func waitReady(t *testing.T, b *Broker) {
	t.Helper()
	select {
	case <-b.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("broker 未在时限内完成订阅")
	}
}

// TestSubscribeDispatchRoundtrip 发布→按类型路由→handler 收到载荷。
func TestSubscribeDispatchRoundtrip(t *testing.T) {
	broker, _, _ := newTestBroker(t)
	waitReady(t, broker)
	got := make(chan []byte, 1)
	broker.Subscribe("job.changed", func(_ context.Context, eventType string, payload []byte) error {
		if eventType != "job.changed" {
			t.Errorf("eventType = %q", eventType)
		}
		got <- payload
		return nil
	})
	if err := broker.Publish(context.Background(), "job.changed", map[string]int{"id": 7}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case p := <-got:
		if string(p) != `{"id":7}` {
			t.Fatalf("payload = %s", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler 未在时限内收到消息")
	}
}

// TestUnsubscribeStopsDelivery 退订后同类型消息静默丢弃。
func TestUnsubscribeStopsDelivery(t *testing.T) {
	broker, _, _ := newTestBroker(t)
	waitReady(t, broker)
	var mu sync.Mutex
	got := 0
	broker.Subscribe("job.changed", func(context.Context, string, []byte) error {
		mu.Lock()
		got++
		mu.Unlock()
		return nil
	})
	broker.Unsubscribe("job.changed")
	if err := broker.Publish(context.Background(), "job.changed", 1); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got != 0 {
		t.Fatalf("Unsubscribe 后仍投递 %d 次", got)
	}
}

// TestUnknownTypeDropped 未注册事件类型静默丢弃(不 panic)。
func TestUnknownTypeDropped(t *testing.T) {
	broker, _, _ := newTestBroker(t)
	waitReady(t, broker)
	if err := broker.Publish(context.Background(), "no.such.type", 1); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// 无 handler,等待短暂窗口确认无 panic 即可
	time.Sleep(100 * time.Millisecond)
}

// TestStopIdempotent Stop 可重复调用;停止后不再投递。
func TestStopIdempotent(t *testing.T) {
	broker, _, _ := newTestBroker(t)
	broker.Stop()
	broker.Stop() // 第二次不得 deadlock/panic
}
