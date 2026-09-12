package middleware

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// PermissionGuard coalesces concurrent cache-miss loads for the same user into a
// single backend call, preventing thundering-herd DB queries.
// Create one instance at startup with its dependencies injected; routes then
// only pass the required permission code (no per-route dependency plumbing).
type PermissionGuard struct {
	sfGroup   singleflight.Group
	authSvc   AuthServiceInterface
	permStore SessionStoreInterface
	cfgProv   ConfigGetterInterface
	logger    logger.LoggerInterface
}

// NewPermissionGuard creates a PermissionGuard with its collaborators.
func NewPermissionGuard(authSvc AuthServiceInterface, permStore SessionStoreInterface, cfgProv ConfigGetterInterface, logger logger.LoggerInterface) *PermissionGuard {
	return &PermissionGuard{
		authSvc:   authSvc,
		permStore: permStore,
		cfgProv:   cfgProv,
		logger:    logger,
	}
}

// Permission returns a middleware that enforces RBAC permission checks.
// It loads the user's permission set from the session store (falling back to
// AuthService) and verifies that either the "admin" super-permission or the
// required permission string is present.
func (g *PermissionGuard) Permission(requiredPerm string) gin.HandlerFunc {
	authSvc, permStore, cfgProv, logger := g.authSvc, g.permStore, g.cfgProv, g.logger
	return func(c *gin.Context) {
		claimsRaw, exists := c.Get(CtxClaims)
		if !exists {
			logger.Error("permission check: claims not found in context")
			app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
			c.Abort()
			return
		}
		claims, ok := claimsRaw.(*jwt.Claims)
		if !ok {
			logger.Error("permission check: claims has unexpected type", zap.Any("claims", claimsRaw))
			app.Error(c, apperror.Internal("服务器内部错误"))
			c.Abort()
			return
		}
		uid := claims.UserID
		logger.Debug("permission check", zap.Uint64("userId", uid), zap.String("requiredPerm", requiredPerm))

		// 安全修复（评审 #6）：权限缓存的持久键现在携带 scope 指纹，与
		// singleflight 去重键一致。此前 LoadPerms 只按 uid 读 "perms:<uid>"，
		// 而 singleflight 已按 scope 分流 —— 结果写进同一个无 scope 的键，
		// 后续不同 scope 的请求会读到越界缓存。
		sfKey := scopeFingerprint(c.Request.Context())

		// Check session store cache
		perms, err := permStore.LoadPerms(c.Request.Context(), uid, sfKey)
		if err != nil {
			logger.Warn("permission cache read failed, falling back to service", zap.Error(err))
		}
		if perms == nil {
			// singleflight:合并同一用户的并发缓存 miss 为一次 DB 查询。
			//
			// key 必须包含 scope 指纹,不能只用 uid:回源经
			// AuthService.GetUserPermissions → FindMenuPerms,该查询依赖
			// ctx 中的 ScopeContext 过滤 sys_menu(scope 插件注入),
			// 且 roleScopeAllFromCtx 也据此决定是否追加 "admin" 通配标记。
			// 同一用户在**不同 scope 上下文**下(例如登录路径构造的 ctx
			// 与请求路径经 ScopeResolverHandler 构造的 ctx)回源结果可能
			// 不同,仅按 uid 合并会把其中一个的结果写进缓存供另一个复用,
			// 最长固化 accessExpire(默认 2h)。带上指纹后不同上下文各飞各的,
			// 相同上下文(绝大多数并发场景)仍然合并。
			sfKeyFull := strconv.FormatUint(uid, 10) + ":" + sfKey
			v, err, _ := g.sfGroup.Do(sfKeyFull, func() (any, error) {
				// 回源必须携带请求 ctx:经 ScopeResolverHandler(router 组级中间件)
				// 注入 ScopeContext,scope 插件据此过滤 sys_menu,权限点与运行时同源。
				// 传 Background 会让 scope 静默失效(旧行为)。
				p, loadErr := authSvc.GetUserPermissions(c.Request.Context(), uid)
				if loadErr != nil {
					return nil, loadErr
				}
				// 写入缓存
				ttl := time.Duration(cfgProv.GetInt(context.Background(), "sys.jwt.accessExpire", 7200)) * time.Second
				if storeErr := permStore.StorePerms(context.Background(), uid, sfKey, p, ttl); storeErr != nil {
					logger.Warn("failed to cache permissions", zap.Error(storeErr))
				}
				return p, nil
			})
			if err != nil {
				logger.Error("failed to load permissions", zap.Error(err))
				app.Error(c, apperror.Internal("服务器内部错误"))
				c.Abort()
				return
			}
			perms = v.([]string)
		}

		// admin role bypass
		if slices.Contains(perms, "admin") {
			c.Next()
			return
		}

		if !slices.Contains(perms, requiredPerm) {
			app.Error(c, apperror.Forbidden("无操作权限"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// scopeFingerprint 返回 ctx 中 ScopeContext 的稳定指纹,用于给 singleflight
// 的 key 加上"回源所依赖的上下文"这一维度。详见 Permission() 内的说明。
//
// 指纹覆盖 UserID、各维度名与其 Level/SelfID/AllowedIDs —— 这些正是影响
// sys_menu 过滤结果与 roleScopeAllFromCtx 判定的全部输入。
//
// AllowedIDs 排序后再参与:维度解析的取值顺序不影响语义(插件把它拼成
// IN 列表),不排序会让同一语义产生不同指纹、白白多打一次 DB。
//
// 无 ScopeContext 时返回 "noscope":这与"有 scope"必须区分开,
// 否则未注入 scope 的调用(如测试直连)会复用带 scope 的缓存结果。
func scopeFingerprint(ctx context.Context) string {
	sc, ok := datascope.ScopeContextFromCtx(ctx)
	if !ok || sc == nil {
		return "noscope"
	}
	var b strings.Builder
	b.WriteString(strconv.FormatUint(sc.UserID, 10))
	dims := make([]string, 0, len(sc.Dimensions))
	for name := range sc.Dimensions {
		dims = append(dims, name)
	}
	sort.Strings(dims)
	for _, name := range dims {
		dim := sc.Dimensions[name]
		b.WriteByte('|')
		b.WriteString(name)
		b.WriteByte(':')
		if dim == nil {
			b.WriteString("nil")
			continue
		}
		b.WriteString(strconv.FormatInt(int64(dim.Level), 10))
		b.WriteByte('/')
		b.WriteString(strconv.FormatUint(dim.SelfID, 10))
		if dim.AllowedIDs == nil {
			b.WriteString("/all") // nil = 无限制,与"空集=无权限"语义不同,必须区分
			continue
		}
		ids := make([]uint64, len(dim.AllowedIDs))
		copy(ids, dim.AllowedIDs)
		slices.Sort(ids)
		b.WriteByte('/')
		for i, id := range ids {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.FormatUint(id, 10))
		}
	}
	return b.String()
}
