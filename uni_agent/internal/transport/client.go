// Package transport 实现 uni_agent 与 uni_core 之间的 WebSocket 长连接：
// 注册/鉴权（hello → hello_ack）、周期上报、心跳、断线指数退避重连。
//
// 状态机（spec §4.1）：
//
//	CONNECTING → AWAIT_ACK → ACTIVE
//
// 在收到 core.hello_ack 之前**只能**发 agent.hello；发别的会被 core 以
// CloseProtocolViolation 断开。故写操作全部经 Client.send 收口，
// 由它检查状态 —— 不要在别处直接 ws.WriteMessage。
package transport

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 状态常量。
const (
	stateConnecting = iota
	stateAwaitAck
	stateActive
)

// Config 是连接的静态配置。
type Config struct {
	// URL 形如 ws://host:port/api/v1/agent/ws。
	URL string
	// EnrollToken 首次注册用；成功后 core 返回 AgentToken，此后只用 AgentToken。
	// 两者都带上是可以的：core 会优先用 agent_token 鉴权（spec §4.2）。
	EnrollToken string
	// AgentToken 是上次 enroll 拿到的凭据，落盘后重启可直接鉴权。
	AgentToken string
	// InstanceID 是设备指纹，必须跨重启稳定 —— core 用它做幂等与顶号判定。
	InstanceID string
	// HeartbeatInterval 是应用层心跳周期（WS ping/pong 之外兜底）。
	HeartbeatInterval time.Duration
	// Logger 可为空。
	Logger Logger
	// Hook 是升级运行时与连接生命周期的接点（可为 nil —— agent 不装配升级能力时
	// 一切照旧，新协议消息只是被忽略）。
	Hook UpgradeHook
}

// UpgradeHook 是升级运行时需要的三个连接侧事件。
//
// 为什么用「事件」而不是让运行时自己去看连接：连接状态的所有权在本包（状态机、
// 退避、读循环都在这儿），让运行时另开一条路去探测会变成两份实现。
type UpgradeHook interface {
	// OnDirective 收到一次升级指令（hello_ack 内嵌或 core.agent.upgrade 推送）。
	// 实现必须是**非阻塞**的：它在读循环里被调用，阻塞会拖住整条连接的读。
	OnDirective(d *agentproto.UpgradeDirective)
	// OnConnected 握手完成（hello_ack 被接受）。升级事务靠它确认「新版本确实连上了」。
	OnConnected()
	// OnConnectFailed 一次连接尝试失败（拨号失败、握手失败或握手期间断开）。
	// 用于试用期满后的探测计数（D3：先探测几次再回滚，避免 core 停机被误判）。
	OnConnectFailed()
}

// Logger 是本包依赖的最小日志接口（避免把 zap 拖进 agent）。
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Debug(msg string, kv ...any)
}

// Client 是一条到 core 的长连接客户端。
//
// 它自己负责重连，调用方只需在 Run 期间通过 Send 投递样本。
type Client struct {
	cfg Config
	log Logger

	mu    sync.Mutex
	state int
	conn  *websocket.Conn
	// agentToken 在 enroll 成功后由 hello_ack 写入，之后重连都用它。
	agentToken string
	// reportInterval 由 core 在 hello_ack 里下发（秒），0 表示用本地默认。
	reportInterval time.Duration

	sendCh chan *agentproto.MetricsSample
	// controlCh 投递**非指标**消息（升级状态上报）。
	//
	// 与样本队列分开：样本队列的目的是「断线补发最近 5 分钟」，而状态上报只关心
	// 「现在」—— 排在 30 个陈旧样本后面毫无意义（那时阶段早已跃迁）。分开之后
	// 它还能被写循环的 select 立即取走，不必等下一次上报 tick。
	controlCh chan *agentproto.Message
	// dropCount 统计因积压被丢弃的样本数（spec §4.3：drop-oldest 保最新）。
	dropCount uint64

	// hello 是连接建立后发送的注册载荷，由 SetHello 注入。
	// 每帧字段随设备重启才变，故只在连接时读一次。
	hello *agentproto.Hello
}

// 环形缓冲容量：spec §4.3 默认「最近 5min」。上报周期 10s → 30 个样本。
const outboxCap = 30

// controlCap 是控制消息队列深度。小容量是刻意的：状态上报是「最新状态覆盖旧状态」
// 的语义，积压一串历史状态没有意义；满了丢最旧的一条（下一次阶段跃迁还会再报）。
const controlCap = 8

// SendUpgradeStatus 上报一次升级状态（**永不阻塞**：队列满则丢最旧的一条）。
//
// 返回错误只在消息本身构造失败（无法序列化）时出现 —— 那种情况是代码缺陷，
// 不是运行时状况。
func (c *Client) SendUpgradeStatus(st *agentproto.UpgradeStatus) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentUpgradeStatus, st)
	if err != nil {
		return err
	}
	select {
	case c.controlCh <- msg:
		return nil
	default:
	}
	// 满了：丢最旧的再放（与样本队列同款取向，但这里丢的是**旧状态**）。
	select {
	case <-c.controlCh:
	default:
	}
	select {
	case c.controlCh <- msg:
	default:
		c.log.Warn("upgrade status dropped (control queue full)", "state", st.State)
	}
	return nil
}

// New 构造客户端。
func New(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = nopLogger{}
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	return &Client{
		cfg:       cfg,
		log:       cfg.Logger,
		state:     stateConnecting,
		sendCh:    make(chan *agentproto.MetricsSample, outboxCap),
		controlCh: make(chan *agentproto.Message, controlCap),
	}
}

// Send 投递一个样本。**永不阻塞**：缓冲满时丢弃最旧的（drop-oldest）。
//
// 为什么不阻塞：断网时采集不能停（spec §4.3「断线照常采集不阻塞」），
// 若这里阻塞，采集 goroutine 会跟着卡死，恢复连接后也补不回这段时间的数据。
// 丢最旧而不是最新：运维更关心「现在怎么了」，5 分钟前的样本已经过时。
func (c *Client) Send(s *agentproto.MetricsSample) {
	if s == nil {
		return
	}
	select {
	case c.sendCh <- s:
		return
	default:
	}
	// 缓冲满：丢一个最旧的再放入新的。
	select {
	case <-c.sendCh:
		c.mu.Lock()
		c.dropCount++
		c.mu.Unlock()
	default:
	}
	select {
	case c.sendCh <- s:
	default:
		// 极端竞争下仍放不进（另一个 Sender 抢先），本帧丢弃并计数。
		c.mu.Lock()
		c.dropCount++
		c.mu.Unlock()
	}
}

// DropCount 返回累计丢弃样本数（用于日志/诊断）。
func (c *Client) DropCount() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropCount
}

// Pending 返回当前待上报的样本数（用于 AgentMetric 的 pending_backlog）。
func (c *Client) Pending() int {
	return len(c.sendCh)
}

// ReportInterval 返回 core 下发的上报周期（未收到 ack 时为 0）。
func (c *Client) ReportInterval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reportInterval
}

// AgentToken 返回当前生效的 agent token（enroll 成功后才有值）。
// 调用方可把它落盘，供下次启动直接鉴权。
func (c *Client) AgentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agentToken != "" {
		return c.agentToken
	}
	return c.cfg.AgentToken
}

// Run 是重连主循环，阻塞直到 ctx 取消。
//
// 退避策略：指数增长到上限后**加入抖动**（jitter）。
// 没有抖动时，core 重启会让所有 agent 在同一毫秒重连（惊群），
// 把刚起来的服务再打挂一次。
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	const (
		minBackoff = time.Second
		maxBackoff = 60 * time.Second
	)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			c.log.Warn("connection ended, will retry", "err", err.Error(), "backoff", backoff.String())
		}
		// 等待退避，但可被 ctx 提前唤醒（退出时不至于卡 60s）。
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(backoff)):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = minBackoff // 连上过一次就重置节奏，避免长期顶格
		}
	}
}

// jitter 在 [0.5d, 1.5d) 区间内抖动。
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := float64(d) / 2
	return time.Duration(half + rand.Float64()*float64(d))
}

// runOnce 建立一次连接并跑到断开为止。
func (c *Client) runOnce(ctx context.Context) error {
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second

	ws, resp, err := dialer.DialContext(ctx, c.cfg.URL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s: %w (http %d)", c.cfg.URL, err, resp.StatusCode)
		}
		return fmt.Errorf("dial %s: %w", c.cfg.URL, err)
	}
	defer ws.Close()

	c.mu.Lock()
	c.conn = ws
	c.state = stateAwaitAck
	c.mu.Unlock()

	// 读循环放到 goroutine：既处理 hello_ack，也负责探测断链。
	// 写循环在主 goroutine 上跑心跳/上报。
	readErr := make(chan error, 1)
	closeRead := make(chan struct{})
	go func() { readErr <- c.readLoop(ws, closeRead) }()

	if err := c.sendHello(ws); err != nil {
		close(closeRead)
		return err
	}

	// 等 hello_ack；同时盯着读循环是否已失败。
	ackTimer := time.NewTimer(20 * time.Second)
	defer ackTimer.Stop()
	for {
		c.mu.Lock()
		st := c.state
		c.mu.Unlock()
		if st == stateActive {
			break
		}
		select {
		case <-ctx.Done():
			close(closeRead)
			return ctx.Err()
		case err := <-readErr:
			close(closeRead)
			if err == nil {
				err = errors.New("connection closed before hello_ack")
			}
			return err
		case <-ackTimer.C:
			close(closeRead)
			return errors.New("timeout waiting for core.hello_ack")
		case <-time.After(50 * time.Millisecond):
		}
	}

	interval := c.ReportInterval()
	if interval <= 0 {
		interval = 10 * time.Second
	}
	c.log.Info("connected and enrolled",
		"url", c.cfg.URL, "reportInterval", interval.String())

	writeErr := make(chan error, 1)
	closeWrite := make(chan struct{})
	go func() { writeErr <- c.writeLoop(ctx, ws, interval, closeWrite) }()

	select {
	case <-ctx.Done():
		close(closeRead)
		close(closeWrite)
		return ctx.Err()
	case err := <-readErr:
		close(closeWrite)
		return err
	case err := <-writeErr:
		close(closeRead)
		return err
	}
}

// readWait 是「多久没收到 core 的任何帧就判定链路已死」。
//
// 必须大于 core 的 ping 周期（否则空闲链路会被自己误杀），
// 也不能太大（否则真断链后要很久才重连）。core 的 ping 周期按
// 心跳配置（默认 30s），取 3 倍留出抖动余量。
const readWait = 90 * time.Second

// readLoop 处理来自 core 的消息直到出错。
//
// gorilla 的 ReadMessage 只在**数据帧**返回；ping/pong 这类控制帧
// 会被内部消化掉，不会让 ReadMessage 返回。因此如果只在循环开头设一次
// 读期限，一条「长期只有 ping、没有数据」的链路会在期限到达时被自己
// 判死（实测：连接稳定但每 90s 断一次，日志报 i/o timeout）。
// 正确做法是在收到 pong/ping 时**续期**。
func (c *Client) readLoop(ws *websocket.Conn, done <-chan struct{}) error {
	// 收到 pong：续期读期限。
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(readWait))
	})
	// 收到 ping：续期并回 pong。
	//
	// 这里**必须自己发 pong**：一旦覆盖了 ping handler，
	// gorilla 默认的「自动回 pong」就不再执行，不回会让 core 侧
	// 认为链路已死（core 用 ping/pong 判活）。
	ws.SetPingHandler(func(appData string) error {
		if err := ws.SetReadDeadline(time.Now().Add(readWait)); err != nil {
			return err
		}
		// 用 WriteControl 而不是 WriteMessage：控制帧可以在写锁外并发发送，
		// 不会与上报线程抢写锁（gorilla 保证 WriteControl 是并发安全的）。
		return ws.WriteControl(websocket.PongMessage, []byte(appData),
			time.Now().Add(10*time.Second))
	})

	for {
		select {
		case <-done:
			return nil
		default:
		}
		ws.SetReadDeadline(time.Now().Add(readWait))
		_, raw, err := ws.ReadMessage()
		if err != nil {
			// 关闭码要记下来：4001（凭据失效）与 4007（服务端停机）对运维
			// 是完全不同的处置方向。
			if ce, ok := err.(*websocket.CloseError); ok {
				return fmt.Errorf("read: closed code=%d reason=%q", ce.Code, ce.Text)
			}
			return fmt.Errorf("read: %w", err)
		}
		c.handleFrame(raw)
	}
}

// handleFrame 处理一帧来自 core 的消息。
//
// 未知类型（含未来新增的 core.* 消息）**忽略并计数**，不断开连接 ——
// spec §5.4 明确要求向前兼容：core 加了新消息不该让老 agent 掉线。
func (c *Client) handleFrame(raw []byte) {
	m, err := agentproto.Decode(raw)
	if err != nil {
		c.log.Warn("bad frame from core", "err", err.Error())
		return
	}
	switch m.Type {
	case agentproto.TypeCoreHelloAck:
		var ack agentproto.HelloAck
		if err := m.DecodeData(&ack); err != nil {
			c.log.Warn("bad hello_ack payload", "err", err.Error())
			return
		}
		if !ack.Accepted {
			// 被拒后不自作主张重试：拒因是凭据/版本问题，重试只会刷屏。
			c.log.Warn("hello rejected", "reason", ack.RejectReason)
			return
		}
		c.mu.Lock()
		if ack.AgentToken != "" {
			c.agentToken = ack.AgentToken
		}
		if ack.ReportInterval > 0 {
			c.reportInterval = time.Duration(ack.ReportInterval) * time.Second
		}
		c.state = stateActive
		c.mu.Unlock()
		// 顺序要紧：先确认「连上了」（升级事务据此确认新版本可用），再处理指令。
		// 反过来的话，一台刚确认就崩的设备仍会把自己标记成「升级成功」。
		if c.cfg.Hook != nil {
			c.cfg.Hook.OnConnected()
		}
		if ack.Upgrade != nil && c.cfg.Hook != nil {
			c.cfg.Hook.OnDirective(ack.Upgrade)
		}
	case agentproto.TypeCoreAgentUpgrade:
		// 升级指令的**即时催办**（hello_ack 是声明式对账，这条只是让在线设备
		// 不必等下一次重连）。方向校验走 DecodeTypedFor：core.* 出现在 agent 侧
		// 是正常的（这条就是），但载荷必须真的是 core→agent 方向登记的类型。
		decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirCoreToAgent)
		if err != nil {
			c.log.Warn("bad upgrade directive", "err", err.Error())
			return
		}
		d, ok := decoded.(*agentproto.UpgradeDirective)
		if !ok {
			c.log.Warn("upgrade directive type mismatch")
			return
		}
		if c.cfg.Hook != nil {
			c.cfg.Hook.OnDirective(d)
		}
	default:
		// 未知/未处理的类型：忽略（不关连接）。
		c.log.Debug("ignoring frame", "type", m.Type)
	}
}

// sendHello 发送注册/鉴权帧。
//
// enroll_token 与 agent_token 都带上：core 侧「agent_token 优先、enroll 兜底」，
// 这样首次注册与重启复用走同一条代码路径。
func (c *Client) sendHello(ws *websocket.Conn) error {
	hello := c.currentHello()
	if hello == nil {
		return errors.New("hello payload not configured (call SetHello first)")
	}
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentHello, hello)
	if err != nil {
		return fmt.Errorf("build hello: %w", err)
	}
	return c.writeJSON(ws, msg)
}

// SetHook 注入升级运行时（必须在 Run 之前调用）。
//
// 与 SetHello 同一契约：Hook 在连接生命周期里被读，运行中替换它没有意义。
func (c *Client) SetHook(h UpgradeHook) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Hook = h
}

// SetHello 注入 hello 载荷（InstanceID/Hostname/OS/... 与凭据）。
//
// 必须在 Run 之前调用：hello 里带的 instance_id 决定 core 侧是「新建设备」
// 还是「复用设备」，运行中改动没有意义。
func (c *Client) SetHello(h *agentproto.Hello) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hello = h
}

// currentHello 返回一份 hello 副本，并现场填上**恰好一个**凭据。
//
// 契约要求两者恰有其一（hello.CredentialKind）：
//   - 都缺失 → 4001「hello 凭据缺失或歧义」
//   - **都存在 → 同样 4001**：core 刻意「不暗自取优先级」，
//     两个都发会被判为歧义而拒连
//
// 这一点极易踩坑：直觉上「都带上更保险」，实际会让每一次重连都被拒，
// 而且现象是「首次能连上、之后再也连不上」—— 因为首次 enroll 时
// agent_token 还是空的，恰好只有一个凭据。
// 故这里严格二选一：有 agent_token 就只发它，否则只发 enroll_token。
//
// 凭据每次都现取：enroll 成功后 token 变了，若用启动时快照，
// 重连会一直拿旧 token 去鉴权。
func (c *Client) currentHello() *agentproto.Hello {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hello == nil {
		return nil
	}
	out := *c.hello
	// 先清空两者，再按优先级只填一个 —— 避免残留字段造成「凭据歧义」。
	out.EnrollToken = ""
	out.AgentToken = ""
	if tok := c.agentToken; tok != "" {
		out.AgentToken = tok
	} else if tok := c.cfg.AgentToken; tok != "" {
		out.AgentToken = tok
	} else {
		out.EnrollToken = c.cfg.EnrollToken
	}
	return &out
}

// writeLoop 以 reportInterval 为节奏：到点上报积压样本，并周期发心跳。
func (c *Client) writeLoop(ctx context.Context, ws *websocket.Conn, interval time.Duration, done <-chan struct{}) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	hbTicker := time.NewTicker(c.cfg.HeartbeatInterval)
	defer hbTicker.Stop()

	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-c.controlCh:
			// 控制消息即时发（不排队等上报 tick）：升级阶段的跃迁要尽快让服务端
			// 看见，而 10 秒的上报周期对状态面板来说太慢。
			if err := c.writeJSON(ws, msg); err != nil {
				return err
			}
		case <-ticker.C:
			// 一次 tick 把积压的样本**全部**发出去（重连后补发），
			// 而不是只发一个：否则断线 5 分钟攒下的 30 个样本要 5 分钟才追平。
			for {
				select {
				case s := <-c.sendCh:
					if err := c.sendMetrics(ws, s); err != nil {
						return err
					}
					continue
				default:
				}
				break
			}
		case <-hbTicker.C:
			if err := c.sendHeartbeat(ws); err != nil {
				return err
			}
			// WS 层 ping 兜底判活（应用层心跳之外的一条命）。
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		}
	}
}

func (c *Client) sendMetrics(ws *websocket.Conn, s *agentproto.MetricsSample) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentReportMetrics, s)
	if err != nil {
		// 样本本身不合法：这是采集侧的问题，丢弃本帧但不该断连接。
		c.log.Warn("dropping invalid sample", "err", err.Error())
		return nil
	}
	return c.writeJSON(ws, msg)
}

func (c *Client) sendHeartbeat(ws *websocket.Conn) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentHeartbeat, nil)
	if err != nil {
		return err
	}
	return c.writeJSON(ws, msg)
}

// writeJSON 是唯一的写出口（含写锁与超时）。
func (c *Client) writeJSON(ws *websocket.Conn, msg *agentproto.Message) error {
	b, err := msg.Marshal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
		return fmt.Errorf("write %s: %w", msg.Type, err)
	}
	return nil
}

// newID 生成信封 id。
//
// 契约要求「非空十进制无符号整数串」（isDecimalID 会拒其它形态）。
// 这里用纳秒时间戳 + 进程内自增，保证同进程唯一且长度远小于 MaxIDLen。
var idSeq uint64
var idMu sync.Mutex

func newID() string {
	idMu.Lock()
	idSeq++
	n := idSeq
	idMu.Unlock()
	return strconv.FormatInt(time.Now().UnixNano(), 10) + strconv.FormatUint(n%1000, 10)
}

// nopLogger 是默认日志实现。
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}
