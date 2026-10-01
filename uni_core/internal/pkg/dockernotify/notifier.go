// Package dockernotify 是 **docker 事件 → system-notice 通知联动器**（七期·P2：
// 从「看」到「被通知」—— 容器异常退出等关键事件主动推给运维，而不是等人巡检）。
//
// 它不新建任何通道、不另拉事件流：以**常驻内部消费者**的身份登记进 dockerevents
// 管理器（见 Manager.RegisterResident —— 内部消费者不计入 HTTP 面的归零判据，
// /docker/events 连接全断开时订阅保持常开），帧路径上同步收到的是与活动流同一条
// 扇出链的带归属事件。联动器在收到的那一刻只做**非阻塞入队**（读循环纪律），
// 规则过滤、节流合并、通知落库全部在自己的工作协程（Run）里完成 —— 绝不把
// Redis/DB IO 带回帧路径。
//
// 两条防线保证「flapping 容器不许刷屏」：
//   - **规则最小集**：只报两种异常（die 且 exit ≠ 0；health_status: unhealthy）。
//     start/stop/destroy 等常规运维动作是「自己人干的活」，一律不响 —— 报它们
//     等于用通知轰炸操作自己的手；
//   - **窗口节流**：同主机、同容器、同类事件在 N 分钟窗口内合并成一条通知，
//     首条**立即**发出（第一现场不迟到），窗口内的重复静默计数；跨窗仍发作才补发
//     一条「仍在持续」的汇总（含累计次数）。窗口长度与开关全部走 sys.docker.notify.*
//     热配置（与 sys.docker.snapshotInterval 同一条既有配置链路，改完即生效，不重启）。
//
// 退出口径（为什么这些选择是对的，见各常量/方法的就地注释）：
//   - die 事件的 exit_code 是**唯一事实源**：nil（旧 agent/未上报）不告警也不反推 ——
//     宁可少报一条，不把正常的 docker stop（exit 0 收尾）误报成事故；
//   - 通知失败只记日志不重试：事件面（活动流）本身就是可回看的账，通知是增值提醒；
//   - 节流账本在内存里：与环形缓冲同寿命（core 重启即清），多实例/重启不共享账本
//     是已知妥协 —— 代价是重启后首条会重报，收益是无任何外部状态依赖。
package dockernotify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerevents"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 配置键（sys.docker.notify.*，走既有热配置链路）──────────────────────
//
// 默认**全开**。为什么默认开而不是关：这套通知的规则面天然窄（只报异常、不报
// 常规动作），开着的代价是「真故障时有人被吵醒」，关着的代价是「P2 命题失效、
// 一键回到等人巡检」—— 通知模块的存在意义就是默认在场。可关总比可开重要：
// 受不了噪音的部署关总开关即可。
const (
	// ConfigNotifyEnabled 总开关：false 时联动器完全静默（连节流账本都清空）。
	ConfigNotifyEnabled = "sys.docker.notify.enabled"
	// ConfigNotifyDie die 规则开关（异常退出：exit ≠ 0）。
	ConfigNotifyDie = "sys.docker.notify.die"
	// ConfigNotifyHealth health 规则开关（health_status: unhealthy）。
	ConfigNotifyHealth = "sys.docker.notify.health"
	// ConfigNotifyWindowMinutes 节流窗口（分钟）。同主机同容器同类事件的去抖窗：
	// 首条立即报，窗口内重复只计数，跨窗仍发作才补「仍在持续」汇总。
	// 默认 10 分钟 —— 与告警系统通用的去抖窗同档：低于 1 分钟退化成「每条都报」，
	// 高于一天则汇总失去「事故还在当下」的时效。
	ConfigNotifyWindowMinutes = "sys.docker.notify.windowMinutes"
)

const (
	defaultNotifyWindowMinutes = 10
	minNotifyWindowMinutes     = 1
	maxNotifyWindowMinutes     = 1440
	// defaultQueueDepth 是联动器的入队上限（与管理器客户端队列同纪律）：帧路径上
	// 的 DeliverDockerEvent 只允许非阻塞 push，满了丢最旧并计数 —— 丢一条节流
	// 输入好过堵住 agent 读循环（那会连累指标/心跳）。
	defaultQueueDepth = 256
	// defaultPublishTimeout 是单条通知（配置读 + 落库 + 发布推送）的用时上限。
	// 通知是增值面：DB 抖动时卡住工作协程没有意义，超时即放弃并留痕。
	defaultPublishTimeout = 10 * time.Second
)

// ── 消费方窄接口（接口定义在消费方）──────────────────────────────────────

// ConfigGetter 读取热配置（由 service.ConfigService 满足；与 AgentConfigGetter
// 同款两方法，本包只声明自己用得上的）。
type ConfigGetter interface {
	GetBool(ctx context.Context, key string, defaultVal bool) bool
	GetInt(ctx context.Context, key string, defaultVal int) int
}

// Alert 是一条待发布的通知（纯数据：与 system-notice 的 DTO 解耦 —— 本包不
// 认识 CreateNoticeReq；受众/优先级由 wireup 的适配器决定）。
type Alert struct {
	Title   string
	Content string
}

// NoticeSink 是通知的发布面（由 wireup 的适配器满足：调 notice 服务层的
// Create + Publish，**不是** HTTP 自调 —— 联动器是服务内部组件）。
type NoticeSink interface {
	PublishAlert(ctx context.Context, alert Alert) error
}

// ── 规则与节流的状态 ─────────────────────────────────────────────────────

// eventClass 是「可通知事件」的类别（节流键的第三元 —— 同一容器 die 与 health
// 互不合并：两者是不同的事故，作业面上要分开看）。
type eventClass uint8

const (
	classNone      eventClass = iota
	classDie                  // 异常退出（exit ≠ 0 的 die）
	classUnhealthy            // health_status: unhealthy
)

// classify 判定一条事件是否值得通知。
//
// 为什么 die 只看 exit ≠ 0 且 **nil 不报**：正常 docker stop 的收尾就是一条
// exit 0 的 die —— 没有退出码可考时无法与它区分，报了就违背「自己人干的活不吵
// 自己」。旧 agent（不传 exit_code）在升级完成前的表现是 die 规则静默，而不是
// 把每次发布都广播成事故 —— 两害相权，静默是更诚实的一侧。
func classify(it *agentproto.DockerEventItem) eventClass {
	if it.Type != agentproto.DockerEventTypeContainer {
		return classNone
	}
	token := it.Action
	if i := strings.Index(it.Action, ": "); i >= 0 {
		token = it.Action[:i]
	}
	switch token {
	case "die":
		if it.ExitCode == nil || *it.ExitCode == 0 {
			return classNone
		}
		return classDie
	default:
		if it.Action == "health_status: unhealthy" {
			return classUnhealthy
		}
	}
	return classNone
}

// eventKey 是节流账本的键：同主机 + 同容器 + 同类。容器标识取 ActorID 优先
// （短 id 是 daemon 给的稳定身份，重命名不动它），拿不到 ID 才退回名字。
type eventKey struct {
	deviceID uint64
	actor    string
	class    eventClass
}

func keyOf(e dockerevents.Event, cls eventClass) eventKey {
	actor := e.Item.ActorID
	if actor == "" {
		actor = e.Item.ActorName
	}
	return eventKey{deviceID: e.DeviceID, actor: actor, class: cls}
}

// windowState 是一个键的窗口账目。win 是当前窗口序号（UnixNano / 窗口时长）：
// silent 是本窗口内被吞掉（上一条通知之后）的事件数；total 是**发作以来该键的
// 全部事件数**（乐观计数，发布失败也计入 —— 好处是失败期间每个窗口仍至多一次
// 发布尝试，不会把 DB 抖动期间的每次 flap 都变成一次发布重试；「累计」口径因此
// 是事实次数，而不是「成功通知过的次数」）。
type windowState struct {
	win    int64
	silent int
	total  int
}

// ── 联动器 ────────────────────────────────────────────────────────────────

// Options 是可注入参数（测试要确定性时钟与可压小的队列）。
type Options struct {
	QueueDepth int
	Now        func() time.Time
}

func (o Options) withDefaults() Options {
	if o.QueueDepth <= 0 {
		o.QueueDepth = defaultQueueDepth
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Notifier 是通知联动器：常驻内部消费者（实现 dockerevents.ResidentSink）+ 自己的
// 工作协程。构造成本为零副作用 —— 让它"常驻"的是 wireup 的 RegisterResident。
//
// 编译期断言见 Notifier 下方的 var _（与 wireup 的适配器断言同一纪律）。
type Notifier struct {
	cfg  ConfigGetter
	sink NoticeSink
	log  logger.LoggerInterface
	opts Options

	queue chan dockerevents.Event

	mu          sync.Mutex
	states      map[eventKey]*windowState
	lastSweep   int64
	warnDropped atomic.Bool
	dropped     atomic.Uint64
}

// NewNotifier 构造联动器（缺省参数见 Options.withDefaults）。
func NewNotifier(opts Options, cfg ConfigGetter, sink NoticeSink, log logger.LoggerInterface) *Notifier {
	if sink == nil {
		sink = discardSink{} // 未装配 sink：静默丢弃而不是 nil panic（规则/节流逻辑照常可测）
	}
	opts = opts.withDefaults()
	return &Notifier{
		cfg:    cfg,
		sink:   sink,
		log:    log,
		opts:   opts,
		queue:  make(chan dockerevents.Event, opts.QueueDepth),
		states: map[eventKey]*windowState{},
	}
}

// discardSink 是 NoticeSink 的空实现（未装配/测试未注入时的降级）。
type discardSink struct{}

func (discardSink) PublishAlert(context.Context, Alert) error { return nil }

// DeliverDockerEvent 实现 dockerevents.ResidentSink：**非阻塞**入队（读循环
// 纪律 —— 管理器要求 ResidentSink 绝不阻塞）。队列满丢这条并计数；真正的处理在
// Run 的工作协程里。
func (n *Notifier) DeliverDockerEvent(e dockerevents.Event) {
	select {
	case n.queue <- e:
	default:
		n.dropped.Add(1)
		if n.warnDropped.CompareAndSwap(false, true) && n.log != nil {
			n.log.Warn("docker 通知联动队列溢出，丢弃事件（只丢联动输入，不影响活动流）")
		}
	}
}

// Dropped 返回因队列满被丢弃的事件数（测试/健康自检用）。
func (n *Notifier) Dropped() uint64 { return n.dropped.Load() }

// 编译期断言：Notifier 满足 dockerevents 的常驻内部消费者接口。
var _ dockerevents.ResidentSink = (*Notifier)(nil)

// Run 是联动器的工作协程（wireup 起 goroutine，形状与 dockerevents 管理器同款）：
// 单一收口 drain 队列，规则/配置读/通知 IO 全部在这里 —— 锁只护账本，不护 IO。
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-n.queue:
			n.handle(ctx, e)
		}
	}
}

// handle 处理一条事件：分类 → 开关/窗口配置 → 节流裁决 → （值得报才）发布。
func (n *Notifier) handle(ctx context.Context, e dockerevents.Event) {
	cls := classify(&e.Item)
	if cls == classNone {
		return // 常规事件不吵：连配置读都省下（事件面本来就很热闹）
	}
	key := keyOf(e, cls)

	// 开关（热配置）：关总开关或关所属规则，联动静默并把账本记清 —— 清账本的
	// 理由：重新打开时**从新事件开始新一轮**，而不是把关闭期间的积压当新发作
	// 补报（那条「仍在持续」对没收到过第一条的人没有意义）。
	if !n.cfg.GetBool(ctx, ConfigNotifyEnabled, true) {
		n.clearState(key)
		return
	}
	switch cls {
	case classDie:
		if !n.cfg.GetBool(ctx, ConfigNotifyDie, true) {
			n.clearState(key)
			return
		}
	case classUnhealthy:
		if !n.cfg.GetBool(ctx, ConfigNotifyHealth, true) {
			n.clearState(key)
			return
		}
	}

	winMinutes := n.cfg.GetInt(ctx, ConfigNotifyWindowMinutes, defaultNotifyWindowMinutes)
	if winMinutes < minNotifyWindowMinutes || winMinutes > maxNotifyWindowMinutes {
		if n.log != nil {
			n.log.Warn("docker 通知窗口配置非法，回退默认",
				zap.Int("value", winMinutes), zap.Int("default", defaultNotifyWindowMinutes))
		}
		winMinutes = defaultNotifyWindowMinutes
	}

	now := n.opts.Now()
	alert, ok := n.admit(key, e, cls, now, time.Duration(winMinutes)*time.Minute)
	if !ok {
		return // 落在节流窗口内：静默计数，不打扰
	}

	dctx, cancel := context.WithTimeout(ctx, defaultPublishTimeout)
	defer cancel()
	if err := n.sink.PublishAlert(dctx, alert); err != nil && n.log != nil {
		n.log.Warn("docker 通知发布失败",
			zap.String("title", alert.Title), zap.Error(err))
	}
}

// admit 是节流裁决（调用方已过规则与开关）：返回值得发布的 Alert；false = 已合并
// 进窗口账目。
//
// 窗口机器（为什么这样设计）：
//   - 首条**立即**发布 —— 第一现场不能等一次窗口聚合（10 分钟后的「你的容器炸了」
//     没有意义）；
//   - 同窗口的重复只累加 silent，零 IO；
//   - 跨进**相邻**窗口且上窗仍有积压：补一条「仍在持续」汇总（含「又发生 N 次」
//     与「累计 M 次」），每个键每窗口至多一条通知 —— flapping 的容器刷不了屏，
//     但持续发作不会被无声吞掉。
//   - 间隔了**一个完整空窗**再出现：上一轮发作已收束，账本重置、按又一次首报起算
//     （未上报的积压随发作收束作废 —— 补一条「已停止」的通知没有作业价值）。
func (n *Notifier) admit(key eventKey, e dockerevents.Event, cls eventClass,
	now time.Time, window time.Duration) (Alert, bool) {
	w := now.UnixNano() / int64(window)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sweepLocked(w)

	st := n.states[key]
	if st == nil {
		n.states[key] = &windowState{win: w, total: 1}
		return buildAlert(cls, e, now, ""), true
	}
	if w == st.win {
		st.silent++
		return Alert{}, false
	}
	if w > st.win+1 {
		// 至少隔了一个完整空窗：发作收束，重新起账。
		n.states[key] = &windowState{win: w, total: 1}
		return buildAlert(cls, e, now, ""), true
	}
	count := st.silent + 1 // 上窗积压 + 跨窗这一条
	st.total += count
	st.silent = 0
	st.win = w
	return buildAlert(cls, e, now, fmt.Sprintf("自上一条同故障通知以来又发生 %d 次（累计 %d 次）", count, st.total)), true
}

// sweepLocked 每个窗口至多跑一次：清掉「超过一个完整窗口没动静」的死键 —— 容器
// 随部署换代、ID 常新，账本不留这些人的墓碑（也是内存有界的原因：键集的稳态
// 大小 ≈ 最近两个窗口内发作过的容器数，与历史时长无关）。
func (n *Notifier) sweepLocked(w int64) {
	if w == n.lastSweep {
		return
	}
	n.lastSweep = w
	for k, st := range n.states {
		if st.win < w-1 {
			delete(n.states, k)
		}
	}
}

// clearState 清除一个键的账目（开关关闭时用）。
func (n *Notifier) clearState(key eventKey) {
	n.mu.Lock()
	delete(n.states, key)
	n.mu.Unlock()
}

// ── 通知文案 ──────────────────────────────────────────────────────────────

// hostLabel 是主机的展示名（降级链：hostname → 「主机 #id」）。管理器注入的
// hostname 可能为空（设备已删/读不到），通知不能因此变成「某某容器在某未知处」。
func hostLabel(e dockerevents.Event) string {
	if e.Hostname != "" {
		return e.Hostname
	}
	return fmt.Sprintf("主机 #%d", e.DeviceID)
}

// clip 按字符数截断并加省略号 —— title 有 128 上限（system-notice 的 binding），
// 长名字不能把通知本体顶出校验。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// buildAlert 组装一条通知：标题带主谓（容器 + 主机），正文带归因（主机、容器、
// 退出码/状态、时间），补记行（extra）带节流的「又发生/累计」口径。
func buildAlert(cls eventClass, e dockerevents.Event, at time.Time, extra string) Alert {
	name := clip(containerName(e), 40)
	host := clip(hostLabel(e), 20)
	var title, actionLine string
	switch cls {
	case classDie:
		actionLine = fmt.Sprintf("退出码：%d", exitCodeOf(e))
		if extra != "" {
			title = fmt.Sprintf("容器持续异常退出：%s（%s）", name, host)
		} else {
			title = fmt.Sprintf("容器异常退出：%s（%s）", name, host)
		}
	case classUnhealthy:
		actionLine = "检查状态：unhealthy"
		if extra != "" {
			title = fmt.Sprintf("容器健康检查仍在失败：%s（%s）", name, host)
		} else {
			title = fmt.Sprintf("容器健康检查失败：%s（%s）", name, host)
		}
	}
	lines := []string{
		fmt.Sprintf("主机：%s（#%d）", hostLabel(e), e.DeviceID),
		fmt.Sprintf("容器：%s（%s）", containerName(e), containerID(e)),
		actionLine,
	}
	if extra != "" {
		lines = append(lines, extra)
	}
	lines = append(lines, fmt.Sprintf("时间：%s", at.Format("2006-01-02 15:04:05")))
	return Alert{Title: title, Content: strings.Join(lines, "\n")}
}

// exitCodeOf 给 die 告警取退出码（规则已保证非 nil 且 ≠ 0；再防御一次只为文案）。
func exitCodeOf(e dockerevents.Event) int32 {
	if e.Item.ExitCode != nil {
		return *e.Item.ExitCode
	}
	return -1
}

// containerName 是容器展示名：有名字用名字，没有退回短 id 截断。
func containerName(e dockerevents.Event) string {
	if e.Item.ActorName != "" {
		return e.Item.ActorName
	}
	return clip(e.Item.ActorID, 12)
}

// containerID 是容器的标识（与名字并列给归因用）。
func containerID(e dockerevents.Event) string {
	if e.Item.ActorID == "" {
		return "未知"
	}
	return e.Item.ActorID
}
