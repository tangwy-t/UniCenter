package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 替身：三个能力面 + 一个记账日志 ──────────────────────────────────────

type stubTransferHosts struct {
	ids      []uint64
	states   map[uint64]*dockerstate.Envelope
	getErr   map[uint64]error
	hostsErr error
}

func (s *stubTransferHosts) Hosts(context.Context) ([]uint64, error) {
	if s.hostsErr != nil {
		return nil, s.hostsErr
	}
	return s.ids, nil
}

func (s *stubTransferHosts) Get(_ context.Context, deviceID uint64) (*dockerstate.Envelope, error) {
	if err := s.getErr[deviceID]; err != nil {
		return nil, err
	}
	return s.states[deviceID], nil
}

type stubTransferCmds struct {
	createErr error
	getErr    error
	// terminal 是「result 已回写」的终态记录（缺席 = 仍 pending）。
	terminal map[string]*dockerstate.CmdRecord
	// 记账：Create 收到的记录与超时形参（断言「记录了、超时取自策略表」）。
	created  map[string]*dockerstate.CmdRecord
	timeouts map[string]time.Duration
	gets     int
}

func newStubTransferCmds() *stubTransferCmds {
	return &stubTransferCmds{
		terminal: map[string]*dockerstate.CmdRecord{},
		created:  map[string]*dockerstate.CmdRecord{},
		timeouts: map[string]time.Duration{},
	}
}

func (s *stubTransferCmds) Create(_ context.Context, rec *dockerstate.CmdRecord, timeout time.Duration) error {
	if s.createErr != nil {
		return s.createErr
	}
	// 与真实 CmdStore.Create 同一形态：受理即 pending（Status 由 store 定，不由调用方）。
	rec.Status = dockerstate.StatusPending
	s.created[rec.Ref] = rec
	s.timeouts[rec.Ref] = timeout
	return nil
}

func (s *stubTransferCmds) Get(_ context.Context, ref string) (*dockerstate.CmdRecord, error) {
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	if rec, ok := s.terminal[ref]; ok {
		return rec, nil
	}
	if rec, ok := s.created[ref]; ok {
		return rec, nil
	}
	return nil, nil // 记录已过期
}

type sentTransferCmd struct {
	deviceID uint64
	ref      string
	action   string
}

type stubTransferSender struct {
	// errs 按设备给「未送达」（离线/抖动）。
	errs map[uint64]error

	mu   sync.Mutex
	sent []sentTransferCmd
}

func (s *stubTransferSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	if err := s.errs[deviceID]; err != nil {
		return err
	}
	var cmd agentproto.DockerCmd
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentTransferCmd{deviceID: deviceID, ref: msg.ID, action: cmd.Action})
	return nil
}

func (s *stubTransferSender) all() []sentTransferCmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentTransferCmd(nil), s.sent...)
}

// recLog 是记账日志：本任务的**唯一战果出口**就是日志，故断言必须读它
//（不是「顺便记一句」—— 这批指令不入任务历史也不入审计，见任务头注释）。
type recLog struct {
	mu    sync.Mutex
	lines []recLine
}

type recLine struct {
	level string
	msg   string
	strs  map[string]string
	ints  map[string]int64
}

func (l *recLog) add(level, msg string, fields ...zap.Field) {
	line := recLine{level: level, msg: msg, strs: map[string]string{}, ints: map[string]int64{}}
	for _, f := range fields {
		switch f.Type {
		case zapcore.StringType:
			line.strs[f.Key] = f.String
		case zapcore.Int64Type, zapcore.Int32Type, zapcore.Uint64Type:
			line.ints[f.Key] = f.Integer
		default:
			line.strs[f.Key] = fmt.Sprint(f.Interface)
		}
	}
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *recLog) Debug(msg string, fields ...zap.Field) { l.add("debug", msg, fields...) }
func (l *recLog) Info(msg string, fields ...zap.Field)  { l.add("info", msg, fields...) }
func (l *recLog) Warn(msg string, fields ...zap.Field)  { l.add("warn", msg, fields...) }
func (l *recLog) Error(msg string, fields ...zap.Field) { l.add("error", msg, fields...) }
func (l *recLog) IsDebug() bool                         { return true }

// find 返回第一条 msg 匹配的行（找不到即判死：日志是本任务唯一的战果出口）。
func (l *recLog) find(t *testing.T, msg string) recLine {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line.msg == msg {
			return line
		}
	}
	t.Fatalf("日志里没有 %q（本任务的战果只在这里）", msg)
	return recLine{}
}

// count 返回 msg 匹配的条数。
func (l *recLog) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if line.msg == msg {
			n++
		}
	}
	return n
}

// ── 工具 ────────────────────────────────────────────────────────────────

const transferCleanupDoneMsg = "docker 上传产物回收完成"

// testRef 给出第 n 个可预知的 ref。
//
// **必须是十进制串**：协议的 id 只认十进制（JS 侧不能丢精度，NewMessage 会拒
// 非十进制），生产的 ref 由 service.NewRequestID 给出同样形态的十进制串 ——
// 替身也必须守这条，否则测的是一条线上不存在的指令。
func testRef(n int) string { return strconv.Itoa(1790000000000000000 + n) }

// seqRefGen 给出确定的 ref（第 1 台 = testRef(1)…）：结果替身要按 ref 预置终态，
// ref 必须可预知。
func seqRefGen() func() string {
	var n int
	return func() string {
		n++
		return testRef(n)
	}
}

func dockerOK(ok bool) *dockerstate.Envelope {
	return &dockerstate.Envelope{State: agentproto.DockerState{DockerOK: ok}}
}

// newCleanupTask 装一个测试任务：固定的现在、毫秒级窗口（不必真等两分钟）。
func newCleanupTask(hosts DockerTransferHosts, cmds DockerTransferCmdStore,
	sender DockerTransferSender, log *recLog) *DockerTransferCleanupTask {
	t := NewDockerTransferCleanupTask(hosts, cmds, sender, seqRefGen(), log)
	t.now = func() time.Time { return time.Unix(1790000000, 0) }
	t.wait = 50 * time.Millisecond
	t.poll = 5 * time.Millisecond
	return t
}

// ── 用例 ────────────────────────────────────────────────────────────────

// 全舰队派发 + 战果落日志：判据是「这台报过快照」而不是快照里的 docker_ok ——
// **docker 不可用的主机照样要回收**（它一个字节都不碰 daemon，而盘被产物占满
// 恰恰可能是 daemon 挂掉的原因）。逐台的结果（成功/失败）各计一份，成功那台的
// detail 原文进日志 ——「昨天到底清了几个」由这条日志回答。
func TestDockerTransferCleanupDispatchesToManageableHosts(t *testing.T) {
	hosts := &stubTransferHosts{
		ids: []uint64{3, 1, 2}, // 乱序：升序是任务的纪律（日志可复现）
		states: map[uint64]*dockerstate.Envelope{
			1: dockerOK(true),
			2: dockerOK(false), // docker 不可用：**仍要派发**
			// 3 没有快照（从未上报）：跳过
		},
		getErr: map[uint64]error{},
	}
	cmds := newStubTransferCmds()
	// ref 按主机升序生成：1 → testRef(1)、2 → testRef(2)。
	cmds.terminal[testRef(1)] = &dockerstate.CmdRecord{
		Ref: testRef(1), Status: dockerstate.StatusSucceeded, Detail: "removed=2 freed_bytes=1048576",
	}
	cmds.terminal[testRef(2)] = &dockerstate.CmdRecord{
		Ref: testRef(2), Status: dockerstate.StatusFailed, Error: "产物目录不可用（/x）",
	}
	sender := &stubTransferSender{errs: map[uint64]error{}}
	log := &recLog{}

	task := newCleanupTask(hosts, cmds, sender, log)
	if err := task.Execute(context.Background(), nil); err != nil {
		t.Fatalf("整轮必须成功（逐台结论各自入账，不是整轮错误）: %v", err)
	}

	sent := sender.all()
	if len(sent) != 2 {
		t.Fatalf("应发给报过快照的两台（含 docker 不可用的那台），实得 %d 条: %+v", len(sent), sent)
	}
	for i, wantDevice := range []uint64{1, 2} {
		if sent[i].deviceID != wantDevice {
			t.Fatalf("第 %d 条应发往 %d，实得 %+v", i, wantDevice, sent[i])
		}
		if sent[i].action != dockerTransferCleanupAction {
			t.Fatalf("派发的 action 应为 %s，实得 %q", dockerTransferCleanupAction, sent[i].action)
		}
	}
	// 记录必须建全（result 回写只认记录存在的 ref），权限与超时取自**策略表**。
	pol, ok := dockerpolicy.Lookup(dockerTransferCleanupAction)
	if !ok {
		t.Fatalf("策略表缺 %s 行", dockerTransferCleanupAction)
	}
	if pol.Perm != permission.PermDockerDelete {
		t.Fatalf("权限档应为 docker:delete，实得 %q", pol.Perm)
	}
	for _, ref := range []string{testRef(1), testRef(2)} {
		rec, ok := cmds.created[ref]
		if !ok {
			t.Fatalf("%s 没有建记录（agent 的 result 会被当伪造丢掉）", ref)
		}
		if rec.Action != dockerTransferCleanupAction || rec.Perm != pol.Perm {
			t.Fatalf("%s 的记录形态不对: action=%q perm=%q", ref, rec.Action, rec.Perm)
		}
		if rec.UserID != 0 {
			t.Fatalf("%s 的发起人应为 0（core 自己派的内部指令）", ref)
		}
		if got := cmds.timeouts[ref]; got != pol.AcceptTimeout() {
			t.Fatalf("%s 的记录档应取自策略表 %v，实得 %v", ref, pol.AcceptTimeout(), got)
		}
	}

	sum := log.find(t, transferCleanupDoneMsg)
	wantInts := map[string]int64{
		"hosts": 3, "skipped": 1, "dispatched": 2, "undelivered": 0, "createFailed": 0,
		"succeeded": 1, "failedResult": 1, "unreported": 0,
	}
	for k, want := range wantInts {
		if got := sum.ints[k]; got != want {
			t.Errorf("结果计数 %s = %d, want %d（整行的全部字段：%+v）", k, got, want, sum.ints)
		}
	}
	// 战果原文与失败原因各有一条逐台日志。
	if got := log.find(t, "docker 上传产物回收：一台完成").strs["detail"]; got != "removed=2 freed_bytes=1048576" {
		t.Fatalf("成功的战果应原文进日志，实得 %q", got)
	}
	if got := log.find(t, "docker 上传产物回收：一台未成功").strs["status"]; got != dockerstate.StatusFailed {
		t.Fatalf("失败那台应记状态，实得 %q", got)
	}
}

// 逐台失败不拖垮整轮（照 agent_upgrade_patrol 的错误处置口径）：快照读失败、
// 未送达、记录写不进去 —— 三种单台失败各自跳过，另外的主机照常收到指令，
// 且整轮**不返回错误**（返回错误会让调度器重试整轮，把已经做过的那些又做一遍）。
func TestDockerTransferCleanupPerHostFailureDoesNotAbortRound(t *testing.T) {
	hosts := &stubTransferHosts{
		ids: []uint64{1, 2, 3, 4},
		states: map[uint64]*dockerstate.Envelope{
			1: dockerOK(true), 2: dockerOK(true), 3: dockerOK(true), 4: dockerOK(true),
		},
		// 4 号机的快照读失败（Redis 抖动/单键损坏）。
		getErr: map[uint64]error{4: errors.New("redis 抖动")},
	}
	cmds := newStubTransferCmds()
	sender := &stubTransferSender{errs: map[uint64]error{
		2: errors.New("设备当前离线"), // 2 号机未送达
	}}
	log := &recLog{}

	task := newCleanupTask(hosts, cmds, sender, log)
	if err := task.Execute(context.Background(), nil); err != nil {
		t.Fatalf("单台失败不得升级成整轮错误: %v", err)
	}
	sent := sender.all()
	if len(sent) != 2 || sent[0].deviceID != 1 || sent[1].deviceID != 3 {
		t.Fatalf("1/3 号机必须照常收到指令，实得 %+v", sent)
	}
	sum := log.find(t, transferCleanupDoneMsg)
	if sum.ints["skipped"] != 1 || sum.ints["undelivered"] != 1 || sum.ints["dispatched"] != 2 {
		t.Fatalf("结果计数不符: %+v", sum.ints)
	}
	if log.count("docker 上传产物回收：主机快照读取失败，跳过") != 1 ||
		log.count("docker 上传产物回收：指令未送达，该台跳过") != 1 {
		t.Fatal("两种单台失败各应留一条日志（静默跳过等于事故不可见）")
	}
}

// 有界等待：窗口内没等到结论的那台计「未回报」，且整轮必须**在窗口到期时收工**
//（调度器的 worker 不能被一台卡住的主机无限期占住）。
func TestDockerTransferCleanupGivesUpAfterWindow(t *testing.T) {
	hosts := &stubTransferHosts{
		ids:    []uint64{1},
		states: map[uint64]*dockerstate.Envelope{1: dockerOK(true)},
		getErr: map[uint64]error{},
	}
	cmds := newStubTransferCmds() // 无终态：永远 pending
	sender := &stubTransferSender{errs: map[uint64]error{}}
	log := &recLog{}

	task := newCleanupTask(hosts, cmds, sender, log)
	start := time.Now()
	if err := task.Execute(context.Background(), nil); err != nil {
		t.Fatalf("窗口到期不是整轮错误: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("窗口到期必须收工（实耗 %v）", elapsed)
	}
	sum := log.find(t, transferCleanupDoneMsg)
	if sum.ints["dispatched"] != 1 || sum.ints["unreported"] != 1 || sum.ints["succeeded"] != 0 {
		t.Fatalf("未回报应如实计数: %+v", sum.ints)
	}
	if cmds.gets == 0 {
		t.Fatal("必须真的去收过结果（不 Get 无法区分「成功」与「没回来」）")
	}
}

// 整轮不可执行（枚举源读不到）才返回错误：调度器据此重试 —— 这一轮什么都没发生。
func TestDockerTransferCleanupReturnsErrorWhenHostsUnavailable(t *testing.T) {
	hosts := &stubTransferHosts{hostsErr: errors.New("redis 不可达"), getErr: map[uint64]error{}}
	task := newCleanupTask(hosts, newStubTransferCmds(),
		&stubTransferSender{errs: map[uint64]error{}}, &recLog{})
	if err := task.Execute(context.Background(), nil); err == nil {
		t.Fatal("枚举源读不到必须返回错误（否则这一轮会被当作「做完了」）")
	}
}

// 未装配（All(Deps{}) 的零值路径）必须是**一句错误**而不是 panic：
// 调度器会把它记成任务失败，而不是把整个 worker 带走。
func TestDockerTransferCleanupWithoutDepsFailsCleanly(t *testing.T) {
	task := NewDockerTransferCleanupTask(nil, nil, nil, nil, nil)
	if err := task.Execute(context.Background(), nil); err == nil {
		t.Fatal("依赖未装配必须报错")
	}
	if task.Name() != "docker-transfer-cleanup" {
		t.Fatalf("Name 必须与任务表的 invoke_target 逐字一致，实得 %q", task.Name())
	}
}
