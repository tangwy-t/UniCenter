package dockerops

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 流会话（spec §3.1.2 帧纪律的**生产者侧**）──────────────────────────────
//
// 为什么要有这一层：core 对每条入站帧计费，超 900 帧/分是 **CloseRateLimited(4006)
// 断整条连接**（指标、心跳、指令全部陪葬）——「被限流断连」是本设计不允许发生的故障。
// 故 agent 侧必须自己把帧率压住：合帧、会话预算、全局护栏三道闸，全部在这里。
//
// 会话的生命周期（三端一致，plan §0）：
//
//	建立：执行器 open 一个会话 → result 回 session_id（唯一出口是 StreamExecutor）
//	数据：生产者 write/writePTY → 泵按阈值合帧 → SendFrame
//	控制：Runtime.OnFrame → 本文件按 op 分派（input/resize/cancel）
//	结束：数据源自然终止 → eof=true 帧；cancel/空闲超时 → 直接释放槽位

// 帧整形常数：全部来自冻结契约（plan §0），改动需三方同步。
const (
	// logFrameInterval / logFrameBytes 是日志合帧的「先到先发」双阈值。
	logFrameInterval = 100 * time.Millisecond
	logFrameBytes    = 64 << 10
	// ptyFrameInterval / ptyFrameBytes 是 PTY 输出合帧的双阈值（交互感优先）。
	ptyFrameInterval = 50 * time.Millisecond
	ptyFrameBytes    = 4 << 10
	// sessionFramesPerSec 是单会话帧预算（15 帧/s：单会话最大也只占 900/60 的份额）。
	sessionFramesPerSec = 15
	// maxStreamSessions 是 agent 并发流会话上限（与 core 的登记上限同值）。
	maxStreamSessions = 3
	// streamIdleTimeout 是会话空闲超时（10 分钟无任何数据：日志源卡死、用户忘了关）。
	streamIdleTimeout = 10 * time.Minute

	// dockerFramesPerMin 是 docker 帧的**全局**预算（护栏）。core 的 900 里留 100
	// 给指标/心跳/指令结果 —— 绝不能用满。
	dockerFramesPerMin = 800
	// dockerBurstFrames 是全局令牌桶容量 = 1 秒的预算量。
	//
	// 为什么不是 800（整分钟容量）：桶容量就是「瞬时突发」的上限，容量 800 会让
	// 任意 60 秒窗口最多到 800(突发)+800(回填)=1600 帧 —— 恰好越过 core 的断连线。
	// 容量压到 60（秒级突发）后，任意 60 秒窗口最多 60+800=860 帧 < 900。
	dockerBurstFrames = 60
	// dockerScaleAt 是「逼近」的两个档：最近 60 秒帧数达到它就把合帧间隔 ×2 / ×4。
	dockerScaleAt    = dockerFramesPerMin * 4 / 5 // 640
	dockerScaleMaxAt = dockerFramesPerMin         // 800

	// ptyPendingBytes 是 PTY 未发送数据的字节上限：超过即让读循环阻塞（反压到 core），
	// **不丢**（终端丢一个字符就是错的输入）。
	//
	// 取值 60KB 是算出来的：上游每块 ≤ ptyFrameBytes(4KB)，故 pending 最大
	// 60KB + 4KB = 64KB = 协议单帧上限（MaxDockerFrameDataBytes）—— 缓冲上限与
	// 协议上限对齐后，正常路径永远不需要切帧。
	ptyPendingBytes = 60 << 10
	// statsFrameInterval / statsFrameBytes 是 stats 样本的整形参数。
	//
	// 间隔为 0 = **一个样本一帧、随到随发**：样本率 ≤1/s（daemon 的采样周期），
	// 合帧窗口只会把曲线人为拖慢 100ms，没有任何合帧收益；0 让泵在有数据时立刻
	// 冲刷。框架的 defence（会话在 800 帧/分护栏下会把间隔按倍率拉长）对 0 也成立
	// —— 0×N 仍是 0，样本节奏天然远低于任何预算。
	// 字节阈值给一个大数：样本永远撞不到它，也就不会被「超阈值切帧」把一个
	// JSON 行从半路截断（core 按行解析样本）。
	statsFrameInterval = 0
	statsFrameBytes    = 1 << 20
	// pullFrameInterval 是**拉取进度**的折叠窗口（4b）：生产者按这个宽度把 daemon 的
	// 高频进度行（每层每秒可达几十条 current 抖动）合并成一批再写会话，会话泵保持
	// 间隔 0 随到随发。
	//
	// 为什么是 200ms：折叠后帧率 ≤5/s —— 乘上 15 分钟的拉取上限也只占全局
	// 800 帧/分预算的一半（5×60×15 = 4500 < 12000），进度条 200ms 的刷新节奏对
	// 视觉足够顺滑；再宽（如 1s）看不出收益而体感变钝。节流放在生产者而不是泵：
	// 泵只管「有数据就发」，生产者才知道「哪几行是同一个状态的连续抖动，可以只留
	// 最后一行」（合并是**语义**，不是整形）。
	pullFrameInterval = 200 * time.Millisecond
	// pullFrameBytes 是折叠缓冲的**顺手冲刷**阈值：折叠批 ≤32KB 时即使窗口没到也
	// 立即发走 —— 保证单帧远低于协议 64KB 上限，一行 JSON 永远不会被 pump 的
	// 防御性切帧拦腰截断（core 按行解析，半行 = 丢一行）。
	//
	// 正常路径到不了它：一个窗口里的层数 × 一行 JSON（约 100 层 × 120B ≈ 12KB）；
	// 它兜的是「一台主机数百层 + 消息行」的极端镜像 —— 那种情况下半行 JSON 才是
	// 真正的损失。
	pullFrameBytes = 32 << 10
	// eventsFrameInterval / eventsFrameBytes 是事件记录的整形参数，**与 stats 同值**：
	// 事件也是「一条 JSON 行一条记录、随到随发」—— 间隔 0 拿去合帧只会人为拖慢
	// 活动流；字节阈值大数保证一行 JSON 永远不会被切帧拦腰截断（core 按行解析）。
	// 事件是突发型数据（compose up 一秒钟可能几十条），超预算时与 stats 同策略
	// —— 等待而不是丢帧：事件是「刚才发生了什么」的事实，晚到比缺一行好。
	eventsFrameInterval = 0
	eventsFrameBytes    = 1 << 20
	// logPendingCapBytes 是日志待发送缓冲上限：超过即丢最旧。
	// 合帧阈值之上再留 4 倍余量；再大就说明生产端快过网络几个数量级，攒着只会吃内存。
	logPendingCapBytes = 4 * logFrameBytes
	// inputQueueCap / resizeQueueCap 是控制面的小队列（读循环只用非阻塞投递）。
	inputQueueCap  = 32
	resizeQueueCap = 1
)

// errStreamLimit 是并发会话达到上限时的**结论句**（页面直接显示它）。
var errStreamLimit = errors.New("同时进行的流会话已达上限（最多 3 个），请先关闭其它日志或终端")

// errEventsLimit 是事件订阅已存在时的结论（core 常驻管理器的退避重试据此收敛：
// 事件槽位每台设备至多一条，重复订阅只出现在「上一段连接的旧会话还没被收掉」的
// 竞态里 —— 拒绝 + 重试是对这个竞态的正确代价）。
var errEventsLimit = errors.New("事件流订阅已存在")

// streamKind 区分两类流：整形参数与「超预算怎么办」都按它分派。
type streamKind int

const (
	// streamLog 是日志流：合帧阈值大，超预算丢最旧并计数。
	streamLog streamKind = iota
	// streamPTY 是终端：合帧阈值小，超预算反压（不丢）。
	streamPTY
	// streamStats 是 stats 实时流：样本即帧（间隔 0），超预算等待（样本是
	//「曲线上的点」，晚到比缺一格好）。
	streamStats
	// streamEvents 是事件订阅（docker events，core 的常驻流）：与 stats 同一整形
	//（记录即帧、超预算等待），但**豁免空闲超时**且**不占用户流槽位** —— 平静主机
	// 几小时没有事件是常态，不是「被遗忘的终端」；它的寿命由 core 的退订 cancel
	// 与连接更替（OnReconnected 收摊）决定。
	streamEvents
	// streamPull 是拉取进度流（write 指令 image:pull 的进度透出，4b）：与 stats
	// 同一整形（生产者折叠成批、泵随到随发、超预算等待 —— 进度是「刚发生的事实」，
	// 晚到比丢一行好），且与 events 一样**豁免空闲超时、不占用户流槽位**：拉取
	// 停滞几分钟（registry 限速/排队）是常态不是泄漏；每台主机同一时刻至多一条
	//（派发器单 worker 串行执行），寿命由指令自身的 15 分钟时限 + cancel 决定，
	// 不需要「用户忘了关」的空闲清退。
	streamPull
	// streamBuild / streamPush 是构建/推送进度流（P2·分发面）：与 pull 同一整形
	//（生产者折叠成批、泵随到随发）、同一豁免（构建的 RUN 步骤静默几分钟是常态、
	// 推送停滞是传输事实）、同一槽位纪律（不占用户流槽位、派发器串行保证至多一条）。
	// 三个进度族各建各的 kind：日志口径与 String() 按 kind 区分，若共用会出现
	//「哪一场操作结束」查无实据的模糊。
	streamBuild
	streamPush
)

func (k streamKind) String() string {
	switch k {
	case streamPTY:
		return "pty"
	case streamStats:
		return "stats"
	case streamEvents:
		return "events"
	case streamPull:
		return "pull"
	case streamBuild:
		return "build"
	case streamPush:
		return "push"
	}
	return "log"
}

// progressLike 报告该 kind 是否属于进度三族（pull/build/push）：三族共用同一整形
// 与豁免（interval 0、豁免空闲、不占用户槽位），用单一谓词表达而不是把条件
// 越写越长。
func (k streamKind) progressLike() bool {
	return k == streamPull || k == streamBuild || k == streamPush
}

// interval / maxBytes 返回该种类的合帧窗口与合帧字节阈值（冻结值）。
func (k streamKind) interval() time.Duration {
	switch k {
	case streamPTY:
		return ptyFrameInterval
	case streamStats, streamEvents, streamPull, streamBuild, streamPush:
		return statsFrameInterval
	}
	return logFrameInterval
}

func (k streamKind) maxBytes() int {
	if k == streamPTY {
		return ptyFrameBytes
	}
	if k == streamStats || k == streamEvents || k == streamPull || k == streamBuild || k == streamPush {
		return statsFrameBytes
	}
	return logFrameBytes
}

// sessionConfig 是会话管理器的全部外部依赖（时钟可注入：帧整形与空闲超时都要能被
// 测试确定性地驱动，而不是靠 sleep 猜）。
type sessionConfig struct {
	send        func(*agentproto.DockerFrame) error
	log         Logger
	now         func() time.Time
	after       func(time.Duration) <-chan time.Time
	maxSessions int
	idleTimeout time.Duration
}

// SessionManager 管住全部流会话：上限、控制面分派、全局帧护栏。
type SessionManager struct {
	cfg      sessionConfig
	governor *frameGovernor

	mu       sync.Mutex
	sessions map[string]*streamSession
}

// newSessionManager 构造会话管理器（生产入口在 Runtime.New）。
func newSessionManager(cfg sessionConfig) *SessionManager {
	if cfg.log == nil {
		cfg.log = nopLogger{}
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.after == nil {
		cfg.after = time.After
	}
	if cfg.maxSessions <= 0 {
		cfg.maxSessions = maxStreamSessions
	}
	if cfg.idleTimeout <= 0 {
		cfg.idleTimeout = streamIdleTimeout
	}
	return &SessionManager{
		cfg:      cfg,
		governor: newFrameGovernor(cfg.now),
		sessions: map[string]*streamSession{},
	}
}

// count 返回当前占用的会话槽位（测试与日志用）。
func (m *SessionManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// open 建一个会话：分配 id、占槽位、起泵。parent 只用于**建立阶段**的取消传播，
// 会话本身活在独立的 ctx 上（见 streamSession.ctx 的说明）。
//
// 槽位满时返回 errStreamLimit 的措辞（exec/logs 的 result 用它当结论句）。
func (m *SessionManager) open(kind streamKind) (*streamSession, error) {
	// 基 ctx 刻意是 Background：派发器的执行 ctx 带 5 分钟超时，而流会话是长命的
	//（用户看日志/开终端），把它挂在指令超时上会让终端 5 分钟后无声断开。
	// 会话的终止由 cancel / 数据源 EOF / 空闲超时三条路径负责。
	return m.openWith(context.Background(), kind)
}

// openNamed 以**指定句柄**建会话（拉取进度专用）；普通流会话用 open（随机句柄）。
//
// 为什么拉取进度的句柄必须是指定而不是随机：core 在**受理指令时**就用协议派生的
// DockerPullSessionID(ref) 预登记了会话（那时 agent 还没收到指令，更没回 result ——
// 随机句柄只经 result 上行，对拉取这种「句柄先于执行存在」的形态不合用）。两端用
// 同一派生函数，帧才能落在登记好的会话上。随机句柄的保密理由（可枚举 = 可劫持，
// 见 newStreamSessionID）对派生句柄不成立：它的授权在**接入端**（记录发起人 + 权限
// 码双闸，且帧的 device 归属校验不认句柄本身），详见协议 DockerPullSessionID 的说明。
func (m *SessionManager) openNamed(kind streamKind, id string) (*streamSession, error) {
	if id == "" {
		return m.open(kind)
	}
	// 防御性校验：句柄要进会话路由 map，非法形态（对派生函数而言不该出现）在这里
	// 现形，而不是变成「帧永远送不到」的线上悬案。
	if !agentproto.IsDockerSessionID(id) {
		return nil, errors.New("非法的流会话句柄")
	}
	return m.openWithID(context.Background(), kind, id)
}

func (m *SessionManager) openWith(parent context.Context, kind streamKind) (*streamSession, error) {
	id, err := newStreamSessionID()
	if err != nil {
		return nil, err
	}
	return m.openWithID(parent, kind, id)
}

func (m *SessionManager) openWithID(parent context.Context, kind streamKind, id string) (*streamSession, error) {
	m.mu.Lock()
	if kind == streamEvents {
		// 事件订阅是 core 的**常驻流**：独立槽位（每台设备至多一条），不进 3 条用户
		// 槽位的账。混在同一个上限里会有两种粘连：用户开满 3 条流会把总览页的活动流
		// 挤出（静默断供），反过来常驻订阅占着槽位会把用户的终端挡在「已达上限」外
		// —— 两类资源的生命周期不同（用户流有惰性、常驻流有主人），上限也就该不同。
		for _, s := range m.sessions {
			if s.kind == streamEvents {
				m.mu.Unlock()
				return nil, errEventsLimit
			}
		}
	} else if !kind.progressLike() && len(m.sessions) >= m.cfg.maxSessions {
		// 进度三族（pull/build/push）也**不进用户槽位的账**：它们是一场已经受理的
		// 写操作的进度透出（用户没做「又开一条流」这个动作），开满日志/终端不该
		// 挡掉拉取/构建/推送；且派发器单 worker 串行保证同一时刻至多一条进度会话
		// 在跑（队列里的还没开会话），上限天然成立 = 1，不需要另设账目 ——
		// 让它们撞 3 条用户流的上限才是错的。
		m.mu.Unlock()
		return nil, errStreamLimit
	}
	ctx, cancel := context.WithCancel(parent)
	s := newStreamSession(m, id, kind, ctx, cancel)
	m.sessions[id] = s
	m.mu.Unlock()

	go s.pump()
	return s, nil
}

// lookup 按 id 找会话。
func (m *SessionManager) lookup(id string) *streamSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// remove 释放槽位（幂等：delete 不存在的键是空操作）。
func (m *SessionManager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// OnFrame 是控制面入口（Runtime 在读循环里调用，**必须非阻塞**）。
func (m *SessionManager) OnFrame(f *agentproto.CoreDockerFrame) {
	if f == nil {
		return
	}
	s := m.lookup(f.SessionID)
	if s == nil {
		// 会话已结束/未知：忽略并留痕（core 的 sweep 与 agent 的释放之间有窗口，
		// 用户点「取消」的帧晚到是正常时序，不是错误）。
		m.cfg.log.Debug("docker control frame for unknown session",
			"session", f.SessionID, "op", f.Op)
		return
	}
	s.onControl(f)
}

// OnReconnected 收掉上一段连接的**常驻**会话（events）：常驻订阅与**连接**同寿 ——
// 连接更换后，旧订阅的帧已经送不出去（也会被泵按发送失败判死），而它占着的 events
// 专属槽位会挡住 core 重连后的重订（"事件流订阅已存在"）。用户会话不动：它们的
// 生命周期自有 cancel / 空闲超时 / 发送失败三条路径，与连接更换无关。
//
// 由 Runtime.OnConnected 调用（握手完成时），**必须非阻塞**：teardown 不碰 socket
// 读循环（cancel 关闭的是本地资源与 ctx）。
func (m *SessionManager) OnReconnected() {
	m.mu.Lock()
	list := make([]*streamSession, 0, 2)
	for _, s := range m.sessions {
		if s.kind == streamEvents {
			list = append(list, s)
		}
	}
	m.mu.Unlock()
	for _, s := range list {
		s.cancelBy("connection replaced")
	}
}

// newStreamSessionID 生成会话 id：crypto/rand ≥16B，URL 安全字符。
//
// 为什么不用雪花（§10 第 5 条）：可预测即可枚举，而枚举 session id 等于劫持别人的
// 日志与终端。base64url 无填充正好落在协议 IsDockerSessionID 的字符集里（22 字符）。
func newStreamSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ── 单个会话 ────────────────────────────────────────────────────────────

// streamSession 是一条流会话：生产者往里写，泵按帧纪律往外发。
type streamSession struct {
	id   string
	kind streamKind
	mgr  *SessionManager

	ctx    context.Context
	cancel context.CancelFunc

	wake chan struct{} // 生产者 → 泵：有新数据/EOF（容量 1，合并）

	budget *tokenBucket // 每会话 15 帧/s

	mu      sync.Mutex
	pending []byte
	// drained 是 PTY 反压的广播通道：泵每冲刷一次就关闭并重建，写方据此知道
	//「有余量了」。用广播而不是每块一个槽位：切帧/丢弃后块数记账会失准，
	// 而字节数不会。
	drained   chan struct{}
	seq       uint64
	frames    int
	lastAct   time.Time
	closed    bool
	eofPend   bool
	wroteWarn bool

	droppedFrames uint64
	droppedBytes  uint64

	// exec 专属（attachExec 后可用）
	stdin     io.Writer
	resizeFn  func(context.Context, int, int) error
	closeFn   func() error
	inputCh   chan []byte
	resizeCh  chan struct{}
	resizeReq *termSize

	teardownOnce sync.Once
}

type termSize struct{ cols, rows int }

func newStreamSession(m *SessionManager, id string, kind streamKind, ctx context.Context, cancel context.CancelFunc) *streamSession {
	s := &streamSession{
		id: id, kind: kind, mgr: m, ctx: ctx, cancel: cancel,
		budget:  newTokenBucket(sessionFramesPerSec, sessionFramesPerSec, m.cfg.now()),
		wake:    make(chan struct{}, 1),
		lastAct: m.cfg.now(),
	}
	if kind == streamPTY {
		s.drained = make(chan struct{})
		s.inputCh = make(chan []byte, inputQueueCap)
		s.resizeCh = make(chan struct{}, resizeQueueCap)
	}
	return s
}

// ID 返回会话句柄（进 result.session_id）。
func (s *streamSession) ID() string { return s.id }

// write 是日志数据入口：**永不阻塞**。超缓冲上限丢最旧并计数 —— 日志是「看一眼」
// 的数据，宁可少几行也不能把 agent 的内存吃干。
func (s *streamSession) write(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, data...)
	if over := len(s.pending) - logPendingCapBytes; over > 0 {
		s.dropOldestLocked(over)
	}
	s.warnDropLocked()
	s.mu.Unlock()
	s.signal()
}

// writePTY 是终端输出入口：**会阻塞**（反压）。
//
// 反压链：泵来不及发 → 缓冲满 → 本方法等待 → exec 的读循环停读 → hijacked 连接的
// 内核缓冲满 → 容器侧写阻塞。这正是「PTY 不丢」的实现方式：宁让终端慢下来，
// 也不丢用户的输入回显。
func (s *streamSession) writePTY(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return false
		}
		if s.drained == nil { // 非 PTY 会话误用：退化成日志语义
			s.pending = append(s.pending, data...)
			if over := len(s.pending) - logPendingCapBytes; over > 0 {
				s.dropOldestLocked(over)
			}
			s.warnDropLocked()
			s.mu.Unlock()
			s.signal()
			return true
		}
		if len(s.pending) < ptyPendingBytes {
			s.pending = append(s.pending, data...)
			s.mu.Unlock()
			s.signal()
			return true
		}
		// 缓冲已满：等泵冲刷（每次冲刷会关闭并重建 drained 做广播）。
		drained := s.drained
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return false
		case <-drained:
		}
	}
}

// markEOF 标记数据源自然结束：泵会把残留数据与 eof=true 同帧发出后收摊
// （契约允许「可与末帧数据同帧」）。
func (s *streamSession) markEOF() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.eofPend = true
	s.mu.Unlock()
	s.signal()
}

// attachExec 注入 exec 的控制面（stdin / resize / 关闭）并起输入与 resize 的消费协程。
func (s *streamSession) attachExec(es *ExecSession) {
	s.mu.Lock()
	s.stdin = es.Writer
	s.resizeFn = es.Resize
	s.closeFn = es.Close
	s.mu.Unlock()
	go s.inputLoop()
	go s.resizeLoop()
}

// attachUpstream 注入上游关闭函数（日志流用：取消会话时关闭它，读循环随之退出）。
func (s *streamSession) attachUpstream(close func() error) {
	s.mu.Lock()
	s.closeFn = close
	s.mu.Unlock()
}

// closeUpstream 关闭上游连接（日志流 / hijacked 连接），幂等。
func (s *streamSession) closeUpstream() {
	s.mu.Lock()
	fn := s.closeFn
	s.closeFn = nil
	s.mu.Unlock()
	if fn != nil {
		if err := fn(); err != nil {
			s.mgr.cfg.log.Debug("docker stream upstream close failed", "session", s.id, "err", err.Error())
		}
	}
}

// onControl 处理一条控制帧。**非阻塞**：input/resize 只投队列，实际动作在会话自己的
// 协程里做（读循环一秒都不能停）。
func (s *streamSession) onControl(f *agentproto.CoreDockerFrame) {
	s.touch()
	switch f.Op {
	case agentproto.DockerFrameOpInput:
		if s.kind != streamPTY {
			return
		}
		select {
		case s.inputCh <- f.Data:
		default:
			// 输入队列满：终端里连打几十秒不松手、而进程完全没读 stdin 才会到这。
			// 丢这一块并留痕（阻塞读循环是更糟的选项）。
			s.mgr.cfg.log.Warn("docker pty input dropped (queue full)", "session", s.id)
		}
	case agentproto.DockerFrameOpResize:
		if s.kind != streamPTY {
			return
		}
		s.mu.Lock()
		s.resizeReq = &termSize{cols: f.Cols, rows: f.Rows}
		s.mu.Unlock()
		select {
		case s.resizeCh <- struct{}{}:
		default: // 已有待处理信号：新尺寸已覆盖旧尺寸，足够
		}
	case agentproto.DockerFrameOpCancel:
		s.cancelBy("client")
	}
}

// cancelBy 结束会话并**立刻释放槽位**（不等泵退出）。
//
// 为什么立刻：用户看到「已断开」时槽位就该空出来；等泵从限速等待里醒来再删，
// 会让「取消后马上开新终端」撞上「会话已达上限」的假错误。
func (s *streamSession) cancelBy(reason string) {
	s.mgr.cfg.log.Debug("docker stream session cancelled", "session", s.id, "kind", s.kind.String(), "reason", reason)
	s.teardown()
}

// teardown 收摊（幂等）：标关闭、取消 ctx（生产者退出）、关上游、释放槽位。
func (s *streamSession) teardown() {
	s.teardownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		dropF, dropB, frames := s.droppedFrames, s.droppedBytes, s.frames
		s.mu.Unlock()
		s.cancel()
		s.closeUpstream()
		s.mgr.remove(s.id)
		if dropF > 0 || dropB > 0 {
			s.mgr.cfg.log.Warn("docker stream session ended with drops",
				"session", s.id, "kind", s.kind.String(),
				"droppedFrames", dropF, "droppedBytes", dropB)
		} else {
			s.mgr.cfg.log.Debug("docker stream session ended",
				"session", s.id, "kind", s.kind.String(), "frames", frames)
		}
	})
}

// isClosing 报告会话是否已收摊/取消（生产者在读循环报错时用它区分
// 「用户取消导致的读错误」与「真故障」，前者不该刷 Warn）。
func (s *streamSession) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// signal 非阻塞地叫醒泵。
func (s *streamSession) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// touch 记录活动时刻（空闲超时以「有没有数据来往」为准，不以「有没有帧」为准：
// 一帧都没发过的会话同样会超时）。
func (s *streamSession) touch() {
	s.mu.Lock()
	s.lastAct = s.mgr.cfg.now()
	s.mu.Unlock()
}

// idleIn 返回距空闲超时还有多久（≤0 表示已超时）。
func (s *streamSession) idleIn() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mgr.cfg.idleTimeout - s.mgr.cfg.now().Sub(s.lastAct)
}

// dropOldestLocked 丢最旧的数据并计数（只用于日志会话）。
func (s *streamSession) dropOldestLocked(n int) {
	if n <= 0 || len(s.pending) == 0 {
		return
	}
	if n > len(s.pending) {
		n = len(s.pending)
	}
	rest := copy(s.pending, s.pending[n:])
	s.pending = s.pending[:rest]
	s.droppedFrames++
	s.droppedBytes += uint64(n)
}

// warnDropLocked 首次丢弃时记一条 Warn（否则「少了几行日志」会查无实据）。
func (s *streamSession) warnDropLocked() {
	if s.droppedFrames == 0 || s.wroteWarn {
		return
	}
	s.wroteWarn = true
	s.mgr.cfg.log.Warn("docker log frames dropped (over budget or buffer cap)",
		"session", s.id, "droppedFrames", s.droppedFrames, "droppedBytes", s.droppedBytes)
}

// ── 泵：合帧 + 限速 + 发送 ──────────────────────────────────────────────

// pump 是会话的主循环：按「先到先发」的双阈值合帧，受限速拖慢，按 eof 收摊。
func (s *streamSession) pump() {
	defer s.teardown()

	window := (<-chan time.Time)(nil)
	for {
		s.mu.Lock()
		closed := s.closed
		pending := len(s.pending)
		eof := s.eofPend
		s.mu.Unlock()
		if closed {
			return
		}

		// 没有可发的东西：等生产者，或到点判空闲。
		if pending == 0 && !eof {
			if s.kind == streamEvents || s.kind.progressLike() {
				// 常驻订阅与进度三族**豁免空闲超时**：「无事件」是平静主机的常态、
				//「拉取停滞」（registry 限速/排队/大层静默下载）与「构建的 RUN 步骤
				// 静默几分钟」是长耗时写操作的常态、推送停滞是传输事实 —— 都不是
				//「被遗忘的终端」—— 用户流 10 分钟清退的理由（卡死的日志源、
				// 忘了关的终端）对它们不成立。寿命走另外两条路：core 的 cancel（三族
				// 各自还有最后一条路：指令自身的 15/30 分钟时限）、以及发送失败被判死。
				select {
				case <-s.ctx.Done():
					return
				case <-s.wake:
				}
				continue
			}
			d := s.idleIn()
			if d <= 0 {
				// 空闲超时：发一帧 eof 收尾（消费端据此收流），随后循环自然退出。
				s.mgr.cfg.log.Info("docker stream session idle timeout", "session", s.id, "kind", s.kind.String())
				s.markEOF()
				continue
			}
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
			case <-s.mgr.cfg.after(d):
			}
			continue
		}

		// 有数据/EOF：够阈值或已 eof 就立刻发（先到先发），否则等合帧窗口。
		if !eof && pending < s.kind.maxBytes() {
			if window == nil {
				window = s.mgr.cfg.after(s.mgr.frameInterval(s.kind))
			}
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
				continue // 数据变多：回循环头重判阈值
			case <-window:
				window = nil
			}
		}

		// 限速：会话预算与全局护栏。日志超预算丢最旧，PTY 等待（反压）。
		if wait, ok := s.reserve(); !ok {
			if s.kind == streamLog {
				s.mu.Lock()
				s.dropOldestLocked(len(s.pending))
				s.warnDropLocked()
				s.mu.Unlock()
			}
			window = nil
			select {
			case <-s.ctx.Done():
				return
			case <-s.mgr.cfg.after(wait):
			}
			continue
		}
		if !s.flush() {
			return
		}
		window = nil
	}
}

// frameInterval 返回该会话当前的合帧间隔（全局护栏逼近预算时按倍率拉长）。
func (m *SessionManager) frameInterval(kind streamKind) time.Duration {
	scale := m.governor.scale()
	base := kind.interval()
	switch {
	case scale >= 4:
		return base * 4
	case scale >= 2:
		return base * 2
	default:
		return base
	}
}

// reserve 申请一帧的额度：先会话预算（15/s），再全局护栏（800/min）。
// ok=false 时返回值是建议的等待时长。
func (s *streamSession) reserve() (time.Duration, bool) {
	now := s.mgr.cfg.now()
	if wait, ok := s.budget.acquire(now); !ok {
		return wait, false
	}
	if wait, ok := s.mgr.governor.acquire(); !ok {
		s.budget.refund(now)
		return wait, false
	}
	return 0, true
}

// flush 把残留数据（以及必要时 eof）发成一帧。返回 false 表示会话该收摊了
// （发送通道失败：连接已不健康，继续攒数据没有意义）。
func (s *streamSession) flush() bool {
	now := s.mgr.cfg.now()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	data := s.pending
	eof := s.eofPend
	s.pending = nil
	// 防御性切帧：单帧必须 ≤ 协议上限（MaxDockerFrameDataBytes）。正常路径到不了这里
	// （PTY 缓冲 60KB+4KB、日志按 64KB 阈值冲刷），但「缓冲上限未来被放宽」不该变成
	// 「整帧被 core 拒掉、用户少一段日志」。切出来的余量留在 pending 里下一帧发。
	if len(data) > agentproto.MaxDockerFrameDataBytes {
		rest := append([]byte(nil), data[agentproto.MaxDockerFrameDataBytes:]...)
		s.pending = rest
		data = data[:agentproto.MaxDockerFrameDataBytes]
		// eof 必须跟着**最后一帧**走（否则消费端会以为流已经结束）。
		eof = false
	}
	s.seq++
	s.frames++
	s.lastAct = now
	if eof {
		s.eofPend = false
	}
	frame := &agentproto.DockerFrame{SessionID: s.id, Seq: s.seq, Data: data, EOF: eof}
	// 广播「缓冲有余量了」：PTY 的写方据此解除反压（数据已经脱离 pending）。
	if s.drained != nil {
		close(s.drained)
		s.drained = make(chan struct{})
	}
	s.mu.Unlock()

	// 协议校验是最后一道闸：发出去的每一帧都必须过 IsDockerSessionID / seq / 尺寸
	//（core 侧同样会校验，但被 core 拒掉的帧在这里是「我们发了非法帧」，
	// 应该在本进程就现形，而不是变成 core 日志里的一条 Warn）。
	if err := frame.Validate(); err != nil {
		s.mgr.cfg.log.Warn("refusing to send invalid docker frame", "session", s.id, "seq", frame.Seq, "err", err.Error())
		return false
	}

	if err := s.mgr.cfg.send(frame); err != nil {
		s.mgr.cfg.log.Warn("docker frame send failed", "session", s.id, "seq", frame.Seq, "err", err.Error())
		return false
	}
	s.mgr.governor.record()
	if eof {
		return false // eof 已发：会话收摊
	}
	return true
}

// ── 限速零件 ────────────────────────────────────────────────────────────

// tokenBucket 是「每秒 N 帧」的令牌桶（会话预算与全局护栏共用同一实现）。
//
// 为什么不用固定窗口计数：固定窗口在边界上会放两倍量（窗口切换瞬间的那一批），
// 而这里的上限是「绝不能被 core 断连」的硬线，边界抖动不能忍。
type tokenBucket struct {
	perSec   float64
	capacity float64
	tokens   float64
	last     time.Time
}

func newTokenBucket(perSec, capacity float64, last time.Time) *tokenBucket {
	return &tokenBucket{perSec: perSec, capacity: capacity, tokens: capacity, last: last}
}

// minTokenWait 是令牌不足时的最小等待：回填与消耗的差可以是亚纳秒，直接截断成 0
// 会让泵空转（busy loop）。1ms 对帧率没有影响。
const minTokenWait = time.Millisecond

func (b *tokenBucket) refill(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.tokens += elapsed * b.perSec
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// acquire 取一个令牌；不足时返回需要等待的时长（不取）。
func (b *tokenBucket) acquire(now time.Time) (time.Duration, bool) {
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	wait := time.Duration((1 - b.tokens) / b.perSec * float64(time.Second))
	if wait < minTokenWait {
		wait = minTokenWait
	}
	return wait, false
}

// refund 归还一个令牌（全局护栏拒绝时把会话预算的那一个退回去）。
func (b *tokenBucket) refund(now time.Time) {
	b.refill(now)
	b.tokens++
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// frameGovernor 是**全局**护栏：docker 帧独立计数，逼近 800 帧/分时拉长合帧间隔。
//
// 两个量各司其职：
//   - 令牌桶（容量 60、回填 800/min）是**硬闸**：它保证任意 60 秒窗口 ≤860 帧；
//   - 最近 60 秒的滑动计数（按秒分桶）是**软闸**：逼近 640/800 时把合帧间隔 ×2/×4，
//     让降速发生在越线之前（软闸先动，硬闸兜底）。
//
// 它是**多会话共享**的：每个会话的泵都在各自 goroutine 里记账，故全部入口都要加锁
// （帧纪律的账必须是全局一个数，分片记账会各算各的、护栏形同虚设）。
type frameGovernor struct {
	mu      sync.Mutex
	now     func() time.Time
	perMin  float64
	bucket  *tokenBucket
	seconds [60]int
	slot    int64
	head    int
	initial bool
}

func newFrameGovernor(now func() time.Time) *frameGovernor {
	return &frameGovernor{
		now:     now,
		perMin:  dockerFramesPerMin,
		bucket:  newTokenBucket(dockerFramesPerMin/60, dockerBurstFrames, now()),
		initial: true,
		slot:    now().Unix(),
	}
}

// advanceLocked 把每秒分桶滚到 now（跳过的秒清零）。调用方必须持锁。
func (g *frameGovernor) advanceLocked(now time.Time) {
	sec := now.Unix()
	if g.initial {
		g.slot = sec
		g.initial = false
	}
	if sec == g.slot {
		return
	}
	steps := sec - g.slot
	if steps > int64(len(g.seconds)) {
		steps = int64(len(g.seconds))
	}
	for i := int64(0); i < steps; i++ {
		g.head = (g.head + 1) % len(g.seconds)
		g.seconds[g.head] = 0
	}
	g.slot = sec
}

// record 记一帧发出（同时消耗全局令牌）。
func (g *frameGovernor) record() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.advanceLocked(now)
	g.bucket.refill(now)
	if g.bucket.tokens >= 1 {
		g.bucket.tokens--
	}
	g.seconds[g.head]++
}

// acquire 取全局额度；不足时返回需等待的时长。
func (g *frameGovernor) acquire() (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bucket.acquire(g.now())
}

// recent 返回最近 60 秒的 docker 帧数（软闸依据）。
func (g *frameGovernor) recent() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advanceLocked(g.now())
	total := 0
	for _, n := range g.seconds {
		total += n
	}
	return total
}

// scale 返回软闸倍率（1 / 2 / 4）。
func (g *frameGovernor) scale() int {
	switch recent := g.recent(); {
	case recent >= dockerScaleMaxAt:
		return 4
	case recent >= dockerScaleAt:
		return 2
	default:
		return 1
	}
}

// ── 控制面的两个消费协程（PTY 专属）─────────────────────────────────────

// inputLoop 把终端输入逐块写进 exec 的 stdin。
func (s *streamSession) inputLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case b := <-s.inputCh:
			s.mu.Lock()
			w := s.stdin
			s.mu.Unlock()
			if w == nil {
				continue
			}
			if _, err := w.Write(b); err != nil {
				s.mgr.cfg.log.Debug("docker pty stdin write failed", "session", s.id, "err", err.Error())
				s.teardown()
				return
			}
		}
	}
}

// resizeLoop 消费 resize 请求（只保留最新尺寸：中间态没有上报价值）。
func (s *streamSession) resizeLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.resizeCh:
			s.mu.Lock()
			r := s.resizeReq
			s.resizeReq = nil
			fn := s.resizeFn
			s.mu.Unlock()
			if r == nil || fn == nil {
				continue
			}
			if err := fn(s.ctx, r.cols, r.rows); err != nil {
				s.mgr.cfg.log.Debug("docker pty resize failed",
					"session", s.id, "cols", r.cols, "rows", r.rows, "err", err.Error())
			}
		}
	}
}
