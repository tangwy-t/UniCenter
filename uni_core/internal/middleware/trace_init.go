package middleware

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// TraceInit 返回一个中间件，从请求头 X-Request-Id 提取 traceId，
// 若不存在则生成新的 UUID，注入 context.Context 并设置响应头。
// 必须在中间件链最早期运行（Recovery 之后、RequestLogger 之前）。
func TraceInit() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Request-Id")
		if traceID == "" {
			traceID = uuid.New().String()
		}
		c.Header("X-Request-Id", traceID)
		ctx := contextkeys.WithTraceID(c.Request.Context(), traceID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
