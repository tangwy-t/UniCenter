package middleware

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// stubAuthSvc 记录回源 ctx 是否携带 ScopeContext。
type stubAuthSvc struct {
	gotScopeCtx bool
	perms       []string
}

func (s *stubAuthSvc) GetUserPermissions(ctx context.Context, userID uint64) ([]string, error) {
	_, s.gotScopeCtx = datascope.ScopeContextFromCtx(ctx)
	return s.perms, nil
}

type stubPermStore struct{}

func (stubPermStore) LoadPerms(context.Context, uint64, string) ([]string, error) { return nil, nil }
func (stubPermStore) StorePerms(context.Context, uint64, string, []string, time.Duration) error {
	return nil
}

type stubCfgGateway struct{}

func (stubCfgGateway) GetString(context.Context, string, string) string { return "" }
func (stubCfgGateway) GetInt(context.Context, string, int) int          { return 7200 }
func (stubCfgGateway) GetBool(context.Context, string, bool) bool       { return false }

// TestPermissionGuardFallbackCarriesScopeContext 缓存 miss 回源时,必须把
// 请求 ctx(含 ScopeContext)传给 GetUserPermissions —— 此前传 Background
// 导致 scope 插件不注入,回源权限点与运行时语义脱节。
func TestPermissionGuardFallbackCarriesScopeContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authSvc := &stubAuthSvc{perms: []string{"system:user:list"}}
	guard := NewPermissionGuard(authSvc, stubPermStore{}, stubCfgGateway{}, logger.NewNop())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(CtxClaims, &jwt.Claims{UserID: 1})
	sc := &datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeCustom, AllowedIDs: []uint64{1, 2}},
	}}
	ctx := datascope.WithScopeContext(context.Background(), sc)
	c.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	guard.Permission("system:user:list")(c)

	if !authSvc.gotScopeCtx {
		t.Fatal("fallback 回源 ctx 未携带 ScopeContext:PermissionGuard 必须传请求 ctx 而非 Background")
	}
	if c.IsAborted() {
		t.Fatal("权限应通过,实际被 Abort")
	}
}

// TestScopeFingerprint_DistinguishesContexts 锁定 singleflight key 的
// scope 维度:不同 scope 上下文必须产生不同指纹,否则并发回源会互相复用
// 对方的结果(权限点按 scope 过滤,结果本就不该相同)。
func TestScopeFingerprint_DistinguishesContexts(t *testing.T) {
	mk := func(dims map[string]*datascope.ResolvedDimension) context.Context {
		return datascope.WithScopeContext(context.Background(),
			&datascope.ScopeContext{UserID: 1, Dimensions: dims})
	}

	// 无 ScopeContext 必须与"有 scope"区分开。
	if scopeFingerprint(context.Background()) ==
		scopeFingerprint(mk(map[string]*datascope.ResolvedDimension{})) {
		t.Error("noscope 与空 scope 指纹相同,无 scope 调用会复用带 scope 的缓存")
	}

	all := mk(map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeAll},
	})
	custom12 := mk(map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeCustom, AllowedIDs: []uint64{1, 2}},
	})
	custom13 := mk(map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeCustom, AllowedIDs: []uint64{1, 3}},
	})

	if scopeFingerprint(all) == scopeFingerprint(custom12) {
		t.Error("ScopeAll(nil=全量)与 ScopeCustom 指纹相同 —— 权限范围不同却共用回源结果")
	}
	if scopeFingerprint(custom12) == scopeFingerprint(custom13) {
		t.Error("不同 AllowedIDs 指纹相同 —— 可见菜单集合不同却共用回源结果")
	}

	// nil(无限制)与空切片(无权限)语义不同,指纹必须区分。
	nilIDs := mk(map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeAll, AllowedIDs: nil},
	})
	emptyIDs := mk(map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeAll, AllowedIDs: []uint64{}},
	})
	if scopeFingerprint(nilIDs) == scopeFingerprint(emptyIDs) {
		t.Error("nil(全量)与空集(无权限)指纹相同 —— 两者通配语义相反")
	}
}

// TestScopeFingerprint_StableAcrossIDOrder AllowedIDs 取值顺序不影响语义
// (插件拼成 IN 列表),指纹必须稳定,否则同一语义会重复回源。
func TestScopeFingerprint_StableAcrossIDOrder(t *testing.T) {
	mk := func(ids []uint64) string {
		ctx := datascope.WithScopeContext(context.Background(),
			&datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
				"dept": {Level: datascope.ScopeDeptAndBelow, AllowedIDs: ids},
			}})
		return scopeFingerprint(ctx)
	}
	if mk([]uint64{3, 1, 2}) != mk([]uint64{1, 2, 3}) {
		t.Error("AllowedIDs 顺序不同导致指纹不同 —— 同语义会重复回源")
	}
}

// TestScopeFingerprint_StableAcrossDimensionOrder 维度 map 遍历顺序随机,
// 指纹必须与顺序无关。
func TestScopeFingerprint_StableAcrossDimensionOrder(t *testing.T) {
	mk := func(rev bool) string {
		dims := map[string]*datascope.ResolvedDimension{
			"dept": {Level: datascope.ScopeDept, AllowedIDs: []uint64{7}},
			"self": {Level: datascope.ScopeSelf, AllowedIDs: []uint64{42}},
		}
		_ = rev
		ctx := datascope.WithScopeContext(context.Background(),
			&datascope.ScopeContext{UserID: 1, Dimensions: dims})
		return scopeFingerprint(ctx)
	}
	if mk(false) != mk(true) {
		t.Error("维度遍历顺序影响指纹")
	}
}

// TestPermissionGuard_SingleflightNotSharedAcrossScopes 端到端锁定 P1:
// 同一用户在两个不同 scope 上下文下的**并发**缓存 miss 不得互相复用结果。
//
// 必须真正并发才能触发 singleflight 的共享语义:顺序调用时第一次结果
// 已写入 store(此处 stub 不缓存),不会走合并路径。
func TestPermissionGuard_SingleflightNotSharedAcrossScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authSvc := &blockingScopeAuthSvc{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	guard := NewPermissionGuard(authSvc, stubPermStore{}, stubCfgGateway{}, logger.NewNop())

	run := func(level int8, ids []uint64, perm string) <-chan bool {
		done := make(chan bool, 1)
		go func() {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(CtxClaims, &jwt.Claims{UserID: 1})
			ctx := datascope.WithScopeContext(context.Background(),
				&datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
					datascope.DimRole: {Level: level, AllowedIDs: ids},
				}})
			c.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)
			guard.Permission(perm)(c)
			done <- !c.IsAborted()
		}()
		return done
	}

	// 两个不同 scope 上下文的并发请求,都触发缓存 miss。
	adminDone := run(datascope.ScopeAll, nil, "system:user:list")
	customDone := run(datascope.ScopeCustom, []uint64{1}, "system:user:list")

	// 等两个回源都进入(若被合并,只会有一个进入)。
	for i := 0; i < 2; i++ {
		select {
		case <-authSvc.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("只有 %d 个回源进入 —— singleflight key 未隔离 scope,两个上下文被合并", i)
		}
	}
	close(authSvc.release)

	if !<-adminDone {
		t.Error("ScopeAll 应通过")
	}
	if <-customDone {
		t.Error("ScopeCustom 不应通过 —— 复用了 ScopeAll 的回源结果(singleflight key 缺 scope 维度)")
	}
}

// blockingScopeAuthSvc 回源结果由 ctx 的 role 维度决定,并在返回前阻塞,
// 以便测试确定地观察到"两个回源是否被 singleflight 合并成一个"。
type blockingScopeAuthSvc struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingScopeAuthSvc) GetUserPermissions(ctx context.Context, _ uint64) ([]string, error) {
	s.entered <- struct{}{}
	<-s.release
	sc, ok := datascope.ScopeContextFromCtx(ctx)
	if !ok || sc == nil {
		return []string{}, nil
	}
	if dim := sc.Dimensions[datascope.DimRole]; dim != nil && dim.Level == datascope.ScopeAll {
		return []string{"system:user:list"}, nil
	}
	return []string{}, nil
}

// scopeAwareAuthSvc 回源结果由 ctx 的 role 维度决定,用于验证 key 隔离。
type scopeAwareAuthSvc struct{}

func (scopeAwareAuthSvc) GetUserPermissions(ctx context.Context, _ uint64) ([]string, error) {
	sc, ok := datascope.ScopeContextFromCtx(ctx)
	if !ok || sc == nil {
		return []string{}, nil
	}
	if dim := sc.Dimensions[datascope.DimRole]; dim != nil && dim.Level == datascope.ScopeAll {
		return []string{"system:user:list"}, nil
	}
	return []string{}, nil
}
