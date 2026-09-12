package middleware

import (
	"net/http"
	"slices"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// CORS returns a middleware that sets Cross-Origin Resource Sharing headers
// and short-circuits preflight OPTIONS requests with 204 No Content.
// Only origins in allowedOrigins are permitted; if empty, CORS is disabled.
// Rejected origins are logged at Warn level.
func CORS(allowedOrigins []string, logger logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if len(allowedOrigins) == 0 || !slices.Contains(allowedOrigins, origin) {
			if origin != "" {
				logger.Warn("CORS origin rejected",
					zap.String("origin", origin),
					zap.Strings("allowed", allowedOrigins))
			}
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization")
		c.Header("Access-Control-Max-Age", "3600")
		c.Header("Access-Control-Expose-Headers", "Content-Length,Content-Type,X-Request-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
