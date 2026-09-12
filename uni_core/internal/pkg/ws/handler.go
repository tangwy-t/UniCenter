package ws

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"net"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var upGrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkOrigin,
}

// checkOrigin rejects cross-site WebSocket hijacking attempts. Browsers always
// send an Origin header; non-browser clients may omit it and are allowed.
// Same-host origins (any port, for dev setups) and loopback origins are
// accepted; everything else is denied.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (curl, server-to-server)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := u.Hostname()
	reqHost, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		reqHost = r.Host // r.Host carries no port
	}
	if originHost == reqHost {
		return true
	}
	// 开发环境允许本机跨端口连接（如 vite dev server）
	return originHost == "localhost" || originHost == "127.0.0.1" || originHost == "::1"
}

func HandleUpgrade(hub HubInterface, logger logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		conn, err := upGrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			logger.Warn("ws upgrade failed", zap.Error(err))
			return
		}
		client := NewClient(hub, conn, logger)
		hub.PumpStarted()
		go func() {
			defer hub.PumpStopped()
			client.WritePump()
		}()
		hub.PumpStarted()
		go func() {
			defer hub.PumpStopped()
			client.ReadPump()
		}()
	}
}
