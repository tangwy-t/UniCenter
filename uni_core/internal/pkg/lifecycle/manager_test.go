package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

func testLogger() logger.LoggerInterface {
	return logger.NewNop()
}

// 慢于阶段超时的 hook 必须被强制中断:关闭流程不被卡死,后续 hook 照常执行。
func TestShutdownStagedHookTimeoutEnforced(t *testing.T) {
	m := New(testLogger())
	m.DefinePhases(Phase{Name: "cleanup", Timeout: 80 * time.Millisecond})

	var fastHookRan bool
	m.RegisterTo("cleanup", "fast", func(ctx context.Context) error {
		fastHookRan = true
		return nil
	})
	// LIFO: hang 先执行(fast 后执行),hang 不应吞掉 fast 的机会。
	m.RegisterTo("cleanup", "hang", func(ctx context.Context) error {
		<-ctx.Done() // 忽略 ctx 的钩子 —— 关闭流程最坏情况
		<-ctx.Done() // 故意连 ctx.Done 都不直接返回,模拟最劣实现
		time.Sleep(5 * time.Second)
		return nil
	})

	start := time.Now()
	err := m.ShutdownStaged()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from hung hook")
	}
	if !errors.Is(err, errHookTimeout) {
		t.Fatalf("expected errHookTimeout in chain, got: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("shutdown blocked %v; hook timeout not enforced", elapsed)
	}
	if !fastHookRan {
		t.Fatal("fast hook after hung hook must still run")
	}
}

// hook panic 不应中断关闭流程,且被记为错误。
func TestShutdownStagedHookPanicRecovered(t *testing.T) {
	m := New(testLogger())
	m.DefinePhases(Phase{Name: "cleanup", Timeout: time.Second})

	m.RegisterTo("cleanup", "boom", func(ctx context.Context) error {
		panic("boom")
	})

	err := m.ShutdownStaged()
	if err == nil {
		t.Fatal("expected error from panicking hook")
	}
}

// 正常路径:全部 hook 成功,LIFO 逆序执行。
func TestShutdownStagedLIFOOrder(t *testing.T) {
	m := New(testLogger())
	m.DefinePhases(Phase{Name: "cleanup", Timeout: time.Second})

	var order []string
	m.RegisterTo("cleanup", "first", func(ctx context.Context) error {
		order = append(order, "first")
		return nil
	})
	m.RegisterTo("cleanup", "second", func(ctx context.Context) error {
		order = append(order, "second")
		return nil
	})

	if err := m.ShutdownStaged(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("expected LIFO order [second first], got %v", order)
	}
}
