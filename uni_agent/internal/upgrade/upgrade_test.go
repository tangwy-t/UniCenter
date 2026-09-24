package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 收集状态上报的桩（断言「报了什么」是这些用例的主要手段）。
type statusRecorder struct {
	mu   sync.Mutex
	got  []*agentproto.UpgradeStatus
	err  error
	fail bool
}

func (s *statusRecorder) send(st *agentproto.UpgradeStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return s.err
	}
	s.got = append(s.got, st)
	return nil
}

func (s *statusRecorder) states() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.got))
	for _, st := range s.got {
		out = append(out, st.State)
	}
	return out
}

func (s *statusRecorder) last() *agentproto.UpgradeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) == 0 {
		return nil
	}
	return s.got[len(s.got)-1]
}

func (s *statusRecorder) contains(state string) bool {
	for _, st := range s.states() {
		if st == state {
			return true
		}
	}
	return false
}

// testEnv 是一套完全注入的运行环境：临时目录当安装目录、假的下载服务、
// 不真的 exec、不真的跑冒烟子进程。
type testEnv struct {
	dir      string // 状态目录
	exeDir   string // 「安装目录」（可执行文件所在目录）
	exe      string // 当前二进制路径
	server   *httptest.Server
	recorder *statusRecorder
	// mu 保护下面三个收集器：它们由升级 goroutine 写、由测试读（-race 会抓）。
	mu         sync.Mutex
	execCalls  []string
	sleeps     []time.Duration
	savedToken string
	now        time.Time
	// sha 是服务端内容的摘要（构造指令时用它；测试里「改摘要」即可模拟不符）。
	sha string
	// newVersion 是「新二进制」自述的版本（冒烟自检读它）—— 注意它与 Deps.Version
	// （当前版本）**不同**：冒烟要证明的正是「装上去的这一份是对的目标版本」。
	newVersion string
}

func newTestEnv(t *testing.T, body []byte, version string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "uni_agent")
	if err := os.WriteFile(exe, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	env := &testEnv{
		dir: dir, exeDir: exeDir, exe: exe,
		recorder:   &statusRecorder{},
		now:        time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		sha:        hex.EncodeToString(sum[:]),
		newVersion: version,
	}
	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(env.server.Close)
	// 指令里的 sha/size 由调用方按 body 计算；这里把 version 记在 env 上便于构造。
	t.Logf("body sha=%s version=%s", hex.EncodeToString(sum[:]), version)
	return env
}

func (e *testEnv) deps(version string, body []byte) Deps {
	return Deps{
		Version:      version,
		StateDir:     e.dir,
		DownloadBase: e.server.URL,
		Token:        func() string { return "tok" },
		SendStatus:   e.recorder.send,
		SaveToken:    func(tok string) error { e.saveToken(tok); return nil },
		GOOS:         "linux",
		Executable:   func() (string, error) { return e.exe, nil },
		Exec: func(argv0 string, argv, envv []string) error {
			e.addExec(argv0)
			// 生产里的 syscall.Exec 成功时不返回；测试里返回即代表「exec 失败」，
			// 于是运行时应当还原备份并上报失败 —— 这条路径同样必须被覆盖。
			return errors.New("test: exec returned")
		},
		Now:          e.getNow,
		Sleep:        func(_ context.Context, d time.Duration) { e.addSleep(d) },
		HTTPClient:   e.server.Client(),
		SmokeRun:     func(context.Context, string) (string, error) { return "uni_agent " + e.newVersion, nil },
		SmokeTimeout: time.Second,
	}
}

func (e *testEnv) setNow(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = t
}

func (e *testEnv) getNow() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *testEnv) addExec(argv0 string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.execCalls = append(e.execCalls, argv0)
}

func (e *testEnv) execCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.execCalls)
}

func (e *testEnv) addSleep(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sleeps = append(e.sleeps, d)
}

func (e *testEnv) sleepCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sleeps)
}

func (e *testEnv) saveToken(tok string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.savedToken = tok
}

func (e *testEnv) token() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.savedToken
}

func (e *testEnv) readExe(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDirectiveSkipsSameVersion(t *testing.T) {
	env := newTestEnv(t, []byte("NEW"), "0.2.0")
	deps := env.deps("0.2.0", []byte("NEW"))
	r := New(deps)

	r.OnDirective(&agentproto.UpgradeDirective{
		RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: 3,
	})
	if len(env.recorder.states()) != 0 {
		t.Fatalf("已是目标版本时不该上报任何状态: %v", env.recorder.states())
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("不该替换文件: %q", got)
	}
}

func TestNonLinuxRejected(t *testing.T) {
	env := newTestEnv(t, []byte("NEW"), "0.2.0")
	deps := env.deps("0.1.0", []byte("NEW"))
	deps.GOOS = "windows"
	r := New(deps)

	r.OnDirective(&agentproto.UpgradeDirective{
		RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: 3,
	})
	waitFor(t, func() bool { return env.recorder.last() != nil })
	last := env.recorder.last()
	if last.State != agentproto.UpgradeStateFailed ||
		last.ReasonCode != agentproto.ReasonPlatformUnsupported {
		t.Fatalf("非 linux 应上报「平台不支持」: %+v", last)
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("不该替换文件: %q", got)
	}
}

// TestUpgradePipelineReplacesAndRestarts 覆盖成功路径的**替换前状态**。
//
// exec 在测试里会返回（生产里成功不返回），运行时的语义是「返回即失败」——
// 故这里能同时验证两件事：exec 之前的状态正确（新二进制在位、事务 pending、
// 备份存在、restarting 已上报、token 已落盘），以及 exec 失败后会还原。
func TestUpgradePipelineReplacesAndRestarts(t *testing.T) {
	body := []byte("NEW-BINARY-CONTENT")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)

	type execSnapshot struct {
		exe        string
		tx         *transaction
		backupOK   bool
		restarting bool
		tokenSaved string
	}
	var (
		snapMu  sync.Mutex
		atExec  execSnapshot
		haveSmp bool
	)
	deps.Exec = func(argv0 string, argv, envv []string) error {
		env.addExec(argv0)
		var snap execSnapshot
		if b, err := os.ReadFile(env.exe); err == nil {
			snap.exe = string(b)
		}
		snap.tx = readTransaction(env.dir)
		if _, err := os.Stat(env.exe + ".prev"); err == nil {
			snap.backupOK = true
		}
		snap.restarting = env.recorder.contains(agentproto.UpgradeStateRestarting)
		snap.tokenSaved = env.token()
		snapMu.Lock()
		atExec, haveSmp = snap, true
		snapMu.Unlock()
		return errors.New("test: exec returned")
	}
	r := New(deps)
	r.OnDirective(&agentproto.UpgradeDirective{
		RequestID: "42", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: int64(len(body)),
	})
	waitFor(t, func() bool { return env.execCount() == 1 })
	snapMu.Lock()
	got := atExec
	ok := haveSmp
	snapMu.Unlock()
	if !ok {
		t.Fatal("exec 未发生")
	}
	atExec = got

	if atExec.exe != string(body) {
		t.Fatalf("exec 之前新二进制应在位，实得 %q", atExec.exe)
	}
	if atExec.tx == nil || atExec.tx.Phase != phasePending ||
		atExec.tx.RequestID != "42" || atExec.tx.FromVersion != "0.1.0" {
		t.Fatalf("事务应在替换前落盘为 pending: %+v", atExec.tx)
	}
	if !atExec.backupOK {
		t.Fatal("替换前必须留下备份（本地回滚的唯一依据）")
	}
	if !atExec.restarting {
		t.Fatalf("重启前应上报 restarting: %v", env.recorder.states())
	}
	if atExec.tokenSaved != "tok" {
		t.Fatalf("重启前必须落盘 token（exec 会绕过 main 的退出路径），实得 %q", atExec.tokenSaved)
	}

	// exec 返回 = 失败：必须还原备份且如实上报。
	waitFor(t, func() bool {
		st := env.recorder.last()
		return st != nil && st.State == agentproto.UpgradeStateFailed
	})
	if last := env.recorder.last(); last.ReasonCode != agentproto.ReasonExecFailed {
		t.Fatalf("exec 失败应上报 exec_failed: %+v", last)
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("exec 失败后应还原旧版本，实得 %q", got)
	}
	if tx := readTransaction(env.dir); tx != nil {
		t.Fatalf("失败后事务应被清除: %+v", tx)
	}
}

func TestChecksumMismatchRejectsWithoutReplacing(t *testing.T) {
	body := []byte("SERVED-BYTES")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)
	env.sha = strings.Repeat("f", 64) // 指令里的摘要与服务端内容不符
	r := New(deps)

	r.OnDirective(&agentproto.UpgradeDirective{
		RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: int64(len(body)),
	})
	waitFor(t, func() bool { return env.recorder.last() != nil })
	if last := env.recorder.last(); last.State != agentproto.UpgradeStateFailed ||
		last.ReasonCode != agentproto.ReasonChecksumMismatch {
		t.Fatalf("摘要不符应上报 checksum_mismatch: %+v", last)
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("摘要不符绝不能替换: %q", got)
	}
	if env.execCount() != 0 {
		t.Fatal("摘要不符不该 exec")
	}
	// 临时文件必须清掉（否则安装目录里会积累半截二进制）。
	entries, _ := os.ReadDir(env.exeDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".uni_agent.new-") {
			t.Fatalf("临时文件未清理: %s", e.Name())
		}
	}
}

func TestSmokeFailureRejectsWithoutReplacing(t *testing.T) {
	body := []byte("RUNS-BUT-WRONG-VERSION")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)

	t.Run("跑不起来", func(t *testing.T) {
		d := deps
		d.SmokeRun = func(context.Context, string) (string, error) {
			return "", errors.New("exec format error")
		}
		r := New(d)
		r.OnDirective(&agentproto.UpgradeDirective{
			RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: int64(len(body)),
		})
		waitFor(t, func() bool { return env.recorder.last() != nil })
		if last := env.recorder.last(); last.ReasonCode != agentproto.ReasonSmokeTestFailed {
			t.Fatalf("冒烟失败应上报 smoke_test_failed: %+v", last)
		}
		if got := env.readExe(t); got != "OLD-BINARY" {
			t.Fatalf("冒烟失败绝不能替换: %q", got)
		}
	})

	t.Run("自述版本不符", func(t *testing.T) {
		env2 := newTestEnv(t, body, "0.2.0")
		d := env2.deps("0.1.0", body)
		d.SmokeRun = func(context.Context, string) (string, error) {
			return "uni_agent 0.1.9", nil
		}
		r := New(d)
		r.OnDirective(&agentproto.UpgradeDirective{
			RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: int64(len(body)),
		})
		waitFor(t, func() bool { return env2.recorder.last() != nil })
		last := env2.recorder.last()
		if last.ReasonCode != agentproto.ReasonSmokeTestFailed {
			t.Fatalf("自述版本不符应上报 smoke_test_failed: %+v", last)
		}
		if !strings.Contains(last.ReasonDetail, "0.1.9") {
			t.Fatalf("原因细节应含实际自述版本（排障线索）: %q", last.ReasonDetail)
		}
		if got := env2.readExe(t); got != "OLD-BINARY" {
			t.Fatalf("绝不能替换: %q", got)
		}
	})
}

// TestBackoffOnSameRequest 钉住两条语义：
//   - 同一个 request_id 的重投递（重连时的 hello_ack 会重发）不重复下载；
//   - 换 request_id（服务端开了新一次尝试 = 操作员点了重试）立即执行。
func TestBackoffOnSameRequest(t *testing.T) {
	body := []byte("NEW")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)
	deps.SmokeRun = func(context.Context, string) (string, error) {
		return "", errors.New("always fails")
	}
	var hits int
	var mu sync.Mutex
	env.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write(body)
	})
	r := New(deps)

	d := &agentproto.UpgradeDirective{RequestID: "7", TargetVersion: "0.2.0",
		SHA256: env.sha, SizeBytes: int64(len(body))}
	r.OnDirective(d)
	waitFor(t, func() bool { return env.recorder.last() != nil })
	mu.Lock()
	first := hits
	mu.Unlock()

	// 同一 request_id 重投：退避窗口内跳过（不再下载）。
	r.OnDirective(d)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if hits != first {
		t.Fatalf("同一 request_id 的重投递不该重复下载（hits=%d）", hits)
	}
	mu.Unlock()

	// 新 request_id：立即执行。
	r.OnDirective(&agentproto.UpgradeDirective{RequestID: "8", TargetVersion: "0.2.0",
		SHA256: env.sha, SizeBytes: int64(len(body))})
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits > first
	})
}

func TestProgressReaderThrottles(t *testing.T) {
	// total=100，分 10 次读 10 字节：应上报 10/20/…/100，且每秒最多一条。
	var got []int
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := &progressReader{
		r:     strings.NewReader(strings.Repeat("x", 100)),
		total: 100,
		now:   func() time.Time { return now },
		onProgress: func(pct int) {
			got = append(got, pct)
			now = now.Add(2 * time.Second) // 每次上报后推进时钟，绕过 1 秒节流
		},
	}
	buf := make([]byte, 10)
	for {
		if _, err := p.Read(buf); err != nil {
			break
		}
	}
	if len(got) < 5 {
		t.Fatalf("应至少有 5 次进度上报，实得 %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("进度必须单调递增: %v", got)
		}
	}
	if got[len(got)-1] != 100 {
		t.Fatalf("最后一次应为 100: %v", got)
	}

	// 时钟不推进时：一条都不发（1 秒节流）。
	got = nil
	frozen := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p2 := &progressReader{r: strings.NewReader(strings.Repeat("x", 100)), total: 100,
		now:        func() time.Time { return frozen },
		onProgress: func(pct int) { got = append(got, pct) }}
	for {
		if _, err := p2.Read(buf); err != nil {
			break
		}
	}
	// 首条不受 1 秒节流约束（时钟还没走过，lastAt 为零值）：让运维立刻看到
	// 「下载开始了」；之后按节流静默，直到完成的那一条（100% 永远上报）。
	if len(got) != 2 || got[0] != 10 || got[1] != 100 {
		t.Fatalf("冻结时钟下应为 [10 100]，实得 %v", got)
	}
}

func TestJitterInjectedInTests(t *testing.T) {
	body := []byte("NEW")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)
	deps.JitterMax = 0 // 测试默认不抖
	r := New(deps)
	r.OnDirective(&agentproto.UpgradeDirective{RequestID: "1", TargetVersion: "0.2.0",
		SHA256: env.sha, SizeBytes: int64(len(body))})
	waitFor(t, func() bool { return env.execCount() == 1 || env.recorder.last() != nil })
	if n := env.sleepCount(); n != 0 {
		t.Fatalf("JitterMax=0 不该有等待（实得 %d 次）", n)
	}
}

// waitFor 等待条件成立（自升级流程在 goroutine 里跑）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// TestSmokeSeesExecutableTempFile 钉住冒烟自检之前的**文件模式**。
//
// 由来（端到端抓到的真问题）：临时文件以 0600 下载（防止下载中被执行半截文件），
// 而冒烟自检要 fork/exec 它 —— 顺序反了会得到 `permission denied`，
// 上报的原因码是「新版本无法启动」（不算撒谎，但真正的原因是流程顺序）。
// 单元测试此前看不见它，因为冒烟是注入的、从不真的 exec。
func TestSmokeSeesExecutableTempFile(t *testing.T) {
	body := []byte("NEW-BINARY-CONTENT")
	env := newTestEnv(t, body, "0.2.0")
	deps := env.deps("0.1.0", body)
	// 冒烟替身：在「被执行的那一刻」检查文件权限位。
	// 用互斥保护：写入发生在升级 goroutine，读取在测试里（-race 会抓）。
	var mu sync.Mutex
	var mode os.FileMode
	deps.SmokeRun = func(_ context.Context, bin string) (string, error) {
		fi, err := os.Stat(bin)
		if err != nil {
			return "", err
		}
		mu.Lock()
		mode = fi.Mode()
		mu.Unlock()
		return "uni_agent " + env.newVersion, nil
	}
	r := New(deps)
	r.OnDirective(&agentproto.UpgradeDirective{
		RequestID: "1", TargetVersion: "0.2.0", SHA256: env.sha, SizeBytes: int64(len(body)),
	})
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return mode != 0
	})
	mu.Lock()
	got := mode
	mu.Unlock()
	if got.Perm()&0o111 == 0 {
		t.Fatalf("冒烟自检时临时文件必须已可执行，实得 %v（顺序反了会 permission denied）", got.Perm())
	}
}

// TestNewForProcessFillsProcessDependencies 钉住「生产构造函数必须填满三个进程依赖」。
//
// 由来（端到端抓到的缺陷）：main 曾用裸 New 构造，Exec 为空 —— 升级流程把文件
// 替换成 0.2.0 之后**没有重启进程**：设备仍跑旧代码、上报旧版本，服务端永远显示
// 「升级中」。这类漏设不会报错，只在真的升级时暴露，故用测试把它变成红灯。
func TestNewForProcessFillsProcessDependencies(t *testing.T) {
	r := NewForProcess(Deps{Version: "0.1.0", StateDir: t.TempDir()})
	if r.deps.Exec == nil {
		t.Fatal("NewForProcess 必须注入 Exec（否则替换后不会重启进程）")
	}
	if r.deps.Executable == nil {
		t.Fatal("NewForProcess 必须注入 Executable（否则定位不到自身路径）")
	}
	if r.deps.GOOS == "" {
		t.Fatal("NewForProcess 必须注入 GOOS（否则平台检查失效）")
	}
	if r.deps.GOOS != runtime.GOOS {
		t.Fatalf("GOOS 应取运行时值，实得 %q", r.deps.GOOS)
	}
}
