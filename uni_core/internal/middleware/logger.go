package middleware

import (
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// RequestLogger returns a middleware that logs each HTTP request's method,
// path, status code, latency, client IP, traceId, and userId (when authenticated)
// using the injected logger.
// traceId is now read from context (set by TraceInit middleware) instead of
// being generated inline.
func RequestLogger(logger logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		traceID, _ := contextkeys.TraceIDFromCtx(c.Request.Context())

		c.Next()

		userID, _ := contextkeys.UserIDFromCtx(c.Request.Context())
		fields := []zap.Field{
			zap.String("traceId", traceID),
			zap.String("method", method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("ip", c.ClientIP()),
		}
		if userID != 0 {
			fields = append(fields, zap.Uint64("userId", userID))
		}
		logger.Info("request completed", fields...)
	}
}
