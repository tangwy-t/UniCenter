package limiter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ── 默认值常量 ─────────────────────────────────────────────────────

const (
	defaultGlobalEnabled     = true
	defaultGlobalLimit       = 100
	defaultGlobalWindowSecs  = 60
	defaultLoginEnabled      = true
	defaultLoginLimit        = 10
	defaultLoginWindowSecs   = 60
	defaultRefreshEnabled    = true
	defaultRefreshLimit      = 30
	defaultRefreshWindowSecs = 60
)

// ── Redis key 前缀 ─────────────────────────────────────────────────

const rateLimitKeyPrefix = "ratelimit"

// ── 跳过的路由路径 ─────────────────────────────────────────────────

// skippedRouteSuffixes 是豁免全局限流的路由(它们各自有更专门的限流:
// /login 挂 LoginRateLimit,/captcha/generate 有自身的频控)。
//
// 只存**相对 API 前缀**的路径,不存绝对路径:c.FullPath() 返回的是
// 注册时的完整模式(含 server.apiPrefix),硬编码 "/api/v1/login" 会在
// apiPrefix 改成 /api/v2 时静默失配 —— 登录接口将掉进全局限流(默认
// 100/60s)而失去专门额度,且没有任何报错可查。apiPrefix 在
// router.Setup 与 swagger BasePath 都已跟随配置,这里同样跟随。
var skippedRouteSuffixes = []string{
	"/login",
	"/captcha/generate",
}

// isSkippedRoute 判断请求路径是否为豁免全局限流的路由。
//
// 每个豁免项自带前导 "/",因此 HasSuffix 本身就是段边界匹配:
//   - "/api/v1/login" 以 "/login" 结尾 → 匹配
//   - "/api/v2/login" 同理 → 匹配(与前缀无关)
//   - "/xlogin"       不以 "/login" 结尾(缺分隔符)→ 不匹配
//   - "/users/login-history" 不以 "/login" 结尾 → 不匹配
//
// 不需要额外做分隔符校验,前导斜杠已经承担了这个作用。
func isSkippedRoute(fullPath string) bool {
	for _, suffix := range skippedRouteSuffixes {
		if strings.HasSuffix(fullPath, suffix) {
			return true
		}
	}
	return false
}

// ── RateLimiter ────────────────────────────────────────────────────

// RateLimiter 实现基于 Redis 滑动窗口计数器的 API 请求限流。
type RateLimiter struct {
	cacheStore CacheStoreInterface
	cfgProv    ConfigGetterInterface
	logger     logger.LoggerInterface
}

// NewRateLimiter 创建 RateLimiter 实例。
func NewRateLimiter(cacheStore CacheStoreInterface, cfgProv ConfigGetterInterface, logger logger.LoggerInterface) *RateLimiter {
	return &RateLimiter{cacheStore: cacheStore, cfgProv: cfgProv, logger: logger}
}

// ── 滑动窗口计数器算法 ──────────────────────────────────────────────

// allowResult 包含限流决策结果。
type allowResult struct {
	allowed   bool
	remaining int
	resetSecs int
}

// allow 执行滑动窗口计数器算法。
// key: 限流标识符（如 "user:123" 或 "ip:10.0.0.1"）
// limit: 窗口内最大请求数
// windowSecs: 窗口大小（秒）
// 返回 nil error 表示 Redis 正常；非 nil 表示 Redis 错误，调用方应 fail-open。
func (rl *RateLimiter) allow(ctx context.Context, key string, limit, windowSecs int) (*allowResult, error) {
	now := time.Now().Unix()

	// windowSecs 来自 DB 配置,validateConfigValue 只验证可解析,0 合法:
	// 不 clamp 会整除零 panic,每个请求都 500。clamp 到非零默认窗口。
	if windowSecs <= 0 {
		windowSecs = defaultGlobalWindowSecs
	}
	w := int64(windowSecs)

	currWindow := now / w * w
	prevWindow := currWindow - w

	prevKey := fmt.Sprintf("%s:%d:%s", rateLimitKeyPrefix, prevWindow, key)
	currKey := fmt.Sprintf("%s:%d:%s", rateLimitKeyPrefix, currWindow, key)

	ttl := int(2 * w)
	res, err := rl.cacheStore.SlidingWindowIncr(ctx, []string{prevKey, currKey}, ttl)
	if err != nil {
		return nil, err
	}
	arr, ok := res.([]any)
	if !ok || len(arr) < 2 {
		return nil, fmt.Errorf("slidingWindowIncr: unexpected result shape %T", res)
	}
	prevCount := toInt(arr[0])
	currCount := toInt(arr[1])

	// 加权计算：使用整数运算避免浮点精度误差。
	// (prevCount * (w - elapsed) + w/2) / w 等价于 round(prevCount * weight)
	elapsed := now - currWindow
	halfW := int64(w / 2)
	estimated := int((prevCount*int64(w-elapsed)+halfW)/int64(w)) + int(currCount)

	remaining := limit - estimated
	if remaining < 0 {
		remaining = 0
	}
	resetSecs := int(w - elapsed)

	return &allowResult{
		allowed:   estimated <= limit,
		remaining: remaining,
		resetSecs: resetSecs,
	}, nil
}

// ── 标识符提取 ─────────────────────────────────────────────────────

// extractIdentity 从请求中提取限流标识符。
// 优先从 JWT 解析 userID；失败则 fallback 到 clientIP。
func (rl *RateLimiter) extractIdentity(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		secret := rl.cfgProv.GetString(c.Request.Context(), jwt.SecretConfigKey, jwt.DefaultSecretFallback)
		claims, err := jwt.ParseAccessToken(tokenStr, secret)
		if err == nil {
			return fmt.Sprintf("user:%d", claims.UserID)
		}
	}
	return fmt.Sprintf("ip:%s", c.ClientIP())
}

// ── 配置读取 ───────────────────────────────────────────────────────

// readGlobalConfig 读取全局限流配置。
func (rl *RateLimiter) readGlobalConfig(ctx context.Context) (enabled bool, limit, windowSecs int) {
	enabled = rl.cfgProv.GetBool(ctx, "sys.rateLimit.global.enabled", defaultGlobalEnabled)
	limit = rl.cfgProv.GetInt(ctx, "sys.rateLimit.global.limit", defaultGlobalLimit)
	windowSecs = rl.cfgProv.GetInt(ctx, "sys.rateLimit.global.windowSecs", defaultGlobalWindowSecs)
	return
}

// readLoginConfig 读取登录限流配置。
func (rl *RateLimiter) readLoginConfig(ctx context.Context) (enabled bool, limit, windowSecs int) {
	enabled = rl.cfgProv.GetBool(ctx, "sys.rateLimit.login.enabled", defaultLoginEnabled)
	limit = rl.cfgProv.GetInt(ctx, "sys.rateLimit.login.limit", defaultLoginLimit)
	windowSecs = rl.cfgProv.GetInt(ctx, "sys.rateLimit.login.windowSecs", defaultLoginWindowSecs)
	return
}

// readRefreshConfig 读取 refresh token 兑换限流配置。
// refresh 是重放/枚举的核心目标,独立于登录与全局限流,额度更严格。
func (rl *RateLimiter) readRefreshConfig(ctx context.Context) (enabled bool, limit, windowSecs int) {
	enabled = rl.cfgProv.GetBool(ctx, "sys.rateLimit.refresh.enabled", defaultRefreshEnabled)
	limit = rl.cfgProv.GetInt(ctx, "sys.rateLimit.refresh.limit", defaultRefreshLimit)
	windowSecs = rl.cfgProv.GetInt(ctx, "sys.rateLimit.refresh.windowSecs", defaultRefreshWindowSecs)
	return
}

// ── 中间件工厂方法 ──────────────────────────────────────────────────

// GlobalRateLimit 返回全局限流中间件。
// 跳过 /login 与 /captcha/generate(见 isSkippedRoute:与前缀无关)。
func (rl *RateLimiter) GlobalRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过特定路由
		if isSkippedRoute(c.FullPath()) {
			c.Next()
			return
		}

		enabled, limit, windowSecs := rl.readGlobalConfig(c.Request.Context())
		if !enabled {
			c.Next()
			return
		}

		key := rl.extractIdentity(c)
		if !rl.enforce(c, key, limit, windowSecs, "rate limiter: request rejected") {
			return
		}
		c.Next()
	}
}

// enforce 执行限流判定并写响应头/拒绝响应。Global 与 Login 两个中间件
// 的同形分支(响应头三连 + 拒绝日志 + 429)收敛于此。
// 返回 true 表示放行(已调 c.Next 由调用方处理或此处直接放行)。
func (rl *RateLimiter) enforce(c *gin.Context, key string, limit, windowSecs int, rejectMsg string) bool {
	result, err := rl.allow(c.Request.Context(), key, limit, windowSecs)
	if err != nil {
		// Redis 不可用 → fail-open
		rl.logger.Error("rate limiter: Redis unavailable, failing open",
			zap.String("key", key), zap.Error(err))
		return true
	}

	resetTime := time.Now().Add(time.Duration(result.resetSecs) * time.Second).Unix()
	c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Header("X-RateLimit-Remaining", strconv.Itoa(result.remaining))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))

	if !result.allowed {
		rl.logger.Warn(rejectMsg,
			zap.String("key", key),
			zap.Int("limit", limit),
			zap.Int("windowSecs", windowSecs))
		app.Error(c, apperror.RateLimited("请求过于频繁，请稍后再试"))
		c.Abort()
		return false
	}
	return true
}

// LoginRateLimit 返回登录接口独立限流中间件。
// 始终按 clientIP 限流。
func (rl *RateLimiter) LoginRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, limit, windowSecs := rl.readLoginConfig(c.Request.Context())
		if !enabled {
			c.Next()
			return
		}

		key := fmt.Sprintf("ip:%s", c.ClientIP())
		if !rl.enforce(c, key, limit, windowSecs, "rate limiter: login request rejected") {
			return
		}
		c.Next()
	}
}

// RefreshRateLimit 返回 refresh token 兑换接口独立限流中间件。
// refresh token 是重放与暴力枚举的核心目标,挂独立且更严格的额度。
func (rl *RateLimiter) RefreshRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, limit, windowSecs := rl.readRefreshConfig(c.Request.Context())
		if !enabled {
			c.Next()
			return
		}

		key := fmt.Sprintf("ip:%s", c.ClientIP())
		if !rl.enforce(c, key, limit, windowSecs, "rate limiter: refresh request rejected") {
			return
		}
		c.Next()
	}
}

func toInt(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case string:
		n, _ := strconv.ParseInt(val, 10, 64)
		return n
	}
	return 0
}
