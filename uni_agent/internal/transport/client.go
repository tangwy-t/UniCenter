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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
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
	// CAFile 是额外的根证书（PEM）路径；为空 = 只用系统信任库。
	//
	// wss 连自签服务端时的唯一信任来源（-ca-file / UNI_AGENT_CA_FILE）。
	// 本包刻意**不提供**「跳过校验」的开关：那会让加密退化成谁都能中间人。
	CAFile string
	// HeartbeatInterval 是应用层心跳周期（WS ping/pong 之外兜底）。
	HeartbeatInterval time.Duration
	// Logger 可为空。
	Logger Logger
	// Hook 是升级运行时与连接生命周期的接点（可为 nil —— agent 不装配升级能力时
	// 一切照旧，新协议消息只是被忽略）。
	Hook UpgradeHook
	// DockerHook 是 docker 运行时的连接侧接点（可为 nil —— 不装配 docker 能力时一切照旧）。
	DockerHook DockerHook
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

// DockerHook 是 docker 运行时需要的连接侧事件。
//
// 与 UpgradeHook **分开**而不是合并成一个接口：两者是独立的可选能力（一个部署可以只装
// 升级、只装 docker、或都装），合并会让任一能力缺失时另一个也得实现空方法 ——
// 而「实现了但什么都不做」正是最难发现的那类接线错误。
//
// 全部回调都必须在**读循环里非阻塞**（OnConnected/OnCmd 由 dockerops.Runtime
// 保证；OnBinaryFrame 的一次调用只做「查表 + 一次有界文件写」—— 分片 ≤ 256KB、
// 写的是页缓存，不走 fsync，不会把读循环拖进磁盘停摆，选型注释见
// dockerops/build_ctx_receive.go 的 Chunk）。
type DockerHook interface {
	// OnConnected 握手完成（docker 侧据此立刻上报首帧快照）。
	OnConnected()
	// OnConfig 收到 hello_ack 的 docker 配置块（重连时生效）。
	OnConfig(cfg *agentproto.DockerConfig)
	// OnCmd 收到一条操作指令。
	OnCmd(cmd *agentproto.DockerCmd)
	// OnFrame 收到一条流控制帧（一期不消费）。
	OnFrame(f *agentproto.CoreDockerFrame)
	// ── v1.3：构建上下文上传通道的接收面 ─────────────────────────────
	// OnBinaryFrame 收到一帧二进制分片（core 中转的浏览器上传字节）。
	// 帧头已由协议层解包并校验（魔数/版本/标志位/序号形态）—— 组装层的
	// 序号纪律（乱序/重复/末帧）与文件落盘在 dockerops 的接收器里做。
	OnBinaryFrame(f agentproto.BinaryFrame)
	// OnBuildCtxFinish 收到完成控制帧（期望哈希 + 产物名）：终验入口。
	OnBuildCtxFinish(m *agentproto.CoreDockerBuildCtxFinish)
	// OnBuildCtxAbort 收到中止控制帧：丢弃半成品。
	OnBuildCtxAbort(m *agentproto.CoreDockerBuildCtxAbort)
	// OnDisconnected 连接结束（读/写任一方向终止，从 runOnce 收尾前回调）：
	// 半成品上传必须在这里被收掉 —— 通道断了，完成/中止帧都不会再来。
	OnDisconnected()
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
	// dockerCh 投递 docker 的上行消息（快照与指令结果）。
	//
	// 与 controlCh 分开：两者的丢弃语义相反 —— 控制消息（升级状态）「新盖旧」，
	// 而 docker 的结果**丢了就要等 sweep 超时**（用户会看到「超时」而操作其实成功了）。
	dockerCh chan *agentproto.Message
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

// dockerCap 是 docker 上行队列深度。
//
// 取值 16 是「够一次突发、又不会让结果排太久」：单 worker 串行执行指令，
// 队列里同时有两条以上结果说明指令已经积压，此时再排队只会让用户等更久。
const dockerCap = 16

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

// SendDockerState 上报一帧快照。**非阻塞**：断线或队列满则立即返回错误。
//
// 为什么不排队：快照是**幂等的覆盖式数据**（下一轮 30 秒后就到），为它排队只会
// 让一份陈旧快照在重连后立刻发出去，反而把新鲜的那份挤掉。
func (c *Client) SendDockerState(st *agentproto.DockerState) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentDockerState, st)
	if err != nil {
		return err
	}
	if !c.active() {
		return errors.New("not connected")
	}
	select {
	case c.dockerCh <- msg:
		return nil
	default:
		return errors.New("docker send queue full")
	}
}

// SendDockerResult 上报一条指令结果。**可短暂等待**（最多 5 秒）。
//
// 为什么结果与快照的取舍相反：结果的丢失会让服务端把一条**已经执行成功**的指令终结为
// timeout（用户看到「超时」而实际生效了），这种不一致比多等 5 秒糟得多。
func (c *Client) SendDockerResult(res *agentproto.DockerCmdResult) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentDockerResult, res)
	if err != nil {
		return err
	}
	if !c.active() {
		// 断线时不排队：服务端会把它终结为超时（诚实的结果），而排队只会让
		// 「指令成功但结果 3 分钟后才到」这种更糟的时序发生。
		return errors.New("not connected")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case c.dockerCh <- msg:
		return nil
	case <-timer.C:
		return errors.New("docker send queue full after 5s")
	}
}

// active 报告连接是否处于 ACTIVE（已收到 hello_ack）。
func (c *Client) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == stateActive
}

// SendDockerFrame 上报一帧流数据（日志 chunk / PTY 输出）。**最多等 5 秒**。
//
// 为什么不像快照那样立即失败：帧是「已经发生」的数据（终端回显、日志行），丢掉就
// 不会再来。但也不能无限等 —— 它在会话的泵里被调用，无限等会把帧整形（合帧/限速）
// 一起拖住。等不到时如实返回错误，由会话管理器结束会话（消费端看到流结束，
// 而不是一份静默缺行的日志）。
func (c *Client) SendDockerFrame(f *agentproto.DockerFrame) error {
	msg, err := agentproto.NewMessage(newID(), agentproto.TypeAgentDockerFrame, f)
	if err != nil {
		return err
	}
	if !c.active() {
		return errors.New("not connected")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case c.dockerCh <- msg:
		return nil
	case <-timer.C:
		return errors.New("docker send queue full after 5s")
	}
}

// SetDockerHook 注入 docker 运行时（必须在 Run 之前调用，与 SetHook 同一契约）。
func (c *Client) SetDockerHook(h DockerHook) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.DockerHook = h
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
		dockerCh:  make(chan *agentproto.Message, dockerCap),
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
	// wss + 自签证书：显式信任给定的 CA 文件（不设 = 用系统信任库）。
	// **不提供 InsecureSkipVerify 选项**：那会让「加密」退化成「谁都能中间人」，
	// 而这条通道二期起能换来宿主机 root 执行。
	//
	// 这段必须在 DialContext **之前**：CA 文件坏掉时静默退化到系统信任库，
	// 会让人以为「配了 CA」而实际走的是另一套信任链（排障时最难发现的一类）。
	if c.cfg.CAFile != "" {
		pool, err := LoadCAPool(c.cfg.CAFile)
		if err != nil {
			return err
		}
		dialer.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

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

	// 每次连接结束（不管哪一路）都必须通知 docker 侧一次「连接没了」：半成品
	// 上传的分片流到此为止，完成/中止帧都不会再送达 —— 接收器靠它删掉半成品。
	disconnected := sync.OnceFunc(func() {
		if c.cfg.DockerHook != nil {
			c.cfg.DockerHook.OnDisconnected()
		}
	})
	defer disconnected()

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
		mt, raw, err := ws.ReadMessage()
		if err != nil {
			// 关闭码要记下来：4001（凭据失效）与 4007（服务端停机）对运维
			// 是完全不同的处置方向。
			if ce, ok := err.(*websocket.CloseError); ok {
				return fmt.Errorf("read: closed code=%d reason=%q", ce.Code, ce.Text)
			}
			return fmt.Errorf("read: %w", err)
		}
		switch mt {
		case websocket.TextMessage:
			c.handleFrame(raw)
		case websocket.BinaryMessage:
			c.handleBinaryFrame(raw)
		default:
			c.log.Debug("ignoring ws frame", "message_type", mt)
		}
	}
}

// handleBinaryFrame 处理一条二进制消息（v1.3 core→agent 上传分片）。
//
// 解包失败只记日志**不断连**：二进制帧是搭在连接上的旁路载荷，为它断连会
// 连累指标与心跳 —— 帧头的严格校验已由协议层在解包时做完（魔数/版本/标志
// 位），「分片的序号纪律」由 dockerops 的接收器裁决（弃会话而不是弃连接）。
func (c *Client) handleBinaryFrame(raw []byte) {
	f, err := agentproto.UnpackBinaryFrame(raw)
	if err != nil {
		c.log.Warn("bad binary frame from core", "err", err.Error())
		return
	}
	if c.cfg.DockerHook != nil {
		c.cfg.DockerHook.OnBinaryFrame(f)
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
		// 顺序要紧：**先应用配置、再触发首帧快照** —— 反过来的话首帧会用上一份
		// 保护清单/周期采集，而那一帧正是页面看到的第一印象。
		if c.cfg.DockerHook != nil {
			if ack.Docker != nil {
				c.cfg.DockerHook.OnConfig(ack.Docker)
			}
			c.cfg.DockerHook.OnConnected()
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
	case agentproto.TypeCoreDockerCmd:
		// 与升级指令同一取向：类型登记了就不该「未知」，解不出来只记日志。
		decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirCoreToAgent)
		if err != nil {
			c.log.Warn("bad docker cmd", "err", err.Error())
			return
		}
		cmd, ok := decoded.(*agentproto.DockerCmd)
		if !ok {
			c.log.Warn("docker cmd type mismatch")
			return
		}
		if c.cfg.DockerHook != nil {
			c.cfg.DockerHook.OnCmd(cmd)
		}
	case agentproto.TypeCoreDockerFrame:
		decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirCoreToAgent)
		if err != nil {
			c.log.Warn("bad docker frame", "err", err.Error())
			return
		}
		if f, ok := decoded.(*agentproto.CoreDockerFrame); ok && c.cfg.DockerHook != nil {
			c.cfg.DockerHook.OnFrame(f)
		}
	case agentproto.TypeCoreDockerBuildCtxFinish, agentproto.TypeCoreDockerBuildCtxAbort:
		// v1.3 上传控制帧：与升级指令同一取向 —— 类型登记了就不该「未知」，
		// 解不出来只记日志（被丢弃的半成品由 24h 清扫与断连清理兜底）。
		decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirCoreToAgent)
		if err != nil {
			c.log.Warn("bad build_ctx control", "type", m.Type, "err", err.Error())
			return
		}
		if c.cfg.DockerHook == nil {
			return
		}
		switch ctl := decoded.(type) {
		case *agentproto.CoreDockerBuildCtxFinish:
			c.cfg.DockerHook.OnBuildCtxFinish(ctl)
		case *agentproto.CoreDockerBuildCtxAbort:
			c.cfg.DockerHook.OnBuildCtxAbort(ctl)
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
		case msg := <-c.dockerCh:
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

// LoadCAPool 读取 PEM 文件并构造根证书池。
//
// 单独提出来是因为它有两个消费方：WS 拨号器（wss）与**升级产物的下载客户端**（https）。
// 只给 WS 配 CA 而漏掉下载客户端，症状是「连接一直正常，但下一次 agent 升级永远失败在
// x509: certificate signed by unknown authority」—— 故障出现在升级链路上，与「证书配置」
// 的联想距离很远，故让两处共用同一份实现：漏配时至少是同一处代码。
func LoadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 文件失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA 文件不含可用的证书: %s", path)
	}
	return pool, nil
}

// CAClient 返回信任给定 CA 的 HTTP 客户端；path 为空时返回 (nil, nil)，
// 表示「用默认客户端（系统信任库）」——调用方据此保留 nil 即可。
func CAClient(path string, timeout time.Duration) (*http.Client, error) {
	if path == "" {
		return nil, nil
	}
	pool, err := LoadCAPool(path)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}
