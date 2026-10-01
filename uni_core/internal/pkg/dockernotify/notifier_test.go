package dockernotify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerevents"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 替身与夹具 ─────────────────────────────────────────────────────────────

// fakeCfg 是配置替身：未写入的键回退到调用方给的默认值（与 config 服务的
// 缺省语义一致 —— 键不存在 = 用默认，正是「默认开」的测试口径）。
type fakeCfg struct {
	mu    sync.Mutex
	bools map[string]bool
	ints  map[string]int
}

func newFakeCfg() *fakeCfg {
	return &fakeCfg{bools: map[string]bool{}, ints: map[string]int{}}
}

func (f *fakeCfg) setBool(key string, v bool) {
	f.mu.Lock()
	f.bools[key] = v
	f.mu.Unlock()
}

func (f *fakeCfg) setInt(key string, v int) {
	f.mu.Lock()
	f.ints[key] = v
	f.mu.Unlock()
}

func (f *fakeCfg) GetBool(_ context.Context, key string, defaultVal bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.bools[key]; ok {
		return v
	}
	return defaultVal
}

func (f *fakeCfg) GetInt(_ context.Context, key string, defaultVal int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.ints[key]; ok {
		return v
	}
	return defaultVal
}

// fakeSink 记录型通知替身。
type fakeSink struct {
	mu     sync.Mutex
	alerts []Alert
	err    error
}

func (s *fakeSink) PublishAlert(_ context.Context, a Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, a)
	return s.err
}

func (s *fakeSink) all() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Alert(nil), s.alerts...)
}

// fixedClock 是确定性时钟（可以直接拨）。
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

type notifierEnv struct {
	n    *Notifier
	cfg  *fakeCfg
	sink *fakeSink
	clk  *fixedClock
}

// newTestNotifier 构造夹具：默认窗口 10 分钟（走配置缺省 —— 不写键就是默认开）。
// 基准时钟**窗口对齐**（对 1/2/10 分钟窗都是整倍数）—— Advance(90s) 之类的推进
// 才不至于一头栽进相邻窗口，把「窗口内合并」的断言打成假的。
func newTestNotifier(t *testing.T) *notifierEnv {
	t.Helper()
	cfg := newFakeCfg()
	sink := &fakeSink{}
	clk := &fixedClock{now: time.Unix(1789948800, 0)} // 除 120 / 600 均整除
	n := NewNotifier(Options{Now: clk.Now}, cfg, sink, logger.NewNop())
	return &notifierEnv{n: n, cfg: cfg, sink: sink, clk: clk}
}

func (e *notifierEnv) handle(event dockerevents.Event) {
	e.n.handle(context.Background(), event)
}

func (e *notifierEnv) count() int { return len(e.sink.all()) }

// ── 事件工厂 ──────────────────────────────────────────────────────────────

// dieEv 编一条主机的容器 die 事件（host 7 / 名字 web / id ab12）。
func dieEv(code *int32, name, id string) dockerevents.Event {
	return dockerevents.Event{
		DeviceID: 7,
		Hostname: "alpha",
		Item: agentproto.DockerEventItem{
			T: 1790000000000, Type: "container", Action: "die",
			ActorName: name, ActorID: id, ExitCode: code,
		},
	}
}

func healthEv(action string, name, id string) dockerevents.Event {
	return dockerevents.Event{
		DeviceID: 7,
		Hostname: "alpha",
		Item: agentproto.DockerEventItem{
			T: 1790000000000, Type: "container", Action: action,
			ActorName: name, ActorID: id,
		},
	}
}

func int32p(v int32) *int32 { return &v }

// ── 规则过滤 ──────────────────────────────────────────────────────────────

// die 且 exit ≠ 0 是事故：立即出一条带退出码的通知。
func TestDieNonZeroExitNotifies(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(dieEv(int32p(137), "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("非零退出必须通知，实际 %d 条", env.count())
	}
	a := env.sink.all()[0]
	if !contains(a.Title, "web") || !contains(a.Title, "alpha") {
		t.Fatalf("标题必须带容器与主机: %q", a.Title)
	}
	if !contains(a.Content, "退出码：137") || !contains(a.Content, "主机：alpha（#7）") {
		t.Fatalf("正文必须带归因: %q", a.Content)
	}
}

// die 且 exit 0 是「自己人停的」：常规运维动作不吵自己；nil（旧 agent/不可考）
// 也不反推成事故 —— 两害相权取静默，宁少勿错。
func TestDieZeroExitAndUnknownIgnored(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(dieEv(int32p(0), "web", "ab12"))
	env.handle(dieEv(nil, "web", "ab12"))
	if env.count() != 0 {
		t.Fatalf("exit 0 与不可考退出都不该通知，实际 %d 条", env.count())
	}
}

// health_status: unhealthy 是事故；healthy 是恢复（安抚谁、不打扰谁 —— 恢复本身
// 不需要人去处置）。
func TestHealthUnhealthyNotifies(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(healthEv("health_status: unhealthy", "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("unhealthy 必须通知，实际 %d 条", env.count())
	}
	if !contains(env.sink.all()[0].Title, "健康检查失败") {
		t.Fatalf("标题必须点明健康检查: %q", env.sink.all()[0].Title)
	}
	env.handle(healthEv("health_status: healthy", "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("healthy 不得通知，实际 %d 条", env.count())
	}
}

// 常规运维动作一律不响：start/stop/destroy/restart/exec_die…… 都是人干的活。
func TestRoutineEventsNeverNotify(t *testing.T) {
	env := newTestNotifier(t)
	routine := []string{"start", "stop", "destroy", "create", "restart", "kill", "pause",
		"unpause", "rename", "update", "attach", "detach", "exec_die"}
	for i, act := range routine {
		env.handle(dockerevents.Event{DeviceID: 7, Hostname: "alpha",
			Item: agentproto.DockerEventItem{T: 1, Type: "container",
				Action: act, ActorName: "web", ActorID: "ab12"}})
		if env.count() != 0 {
			t.Fatalf("常规动作 %q 不得通知（第 %d 条后共 %d 条）", act, i, env.count())
		}
	}
	// 非容器类型也不响（image:delete 之类与本联动无关）。
	env.handle(dockerevents.Event{DeviceID: 7, Hostname: "alpha",
		Item: agentproto.DockerEventItem{T: 1, Type: "image", Action: "destroy", ActorName: "nginx:1.27", ActorID: "sha256:beef"}})
	if env.count() != 0 {
		t.Fatalf("非容器事件不得通知，实际 %d 条", env.count())
	}
}

// ── 节流与合并 ────────────────────────────────────────────────────────────

// 同主机同容器同类：窗口内合并成一条（首条立即、重复静默计数）；跨进相邻窗口
// 仍发作才补「仍在持续」汇总（含又发生与累计次数）。
func TestThrottleCoalescesWithinWindow(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("首条必须立即发出，实际 %d 条", env.count())
	}
	// 窗口内连炸 5 次：静默计数，零新增通知。
	for range 5 {
		env.clk.advance(time.Minute)
		env.handle(dieEv(int32p(1), "web", "ab12"))
	}
	if env.count() != 1 {
		t.Fatalf("窗口内重复必须合并，实际 %d 条", env.count())
	}
	n := env.n
	n.mu.Lock()
	st, ok := n.states[eventKey{deviceID: 7, actor: "ab12", class: classDie}]
	n.mu.Unlock()
	if !ok || st.silent != 5 {
		t.Fatalf("静默计数应为 5，实际 %+v", st)
	}

	// 跨进相邻窗口再来一条：补发「仍在持续」，口径 = 上窗积压 + 本条 = 6。
	env.clk.advance(5 * time.Minute) // 已跨窗
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 2 {
		t.Fatalf("跨窗仍发作必须补发汇总，实际 %d 条", env.count())
	}
	roll := env.sink.all()[1]
	if !contains(roll.Title, "持续异常退出") || !contains(roll.Content, "又发生 6 次（累计 7 次）") {
		t.Fatalf("汇总口径不符: %q / %q", roll.Title, roll.Content)
	}
}

// 间隔一个完整空窗后再出现 = 上一轮发作收束：账本重置、按新一次首报起算
// （而不是报一条「又发生 1 次」的陈旧汇总）。
func TestThrottleEpisodeResetsAfterQuietWindow(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	env.clk.advance(30 * time.Minute) // 整整 3 个窗口
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 2 {
		t.Fatalf("新发作必须重新首报，实际 %d 条", env.count())
	}
	if !contains(env.sink.all()[1].Title, "容器异常退出") ||
		contains(env.sink.all()[1].Title, "持续") {
		t.Fatalf("新发作的标题应是首报形态: %q", env.sink.all()[1].Title)
	}
}

// 节流键按（主机、容器、类别）独立：隔壁容器的 flapping 不吞掉本容器的首报；
// 同一容器的 die 与 health 也各算各的账。
func TestThrottleKeysIndependent(t *testing.T) {
	env := newTestNotifier(t)
	env.handle(dieEv(int32p(1), "web", "aa"))
	env.handle(dieEv(int32p(1), "db", "bb"))
	if env.count() != 2 {
		t.Fatalf("不同容器必须各报首条，实际 %d 条", env.count())
	}
	env.handle(healthEv("health_status: unhealthy", "web", "aa"))
	if env.count() != 3 {
		t.Fatalf("同一容器的 die 与 health 互不合并，实际 %d 条", env.count())
	}
	// web 的 die 在窗口内再来一次 —— 只合并进 die 的账，不代表其它没发生。
	env.clk.advance(2 * time.Minute)
	env.handle(dieEv(int32p(1), "web", "aa"))
	if env.count() != 3 {
		t.Fatalf("同类同容器才合并，实际 %d 条", env.count())
	}
	// 另一台主机的同类事件是独立事故。
	other := dieEv(int32p(1), "web", "aa")
	other.DeviceID = 42
	other.Hostname = "beta"
	env.handle(other)
	if env.count() != 4 {
		t.Fatalf("不同主机的事件必须独立首报，实际 %d 条", env.count())
	}
}

// ── 开关与窗口配置 ────────────────────────────────────────────────────────

// 总开关 false = 联动完全静默；重新打开后从**新事件**开始新一轮（关闭期间的
// 账本已清，不补陈旧汇总）。
func TestMasterSwitchOffThenOn(t *testing.T) {
	env := newTestNotifier(t)
	env.cfg.setBool(ConfigNotifyEnabled, false)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 0 {
		t.Fatalf("总开关关闭必须零通知，实际 %d 条", env.count())
	}
	env.cfg.setBool(ConfigNotifyEnabled, true)
	env.clk.advance(time.Minute)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("重新打开后新事件必须立即首报，实际 %d 条", env.count())
	}
	if !contains(env.sink.all()[0].Title, "容器异常退出") {
		t.Fatalf("重开后应是首报形态而不是陈旧汇总: %q", env.sink.all()[0].Title)
	}
}

// 规则开关独立生效：关 die 只静默 die，health 照报。
func TestRuleSwitchesIndependent(t *testing.T) {
	env := newTestNotifier(t)
	env.cfg.setBool(ConfigNotifyDie, false)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	env.handle(healthEv("health_status: unhealthy", "web", "ab12"))
	if env.count() != 1 || !contains(env.sink.all()[0].Title, "健康检查") {
		t.Fatalf("关 die 不得牵连 health: %+v", env.sink.all())
	}
	env.cfg.setBool(ConfigNotifyHealth, false)
	env.handle(healthEv("health_status: unhealthy", "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("关 health 后不得再报，实际 %d 条", env.count())
	}
}

// 窗口长度走配置：2 分钟窗（写键生效）；非法值回退默认 10 分钟并照常工作。
func TestWindowFromConfigAndInvalidFallback(t *testing.T) {
	env := newTestNotifier(t)
	env.cfg.setInt(ConfigNotifyWindowMinutes, 2)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	env.clk.advance(90 * time.Second)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 1 {
		t.Fatalf("2 分钟窗内重复必须合并，实际 %d 条", env.count())
	}
	env.clk.advance(90 * time.Second) // 跨过 2 分钟窗
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 2 {
		t.Fatalf("跨 2 分钟窗必须补报汇总，实际 %d 条", env.count())
	}

	// 非法值（0 / 超上限）→ 回退默认 10 分钟：9 分钟间隔仍在同一窗口内。
	env2 := newTestNotifier(t)
	env2.cfg.setInt(ConfigNotifyWindowMinutes, 0)
	env2.handle(dieEv(int32p(1), "web", "ab12"))
	env2.clk.advance(9 * time.Minute)
	env2.handle(dieEv(int32p(1), "web", "ab12"))
	if env2.count() != 1 {
		t.Fatalf("非法窗口回退默认 10 分钟后应合并，实际 %d 条", env2.count())
	}

	env3 := newTestNotifier(t)
	env3.cfg.setInt(ConfigNotifyWindowMinutes, 999999)
	env3.handle(dieEv(int32p(1), "web", "ab12"))
	env3.clk.advance(9 * time.Minute)
	env3.handle(dieEv(int32p(1), "web", "ab12"))
	if env3.count() != 1 {
		t.Fatalf("超上限窗口回退默认 10 分钟后应合并，实际 %d 条", env3.count())
	}
}

// 通知失败不炸联动器（只留痕）：节流账本**乐观计数**照常推进 —— 失败期间
// 每个窗口仍至多一次发布尝试（DB 抖动 + flapping 时不会把每次 flap 都变成一次
// 发布重试），恢复后跨窗的汇总口径 = 发作以来的全部事件数。
func TestSinkFailureDoesNotBreakNotifier(t *testing.T) {
	env := newTestNotifier(t)
	env.sink.err = errors.New("db 抖动")
	env.handle(dieEv(int32p(1), "web", "ab12")) // 发布失败：账本已计 1
	env.sink.err = nil
	env.handle(dieEv(int32p(1), "web", "ab12")) // 同窗口：合并，不重发
	if env.count() != 1 {
		t.Fatalf("失败后同窗口事件应继续合并，实际 %d 条", env.count())
	}
	env.clk.advance(11 * time.Minute)
	env.handle(dieEv(int32p(1), "web", "ab12"))
	if env.count() != 2 {
		t.Fatalf("恢复后跨窗照常补报，实际 %d 条", env.count())
	}
	roll := env.sink.all()[1]
	if !strings.Contains(roll.Content, "又发生 2 次（累计 3 次）") {
		t.Fatalf("累计口径是发作内全部事件数: %q", roll.Content)
	}
}

// ── 帧路径纪律（ResidentSink 契约）────────────────────────────────────────

// DeliverDockerEvent 绝不阻塞：队列满直接丢并计数 —— 丢的是联动输入，活动流
// 不受影响。
func TestDeliverNonBlockingQueueOverflow(t *testing.T) {
	env := newTestNotifier(t)
	env.n.queue = make(chan dockerevents.Event, 2) // 压小队列模拟满载
	env.n.opts.QueueDepth = 2
	for range 3 {
		env.n.DeliverDockerEvent(dieEv(int32p(1), "web", "ab12"))
	}
	if got := env.n.Dropped(); got != 1 {
		t.Fatalf("第三条必须被丢并计数，实际 %d", got)
	}
}

// 队列 → 工作协程 → 通知的全程集成：真 goroutine、真管道（其余用例直接驱动
// handle 保证确定性，这一条证明 Run 的接线是真的）。
func TestQueueToWorkerIntegration(t *testing.T) {
	env := newTestNotifier(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go env.n.Run(ctx)
	env.n.DeliverDockerEvent(dieEv(int32p(1), "web", "ab12"))

	deadline := time.Now().Add(3 * time.Second)
	for env.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if env.count() != 1 {
		t.Fatalf("事件应经队列到工作协程发布出去，实际 %d 条", env.count())
	}
}

// ── 小工具 ────────────────────────────────────────────────────────────────

func contains(s, sub string) bool { return strings.Contains(s, sub) }
