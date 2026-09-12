package middleware

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Recovery returns a middleware that recovers from panics, logs the error,
// and responds with a 500 Internal Server Error JSON envelope.
func Recovery(logger logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				stack := string(debug.Stack())
				traceID, _ := contextkeys.TraceIDFromCtx(c.Request.Context())
				logger.Error("panic recovered",
					zap.Any("panic", err),
					zap.String("stack", stack),
					zap.String("traceId", traceID),
				)
				app.Error(c, apperror.Internal("服务器内部错误"))
				c.Abort()
			}
		}()
		c.Next()
	}
}
