package lock

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func newTestLocker(t *testing.T) (*RedisLocker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewLocker(client), mr
}

// TestTryLockExclusive 同 key 仅一个 owner 能拿到锁。
func TestTryLockExclusive(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLocker(t)

	ok, err := l.TryLock(ctx, "job:1", "node-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("首次 TryLock = %v/%v, want true/nil", ok, err)
	}
	ok, err = l.TryLock(ctx, "job:1", "node-b", time.Minute)
	if err != nil || ok {
		t.Fatalf("二次 TryLock = %v/%v, want false/nil", ok, err)
	}
}

// TestUnlockOwnerOnly 只有持有者能解锁:错误 owner 不动锁,正确 owner 删除。
func TestUnlockOwnerOnly(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLocker(t)

	_, _ = l.TryLock(ctx, "job:1", "node-a", time.Minute)

	if err := l.Unlock(ctx, "job:1", "node-b"); err != nil {
		t.Fatalf("错误 owner Unlock 报错: %v", err)
	}
	ok, _ := l.TryLock(ctx, "job:1", "node-c", time.Minute)
	if ok {
		t.Fatal("错误 owner 的 Unlock 释放了锁")
	}

	if err := l.Unlock(ctx, "job:1", "node-a"); err != nil {
		t.Fatalf("正确 owner Unlock 报错: %v", err)
	}
	ok, _ = l.TryLock(ctx, "job:1", "node-c", time.Minute)
	if !ok {
		t.Fatal("正确 owner Unlock 后应可重新获得锁")
	}
}

// TestTryLockTTL ttl 到期后锁自动释放。
func TestTryLockTTL(t *testing.T) {
	ctx := context.Background()
	l, mr := newTestLocker(t)

	_, _ = l.TryLock(ctx, "job:1", "node-a", time.Minute)
	mr.FastForward(2 * time.Minute)

	ok, _ := l.TryLock(ctx, "job:1", "node-b", time.Minute)
	if !ok {
		t.Fatal("锁过期后应可被其他 owner 获得")
	}
}
