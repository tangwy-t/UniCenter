package service

import (
	"context"
	"errors"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/crypto"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"

	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	gojwt "github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// LoginLogServiceInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type LoginLogServiceInterface interface {
	RecordLogin(ctx context.Context, userID uint64, username, ip, userAgent string, code int, msg string) error
	RecordLogout(ctx context.Context, userID uint64, username, ip string) error
}

// ConfigGetterInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type ConfigGetterInterface interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
	GetBool(ctx context.Context, key string, defaultVal bool) bool
}

// CaptchaInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type CaptchaInterface interface {
	Verify(ctx context.Context, key, code string) error
	IncrementFailCount(ctx context.Context, userID uint64) (int, error)
	IsCaptchaRequired(ctx context.Context, userID uint64) (bool, error)
	IsLocked(ctx context.Context, userID uint64) (bool, int, error)
	SetLock(ctx context.Context, userID uint64) error
	Unlock(ctx context.Context, userID uint64) error
}

// AuthRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type AuthRepositoryInterface interface {
	FindByUsername(ctx context.Context, username string) (*entity.SysUser, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
	GetRoleCodes(ctx context.Context, userID uint64) ([]string, error)
	FindMenuPerms(ctx context.Context) ([]string, error)
	GetUserDataScope(ctx context.Context, userID uint64) (int8, uint64, error)
	GetUserRoleScope(ctx context.Context, userID uint64) int8
	UpdatePassword(ctx context.Context, userID uint64, newPassword string, newSalt *string) error
	UpdateLoginInfo(ctx context.Context, userID uint64, ip string) error
	UpdateProfile(ctx context.Context, userID uint64, user *entity.SysUser) error
	UpdateAvatar(ctx context.Context, userID uint64, avatar string) error
	CountUserLogins(ctx context.Context, userID uint64) (int64, error)
	FindUserLoginLogsSince(ctx context.Context, userID uint64, since time.Time) ([]entity.SysLoginLog, error)
	GetDeptName(ctx context.Context, deptID uint64) (string, error)
}

type AuthService struct {
	cfgProv       ConfigGetterInterface
	repo          AuthRepositoryInterface
	logger        logger.LoggerInterface
	sessionStore  SessionStoreInterface
	loginLogSvc   LoginLogServiceInterface
	captchaSvc    CaptchaInterface
	apiPrefix     string // server.apiPrefix,用于拼接头像访问路径
	scopeResolver *datascope.ScopeResolver
	passwordSvc   *PasswordService
}

// NewAuthService constructs an AuthService with the given dependencies.
func NewAuthService(
	cfgProv ConfigGetterInterface,
	repo AuthRepositoryInterface,
	logger logger.LoggerInterface,
	sessionStore SessionStoreInterface,
	loginLogSvc LoginLogServiceInterface,
	captchaSvc CaptchaInterface,
	apiPrefix string,
	scopeResolver *datascope.ScopeResolver,
	passwordSvc *PasswordService,
) *AuthService {
	return &AuthService{
		cfgProv:       cfgProv,
		repo:          repo,
		logger:        logger,
		sessionStore:  sessionStore,
		loginLogSvc:   loginLogSvc,
		captchaSvc:    captchaSvc,
		apiPrefix:     apiPrefix,
		scopeResolver: scopeResolver,
		passwordSvc:   passwordSvc,
	}
}

// ChangePassword 委托给密码域服务(含强度策略与改密提醒清理)。
func (s *AuthService) ChangePassword(ctx context.Context, req *request.ChangePasswordReq, accessToken string) error {
	return s.passwordSvc.ChangePassword(ctx, req, accessToken)
}

// VerifyPassword 委托给密码域服务(锁屏解锁校验)。
func (s *AuthService) VerifyPassword(ctx context.Context, req *request.VerifyPasswordReq) (*response.VerifyPasswordResp, error) {
	return s.passwordSvc.VerifyPassword(ctx, req)
}

// issueTokens generates a signed access token and refresh token for the given user.
func (s *AuthService) issueTokens(ctx context.Context, userID uint64, perms []string, scopes []jwt.ScopeClaim) (accessToken, refreshToken string, err error) {
	secret := s.cfgProv.GetString(ctx, jwt.SecretConfigKey, jwt.DefaultSecretFallback)
	accessExpire := s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200)
	refreshExpire := s.cfgProv.GetInt(ctx, "sys.jwt.refreshExpire", 604800)
	accessToken, err = jwt.GenerateAccessToken(userID, perms, scopes, secret, accessExpire)
	if err != nil {
		return "", "", err
	}
	refreshToken, err = jwt.GenerateRefreshToken(userID, secret, refreshExpire)
	if err != nil {
		return "", "", err
	}
	return accessToken, refreshToken, nil
}

// storeSession writes the access token, refresh token, and permission cache
// to the session hashStore. A StoreAccess failure is returned to the caller:
// IsAccessValid treats a whitelist miss as invalid, so handing out a token
// whose whitelist write failed means "login succeeds, then immediate 401".
// Refresh/perms writes stay best-effort (degraded, not broken).
// storeAccessAndPerms 写入 access 白名单与权限缓存,不触碰 refresh token。
// 供 RefreshToken 使用:refresh 的写入走 RotateRefresh(CAS),
// 不能像 Login 那样用 storeSession 无条件覆盖(会绕过重放防护)。
func (s *AuthService) storeAccessAndPerms(ctx context.Context, userID uint64, accessToken string, perms []string) error {
	accessTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200)) * time.Second
	if err := s.sessionStore.StoreAccess(ctx, accessToken, userID, accessTTL); err != nil {
		s.logger.Error("failed to store access token whitelist", zap.Error(err))
		return err
	}
	if err := s.sessionStore.StorePerms(ctx, userID, "", perms, accessTTL); err != nil {
		s.logger.Warn("failed to store permissions", zap.Error(err))
	}
	return nil
}

// storeSessionMeta 写入会话元数据(在线用户列表展示)。best-effort:失败仅告警,
// 不阻断登录 —— 元数据不是安全边界,缺了只是列表少一行 IP/设备信息。
func (s *AuthService) storeSessionMeta(ctx context.Context, userID uint64, accessToken, ip, userAgent string) {
	ua := util.ParseUA(userAgent)
	accessTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200)) * time.Second
	now := time.Now()
	meta := &session.SessionMeta{
		UserID:    userID,
		IP:        ip,
		UserAgent: userAgent,
		Browser:   ua.Browser,
		OS:        ua.OS,
		LoginAt:   now.Unix(),
		ExpireAt:  now.Add(accessTTL).Unix(),
	}
	if err := s.sessionStore.StoreSessionMeta(ctx, accessToken, meta, accessTTL); err != nil {
		s.logger.Warn("failed to store session meta", zap.Uint64("userId", userID), zap.Error(err))
	}
}

func (s *AuthService) storeSession(ctx context.Context, userID uint64, accessToken, refreshToken string, perms []string) error {
	accessTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200)) * time.Second
	refreshTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.jwt.refreshExpire", 604800)) * time.Second
	if err := s.sessionStore.StoreAccess(ctx, accessToken, userID, accessTTL); err != nil {
		s.logger.Error("failed to store access token whitelist", zap.Error(err))
		return err
	}
	if err := s.sessionStore.StoreRefresh(ctx, userID, refreshToken, refreshTTL); err != nil {
		s.logger.Warn("failed to store refresh token", zap.Error(err))
	}
	if err := s.sessionStore.StorePerms(ctx, userID, "", perms, accessTTL); err != nil {
		s.logger.Warn("failed to store permissions", zap.Error(err))
	}
	return nil
}

// buildUserScopes 构造 JWT 数据范围声明。Login 与 RefreshToken 共用:
// 此前两处独立构造且已发散(refresh 无条件附 dept+self 双维度,login 按
// 数据范围条件构造),同一用户经两条路径签发的 token 可见数据集不同。
// 语义以 Login 为准:仅包含与用户实际数据范围匹配的维度。
func buildUserScopes(userID uint64, dataScope int8, deptID uint64, roleScope int8) []jwt.ScopeClaim {
	var scopes []jwt.ScopeClaim
	if dataScope == datascope.ScopeSelf {
		scopes = []jwt.ScopeClaim{
			{Dimension: "self", Level: datascope.ScopeSelf, SelfID: userID},
		}
	} else {
		scopes = []jwt.ScopeClaim{
			{Dimension: "dept", Level: dataScope, SelfID: deptID},
		}
	}
	scopes = append(scopes, jwt.ScopeClaim{Dimension: "role", Level: roleScope, SelfID: 0})
	return scopes
}

// roleScopeAllFromCtx 判断 ctx 中 role 维度是否为 ScopeAll(admin)。
// 无 ScopeContext / 无 role 维度时返回 false(防御性不加 admin 标记)。
func roleScopeAllFromCtx(ctx context.Context) bool {
	sc, ok := datascope.ScopeContextFromCtx(ctx)
	if !ok || sc == nil || sc.Dimensions == nil {
		return false
	}
	dim, ok := sc.Dimensions[datascope.DimRole]
	return ok && dim != nil && dim.Level == datascope.ScopeAll
}

// scopeCtxFor 按 scopes 声明构造 ScopeContext 注入 ctx,复用与
// middleware.ScopeResolverHandler 完全相同的 resolver,权限口径与运行时一致。
// resolver 解析失败降级为该维度空集(fail-closed,不放大为全量),
// scopeResolver 未注入(测试)时原样返回。
func (s *AuthService) scopeCtxFor(ctx context.Context, userID uint64, scopes []jwt.ScopeClaim) context.Context {
	if s.scopeResolver == nil {
		return ctx
	}
	sc := &datascope.ScopeContext{UserID: userID, Dimensions: make(map[string]*datascope.ResolvedDimension, len(scopes))}
	for _, claim := range scopes {
		resolver, ok := s.scopeResolver.Resolvers[claim.Dimension]
		if !ok {
			continue
		}
		dim, err := resolver.Resolve(ctx, claim.Level, claim.SelfID, userID)
		if err != nil {
			s.logger.Warn("scope resolve failed during access resolution",
				zap.String("dimension", claim.Dimension),
				zap.Int8("level", claim.Level),
				zap.Error(err))
			// 降级与 middleware.ScopeResolverHandler 完全一致:写入该维度
			// 并置 **空集**。此前两处的降级都只填 Level/SelfID,AllowedIDs
			// 保持 nil —— 而 plugin.scopeCallback 把 nil 读作"授予全部",
			// 于是"解析失败"被放大为"可见全部",与注释声称的防护相反。
			// 空集才是真正的 fail-closed:该维度无可见项,但仍允许其他
			// 维度授权(插件按维度 OR 拼接)。
			sc.Dimensions[claim.Dimension] = &datascope.ResolvedDimension{
				Level:      claim.Level,
				SelfID:     claim.SelfID,
				AllowedIDs: []uint64{},
			}
			continue
		}
		sc.Dimensions[claim.Dimension] = dim
	}
	return datascope.WithScopeContext(ctx, sc)
}

// resolveUserAccess 一次解析登录/刷新所需的全部权限数据:权限点集合与
// 数据范围声明。权限点由 scope 插件自动注入过滤(scopeCtxFor 构造的
// ScopeContext),与运行时菜单查询共用同一过滤来源;admin(role scope=ScopeAll)
// 附加 "admin" 通配标记。加载失败降级为空权限,不阻断发 token(与旧行为一致)。
func (s *AuthService) resolveUserAccess(ctx context.Context, userID uint64) ([]string, []jwt.ScopeClaim) {
	dataScope, deptID, err := s.repo.GetUserDataScope(ctx, userID)
	if err != nil {
		s.logger.Warn("failed to load user data scope", zap.Uint64("userId", userID), zap.Error(err))
		dataScope = datascope.ScopeSelf
		deptID = 0
	}

	roleScope := s.repo.GetUserRoleScope(ctx, userID)
	scopes := buildUserScopes(userID, dataScope, deptID, roleScope)

	perms, err := s.repo.FindMenuPerms(s.scopeCtxFor(ctx, userID, scopes))
	if err != nil {
		s.logger.Warn("failed to load user permissions", zap.Uint64("userId", userID), zap.Error(err))
		perms = []string{}
	}
	if roleScope == datascope.ScopeAll {
		perms = append(perms, "admin")
	}
	return perms, scopes
}

func (s *AuthService) Login(ctx context.Context, req *request.LoginReq, ip, userAgent string) (*response.LoginResp, error) {
	user, err := s.repo.FindByUsername(ctx, req.Username)
	if err != nil {
		go s.recordLoginAsync(0, req.Username, ip, userAgent, apperror.CodeUnauthorized, "用户名或密码错误")
		return nil, apperror.Unauthorized("用户名或密码错误")
	}
	// Captcha check
	if s.captchaSvc != nil {
		required, err := s.captchaSvc.IsCaptchaRequired(ctx, user.ID)
		if err != nil {
			s.logger.Warn("failed to check captcha required", zap.Uint64("userId", user.ID), zap.Error(err))
			return nil, apperror.Internal("验证码服务异常")
		}
		if required {
			if req.CaptchaKey == "" || req.CaptchaCode == "" {
				return nil, apperror.CaptchaRequired("验证码已启用，请先获取验证码")
			}
			if err := s.captchaSvc.Verify(ctx, req.CaptchaKey, req.CaptchaCode); err != nil {
				// 验证码错误与密码错误共用同一失败计数与锁定评估:否则攻击者可
				// 无限次枚举验证码而不触发账号锁定。
				s.recordAuthFailure(ctx, user.ID)
				code := apperror.CodeCaptchaIncorrect
				var appErr *apperror.AppError
				if errors.As(err, &appErr) {
					code = appErr.Code
				}
				go s.recordLoginAsync(user.ID, user.Username, ip, userAgent, code, "验证码错误")
				return nil, err
			}
		}
	}

	if user.Status != nil && *user.Status == entity.UserStatusDisabled {
		go s.recordLoginAsync(user.ID, user.Username, ip, userAgent, apperror.CodeUnauthorized, "账号已被禁用")
		return nil, apperror.Unauthorized("账号已被禁用，请联系管理员")
	}

	// Lock check:与 IsCaptchaRequired 一致,fail-closed。锁定是账号爆破
	// 防护的关键一环,Redis 不可用时若放行登录(fail-open),攻击者可在
	// 故障窗口内无限爆破。宁可短暂拒绝登录(可用性受损),也不放行爆破。
	if s.captchaSvc != nil {
		locked, remaining, err := s.captchaSvc.IsLocked(ctx, user.ID)
		if err != nil {
			s.logger.Error("failed to check lock status, failing closed", zap.Uint64("userId", user.ID), zap.Error(err))
			return nil, apperror.Internal("账号锁定状态查询失败，请稍后重试")
		}
		if locked {
			go s.recordLoginAsync(user.ID, user.Username, ip, userAgent, apperror.CodeAccountLocked, "账号已锁定")
			return nil, apperror.AccountLocked(remaining)
		}
	}

	salt := ""
	if user.PasswordSalt != nil {
		salt = *user.PasswordSalt
	}
	if !crypto.VerifyPassword(req.Password, user.Password, salt) {
		// Increment fail count on wrong password
		if s.captchaSvc != nil {
			s.recordAuthFailure(ctx, user.ID)
		}
		go s.recordLoginAsync(user.ID, user.Username, ip, userAgent, apperror.CodeUnauthorized, "用户名或密码错误")
		return nil, apperror.Unauthorized("用户名或密码错误")
	}

	// Unlock on successful login
	if s.captchaSvc != nil {
		if unlockErr := s.captchaSvc.Unlock(ctx, user.ID); unlockErr != nil {
			s.logger.Warn("failed to unlock", zap.Uint64("userId", user.ID), zap.Error(unlockErr))
		}
	}

	// Load permissions + data scope claims in one consolidated pass
	// (role codes 死查询一并移除:此前加载后仅 `_ = roleCodes` 丢弃)。
	perms, scopes := s.resolveUserAccess(ctx, user.ID)
	accessToken, refreshToken, err := s.issueTokens(ctx, user.ID, perms, scopes)
	if err != nil {
		return nil, apperror.Internal("生成 token 失败")
	}

	// 硬顶号:同一账号在新设备登录时,吊销既有全部会话(access+refresh+perms),
	// 使旧设备立即失效,而非仅靠刷新槽覆盖成"软顶号"(旧 access 仍可用到过期)。
	// best-effort:清理失败不阻断登录,仅降级为旧的软顶号行为。
	if err := s.sessionStore.RevokeAll(ctx, user.ID, ""); err != nil {
		s.logger.Warn("login: 清理旧会话失败", zap.Uint64("userId", user.ID), zap.Error(err))
	}

	// Store session data in Redis
	if err := s.storeSession(ctx, user.ID, accessToken, refreshToken, perms); err != nil {
		return nil, apperror.Internal("会话存储失败", err)
	}
	s.storeSessionMeta(ctx, user.ID, accessToken, ip, userAgent)

	// Update last-login metadata (best-effort)
	if err := s.repo.UpdateLoginInfo(ctx, user.ID, ip); err != nil {
		s.logger.Warn("failed to update login info", zap.Uint64("userId", user.ID), zap.Error(err))
	}

	// Record successful login asynchronously
	go s.recordLoginAsync(user.ID, user.Username, ip, userAgent, apperror.CodeOK, "")

	s.logger.Info("user login",
		zap.Uint64("userId", user.ID),
		zap.String("username", user.Username),
		zap.String("ip", ip))

	return &response.LoginResp{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200),
	}, nil
}

// recordAuthFailure increments the shared fail counter and locks the account
// once the configured threshold is reached. Wrong-password and wrong-captcha
// paths both go through it so pure captcha enumeration locks the account too.
// Caller must guarantee s.captchaSvc != nil.
func (s *AuthService) recordAuthFailure(ctx context.Context, userID uint64) {
	count, err := s.captchaSvc.IncrementFailCount(ctx, userID)
	if err != nil {
		s.logger.Warn("failed to increment fail count", zap.Uint64("userId", userID), zap.Error(err))
		return
	}
	lockThreshold := s.cfgProv.GetInt(ctx, "sys.auth.lockThreshold", 10)
	if count >= lockThreshold {
		if lockErr := s.captchaSvc.SetLock(ctx, userID); lockErr != nil {
			s.logger.Warn("failed to set lock", zap.Uint64("userId", userID), zap.Error(lockErr))
		}
	}
}

// withAsyncLogContext 统一异步日志记录的公共样板:panic 兜底、loginLogSvc
// nil 检查、带超时的独立 ctx,然后用该 ctx 调用 fn(name 仅用于 panic 日志归属)。
// recover 必须作为跑此方法的 goroutine 的栈顶 defer —— 调用方以 go 关键字
// 起新 goroutine 时,即由这里的 defer 完成兜底,避免后台日志 panic 拖垮进程。
func (s *AuthService) withAsyncLogContext(name string, fn func(ctx context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("async log panic recovered", zap.String("task", name), zap.Any("panic", r))
		}
	}()
	if s.loginLogSvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfgProv.GetInt(context.Background(), "sys.auth.asyncLogTimeout", 5))*time.Second)
	defer cancel()
	fn(ctx)
}

func (s *AuthService) recordLoginAsync(userID uint64, username, ip, userAgent string, code int, msg string) {
	s.withAsyncLogContext("recordLoginAsync", func(ctx context.Context) {
		if err := s.loginLogSvc.RecordLogin(ctx, userID, username, ip, userAgent, code, msg); err != nil {
			s.logger.Warn("failed to record login log", zap.Error(err))
		}
	})
}

func (s *AuthService) Logout(ctx context.Context, token, ip string) error {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return apperror.Unauthorized("未登录")
	}
	if err := s.sessionStore.RevokeAll(ctx, userID, token); err != nil {
		return err
	}
	// Record logout asynchronously
	go s.withAsyncLogContext("recordLogoutAsync", func(ctx context.Context) {
		user, err := s.repo.FindByID(ctx, userID)
		if err != nil {
			s.logger.Warn("failed to resolve user for logout log", zap.Uint64("userId", userID), zap.Error(err))
			return
		}
		if err := s.loginLogSvc.RecordLogout(ctx, userID, user.Username, ip); err != nil {
			s.logger.Warn("failed to record logout log", zap.Error(err))
		}
	})
	return nil
}

// GetUserPermissions 从请求 ctx 携带的 ScopeContext 派生权限点:scope 插件
// 自动注入 sys_menu.id 过滤,role 维度为 ScopeAll(admin)时附加 "admin" 标记。
// 消费方是 middleware.PermissionGuard 缓存回源 —— ctx 必须是经过
// ScopeResolverHandler 的请求 ctx(而非裸 Background),否则 scope 不会注入。
func (s *AuthService) GetUserPermissions(ctx context.Context, userID uint64) ([]string, error) {
	perms, err := s.repo.FindMenuPerms(ctx)
	if err != nil {
		return nil, err
	}
	if roleScopeAllFromCtx(ctx) {
		perms = append(perms, "admin")
	}
	return perms, nil
}

// RefreshToken validates a refresh token, issues new tokens, and rotates the old refresh token.
func (s *AuthService) RefreshToken(ctx context.Context, req *request.RefreshTokenReq, ip, userAgent string) (*response.RefreshTokenResp, error) {
	secret := s.cfgProv.GetString(ctx, jwt.SecretConfigKey, jwt.DefaultSecretFallback)

	// 1. Parse the JWT refresh token
	claims, err := jwt.ParseRefreshToken(req.RefreshToken, secret)
	if err != nil {
		if errors.Is(err, gojwt.ErrTokenExpired) {
			return nil, apperror.TokenExpired("refresh token 已过期")
		}
		return nil, apperror.Unauthorized("无效的 refresh token")
	}

	// 2. Validate Subject == "refresh"
	sub, err := claims.GetSubject()
	if err != nil || sub != "refresh" {
		return nil, apperror.Unauthorized("无效的 token 类型")
	}

	// 3. Redis double-verify: stored token must match request token
	storedToken, err := s.sessionStore.GetRefresh(ctx, claims.UserID)
	if err != nil {
		s.logger.Warn("failed to get refresh token from redis", zap.Uint64("userId", claims.UserID), zap.Error(err))
	}
	if err != nil || storedToken != req.RefreshToken {
		return nil, apperror.Unauthorized("refresh token 已失效")
	}

	// 5. Verify user status
	user, err := s.repo.FindByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.Unauthorized("用户不存在")
		}
		return nil, err
	}
	if user.Status != nil && *user.Status == entity.UserStatusDisabled {
		return nil, apperror.Unauthorized("账号已被禁用，请联系管理员")
	}

	// 6. Load latest permissions + scopes(与 Login 共用 resolveUserAccess 收敛入口)
	perms, scopes := s.resolveUserAccess(ctx, claims.UserID)
	// 7. Generate new tokens
	accessToken, refreshToken, err := s.issueTokens(ctx, claims.UserID, perms, scopes)
	if err != nil {
		return nil, apperror.Internal("生成 token 失败")
	}

	// 9. 轮换会话:先写入 access 白名单与权限,再以 CAS 原子地把
	//    refresh token 从旧值换成新值。
	//
	//    顺序是有意的:新令牌必须先全部就绪,才提交"作废旧令牌"这一步。
	//    此前的实现顺序相反(校验通过后立刻删旧令牌,再签发/存储新令牌),
	//    一旦签发或存储失败(DB 抖动、账号被禁用等),用户既没有旧令牌也
	//    没有新令牌 —— 被强制登出且无法重试。现在失败时旧令牌仍然有效,
	//    用户重试即可。
	if err := s.storeAccessAndPerms(ctx, claims.UserID, accessToken, perms); err != nil {
		return nil, apperror.Internal("会话存储失败", err)
	}
	s.storeSessionMeta(ctx, claims.UserID, accessToken, ip, userAgent)
	refreshTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.jwt.refreshExpire", 604800)) * time.Second
	consumed, err := s.sessionStore.RotateRefresh(ctx, claims.UserID, req.RefreshToken, refreshToken, refreshTTL)
	if err != nil {
		return nil, apperror.Internal("会话存储失败", err)
	}
	if !consumed {
		// 旧令牌在此刻已不是当前值:并发刷新中另一个请求先完成了兑换。
		// 必须拒绝,否则一个 refresh token 能换出多组有效令牌(重放)。
		return nil, apperror.Unauthorized("refresh token 已失效")
	}

	return &response.RefreshTokenResp{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.cfgProv.GetInt(ctx, "sys.jwt.accessExpire", 7200),
	}, nil
}
