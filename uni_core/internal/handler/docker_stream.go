package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 流通道的对外端点：
//
//	GET /docker/hosts/:id/cmds/:ref/stream  —— 日志 Follow：NDJSON，Bearer + 归属校验
//	                                          （container:logs{follow} 与 compose:logs
//	                                          聚合日志共用 —— 5a 零新端点）
//	GET /docker/hosts/:id/cmds/:ref/stats   —— stats 实时流：NDJSON（样本行），Bearer + 归属校验
//	GET /docker/hosts/:id/cmds/:ref/pull    —— 拉取进度流：NDJSON（进度行），Bearer + 归属校验
//	GET /docker/hosts/:id/cmds/:ref/build   —— 构建进度流：NDJSON（步骤/文本行），Bearer + 归属校验
//	GET /docker/hosts/:id/cmds/:ref/push    —— 推送进度流：NDJSON（进度行），Bearer + 归属校验
//	POST /docker/hosts/:id/cmds/:ref/cancel —— 显式取消进度族指令（202），Bearer + 归属校验
//	GET /docker/hosts/:id/stream/exec       —— 终端：WebSocket 升级，一次性 ticket
//
// 为什么两类端点的认证模型不同：日志 / stats / 各进度流用 fetch + ReadableStream
// （前端 HTTP 层能带 Authorization），终端必须用浏览器 WebSocket（带不了头），故走
// 一次性 ticket。这不是两套安全策略，而是同一条纪律（凭证不落 URL 长驻）在不同
// 浏览器 API 下的形态。进度三族走 fetch 形态（与日志/stats 同族），且它们是
// 「指令还在 pending 就可接入」的流 —— 会话由受理指令时预登记（见
// service/docker_cmd.go 与协议 DockerPullSessionID / DockerBuildSessionID /
// DockerPushSessionID）。
//
// **观看与执行解耦**（本波语义收口）：进度三族的流是「观看」——断开/写失败只解除
// 接入（不发 cancel），任务照常跑完、结果经既有 result 通道入账；终止操作只有一条
// 路：显式取消端点（CancelCmd）。日志/stats 维持「断开即停流」（它们是纯观看流，
// 断开不牵动任何在跑的指令）；终端维持「断开即取消」（它承载的进程本身就是会话）。
//
// 六条 NDJSON 端点全部挂 middleware.LongLived（挂载点在 router.go）：http.Server 的
// WriteTimeout 是**整条响应**的绝对窗口，不接管写截止的话，任何 >30s 的流都会在
// 30.0s 被写失败掐断 —— 对进度三族，这声掐断现在只是「观看结束」（任务不受影响），
// 对日志/终端仍是实打实的断开。机制说明见 internal/middleware/long_lived.go。

const (
	// execWriteTimeout 是单条 WS 消息的写超时：对端 TCP 卡死时写侧不能永久挂住
	// （它是会话收尾路径上的最后一环）。
	execWriteTimeout = 10 * time.Second
	// maxExecClientBytes 是客户端 WS 消息上限：输入明文 ≤ 协议单帧上限（64KB），
	// 再留 JSON 包壳的余量。没有上限时一条巨帧就能打爆服务端内存。
	maxExecClientBytes = agentproto.MaxDockerFrameDataBytes + (4 << 10)

	// WS 消息类型（plan §0 的线上形状，console 与 core 的共同契约）。
	execMsgData   = "data"
	execMsgEOF    = "eof"
	execMsgInput  = "input"
	execMsgResize = "resize"
	execMsgCancel = "cancel"
)

// dockerExecUpGrader 是终端 WS 的升级器。
//
// Origin 策略**与 console/agent 两个端点共用同一个函数值**（internal/pkg/ws.CheckOrigin）：
// 策略一旦分叉就会变成「为什么 console 能连、终端不能」的线上玄学，而两者肩并肩
// 挂在同一个 api 组上。
var dockerExecUpGrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     ws.CheckOrigin,
}

// ndjsonLine 是日志流的一行（Content-Type: application/x-ndjson）。
//
// Data 用 []byte：JSON 编码自动得到 base64（与协议帧的 data 字段同一形态），
// 非 UTF-8 的日志字节不会被字符串编码悄悄替换成 U+FFFD。
type ndjsonLine struct {
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof"`
}

// LogStream 日志 Follow：把会话帧以 NDJSON 持续写给客户端。
//
// 认证与归属：JWT（auth 组）+ 按记录权限码再校验一次 + **发起人归属**（spec §10.5）。
// 三条缺一不可 —— 记录里带的是会话句柄，接入即读该会话的全部输出。
//
// 端点承载 container:logs{follow:true} 与 compose:logs 两种 action（5a 聚合日志
// 复用同一端点、零新路由）：两者的数据形态都是「原始文本行 + eof」—— compose 聚合
// 输出本身就是 `<时间戳> <服务>  | 正文` 的行，NDJSON 的 {seq,data,eof} 原样承载，
// 行形状不需要也不应该分叉（分叉只会让 5b 的工作台多接一种解析）。
//
// @Summary      容器/项目聚合日志流(NDJSON Follow)
// @Description  按指令号接入已建立的日志流会话（container:logs follow 或 compose:logs 项目聚合日志）；每行 {"seq","data"(base64),"eof"}；客户端断开即下发 cancel
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      400  "请求参数不合法 / 该指令不支持日志流"
// @Failure      401  "未登录"
// @Failure      403  "无权限 / 非发起人"
// @Failure      404  "指令不存在或已过期"
// @Failure      409  "该指令没有可接入的流会话 / 会话已有连接"
// @Router       /docker/hosts/{id}/cmds/{ref}/stream [get]
func (h *DockerHandler) LogStream(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	if h.streams == nil {
		app.Error(c, apperror.Internal("流通道未装配"))
		return
	}
	rec, err := h.cmds.Lookup(c.Request.Context(), id, ref)
	if err != nil {
		app.Error(c, err)
		return
	}
	if !h.guard.Ensure(c, rec.Perm) {
		return // Ensure 已写好 403
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return
	}
	if rec.UserID != uid {
		// 归属校验：发起人才能接（否则 403）。防止同权限用户劫持别人的日志。
		app.Error(c, apperror.Forbidden("无权接入该指令的流会话"))
		return
	}
	if rec.Action != agentproto.DockerActionContainerLogs && rec.Action != agentproto.DockerActionComposeLogs {
		// 这条端点只承载**文本日志流**（container:logs follow / compose:logs 聚合）：
		// stats/pull 走各自的打平字段端点、PTY 走专用 WebSocket（通道的消息形状不同，
		// 交叉使用只会让 console 收到它解析不了的行）。
		app.Error(c, apperror.BadRequest("该指令不支持日志流"))
		return
	}
	if rec.SessionID == "" {
		// 一次性取日志（follow=false）没有会话：前端不该来接，接了给结论句而不是空白流。
		app.Error(c, apperror.Conflict("该指令没有可接入的流会话"))
		return
	}
	sess, err := h.streams.AttachSession(rec.SessionID, uid, id)
	if err != nil {
		app.Error(c, err)
		return
	}
	h.serveLogNDJSON(c, sess)
}

// serveLogNDJSON 是日志流的转发循环。
//
// 收尾的两种结局不可混淆：
//   - 收到 agent 的 eof（转发完那一行）→ 会话自然结束，只做幂等清理；
//   - 其余（ctx 被取消 = 客户端断开、写失败、会话被取消）→ **向 agent 发 cancel**
//     （plan §0：客户端断开 → 下发 cancel），否则日志进程会一直挂到 agent 的空闲超时。
func (h *DockerHandler) serveLogNDJSON(c *gin.Context, sess *dockerstream.Session) {
	ctx := c.Request.Context()
	// 流式响应的三个头：Content-Type 是契约；no-store 防缓存；X-Accel-Buffering
	// 让 nginx 不缓冲（否则每行都堵在代理缓冲里，Follow 变成「几十秒后一次性出现」）。
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	ended := false
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			break
		}
		if err := enc.Encode(ndjsonLine{Seq: f.Seq, Data: f.Data, EOF: f.EOF}); err != nil {
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.EOF {
			ended = true
			break
		}
	}
	if ended {
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.CancelSession(context.Background(), sess, "客户端断开或写失败")
}

// statsNDJSONLine 是 stats 流的一行（Content-Type: application/x-ndjson）。
//
// 样本字段**直接打平**（帧 data 里的 JSON 样本行被解码成字段转发）：曲线数据要能被
// 前端直接用来画点，而不是先解 base64 再解一层 JSON。字段名与快照 DockerContainer 的
// 同名字段一致 —— 抽屉里的曲线与表格读数是同一套数据约定。
type statsNDJSONLine struct {
	Seq uint64 `json:"seq"`
	// T 是 agent 采样时刻（unix 毫秒），曲线的 x 轴刻度。
	T             int64   `json:"t"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsageMB    float64 `json:"mem_usage_mb"`
	MemLimitMB    float64 `json:"mem_limit_mb"`
	NetRXBytesSec float64 `json:"net_rx_bytes_sec"`
	NetTXBytesSec float64 `json:"net_tx_bytes_sec"`
	EOF           bool    `json:"eof"`
}

// StatsStream 容器 stats 实时流：把会话帧里的样本以 NDJSON 持续写给客户端。
//
// 认证与归属与日志流**逐条相同**（JWT + 按记录权限码再校验 + 发起人归属）：
// 它同样是「接入即读该会话全部输出」的通道，纪律不许因为数据类型不同而有差别。
// 与日志流的差别只有行形状（样本字段 vs data/base64）。
//
// @Summary      容器 stats 实时流(NDJSON)
// @Description  按指令号接入已建立的 stats 会话；每行一个样本 {"seq","t","cpu_percent","mem_usage_mb","mem_limit_mb","net_rx_bytes_sec","net_tx_bytes_sec","eof"}；客户端断开即下发 cancel
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      400  "请求参数不合法 / 该指令不支持 stats 流"
// @Failure      401  "未登录"
// @Failure      403  "无权限 / 非发起人"
// @Failure      404  "指令不存在或已过期"
// @Failure      409  "该指令没有可接入的流会话 / 会话已有连接"
// @Router       /docker/hosts/{id}/cmds/{ref}/stats [get]
func (h *DockerHandler) StatsStream(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	if h.streams == nil {
		app.Error(c, apperror.Internal("流通道未装配"))
		return
	}
	rec, err := h.cmds.Lookup(c.Request.Context(), id, ref)
	if err != nil {
		app.Error(c, err)
		return
	}
	if !h.guard.Ensure(c, rec.Perm) {
		return // Ensure 已写好 403
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return
	}
	if rec.UserID != uid {
		app.Error(c, apperror.Forbidden("无权接入该指令的流会话"))
		return
	}
	if rec.Action != agentproto.DockerActionContainerStats {
		// 这条端点只承载 stats 流：日志走 NDJSON 日志端点、PTY 走专用 WebSocket
		// （三条通道的行形状不同，交叉使用只会让 console 收到它解析不了的行）。
		app.Error(c, apperror.BadRequest("该指令不支持 stats 流"))
		return
	}
	if rec.SessionID == "" {
		app.Error(c, apperror.Conflict("该指令没有可接入的流会话"))
		return
	}
	sess, err := h.streams.AttachSession(rec.SessionID, uid, id)
	if err != nil {
		app.Error(c, err)
		return
	}
	h.serveStatsNDJSON(c, sess)
}

// serveStatsNDJSON 是 stats 流的转发循环：帧 data 是一条或多条 JSON 样本行，
// 逐行解码成字段写成 NDJSON 行；eof 挂在最后一个样本行上（与日志流同理）；
// 一行解码失败时**跳过该行**并留痕 —— 丢一个样本点只让曲线缺一格（seq 让消费端
// 知道），把整条流转成错误状态则是杀了全部数据。
//
// 收尾纪律与 serveLogNDJSON 相同：收到 eof 转发完 → 幂等清理；其余（客户端断开、
// 写失败、会话被取消）→ 向 agent 发 cancel。
func (h *DockerHandler) serveStatsNDJSON(c *gin.Context, sess *dockerstream.Session) {
	ctx := c.Request.Context()
	// 流式响应的三个头与日志流同一来源（no-store 防缓存、X-Accel-Buffering
	// 让 nginx 不缓冲 —— 样本行堵在代理缓冲里就会变成「几十秒后一次性出现」）。
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	ended := false
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			break
		}
		lines := 0
		for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
			if len(raw) == 0 {
				continue
			}
			var s agentproto.DockerStatsSample
			if err := json.Unmarshal(raw, &s); err != nil || s.Validate() != nil {
				// 解不开的样本行：跳过并留痕（与注册表「非法帧丢弃并告警」同一纪律）。
				if h.log != nil {
					h.log.Warn("docker stats sample dropped (invalid)",
						zap.String("session", sess.ID()), zap.Uint64("seq", f.Seq), zap.Error(err))
				}
				continue
			}
			line := statsNDJSONLine{Seq: f.Seq, T: s.T, CPUPercent: s.CPUPercent,
				MemUsageMB: s.MemUsageMB, MemLimitMB: s.MemLimitMB,
				NetRXBytesSec: s.NetRXBytesSec, NetTXBytesSec: s.NetTXBytesSec}
			if f.EOF {
				line.EOF = true // eof 挂在最后一个样本行（与日志「末帧数据+eof 同帧」同理）
			}
			if err := enc.Encode(line); err != nil {
				h.streams.CancelSession(context.Background(), sess, "客户端断开或写失败")
				return
			}
			lines++
		}
		if f.EOF && lines == 0 {
			// eof 帧不带样本：单独发一行 eof（消费端不能永远等不到收尾信号）。
			if err := enc.Encode(statsNDJSONLine{Seq: f.Seq, EOF: true}); err != nil {
				h.streams.CancelSession(context.Background(), sess, "客户端断开或写失败")
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.EOF {
			ended = true
			break
		}
	}
	if ended {
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.CancelSession(context.Background(), sess, "客户端断开或写失败")
}

// progressStreamSession 是进度三族（pull/build/push）端点共用的接入序：
// 参数 → 记录 → 权限（记录的权限码）→ 发起人归属 → **派生句柄**接入。
// 三族逐条同款（JWT 由 auth 组的前端 fetch 层负责；看进度与发起操作同档，
// 因为进度如实露出「在拉/在推/在构建什么」）。
//
// streamDesc 用于 action 不匹配时的结论句（每条端点报自己的名字）。
// 失败路径已写好响应并返回 (nil, false)；成功返回已接入的会话。
func (h *DockerHandler) progressStreamSession(c *gin.Context, streamAction, streamDesc string) (*dockerstream.Session, bool) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return nil, false
	}
	ref := c.Param("ref")
	if ref == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return nil, false
	}
	if h.streams == nil {
		app.Error(c, apperror.Internal("流通道未装配"))
		return nil, false
	}
	rec, err := h.cmds.Lookup(c.Request.Context(), id, ref)
	if err != nil {
		app.Error(c, err)
		return nil, false
	}
	if !h.guard.Ensure(c, rec.Perm) {
		return nil, false // Ensure 已写好 403
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return nil, false
	}
	if rec.UserID != uid {
		// 归属校验：发起人才能接（否则 403）。防止同权限用户劫持别人的进度/日志。
		app.Error(c, apperror.Forbidden("无权接入该指令的流会话"))
		return nil, false
	}
	if rec.Action != streamAction {
		// 每条端点只承载自己的进度族：日志/stats 走各自端点（各通道的行形状不同，
		// 交叉使用只会让 console 收到它解析不了的行）。
		app.Error(c, apperror.BadRequest("该指令不支持"+streamDesc))
		return nil, false
	}
	sess, err := h.streams.AttachSession(progressSessionIDOf(streamAction, rec.Ref), uid, id)
	if err != nil {
		app.Error(c, err)
		return nil, false
	}
	return sess, true
}

// progressSessionIDOf 与 core 受理时预登记同源的**派生句柄**（协议
// DockerPullSessionID / DockerBuildSessionID / DockerPushSessionID）—— 进度会话
// 不取记录的 session_id（result 只在操作结束时回，届时进度也发完了）。
func progressSessionIDOf(action, ref string) string {
	switch action {
	case agentproto.DockerActionImagePull:
		return agentproto.DockerPullSessionID(ref)
	case agentproto.DockerActionImageBuild:
		return agentproto.DockerBuildSessionID(ref)
	case agentproto.DockerActionImagePush:
		return agentproto.DockerPushSessionID(ref)
	}
	return ""
}

// PullStream 镜像拉取进度流：接入序见 progressStreamSession —— 权限码就是
// image:pull 的受理权限码（docker:manage），因为进度如实露出「在拉什么」
// （镜像名/层/体积都是仓库面的事实）。断开只是停止观看，拉取照常跑完（终止走
// POST /cmds/:ref/cancel）。
//
// @Summary      镜像拉取进度流(NDJSON)
// @Description  按指令号接入拉取进度会话；每行一个进度记录 {"seq","t","id","status","current","total","done","error","eof"}；客户端断开只是停止观看（拉取继续，终止走 POST /cmds/:ref/cancel）
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      400  "请求参数不合法 / 该指令不支持拉取进度流"
// @Failure      401  "未登录"
// @Failure      403  "无权限 / 非发起人"
// @Failure      404  "指令不存在或已过期"
// @Failure      409  "该指令没有可接入的流会话 / 会话已有连接"
// @Router       /docker/hosts/{id}/cmds/{ref}/pull [get]
func (h *DockerHandler) PullStream(c *gin.Context) {
	sess, ok := h.progressStreamSession(c, agentproto.DockerActionImagePull, "拉取进度流")
	if !ok {
		return
	}
	h.servePullNDJSON(c, sess)
}

// pullNDJSONLine 是拉取进度流的一行（Content-Type: application/x-ndjson）。
//
// 记录字段**直接打平**（与 stats 同一取向）：进度对话框要能直接拿字段渲染
// 进度条/文案，而不是先解 base64 再解一层 JSON。字段名与协议
// DockerPullProgressItem 同名字段一一对应。
type pullNDJSONLine struct {
	Seq uint64 `json:"seq"`
	T   int64  `json:"t"`
	ID  string `json:"id,omitempty"`
	// Status 是 daemon 原文状态文案。
	Status  string `json:"status,omitempty"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
	// Done / Error 是终态标记（恰在流末一条），与 eof 同帧到最后一行。
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
	EOF   bool   `json:"eof"`
}

// servePullNDJSON 是拉取进度的转发循环：帧 data 是一条或多条进度记录行，
// 逐行解码成字段写成 NDJSON 行；eof 挂在最后一个记录行上（与 stats 流同理）；
// 一行解码失败时**跳过该行**并留痕（丢一眼进度不致命，把整条流转成错误才是）。
//
// 收尾纪律（本波语义收口，与日志/stats 的「断开即停流」**刻意分家**）：
//   - 收到 eof 转发完 → 幂等清理（自然结束）；
//   - 其余（客户端断开、写失败、会话被取消）→ **只解除接入**（ReleaseSession），
//     **不发 cancel**：进度三族的流是「观看」，不是操作本身 —— 关掉对话框/切页/
//     收起进度行只是停止观看，拉取照常跑完，结果经既有 result 通道入账。会话留在
//     注册表里等下一次接入（重新观看），寿命 = 指令寿命（终态对账回收，见 Sweep）。
//     终止这场拉取只有一条路：显式取消端点（POST /cmds/:ref/cancel →
//     CancelProgress → 下发 cancel）。
func (h *DockerHandler) servePullNDJSON(c *gin.Context, sess *dockerstream.Session) {
	ctx := c.Request.Context()
	// 流式响应的三个头与日志/stats 流同一来源（no-store 防缓存、X-Accel-Buffering
	// 让 nginx 不缓冲 —— 进度行堵在代理缓冲里就会变成「几十秒后一次性出现」）。
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	ended := false
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			break
		}
		lines := 0
		for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
			if len(raw) == 0 {
				continue
			}
			var p agentproto.DockerPullProgressItem
			if err := json.Unmarshal(raw, &p); err != nil || p.Validate() != nil {
				// 解不开的进度行：跳过并留痕（与 stats 样本行同一纪律）。
				if h.log != nil {
					h.log.Warn("docker pull progress item dropped (invalid)",
						zap.String("session", sess.ID()), zap.Uint64("seq", f.Seq), zap.Error(err))
				}
				continue
			}
			line := pullNDJSONLine{Seq: f.Seq, T: p.T, ID: p.ID, Status: p.Status,
				Current: p.Current, Total: p.Total, Done: p.Done, Error: p.Error}
			if f.EOF {
				line.EOF = true // eof 挂在最后一个进度行（与日志「末帧数据+eof 同帧」同理）
			}
			if err := enc.Encode(line); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
			lines++
		}
		if f.EOF && lines == 0 {
			// eof 帧不带进度行（终态项与 eof 分了帧）：单独发一行 eof。
			if err := enc.Encode(pullNDJSONLine{Seq: f.Seq, EOF: true}); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.EOF {
			ended = true
			break
		}
	}
	if ended {
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.ReleaseSession(sess.ID())
}

// buildNDJSONLine 是构建进度流的一行（Content-Type: application/x-ndjson）。
//
// 记录字段**直接打平**（与 pull/stats 同一取向），字段名与协议
// DockerBuildProgressItem 同名字段一一对应；Stream 是构建输出文本行
// （步骤行/输出行 —— 页面按到达顺序渲染成一份「构建播报」）。
type buildNDJSONLine struct {
	Seq uint64 `json:"seq"`
	T   int64  `json:"t"`
	ID  string `json:"id,omitempty"`
	// Status 是状态文案（步骤 1/2 + FROM node:20 这类 legacy 行）。
	Status string `json:"status,omitempty"`
	// Stream 是构建输出的文本行。
	Stream string `json:"stream,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Error  string `json:"error,omitempty"`
	EOF    bool   `json:"eof"`
}

// BuildStream 镜像构建进度流（P2）：接入序与拉取进度**逐条相同**（权限码 =
// image:build 的受理权限码 docker:manage —— 看进度与发起构建同档，构建输出会
// 露出「用什么基础镜像、跑什么步骤」）。
//
// @Summary      镜像构建进度流(NDJSON)
// @Description  按指令号接入构建进度会话；每行一个记录 {"seq","t","id","status","stream","done","error","eof"}；客户端断开只是停止观看（构建继续，终止走 POST /cmds/:ref/cancel）
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      400  "请求参数不合法 / 该指令不支持构建进度流"
// @Failure      401  "未登录"
// @Failure      403  "无权限 / 非发起人"
// @Failure      404  "指令不存在或已过期"
// @Failure      409  "该指令没有可接入的流会话 / 会话已有连接"
// @Router       /docker/hosts/{id}/cmds/{ref}/build [get]
func (h *DockerHandler) BuildStream(c *gin.Context) {
	sess, ok := h.progressStreamSession(c, agentproto.DockerActionImageBuild, "构建进度流")
	if !ok {
		return
	}
	h.serveBuildNDJSON(c, sess)
}

// serveBuildNDJSON 是构建进度的转发循环：帧 data 逐行解码成字段写成 NDJSON 行；
// eof 挂在最后一个记录行上；一行解码失败跳过并留痕（与 stats/pull 同纪律）。
// 收尾纪律同 servePullNDJSON（本波语义收口）：收到 eof 转发完 → 幂等清理；其余
// （断开/写失败）→ **只解除接入，不发 cancel** —— 构建照常跑完，终止只有显式取消
// 端点一条路。
func (h *DockerHandler) serveBuildNDJSON(c *gin.Context, sess *dockerstream.Session) {
	ctx := c.Request.Context()
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	ended := false
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			break
		}
		lines := 0
		for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
			if len(raw) == 0 {
				continue
			}
			var p agentproto.DockerBuildProgressItem
			if err := json.Unmarshal(raw, &p); err != nil || p.Validate() != nil {
				if h.log != nil {
					h.log.Warn("docker build progress item dropped (invalid)",
						zap.String("session", sess.ID()), zap.Uint64("seq", f.Seq), zap.Error(err))
				}
				continue
			}
			line := buildNDJSONLine{Seq: f.Seq, T: p.T, ID: p.ID, Status: p.Status,
				Stream: p.Stream, Done: p.Done, Error: p.Error}
			if f.EOF {
				line.EOF = true
			}
			if err := enc.Encode(line); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
			lines++
		}
		if f.EOF && lines == 0 {
			if err := enc.Encode(buildNDJSONLine{Seq: f.Seq, EOF: true}); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.EOF {
			ended = true
			break
		}
	}
	if ended {
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.ReleaseSession(sess.ID())
}

// pushNDJSONLine 是推送进度流的一行（与 pull 同字段集 —— daemon 的 push 与
// pull 是同一个 JSON 进度流，协议 DockerPushProgressItem 与 DockerPullProgressItem
// 同形）。
type pushNDJSONLine struct {
	Seq uint64 `json:"seq"`
	T   int64  `json:"t"`
	ID  string `json:"id,omitempty"`
	// Status 是原文状态文案（"The push refers to …" / "Pushing" / "Pushed"…）。
	Status  string `json:"status,omitempty"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Error   string `json:"error,omitempty"`
	EOF     bool   `json:"eof"`
}

// PushStream 镜像推送进度流（P2）：接入序与拉取/构建进度**逐条相同**（权限码 =
// image:push 的受理权限码 docker:manage —— 看进度与发起推送同档）。
//
// @Summary      镜像推送进度流(NDJSON)
// @Description  按指令号接入推送进度会话；每行一个进度记录 {"seq","t","id","status","current","total","done","error","eof"}；客户端断开只是停止观看（推送继续，终止走 POST /cmds/:ref/cancel）
// @Tags         Docker 管理
// @Produce      application/x-ndjson
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  "流已建立(逐行 NDJSON)"
// @Failure      400  "请求参数不合法 / 该指令不支持推送进度流"
// @Failure      401  "未登录"
// @Failure      403  "无权限 / 非发起人"
// @Failure      404  "指令不存在或已过期"
// @Failure      409  "该指令没有可接入的流会话 / 会话已有连接"
// @Router       /docker/hosts/{id}/cmds/{ref}/push [get]
func (h *DockerHandler) PushStream(c *gin.Context) {
	sess, ok := h.progressStreamSession(c, agentproto.DockerActionImagePush, "推送进度流")
	if !ok {
		return
	}
	h.servePushNDJSON(c, sess)
}

// servePushNDJSON 是推送进度的转发循环（与 servePullNDJSON 同构：同 line 形状、
// 同 eof 纪律、同解码失败跳过、同「断开只解除接入」的收尾 —— 只有 item 类型与
// 日志措辞不同）。
func (h *DockerHandler) servePushNDJSON(c *gin.Context, sess *dockerstream.Session) {
	ctx := c.Request.Context()
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	enc := json.NewEncoder(c.Writer)
	ended := false
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			break
		}
		lines := 0
		for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
			if len(raw) == 0 {
				continue
			}
			var p agentproto.DockerPushProgressItem
			if err := json.Unmarshal(raw, &p); err != nil || p.Validate() != nil {
				if h.log != nil {
					h.log.Warn("docker push progress item dropped (invalid)",
						zap.String("session", sess.ID()), zap.Uint64("seq", f.Seq), zap.Error(err))
				}
				continue
			}
			line := pushNDJSONLine{Seq: f.Seq, T: p.T, ID: p.ID, Status: p.Status,
				Current: p.Current, Total: p.Total, Done: p.Done, Error: p.Error}
			if f.EOF {
				line.EOF = true
			}
			if err := enc.Encode(line); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
			lines++
		}
		if f.EOF && lines == 0 {
			if err := enc.Encode(pushNDJSONLine{Seq: f.Seq, EOF: true}); err != nil {
				h.streams.ReleaseSession(sess.ID())
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if f.EOF {
			ended = true
			break
		}
	}
	if ended {
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.ReleaseSession(sess.ID())
}

// CancelCmd 显式取消一条进度族指令（image:pull / image:build / image:push）。
//
// 它是「取消」这件事在系统里的**唯一**入口（本波语义收口）：观看面（进度流的
// 断开）与执行彻底解耦 —— 关对话框/切页/收起进度行都只是停止观看，只有这里会
// 向 agent 下发 cancel 帧去终止操作。
//
// 鉴权与进度流接入**同一张档**（三闸逐条相同）：记录存在且设备匹配（404）、
// 记录的权限码 —— 进度三族都是 docker:manage（403）、发起人归属（403）。刻意与
// 流端点逐字对齐而不是另立一档：取消端点的权限包络必须等于它替换掉的那条隐式
// 路径（断流即取消），否则「有了一个显式按钮」就悄悄放宽或收紧了同一件事。
//
// 202 而不是 200：这里受理的是**取消请求**，截止与否由指令记录结算 —— agent 按
// 操作的实际结局回写（迟到的 cancel 是 no-op：操作若恰好已完成，终态是成功而
// 不是「已取消」，见 pull_progress.go 的竞态注释）。core 不代 agent 下结论，
// 也不改写记录（记录的终态只有 agent 的 result 一个写入者）。
//
// @Summary      取消进度族指令
// @Description  向目标主机下发 cancel 帧终止 image:pull / image:build / image:push 的执行（best-effort）；返回 202 表示取消请求已下发，截止与否以指令记录的终态为准
// @Tags         Docker 管理
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      202  {object}  app.Response{data=response.DockerCmdResp}  "取消请求已下发（data.ref 为指令号）"
// @Failure      400  {object}  app.Response  "请求参数不合法 / 该任务不支持取消"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无操作权限 / 非发起人"
// @Failure      404  {object}  app.Response  "指令不存在或已过期"
// @Failure      409  {object}  app.Response  "该任务已结束"
// @Failure      500  {object}  app.Response  "流通道未装配 / 取消未送达（设备离线等）"
// @Router       /docker/hosts/{id}/cmds/{ref}/cancel [post]
func (h *DockerHandler) CancelCmd(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	if h.streams == nil {
		app.Error(c, apperror.Internal("流通道未装配"))
		return
	}
	rec, err := h.cmds.Lookup(c.Request.Context(), id, ref)
	if err != nil {
		app.Error(c, err)
		return
	}
	if !h.guard.Ensure(c, rec.Perm) {
		return // Ensure 已写好 403
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return
	}
	if rec.UserID != uid {
		// 归属校验：取消是「打断别人正在做的事」，比看进度更需要归属 —— 与原
		// 「断流即取消」路径的判定逐字相同（只有发起人能接流，也就只有他能取消）。
		app.Error(c, apperror.Forbidden("无权取消该指令"))
		return
	}
	if rec.Status != dockerstate.StatusPending {
		// 已终态：没有可取消的执行。迟到的点击如实回一句，而不是发一帧噪声
		//（agent 侧那条会话可能早已随 eof 收摊）。
		app.Error(c, apperror.Conflict("该任务已结束"))
		return
	}
	cancellable, err := h.streams.CancelProgress(c.Request.Context(), rec, "用户取消")
	if err != nil {
		// 帧未送达（设备离线等）：如实说，别让用户以为「点了取消就一定会截止」。
		app.Error(c, err)
		return
	}
	if !cancellable {
		// 有记录但不是进度三族（如 container:stop 这类短写指令）：没有可取消的会话。
		app.Error(c, apperror.BadRequest("该任务不支持取消"))
		return
	}
	// 202 Accepted：取消请求已下发，不是「已取消」—— 信封与 SendCmd 同形
	//（同一个 app.Response + 同一个 DockerCmdResp），前端拦截器认得出。
	c.JSON(http.StatusAccepted, app.Response{
		Code: apperror.CodeOK, Message: "success", Data: response.DockerCmdResp{Ref: ref},
	})
}

// execClientMsg 是终端 WS 的**客户端 → 服务端**消息。
//
// input.data 是**明文 UTF-8**（键盘输入是文本，且 xterm 的 onData 本就是字符串）——
// 与 data 帧的 base64 刻意不对称：服务端 → 客户端的 PTY 输出可能不是合法 UTF-8
// （二进制控制序列），必须 base64；客户端输入不需要。混用两个方向会让「用户输入的
// 恰好是合法 base64 字符串」被静默转义（例如输入 "test"），那是不可接受的歧义。
type execClientMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// execServerMsg 是**服务端 → 客户端**消息（data 为 base64；eof 不带 data）。
type execServerMsg struct {
	Type string `json:"type"`
	Data []byte `json:"data,omitempty"`
}

// ExecStream 终端：一次性 ticket 认证 + WebSocket 升级 + 双向转发。
//
// ticket 在**升级之前**校验并消费（失败按 401/403 应答，且不泄漏「是过期还是不存在」）；
// 同一会话的第二条连接在接入处拿 409。升级失败时释放接入占用但**不取消会话**
// （agent 侧还在跑，用户重新轮询即可拿到新票据重接）。
//
// @Summary     容器终端(WebSocket)
// @Description  用 streamTicket 升级；服务端发 {"type":"data","data":base64} / {"type":"eof"}，客户端发 input/resize/cancel
// @Tags         Docker 管理
// @Param        id      path  uint64  true  "设备ID"
// @Param        ticket  query string  true  "一次性票据(来自 cmds/:ref 结果)"
// @Success      101  "握手成功，协议切换到 WebSocket"
// @Failure      400  "参数不合法"
// @Failure      401  "票据无效或已过期"
// @Failure      403  "票据与主机不符"
// @Failure      409  "该流会话已有连接"
// @Router       /docker/hosts/{id}/stream/exec [get]
func (h *DockerHandler) ExecStream(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if h.streams == nil {
		app.Error(c, apperror.Internal("流通道未装配"))
		return
	}
	ticket := c.Query("ticket")
	if ticket == "" {
		app.Error(c, apperror.Unauthorized("票据无效或已过期"))
		return
	}
	tk, err := h.streams.RedeemTicket(c.Request.Context(), ticket)
	if err != nil {
		app.Error(c, err)
		return
	}
	if tk == nil {
		// 不存在与已过期同码同话术：票据是凭证，不向持票人泄漏「它曾经存在过」。
		app.Error(c, apperror.Unauthorized("票据无效或已过期"))
		return
	}
	if tk.DeviceID != id {
		// 有效票据但不是这台主机：同样是「不泄漏细节」的 403（不带任何 id 信息）。
		app.Error(c, apperror.Forbidden("票据无效或已过期"))
		return
	}
	sess, err := h.streams.AttachSession(tk.SessionID, tk.UserID, id)
	if err != nil {
		app.Error(c, err)
		return
	}
	if sess.Kind() != dockerstream.KindPTY {
		// 日志会话不能从这个端点接入（消息形状不同）；释放占用，票据已消耗。
		h.streams.ReleaseSession(sess.ID())
		app.Error(c, apperror.BadRequest("该流会话不是终端会话"))
		return
	}
	conn, err := dockerExecUpGrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// gorilla 已把 HTTP 错误写进 c.Writer（Origin 拒绝/非 WS 请求）；这里只记日志。
		h.streams.ReleaseSession(sess.ID())
		if h.log != nil {
			h.log.Warn("docker exec ws upgrade failed", zap.String("session", sess.ID()), zap.Error(err))
		}
		return
	}
	defer conn.Close()
	h.serveExecWS(c, sess, conn)
}

// serveExecWS 是终端的双向转发：一个读协程（客户端 → agent）与一个写协程
// （会话帧 → 客户端）。gorilla 只允许「一个并发读 + 一个并发写」，这是它的边界。
//
// 收尾纪律：客户端断开（读错误）→ 取消会话（向 agent 发 cancel）；收到 eof 并
// 转发完毕 → 幂等清理。两条路径都以「写协程退出」为界，避免在 Close 之后还有写。
func (h *DockerHandler) serveExecWS(c *gin.Context, sess *dockerstream.Session, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.pumpExecToClient(ctx, sess, conn)
	}()

	conn.SetReadLimit(maxExecClientBytes)
	for {
		var m execClientMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		switch m.Type {
		case execMsgInput:
			if m.Data == "" {
				continue
			}
			if err := h.streams.SendInput(ctx, sess, []byte(m.Data)); err != nil {
				h.logDebug("docker exec input not delivered", sess, zap.Error(err))
			}
		case execMsgResize:
			if err := h.streams.SendResize(ctx, sess, m.Cols, m.Rows); err != nil {
				h.logDebug("docker exec resize not delivered", sess, zap.Error(err))
			}
		case execMsgCancel:
			// 客户端显式取消：立刻终止 agent 侧进程并释放槽位，不等读循环自然退出。
			h.streams.CancelSession(context.Background(), sess, "客户端取消")
			cancel()
			<-writerDone
			return
		default:
			// 未知类型忽略（向前兼容：将来加 ping/ack 之类不需要两端同步升级）。
		}
	}
	// 客户端断开：让写侧从 Next 上醒来并收手（写超时保证它不会永久挂住）。
	cancel()
	<-writerDone
	if sess.Closed() {
		// 会话已自然收尾（eof 已转发）或已被取消（超时/离线 sweep）：幂等清理即可。
		h.streams.FinishSession(sess.ID())
		return
	}
	h.streams.CancelSession(context.Background(), sess, "客户端断开")
}

// pumpExecToClient 把会话帧写给客户端：数据帧先发（eof 可与末帧数据同帧，不能丢），
// 再发 eof 并关闭连接。
func (h *DockerHandler) pumpExecToClient(ctx context.Context, sess *dockerstream.Session, conn *websocket.Conn) {
	for {
		f, err := sess.Next(ctx)
		if err != nil {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "会话已结束"),
				time.Now().Add(execWriteTimeout))
			_ = conn.Close()
			return
		}
		if len(f.Data) > 0 {
			if err := writeExecJSON(conn, execServerMsg{Type: execMsgData, Data: f.Data}); err != nil {
				return
			}
		}
		if f.EOF {
			_ = writeExecJSON(conn, execServerMsg{Type: execMsgEOF})
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(execWriteTimeout))
			// 主动关闭：让阻塞在 ReadJSON 的读侧立刻醒来（Close 可与其它方法并发调用）。
			_ = conn.Close()
			return
		}
	}
}

func writeExecJSON(conn *websocket.Conn, msg execServerMsg) error {
	_ = conn.SetWriteDeadline(time.Now().Add(execWriteTimeout))
	return conn.WriteJSON(msg)
}

func (h *DockerHandler) logDebug(msg string, sess *dockerstream.Session, fields ...zap.Field) {
	if h.log == nil {
		return
	}
	fields = append(fields, zap.String("session", sess.ID()), zap.Uint64("deviceId", sess.DeviceID()))
	h.log.Debug(msg, fields...)
}
