package dockernotify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// ── 受众解析件（PermAudienceResolver）测试 ────────────────────────────────
//
// 反查的 SQL 形态（多菜单同码、perms 多值、软删菜单）由仓储层的 sqlite 测试
// 钉住；这里钉的是解析件自己的三块面：缓存账目（命中/过期/空集也缓存）、
// 失败口径（不缓存、上抛）、副本纪律（调用方改不动缓存）。

// fakeRoleQuery 是反查替身：返回预置集合，数着调用次数（缓存行为的断言源）。
type fakeRoleQuery struct {
	mu    sync.Mutex
	roles []uint64
	err   error
	calls int
}

func (q *fakeRoleQuery) FindRoleIDsByPerm(_ context.Context, _ string) ([]uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.err != nil {
		return nil, q.err
	}
	return q.roles, nil
}

func (q *fakeRoleQuery) callCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

// newTestResolver 构造夹具：1 分钟 TTL + 可拨的固定时钟（fixedClock 复用
// notifier_test 的替身 —— 同包测试共享夹具是这里的惯例）。
func newTestResolver(t *testing.T) (*PermAudienceResolver, *fakeRoleQuery, *fixedClock) {
	t.Helper()
	q := &fakeRoleQuery{roles: []uint64{1, 2, 3}}
	clk := &fixedClock{now: time.Unix(1789948800, 0)}
	r := NewPermAudienceResolver(PermAudienceOptions{
		TTL: time.Minute,
		Now: clk.Now,
	}, q, logger.NewNop())
	return r, q, clk
}

// 多角色（正常路径）：原样透传仓储给的「去重升序」集合 —— 归一化是仓储的
// 契约（RolePermQuery 的注释），解析件不二次加工。
func TestPermAudienceResolveMultiRole(t *testing.T) {
	r, _, _ := newTestResolver(t)
	roles, err := r.Resolve(context.Background(), "docker:list")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(roles) != 3 || roles[0] != 1 || roles[1] != 2 || roles[2] != 3 {
		t.Fatalf("roles = %v, want [1 2 3]", roles)
	}
}

// TTL 内命中缓存：第二次解析不打反查（故障路径零 SQL 的承诺）。
func TestPermAudienceCacheHitWithinTTL(t *testing.T) {
	r, q, clk := newTestResolver(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "docker:list"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	clk.advance(59 * time.Second)
	if _, err := r.Resolve(ctx, "docker:list"); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if q.callCount() != 1 {
		t.Fatalf("缓存窗口内应只查一次，实际 %d 次", q.callCount())
	}
}

// 跨 TTL 过期：缓存条目自过期，重新反查拿到**新**结果（角色授权变更在短
// TTL 后生效的口径）。
func TestPermAudienceCacheExpiresAfterTTL(t *testing.T) {
	r, q, clk := newTestResolver(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "docker:list"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	// 授权变了：角色 2 被摘走。
	q.mu.Lock()
	q.roles = []uint64{1, 3}
	q.mu.Unlock()
	clk.advance(time.Minute)
	roles, err := r.Resolve(ctx, "docker:list")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if len(roles) != 2 || roles[1] != 3 {
		t.Fatalf("过期后应拿到新集合 [1 3]，实际 %v", roles)
	}
	if q.callCount() != 2 {
		t.Fatalf("过期后应重新反查，实际查询 %d 次", q.callCount())
	}
}

// 无角色（空集）是合法结果且被缓存：空集同样是事实（「权限没人持有」），
// 缓存它让 5 分钟内的后续告警零查询即走「跳过」分支 —— 而不是每条告警
// 都重新问一次库。
func TestPermAudienceEmptyResultCached(t *testing.T) {
	r, q, clk := newTestResolver(t)
	q.mu.Lock()
	q.roles = nil
	q.mu.Unlock()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		roles, err := r.Resolve(ctx, "docker:list")
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i+1, err)
		}
		if len(roles) != 0 {
			t.Fatalf("Resolve #%d: roles = %v, want empty", i+1, roles)
		}
	}
	clk.advance(59 * time.Second) // 仍在 TTL 内
	if _, err := r.Resolve(ctx, "docker:list"); err != nil {
		t.Fatalf("third Resolve: %v", err)
	}
	if q.callCount() != 1 {
		t.Fatalf("空集也应缓存（一次查询），实际 %d 次", q.callCount())
	}
}

// 失败口径：错误上抛（降级决策在 wireup 的 sink）且**不缓存** —— 下一条
// 告警立刻重试，不把一次 DB 抖动固化成 5 分钟的「查不到」。
func TestPermAudienceErrorNotCached(t *testing.T) {
	r, q, _ := newTestResolver(t)
	q.mu.Lock()
	q.err = errors.New("db down")
	q.mu.Unlock()
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "docker:list"); err == nil {
		t.Fatal("反查失败必须上抛错误")
	}
	q.mu.Lock()
	q.err = nil // 库恢复了
	q.mu.Unlock()
	roles, err := r.Resolve(ctx, "docker:list")
	if err != nil {
		t.Fatalf("恢复后的 Resolve: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("roles = %v, want [1 2 3]", roles)
	}
	if q.callCount() != 2 {
		t.Fatalf("失败不缓存：两次解析应两次查询，实际 %d 次", q.callCount())
	}
}

// 副本纪律：返回切片是缓存的副本 —— 调用方排序/改写不至于污染后续解析。
func TestPermAudienceReturnsCopy(t *testing.T) {
	r, q, _ := newTestResolver(t)
	ctx := context.Background()
	roles, err := r.Resolve(ctx, "docker:list")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	roles[0] = 999 // 恶意/无意改写
	again, err := r.Resolve(ctx, "docker:list")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if again[0] != 1 {
		t.Fatalf("缓存被调用方污染: %v", again)
	}
	if q.callCount() != 1 {
		t.Fatalf("两次解析应共享缓存（一次查询），实际 %d 次", q.callCount())
	}
}

// 不同权限码各自记账（互不顶替、互不污染）。
func TestPermAudiencePerKeyCache(t *testing.T) {
	r, q, _ := newTestResolver(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "docker:list"); err != nil {
		t.Fatalf("Resolve docker:list: %v", err)
	}
	if _, err := r.Resolve(ctx, "docker:manage"); err != nil {
		t.Fatalf("Resolve docker:manage: %v", err)
	}
	if q.callCount() != 2 {
		t.Fatalf("不同码各查各的，实际 %d 次", q.callCount())
	}
}
