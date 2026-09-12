package metricshistory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	goredis "github.com/redis/go-redis/v9"
)

// 本文件是「metricshistory.Window.Query 持锁做 Redis 网络 IO」（评审报告 #20）
// 修复后的回归验证。
//
// 修复内容：Query 的互斥锁只保护内存 cache 快读写，Redis 往返移到锁外。
// 本测试在首个 Query 阻塞于 Redis 往返期间，直接尝试获取 w.mu：若可立即
// 获取，则证明锁未跨 Redis IO 持有（队头阻塞消除）。

// TestPoc_WindowQuery_DoesNotHoldLockAcrossRedisIO 证明 Redis IO 期间锁是空闲的。
func TestPoc_WindowQuery_DoesNotHoldLockAcrossRedisIO(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	defer mr.Close()

	win := NewWindow(rdb, Options{
		Key: "test:metrics", Step: 2 * time.Second, MaxPoints: 4096, QueryTTL: time.Millisecond,
	}, countAgg)

	if err := win.Append(context.Background(), testPoint{T: time.Now().UnixMilli(), V: 1}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// 阻塞首个 LRANGE，制造一个正在进行的慢 Redis 往返。
	release := make(chan struct{})
	firstEntered := make(chan struct{})
	var hookOnce sync.Once
	mr.Server().SetPreHook(func(_ *server.Peer, cmd string, _ ...string) bool {
		if cmd == "LRANGE" {
			hookOnce.Do(func() {
				close(firstEntered)
				<-release
			})
		}
		return false
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = win.Query(context.Background(), 2*time.Minute, 2*time.Second)
	}()

	<-firstEntered // 首个 Query 已阻塞在 Redis 命令上

	// 修复后，Redis IO 在锁外完成 → 此时 w.mu 应为空闲，可立即 TryLock。
	if !win.mu.TryLock() {
		t.Fatal("Redis IO 期间 w.mu 仍被持有 —— 锁跨网络 IO 持有未修复")
	}
	win.mu.Unlock()

	close(release)
	wg.Wait()

	t.Log("确认：Redis IO 期间 w.mu 空闲，队头阻塞已消除")
}
