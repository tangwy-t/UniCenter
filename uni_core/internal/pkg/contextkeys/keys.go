package contextkeys

import "context"

type contextKey string

const TraceID contextKey = "traceId"

// ─── 身份上下文键 ─────────────────────────────────────────────────
// 这些键由 HTTP 中间件(middleware.Auth)与 WebSocket 客户端(ws.Client)
// 写入 request/context,供 service、repository(GORM 审计回调)读取。
// 集中定义在此处,避免业务层反向依赖 middleware(传输层)。
// 数据范围(scope)不在此列:唯一口径是 datascope.ScopeContext。
const (
	UserID contextKey = "userID"
)

// TraceIDFromCtx 从 context.Context 中提取 traceId。
// 返回空字符串和 false 如果未找到。
func TraceIDFromCtx(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(TraceID).(string)
	return id, ok
}

// WithTraceID 将 traceId 注入 context.Context。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, TraceID, traceID)
}

// UserIDFromCtx 提取认证用户 ID;未认证返回 0,false。
func UserIDFromCtx(ctx context.Context) (uint64, bool) {
	id, ok := ctx.Value(UserID).(uint64)
	return id, ok
}

// WithUserID 注入认证用户 ID(供中间件、WS 客户端与测试使用)。
func WithUserID(ctx context.Context, userID uint64) context.Context {
	return context.WithValue(ctx, UserID, userID)
}
