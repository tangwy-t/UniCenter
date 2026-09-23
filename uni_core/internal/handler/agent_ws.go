package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
)

// AgentConnRegistrar 是 handler 需要的 hub 能力面（消费方窄接口）。
//
// 为什么不用具体 *agenthub.Hub：handler 对 hub 只用两件事 —— 把新建的连接交给
// 注册表（其实由 Conn 在 hello 成功后自己调 Register，见 agenthub.SelfUnregisterer），
// 以及在 Serve 返回后**按连接身份**注销。写成窄接口后 handler 不依赖注册表的完整
// 能力面（DrainAll/DeviceIDs/CloseDevice 属 wireup 与 task），也不会出现
// `h.hub.(*agenthub.Hub)` 这种绕路断言。
//
// 为什么**内嵌** agenthub.SelfUnregisterer 而不是各列一遍方法：handler 拿到的
// 同一个对象要原样交给 `agenthub.NewConn`（连接在 hello 成功后自己 Register），
// 所以这里的能力面必须**包含**连接那侧要的接口。各列一遍方法只会得到签名相同
// 但类型不同的接口 —— 传参仍不兼容，于是只能退回类型断言（正是要避免的绕路）。
// 内嵌是显式声明这条「同一对象满足两侧」的关系，而不是把它藏在断言里。
//
// 注意 SelfUnregisterer 带未导出方法：**只有 agenthub 包内的类型能实现这个接口**，
// 这正是想要的边界 —— 注册表不可能被别处冒充。
type AgentConnRegistrar interface {
	agenthub.SelfUnregisterer

	// Unregister 按连接身份注销：deviceID 与 c 必须同时匹配才移除，
	// 否则旧连接的延迟注销会把刚上线的新连接挤掉。
	Unregister(deviceID uint64, c *agenthub.Conn)
}

// 编译期断言：真实注册表必须满足 handler 的能力面 —— wireup 注入的
// `agenthub.NewHub(...)` 一旦签名漂移，红灯落在这里而不是 wireup 的调用点。
var _ AgentConnRegistrar = (*agenthub.Hub)(nil)

// agentUpGrader 是 agent 通道的升级器。
//
// Origin 策略**与 console WS 共用同一个函数**（internal/pkg/ws.CheckOrigin，
// 由本文件的 TestAgentUpGraderSharesOriginCheckWithConsole 断言「是同一个函数值」，
// 不只是「行为当前恰好一致」）：两个端点肩并肩挂在同一个 api 组上，
// 策略一旦分叉就会变成「为什么 console 能连、agent 不能」的线上玄学 ——
// 而这正是「一份实现 + 一处修改」能避免的 Shotgun Surgery。
//
// 为什么 agent 端点沿用「无 Origin 放行」这条：agent 是**非浏览器客户端**，
// Origin 头对它没有意义、它本来就不带；把它当跨站请求拒掉会让整条上报通道直接不可用。
var agentUpGrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     ws.CheckOrigin,
}

// AgentWSHandler 是 agent 的 WebSocket 入口。
//
// **它是未鉴权端点**（挂在鉴权组之外）：agent 没有 JWT，它的凭据是首帧 hello 里的
// enroll/agent token —— 因此**升级握手阶段不做任何鉴权**，所有校验都在首帧上做。
// 这与既有 console 的 /ws 端点同构（见 internal/pkg/ws/handler.go）。
//
// 正因如此，**必须**把它挂在 api 组（而非 auth 组）下：挂进 auth 组会让所有 agent
// 在握手阶段就收到 401，而 agent 无从拿到 JWT —— 整条通道静默失效。
type AgentWSHandler struct {
	hub  AgentConnRegistrar
	deps agenthub.Deps
	log  logger.LoggerInterface
}

// NewAgentWSHandler 构造 agent WS 入口。
//
// hub 收窄接口而不是 *agenthub.Hub，deps 是**按值**传入的依赖束（Task 8 完成
// 完整装配；本任务只要求这个入口可用）。
func NewAgentWSHandler(hub AgentConnRegistrar, deps agenthub.Deps, log logger.LoggerInterface) *AgentWSHandler {
	return &AgentWSHandler{hub: hub, deps: deps, log: log}
}

// Serve 升级连接并把它交给单连接状态机。
//
// @Summary      agent 上报通道(WebSocket 升级)
// @Description  未鉴权端点：升级握手阶段不做任何校验，凭据是首帧 agent.hello 里的 enroll/agent token。
// @Description  升级后的动作/上报/心跳全部走帧协议(agent.report.metrics、agent.heartbeat、agent.resource.*)，不体现在 HTTP 层。
// @Tags         设备监控
// @Success      101  "握手成功，协议切换到 WebSocket"
// @Failure      400  "非 WebSocket 升级请求(缺少 Upgrade 头)"
// @Failure      403  "Origin 被 CheckOrigin 拒绝(真跨站请求)"
// @Router       /agent/ws [get]
//
// 生命周期（每一步都不可省）：
//  1. Upgrade —— 失败即返回：gorilla 已经把 HTTP 错误响应写进 c.Writer，
//     这里再写一次会把状态码覆盖成 200（且 header 已发出，写不进去）。
//  2. NewConn —— 把真实 socket 交给 agenthub；**此时还没有任何注册**，
//     连接处于 StateAwaitHello，注册发生在 hello 成功之后（由 Conn 自己调
//     Register）。这正是「匿名连接不得占住设备槽位」的落点。
//  3. defer Unregister —— 按**连接身份**注销。Serve 返回即读循环已结束
//     （对端断开/关闭码下发/心跳超时），连接不再可用。
//  4. defer Close —— 兜底关闭底层 socket（Serve 内部也会关，关闭幂等）。
func (h *AgentWSHandler) Serve(c *gin.Context) {
	ws, err := agentUpGrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// gorilla 已写响应（含 4xx 握手失败）。这里只记日志：这是**协议层**
		// 拒绝（Origin 跨站 / 非 WS 请求），不是业务错误，不需要再包一层响应体。
		if h.log != nil {
			h.log.Warn("agent ws upgrade failed", zap.Error(err))
		}
		return
	}

	// 来源 IP 必须用 c.ClientIP() 而**不是** ws.RemoteAddr()：
	//   - ws.RemoteAddr() 是 TCP 对端。生产部署下 agent 前面有 nginx
	//     （见 uni_console/nginx.conf 的 location /api/），对端是**代理**，
	//     落库会变成「所有设备的 IP 都是 nginx 的 IP」——比没有更糟，因为
	//     它看起来像个真实答案，排障时会把人引向错误方向。
	//   - c.ClientIP() 是 Gin 的代理感知解析：trustedProxies 非空时按
	//     X-Forwarded-For/X-Real-IP 取真实客户端；**为空时取对端地址**
	//     （router.go 在 trustedProxies 为空时 SetTrustedProxies(nil)，
	//     正是为了不让 X-Forwarded-For 被伪造成限流/审计身份）。
	// 于是这里的取值口径与全站其余审计入口（操作日志等）保持一致。
	//
	// 取值的**时点**：升级握手之后、交给连接之前。此刻请求头仍然可用
	// （Upgrade 成功后 c.Request 仍可读），且早于任何 hello 处理。
	conn := agenthub.NewConn(h.hub, ws, h.deps, h.log, c.ClientIP())
	defer func() {
		// 先关 socket 再销注册表：Unregister 之后若有并发 DrainAll，
		// 注册表里已经没有这条连接（它已经不收任何通知也没关系）；
		// 反过来先销注册表则会出现「已被移出注册表但仍在读循环里跑」的窗口。
		_ = ws.Close()
		// 按连接身份注销：deviceID 尚未绑定（hello 前断开）时为 0，
		// hub 里本就没有 0 号槽位，Unregister 会安全地什么都不做。
		h.hub.Unregister(conn.DeviceID(), conn)
	}()

	// 用**请求上下文**：HTTP 连接被关闭（含超时/服务端停机）时它会被取消，
	// 入湖与 last_seen 写库因此能随连接一起收手，而不是挂着一个已死的请求继续写。
	conn.Serve(c.Request.Context())
}
