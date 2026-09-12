package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PprofGuard 返回一个中间件，检查运行时可观测性配置中的 pprof.enabled 开关。
// 当 pprof 未启用时返回 404，防止未授权的 pprof 访问。
func PprofGuard(configProv ConfigGetterInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled := configProv.GetBool(context.Background(), "sys.pprof.enabled", false)
		if !enabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}
