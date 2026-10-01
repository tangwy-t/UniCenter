package dockerops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 终端（container:exec）────────────────────────────────────────────────

// fakeExecStream 是 ExecSession 的替身：stdin 收字、resize 记账、close 记账。
type fakeExecStream struct {
	mu     sync.Mutex
	stdin  bytes.Buffer
	resize [][2]int
	closed bool
	reader io.Reader
}

type lockedWriter struct{ f *fakeExecStream }

func (w lockedWriter) Write(b []byte) (int, error) {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	return w.f.stdin.Write(b)
}

func (f *fakeExecStream) session() *ExecSession {
	return &ExecSession{
		Reader: f.reader,
		Writer: lockedWriter{f},
		Resize: func(_ context.Context, cols, rows int) error {
			f.mu.Lock()
			f.resize = append(f.resize, [2]int{cols, rows})
			f.mu.Unlock()
			return nil
		},
		Close: func() error {
			f.mu.Lock()
			f.closed = true
			f.mu.Unlock()
			return nil
		},
	}
}

func (f *fakeExecStream) input() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stdin.String()
}

func (f *fakeExecStream) resizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resize...)
}

func (f *fakeExecStream) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// exec 会话：argv 原样传给 daemon、PTY 输出成帧、input/resize 按 op 路由、cancel 释放槽位。
func TestStreamExecutorExecSession(t *testing.T) {
	ptyOut, ptyIn := io.Pipe()
	defer ptyIn.Close()
	api := &stubAPI{}
	fs := &fakeExecStream{reader: ptyOut}
	api.execSession = fs.session()

	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)

	cmd := &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerExec,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Command: []string{"/bin/bash", "-lc", "echo hi"}}}
	if _, err := e.Do(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	if !agentproto.IsDockerSessionID(sid) {
		t.Fatalf("会话 id 必须过协议校验: %q", sid)
	}
	if m.count() != 1 {
		t.Fatalf("exec 会话必须占住槽位，实际 %d", m.count())
	}
	// argv 原样传递（不经 shell）。
	calls := api.execArgvs()
	if len(calls) != 1 || calls[0].name != "mysql" || strings.Join(calls[0].argv, " ") != "/bin/bash -lc echo hi" {
		t.Fatalf("exec 调用不符: %+v", calls)
	}

	// PTY 输出 → 帧（50ms 合帧窗口）。
	if _, err := ptyIn.Write([]byte("hi\r\n")); err != nil {
		t.Fatal(err)
	}
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool { return sink.count() >= 1 })
	if got := sink.data(); got != "hi\r\n" {
		t.Fatalf("PTY 输出必须进帧，实际 %q", got)
	}

	// input：写进 exec 的 stdin（经会话的输入队列，读循环不阻塞）。
	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpInput, Data: []byte("ls\n")})
	waitFor(t, "输入写入 stdin", func() bool { return fs.input() == "ls\n" })

	// resize：调 daemon 的 resize。
	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpResize, Cols: 120, Rows: 40})
	waitFor(t, "resize 落到位", func() bool { return len(fs.resizes()) == 1 })
	if got := fs.resizes()[0]; got != [2]int{120, 40} {
		t.Fatalf("resize 参数不符: %v", got)
	}

	// cancel：关连接、释放槽位、不再有数据帧。
	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 关连接", func() bool { return fs.isClosed() })
	waitFor(t, "cancel 释放槽位", func() bool { return m.count() == 0 })
}

// 未给 command 时用缺省 ["/bin/sh"]（协议冻结缺省）。
func TestStreamExecutorExecDefaultCommand(t *testing.T) {
	api := &stubAPI{}
	fs := &fakeExecStream{reader: strings.NewReader("")}
	api.execSession = fs.session()
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerExec,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}); err != nil {
		t.Fatal(err)
	}
	calls := api.execArgvs()
	if len(calls) != 1 || len(calls[0].argv) != 1 || calls[0].argv[0] != "/bin/sh" {
		t.Fatalf("缺省 argv 应为 [\"/bin/sh\"]，实际 %+v", calls)
	}
}

// ── 日志 follow ────────────────────────────────────────────────────────

// 日志 follow：逐块收帧、自然结束发 eof=true、槽位释放；tail/since 原样传递。
func TestStreamExecutorLogFollowFramesAndEOF(t *testing.T) {
	logOut, logIn := io.Pipe()
	api := &stubAPI{}
	api.logStreamFn = func() io.ReadCloser { return logOut }

	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)

	cmd := &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Follow: true, Tail: 50, Since: 1790000000}}
	if _, err := e.Do(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	calls := api.followCalls()
	if len(calls) != 1 || calls[0].name != "mysql" || calls[0].tail != 50 || calls[0].since != 1790000000 {
		t.Fatalf("follow 调用不符: %+v", calls)
	}

	// 两块数据分别落在两个合帧窗口里 → 两帧（而不是一帧）。
	if _, err := logIn.Write([]byte("line1\n")); err != nil {
		t.Fatal(err)
	}
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool { return sink.count() >= 1 })
	if _, err := logIn.Write([]byte("line2\n")); err != nil {
		t.Fatal(err)
	}
	advanceUntil(t, clk, 25*time.Millisecond, time.Second, func() bool { return sink.count() >= 2 })
	frames := sink.all()
	if len(frames) != 2 || string(frames[0].Data) != "line1\n" || string(frames[1].Data) != "line2\n" {
		t.Fatalf("两块数据应成两帧: %+v", frames)
	}
	if frames[0].Seq != 1 || frames[1].Seq != 2 {
		t.Fatalf("seq 必须从 1 单调递增: %d %d", frames[0].Seq, frames[1].Seq)
	}

	// 流自然结束 → eof（可与末帧数据同帧）→ 槽位释放。
	if _, err := logIn.Write([]byte("bye\n")); err != nil {
		t.Fatal(err)
	}
	if err := logIn.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "eof 帧", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	last := sink.all()[len(sink.all())-1]
	if string(last.Data) != "bye\n" {
		t.Fatalf("末帧数据必须与 eof 同帧发出，实际 %q", last.Data)
	}
	if last.SessionID != sid {
		t.Fatalf("帧的会话 id 必须与 result 一致: %q vs %q", last.SessionID, sid)
	}
	waitFor(t, "会话槽位释放", func() bool { return m.count() == 0 })
}

// tail 缺省 = 100（与一次性取日志同一默认）。
func TestStreamExecutorLogFollowDefaultTail(t *testing.T) {
	out, in := io.Pipe()
	defer in.Close()
	api := &stubAPI{}
	api.logStreamFn = func() io.ReadCloser { return out }
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Follow: true}}); err != nil {
		t.Fatal(err)
	}
	if calls := api.followCalls(); len(calls) != 1 || calls[0].tail != defaultLogTail {
		t.Fatalf("缺省 tail 应为 %d，实际 %+v", defaultLogTail, calls)
	}
}

// 打开日志流失败：回一句结论（不建会话、不占槽位）。
func TestStreamExecutorLogFollowOpenFailure(t *testing.T) {
	api := &stubAPI{}
	api.logStreamErr = io.ErrUnexpectedEOF
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Follow: true}})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "读取容器日志失败" {
		t.Fatalf("打开失败必须回结论句，实际 %v", err)
	}
	if m.count() != 0 {
		t.Fatalf("失败的会话不得占槽位，实际 %d", m.count())
	}
}

// ── 分流：不带 follow 的既有一次性路径必须原样 ────────────────────────────

func TestIsStreamActionSplitsLogsByFollow(t *testing.T) {
	if IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}) {
		t.Fatal("不带 follow 的日志是一次性读（一期路径），不得走流会话")
	}
	if !IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Follow: true}}) {
		t.Fatal("带 follow 的日志必须走流会话")
	}
	if !IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerExec}) {
		t.Fatal("exec 必须走流会话")
	}
	if !IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerStats}) {
		t.Fatal("stats 必须走流会话（它是会话制，没有一次性形态）")
	}
	if IsStreamAction(&agentproto.DockerCmd{Action: agentproto.DockerActionContainerInspect}) {
		t.Fatal("只读 action 不得走流会话")
	}
	if IsStreamAction(nil) {
		t.Fatal("nil 不应 panic")
	}
}

// 回归：一次性日志（跟随 false）走 ReadExecutor，结果载荷与三期之前逐字一致，
// 且**不会**碰流接口（碰了就是「既有路径被改」）。
func TestOneShotLogsPathUnchanged(t *testing.T) {
	api := &stubAPI{}
	api.logLines = "2026-09-28 boot ok\n"
	api.logTruncated = true
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	api2 := &stubAPI{}
	api2.logLines = api.logLines
	api2.logTruncated = true
	ne := &nodeExecutor{
		read:   NewReadExecutor(api2),
		write:  NewWriteExecutor(api2, ParseProtected(""), "", "", nil),
		stream: NewStreamExecutor(api2, m),
	}
	payload, err := ne.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql", Tail: 100}})
	if err != nil {
		t.Fatal(err)
	}
	var p agentproto.DockerLogsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Lines != api.logLines || !p.Truncated {
		t.Fatalf("一次性日志载荷必须原样: %+v", p)
	}
	if n := len(api2.followCalls()); n != 0 {
		t.Fatalf("一次性路径不得碰流接口，实际调用 %d 次", n)
	}
	if m.count() != 0 {
		t.Fatalf("一次性路径不得建会话，实际 %d", m.count())
	}
}

// lastResult 取最后一条结果（stateSink 的读侧助手）。
func (s *stateSink) lastResult(t *testing.T) *agentproto.DockerCmdResult {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		t.Fatal("还没有任何结果")
	}
	return s.results[len(s.results)-1]
}

// Runtime 全链路：exec 指令 → result 带 session_id（协议校验通过）→ 控制帧按 op 路由
// → cancel 释放槽位；并发会话上限 3 对**指令路径**同样生效。
func TestRuntimeExecCommandCarriesSessionIDAndEnforcesLimit(t *testing.T) {
	// PTY 输出用**不结束**的管道：会话要一直活着才能测上限与 cancel（读到 EOF 的
	// 会话会自己发 eof 收摊、释放槽位）。
	ptyOut, ptyIn := io.Pipe()
	defer ptyIn.Close()
	fs := &fakeExecStream{reader: ptyOut}
	api := &stubAPI{execSession: fs.session()}
	sink := &stateSink{}
	frames := &frameSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		SendFrame: frames.send, Log: testLogger(), API: api,
		Now: func() time.Time { return time.Unix(1790000000, 0) }})

	exec := func(ref string) *agentproto.DockerCmdResult {
		t.Helper()
		before := sink.resultCount()
		r.OnCmd(&agentproto.DockerCmd{Ref: ref, Action: agentproto.DockerActionContainerExec,
			Options: agentproto.DockerCmdOptions{Target: "mysql"}})
		r.dispatcher().ExecuteNow(context.Background(), ref)
		waitFor(t, "exec 结果 "+ref, func() bool { return sink.resultCount() == before+1 })
		return sink.lastResult(t)
	}

	res := exec("1")
	if !res.OK {
		t.Fatalf("exec 建立会话必须成功: %+v", res)
	}
	if !agentproto.IsDockerSessionID(res.SessionID) {
		t.Fatalf("result.session_id 必须过协议校验: %q", res.SessionID)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("带 session_id 的结果必须语义合法: %v", err)
	}
	sid := res.SessionID

	// 上限 3：第 4 条回结论句（而不是 panic 或静默）。
	_ = exec("2")
	_ = exec("3")
	over := exec("4")
	if over.OK || over.Error != errStreamLimit.Error() {
		t.Fatalf("第 4 个会话必须被拒且结论句可读，实际 %+v", over)
	}

	// OnFrame 必须**非阻塞**（它在连接读循环里被调用）。
	done := make(chan struct{})
	go func() {
		r.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpInput, Data: []byte("id\n")})
		r.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpResize, Cols: 80, Rows: 24})
		r.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpCancel})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnFrame 阻塞了（它会拖住整条连接的读循环）")
	}
	waitFor(t, "cancel 释放槽位", func() bool { return r.sessions.count() == 2 })
	if !fs.isClosed() {
		t.Fatal("cancel 必须关闭 exec 连接")
	}
}

// 会话帧的出口没注入时：会话以明确结论结束（而不是泵里空指针）。
func TestRuntimeStreamWithoutFrameSinkFailsCleanly(t *testing.T) {
	api := &stubAPI{}
	api.execSession = (&fakeExecStream{reader: strings.NewReader("")}).session()
	sink := &stateSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: api, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.OnCmd(&agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerExec,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	r.dispatcher().ExecuteNow(context.Background(), "1")
	waitFor(t, "结果", func() bool { return sink.resultCount() == 1 })
	// 会话建立本身仍是成功的（帧出口只在发送时用到）；随后一写数据就会因发送失败收摊。
	if res := sink.lastResult(t); !res.OK {
		t.Fatalf("建立会话不该依赖帧出口: %+v", res)
	}
}
