package agenthub

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// Enroller 处理带 enroll_token 的首次注册（由 service.AgentIngestService 实现）。
type Enroller interface {
	Enroll(ctx context.Context, h *agentproto.Hello) (deviceID uint64, agentToken string, err error)
}

// Authenticator 处理带 agent_token 的重连鉴权。
type Authenticator interface {
	Authenticate(ctx context.Context, h *agentproto.Hello) (deviceID uint64, err error)
}

// Ingestor 把一条已校验的样本写进热层（Redis 原始窗 + 水位）。
type Ingestor interface {
	Ingest(ctx context.Context, deviceID uint64, s *agentproto.MetricsSample) error
}

// Toucher 刷新设备的 last_seen_at（online 由它推导，不落库）。
type Toucher interface {
	Touch(ctx context.Context, deviceID uint64) error
}

// Policy 是下发给 agent 的运行参数。
//
// 注意协议现状：hello_ack 只有 ReportInterval（秒），**没有**心跳间隔字段
// —— 所以 HeartbeatInterval 不上线，它在本包里的用途是校准服务端 ping 的节奏
// （见 pingInterval），并由 wireup 用来推导 hub 的 PingInterval/PongWait。
type Policy interface {
	ReportInterval() time.Duration
	HeartbeatInterval() time.Duration
}

// Deps 是单连接状态机的依赖束。
//
// 为什么打包成结构体而不是 6 个构造参数：这些依赖**全是接口**，调用方一旦把
// 两个同为「服务」的依赖写反，编译器救不了；具名字段至少让写反一眼可见。
type Deps struct {
	Enroller      Enroller
	Authenticator Authenticator
	Ingestor      Ingestor
	Toucher       Toucher
	Decider       Decider
	Policy        Policy
	// Limiter 是**入站帧级**限流器（窄接口见 ratelimit.go，实现由 wireup 注入）。
	//
	// 它是 Deps 里唯一「故障时放行」的依赖，这不是笔误：其余五个守的是**安全**
	// （谁能上报、上报归谁），故障时必须失败关闭；它守的是 **CPU / Redis 带宽**
	// 这类容量资源，故障时断连会把「Redis 抖一下」放大成全体 agent 的重连风暴
	// （一次重连要查库、要重新 enroll），代价高于挺过这段抖动。
	// 取向落在 allowFrame，且有断言钉住（nil 与报错两条路径各一条）。
	//
	// nil = 未装配：同样放行 + Warn（限流器缺失不得让整条通道停摆）。
	Limiter RateLimiter
}

// socket 是连接的入站/控制通路。生产态就是 `*websocket.Conn`（NewConn 收它），
// 测试态是内存替身 —— 这样读循环里的每一条守卫（方向校验、关闭码、读超时、
// 背压）都能在**不起真实 TCP** 的前提下被驱动：gorilla 不导出可注入的
// Conn 构造函数，只有 Upgrade 能拿到真连接，而真实升级路径由 handler 负责。
type socket interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	SetReadLimit(limit int64)
	SetReadDeadline(t time.Time) error
	SetPongHandler(h func(appData string) error)
	// RemoteAddr 是远端地址：**未鉴权阶段的限流键身份**只有它（见 ratelimit.go 的
	// FrameLimitKey）。放进窄接口而不是让连接持有具体 *websocket.Conn，是为了让
	// 「IP 不含端口」这条规则能在不起真实 TCP 的测试里被驱动。
	RemoteAddr() net.Addr
	Close() error
}

var (
	// errSendQueueFull 表示发送队列已满：消息被**丢弃并计数**（读循环绝不因此阻塞）。
	errSendQueueFull = errors.New("agenthub: send queue full")
	// errConnClosed 表示连接已关闭，消息不再入队。
	errConnClosed = errors.New("agenthub: conn closed")
	// errNilMessage 表示 SendMessage 收到 nil —— 一个笔误不该打崩读循环。
	errNilMessage = errors.New("agenthub: nil message")
)

const (
	// minReportIntervalSeconds 是契约层的下限：HelloAck.ReportInterval 非 0 时必须 ≥2。
	minReportIntervalSeconds = 2
	// defaultReportIntervalSeconds 取配置项 sys.agent.reportInterval 的种子值。
	// 依赖缺失时**不能**退回下限 2：那会让 agent 以 5 倍频率上报，把热层与
	// 数据库一起压上去。
	defaultReportIntervalSeconds = 10
)

// NewConn 创建一条**已接管真实 socket** 的连接。
//
// hub 是**消费方窄接口**（SelfUnregisterer）而不是具体 *Hub：连接对注册表只用
// 到两件事 —— hello 成功后 Register（顶替同设备的旧连接必须由注册表做，它才知道
// 谁是「旧」的），以及交出运行参数 opts()。把它写成具名接口后，handler 侧的注入
// 就不必依赖具体类型：同一个对象既能在这里被连接调用 Register，又能在 handler
// 里被显式 Unregister。*Hub 天然满足它（见 hub.go 的编译期断言）。
//
// 收尾（注销）不在这里做：CloseWith 刻意**不动注册表** —— 关闭幂等与
// 「已关闭但尚未注销的连接仍能被 DrainAll 通知到」是 Task 1 用断言钉住的语义。
// 注销由 handler 在 Serve 返回后按连接身份做。
func NewConn(hub SelfUnregisterer, ws *websocket.Conn, deps Deps, log logger.LoggerInterface) *Conn {
	if ws == nil {
		return newConn(hub, nil, deps, log)
	}
	return newConn(hub, realSocket{c: ws}, deps, log)
}

// realSocket 把生产态的 *websocket.Conn 适配到 socket 窄接口。
//
// 为什么要这层薄适配，而不是让 NewConn 的形参直接写成 *websocket.Conn：
// 构造入口一旦钉死具体类型，测试就再也无法在**不起真实 TCP** 的前提下驱动读循环
// —— 而那是 Task 2 全部守卫（方向校验、关闭码、读超时、背压）的落点。
// 这层适配把具体类型挡在构造入口之外，代价是 7 行转发。
type realSocket struct{ c *websocket.Conn }

func (s realSocket) ReadMessage() (int, []byte, error) { return s.c.ReadMessage() }

func (s realSocket) WriteMessage(mt int, data []byte) error {
	return s.c.WriteMessage(mt, data)
}

func (s realSocket) WriteControl(mt int, data []byte, deadline time.Time) error {
	return s.c.WriteControl(mt, data, deadline)
}

func (s realSocket) SetReadLimit(limit int64)            { s.c.SetReadLimit(limit) }
func (s realSocket) SetReadDeadline(t time.Time) error   { return s.c.SetReadDeadline(t) }
func (s realSocket) SetPongHandler(h func(string) error) { s.c.SetPongHandler(h) }
func (s realSocket) Close() error                        { return s.c.Close() }

// RemoteAddr 转发真实连接的远端地址；host 部分的提取在 remoteIPOf（构造时做一次）。
func (s realSocket) RemoteAddr() net.Addr { return s.c.RemoteAddr() }

// 编译期断言：适配器必须真的满足读循环要的窄接口 —— 漏一个方法在这里就红。
var _ socket = realSocket{}

// newConn 是 NewConn 的实现，并顺带接受测试态的 socket 替身。
func newConn(hub SelfUnregisterer, ws socket, deps Deps, log logger.LoggerInterface) *Conn {
	opts := Options{}.withDefaults()
	if hub != nil {
		// opts() 是 SelfUnregisterer 的第二个（未导出）方法：它让调用方无需知道
		// 具体类型也能拿到注册表的运行参数。nil 判定必须先做 —— nil 接口上调用
		// 任何方法都会 panic。
		opts = hub.opts()
	}
	c := &Conn{
		hub:   hub,
		opts:  opts,
		deps:  deps,
		log:   log,
		ws:    ws,
		send:  make(chan []byte, opts.SendQueue),
		done:  make(chan struct{}),
		state: StateAwaitHello,
	}
	if ws != nil {
		// 远端 IP 在这里算**一次**（而非每帧从 socket 取）：它只在未鉴权阶段的限流键上
		// 用到，而每帧解析一次地址串是白付的开销。host 部分不含端口 —— 理由见 remoteIPOf。
		c.remoteIP = remoteIPOf(ws.RemoteAddr())
		c.sendFn = func(b []byte) error {
			return ws.WriteMessage(websocket.TextMessage, b)
		}
		c.writeCloseFn = func(code int, reason string) error {
			return ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(code, reason), time.Now().Add(opts.WriteTimeout))
		}
	}
	return c
}

// ── 可观测状态 ─────────────────────────────────────────────

// DeviceID 返回连接绑定的设备 ID；尚未完成 hello 时为 0。
func (c *Conn) DeviceID() uint64 { return c.deviceID() }

// State 返回当前状态机状态。
func (c *Conn) State() ConnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// DroppedCount 返回因发送队列满而被丢弃的**数据帧**数。
// 控制帧（ping / 关闭）不走队列，因此永远不计入这里 —— 它们不会被丢弃。
func (c *Conn) DroppedCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// SkewCount 返回被「时钟偏移超限」拒绝的消息数（软校验：拒绝该条、不断连）。
func (c *Conn) SkewCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skew
}

// RateLimitedCount 返回被入站帧级限流拦下的消息数（超限即为 0 或 1：拦下即关闭，
// 读循环随即终止）。它是「限流真的触发了」的唯一服务端账目 —— 关闭码 4006 只能
// 证明这条连接被关，证明不了它是被限流关的（关闭帧的码对日志不可检索）。
func (c *Conn) RateLimitedCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rateLimited
}

func (c *Conn) setState(s ConnState) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

func (c *Conn) bindDevice(id uint64) {
	c.mu.Lock()
	c.devID = id
	c.mu.Unlock()
}

func (c *Conn) countSkew() {
	c.mu.Lock()
	c.skew++
	c.mu.Unlock()
}

// countRateLimited 记一次「因限流被拦下」。
func (c *Conn) countRateLimited() {
	c.mu.Lock()
	c.rateLimited++
	c.mu.Unlock()
}

// socket 返回入站通路；未接管真实 socket（测试态）时为 nil。
func (c *Conn) socket() socket { return c.ws }

// ── 运行期 ─────────────────────────────────────────────────

// Serve 跑这条连接：一个读循环（本协程）+ 一个写协程（串行化写 socket）。
//
// gorilla 的 `*websocket.Conn` 不允许并发 WriteMessage，所以数据帧**只能**由
// writeLoop 一个协程写；控制帧（ping / 关闭）走 WriteControl —— gorilla 明确
// 允许它与其它方法并发调用，这也正是「队列满时心跳/关闭帧仍能送达」的落点。
//
// Serve 返回前会关掉底层 socket；销注册表留给调用方（见 NewConn 的说明）。
func (c *Conn) Serve(ctx context.Context) {
	sock := c.socket()
	if sock == nil {
		// 未接管真实 socket（Task 1 的 newIdleConn/newBareConn）：没有入站通路，
		// 读循环无从开始 —— 短路而不是 panic。
		return
	}
	defer func() {
		// 兜底关闭底层 TCP：无论走哪条收尾路径都不留半开连接。
		// 关闭帧在此之前已经写入（TCP 保证在 FIN 之前送达）。
		if err := sock.Close(); err != nil {
			c.log.Debug("agent socket close failed", zap.Error(err))
		}
	}()

	go c.writeLoop()
	c.readLoop(ctx, sock)
	c.teardown("读循环结束")
}

// writeLoop 串行化所有出站数据帧，并按 pingInterval 发 keepalive。
func (c *Conn) writeLoop() {
	ticker := time.NewTicker(c.pingInterval())
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.sendPing()
		case b := <-c.send:
			if err := c.sendFn(b); err != nil {
				// 写失败说明链路已断：收尾并退出 —— 不再尝试下发关闭帧（写不出去）。
				c.log.Warn("agent write failed", zap.Uint64("device_id", c.deviceID()), zap.Error(err))
				c.teardown("写 socket 失败")
				return
			}
		}
	}
}

// sendPing 下发一个 WebSocket ping 控制帧。
//
// 为什么走 WriteControl 而不是发送队列：队列可能在接收端退避时被填满，而
// **丢心跳会被两端误判成链路已死**（我们判它超时、它判我们掉线），
// 所以控制帧必须有一条不经过队列的独立通路。同理见 finish（关闭帧）。
func (c *Conn) sendPing() {
	sock := c.socket()
	if sock == nil {
		return
	}
	if err := sock.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.opts.WriteTimeout)); err != nil {
		c.log.Warn("agent ping failed", zap.Uint64("device_id", c.deviceID()), zap.Error(err))
	}
}

// pingInterval 是服务端 ping 的节奏：以 opts.PingInterval 为上限，
// 且**不慢于** agent 的心跳节奏（Policy.HeartbeatInterval）。
//
// 为什么要看 agent 的心跳节奏：半开连接（NAT / 代理静默丢弃）只能靠 ping/pong
// 发现；若我们的 ping 比 agent 自己的心跳还慢，就会比 agent 更晚发现链路已死。
func (c *Conn) pingInterval() time.Duration {
	interval := c.opts.PingInterval
	if c.deps.Policy == nil {
		return interval
	}
	if hb := c.deps.Policy.HeartbeatInterval(); hb > 0 && hb < interval {
		return hb
	}
	return interval
}

// reportIntervalSeconds 把 Policy.ReportInterval 折算成 hello_ack 要的**秒**。
func (c *Conn) reportIntervalSeconds() int {
	if c.deps.Policy == nil {
		return defaultReportIntervalSeconds
	}
	secs := int(c.deps.Policy.ReportInterval() / time.Second)
	if secs < minReportIntervalSeconds {
		// 契约层拒绝 report_interval ∈ (0,2)，退回默认值而不是发一个非法 ack。
		return defaultReportIntervalSeconds
	}
	return secs
}

// SendMessage 把一条信封消息排进发送队列。
//
// 队列满时**丢弃并计数**（DroppedCount），绝不阻塞读循环：阻塞读循环会让 TCP
// 接收窗口关闭，进而把 agent 也拖死；而 agent 侧有本地待发队列，丢一帧趋势数据
// 不致命。控制帧（ping / 关闭帧）不走这条队列。
func (c *Conn) SendMessage(m *agentproto.Message) error {
	if m == nil {
		return errNilMessage
	}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	return c.enqueue(b)
}

func (c *Conn) enqueue(b []byte) error {
	if c.send == nil || c.sendFn == nil {
		// 未接管真实 socket（测试态）：短路，不排队、不计数、不 panic。
		return nil
	}
	select {
	case <-c.done:
		return errConnClosed
	default:
	}
	select {
	case c.send <- b:
		return nil
	default:
		c.mu.Lock()
		c.dropped++
		dropped := c.dropped
		c.mu.Unlock()
		c.log.Warn("agent send queue full, message dropped",
			zap.Uint64("device_id", c.deviceID()),
			zap.Int64("dropped_total", dropped))
		return errSendQueueFull
	}
}

// readLoop 是单连接的唯一入站循环。处理顺序**不可调换**（每一步都对应一个
// 精确的关闭码，调换就会让某两种情形塌缩成同一个码，agent 也就无从区分处理策略）：
//
//  1. SetReadLimit：单帧上限（没有它，一条巨帧就能打爆服务端内存）
//  2. Decode 信封：版本越界 → 4000；JSON 非法或信封校验失败 → 4002
//     （契约层的 Decode = Unmarshal + Message.Validate，所以「解码」与「信封校验」
//     是同一次调用；版本必须按 ErrUnsupportedVersion 哨兵单独摘出来，
//     否则旧版 agent 只会收到「报文损坏」，一直重试同一个版本）
//  3. 入站**帧级限流**：超限 → 4006（见 handleFrame 里该分支对位置的说明）
//  4. LookupType 未登记 → 4003
//  5. 方向不符（收到 core.* 或回环）→ 4003
//  6. hello 之前收到非 hello → 4008
//  7. 时钟偏移超限 → 拒绝该条并计数，**不断连**（**hello 除外**：握手帧的 TS
//     偏移无语义价值，而对首帧应用「拒绝但不断连」会让连接永久停在 StateAwaitHello，
//     详见 handleFrame 里该分支的说明）
//  8. 按类型分派（表见 agentHandlers）
//  9. 每条成功处理的消息后 Touch（刷新 last_seen_at）
//
// 为什么第 6 步（hello 之前）排在方向校验之后：一条 core.hello_ack 作为首帧
// 同时满足「非 hello」与「方向错误」，两者必须归到**方向**这个码上
// （它是伪造/回环信号，客户端据此应加载新协议而不是先去发 hello 再等超时）；
// 只有「方向正确、但 hello 之前发」的帧才归到 4008。
func (c *Conn) readLoop(ctx context.Context, sock socket) {
	sock.SetReadLimit(c.opts.MaxMessageBytes)
	if err := c.refreshReadDeadline(sock); err != nil {
		c.log.Warn("set read deadline failed", zap.Uint64("device_id", c.deviceID()), zap.Error(err))
	}
	// pong 同样算「活跃」信号：不刷新读期限的话，ping/pong keepalive 形同虚设。
	sock.SetPongHandler(func(string) error { return c.refreshReadDeadline(sock) })

	for {
		_, raw, err := sock.ReadMessage()
		if err != nil {
			c.handleReadError(err)
			return
		}
		// 任何入站消息都刷新读超时窗口（不只是心跳）：上报类消息同样证明设备活着。
		if derr := c.refreshReadDeadline(sock); derr != nil {
			c.log.Warn("set read deadline failed", zap.Uint64("device_id", c.deviceID()), zap.Error(derr))
		}
		if !c.handleFrame(ctx, raw) {
			return
		}
	}
}

// refreshReadDeadline 把读期限推到 now+PongWait。
//
// 为什么「判状态」与「设期限」必须在同一个临界区里：收尾（finish）会把读期限
// 压到当下以解开阻塞的读，而读循环在**每条**消息后、以及启动时都会刷新期限 ——
// 两者交错时，若刷新落在「置 Closed 之后、压期限之前」，它会把这个「解阻塞期限」
// 推回未来，于是读循环要一直挂到 PongWait（默认 90s）才结束，
// 关闭后的 Serve 也就迟迟不返回（停机排空被拖住）。
// 放进同一把锁后，置 Closed 之后不可能再有一次刷新把期限推回去。
func (c *Conn) refreshReadDeadline(sock socket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateClosed {
		// 已收尾：finish 压下的「当下期限」必须保持原样（它正等着把读解开）。
		return nil
	}
	return sock.SetReadDeadline(time.Now().Add(c.opts.PongWait))
}

// handleReadError 判别读循环的退出原因，并只在**真超时**时下发 4005。
func (c *Conn) handleReadError(err error) {
	if c.State() == StateClosed {
		// 我们自己关了这条连接：finish 会把读期限压到当下以解开阻塞的读，
		// 于是这里必然收到一个超时错误 —— 那是主动关闭的收尾，不是心跳超时。
		return
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		c.CloseWith(agentproto.CloseHeartbeatTimeout, "超过 PongWait 未收到任何消息")
		return
	}
	// 对端主动断开 / EOF / 底层协议错误：连接已经没了，无需（也无法）再下发关闭帧。
	c.log.Info("agent connection read ended", zap.Uint64("device_id", c.deviceID()), zap.Error(err))
}

// handleFrame 处理一条入站帧；返回 false 表示连接已关闭，读循环必须退出。
func (c *Conn) handleFrame(ctx context.Context, raw []byte) bool {
	m, err := agentproto.Decode(raw)
	if err != nil {
		if errors.Is(err, agentproto.ErrUnsupportedVersion) {
			// 版本越界必须先于「报文损坏」被摘出来：它要的是「升级客户端」，不是「修 JSON」。
			c.CloseWith(agentproto.CloseVersionMismatch, "信封版本不在受支持区间")
			return false
		}
		c.CloseWith(agentproto.CloseMalformedMessage, "信封 JSON 非法或校验失败")
		return false
	}

	// 步骤 3：入站帧级限流。位置**不可**再往后挪，理由有两条：
	//
	//  1. 「分派之前」是硬要求：限流若落在分派之后（或落在任何写路径里），超限的帧
	//     照样会入湖 / 刷 last_seen_at —— 那正是要防的「CPU 与 Redis 带宽被吃满」。
	//  2. 为什么选「解码一成功就判」而不是「过了方向/状态守卫再判」：**解码本身**
	//     就是最主要的 CPU 开销，而方向错误/未登记类型/hello 之前这几类帧同样会
	//     被刷（刷它们不需要任何凭据）。若把限流排在这些守卫之后，一个攻击者只要
	//     一直发「会被软拒绝的帧」（例如时钟偏移超限的帧 —— 那条路径是「拒绝但不断连」）
	//     就能无限量消耗解码 CPU 而永远不被计数。排在守卫之前，代价只是「已在超限
	//     连接上的协议错误帧会报 4006 而不是 4002/4003/4008」—— 那些守卫本来就会
	//     立即关闭连接，两种码都是「你的连接到此为止」，而 4006 更准确地描述了
	//     此刻的事实（这条连接正在刷帧）。
	if !c.allowFrame(ctx) {
		c.countRateLimited()
		c.log.Warn("agent inbound frame rate limited, closing",
			zap.Uint64("device_id", c.deviceID()),
			zap.String("remote_ip", c.remoteIP),
			zap.Int("close_code", agentproto.CloseRateLimited))
		// reason 传空串：线上 reason 一律由 finish 取 agentproto.CloseReason(code)
		// （协议规范话术，见 types.go 的说明），触发细节已由上面那条 Warn 带出。
		c.CloseWith(agentproto.CloseRateLimited, "")
		return false
	}

	if _, ok := agentproto.LookupType(m.Type); !ok {
		c.CloseWith(agentproto.CloseUnsupportedType, "未登记的消息类型")
		return false
	}
	if agentproto.DirectionOf(m.Type) != agentproto.DirAgentToCore {
		// 协议注释：收到 core.* 即方向错误（回环或伪造），用 CloseUnsupportedType。
		c.CloseWith(agentproto.CloseUnsupportedType, "方向错误：core→agent 的消息出现在 agent 通道上")
		return false
	}
	if c.State() == StateAwaitHello && m.Type != agentproto.TypeAgentHello {
		// 状态机只能前进：hello 之前的一切（含上报）都在这里被拦，agent 必须先握手。
		c.CloseWith(agentproto.CloseProtocolViolation, "hello 之前收到非 hello 消息")
		return false
	}

	now := time.Now().UnixMilli()
	if m.Type == agentproto.TypeAgentHello {
		// hello **不做**时钟偏移软校验（只记日志）。理由（二选一里选了「不应用」这条）：
		//
		//  1. 语义：hello 是**握手帧**，它的 TS 偏移没有任何语义价值 —— 它不携带
		//     时间序列数据，不会落进任何一个指标桶；要防的「时间戳错位污染分桶」
		//     这件事在 hello 上根本不存在。而它的载荷 `HelloAck.ServerTime` 本身就是
		//     用来让 agent 自行校准时钟的（§5.5），先把 ack 发出去才是正确的修复路径。
		//  2. 状态机：软校验的分支是「拒绝该条但**不断连**」。对首帧 hello 应用它，
		//     连接会停在 StateAwaitHello（既没握手成功、也没关闭），一直挂到 PongWait
		//     （默认 90s）才以**心跳超时**（4005）关闭 —— 一台时钟偏移超限的设备因此
		//     永远连不上，而症状是一个与真实原因无关的关闭码（排障会被带到「网络/
		//     心跳」方向上去）。
		//  3. 若改成「超限就明确关闭」，同样不可接受：关连接不会让 agent 的时钟变准，
		//     只会把它永久挡在门外（连 hello_ack 里的 ServerTime 都拿不到，无从校准），
		//     且既有的关闭码里没有一个语义等于「时钟偏移」（4008 是协议违规、
		//     4002 是报文损坏，借用它们会把两种完全不同的故障塌缩成同一个码）。
		//
		// 偏移仍然**可见**：metrics/heartbeat 的软校验照旧（拒绝+计数+日志），
		// 这里额外记一条 Warn 带上实际偏移，让「这台设备时钟不准」不必靠猜。
		if agentproto.ExceedsClockSkew(m.TS, now) {
			c.log.Warn("agent hello clock skew（不拒绝：握手帧的 TS 偏移无语义价值，且 hello_ack.ServerTime 会让 agent 自行校准）",
				zap.Int64("ts", m.TS),
				zap.Int64("now", now),
				zap.Int64("skew_ms", m.TS-now),
				zap.Int64("max_skew_ms", agentproto.MaxClockSkewMs))
		}
	} else if agentproto.ExceedsClockSkew(m.TS, now) {
		// 软校验：拒绝**这一条**并计数，不断连 —— 关连接不会让 agent 的时钟变准，
		// 只会让整机指标长期全丢。契约层同样要求「不得篡改 ts」。
		c.countSkew()
		c.log.Warn("agent message rejected: clock skew",
			zap.Uint64("device_id", c.deviceID()),
			zap.String("type", m.Type),
			zap.Int64("ts", m.TS),
			zap.Int64("now", now),
			zap.Int64("max_skew_ms", agentproto.MaxClockSkewMs))
		return true
	}

	handle, ok := agentHandlers[m.Type]
	if !ok {
		// 表里缺项只可能是协议新增了 agent→core 类型而没接线（守卫测试会先红）。
		// 关连接而不是静默忽略：让不匹配立刻在 agent 侧可见。
		c.CloseWith(agentproto.CloseUnsupportedType, "已登记但状态机未接线的类型")
		return false
	}
	if !handle(c, ctx, m) {
		return false
	}

	// 步骤 9：每条成功处理的消息后刷新 last_seen_at（而不是只在心跳时）——
	// online 由 last_seen_at 推导（不落库），只在心跳时刷新会把「只上报不心跳」
	// 的实现误判成离线。
	c.touch(ctx)
	return true
}

// agentHandlers 是「已登记且 agent→core 的类型 → 处理函数」的分派表。
//
// 为什么用表而不是 switch：协议新增一个 agent→core 类型时表里会缺项，
// TestConnDispatchCoversAllAgentToCoreTypes 立刻变红；switch 漏分支则只会静默忽略。
var agentHandlers = map[string]func(*Conn, context.Context, *agentproto.Message) bool{
	agentproto.TypeAgentHello:         (*Conn).handleHello,
	agentproto.TypeAgentHeartbeat:     (*Conn).handleHeartbeat,
	agentproto.TypeAgentReportMetrics: (*Conn).handleMetrics,
}

// allowFrame 询问限流器是否放行这一帧。
//
// **fail-open 定死**：Limiter 未装配（nil）或 Allow 返回错误 → **放行** + Warn。
// 理由：限流器守的是 CPU / Redis 带宽这类容量资源，而它自己依赖 Redis ——
// Redis 抖动时若把它当权威，整个 agent 通道会跟着 Redis 一起停摆（全体 agent 收到
// 关闭帧后重连，重连风暴本身要查库、要重新 enroll，比挺过抖动贵得多）。
// 这与 accept()（设备启停）的 fail-closed 并不矛盾：那条守卫管的是**安全**。
//
// 两个身份都如实上报（deviceID + remoteIP），由实现按 deviceID != 0 选键 ——
// 于是**未鉴权阶段同样受限**（hello 之前键是 agent:ws:preauth:{IP}），
// 攻击者无法靠「不发 hello」把握手前的 CPU 刷满。
func (c *Conn) allowFrame(ctx context.Context) bool {
	if c.deps.Limiter == nil {
		c.warnLimiterUnavailable("Limiter 未装配", nil)
		return true
	}
	ok, err := c.deps.Limiter.Allow(ctx, c.deviceID(), c.remoteIP)
	if err != nil {
		c.warnLimiterUnavailable("Allow 返回错误", err)
		return true
	}
	return ok
}

// warnLimiterUnavailable 记一条「限流器不可用，已 fail-open」的 Warn。
//
// 每条连接**只记一次**：限流器故障（Redis 抖动）会持续到恢复为止，而帧速率是
// 每分钟数百量级 —— 按帧记 Warn 会把故障期的日志刷成噪声，反而盖住真正的原因。
func (c *Conn) warnLimiterUnavailable(reason string, err error) {
	c.limiterWarnOnce.Do(func() {
		c.log.Warn("agenthub: 限流器不可用，入站帧限流停用（fail-open：全部放行）",
			zap.Uint64("device_id", c.deviceID()),
			zap.String("remote_ip", c.remoteIP),
			zap.String("reason", reason),
			zap.Error(err))
	})
}

// handleHello 完成 enroll 或重连鉴权，并回 core.hello_ack。
func (c *Conn) handleHello(ctx context.Context, m *agentproto.Message) bool {
	// 状态机只能前进：hello 只允许在 StateAwaitHello 时到达。放行的话，
	// 一条运行中的连接能把自己换成另一台设备 —— 注册表里的 deviceID 与
	// 已经入湖的样本归属会一起错乱。
	if c.State() != StateAwaitHello {
		c.CloseWith(agentproto.CloseProtocolViolation, "hello 只允许在握手阶段发送")
		return false
	}

	h := &agentproto.Hello{}
	if err := m.DecodeData(h); err != nil {
		c.CloseWith(agentproto.CloseMalformedMessage, "hello 载荷无法解码")
		return false
	}

	// 凭据判定**先于**载荷校验：两个 token 都缺失/同时存在是「凭据问题」（4001），
	// 与「报文损坏」（4002）是两回事 —— agent 收到 4001 才会去补/换 token，
	// 收到 4002 只会去查 JSON 序列化。
	// 也正因为这个顺序，这里用 DecodeData + 显式 Validate，而不是 DecodeTypedFor：
	// 后者会把 Hello.Validate 里的凭据错误一并折成载荷错误，凭据永远归不到 4001。
	kind, credErr := h.Credential()
	if credErr != nil {
		c.CloseWith(agentproto.CloseUnauthorized, "hello 凭据缺失或歧义")
		return false
	}
	if err := h.Validate(); err != nil {
		c.CloseWith(agentproto.CloseMalformedMessage, "hello 载荷校验失败")
		return false
	}

	c.setState(StateEnrolling)

	var (
		deviceID   uint64
		agentToken string
	)
	switch kind {
	case agentproto.CredentialEnroll:
		if c.deps.Enroller == nil {
			c.log.Error("agenthub: Enroller 未装配，无法处理 enroll", zap.String("instance_id", h.InstanceID))
			c.CloseWith(agentproto.CloseUnauthorized, "enroller 未装配")
			return false
		}
		id, token, err := c.deps.Enroller.Enroll(ctx, h)
		if err != nil {
			c.log.Warn("agent enroll failed", zap.String("instance_id", h.InstanceID), zap.Error(err))
			c.CloseWith(agentproto.CloseUnauthorized, "enroll 失败")
			return false
		}
		deviceID, agentToken = id, token
	case agentproto.CredentialAgent:
		if c.deps.Authenticator == nil {
			c.log.Error("agenthub: Authenticator 未装配，无法处理重连鉴权", zap.String("instance_id", h.InstanceID))
			c.CloseWith(agentproto.CloseUnauthorized, "authenticator 未装配")
			return false
		}
		id, err := c.deps.Authenticator.Authenticate(ctx, h)
		if err != nil {
			c.log.Warn("agent authenticate failed", zap.String("instance_id", h.InstanceID), zap.Error(err))
			c.CloseWith(agentproto.CloseUnauthorized, "鉴权失败")
			return false
		}
		deviceID = id
	}

	if deviceID == 0 {
		// 鉴权「成功」却拿到 0 号设备：雪花 ID 不可能为 0（Credential() 也已经
		// 排除了「两种 token 都没有」）。放任下去会把连接注册成 0 号设备，
		// 注册表会因此串号 —— 所以这里按失败关闭处理。
		c.log.Error("agenthub: 鉴权返回了 0 号设备", zap.String("instance_id", h.InstanceID))
		c.CloseWith(agentproto.CloseUnauthorized, "鉴权返回 0 号设备")
		return false
	}

	// 先鉴权后查启停，且两者用**同一个码**（4001）：否则关闭码会泄露
	// 「该设备是否存在」——被停用/删除的设备与 token 非法的设备必须无从区分。
	if !c.accept(ctx, deviceID) {
		c.CloseWith(agentproto.CloseUnauthorized, "设备不存在/已停用或状态查询失败")
		return false
	}

	c.bindDevice(deviceID)
	if c.hub != nil {
		// 登记进注册表：同设备已有连接时由 hub 用 CloseDuplicateInstance 顶掉旧的。
		if err := c.hub.Register(c); err != nil {
			c.log.Warn("agent hub register failed", zap.Uint64("device_id", deviceID), zap.Error(err))
		}
	}

	ack := &agentproto.HelloAck{
		Accepted:       true,
		DeviceID:       strconv.FormatUint(deviceID, 10),
		ReportInterval: c.reportIntervalSeconds(),
		ServerTime:     time.Now().UnixMilli(),
		V:              agentproto.CurrentVersion,
	}
	if kind == agentproto.CredentialEnroll {
		// agent_token 的明文**只此一次**：DB 里只存 sha256。重连分支绝不再下发。
		ack.AgentToken = agentToken
	}
	msg, err := agentproto.NewMessage(m.ID, agentproto.TypeCoreHelloAck, ack)
	if err != nil {
		c.log.Error("agenthub: 构造 hello_ack 失败", zap.Uint64("device_id", deviceID), zap.Error(err))
		c.CloseWith(agentproto.CloseServerShutdown, "服务端构造应答失败")
		return false
	}
	if err := c.SendMessage(msg); err != nil {
		// 应答被背压丢弃：记日志但不关连接 —— 队列满说明链路已在退避，
		// agent 收不到 hello_ack 会自己超时重连，比我们在这里僵持更干净。
		c.log.Warn("agent hello_ack dropped by backpressure",
			zap.Uint64("device_id", deviceID), zap.Error(err))
	}

	c.setState(StateActive)
	c.log.Info("agent connection active",
		zap.Uint64("device_id", deviceID),
		zap.String("instance_id", h.InstanceID),
		zap.String("hostname", h.Hostname),
		zap.String("credential", credentialLabel(kind)))
	return true
}

// accept 询问 Decider：设备是否处于可接受上报的状态（启用态）。
//
// 「不接受」与「查询失败」都返回 false：把 DB 故障当成放行，会让停用设备照样
// 上报（fail-open 是安全侧错误）。两种情形的关闭码由调用方统一取 4001。
func (c *Conn) accept(ctx context.Context, deviceID uint64) bool {
	if c.deps.Decider == nil {
		c.log.Error("agenthub: Decider 未装配，无法确认设备状态", zap.Uint64("device_id", deviceID))
		return false
	}
	ok, err := c.deps.Decider.IsAccepting(ctx, deviceID)
	if err != nil {
		c.log.Error("agent device state check failed", zap.Uint64("device_id", deviceID), zap.Error(err))
		return false
	}
	if !ok {
		c.log.Warn("agent device not accepting reports", zap.Uint64("device_id", deviceID))
	}
	return ok
}

// handleMetrics 解码、校验并写入一条指标样本。
func (c *Conn) handleMetrics(ctx context.Context, m *agentproto.Message) bool {
	// DecodeTypedFor 一次做完「方向复核 + 解码 + 载荷 Validate」：
	// 方向在 handleFrame 已经拦过，这里复核是因为协议把「回环/伪造」的判定权
	// 也交给了它（ErrWrongDirection），而载荷语义必须在这里就拒 ——
	// 越界值（如 cpu_used_percent=-1）静默落进热层会污染后续全部聚合。
	decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirAgentToCore)
	if err != nil {
		c.CloseWith(agentproto.CloseMalformedMessage, "指标载荷无法解码或语义非法")
		return false
	}
	sample, ok := decoded.(*agentproto.MetricsSample)
	if !ok {
		c.CloseWith(agentproto.CloseMalformedMessage, "指标载荷类型不符")
		return false
	}
	if c.deps.Ingestor == nil {
		c.log.Error("agenthub: Ingestor 未装配，样本被丢弃", zap.Uint64("device_id", c.deviceID()))
		return true
	}
	if err := c.deps.Ingestor.Ingest(ctx, c.deviceID(), sample); err != nil {
		// 入湖失败**不关连接**：热层故障是服务端问题，关连接只会让整机指标
		// 在故障期间全丢；agent 侧的重试与本地队列会兜住。
		c.log.Warn("agent ingest failed",
			zap.Uint64("device_id", c.deviceID()),
			zap.Int64("sample_ts", sample.T),
			zap.Error(err))
	}
	return true
}

// handleHeartbeat 处理 agent 心跳。
//
// 心跳不携带数据，它的唯一语义是「这台设备还活着」；而「活着」的记账
// （Toucher.Touch → last_seen_at）由 readLoop 在**每条**成功处理的消息后统一做，
// 所以这里不再重复 Touch（重复调用只会白打一次 DB）。
func (c *Conn) handleHeartbeat(_ context.Context, m *agentproto.Message) bool {
	c.log.Debug("agent heartbeat", zap.Uint64("device_id", c.deviceID()), zap.Int64("ts", m.TS))
	return true
}

// touch 刷新设备的 last_seen_at。
//
// 失败只记日志、不关连接：last_seen_at 是能由后续消息自然修正的推断量
// （online 由它推导），为它断连得不偿失。
func (c *Conn) touch(ctx context.Context) {
	deviceID := c.deviceID()
	if deviceID == 0 {
		return
	}
	if c.deps.Toucher == nil {
		c.log.Error("agenthub: Toucher 未装配，last_seen_at 无法刷新",
			zap.Uint64("device_id", deviceID))
		return
	}
	if err := c.deps.Toucher.Touch(ctx, deviceID); err != nil {
		c.log.Warn("agent touch failed", zap.Uint64("device_id", deviceID), zap.Error(err))
	}
}

// ── 收尾 ──────────────────────────────────────────────────

// finish 是唯一的收尾实现（由 once 守，故天然幂等）：置 StateClosed、关 done
// （让写协程与 ping ticker 退出）、解开阻塞的读循环，并按需下发关闭帧。
//
// 关闭帧走 writeCloseFn（生产态 = websocket.WriteControl）而**不经过发送队列**：
// 队列可能已满，而关闭帧绝不能丢 —— 丢它会让对端一直挂着等关闭。
func (c *Conn) finish(code int, reason string, writeCloseFrame bool) {
	// 置 Closed 与「把读期限压到当下」必须在同一临界区里完成（与
	// refreshReadDeadline 争同一把锁）：读循环在启动时与每条消息后都会刷新读期限，
	// 若那一次刷新插在两者之间，它会把解阻塞期限推回未来，读循环就要挂到
	// PongWait（默认 90s）才结束 —— 关闭后排空会被拖住。
	c.mu.Lock()
	c.state = StateClosed
	if c.ws != nil {
		// 压到当下：阻塞在 ReadMessage 上的读循环会立刻以超时返回。
		if err := c.ws.SetReadDeadline(time.Now()); err != nil {
			c.log.Warn("set read deadline on close failed", zap.Error(err))
		}
	}
	c.mu.Unlock()

	if c.done != nil {
		close(c.done)
	}

	if !writeCloseFrame {
		// 对端已经断开（或写 socket 失败）：写也没人收，且不该用自造的码
		// 覆盖对端真实的关闭码。
		c.log.Info("agent connection ended",
			zap.Uint64("device_id", c.deviceID()),
			zap.String("detail", reason))
		return
	}

	if reason != "" {
		c.log.Info("closing agent connection",
			zap.Uint64("device_id", c.deviceID()),
			zap.Int("close_code", code),
			zap.String("detail", reason))
	}

	fn := c.writeCloseFn
	if fn == nil {
		// 测试态：未接管真实 socket —— 短路，不写、不 panic。
		return
	}
	if err := fn(code, agentproto.CloseReason(code)); err != nil {
		c.log.Warn("write close frame failed",
			zap.Uint64("device_id", c.deviceID()),
			zap.Int("close_code", code),
			zap.Error(err))
	}
}

// teardown 结束运行期但**不下发关闭帧**：用于「对端已断开 / 写 socket 失败 /
// 读循环自然结束」这些连接已经不在的路径。
func (c *Conn) teardown(reason string) {
	c.once.Do(func() { c.finish(0, reason, false) })
}

// credentialLabel 是 hello 凭据种类的日志标签（不进线上报文）。
func credentialLabel(k agentproto.CredentialKind) string {
	switch k {
	case agentproto.CredentialEnroll:
		return "enroll"
	case agentproto.CredentialAgent:
		return "agent_token"
	default:
		return "none"
	}
}
