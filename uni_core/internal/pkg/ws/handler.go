package ws

import (
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
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
	CheckOrigin:     CheckOrigin,
}

// CheckOrigin 是**全仓库唯一**的 Origin 判定实现，console WS（本包的 upGrader）
// 与 agent WS（internal/handler/agent_ws.go 的 agentUpGrader）共用同一个函数值。
//
// 为什么必须共享同一份（而不是「两边各写一份、注释说策略一致」）：Origin 策略是
// **安全策略**，任何一次收紧/放宽都必须同时落到两个端点上；两份实现的下场是
// Shotgun Surgery —— 改了 console 那边，agent 通道沿用旧策略，而两者肩并肩挂在
// 同一个 api 组上，症状是「为什么 console 能连、agent 不能」这类线上玄学，
// 且没有任何测试会红（各自的单测只测自己那份拷贝）。
// 「两处用的是同一个函数」由 handler 包的同名守卫断言（比行为一致更强：
// 行为一致可能只是两份拷贝当前恰好相同）。
//
// 策略（三条）：
//   - 无 Origin → 放行。浏览器一定会带 Origin；不带的都是非浏览器客户端
//     （curl / server-to-server / **agent 正是这一类**），Origin 头对它们没有意义，
//     把它们当跨站请求拒掉会让整条上报通道直接不可用。
//   - Origin 主机 == 请求 Host（**任意端口**）→ 放行（同主机不同端口的开发部署）。
//   - Origin 主机是 localhost / 127.0.0.1 / ::1 → 放行（本机跨端口的开发环境，
//     如 vite dev server）。
//   - 其余（真跨站）→ 拒绝：浏览器发起的跨站 WebSocket 请求确实存在（CSWSH），
//     外部站点不该能替用户连上我们的任何 WS 端点。
func CheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (curl, server-to-server, agent)
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
	// 开发环境允许本机跨端口连接（如 vite dev server、以及同机的 agent）
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
