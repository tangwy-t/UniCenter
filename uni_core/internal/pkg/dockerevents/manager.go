// Package dockerevents 是 core 侧的**事件流常驻订阅管理器**（总览页「活动流」的
// 数据源，六期·监控面）。
//
// 它与既有三类流（日志/终端/stats）的根本差别：后三类是**逐客户端代理** —— 由用户
// 发指令、agent 回会话、客户端接流；事件流则是 **core 自己当常驻客户端**：
//
//	首个 console 会话连上聚合端点 → core 对全部 dockerOk 主机逐台发起 docker:events
//	    指令并开流（复用 core.docker.cmd / agent.docker.result / agent.docker.frame
//	    同一条指令通道，只因 frame 的 session 登记不同而归本包所有）；
//	最后一个 console 会话断开 → 全部退订（**引用计数**，不许留下孤儿订阅）；
//	单台 agent 断连/结果失败/会话 eof → 该主机退订并按退避重试（不影响其它主机）；
//	主机清单变化（新增可管主机 / 设备删除 / docker_ok 翻转）→ 对账循环增量订/退。
//
// 事件注入 hostId/hostname 后才扇出：console 拿到的是带归属的聚合流。
//
// 三条不变式（测试逐条钉住）：
//   - **投递绝不阻塞**：帧来自 agent 连接的唯一读循环，堵住它会连累指标/心跳 ——
//     因此帧路径只在内存里动（JSON 解析 + 环形缓冲 + 分发），一切 Redis/网络 IO
//     都在对账循环的锁外段执行；
//   - **常驻订阅有寿命**：引用计数归零即全量退订；主机离开可管清单即退订且不留
//     环形缓冲（设备删除后回放旧事件会让人误以为它还活着）；
//   - **失败是单台的**：一台主机的退避/断连不影响别的主机，也绝不升级成对客户端
//     的错误 —— 活动流少一行（该主机的），好过整条流断掉。
package dockerevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 冻结常数与可注入参数 ─────────────────────────────────────────────────

const (
	// defaultRingDepth 是每台主机的回放缓冲条数。
	//
	// 为什么按**主机**分而不是全局一条：归属在回放里就是语义 —— 全局环形会让一台
	// 高频主机把低频主机的历史全挤掉（「我看不到 B 主机发生了啥」），也压缩了
	// 「按主机排序回放」的空间。50 条 ≈ 活动流面板一屏的量：打开即有内容，又不是
	// 一份小日志。
	defaultRingDepth = 50
	// defaultResultWait 是订阅指令结果的等待窗口。建立订阅是本地操作（agent 打开
	// daemon 事件流），10 秒拿不到 result 只可能是 agent 卡死/连接抖动 —— 与其死等，
	// 不如退避后重试（重发是幂等的：agent 侧 events 槽位只有一条，重复订阅会被拒）。
	defaultResultWait = 10 * time.Second
	// defaultReconcileInterval 是对账周期：主机清单变化（新主机第一帧快照、设备删除）
	// 在这个节拍内被收敛；周期与快照上报间隔（30s）同量级 —— 快照是 dockerOk 的
	// 事实源，对账跑得比快照快一半没有任何增量收益。
	defaultReconcileInterval = 10 * time.Second
	// defaultRetryBase / defaultRetryMax 是单台退避的起终点（1s 起、指数、60s 封顶）。
	// 封顶 60s 意味着「agent 重连后事件流 ≤1 分钟内恢复」的可承诺恢复时间。
	defaultRetryBase = time.Second
	defaultRetryMax  = time.Minute
	// defaultMaxPendingFrames 是 session_id 未定前每主机的帧暂存条数。帧借 agent 的
	// 发送顺序先于 result 到线的竞态窗口极小（同一连接的读循环按序处理），留 16 条
	// 只是把「用户流早到帧恰好与订阅建窗撞上」这类边角收住，不是数据湖。
	defaultMaxPendingFrames = 16
	// defaultClientQueueDepth 是每个 console 客户端在内核写缓冲之外的**待消费上限**。
	// 满则丢最旧并计数（与用户流注册表同纪律：客户端读不动就少看几条，而不是拖死
	// 别的客户端与服务端内存）。
	defaultClientQueueDepth = 256
	// cmdRecordTTL 是 events 指令记录的受理窗口（与 dockerpolicy.SessionSetupTimeout
	// 同档 30s）：记录是 result 回链的挂靠点 —— 管理器的 ResultWait（10s）先到期，
	// 记录被 sweep 终结只是兜底（manager 的 ref 已在更早退避后换新）。
	cmdRecordTTL = 30 * time.Second
)

// Options 是管理器的可注入参数（测试要确定性时钟、短间隔与确定的退避）。
type Options struct {
	RingDepth         int
	ResultWait        time.Duration
	ReconcileInterval time.Duration
	RetryBase         time.Duration
	RetryMax          time.Duration
	MaxPendingFrames  int
	ClientQueueDepth  int
	Now               func() time.Time
	After             func(time.Duration) <-chan time.Time
}

func (o Options) withDefaults() Options {
	if o.RingDepth <= 0 {
		o.RingDepth = defaultRingDepth
	}
	if o.ResultWait <= 0 {
		o.ResultWait = defaultResultWait
	}
	if o.ReconcileInterval <= 0 {
		o.ReconcileInterval = defaultReconcileInterval
	}
	if o.RetryBase <= 0 {
		o.RetryBase = defaultRetryBase
	}
	if o.RetryMax <= 0 {
		o.RetryMax = defaultRetryMax
	}
	if o.MaxPendingFrames <= 0 {
		o.MaxPendingFrames = defaultMaxPendingFrames
	}
	if o.ClientQueueDepth <= 0 {
		o.ClientQueueDepth = defaultClientQueueDepth
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	return o
}

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）────────────────────────

// Sender 是下行送达能力面（由 agenthub.Hub 满足）。返回 agenthub.ErrDeviceOffline
// 表示目标不在线 —— 这是退避重试的主信号（重连后重订的收敛点）。
type Sender interface {
	SendToDevice(deviceID uint64, msg *agentproto.Message) error
}

// CmdRecorder 建一条 events 指令记录（由 dockerstate.CmdStore 满足）。
//
// 为什么常驻订阅也要建记录：agent 的 result 回写（agent_ingest.CompleteDockerCmd）
// 只认**记录存在**的 ref —— 不建记录的 result 会被当「过期或伪造」丢掉，session_id
// 就到不了管理器手上。记录的一切用户语义（UserID/Perm）对 events 都是零值。
type CmdRecorder interface {
	Create(ctx context.Context, rec *dockerstate.CmdRecord, timeout time.Duration) error
}

// HostsReader 是可管主机枚举能力面（由 dockerstate.Store 满足）：
// Hosts 给 docker:hosts 集合，Get 给单主机快照（docker_ok 是「应订阅」的判据）。
type HostsReader interface {
	Hosts(ctx context.Context) ([]uint64, error)
	Get(ctx context.Context, deviceID uint64) (*dockerstate.Envelope, error)
}

// HostnameFunc 解析一台主机的展示名（由 device 仓储满足）。读不到（设备已删/DB 抖动）
// 返回空串 —— 归属注入降级为只有 hostId，订阅本身不受牵连（设备已删的场景由
// docker:hosts 集合清理兜底收敛）。
type HostnameFunc func(ctx context.Context, deviceID uint64) string

// Event 是扇出给 console 客户端的一条事件（**已注入归属**）。
type Event struct {
	DeviceID uint64
	Hostname string
	Item     agentproto.DockerEventItem
}

// ErrClosed 表示客户端已关闭且缓冲已排空。
var ErrClosed = errors.New("dockerevents: 客户端已关闭")

// ── 单台主机订阅的状态机 ─────────────────────────────────────────────────

type subState uint8

const (
	// subRetry 是「未订阅」：从没订过、失败了、或已被清退 —— 到 nextTry 才再试。
	subRetry subState = iota
	// subWaiting 是「指令已发，等 result」：result 带 session_id 到才转 active。
	subWaiting
	// subActive 是「订阅已建立」：收帧、记账，直到 eof/离线/退订。
	subActive
)

func (s subState) String() string {
	switch s {
	case subRetry:
		return "retry"
	case subWaiting:
		return "waiting"
	}
	return "active"
}

// hostSub 是一台主机的订阅账目（全部字段只在与 m.mu 相伴时被读写）。
type hostSub struct {
	deviceID uint64
	hostname string

	ref       string
	sessionID string
	state     subState

	failCount      int
	nextTry        time.Time
	resultDeadline time.Time
	// pending 是 session_id 未定前的**帧暂存**：agent 先发帧后发 result 的竞态
	//（会话建立是异步的）下，帧照序排在 result 前到线 —— 先收下，result 到了按
	// session_id 认领补齐（不是订阅的帧在认领时无声丢弃，它本就属于用户流）。
	pending []*agentproto.DockerFrame
}

// hostSub 的全部字段只在持有 m.mu 时被读写 —— runOnce 的锁外段从不直接摸它们，
// 动作结果一律经 subscribeAttempt 回账（帧入口 DeliverDockerFrame 也因此无阻塞 IO）。

// Manager 是事件流常驻订阅的管理者：主机面对账 + 引用计数 + 环形回放 + 扇出。
type Manager struct {
	sender   Sender
	cmds     CmdRecorder
	hosts    HostsReader
	hostname HostnameFunc
	online   func(deviceID uint64) bool
	log      logger.LoggerInterface
	idGen    func() string
	opts     Options

	mu      sync.Mutex
	subs    map[uint64]*hostSub
	rings   map[uint64][]Event
	clients map[*Client]struct{}
	wake    chan struct{}
}

// NewManager 构造管理器（缺省参数见 Options.withDefaults）。
func NewManager(opts Options, sender Sender, cmds CmdRecorder, hosts HostsReader,
	hostname HostnameFunc, log logger.LoggerInterface) *Manager {
	return &Manager{
		sender:   sender,
		cmds:     cmds,
		hosts:    hosts,
		hostname: hostname,
		log:      log,
		idGen:    defaultRefGen,
		opts:     opts.withDefaults(),
		subs:     map[uint64]*hostSub{},
		rings:    map[uint64][]Event{},
		clients:  map[*Client]struct{}{},
		wake:     make(chan struct{}, 1),
	}
}

// WithOnline 注入设备在线判定（active 会话的离线清退依据；由 agenthub.Hub.Get 包装）。
// nil = 跳过离线判定（测试/未装配）：订阅的收敛退化为「eof 或下次重发失败」。
func (m *Manager) WithOnline(online func(deviceID uint64) bool) *Manager {
	m.online = online
	return m
}

// WithIDGen 注入指令号生成器（生产由 wireup 注入与指令面**共享计数器**的函数；
// 缺省实现只保证本管理器内部唯一）。
func (m *Manager) WithIDGen(idGen func() string) *Manager {
	m.idGen = idGen
	return m
}

// refSeq 是缺省 ref 生成器的进程内自增序号。
var refSeq atomic.Uint64

// defaultRefGen 是 idGen 的缺省实现（纳秒时间戳 + 进程内自增，与指令面同算法）。
//
// 生产必须换成 wireup 注入的**共享计数器**函数（service.NewRequestID）：ref 与用户
// 指令共用 docker:cmd:<ref> 键空间，两个独立的计数器会把「同一纳秒、两计数器等值」
// 的撞号窗口重新搬回来；这里保留一份独立实现只为「未注入也能跑」的测试与降级。
func defaultRefGen() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) + strconv.FormatUint(refSeq.Add(1), 10)
}

// SubscriberCount 返回当前连接的 console 客户端数（引用计数）。
func (m *Manager) SubscriberCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clients)
}

// SubscribedHosts 返回当前已建立/在途的订阅主机数（测试与日志用）。
func (m *Manager) SubscribedHosts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

// ── 引用计数与扇出 ───────────────────────────────────────────────────────

// Subscribe 登记一个 console 客户端（引用计数 +1）：先把各主机环形缓冲**按序回放**
// 进它的队列，之后实时事件经广播到达。首个客户端会踢醒对账循环（开始向全部可管
// 主机订阅）；客户端读完或用完必须 Close（引用计数 -1）。
func (m *Manager) Subscribe() *Client {
	c := &Client{m: m, depth: m.opts.ClientQueueDepth, wake: make(chan struct{}, 1)}
	m.mu.Lock()
	wasFirst := len(m.clients) == 0
	m.clients[c] = struct{}{}
	// 回放与广播共用这一把锁：先登记、再快照 —— 期间不可能有事件被广播
	//（广播同样要这把锁），因此回放既不重复（先于登记的事件在环形里，登记后广播
	// 不会再发一遍）也不漏（登记后的事件走广播，必然排在回放之后）。
	for _, id := range m.sortedRingHostsLocked() {
		for _, e := range m.rings[id] {
			c.pushLocked(e)
		}
	}
	m.mu.Unlock()
	if wasFirst {
		m.kick()
	}
	return c
}

// sortedRingHostsLocked 返回有回放缓冲的主机 id（升序 —— 回放顺序稳定可复现）。
func (m *Manager) sortedRingHostsLocked() []uint64 {
	ids := make([]uint64, 0, len(m.rings))
	for id := range m.rings {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// teardownAll 在引用计数归零时退订全部主机（**保留环形缓冲**：下一个客户端打开
// 活动流仍先看到「刚才发生了什么」）。cancel 是锁外 best-effort —— 发不出去只记
// 日志，本地账目必须立即清掉（用户断开后不得再挂着 daemon 侧的事件流）。
func (m *Manager) teardownAll(reason string) {
	m.mu.Lock()
	type off struct {
		sub       *hostSub
		sessionID string
	}
	list := make([]off, 0, len(m.subs))
	for _, sub := range m.subs {
		list = append(list, off{sub: sub, sessionID: sub.sessionID})
	}
	m.mu.Unlock()

	for _, o := range list {
		m.sendCancel(o.sub.deviceID, o.sessionID)
	}
	m.mu.Lock()
	for _, o := range list {
		if m.subs[o.sub.deviceID] == o.sub {
			m.dropSubLocked(o.sub, true)
		}
	}
	m.logInfo("docker events 常驻订阅全量退订", zap.String("reason", reason), zap.Int("hosts", len(list)))
	m.mu.Unlock()
}

// ── 常驻会话的帧入口（agenthub.DockerFrameDeliverer 的窄接口实现）─────────

// DeliverDockerFrame 处理一条 agent.docker.frame。**绝不阻塞**（它在 agent 连接的
// 读循环里）：本方法只在内存里动 —— 解析、环形记账、向客户端队列分发（分发也不
// 阻塞：客户端队列满丢最旧）。
//
// 返回 ErrNotFound 表示「不是常驻订阅的帧」（给路由器的第二段判定用 —— 用户注册表
// 已先判过一次，这里再判一次即「两边都不认识」）。
func (m *Manager) DeliverDockerFrame(_ context.Context, deviceID uint64, f *agentproto.DockerFrame) error {
	if f == nil {
		return nil
	}
	if err := f.Validate(); err != nil {
		// 解码路径已校验过；这里再校验是防御（协议纪律：core 不信任任何帧）。
		m.logWarn("docker events frame invalid, dropped", zap.Uint64("deviceId", deviceID), zap.Error(err))
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[deviceID]
	if sub == nil {
		return dockerstream.ErrNotFound
	}
	if sub.sessionID == "" {
		// result 未到（帧抢在 result 前到线）：暂存，认领时按 session_id 匹配。
		if len(sub.pending) < m.opts.MaxPendingFrames {
			sub.pending = append(sub.pending, f)
		} else if m.log != nil {
			m.log.Debug("docker events pending frames overflow, dropped",
				zap.Uint64("deviceId", deviceID), zap.Uint64("seq", f.Seq))
		}
		return nil
	}
	if f.SessionID != sub.sessionID {
		// 用户会话的早到帧（被路由器第二段接住）或旧会话残帧：不属于常驻流。
		if m.log != nil {
			m.log.Debug("docker events frame session mismatch, dropped",
				zap.Uint64("deviceId", deviceID), zap.String("session", f.SessionID))
		}
		return nil
	}
	if f.EOF {
		// eof 帧**可与末帧数据同帧**（帧契约）：先照常记最后一笔数据，再收摊 ——
		// 顺序反了会把会话的最后几条事件吞掉。
		m.emitLocked(sub, f)
		// agent 侧订阅自然结束（daemon 重启/连接更换）：退避重订 —— 单台失败，
		// 绝不拖累其它主机与客户端。
		m.failLocked(sub, "agent 端订阅结束")
		m.kickLocked()
		return nil
	}
	m.emitLocked(sub, f)
	return nil
}

// emitLocked 解码一帧里的 JSON 事件行：逐行校验 → 进环形缓冲 → 广播给全部客户端
// （调用方必须持 m.mu）。
func (m *Manager) emitLocked(sub *hostSub, f *agentproto.DockerFrame) {
	for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		var item agentproto.DockerEventItem
		if err := json.Unmarshal(raw, &item); err != nil || item.Validate() != nil {
			// 解不开/白名单外的行：跳过并留痕（与 stats 端点「丢一个样本点」同纪律 ——
			// 丢一行事件好过把整条流转成错误）。
			if m.log != nil {
				m.log.Warn("docker event dropped (invalid)",
					zap.Uint64("deviceId", sub.deviceID), zap.Uint64("seq", f.Seq), zap.Error(err))
			}
			continue
		}
		e := Event{DeviceID: sub.deviceID, Hostname: sub.hostname, Item: item}
		ring := append(m.rings[sub.deviceID], e)
		if over := len(ring) - m.opts.RingDepth; over > 0 {
			ring = ring[over:]
		}
		m.rings[sub.deviceID] = ring
		for c := range m.clients {
			c.pushLocked(e)
		}
	}
}

// ── 结果入口（agent_ingest.CompleteDockerCmd 的常驻分支调用）──────────────

// OnEventsResult 认领一条 docker:events 指令的 result。
//
// 它被 agent 通道的**读循环**同步调用（与后续帧同序）：在带内认领 session_id 后，
// 之后到达的帧才会被本连接处理 —— 暂存帧的认领回放也因此天然保序；迟到的 result
// （ref 已换新/订阅已不在）按「陈旧」丢弃 —— 重发是幂等设计的代价。
func (m *Manager) OnEventsResult(deviceID uint64, res *agentproto.DockerCmdResult) {
	if res == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[deviceID]
	if sub == nil || sub.ref != res.Ref {
		if m.log != nil {
			m.log.Debug("docker events stale result, dropped",
				zap.Uint64("deviceId", deviceID), zap.String("ref", res.Ref))
		}
		return
	}
	if !res.OK || res.SessionID == "" || !agentproto.IsDockerSessionID(res.SessionID) {
		m.failLocked(sub, res.Error)
		m.kickLocked()
		return
	}
	sub.sessionID = res.SessionID
	sub.state = subActive
	sub.failCount = 0
	// 认领暂存帧：只补发本订阅（session_id 匹配）的，其余（用户流的早到帧）无声
	// 丢弃 —— 它们本来就该被用户注册表接住，只是恰好撞进了暂存窗口。
	// eof 标记不进暂存回放（带内时序里 result 之前不可能有 eof；真出现也当数据帧
	// 记最后一笔就好，会话寿命由核心状态机自己推进）。
	for _, pf := range sub.pending {
		if pf.SessionID != sub.sessionID {
			continue
		}
		pf.EOF = false
		m.emitLocked(sub, pf)
	}
	sub.pending = nil
	m.kickLocked()
}

// ── 对账循环 ─────────────────────────────────────────────────────────────

// Run 是管理器的对账循环（wireup 起 goroutine）：按「下一次该做什么」的最近时刻
// 唤醒 —— 周期对账、退避到点、结果窗口到期三个节拍合用一个计时器。
func (m *Manager) Run(ctx context.Context) {
	for {
		delay := m.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-m.opts.After(delay):
		}
	}
}

// runOnce 跑一轮对账，返回距下一轮应醒的时长。
//
// 结构是三段式（锁 → IO → 锁）：锁内只算「要做什么」（主机差集、退避到点、超时），
// Redis/网络 IO 全部在锁外 —— 帧投递（DeliverDockerFrame）也在争这把锁，任何一次
// 阻塞 IO 都不能把它拖住（读循环纪律，见包注释）。
func (m *Manager) runOnce(ctx context.Context) time.Duration {
	now := m.opts.Now()
	next := m.opts.ReconcileInterval

	m.mu.Lock()
	hasClients := len(m.clients) != 0
	m.mu.Unlock()
	if !hasClients {
		return next // 没有消费端：主机面静止（归零路径已同步退订，这里是每轮的兜底确认）
	}

	desired, ok := m.computeDesired(ctx)
	if !ok {
		// 枚举失败（Redis 抖动）：整轮跳过 —— 退订动作绝不因「读不到」而误发，
		// 那会让一次抖动瞬间抹掉全部订阅。
		return next
	}

	type leave struct {
		sub       *hostSub
		sessionID string
	}
	type fail struct {
		sub    *hostSub
		reason string
	}
	m.mu.Lock()
	var joins, retries []*hostSub
	var leaves []leave
	var fails []fail
	for id, name := range desired {
		sub, exists := m.subs[id]
		if !exists {
			sub = &hostSub{deviceID: id, hostname: name, state: subRetry, nextTry: now}
			m.subs[id] = sub
			joins = append(joins, sub)
			continue
		}
		sub.hostname = name // 主机名跟随设备表刷新（改名的设备不重订也拿新名）
		switch sub.state {
		case subRetry:
			if !now.Before(sub.nextTry) {
				retries = append(retries, sub)
			}
		case subWaiting:
			if !now.Before(sub.resultDeadline) {
				fails = append(fails, fail{sub: sub, reason: "订阅结果超时"})
			}
		case subActive:
			if m.online != nil && !m.online(id) {
				fails = append(fails, fail{sub: sub, reason: "设备离线"})
			}
		}
	}
	for id, sub := range m.subs {
		if _, ok := desired[id]; !ok {
			leaves = append(leaves, leave{sub: sub, sessionID: sub.sessionID})
		}
	}
	if len(joins)+len(retries)+len(leaves)+len(fails) > 0 {
		next = 0 // 有动作：本轮做完立刻再算一轮（不等满一个周期）
	}
	m.mu.Unlock()

	// ── IO 段（锁外）──────────────────────────────────────────────────────
	for _, lv := range leaves {
		m.sendCancel(lv.sub.deviceID, lv.sessionID)
	}
	attempts := make([]subscribeAttempt, 0, len(joins)+len(retries))
	for _, sub := range joins {
		attempts = append(attempts, m.trySubscribe(ctx, sub, now))
	}
	for _, sub := range retries {
		attempts = append(attempts, m.trySubscribe(ctx, sub, now))
	}

	// ── 收账段（锁内）─────────────────────────────────────────────────────
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lv := range leaves {
		if m.subs[lv.sub.deviceID] == lv.sub {
			// 设备删除/docker_ok 翻转：连环形缓冲一起清掉 —— 回放旧事件会让人
			// 误以为它还在活动。
			m.dropSubLocked(lv.sub, false)
		}
	}
	for _, at := range attempts {
		if m.subs[at.sub.deviceID] != at.sub {
			continue // 执行期间被退订（客户端清零/清单变化）：结果作废
		}
		if at.waiting {
			at.sub.ref = at.ref
			at.sub.state = subWaiting
			at.sub.resultDeadline = now.Add(m.opts.ResultWait)
			continue
		}
		m.failLocked(at.sub, at.reason)
	}
	for _, fl := range fails {
		if m.subs[fl.sub.deviceID] == fl.sub {
			m.failLocked(fl.sub, fl.reason)
		}
	}
	return m.nextWakeLocked(now, next)
}

// computeDesired 枚举「当前应订阅」的主机：快照 docker_ok=true 且设备存在。
//
// 判据与读面唯一事实源一致（docker:hosts 集合 + 快照的信封结论）：没有快照
// （从未上报/键被清）的主机不算可管 —— 不订阅，等它的第一帧快照在对账里收敛。
func (m *Manager) computeDesired(ctx context.Context) (map[uint64]string, bool) {
	ids, err := m.hosts.Hosts(ctx)
	if err != nil {
		m.logWarn("docker events 枚举主机失败", zap.Error(err))
		return nil, false
	}
	desired := make(map[uint64]string, len(ids))
	for _, id := range ids {
		env, err := m.hosts.Get(ctx, id)
		if err != nil || env == nil || !env.State.DockerOK {
			continue // 读失败/从未上报/docker 不可用：都不是「此刻能订阅」的主机
		}
		name := ""
		if m.hostname != nil {
			name = m.hostname(ctx, id)
		}
		desired[id] = name
	}
	return desired, true
}

// trySubscribe 向一台主机发起一次订阅（锁外执行）：建指令记录 → 下发 → 报告结果。
//
// 复用的正是指令面的受理链（Create + core.docker.cmd + result 回链），只是发起人是
// 管理器自己：记录的用户态字段（UserID/Perm/Confirm）全部为零值 —— 这条指令
// 从来不出现在任何用户的轮询面上。
func (m *Manager) trySubscribe(ctx context.Context, sub *hostSub, now time.Time) subscribeAttempt {
	at := subscribeAttempt{sub: sub}
	ref := m.idGen()
	rec := &dockerstate.CmdRecord{
		Ref: ref, DeviceID: sub.deviceID, Action: agentproto.DockerActionEvents,
		UserID: 0, Perm: "", CreatedAt: now.UnixMilli(),
	}
	if err := m.cmds.Create(ctx, rec, cmdRecordTTL); err != nil {
		m.logWarn("docker events 指令记录写入失败",
			zap.Uint64("deviceId", sub.deviceID), zap.Error(err))
		at.reason = "指令记录写入失败"
		return at
	}
	msg, err := agentproto.NewMessage(ref, agentproto.TypeCoreDockerCmd, &agentproto.DockerCmd{
		Ref: ref, Action: agentproto.DockerActionEvents, Options: agentproto.DockerCmdOptions{},
	})
	if err != nil {
		m.logWarn("docker events 指令组装失败", zap.Uint64("deviceId", sub.deviceID), zap.Error(err))
		at.reason = "指令组装失败"
		return at
	}
	if err := m.sender.SendToDevice(sub.deviceID, msg); err != nil {
		if errors.Is(err, agenthub.ErrDeviceOffline) {
			// 离线是**常态事件**（agent 重启/断网）：Debug 而不是 Warn ——
			// 退避重试会在它重连后自动收敛，刷屏的警告只会淹没真正的故障。
			if m.log != nil {
				m.log.Debug("docker events 订阅未送达（设备离线）",
					zap.Uint64("deviceId", sub.deviceID))
			}
		} else {
			m.logWarn("docker events 订阅未送达",
				zap.Uint64("deviceId", sub.deviceID), zap.Error(err))
		}
		at.reason = "指令未送达"
		return at
	}
	at.ref = ref
	at.waiting = true
	return at
}

// ── 工具 ─────────────────────────────────────────────────────────────────

// failLocked 把一台主机转入退避（调用方必须持 m.mu）：计数、算下次时刻、清
// 会话与暂存。reason 只进日志 —— 单台失败绝不升级成对客户端的错误。
func (m *Manager) failLocked(sub *hostSub, reason string) {
	sub.state = subRetry
	sub.failCount++
	sub.ref = ""
	sub.sessionID = ""
	sub.pending = nil
	sub.nextTry = m.opts.Now().Add(m.backoff(sub.failCount))
	m.logInfo("docker events 订阅退避",
		zap.Uint64("deviceId", sub.deviceID),
		zap.String("reason", reason),
		zap.Int("attempt", sub.failCount),
		zap.Duration("in", sub.nextTry.Sub(m.opts.Now())))
}

// subscribeAttempt 是一次 subscribe 尝试的锁外结论（runOnce 的收账段据此转状态）。
type subscribeAttempt struct {
	sub     *hostSub
	ref     string
	waiting bool
	reason  string
}

// backoff 返回第 n 次失败后的退避时长（指数、封顶）。
func (m *Manager) backoff(n int) time.Duration {
	d := m.opts.RetryBase << min(n-1, 16)
	if d > m.opts.RetryMax {
		d = m.opts.RetryMax
	}
	return d
}

// nextWakeLocked 返回下一轮该醒的时刻：周期、各主机退避到点、结果窗口到期取最近。
func (m *Manager) nextWakeLocked(now time.Time, next time.Duration) time.Duration {
	for _, sub := range m.subs {
		var d time.Duration
		switch sub.state {
		case subRetry:
			d = sub.nextTry.Sub(now)
		case subWaiting:
			d = sub.resultDeadline.Sub(now)
		default:
			continue
		}
		if d < 0 {
			d = 0
		}
		if d < next {
			next = d
		}
	}
	return next
}

// dropSubLocked 移除一台主机的订阅账目（调用方必须持 m.mu）；keepRing=false 时
// 连环形缓冲一并清除（主机不再可管）。
func (m *Manager) dropSubLocked(sub *hostSub, keepRing bool) {
	delete(m.subs, sub.deviceID)
	if !keepRing {
		delete(m.rings, sub.deviceID)
	}
	sub.pending = nil
}

// sendCancel 向一台主机下发取消帧（best-effort：发不出去只记 Debug —— 会话账目
// 的清理不依赖送达，本地状态必须立即有效）。
func (m *Manager) sendCancel(deviceID uint64, sessionID string) {
	if sessionID == "" {
		return
	}
	frame := &agentproto.CoreDockerFrame{SessionID: sessionID, Op: agentproto.DockerFrameOpCancel}
	if err := frame.Validate(); err != nil {
		return // 防御：会话句柄来自 agent 的 result，理论合法；不合法宁可不发
	}
	msg, err := agentproto.NewMessage(m.idGen(), agentproto.TypeCoreDockerFrame, frame)
	if err != nil {
		return
	}
	if err := m.sender.SendToDevice(deviceID, msg); err != nil && m.log != nil {
		m.log.Debug("docker events cancel 未送达",
			zap.Uint64("deviceId", deviceID), zap.String("session", sessionID), zap.Error(err))
	}
}

// kick / kickLocked 非阻塞地唤醒对账循环（引用计数变化、结果认领、eof 等都往这投）。
func (m *Manager) kick() {
	m.mu.Lock()
	m.kickLocked()
	m.mu.Unlock()
}

func (m *Manager) kickLocked() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) logWarn(msg string, fields ...zap.Field) {
	if m.log != nil {
		m.log.Warn(msg, fields...)
	}
}

func (m *Manager) logInfo(msg string, fields ...zap.Field) {
	if m.log != nil {
		m.log.Info(msg, fields...)
	}
}

// ── 客户端（fan-out 的消费端）────────────────────────────────────────────

// Client 是一个 console 客户端的待消费队列：回放进场，之后实时事件经广播追加。
// 队列满丢最旧并计数（与用户流注册表同纪律）。
type Client struct {
	m     *Manager
	depth int

	mu      sync.Mutex
	buf     []Event
	closed  bool
	dropped int
	warn    bool
	wake    chan struct{}
}

// Next 取下一条事件：队列空时等待（ctx 取消 / 有新事件 / 已关闭）。缓冲排空且已
// 关闭时返回 ErrClosed；ctx 取消返回 ctx.Err()（HTTP 层据此结束 NDJSON 响应）。
func (c *Client) Next(ctx context.Context) (Event, error) {
	for {
		c.mu.Lock()
		if len(c.buf) > 0 {
			e := c.buf[0]
			c.buf = c.buf[1:]
			c.mu.Unlock()
			return e, nil
		}
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return Event{}, ErrClosed
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-c.wake:
		}
	}
}

// Close 结束消费并**引用计数 -1**（幂等）：最后一个客户端退出时触发全量退订。
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.signal()

	m := c.m
	m.mu.Lock()
	delete(m.clients, c)
	last := len(m.clients) == 0
	m.mu.Unlock()
	if last {
		m.teardownAll("客户端全部断开")
	}
}

// pushLocked 追加一条事件（调用方**必须持 m.mu** —— 广播与回放都在那把锁下，
// 跨客户端的顺序因此一致；c.mu 只护队列本身）。满则丢最旧并计数。
func (c *Client) pushLocked(e Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if len(c.buf) >= c.depth {
		c.buf = c.buf[1:]
		c.dropped++
		if !c.warn {
			c.warn = true
			if c.m.log != nil {
				c.m.log.Warn("docker events 客户端队列溢出，丢最旧",
					zap.Int("dropped", c.dropped))
			}
		}
	}
	c.buf = append(c.buf, e)
	c.mu.Unlock()
	c.signal()
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
