package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
)

// 事件流聚合端点（六期·监控面）：
//
//	GET /docker/events —— docker events 的跨主机聚合实时流（NDJSON，fetch 形态）
//
// 与日志/stats 端点同一条认证判断 —— 走 auth 组、浏览器能带 Authorization ——
// 但它是**聚合流**（路由静态 perm docker:list，与总览同档），处理器里没有「按指令
// 记录校验归属」这一步：它不接任何单主机会话，接入的是 core 自己的常驻订阅的
// **扇出副本**，凭据纪律与读面其余端点一致。
//
// eventNDJSONLine 是事件流的一行（Content-Type: application/x-ndjson）。
//
// 字段**直接打平**（与 stats 样本行同纪律）：前端要能用一行字段渲染活动流条目，
// 而不是先解 base64 再解一层 JSON；host_id/hostname 是 core **注入的归属**
// （协议载荷里没有 —— agent 只上报自己主机的记录，不知道「自己在清单里叫什么」）。
type eventNDJSONLine struct {
	HostID    uint64 `json:"host_id"`
	Hostname  string `json:"hostname"`
	T         int64  `json:"t"`
	Type      string `json:"type"`
	Action    string `json:"action"`
	ActorName string `json:"actor_name,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
}

// EventsStream 事件聚合流：先回放各主机的环形缓冲（打开即有内容），再进实时。
//
// 引用计数纪律全部收敛在管理器里：首个连接建立常驻订阅、最后一个断开全量退订，
// 这里只做「订阅 → 转发 → 收尾必 Close」三件事 —— Close 经 defer 保证即使写失败/
// 客户端断开也把引用减回去（否则会留下跑在 agent 身上的孤儿订阅）。
//
// @Summary      Docker 事件聚合流(NDJSON)
// @Description  跨主机聚合的容器/镜像/卷/网络事件流；连接后先回放各主机最近 50 条，再实时推送；每行 {"host_id","hostname","t","type","action","actor_name","actor_id"}
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      401  "未登录"
// @Failure      403  "无权限(docker:list)"
// @Router       /docker/events [get]
func (h *DockerHandler) EventsStream(c *gin.Context) {
	if h.events == nil {
		app.Error(c, apperror.Internal("事件流未装配"))
		return
	}
	cl := h.events.Subscribe()
	defer cl.Close()

	ctx := c.Request.Context()
	// 流式响应的三个头与日志/stats 流同一来源：Content-Type 是契约；no-store 防缓存；
	// X-Accel-Buffering 让 nginx 不缓冲（否则事件堵在代理缓冲里，「实时流」变「分钟
	// 后一次性出现」）。
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	for {
		e, err := cl.Next(ctx)
		if err != nil {
			// ctx 取消（客户端断开）/写失败/已关闭：defer 的 Close 把引用减回去 ——
			// 常驻订阅的寿命纪律不依赖这条循环的退出路径。
			return
		}
		if err := enc.Encode(eventNDJSONLine{
			HostID:    e.DeviceID,
			Hostname:  e.Hostname,
			T:         e.Item.T,
			Type:      e.Item.Type,
			Action:    e.Item.Action,
			ActorName: e.Item.ActorName,
			ActorID:   e.Item.ActorID,
		}); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
