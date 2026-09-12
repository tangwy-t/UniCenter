package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/crypto"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"gorm.io/gorm"
)

// ── 测试替身 ────────────────────────────────────────────
// stubAuthRepo 满足 AuthRepositoryInterface;仅 FindByID 被测,其余一律零值返回。
type stubAuthRepo struct {
	findByIDUser    *entity.SysUser
	findByIDErr     error
	findMenuPermsFn func(context.Context) ([]string, error)
	dataScope       int8
	deptID          uint64
	roleScope       int8
}

func (m *stubAuthRepo) FindByUsername(context.Context, string) (*entity.SysUser, error) {
	return nil, nil
}
func (m *stubAuthRepo) FindByID(_ context.Context, _ uint64) (*entity.SysUser, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	return m.findByIDUser, nil
}
func (m *stubAuthRepo) GetRoleCodes(context.Context, uint64) ([]string, error) {
	return nil, nil
}
func (m *stubAuthRepo) FindMenuPerms(ctx context.Context) ([]string, error) {
	if m.findMenuPermsFn == nil {
		return nil, nil
	}
	return m.findMenuPermsFn(ctx)
}
func (m *stubAuthRepo) GetUserDataScope(context.Context, uint64) (int8, uint64, error) {
	return m.dataScope, m.deptID, nil
}
func (m *stubAuthRepo) GetUserRoleScope(context.Context, uint64) int8 { return m.roleScope }
func (m *stubAuthRepo) UpdatePassword(context.Context, uint64, string, *string) error {
	return nil
}
func (m *stubAuthRepo) SetMustChangePassword(context.Context, uint64, bool) error {
	return nil
}
func (m *stubAuthRepo) UpdateLoginInfo(context.Context, uint64, string) error { return nil }
func (m *stubAuthRepo) UpdateProfile(context.Context, uint64, *entity.SysUser) error {
	return nil
}
func (m *stubAuthRepo) UpdateAvatar(context.Context, uint64, string) error { return nil }
func (m *stubAuthRepo) CountUserLogins(context.Context, uint64) (int64, error) {
	return 0, nil
}
func (m *stubAuthRepo) FindUserLoginLogsSince(context.Context, uint64, time.Time) ([]entity.SysLoginLog, error) {
	return nil, nil
}
func (m *stubAuthRepo) GetDeptName(context.Context, uint64) (string, error) { return "", nil }

// newVerifyService 构造仅 VerifyPassword 依赖的极简 PasswordService(其余依赖零值)。
// VerifyPassword 现已下沉至密码域服务(PasswordService),AuthService 仅委托。
func newVerifyService(repo PasswordRepositoryInterface) *PasswordService {
	return NewPasswordService(nil, repo, nil, logger.NewNop())
}

func newStubUser(t *testing.T, password string) *entity.SysUser {
	t.Helper()
	hash, salt, err := crypto.HashPassword(password, 10)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return &entity.SysUser{Password: hash, PasswordSalt: &salt}
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, 不是 AppError", err)
	}
	if ae.Code != code {
		t.Fatalf("err.Code = %d, want %d", ae.Code, code)
	}
}

func TestAuthServiceVerifyPasswordOK(t *testing.T) {
	svc := newVerifyService(&stubAuthRepo{findByIDUser: newStubUser(t, "admin123")})
	ctx := contextkeys.WithUserID(context.Background(), 1)

	got, err := svc.VerifyPassword(ctx, &request.VerifyPasswordReq{Password: "admin123"})
	if err != nil {
		t.Fatalf("VerifyPassword err: %v", err)
	}
	if got == nil || !got.Valid {
		t.Fatalf("resp = %+v, want Valid=true", got)
	}
}

func TestAuthServiceVerifyPasswordWrong(t *testing.T) {
	svc := newVerifyService(&stubAuthRepo{findByIDUser: newStubUser(t, "admin123")})
	ctx := contextkeys.WithUserID(context.Background(), 1)

	_, err := svc.VerifyPassword(ctx, &request.VerifyPasswordReq{Password: "wrong-pass"})
	wantCode(t, err, apperror.CodeBadRequest)
}

func TestAuthServiceVerifyPasswordUserNotFound(t *testing.T) {
	svc := newVerifyService(&stubAuthRepo{findByIDErr: gorm.ErrRecordNotFound})
	ctx := contextkeys.WithUserID(context.Background(), 1)

	_, err := svc.VerifyPassword(ctx, &request.VerifyPasswordReq{Password: "x"})
	wantCode(t, err, apperror.CodeNotFound)
}

func TestAuthServiceVerifyPasswordUnauthorized(t *testing.T) {
	svc := newVerifyService(&stubAuthRepo{})
	// 不注入 userID
	_, err := svc.VerifyPassword(context.Background(), &request.VerifyPasswordReq{Password: "x"})
	wantCode(t, err, apperror.CodeUnauthorized)
}

// ── scope 驱动的访问解析测试 ────────────────────────────────────

// stubRoleMenuRepo 满足 datascope.RoleMenuRepoInterface。
type stubRoleMenuRepo struct {
	ids   []uint64
	err   error
	calls int
}

func (m *stubRoleMenuRepo) FindRoleMenuIDs(context.Context, uint64) ([]uint64, error) {
	m.calls++
	return m.ids, m.err
}

// newAccessService 构造仅访问解析相关的 AuthService:注入 role/self 两个
// 维度的 resolver(dept 维度缺省跳过,scopeCtxFor 对缺失 resolver 静默跳过)。
func newAccessService(repo AuthRepositoryInterface, roleMenus *stubRoleMenuRepo) *AuthService {
	scopeResolver := datascope.NewScopeResolver([]datascope.DimensionResolver{
		datascope.NewRoleDimensionResolver(roleMenus),
		datascope.NewSelfDimensionResolver(),
	}, logger.NewNop())
	return NewAuthService(nil, repo, logger.NewNop(), nil, nil, nil, "", scopeResolver, nil)
}

func TestResolveUserAccessCustomRoleScope(t *testing.T) {
	roleMenus := &stubRoleMenuRepo{ids: []uint64{11, 12, 15}}
	repo := &stubAuthRepo{
		dataScope: datascope.ScopeCustom,
		deptID:    9,
		roleScope: datascope.ScopeCustom,
		findMenuPermsFn: func(ctx context.Context) ([]string, error) {
			sc, ok := datascope.ScopeContextFromCtx(ctx)
			if !ok || sc == nil {
				t.Fatalf("FindMenuPerms 未收到携带 ScopeContext 的 ctx")
			}
			dim, ok := sc.Dimensions[datascope.DimRole]
			if !ok || dim == nil {
				t.Fatalf("role 维度缺失")
			}
			if len(dim.AllowedIDs) != 3 || dim.AllowedIDs[0] != 11 {
				t.Fatalf("AllowedIDs = %v, want role-menu 集合 [11 12 15]", dim.AllowedIDs)
			}
			return []string{"system:user:list"}, nil
		},
	}
	svc := newAccessService(repo, roleMenus)

	perms, scopes := svc.resolveUserAccess(context.Background(), 7)

	if len(perms) != 1 || perms[0] != "system:user:list" {
		t.Fatalf("perms = %v, want [system:user:list]", perms)
	}
	if slices.Contains(perms, "admin") {
		t.Fatal("ScopeCustom 用户不应获得 admin 标记")
	}
	if len(scopes) != 2 || scopes[1].Dimension != datascope.DimRole || scopes[1].Level != datascope.ScopeCustom {
		t.Fatalf("scopes = %+v, want [dept custom, role custom]", scopes)
	}
	if roleMenus.calls != 1 {
		t.Fatalf("role resolver 调用次数 = %d, want 1", roleMenus.calls)
	}
}

func TestResolveUserAccessAdminAppendsMarker(t *testing.T) {
	repo := &stubAuthRepo{
		dataScope: datascope.ScopeAll,
		deptID:    9,
		roleScope: datascope.ScopeAll,
		findMenuPermsFn: func(context.Context) ([]string, error) {
			return []string{"system:user:list"}, nil
		},
	}
	svc := newAccessService(repo, &stubRoleMenuRepo{})

	perms, _ := svc.resolveUserAccess(context.Background(), 1)

	if len(perms) != 2 || perms[1] != "admin" {
		t.Fatalf("perms = %v, want [system:user:list admin](ScopeAll → plugin 不加过滤 → 全量 + admin 标记)", perms)
	}
}

func TestGetUserPermissionsUsesScopeFromCtx(t *testing.T) {
	repo := &stubAuthRepo{findMenuPermsFn: func(ctx context.Context) ([]string, error) {
		if _, ok := datascope.ScopeContextFromCtx(ctx); !ok {
			t.Fatal("ctx 缺少 ScopeContext:GetUserPermissions 必须由调用方提供已注入 scope 的 ctx")
		}
		return []string{"system:user:list"}, nil
	}}
	svc := newAccessService(repo, &stubRoleMenuRepo{})

	sc := &datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeAll},
	}}
	ctx := datascope.WithScopeContext(context.Background(), sc)

	perms, err := svc.GetUserPermissions(ctx, 1)
	if err != nil {
		t.Fatalf("GetUserPermissions: %v", err)
	}
	if len(perms) != 2 || perms[1] != "admin" {
		t.Fatalf("perms = %v, want 含 admin 标记", perms)
	}
}

func TestGetUserPermissionsNoScopeCtxNoMarker(t *testing.T) {
	repo := &stubAuthRepo{findMenuPermsFn: func(context.Context) ([]string, error) {
		return []string{"system:user:list"}, nil
	}}
	svc := newAccessService(repo, &stubRoleMenuRepo{})

	perms, err := svc.GetUserPermissions(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUserPermissions: %v", err)
	}
	if len(perms) != 1 || slices.Contains(perms, "admin") {
		t.Fatalf("perms = %v, want 无 admin 标记(无 scope ctx 时防御性不加)", perms)
	}
}

// failingDimResolver 在 Resolve 时始终报错,用于验证 scopeCtxFor 降级路径。
type failingDimResolver struct{}

func (failingDimResolver) DimensionType() string { return "boom" }
func (failingDimResolver) Resolve(context.Context, int8, uint64, uint64) (*datascope.ResolvedDimension, error) {
	return nil, errors.New("boom resolve failed")
}

// TestScopeCtxForFailingDimensionFallsBack 维度解析失败时,scopeCtxFor 必须
// 把降级维度写入 ScopeContext —— 与 middleware.ScopeResolverHandler 同口径。
// 此前此处 continue 跳过写入,导致登录/刷新路径的 ScopeContext 缺失维度,
// scope 插件在该维度上不做任何过滤(解析失败静默放大为全量可见)。
func TestScopeCtxForFailingDimensionFallsBack(t *testing.T) {
	resolver := datascope.NewScopeResolver([]datascope.DimensionResolver{failingDimResolver{}}, logger.NewNop())
	repo := &stubAuthRepo{findByIDUser: &entity.SysUser{}}
	svc := NewAuthService(nil, repo, logger.NewNop(), nil, nil, nil, "", resolver, nil)

	ctx := svc.scopeCtxFor(context.Background(), 1, []jwt.ScopeClaim{{Dimension: "boom", Level: 4, SelfID: 9}})
	sc, ok := datascope.ScopeContextFromCtx(ctx)
	if !ok {
		t.Fatal("scopeCtxFor 应注入 ScopeContext")
	}
	dim, ok := sc.Dimensions["boom"]
	if !ok || dim == nil {
		t.Fatal("解析失败时降级维度必须写入 Dimensions(与 middleware.ScopeResolverHandler 对齐)")
	}
	if dim.Level != 4 || dim.SelfID != 9 {
		t.Fatalf("降级维度 = %+v, 应保留 claim 原始 Level/SelfID", dim)
	}
	// 关键断言:降级必须 fail-closed。plugin.scopeCallback 把 AllowedIDs==nil
	// 读作"授予全部",只断言 Level/SelfID 会漏掉这一点 —— 旧实现正是
	// 只填 Level/SelfID 而让 AllowedIDs 保持 nil,把"解析失败"放大成
	// "可见全部数据"。必须为空集(非 nil)。
	if dim.AllowedIDs == nil {
		t.Fatal("降级维度 AllowedIDs = nil —— 插件会读作『授予全部』,解析失败被放大为全量可见")
	}
	if len(dim.AllowedIDs) != 0 {
		t.Fatalf("降级维度 AllowedIDs = %v, want 空集", dim.AllowedIDs)
	}
}

// newGetUserInfoService 构造 GetUserInfo 所需依赖的最小 AuthService。
func newGetUserInfoService(repo AuthRepositoryInterface) *AuthService {
	return NewAuthService(nil, repo, logger.NewNop(), nil, nil, nil, "", nil, nil)
}

func userInfoCtx(deptID *uint64) *entity.SysUser {
	return &entity.SysUser{Username: "u", DeptID: deptID}
}

func wantDeptID(t *testing.T, got, want uint64) {
	t.Helper()
	if got != want {
		t.Fatalf("DeptID = %d, want %d", got, want)
	}
}

// TestGetUserInfoScopeFromScopeContext 数据范围口径以 ScopeContext 为准:
// dept 维度取解析 Level/SelfID;self 维度取 DataScope=5 且保留实体 DeptID;
// 无 ScopeContext 时全部保留实体值(不再有 0 覆写)。
func TestGetUserInfoScopeFromScopeContext(t *testing.T) {
	dept7 := uint64(7)

	t.Run("dept dimension overrides both", func(t *testing.T) {
		svc := newGetUserInfoService(&stubAuthRepo{findByIDUser: userInfoCtx(&dept7)})
		ctx := contextkeys.WithUserID(context.Background(), 1)
		sc := &datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
			"dept": {Level: 3, SelfID: 42},
		}}
		resp, err := svc.GetUserInfo(datascope.WithScopeContext(ctx, sc))
		if err != nil {
			t.Fatalf("GetUserInfo: %v", err)
		}
		if resp.DataScope != 3 {
			t.Fatalf("DataScope = %d, want 3", resp.DataScope)
		}
		wantDeptID(t, resp.DeptID, 42)
	})

	t.Run("self dimension keeps entity dept", func(t *testing.T) {
		svc := newGetUserInfoService(&stubAuthRepo{findByIDUser: userInfoCtx(&dept7)})
		ctx := contextkeys.WithUserID(context.Background(), 1)
		sc := &datascope.ScopeContext{UserID: 1, Dimensions: map[string]*datascope.ResolvedDimension{
			"self": {Level: datascope.ScopeSelf, SelfID: 1},
		}}
		resp, err := svc.GetUserInfo(datascope.WithScopeContext(ctx, sc))
		if err != nil {
			t.Fatalf("GetUserInfo: %v", err)
		}
		if resp.DataScope != datascope.ScopeSelf {
			t.Fatalf("DataScope = %d, want %d(self)", resp.DataScope, datascope.ScopeSelf)
		}
		wantDeptID(t, resp.DeptID, 7)
	})

	t.Run("no scope context keeps entity values", func(t *testing.T) {
		svc := newGetUserInfoService(&stubAuthRepo{findByIDUser: userInfoCtx(&dept7)})
		ctx := contextkeys.WithUserID(context.Background(), 1)
		resp, err := svc.GetUserInfo(ctx)
		if err != nil {
			t.Fatalf("GetUserInfo: %v", err)
		}
		if resp.DataScope != 0 {
			t.Fatalf("DataScope = %d, want 0(实体口径)", resp.DataScope)
		}
		wantDeptID(t, resp.DeptID, 7)
	})
}
