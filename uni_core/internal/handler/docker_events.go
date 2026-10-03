package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerevents"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 事件流聚合端点（六期·监控面）：
//
//	GET /docker/events         —— docker events 的跨主机聚合实时流（NDJSON，fetch 形态）
//	GET /docker/events/history —— 跨主机事件历史（保留窗口，JSON，游标分页）
//
// 与日志/stats 端点同一条认证判断 —— 走 auth 组、浏览器能带 Authorization ——
// 但它是**聚合流**（路由静态 perm docker:list，与总览同档），处理器里没有「按指令
// 记录校验归属」这一步：它不接任何单主机会话，接入的是 core 自己的常驻订阅的
// **扇出副本**，凭据纪律与读面其余端点一致。历史端点同理（静态 perm docker:list）：
// 它读的是管理器内存里的保留窗口（core 自己收下的事实），没有会话归属可言。
//
// eventNDJSONLine 是事件流的一行（Content-Type: application/x-ndjson）。
//
// 字段**直接打平**（与 stats 样本行同纪律）：前端要能用一行字段渲染活动流条目，
// 而不是先解 base64 再解一层 JSON；host_id/hostname 是 core **注入的归属**
// （协议载荷里没有 —— agent 只上报自己主机的记录，不知道「自己在清单里叫什么」）。
type eventNDJSONLine struct {
	// HostID 是归属主机 id，**字符串形态**（`json:"host_id,string"`）。
	//
	// 为什么不是数字：设备 id 是雪花值（实测 2105604795992641536 > 2^53），JSON
	// number 在消费端（JS）会丢精度 —— 实测症状有两处：主机归属要按 id 跨两个读面
	// 对齐时对不上（历史 DTO 是 string 编码、流行是数字，同一条事件在两边算出两个
	// 不同的 hostId，前端合并去重键因此永不相等），以及降级显示「主机 <id>」会打出
	// 一个错的雪花值。全站（含同族的 DockerEventHistoryItem.hostId）本来就是
	// string 编码，这条流行是漏网的一处。
	HostID    uint64 `json:"host_id,string"`
	Hostname  string `json:"hostname"`
	T         int64  `json:"t"`
	Type      string `json:"type"`
	Action    string `json:"action"`
	ActorName string `json:"actor_name,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
	// ExitCode 是 die 事件的退出码（nil = 不可考：非 die / 旧 agent / daemon 没给）。
	// 消费侧不显示、不猜（与协议 DockerEventItem.ExitCode 同一句话）。
	ExitCode *int32 `json:"exit_code,omitempty"`
}

// ndjsonOpenedFrame 是 **NDJSON 流的首帧 sentinel**（kind=opened）。
//
// 形态纪律：kind=opened 不与任何数据行的形状重叠（事件行必有 host_id/t/type/action；
// 日志行必有 seq；样本/进度行必有 t 或终态标记），且消费端对未知形状的行**一律
// 跳过**是全 NDJSON 族既有的解析纪律（各 parse*Line 返回 null 即跳过，逐函数验证过）。
type ndjsonOpenedFrame struct {
	Kind string `json:"kind"`
}

// writeNDJSONOpened 在流建立、响应头写出之后立即写一帧 sentinel 并冲刷。
//
// 为什么必须有它（实测病灶）：保留窗口空 + 舰队安静时，这条流在首条真事件之前
// **一个字节都没有** —— 直连 core（:8088）时响应头 8ms 就到，但经 vite dev 代理
// （以及任何「等首字节才放行响应头」的中间层）时头被压着不发，前端 fetch 一直
// pending，页面停在「连接中…」直到第一条事件（可能几十分钟后）。sentinel 让
// 「流已建立」这件事本身产生一个字节，头随即放行，页面立刻进入实时态。
//
// 推广评估（本波结论：只挂 events，其余五条流留待一条独立小波）：
//   - **兼容性**：log/stats/pull/build/push 五条流的前端解析器**全部容忍**本帧
//     （parseLogStreamLine 要求 seq 是数字、parseStatsStreamLine 要求 t 为正、
//     pull/build 的解析器要求至少一个内容字段在场 —— 本帧三样都不满足，一律
//     null 跳过），技术上「能全兼容」。
//   - **为什么仍只做 events**：其一，那五条是**会话制**端点（每条的会话建立后
//     agent 就有 tail / 首采样 / 首条 daemon 状态可发），「零数据」不是它们的
//     常态，而 events 的窗口可以是空的（刚重启 / 舰队安静）；其二，给五条流加
//     首行要同时改它们的逐行断言测试（handler 测试按行数断言），改动面不在本波
//     边界内。推广动作已被压缩成「每条 serve*NDJSON 在建流处调一次
//     writeNDJSONOpened」。
func writeNDJSONOpened(enc *json.Encoder, flusher http.Flusher) error {
	if err := enc.Encode(ndjsonOpenedFrame{Kind: "opened"}); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// EventsStream 事件聚合流：先回放各主机保留窗口的最近 50 条（打开即有内容），再进实时。
//
// 引用计数纪律全部收敛在管理器里：首个连接建立常驻订阅、最后一个断开全量退订，
// 这里只做「订阅 → 转发 → 收尾必 Close」三件事 —— Close 经 defer 保证即使写失败/
// 客户端断开也把引用减回去（否则会留下跑在 agent 身上的孤儿订阅）。
//
// @Summary      Docker 事件聚合流(NDJSON)
// @Description  跨主机聚合的容器/镜像/卷/网络事件流；连接即先发一帧 {"kind":"opened"}（消费端跳过），随后回放各主机最近 50 条再实时推送；每行 {"host_id"(字符串形态的雪花 id),"hostname","t","type","action","actor_name","actor_id","exit_code"}
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
	// 首帧 sentinel：把响应头立刻推出代理（病灶与推广评估见 writeNDJSONOpened）。
	// 写失败 = 客户端已走：直接收尾（defer 的 Close 把引用减回去）。
	if err := writeNDJSONOpened(enc, flusher); err != nil {
		return
	}
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
			ExitCode:  e.Item.ExitCode,
		}); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// EventsHistory 事件历史查询：管理器保留窗口上的一页（跨主机聚合，照 9a 读面模式）。
//
// 与流的分工：流回答「此刻起发生了什么」（连接回放近 50 条 + 实时），历史回答
// 「刚才到现在有过什么」（窗口内按 t 降序、游标分页、可过滤）。前端详版页的加载序
// （先拉历史、再接流、按去重键合并）建立在这两条端点之上 —— 合并去重键由前端定
// （事件没有天然唯一 id），服务端只保证「同一窗口、同一份事实」。
//
// 权限：路由静态 perm docker:list（与流/读面其余端点同档）—— 它不接会话、不按
// 记录校验归属，看的是 core 自己收下的舰队事实。
//
// @Summary      Docker 事件历史
// @Description  跨主机事件保留窗口（每主机最近 30 分钟且至多 500 条）的一页：hostId/type/keyword 过滤（互相独立、逐层收窄）、t 降序、limit+游标分页；total 为过滤后窗口内全量条数；窗口空/无匹配返回空数组
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId   query  uint64  false  "限定单主机(缺省=跨主机聚合)"
// @Param        type     query  string  false  "资源类型过滤(container/image/volume/network)"
// @Param        keyword  query  string  false  "主体名/主体id/动作原文子串(大小写不敏感)"
// @Param        limit    query  int     false  "单页条数(缺省 200,上限 500)"
// @Param        cursor   query  string  false  "上一页 nextCursor(原样回传;缺省=从最新开始)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerEventHistoryResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误(type 非法 / 游标非法)"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限(docker:list)"
// @Failure      500  {object}  app.Response  "事件流未装配 / 内部错误"
// @Router       /docker/events/history [get]
func (h *DockerHandler) EventsHistory(c *gin.Context) {
	if h.events == nil {
		app.Error(c, apperror.Internal("事件流未装配"))
		return
	}
	var q request.DockerEventHistoryQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	if q.Type != "" && !agentproto.IsDockerEventType(q.Type) {
		// 非法类型给结论句而不是静默忽略（与 Workloads 的 state 同一句话：静默忽略
		// 会让用户以为「筛了但没生效」）。
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	page, err := h.events.Query(dockerevents.QueryFilter{
		HostID:  q.HostID,
		Type:    q.Type,
		Keyword: q.Keyword,
	}, q.Limit, q.Cursor)
	if err != nil {
		if errors.Is(err, dockerevents.ErrBadCursor) {
			app.Error(c, apperror.BadRequest("请求参数不合法"))
			return
		}
		app.Error(c, apperror.Internal("内部错误", err))
		return
	}
	items := make([]response.DockerEventHistoryItem, 0, len(page.Items))
	for _, e := range page.Items {
		items = append(items, response.DockerEventHistoryItem{
			HostID:    e.DeviceID,
			Hostname:  e.Hostname,
			T:         e.Item.T,
			Type:      e.Item.Type,
			Action:    e.Item.Action,
			ActorName: e.Item.ActorName,
			ActorID:   e.Item.ActorID,
			ExitCode:  e.Item.ExitCode,
		})
	}
	app.Success(c, response.DockerEventHistoryResp{
		Items:      items,
		Total:      page.Total,
		NextCursor: page.NextCursor,
	})
}
