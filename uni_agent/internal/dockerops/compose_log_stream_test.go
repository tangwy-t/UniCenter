package dockerops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 注入替身：能产出真实 stdout/stderr 的假 compose 进程 ────────────────────
//
// composeExecStub（compose_exec_test.go）只回成败，不产数据 —— 聚合日志要验证的
// 恰是「CLI 输出逐行成帧」，故这里的替身控制**输出内容**与**驻留行为**（follow 会话
// 要一直活着才能测取消杀进程）。宿主无关性与既有替身同一手法：测试二进制自身 +
// TestComposeLogsHelperProcess 入口。

// composeLogsStub 是注入 StreamExecutor 的 compose CLI 替身。
type composeLogsStub struct {
	mu sync.Mutex
	// calls 记录 (name, args) 供 argv 断言（与 composeExecStub 同一形状）。
	calls []composeCall
	// stdout / stderr 是替身进程要写出的内容（stderr 验证「合流」纪律）。
	stdout string
	stderr string
	// hold=true 时替身进程驻留 10 分钟（只能被 SIGKILL 打断 —— cancel 即杀进程
	// 的断言依据）。
	hold bool
	// cmds 记录交出的 *exec.Cmd（测试用 Process 存活探测断言「取消杀死了进程」）。
	cmds []*exec.Cmd
}

func (s *composeLogsStub) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, composeCall{name: name, args: append([]string(nil), args...)})
	hold := "0"
	if s.hold {
		hold = "1"
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestComposeLogsHelperProcess$")
	cmd.Env = append(os.Environ(),
		"UNI_TEST_COMPOSE_LOGS_HELPER=1",
		"UNI_TEST_COMPOSE_LOGS_STDOUT="+s.stdout,
		"UNI_TEST_COMPOSE_LOGS_STDERR="+s.stderr,
		"UNI_TEST_COMPOSE_LOGS_HOLD="+hold,
	)
	s.cmds = append(s.cmds, cmd)
	return cmd
}

func (s *composeLogsStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *composeLogsStub) last(t *testing.T) composeCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("compose CLI 没有被执行")
	}
	return s.calls[len(s.calls)-1]
}

func (s *composeLogsStub) lastCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cmds) == 0 {
		t.Fatal("没有交出过 exec.Cmd")
	}
	return s.cmds[len(s.cmds)-1]
}

// TestComposeLogsHelperProcess 不是测试：它是 composeLogsStub 启动的假 compose 进程
// （stdout/stderr 按 env 写出；HOLD=1 时驻留等 SIGKILL）。
func TestComposeLogsHelperProcess(t *testing.T) {
	if os.Getenv("UNI_TEST_COMPOSE_LOGS_HELPER") != "1" {
		return
	}
	if s := os.Getenv("UNI_TEST_COMPOSE_LOGS_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("UNI_TEST_COMPOSE_LOGS_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	if os.Getenv("UNI_TEST_COMPOSE_LOGS_HOLD") == "1" {
		time.Sleep(10 * time.Minute)
	}
	os.Exit(0)
}

// newComposeLogsExecutor 造一个注入了替身 CLI 的流执行器（三 flavor 公共夹具）。
func newComposeLogsExecutor(api DockerAPI, stub *composeLogsStub, flavor string) (*StreamExecutor, *SessionManager, *frameSink, *fakeClock) {
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	e.SetComposeCLI(func() string { return flavor }, stub.command)
	return e, m, sink, clk
}

// ── argv 逐字断言（防注入 + flavor 形态 + 公共子集纪律）────────────────────

// argv 必须逐字可预期（照 TestComposeArgv 的取向）：聚合日志的 argv 一旦串上
// flag 分隔符或引入 shell，等于把宿主机 root 交给请求方。三种 flavor 形态
// （plugin 带 compose 子命令 / 独立二进制不带）与 follow/tail 两个开关都覆盖。
func TestComposeLogsArgv(t *testing.T) {
	cases := []struct {
		name   string
		follow bool
		tail   int
		// want 是 **flavor 无关** 的尾部（plugin 的 "compose" 前缀由用例拼上）。
		want []string
	}{
		{"tail 缺省 = 100（与 container:logs 同默认）", true, 0,
			[]string{"-p", "uni-center", "-f", testComposeConfig, "logs", "--timestamps", "--tail", "100", "--follow"}},
		{"tail 显式 50、非 follow", false, 50,
			[]string{"-p", "uni-center", "-f", testComposeConfig, "logs", "--timestamps", "--tail", "50"}},
	}
	for _, fc := range composeFlavorCases {
		t.Run(fc.flavor, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					argv := composeLogsArgv(fc.flavor, "uni-center", []string{testComposeConfig}, c.follow, c.tail)
					want := append(append([]string(nil), fc.prefix...), c.want...)
					if !slices.Equal(argv, want) {
						t.Fatalf("argv 必须逐字相等:\n got %q\nwant %q", argv, want)
					}
					// 公共子集纪律：--since/--until 在 v1 上不存在（协议层拒 since，
					// argv 侧同样不得出现）；shell 元字符同理不得引入。
					if slices.Contains(argv, "--since") || slices.Contains(argv, "--until") {
						t.Fatalf("argv 不得出现 --since/--until（v1 公共子集纪律）: %q", argv)
					}
					if slices.Contains(argv, "sh") || slices.Contains(argv, "-c") {
						t.Fatalf("argv 不得引入 shell: %q", argv)
					}
				})
			}
		})
	}
}

// 多文件项目（override）：-f 必须按标签原序全部保留（compose 的 merge 语义，
// 与写路径 TestComposeExecutorKeepsOverrideFiles 同一纪律）。
func TestComposeLogsArgvKeepsOverrideFiles(t *testing.T) {
	main := "/data/proj/docker-compose.yml"
	override := "/data/proj/docker-compose.override.yml"
	argv := composeLogsArgv(agentproto.DockerComposeFlavorPlugin, "uni-center",
		[]string{main, override}, true, 100)
	want := []string{"compose", "-p", "uni-center", "-f", main, "-f", override,
		"logs", "--timestamps", "--tail", "100", "--follow"}
	if !slices.Equal(argv, want) {
		t.Fatalf("override 的 -f 必须按原序保留:\n got %q\nwant %q", argv, want)
	}
}

// 执行路径上的 argv 整体再钉一次（不只 composeLogsArgv 单元）：二进制名与 flavor
// 的对应关系在 startComposeLogs 里由 composeBinary 决定。
func TestComposeLogsExecutorRunsFixedArgvPerFlavor(t *testing.T) {
	configFile := writeComposeFixture(t)
	for _, fc := range composeFlavorCases {
		t.Run(fc.flavor, func(t *testing.T) {
			stub := &composeLogsStub{}
			e, m, _, _ := newComposeLogsExecutor(composeProjectFixture(configFile), stub, fc.flavor)
			if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
				Action:  agentproto.DockerActionComposeLogs,
				Options: agentproto.DockerCmdOptions{Target: "uni-center", Follow: true, Tail: 50}}); err != nil {
				t.Fatalf("聚合日志应放行: %v", err)
			}
			call := stub.last(t)
			if call.name != fc.bin {
				t.Fatalf("可执行文件必须是 %q，实际 %q", fc.bin, call.name)
			}
			want := append(append([]string(nil), fc.prefix...),
				"-p", "uni-center", "-f", configFile,
				"logs", "--timestamps", "--tail", "50", "--follow")
			if !slices.Equal(call.args, want) {
				t.Fatalf("实际 argv 与期望逐字不符:\n got %q\nwant %q", call.args, want)
			}
			waitFor(t, "会话收摊（替身进程打完即退）", func() bool { return m.count() == 0 })
		})
	}
}

// ── 流行为：透传、eof、取消 ───────────────────────────────────────────────

// composeLogLines 是替身进程写出的「聚合输出」—— 形态即 compose logs 的原生形态：
// `<RFC3339 时间戳> <服务>  | 正文`。原文透传 = 帧里看到的就是这一整行。
const composeLogLines = "2026-09-30T08:00:00.123456789Z uni_core  | boot ok\n" +
	"2026-09-30T08:00:01.123456789Z uni_core  | listening :8080\n" +
	"2026-09-30T08:00:01.623456789Z web-1    | GET /health 200\n"

// CLI 输出逐行成帧、原文透传（时间戳与服务前缀原样到达）；进程打完即退 →
// eof 收尾、槽位释放。follow=false 也是会话（没有一次性形态）。
func TestComposeLogsStreamPassthroughAndEOF(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeLogsStub{stdout: composeLogLines}
	e, m, sink, clk := newComposeLogsExecutor(composeProjectFixture(configFile), stub,
		agentproto.DockerComposeFlavorPlugin)

	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"}}); err != nil {
		t.Fatalf("非 follow 的聚合日志同样应建立会话: %v", err)
	}
	sid := e.takeSessionID()
	if !agentproto.IsDockerSessionID(sid) {
		t.Fatalf("会话 id 必须过协议校验: %q", sid)
	}
	if m.count() != 1 {
		t.Fatalf("聚合日志会话必须占住槽位，实际 %d", m.count())
	}

	// 进程退出（非 follow 打完即退）→ 数据成帧 + eof 收尾。
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	if got := sink.data(); got != composeLogLines {
		t.Fatalf("CLI 输出必须原文透传（时间戳与服务前缀不解析、不重组）:\n got %q\nwant %q", got, composeLogLines)
	}
	if f := sink.all()[0]; f.Seq != 1 || f.SessionID != sid {
		t.Fatalf("帧的序号与会话 id 必须正确: %+v", f)
	}
	waitFor(t, "eof 后槽位释放", func() bool { return m.count() == 0 })
}

// stderr 合流：CLI 的错误出口是 stderr（"no such service"），合流进同一条流才
// 能到达用户 —— 否则失败形态是「一条空流 + 莫名 eof」。
func TestComposeLogsStreamMergesStderr(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeLogsStub{stdout: "line-a\n", stderr: "no such service: gone\n"}
	e, m, sink, clk := newComposeLogsExecutor(composeProjectFixture(configFile), stub,
		agentproto.DockerComposeFlavorPlugin)

	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"}}); err != nil {
		t.Fatal(err)
	}
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	got := sink.data()
	if !strings.Contains(got, "line-a\n") || !strings.Contains(got, "no such service: gone\n") {
		t.Fatalf("stdout 与 stderr 必须都到达消费端（合流纪律）: %q", got)
	}
	waitFor(t, "eof 后槽位释放", func() bool { return m.count() == 0 })
}

// follow 会话的取消 = 杀 CLI 进程（照 logs 先例「取消关上游」，落点是 SIGKILL）：
// cancel 帧到达 → 槽位立刻释放、进程退出（Signal(0) 探测：进程没了才算杀到）。
func TestComposeLogsFollowCancelKillsProcess(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeLogsStub{hold: true, stdout: "started\n"}
	e, m, sink, clk := newComposeLogsExecutor(composeProjectFixture(configFile), stub,
		agentproto.DockerComposeFlavorPlugin)

	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Follow: true}}); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	// 进程确实起来了（Start 已返回、输出已到达）。
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 释放槽位", func() bool { return m.count() == 0 })

	// OS 级断言：驻留中的进程必须真的死了（会话 ctx 直通 CommandContext → SIGKILL）。
	cmd := stub.lastCmd(t)
	waitFor(t, "进程被 SIGKILL 收掉", func() bool {
		return cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) != nil
	})
}

// tail 参数映射：缺省（0/负）= 100（与 container:logs 同默认），显式 50 原样传递。
func TestComposeLogsTailMapping(t *testing.T) {
	configFile := writeComposeFixture(t)
	for _, c := range []struct {
		tail  int
		wantN string
	}{
		{0, "100"},
		{50, "50"},
	} {
		stub := &composeLogsStub{}
		e, _, _, _ := newComposeLogsExecutor(composeProjectFixture(configFile), stub,
			agentproto.DockerComposeFlavorPlugin)
		if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
			Action:  agentproto.DockerActionComposeLogs,
			Options: agentproto.DockerCmdOptions{Target: "uni-center", Tail: c.tail}}); err != nil {
			t.Fatalf("tail=%d 应放行: %v", c.tail, err)
		}
		call := stub.last(t)
		i := slices.Index(call.args, "--tail")
		if i < 0 || i+1 >= len(call.args) || call.args[i+1] != c.wantN {
			t.Fatalf("tail=%d 的 argv 应含 --tail %s: %q", c.tail, c.wantN, call.args)
		}
	}
}

// ── 拒绝路径：结论句在触达 CLI 之前给出 ───────────────────────────────────

// 没探测到 compose（flavor 空）/ 项目名不合法 / 项目位置未知：一律结论句拒绝且
// 绝不执行 CLI（与 compose 写路径的 TestComposeExecutorRejectsInvalidTargetsBeforeExec
// 同一取向 —— 拒绝要先于执行，不然「拒绝」只是日志里的一行字）。
func TestComposeLogsRejectsBeforeExec(t *testing.T) {
	configFile := writeComposeFixture(t)
	cases := []struct {
		name   string
		api    DockerAPI
		flavor string
		target string
		want   string
	}{
		{"没有检测到 compose", composeProjectFixture(configFile), "", "uni-center", "没有检测到可用的 compose"},
		{"项目名带 shell 元字符", composeProjectFixture(configFile),
			agentproto.DockerComposeFlavorPlugin, "uni-center; rm -rf /", "项目名不合法"},
		{"项目名带斜杠（服务形态不是项目形态）", composeProjectFixture(configFile),
			agentproto.DockerComposeFlavorPlugin, "uni-center/uni_core", "项目名不合法"},
		{"从未见过的项目（无容器无索引）", &stubAPI{},
			agentproto.DockerComposeFlavorPlugin, "uni-center", "先在主机上执行一次"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &composeLogsStub{}
			e, m, _, _ := newComposeLogsExecutor(c.api, stub, c.flavor)
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
				Action:  agentproto.DockerActionComposeLogs,
				Options: agentproto.DockerCmdOptions{Target: c.target}})
			var ee *ExecError
			if !errors.As(err, &ee) || !strings.Contains(ee.Msg, c.want) {
				t.Fatalf("应被结论句拒绝（含 %q），实际 %v", c.want, err)
			}
			if stub.count() != 0 {
				t.Fatal("被拒的指令绝不能触达 compose CLI")
			}
			if m.count() != 0 {
				t.Fatalf("被拒的指令不得占用会话槽位，实际 %d", m.count())
			}
		})
	}
}

// 配置文件残缺（override 被移走）：结论句指明是哪个文件，且不执行 CLI ——
// 带着残缺清单读日志等于读一个与快照不符的项目。
func TestComposeLogsRejectsMissingOverrideFile(t *testing.T) {
	main := writeComposeFixture(t)
	missing := "/data/proj/no-such-override.yml"
	api := composeProjectFixture(main)
	api.containers[0].Labels[composeConfigFilesLabel] = main + "," + missing
	stub := &composeLogsStub{}
	e, _, _, _ := newComposeLogsExecutor(api, stub, agentproto.DockerComposeFlavorPlugin)

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"}})
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, missing) {
		t.Fatalf("缺失的 override 必须被结论句指明，实际 %v", err)
	}
	if stub.count() != 0 {
		t.Fatal("清单残缺时绝不能执行 CLI")
	}
}

// ── 分流与派发器集成 ─────────────────────────────────────────────────────

// compose:logs 两种形态都是流会话（没有一次性路径）；既有 container:logs 的
// 分流行为不变（回归守卫）。
func TestIsStreamActionComposeLogsAlwaysStream(t *testing.T) {
	if !IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Follow: true}}) {
		t.Fatal("follow 的聚合日志必须走流会话")
	}
	if !IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"}}) {
		t.Fatal("非 follow 的聚合日志同样是流会话（CLI 输出没有一次性取回的形态）")
	}
	// 回归：container:logs 的分流点不被本切片移动。
	if IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}) {
		t.Fatal("不带 follow 的容器日志必须仍是一次性读")
	}
}

// 派发器全链路：受理（已实现 + 协议校验）→ 执行器建会话 → result 带 session_id
// 且能过协议校验（core 据此登记会话、前端据此接流）。
func TestDispatcherComposeLogsCarriesSessionID(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeLogsStub{stdout: "2026-09-30T08:00:00.000000000Z uni_core  | boot\n"}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	stream := NewStreamExecutor(composeProjectFixture(configFile), m)
	stream.SetComposeCLI(func() string { return agentproto.DockerComposeFlavorPlugin }, stub.command)
	ne := &nodeExecutor{
		read:   NewReadExecutor(&stubAPI{}),
		write:  NewWriteExecutor(&stubAPI{}, ParseProtected(""), t.TempDir(), "", nil),
		stream: stream,
	}
	results := &collectSink{}
	d := NewDispatcher(ne, results.send, testLogger())

	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeLogs,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Follow: true}})
	d.ExecuteNow(context.Background(), "1")
	got := results.all()
	if len(got) != 1 {
		t.Fatalf("应有一条结果，实际 %d", len(got))
	}
	res := got[0]
	if !res.OK {
		t.Fatalf("聚合日志应成功建立会话: %+v", res)
	}
	if !agentproto.IsDockerSessionID(res.SessionID) {
		t.Fatalf("result.session_id 必须过协议校验: %q", res.SessionID)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("带 session_id 的结果必须语义合法: %v", err)
	}
}

// Runtime 探测到 flavor 后流执行器必须能看到（函数注入 = 单一事实源）：
// 没注进去的话聚合日志会一直回「没有检测到可用的 compose」。
func TestRuntimeInjectsFlavorIntoStreamExecutor(t *testing.T) {
	r := New(Deps{
		StateDir:   t.TempDir(),
		SendState:  func(*agentproto.DockerState) error { return nil },
		SendResult: func(*agentproto.DockerCmdResult) error { return nil },
		Log:        testLogger(),
		API:        &stubAPI{flavor: agentproto.DockerComposeFlavorStandaloneV2, flavorVer: "v2.26.1"},
	})
	if got := r.stream.composeFlavorValue(); got != "" {
		t.Fatalf("探测前 flavor 应为空（未探测不猜形态），实际 %q", got)
	}
	r.probeComposeOnce(context.Background())
	if got := r.stream.composeFlavorValue(); got != agentproto.DockerComposeFlavorStandaloneV2 {
		t.Fatalf("探测后流执行器必须看到 flavor，实际 %q", got)
	}
}
