// Package upgrade 实现 uni_agent 的自升级与回滚：下载、校验、原子替换、自重启，
// 以及**本地自动回滚**（升级事务）。
//
// 设计要点（docs/superpowers/specs/2026-09-23-agent-upgrade-design.md §6.3、§8.3）：
//
//   - 远程回滚依赖「新版本起来后还能连上 core」，而恰恰在最需要回滚的场景
//     （URL/依赖出问题、启动即崩、连不上）它连不上 —— 这条路会断。故设备侧必须
//     有自治回滚，本包的 transaction.go 就是它。
//   - 替换必须**同目录临时文件 + rename**：Linux 允许 rename 覆盖运行中的二进制，
//     直接写会 ETXTBSY。
//   - 替换前先冒烟（跑一次自身 `-self-check`）：把「装了个跑不起来的二进制」
//     挡在替换之前。
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Logger 是本包依赖的最小日志接口（与 transport 同款，避免把 zap 拖进 agent）。
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Debug(msg string, kv ...any)
}

// Deps 是运行时的全部外部依赖。可注入的字段（Executable/Exec/Now/Sleep/HTTPClient/
// GOOS/JitterMax）只为测试而存在 —— 一个会替换自身二进制并自重启的组件，
// 不在测试里把这些动作钉住是不可接受的。
type Deps struct {
	// Version 是编译期注入的版本（config.DefaultVersion）。
	Version string
	// StateDir 存放升级事务与尝试记录（与 instance_id/agent_token 同目录）。
	StateDir string
	// DownloadBase 形如 http://host:8088（不含路径）。
	DownloadBase string
	// Token 取当前 agent token（enroll 之后才有；为空时下载会被拒 —— 这是正确的：
	// 未注册的设备不该能下载程序包）。
	Token func() string
	// SendStatus 上报一次升级状态（非阻塞；transport.Client.SendUpgradeStatus 满足）。
	SendStatus func(*agentproto.UpgradeStatus) error
	// SaveToken 落盘当前 token：exec 会绕过 main 的退出路径，故必须在这里保存。
	SaveToken func(string) error
	Log       Logger

	// ── 可注入（测试）────────────────────────────────────────────────
	GOOS       string
	Executable func() (string, error)
	// Exec 替换当前进程映像（生产：syscall.Exec）。返回错误表示替换失败。
	Exec func(argv0 string, argv []string, envv []string) error
	Now  func() time.Time
	// Sleep 可被测试换成「立即返回」（抖动/退避不该让测试真的等）。
	Sleep      func(ctx context.Context, d time.Duration)
	HTTPClient *http.Client
	// JitterMax 是下载前抖动上限（防惊群）；0 = 不抖动（测试）。
	JitterMax time.Duration
	// SmokeTimeout 是冒烟自检的超时（0 → 5s）。
	SmokeTimeout time.Duration
	// TrialWindow / ProbeWindow 覆盖试用期与延长量（0 → 生产默认）。
	TrialWindow time.Duration
	ProbeWindow time.Duration
	// ProcAttr: 冒烟自检执行子进程用的可注入钩子（测试里不必真的跑二进制）。
	SmokeRun func(ctx context.Context, bin string) (string, error)
}

// 常量：窗口与阈值都在这里，推导见设计 §12。
//
// 两个试用期窗口可由 Deps 覆盖（TrialWindow / ProbeWindow）：默认值面向生产，
// 而测试与快速验收需要把它们压到秒级 —— 否则「本地自愈」这条路径要么测不了，
// 要么每条用例等几分钟。
const (
	// trialWindow 是试用期：新版本自替换后应在此时限内完成一次握手。
	trialWindow = 180 * time.Second
	// probeWindow 是**每一次「试着连但失败」的延长量**：试用期满时若有过失败痕迹，
	// 说明连接循环在正常工作（只是 core 不可达），再给一个窗口等它连上。
	probeWindow = 90 * time.Second
	// maxStartAttempts 是启动计数上限：新版本起来 N 次都没确认即判定崩溃循环。
	maxStartAttempts = 3
	// probeBudget 是试用期满后的**探测预算**（D3）：连接失败累计到该值才回滚，
	// 用来区分「新版本坏了」与「core 暂时不可达」。
	probeBudget = 3
	// retryBackoff 是同一个 request_id 的重投递退避（重连时服务端会重发指令，
	// 不该因此反复下载）。
	retryBackoff = 10 * time.Minute
	// progressStep 是下载进度的上报步长（百分点）。
	progressStep = 5
	// downloadCap 是下载的兜底上限（指令里的 size_bytes 更小则用它）。
	downloadCap = 512 << 20
)

// Runtime 是升级运行时。它由连接层驱动（OnDirective / OnConnected / OnConnectFailed），
// 自身不拥有任何连接。
type Runtime struct {
	deps Deps
	log  Logger

	mu      sync.Mutex
	running bool
	// lastAttempt 记录最近一次尝试（同一 request_id 的退避依据）。
	lastAttempt *attemptRecord
}

// attemptRecord 是最近一次尝试的记录（落盘：进程可能重启，退避必须跨重启）。
type attemptRecord struct {
	RequestID string    `json:"request_id"`
	Version   string    `json:"version"`
	At        time.Time `json:"at"`
	Result    string    `json:"result,omitempty"`
}

// NewForProcess 构造**面向真实进程**的运行时：把三个「只有生产才有意义」的依赖
// 填成真实实现 —— 自替换与自重启所依赖的 os.Executable / syscall.Exec / runtime.GOOS。
//
// 为什么要一个专用构造函数（而不是让 main 逐个字段填）：这三个字段漏设的后果都
// **不会报错**，只是功能悄悄不工作 ——
//   - 漏 Exec：文件被替换了但进程不重启（仍然跑旧代码、上报旧版本，
//     服务端永远显示「升级中」）。这是端到端验收真抓到的缺陷；
//   - 漏 Executable：定位不到自身路径，替换会失败；
//   - 漏 GOOS：平台检查失效（默认按 linux 处理）。
//
// 把它们收进构造函数后，main 只要调用 NewForProcess 就不可能漏；
// New（裸构造）留给测试 —— 测试要的就是「注入替身」。
func NewForProcess(deps Deps) *Runtime {
	deps.Executable = os.Executable
	deps.Exec = syscall.Exec
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	return New(deps)
}

// New 构造运行时（并载入上次的尝试记录，使退避跨重启有效）。
//
// **生产代码请用 NewForProcess**：裸构造不填 Exec/Executable/GOOS，
// 那三个字段缺省时升级只会「替换文件但不重启进程」（见 NewForProcess 的说明）。
func New(deps Deps) *Runtime {
	if deps.Log == nil {
		deps.Log = nopLogger{}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepCtx
	}
	if deps.SmokeTimeout <= 0 {
		deps.SmokeTimeout = 5 * time.Second
	}
	if deps.TrialWindow <= 0 {
		deps.TrialWindow = trialWindow
	}
	if deps.ProbeWindow <= 0 {
		deps.ProbeWindow = probeWindow
	}
	r := &Runtime{deps: deps, log: deps.Log}
	r.lastAttempt = readAttemptRecord(deps.StateDir)
	return r
}

// OnDirective 处理一次升级指令。**非阻塞**：它在连接的读循环里被调用。
//
// 单飞：正在升级时后来的指令只记日志 —— 两个并发的自替换会把彼此的文件搅在一起。
func (r *Runtime) OnDirective(d *agentproto.UpgradeDirective) {
	if d == nil {
		return
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		r.log.Info("upgrade already running, directive ignored",
			"target", d.TargetVersion, "requestId", d.RequestID)
		return
	}
	r.mu.Unlock()

	if d.TargetVersion == r.deps.Version {
		// 服务端可能在我们自报版本之后仍下发一次（例如全站目标恰等于当前版本）。
		// 这是正常情况（对账幂等的一部分），不必上报任何状态：成败由下一次 hello 判定。
		r.log.Info("upgrade not needed: already on target version", "version", d.TargetVersion)
		return
	}
	go r.run(d)
}

// run 是完整的升级流水线。
func (r *Runtime) run(d *agentproto.UpgradeDirective) {
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	ctx := context.Background()

	// ① 退避：同一个 request_id 的重投递（重连时的 hello_ack）不该反复下载。
	// 换 request_id 意味着服务端开了**新的一次尝试**（操作员点了重试或换了目标），
	// 那种情况必须立即执行 —— 「人工催办可覆盖」就是靠这个区分的。
	if r.inBackoff(d) {
		r.log.Info("upgrade skipped: same request within backoff window",
			"target", d.TargetVersion, "requestId", d.RequestID, "backoff", retryBackoff.String())
		return
	}

	// ② 平台：自替换依赖「rename 覆盖运行中的二进制」，Windows 上必然失败
	// （文件被占用），故明确拒绝并给出可解释的原因码。
	goos := r.deps.GOOS
	if goos == "" {
		goos = "linux"
	}
	if goos != "linux" {
		r.fail(d, agentproto.ReasonPlatformUnsupported, "该平台不支持自替换（"+goos+"）", nil)
		return
	}

	// ③ 抖动：全站同时下发时错开下载（防惊群）。默认 0–30s。
	if r.deps.JitterMax > 0 {
		j := time.Duration(rand.Int63n(int64(r.deps.JitterMax)))
		r.log.Info("upgrade jitter before download", "wait", j.String())
		r.deps.Sleep(ctx, j)
	}

	exe, err := r.exePath()
	if err != nil {
		r.fail(d, agentproto.ReasonReplaceFailed, "无法定位自身程序文件", err)
		return
	}

	// ④ 下载到**同目录**临时文件（rename 要求同一文件系统）。
	tmp := filepath.Join(filepath.Dir(exe), fmt.Sprintf(".uni_agent.new-%d", time.Now().UnixNano()))
	sum, err := r.download(ctx, d, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		code := agentproto.ReasonDownloadFailed
		if errors.Is(err, errNoPermission) {
			code = agentproto.ReasonNoWritePermission
		}
		r.fail(d, code, "下载程序包失败", err)
		return
	}
	if sum != strings.ToLower(d.SHA256) {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonChecksumMismatch,
			fmt.Sprintf("摘要不符（期望 %s，实得 %s）", d.SHA256, sum), nil)
		return
	}
	r.report(d, agentproto.UpgradeStateVerifying, nil)

	// ⑤ 冒烟自检：能跑起来且自述版本正确，才允许替换。
	//
	// **必须先给可执行位**：下载时以 0600 落盘（不让人在下载中就去执行半截文件），
	// 而冒烟要 fork/exec 它 —— 顺序反了会得到 `fork/exec …: permission denied`，
	// 被归类成「新版本无法启动」（原因码没错，但真正的原因是流程顺序，不是二进制）。
	// 这一条是端到端验收抓出来的：单元测试里冒烟是注入的，不会真的 exec 文件。
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonNoWritePermission, "设置程序文件权限失败", err)
		return
	}
	selfVersion, err := r.smoke(ctx, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonSmokeTestFailed, "新版本无法启动", err)
		return
	}
	if !strings.Contains(selfVersion, d.TargetVersion) {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonSmokeTestFailed,
			fmt.Sprintf("新版本自述版本为 %q，与目标 %s 不符", strings.TrimSpace(selfVersion), d.TargetVersion), nil)
		return
	}
	// ⑥ 备份 + 写事务 + 原子替换。
	r.report(d, agentproto.UpgradeStateInstalling, nil)
	backup := exe + ".prev"
	if err := copyFile(exe, backup); err != nil {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonReplaceFailed, "备份当前程序文件失败", err)
		return
	}
	tx := &transaction{
		FromVersion: r.deps.Version,
		ToVersion:   d.TargetVersion,
		BackupPath:  backup,
		RequestID:   d.RequestID,
		Phase:       phasePending,
		At:          r.deps.Now(),
	}
	// 事务必须**在替换之前**落盘：替换之后到新进程启动之间没有任何写点，
	// 而「新版本起来了但连不上」的判定完全依赖这份记录。
	if err := writeTransaction(r.deps.StateDir, tx); err != nil {
		_ = os.Remove(tmp)
		r.fail(d, agentproto.ReasonReplaceFailed, "写入升级事务失败", err)
		return
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		_ = clearTransaction(r.deps.StateDir)
		r.fail(d, agentproto.ReasonReplaceFailed, "替换程序文件失败", err)
		return
	}
	r.setAttempt(attemptRecord{RequestID: d.RequestID, Version: d.TargetVersion,
		At: r.deps.Now(), Result: "installed"})

	// ⑦ 上报「即将重启」→ 落盘 token → 自重启。
	r.report(d, agentproto.UpgradeStateRestarting, nil)
	if r.deps.SaveToken != nil && r.deps.Token != nil {
		if tok := r.deps.Token(); tok != "" {
			if err := r.deps.SaveToken(tok); err != nil {
				r.log.Warn("save token before restart failed", "err", err.Error())
			}
		}
	}
	r.log.Info("restarting into new version", "from", r.deps.Version, "to", d.TargetVersion)
	if r.deps.Exec != nil {
		err := r.deps.Exec(exe, os.Args, os.Environ())
		// exec 成功不该返回；返回即失败 → 还原备份，停在新版本以外的地方。
		if restoreErr := os.Rename(backup, exe); restoreErr != nil {
			r.log.Warn("restore after exec failure failed", "err", restoreErr.Error())
		}
		_ = clearTransaction(r.deps.StateDir)
		r.fail(d, agentproto.ReasonExecFailed, "重启失败，已还原旧版本", err)
		return
	}
	// 未注入 Exec（测试态）：到此为止，事务留在 pending 由测试自行断言。
}

// download 下载程序包到 dst，返回内容的 sha256（同时执行大小上限与进度上报）。
func (r *Runtime) download(ctx context.Context, d *agentproto.UpgradeDirective, dst string) (string, error) {
	url := strings.TrimRight(r.deps.DownloadBase, "/") +
		"/api/v1/agent/releases/" + d.TargetVersion + "/download"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// token 明文只在这一个请求头里出现：不进日志、不进 URL（URL 会进各级访问日志）。
	if r.deps.Token != nil {
		req.Header.Set("X-Agent-Token", r.deps.Token())
	}
	resp, err := r.deps.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// 状态码是排障线索，但要截断（细节列不是日志通道）。
		return "", fmt.Errorf("下载入口返回 HTTP %d", resp.StatusCode)
	}

	limit := int64(downloadCap)
	if d.SizeBytes > 0 && d.SizeBytes < limit {
		limit = d.SizeBytes
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", fmt.Errorf("%w: %v", errNoPermission, err)
		}
		return "", err
	}
	hasher := sha256.New()
	// 进度是**真实字节百分比**（已下载 / 指令里给的 size_bytes）：这是三层进度里
	// 唯一有天然刻度的一层（校验/替换/重启没有刻度，故那几层只报阶段）。
	// 节流：跨 5 个百分点且距上次上报 ≥1 秒才发一条（协议里 progress 只许出现在
	// downloading 阶段，这条上报立刻会被服务端拒，若不节流会在几秒内刷出几十帧）。
	body := &progressReader{
		r: resp.Body, total: d.SizeBytes, now: r.deps.Now,
		onProgress: func(pct int) {
			p := pct
			r.report(d, agentproto.UpgradeStateDownloading, &p)
		},
	}
	n, copyErr := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(body, limit+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n > limit {
		return "", fmt.Errorf("程序包超过预期大小（>%d 字节）", limit)
	}
	if n != d.SizeBytes {
		// 大小与指令不符：要么下到一半断了，要么服务端换了产物。
		// 两者都该拒绝（摘要校验也会拦，但这里能给出更准确的原因）。
		return "", fmt.Errorf("程序包大小不符（期望 %d，实得 %d）", d.SizeBytes, n)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// smoke 跑一次自身 `-self-check`，返回它自述的版本。
//
// 为什么必须有这一步：下载+摘要只证明「字节没坏」，不证明「它能在这台机器上跑」。
// 一个架构不对、依赖缺失或构建被截断的二进制，装上去的后果是设备失联 ——
// 而本地自愈要等一个试用期才发现。冒烟把这类问题挡在替换之前。
func (r *Runtime) smoke(ctx context.Context, bin string) (string, error) {
	if r.deps.SmokeRun != nil {
		return r.deps.SmokeRun(ctx, bin)
	}
	ctx, cancel := context.WithTimeout(ctx, r.deps.SmokeTimeout)
	defer cancel()
	out, err := runSelfCheck(ctx, bin)
	if err != nil {
		return "", err
	}
	return out, nil
}

// Start 启动**试用期看门狗**（在建立连接之前调用一次）。
//
// 为什么必须有它：回滚的另一条触发路径（OnConnectFailed 的探测预算）依赖
// 「连接循环在跑」。若新版本启动后卡住、连一次连接尝试都没有发生（不崩也不连），
// 事件永远不会到来 —— 设备会永远停在那份有问题的二进制上，而 systemd 也不会
// 重启它（进程还活着）。看门狗把「到点未确认」这件事变成一个**由时间驱动**的
// 判定，覆盖这条否则无解的路径：
//
//   - 到点（At + TrialWindow）仍 pending → 看本次试用期里有没有**失败痕迹**
//     （ProbeFailures > 0：连接循环确实在跑、只是连不上，例如 core 停机）；
//   - 没有痕迹 → **立即回滚**：进程自己有问题（连尝试都没有）；
//   - 有痕迹 → 每有一次痕迹延长一个 ProbeWindow，最多 probeBudget 次，
//     再用尽即回滚（这就是 D3 的探测预算，由时间兑现而不是靠事件恰好到来）。
func (r *Runtime) Start(ctx context.Context) {
	tx := readTransaction(r.deps.StateDir)
	if tx == nil || tx.Phase != phasePending {
		return
	}
	go r.probationWatch(ctx)
}

// probationWatch 周期巡检试用期状态（读盘为准：事务是唯一事实源）。
func (r *Runtime) probationWatch(ctx context.Context) {
	// 巡检间隔 = 窗口的 1/12（生产：180s/12 = 15s；测试把窗口压到毫秒级时同样按比例，
	// 否则固定下限会让短窗口「到点了却没人来看」）。硬下限只防空转。
	interval := r.deps.TrialWindow / 12
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tx := readTransaction(r.deps.StateDir)
		if tx == nil || tx.Phase != phasePending {
			// 已确认（OnConnected 清了事务）或被别处终结：看门狗使命结束。
			return
		}
		deadline := tx.At.Add(r.deps.TrialWindow)
		if tx.ProbeFailures > 0 {
			deadline = deadline.Add(time.Duration(tx.ProbeFailures) * r.deps.ProbeWindow)
		}
		if r.deps.Now().Before(deadline) {
			continue
		}
		if tx.ProbeFailures == 0 {
			r.log.Warn("upgrade probation expired without any connection attempt",
				"to", tx.ToVersion)
			r.rollback(tx, agentproto.ReasonNotConnectedAfterUpgrade,
				"新版本启动后未能建立连接")
			return
		}
		if tx.ProbeFailures < probeBudget {
			// 还有探测预算：不在这里回滚，交给 OnConnectFailed 继续计数
			//（下一次失败会把 deadline 再推一个窗口）。
			continue
		}
		r.log.Warn("upgrade probation expired after probe budget",
			"to", tx.ToVersion, "probes", tx.ProbeFailures)
		r.rollback(tx, agentproto.ReasonNotConnectedAfterUpgrade, "试用期内未能连上服务端")
		return
	}
}

// OnConnected 握手完成：升级事务据此确认（新版本确实连上了）。
func (r *Runtime) OnConnected() {
	tx := readTransaction(r.deps.StateDir)
	if tx == nil {
		return
	}
	switch tx.Phase {
	case phasePending:
		// 新版本工作了：确认（清掉事务）。此刻**不能**再上报任何状态 ——
		// 成功由服务端在下一次 hello 里用版本号裁决（设计 §4）。
		if err := clearTransaction(r.deps.StateDir); err != nil {
			r.log.Warn("clear upgrade transaction failed", "err", err.Error())
			return
		}
		r.log.Info("upgrade confirmed: new version connected",
			"from", tx.FromVersion, "to", tx.ToVersion)
		// 旧版本的备份留着（人工取证 / 事后回滚都用得上），不主动删。
	case phaseRolledBack:
		// 我们是被回滚回来的旧版本：把这条事实**上报**给服务端，然后清标记。
		// 上报放在这里（连上之后）而不是回滚现场，是因为回滚现场往往根本连不上。
		r.reportRolledBack(tx)
	}
}

// OnConnectFailed 一次连接尝试失败。
//
// 判定规则（D3）：事务仍在试用期内 → 不动（正常重连节奏，且崩溃循环由启动计数兜住）；
// 试用期已过 → 计入探测预算，累计到 3 次才回滚 —— 把「core 停机/网络割接」与
// 「新版本坏了」区分开（前者过一会儿就能连上，回滚反而是白折腾一次）。
func (r *Runtime) OnConnectFailed() {
	tx := readTransaction(r.deps.StateDir)
	if tx == nil || tx.Phase != phasePending {
		return
	}
	now := r.deps.Now()
	if now.Before(tx.At.Add(r.deps.TrialWindow)) {
		return // 试用期内：连接失败是正常现象
	}
	tx.ProbeFailures++
	if err := writeTransaction(r.deps.StateDir, tx); err != nil {
		r.log.Warn("persist probe failure failed", "err", err.Error())
	}
	if tx.ProbeFailures < probeBudget {
		r.log.Info("upgrade probation probe failed",
			"probe", tx.ProbeFailures, "budget", probeBudget)
		return
	}
	r.rollback(tx, agentproto.ReasonNotConnectedAfterUpgrade, "试用期内未能连上服务端")
}

// rollback 还原备份并重启回旧版本（**设备侧自治**，不依赖服务端）。
//
// 调用点有两处：启动检查发现崩溃循环、试用期满后探测预算耗尽。
func (r *Runtime) rollback(tx *transaction, reasonCode, detail string) {
	exe, err := r.exePath()
	if err != nil {
		r.log.Warn("rollback: locate executable failed", "err", err.Error())
		return
	}
	if _, err := os.Stat(tx.BackupPath); err != nil {
		// 备份缺失：**不停机**（宁可留在新版本上，也不要让设备彻底离线），
		// 但要把这件事说清楚 —— 上报失败，并清掉事务（否则每次重连都判一次）。
		r.log.Warn("rollback impossible: backup missing", "backup", tx.BackupPath, "err", err.Error())
		if err := clearTransaction(r.deps.StateDir); err != nil {
			r.log.Warn("clear transaction failed", "err", err.Error())
		}
		st := &agentproto.UpgradeStatus{
			RequestID: tx.RequestID, State: agentproto.UpgradeStateFailed,
			TargetVersion: tx.ToVersion, FromVersion: tx.FromVersion,
			ReasonCode: agentproto.ReasonReplaceFailed,
		}
		r.sendStatus(st)
		return
	}
	if err := os.Rename(tx.BackupPath, exe); err != nil {
		r.log.Warn("rollback: restore backup failed", "err", err.Error())
		return
	}
	// 先把「已回滚」写进事务，再 exec：旧版本起来后会读到它并上报（设计 §6.3 第 5 步）。
	tx.Phase = phaseRolledBack
	tx.Reason = reasonCode
	tx.Detail = detail
	tx.Previous = exe
	if err := writeTransaction(r.deps.StateDir, tx); err != nil {
		r.log.Warn("write rolled-back marker failed", "err", err.Error())
	}
	r.log.Warn("rolled back to previous version",
		"from", tx.ToVersion, "to", tx.FromVersion, "reason", reasonCode)
	if r.deps.Exec == nil {
		return // 测试态：不真的重启
	}
	_ = r.deps.Exec(exe, os.Args, os.Environ())
}

// reportRolledBack 由被回滚回来的旧版本上报「我回滚了」，随后清标记。
func (r *Runtime) reportRolledBack(tx *transaction) {
	code := tx.Reason
	if code == "" {
		code = agentproto.ReasonNotConnectedAfterUpgrade
	}
	r.sendStatus(&agentproto.UpgradeStatus{
		RequestID: tx.RequestID, State: agentproto.UpgradeStateRolledBack,
		TargetVersion: tx.ToVersion, FromVersion: tx.FromVersion,
		ReasonCode: code, ReasonDetail: tx.Detail,
	})
	// 退避记录：让被回滚到的这个版本在未来 10 分钟内不被同一 request_id 重试
	// （服务端本就不会重发，这里只是第二道防线）。
	r.setAttempt(attemptRecord{RequestID: tx.RequestID, Version: tx.ToVersion,
		At: r.deps.Now(), Result: "rolled_back"})
	if err := clearTransaction(r.deps.StateDir); err != nil {
		r.log.Warn("clear transaction after rollback report failed", "err", err.Error())
	}
}

// fail 上报失败并落盘退避记录。detail 只进排障字段，不进页面（设计 §10）。
func (r *Runtime) fail(d *agentproto.UpgradeDirective, code, detail string, cause error) {
	if cause != nil {
		detail = detail + ": " + cause.Error()
	}
	r.log.Warn("upgrade failed", "target", d.TargetVersion, "reason", code, "detail", detail)
	r.sendStatus(&agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateFailed,
		TargetVersion: d.TargetVersion, FromVersion: r.deps.Version,
		ReasonCode: code, ReasonDetail: agentproto.NormalizeReasonDetail(detail),
	})
	r.setAttempt(attemptRecord{RequestID: d.RequestID, Version: d.TargetVersion,
		At: r.deps.Now(), Result: code})
}

// report 上报一次中间阶段。
func (r *Runtime) report(d *agentproto.UpgradeDirective, state string, progress *int) {
	r.sendStatus(&agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: state,
		TargetVersion: d.TargetVersion, FromVersion: r.deps.Version,
		Progress: progress,
	})
}

func (r *Runtime) sendStatus(st *agentproto.UpgradeStatus) {
	if r.deps.SendStatus == nil {
		return
	}
	if err := r.deps.SendStatus(st); err != nil {
		r.log.Warn("send upgrade status failed", "state", st.State, "err", err.Error())
	}
}

// inBackoff 判定「同一个 request_id 是否在退避窗口内」。
func (r *Runtime) inBackoff(d *agentproto.UpgradeDirective) bool {
	rec := r.lastAttempt
	if rec == nil || rec.RequestID != d.RequestID {
		return false
	}
	return r.deps.Now().Sub(rec.At) < retryBackoff
}

// setAttempt 记录一次尝试（内存 + 落盘）。
func (r *Runtime) setAttempt(rec attemptRecord) {
	r.mu.Lock()
	r.lastAttempt = &rec
	r.mu.Unlock()
	writeAttemptRecord(r.deps.StateDir, rec)
}

func (r *Runtime) exePath() (string, error) {
	if r.deps.Executable != nil {
		return r.deps.Executable()
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	// 解析软链：安装布局可能是 /usr/local/bin/uni_agent -> /opt/uni_agent/x.y.z。
	// 替换**解析后的真实文件**，软链保持指向它 —— 这样原子替换不会把软链本身改掉。
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, nil
	}
	return p, nil
}

// errNoPermission 用于把「目录不可写」与「网络问题」区分开（前者要人上机处理）。
var errNoPermission = errors.New("no write permission")

// ── 文件与记录辅助 ──────────────────────────────────────────────────────

// copyFile 复制并保留可执行位（备份当前二进制用）。只复制内容，不复制元数据。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func readAttemptRecord(dir string) *attemptRecord {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, attemptFile))
	if err != nil {
		return nil
	}
	var rec attemptRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil
	}
	return &rec
}

func writeAttemptRecord(dir string, rec attemptRecord) {
	if dir == "" {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// 与 token 同款：临时文件 + rename，避免半截 JSON 让下次启动读不出退避。
	path := filepath.Join(dir, attemptFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}

// progressReader 统计已读字节并按节流上报百分比。
type progressReader struct {
	r          io.Reader
	total      int64
	read       int64
	lastPct    int
	lastAt     time.Time
	now        func() time.Time
	onProgress func(int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total <= 0 || p.onProgress == nil {
		return n, err
	}
	pct := int(p.read * 100 / p.total)
	if pct > 100 {
		pct = 100
	}
	if pct-p.lastPct < progressStep {
		return n, err
	}
	// 每秒最多一条（与「跨 5% 档」取与）：25MB 内网几秒下完时只会有 1–2 个采样点，
	// 这是真实情况而不是缺显示（设计 §6）。
	if now := p.now(); !p.lastAt.IsZero() && now.Sub(p.lastAt) < time.Second && pct < 100 {
		return n, err
	}
	p.lastPct = pct
	p.lastAt = p.now()
	p.onProgress(pct)
	return n, err
}
