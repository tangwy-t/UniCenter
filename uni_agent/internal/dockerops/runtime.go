package dockerops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

const (
	// dockerStateFileName 是配置落盘的文件名（在 agent 的 state dir 下）。
	dockerStateFileName = "docker_state.json"
	// defaultSnapshotInterval 是内置默认快照周期（hello_ack 下发前的保守值）。
	defaultSnapshotInterval = 30 * time.Second
	// minTransferDirMode 是产物目录的权限（0700：只有 agent 能读写 —— 它是 root 进程，
	// 世界可写的目录会变成符号链接攻击的温床，这也是默认不用 /tmp 的原因）。
	minTransferDirMode = 0o700
)

// Deps 是 dockerops 的全部外部依赖（与 upgrade.Deps 同风格）。
type Deps struct {
	// StateDir 是 agent 的状态目录：flavor 探测结果与配置快照落在这里。
	StateDir string
	// SendState / SendResult 是上行出口（由 transport 注入）。
	SendState  func(*agentproto.DockerState) error
	SendResult func(*agentproto.DockerCmdResult) error
	Log        Logger
	Now        func() time.Time
	// API 是 Docker 能力面。nil = 由 NewForProcess 填 SDK 真身。
	API DockerAPI
	// Interval 是初始快照周期（hello_ack 下发后覆盖）。
	Interval time.Duration
}

// dockerStateFile 是持久化的少量事实。
//
// 为什么必须落盘：hello_ack 的 docker 块**重连时生效**，agent 重启后与服务端之间没有
// 「上次拿到的配置」的记忆 —— 不落盘就会用内置默认值跑（例如保护清单为空 → 底座容器
// 失去保护），而服务端的 config_version 没变、不会再下发。
type dockerStateFile struct {
	ConfigVersion       uint64 `json:"config_version"`
	Flavor              string `json:"flavor,omitempty"`
	FlavorVersion       string `json:"flavor_version,omitempty"`
	Protected           string `json:"protected,omitempty"`
	TransferDir         string `json:"transfer_dir,omitempty"`
	SnapshotIntervalSec int    `json:"snapshot_interval_sec,omitempty"`
}

// Runtime 是 dockerops 的门面：连接层只与它打交道（与 upgrade.Runtime 同一形状）。
type Runtime struct {
	deps Deps
	log  Logger

	snapshotter *Snapshotter
	exec        Executor
	// dispOnce/disp 是分派器的**懒构造**（见 dispatcher()）：构造时点必须在
	// 「第一次真正要用」而不是 New —— 在 New 里立刻把 exec 拷进分派器，会让
	// 调用方随后替换 exec 的动作静默失效（跑的还是旧执行器）。
	dispOnce sync.Once
	disp     *Dispatcher

	trigger chan struct{}

	mu            sync.Mutex
	interval      time.Duration
	protected     *ProtectedList
	protectedRaw  string
	transferDir   string
	configVersion uint64
	flavor        string
	flavorVer     string
}

// New 构造门面（**测试与显式注入用**；生产用 NewForProcess）。
func New(deps Deps) *Runtime {
	if deps.Log == nil {
		deps.Log = nopLogger{}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Interval <= 0 {
		deps.Interval = defaultSnapshotInterval
	}
	r := &Runtime{
		deps:      deps,
		log:       deps.Log,
		trigger:   make(chan struct{}, 1),
		interval:  deps.Interval,
		protected: ParseProtected(""),
	}
	r.loadPersisted()
	r.snapshotter = NewSnapshotter(deps.API, r.protected, deps.Log, deps.Now)
	if r.flavor != "" {
		r.snapshotter.SetCompose(r.flavor, r.flavorVer)
	}
	r.exec = NewReadExecutor(deps.API)
	return r
}

// NewForProcess 构造**面向真实进程**的门面：把 API 填成 SDK 真身。
//
// 与 upgrade.NewForProcess 同一条理由：漏设的依赖不会报错，只会让功能悄悄不工作 ——
// 收进构造函数后 main 只要调用它就不可能漏。
func NewForProcess(deps Deps) (*Runtime, error) {
	if deps.API == nil {
		api, err := newSDKAdapter()
		if err != nil {
			return nil, err
		}
		deps.API = api
	}
	return New(deps), nil
}

// dispatcher 返回指令分派器（首次调用时构造）。
//
// 懒构造而不是在 New 里建：exec 是可以在构造之后被替换的字段，提前把它拷进分派器
// 会让替换无效 —— 症状是「换了执行器，跑的还是旧的」，且只有走到那条路径才看得见。
func (r *Runtime) dispatcher() *Dispatcher {
	r.dispOnce.Do(func() { r.disp = NewDispatcher(r.exec, r.deps.SendResult, r.log) })
	return r.disp
}

// loadPersisted 载入上次落盘的配置（文件不存在是正常情形：首次启动）。
func (r *Runtime) loadPersisted() {
	b, err := os.ReadFile(filepath.Join(r.deps.StateDir, dockerStateFileName))
	if err != nil {
		return
	}
	var f dockerStateFile
	if err := json.Unmarshal(b, &f); err != nil {
		r.log.Warn("docker state file corrupt, using defaults", "err", err.Error())
		return
	}
	r.mu.Lock()
	r.configVersion = f.ConfigVersion
	r.flavor, r.flavorVer = f.Flavor, f.FlavorVersion
	r.transferDir = f.TransferDir
	if f.SnapshotIntervalSec > 0 {
		r.interval = time.Duration(f.SnapshotIntervalSec) * time.Second
	}
	r.mu.Unlock()
	if f.Protected != "" {
		r.protected = ParseProtected(f.Protected)
		// 原文也要一起还原：compose 探测成功等路径会再次 persist，若原文是空的，
		// 这一轮写回就把已落盘的保护清单**抹掉**了 —— 而那正是落盘要防的症状。
		r.protectedRaw = f.Protected
	}
}

// persist 原子落盘当前配置（临时文件 + rename：与 agent token 的落盘同一模式，
// 避免进程在写到一半时被杀掉留下一份半截 JSON）。
func (r *Runtime) persist() {
	r.mu.Lock()
	f := dockerStateFile{
		ConfigVersion:       r.configVersion,
		Flavor:              r.flavor,
		FlavorVersion:       r.flavorVer,
		Protected:           r.protectedRaw,
		TransferDir:         r.transferDir,
		SnapshotIntervalSec: int(r.interval / time.Second),
	}
	r.mu.Unlock()

	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		r.log.Warn("marshal docker state failed", "err", err.Error())
		return
	}
	path := filepath.Join(r.deps.StateDir, dockerStateFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		r.log.Warn("write docker state failed", "err", err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		r.log.Warn("rename docker state failed", "err", err.Error())
	}
}

// SnapshotLoop 是快照主循环（main 在自己的 goroutine 里跑，与 collectLoop 并列）。
func (r *Runtime) SnapshotLoop(ctx context.Context) {
	r.probeComposeOnce(ctx)
	for {
		r.collectAndSend(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.trigger:
		case <-time.After(r.currentInterval()):
		}
	}
}

// Run 是分派器 worker（与 SnapshotLoop 分开：指令执行与采集互不阻塞 ——
// 一条 15 分钟的 save 不该让快照停摆）。
func (r *Runtime) Run(ctx context.Context) { r.dispatcher().Run(ctx) }

// OnConnected 握手完成：立刻采一帧（首帧不等周期，spec §2）。
//
// **非阻塞**：它在连接的读循环里被调用，故只做一个「塞进触发通道」的动作。
// 通道深度 1：连续两次触发合并成一次采集（快照是覆盖式数据，采两次没有意义）。
func (r *Runtime) OnConnected() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// OnConfig 应用 hello_ack 下发的 docker 配置块。
//
// 空字段 = **未下发**（保持上一份/内置默认）：协议里 protected 是 omitempty 的字符串，
// 「老 core 不带该字段」与「显式清空清单」在线上同形，而清空是危险方向 ——
// 一期 protected 只用于标注，二期起它会成为拒绝依据，那时必须重新审视这条取舍
// （显式清空需要一个不同于「空」的表示）。
func (r *Runtime) OnConfig(cfg *agentproto.DockerConfig) {
	if cfg == nil {
		return
	}
	r.mu.Lock()
	changed := cfg.ConfigVersion != r.configVersion
	r.configVersion = cfg.ConfigVersion
	if cfg.Protected != "" {
		r.protected = ParseProtected(cfg.Protected)
		r.protectedRaw = cfg.Protected
	}
	if cfg.TransferDir != "" {
		r.transferDir = cfg.TransferDir
	}
	if cfg.SnapshotInterval > 0 {
		r.interval = time.Duration(cfg.SnapshotInterval) * time.Second
	}
	protected, interval := r.protected, r.interval
	r.mu.Unlock()

	r.snapshotter.SetProtected(protected)
	if changed {
		r.log.Info("docker config applied",
			"configVersion", cfg.ConfigVersion,
			"snapshotInterval", interval.String(),
			"protectedEntries", protected.Size())
	}
	r.ensureTransferDir()
	r.persist()
}

// ensureTransferDir 按需创建产物目录（一期只创建，image:save/load 在二期使用）。
//
// 失败只记 Warn 不阻断：目录不可用时二期会退回到一句明确的结论（spec §7.5），
// 现在没有它不影响任何一期功能。
func (r *Runtime) ensureTransferDir() {
	r.mu.Lock()
	dir := r.transferDir
	r.mu.Unlock()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, minTransferDirMode); err != nil {
		r.log.Warn("create docker transfer dir failed", "dir", dir, "err", err.Error())
	}
}

// OnCmd 受理一条下行指令。**非阻塞**：它在连接的读循环里被调用。
//
// 队列满时立刻回一句结论（而不是静默丢弃）：用户点了按钮就该得到回应。
func (r *Runtime) OnCmd(cmd *agentproto.DockerCmd) {
	if cmd == nil {
		return
	}
	if !r.dispatcher().Handle(context.Background(), cmd) {
		// 回结果本身会短暂阻塞（上行队列），故放 goroutine —— 读循环一秒都不能停。
		go func(ref string) {
			if err := r.deps.SendResult(&agentproto.DockerCmdResult{
				Ref: ref, Error: "指令排队已满，请稍后重试",
			}); err != nil {
				r.log.Warn("send busy result failed", "ref", ref, "err", err.Error())
			}
		}(cmd.Ref)
	}
}

// OnFrame 收到一条流控制帧。**一期只忽略**：流通道（日志 follow / 终端）在三期接线，
// 但类型已在协议里登记，老 agent 忽略未知 core.* 不断连 —— 这里显式记 Debug，
// 让「三期没接上」这件事在日志里是可见的而不是猜的。
func (r *Runtime) OnFrame(f *agentproto.CoreDockerFrame) {
	if f == nil {
		return
	}
	r.log.Debug("docker stream frame ignored (phase 3)", "session", f.SessionID, "op", f.Op)
}

// currentInterval 返回当前快照周期（每次采集后重读，配置变更下一轮即生效）。
func (r *Runtime) currentInterval() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interval <= 0 {
		return defaultSnapshotInterval
	}
	return r.interval
}

// collectAndSend 采一帧并发出去。发送失败只记 Warn：快照是幂等的覆盖式数据，
// 下一轮会补上（断线时发送必然失败，那不是异常）。
func (r *Runtime) collectAndSend(ctx context.Context) {
	st := r.snapshotter.Collect(ctx)
	if err := r.deps.SendState(st); err != nil {
		r.log.Debug("docker state not sent", "err", err.Error())
	}
}

// probeComposeOnce 探测一次 compose 形态并缓存（单一 flavor 纪律：主机上定下不再改）。
func (r *Runtime) probeComposeOnce(ctx context.Context) {
	r.mu.Lock()
	have := r.flavor != ""
	r.mu.Unlock()
	if have {
		return
	}
	flavor, version, err := r.deps.API.ComposeVersion(ctx)
	if err != nil {
		// 探测失败不阻断：容器/镜像/卷/网络与 compose 无关，页面上的「项目」页
		// 会显示「未检测到 compose」而不是整机不可用。
		r.log.Warn("compose probe failed", "err", err.Error())
		return
	}
	r.mu.Lock()
	r.flavor, r.flavorVer = flavor, version
	r.mu.Unlock()
	r.snapshotter.SetCompose(flavor, version)
	r.log.Info("compose flavor detected", "flavor", flavor, "version", version)
	r.persist()
}
