package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 流通道的两个对外端点（三期，spec §4.1）：
//
//	GET /docker/hosts/:id/cmds/:ref/stream  —— 日志 Follow：NDJSON，Bearer + 归属校验
//	GET /docker/hosts/:id/stream/exec       —— 终端：WebSocket 升级，一次性 ticket
//
// 为什么两条端点的认证模型不同：日志用 fetch + ReadableStream（前端 HTTP 层能带
// Authorization），终端必须用浏览器 WebSocket（带不了头），故走一次性 ticket。
// 这不是两套安全策略，而是同一条纪律（凭证不落 URL 长驻）在不同浏览器 API 下的形态。

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
// @Summary      容器日志流(NDJSON Follow)
// @Description  按指令号接入已建立的日志流会话；每行 {"seq","data"(base64),"eof"}；客户端断开即下发 cancel
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
	if rec.Action != agentproto.DockerActionContainerLogs {
		// 这条端点只承载日志流：PTY 走专用 WebSocket（两条通道的消息形状不同，
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
