package upgrade

import (
	"os"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 事务测试覆盖本地自愈的**全部判定路径**。这一组用例的价值在于：它们描述的是
// 「远程回滚失效时设备如何自救」——生产里几乎不会有人手动演练它。

// pendingTx 造一个「替换刚完成、新版本刚起来」的现场。
func (e *testEnv) pendingTx(t *testing.T, at time.Time, attempts int) *transaction {
	t.Helper()
	// 备份必须是**旧版本内容**：回滚的语义就是把它还原回去。
	if err := os.WriteFile(e.exe+".prev", []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	tx := &transaction{
		FromVersion: "0.1.0", ToVersion: "0.2.0",
		BackupPath: e.exe + ".prev", RequestID: "42",
		Phase: phasePending, At: at, Attempts: attempts,
	}
	if err := writeTransaction(e.dir, tx); err != nil {
		t.Fatal(err)
	}
	// 现在跑着的「新版本」：磁盘上是新内容。
	if err := os.WriteFile(e.exe, []byte("NEW-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	return tx
}

func (e *testEnv) checkDeps() Deps {
	return Deps{
		Version:      "0.2.0",
		StateDir:     e.dir,
		SendStatus:   e.recorder.send,
		GOOS:         "linux",
		Executable:   func() (string, error) { return e.exe, nil },
		Exec:         func(argv0 string, argv, envv []string) error { e.addExec(argv0); return nil },
		Now:          func() time.Time { return e.now },
		HTTPClient:   e.server.Client(),
		SmokeTimeout: time.Second,
	}
}

// TestStartupConfirmClearsTransaction：新版本连上即确认（成功由服务端在 hello 裁决，
// 设备侧只需把事务销掉）。
func TestStartupConfirmClearsTransaction(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	env.pendingTx(t, env.now, 0)

	restart, err := CheckOnStartup(env.checkDeps())
	if err != nil || restart {
		t.Fatalf("首次启动不该回滚: restart=%v err=%v", restart, err)
	}
	tx := readTransaction(env.dir)
	if tx == nil || tx.Attempts != 1 {
		t.Fatalf("启动计数应 +1: %+v", tx)
	}

	r := New(env.checkDeps())
	r.OnConnected()
	if tx := readTransaction(env.dir); tx != nil {
		t.Fatalf("连上之后事务应被清除（升级确认）: %+v", tx)
	}
	if got := env.readExe(t); got != "NEW-BINARY" {
		t.Fatalf("确认后不该动文件: %q", got)
	}
	if len(env.recorder.states()) != 0 {
		t.Fatalf("确认路径不该上报任何状态（成功只在 hello 里裁决）: %v", env.recorder.states())
	}
}

// TestCrashLoopRollsBackImmediately：起来 N 次都没确认 = 崩溃循环，
// 不必等满试用期（否则一台崩循环的机器要等 3 分钟才自愈）。
func TestCrashLoopRollsBackImmediately(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	env.pendingTx(t, env.now, maxStartAttempts-1) // 已经起来过两次

	restart, err := CheckOnStartup(env.checkDeps())
	if err != nil {
		t.Fatal(err)
	}
	if !restart {
		t.Fatal("崩溃循环应判回滚（restart=true）")
	}
	if env.execCount() != 1 {
		t.Fatalf("回滚后应 exec 旧版本: %v", env.execCalls)
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("应把备份还原回去，实得 %q", got)
	}
	tx := readTransaction(env.dir)
	if tx == nil || tx.Phase != phaseRolledBack ||
		tx.Reason != agentproto.ReasonNotConnectedAfterUpgrade {
		t.Fatalf("事务应留下「已回滚」标记供旧版本上报: %+v", tx)
	}
}

// TestProbeBudgetRollsBackOnlyAfterTrial：试用期满后**先探测再回滚**（D3）——
// 这条区分了「新版本坏了」与「core 暂时不可达」（后者探测几次就能连上）。
func TestProbeBudgetRollsBackOnlyAfterTrial(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	env.pendingTx(t, env.now.Add(-trialWindow-time.Minute), 1) // 试用期已过

	r := New(env.checkDeps())
	for i := 1; i < probeBudget; i++ {
		r.OnConnectFailed()
		if env.execCount() != 0 {
			t.Fatalf("探测预算未耗尽不该回滚（第 %d 次）", i)
		}
	}
	r.OnConnectFailed() // 第 probeBudget 次
	if env.execCount() != 1 {
		t.Fatalf("探测预算耗尽应回滚: %v", env.execCalls)
	}
	if got := env.readExe(t); got != "OLD-BINARY" {
		t.Fatalf("应还原备份: %q", got)
	}
	if tx := readTransaction(env.dir); tx == nil || tx.Phase != phaseRolledBack {
		t.Fatalf("应留下回滚标记: %+v", tx)
	}
}

// TestProbeWithinTrialDoesNotCount：试用期内的连接失败只是正常重连，不计数。
func TestProbeWithinTrialDoesNotCount(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	env.pendingTx(t, env.now, 1) // 试用期刚开始

	r := New(env.checkDeps())
	for i := 0; i < probeBudget*3; i++ {
		r.OnConnectFailed()
	}
	if env.execCount() != 0 {
		t.Fatalf("试用期内不该回滚（实得 %d 次）", env.execCount())
	}
	if tx := readTransaction(env.dir); tx == nil || tx.ProbeFailures != 0 {
		t.Fatalf("试用期内不该累计探测失败: %+v", tx)
	}
}

// TestBackupMissingKeepsDeviceRunning：备份没了**不停机** ——
// 宁可留在新版本上，也不要让设备彻底离线；但要如实上报「还原失败」。
func TestBackupMissingKeepsDeviceRunning(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	env.pendingTx(t, env.now.Add(-trialWindow-time.Minute), 1)
	if err := os.Remove(env.exe + ".prev"); err != nil {
		t.Fatal(err)
	}
	r := New(env.checkDeps())
	for i := 0; i < probeBudget; i++ {
		r.OnConnectFailed()
	}
	if env.execCount() != 0 {
		t.Fatal("备份缺失时不该 exec（会把设备留在没有任何可执行文件的状态）")
	}
	if got := env.readExe(t); got != "NEW-BINARY" {
		t.Fatalf("不该动文件: %q", got)
	}
	last := env.recorder.last()
	if last == nil || last.State != agentproto.UpgradeStateFailed ||
		last.ReasonCode != agentproto.ReasonReplaceFailed {
		t.Fatalf("应上报「还原失败」: %+v", last)
	}
	if tx := readTransaction(env.dir); tx != nil {
		t.Fatalf("应清掉事务（否则每次重连都会重判一次）: %+v", tx)
	}
}

// TestRolledBackReportedOnceOnConnect：被回滚回来的旧版本在**连上之后**上报这条事实，
// 然后清掉标记 —— 回滚现场往往根本连不上，所以上报必须推迟到这里。
func TestRolledBackReportedOnceOnConnect(t *testing.T) {
	env := newTestEnv(t, []byte("x"), "0.2.0")
	tx := &transaction{
		FromVersion: "0.1.0", ToVersion: "0.2.0", RequestID: "42",
		Phase: phaseRolledBack, At: env.now.Add(-time.Hour),
		Reason: agentproto.ReasonNotConnectedAfterUpgrade, Detail: "试用期内未能连上服务端",
	}
	if err := writeTransaction(env.dir, tx); err != nil {
		t.Fatal(err)
	}
	// 启动检查：识别出「我是被回滚回来的」，但不回滚（已经回滚过了）。
	restart, err := CheckOnStartup(env.checkDeps())
	if err != nil || restart {
		t.Fatalf("已回滚的标记不该再次触发回滚: restart=%v err=%v", restart, err)
	}

	r := New(env.checkDeps())
	r.OnConnected()
	last := env.recorder.last()
	if last == nil || last.State != agentproto.UpgradeStateRolledBack ||
		last.ReasonCode != agentproto.ReasonNotConnectedAfterUpgrade ||
		last.RequestID != "42" {
		t.Fatalf("应上报「已回滚 + 原因」: %+v", last)
	}
	if tx := readTransaction(env.dir); tx != nil {
		t.Fatalf("上报后应清掉标记: %+v", tx)
	}

	// 再连一次：不重复上报（标记已清）。
	before := len(env.recorder.states())
	r.OnConnected()
	if after := len(env.recorder.states()); after != before {
		t.Fatalf("不该重复上报: %v", env.recorder.states())
	}
}
