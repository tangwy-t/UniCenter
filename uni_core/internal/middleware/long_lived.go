package middleware

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// LongLived 是**长活响应**端点（>30s 的流式 NDJSON、大文件下载、慢采集）的
// 写/读截止接管中间件。
//
// 为什么需要它（P0：流式响应被服务端 WriteTimeout(30s) 掐断）：
// http.Server 的 WriteTimeout 是**绝对**截止 —— net/http 在请求头解析完的那一刻
// 把它设成 now+WriteTimeout（见 net/http/server.go 的 readRequest），此后整条响应
// 无论中间写没写、写了多少，都在这一个绝对窗口里；写 deadline 一到，下一次写就
// 失败，net/http 随即关连接。对「一次请求一次响应」的普通端点这是正合适的保护；
// 对长活响应却是误杀：拉取进度流 / 日志 Follow / stats 曲线 / 事件聚合 / 大文件
// 下载在 30s 那一刻被写失败掐断。本波「断流 = best-effort cancel」语义把它进一步
// 升级成「取消操作本身」—— QA 终审实测：>30s 的镜像拉取必然在 30.0s（±0.3s）
// 断路，任务行记「失败·拉取已取消」，镜像不落地。
//
// 本中间件把「整条响应一个绝对截止」换成「每次写一个滚动截止」，让写 deadline
// 回到它本来的语义 —— **一次写最长阻塞多久**（对端 TCP 收窗关死/网线被拔时写不会
// 永久挂住），而不是「整条响应必须在 30s 内写完」：
//
//   - 进入时清掉绝对写截止（SetWriteDeadline(time.Time{})）：空闲多久都不再被计时。
//     长活流的**空闲回收仍由既有机制负责**（dockerstream 的 10min idle sweep、
//     dockerevents 的引用计数、各会话自己的收尾），写 deadline 不再兼职做空闲
//     回收 —— 两个机制分工不重叠，谁也不替谁兜底。
//   - 用 longLivedWriter 包住 c.Writer：每次 Write/WriteString/Flush 之前把截止
//     推到 now+grace。数据照常流动的流不会被掐；对端卡死时最迟 grace 后写失败。
//   - 同理清掉整请求读截止（ReadTimeout 也是从首字节起的绝对窗口）：长活请求体
//     （构建上下文上传，上限 512MB）不该按「首字节起 30s 内必须传完」计；请求体
//     的读用 longLivedBody 按「每次读」续期 —— 上传人卡死同样是 grace 后被收住。
//   - c.Next() 返回后给收尾留一个 grace 窗口：HTTP/1.1 chunked 的终结符
//     （"0\r\n\r\n"）是 net/http 在 handler 返回之后才写的；若沿用「最后一帧之后
//     可能已经过期」的截止，客户端会收到被撕开的响应而不是干净的 EOF。
//
// grace 取配置的 server.writeTimeout —— 同一把尺：它本来就是「单次写最长阻塞时间」
// 的口径，长活端点只是不再把它当「整条响应预算」用。grace <= 0（写截止被显式关掉）
// 时只清除、不续期，等价于该路由无写截止。
//
// 挂载点在**路由注册处**（`docker.GET("/events", longLived, ...)`），不按 handler
// 逐个打补丁：路由表是「哪些端点长活」的唯一事实源，新端点漏挂会被
// router 包的 TestLongLivedRoutesCarryDeadlineMiddleware 源码级守卫拦住。
//
// 不管的端点：终端 /ws /agent/ws 三条 WebSocket 都在升级时 Hijack 连接，而
// gorilla 的 Upgrade 在 hijack 后立刻 netConn.SetDeadline(time.Time{})
// （见 gorilla/websocket server.go：「Clear deadlines set by HTTP server」）——
// 它们的 deadline 从一开始就归零，天然不受 WriteTimeout 影响。
func LongLived(log logger.LoggerInterface, grace time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 只有连接级的原始 writer 能设 deadline；中间件栈上可能套着自己的包装
		// （gin 的 responseWriter、操作日志的 bodyCaptureWriter），先穿透到底。
		raw := rawResponseWriter(c.Writer)
		rc := http.NewResponseController(raw)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			// 拿不到底层连接 = 接管不了：保持原行为（宁可仍被 30s 截断，
			// 也不要静默地什么都没做却假装接管了）。留一条告警便于归因。
			if log != nil {
				log.Warn("long-lived: 写截止接管失败，该响应仍按 WriteTimeout 截断",
					zap.String("path", c.Request.URL.Path), zap.Error(err))
			}
			c.Next()
			return
		}
		// 读侧：清掉整请求绝对截止；有请求体的长活请求按「每次读」续期。
		if err := rc.SetReadDeadline(time.Time{}); err == nil &&
			c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = &longLivedBody{ReadCloser: c.Request.Body, rc: rc, grace: grace}
		}
		c.Writer = &longLivedWriter{ResponseWriter: c.Writer, rc: rc, grace: grace}

		c.Next()

		// 收尾写（chunked 终结符 / 末次 flush）在 handler 返回后才发生：
		// 给它一个完整的 grace 窗口，保证客户端拿到的是干净收尾而不是撕开的连接。
		if grace > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(grace))
		}
	}
}

// rawResponseWriter 穿透中间件叠起来的 writer 包装，拿到 net/http 的原始
// http.ResponseWriter（只有它能设连接级 deadline）。
//
// 为什么不能用 http.NewResponseController(c.Writer) 一步到位：它的 Unwrap 链
// 只认 Unwrap() http.ResponseWriter，而 gin 的 ResponseWriter **接口**里没有
// Unwrap（只有具体类型 responseWriter 有），所以任何「内嵌 gin.ResponseWriter
// 接口」的包装（如操作日志的 bodyCaptureWriter）都会把链断在那里 —— 断链的
// 后果是 SetWriteDeadline 返回 ErrNotSupported，中间件静默失效。
func rawResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return w
		}
		w = next
	}
}

// longLivedWriter 每次写/刷前把写截止推到 now+grace：空闲不设截止、写有界。
//
// 内嵌 gin.ResponseWriter（而不是手写全部方法）：中间件只改「写之前续期」这一件
// 事，状态码/字节数/Header 等语义原样透传。
type longLivedWriter struct {
	gin.ResponseWriter
	rc    *http.ResponseController
	grace time.Duration
}

func (w *longLivedWriter) armWriteDeadline() {
	if w.grace > 0 {
		_ = w.rc.SetWriteDeadline(time.Now().Add(w.grace))
	}
}

func (w *longLivedWriter) Write(p []byte) (int, error) {
	w.armWriteDeadline()
	return w.ResponseWriter.Write(p)
}

func (w *longLivedWriter) WriteString(s string) (int, error) {
	w.armWriteDeadline()
	return w.ResponseWriter.WriteString(s)
}

func (w *longLivedWriter) Flush() {
	w.armWriteDeadline()
	w.ResponseWriter.Flush()
}

// Unwrap 让本包装对 http.ResponseController 透明：后续若再有包装/助手要设
// deadline，链不会断在这里（与 rawResponseWriter 是同一份约定的两端）。
func (w *longLivedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// longLivedBody 是长活请求体（构建上下文上传）的读包装：每次读前把读截止推到
// now+grace —— 语义与写侧对称（传输有界、等待不设截止）。
type longLivedBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	grace time.Duration
}

func (b *longLivedBody) Read(p []byte) (int, error) {
	if b.grace > 0 {
		_ = b.rc.SetReadDeadline(time.Now().Add(b.grace))
	}
	return b.ReadCloser.Read(p)
}
