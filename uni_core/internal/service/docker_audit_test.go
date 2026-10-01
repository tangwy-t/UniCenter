package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// captureAuditWriter 是 DockerCmdAuditWriter 的替身：收下每一条条目并可选注入写失败。
// done 是缓冲通道（容量 16）：异步入账的观测点 —— 测试等它而不是 sleep。
type captureAuditWriter struct {
	mu   sync.Mutex
	logs []*entity.SysOperationLog
	err  error
	done chan struct{}
}

func newCaptureAuditWriter() *captureAuditWriter {
	return &captureAuditWriter{done: make(chan struct{}, 16)}
}

func (w *captureAuditWriter) Create(_ context.Context, log *entity.SysOperationLog) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		w.done <- struct{}{}
		return w.err
	}
	w.logs = append(w.logs, log)
	w.done <- struct{}{}
	return nil
}

func (w *captureAuditWriter) snapshots() []*entity.SysOperationLog {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*entity.SysOperationLog, len(w.logs))
	copy(out, w.logs)
	return out
}

func waitAudit(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审计条目未在 2s 内入账（异步挂钩失效）")
	}
}

// newAuditIngestEnv 构造「真 CmdStore + 真 Auditor + 捕获写手」的 ingest 环境：
// 审计挂钩的集成事实（终态转换 → 入账）在真实两段上验证，只有 DB 写手是替身。
func newAuditIngestEnv(t *testing.T, deviceReader DockerDeviceReader) (*AgentIngestService, *captureAuditWriter, *dockerstate.CmdStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cmds := dockerstate.NewCmdStore(rdb)
	writer := newCaptureAuditWriter()
	auditor := NewDockerCmdAuditor(writer, deviceReader, logger.NewNop())
	svc := NewAgentIngestService(nil, nil, nil, nil, cmds, stubCfg{}, logger.NewNop()).
		WithDockerAudit(auditor)
	return svc, writer, cmds
}

func auditRec(ref string) *dockerstate.CmdRecord {
	return &dockerstate.CmdRecord{Ref: ref, DeviceID: 7, UserID: 42,
		Action: agentproto.DockerActionImagePull, Target: "alpine:3.19",
		Status: dockerstate.StatusPending, CreatedAt: 1_000_000}
}

// TestAuditHookSuccess 钉住成功终态入账的字段群：Code=0、结论句兜底、用户/模块/
// 操作类型、结构化 params（含 ref/hostId）、耗时来自记录自身、ErrorMsg 缺席。
func TestAuditHookSuccess(t *testing.T) {
	dev := &entity.Device{Hostname: "bogon"}
	dev.ID = 7
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev}})
	ctx := context.Background()

	rec := auditRec("a-1")
	if err := cmds.Create(ctx, rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: "a-1", OK: true}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, writer.done)
	logs := writer.snapshots()
	if len(logs) != 1 {
		t.Fatalf("成功终态必须恰入一条审计, got %d", len(logs))
	}
	l := logs[0]
	if l.UserID != 42 || l.Username != "" {
		t.Fatalf("UserID 取受理记录；Username 留给共享写路径（OperationLogService.Create）反查: got %+v", l)
	}
	if l.Module != dockerOpLogModule || l.OperationType != dockerOpLogTaskResult {
		t.Fatalf("模块/操作类型必须与受理审计同一叙述面: %q/%q", l.Module, l.OperationType)
	}
	if l.Code != 0 {
		t.Fatalf("成功 Code=0, got %d", l.Code)
	}
	if l.ErrorMsg != nil {
		t.Fatalf("成功不得有 ErrorMsg, got %q", *l.ErrorMsg)
	}
	if l.RequestParams == nil || !strings.Contains(*l.RequestParams, `"ref":"a-1"`) ||
		!strings.Contains(*l.RequestParams, `"hostId":7`) || !strings.Contains(*l.RequestParams, "bogon") {
		t.Fatalf("RequestParams 必须结构化含 ref/hostId/hostname: got %v", l.RequestParams)
	}
	if l.ResponseResult == nil || *l.ResponseResult != dockerTaskSummarySucceeded {
		t.Fatalf("成功结论句兜底入 ResponseResult: got %v", l.ResponseResult)
	}
	if l.CostTime == nil || *l.CostTime < 0 {
		t.Fatalf("耗时取自记录自身（FinishedAt-CreatedAt）: got %v", l.CostTime)
	}
	if l.IP != nil || l.RequestURL != nil || l.RequestMethod != nil {
		t.Fatalf("非 HTTP 条目不得编造 IP/URL/Method: got %+v", l)
	}
}

// TestAuditHookCancelledConclusion 钉住「取消」的呈现形态（6b 盘点结论）：
// 取消没有独立状态码/生产者，语义 = Code 执行失败 + 结论句原文「拉取已取消」——
// 操作日志页据此可读、可过滤（code=70001），关键词在 ErrorMsg 与 ResponseResult 里。
func TestAuditHookCancelledConclusion(t *testing.T) {
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{})
	ctx := context.Background()
	if err := cmds.Create(ctx, auditRec("a-2"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: "a-2", OK: false, Error: "拉取已取消"}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, writer.done)
	logs := writer.snapshots()
	if len(logs) != 1 {
		t.Fatalf("失败终态必须恰入一条审计, got %d", len(logs))
	}
	l := logs[0]
	if l.Code != dockerCmdResultFailed {
		t.Fatalf("执行失败 Code=%d, got %d", dockerCmdResultFailed, l.Code)
	}
	if l.ErrorMsg == nil || *l.ErrorMsg != "拉取已取消" {
		t.Fatalf("取消结论句必须原文入 ErrorMsg, got %v", l.ErrorMsg)
	}
	if l.ResponseResult == nil || *l.ResponseResult != "拉取已取消" {
		t.Fatalf("结论句原文入 ResponseResult（正文展示位）: got %v", l.ResponseResult)
	}
}

// TestAuditHookIdempotent 钉住幂等：同 ref 的重复 result 不会入第二条审计 ——
// 唯一性保证在终态转换（ingest 回写前摘取旧状态，见 CompleteDockerCmd 注释）。
func TestAuditHookIdempotent(t *testing.T) {
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{})
	ctx := context.Background()
	if err := cmds.Create(ctx, auditRec("a-3"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	res := &agentproto.DockerCmdResult{Ref: "a-3", OK: true}
	if err := svc.CompleteDockerCmd(ctx, 7, res); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, res); err != nil { // agent 重发/网络重放
		t.Fatal(err)
	}
	waitAudit(t, writer.done)
	if logs := writer.snapshots(); len(logs) != 1 {
		t.Fatalf("重复 result 必须幂等（恰一条），got %d", len(logs))
	}

	// timeout（sweep 推断）之后的迟到 result：入账的是**事实**那条，仍然恰一条。
	svc2, writer2, cmds2 := newAuditIngestEnv(t, taskDeviceLookup{})
	rec2 := auditRec("a-4")
	if err := cmds2.Create(ctx, rec2, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cmds2.Timeout(ctx, rec2); err != nil {
		t.Fatal(err)
	}
	if err := svc2.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: "a-4", OK: false, Error: "拉取镜像失败"}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, writer2.done)
	logs2 := writer2.snapshots()
	if len(logs2) != 1 || logs2[0].ErrorMsg == nil || *logs2[0].ErrorMsg != "拉取镜像失败" {
		t.Fatalf("迟到 result 覆盖 timeout 后入账事实（sweep 推断本身不入审计）: got %+v", logs2)
	}
}

// TestAuditWriteFailureDoesNotBlockIngest 钉住可靠性纪律：审计写失败只告警，
// 结果回写主链照常（记录终态、ingest 无错）。
func TestAuditWriteFailureDoesNotBlockIngest(t *testing.T) {
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{})
	writer.err = context.DeadlineExceeded // 模拟 DB 故障/超时
	ctx := context.Background()
	if err := cmds.Create(ctx, auditRec("a-5"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: "a-5", OK: false, Error: "执行失败"}); err != nil {
		t.Fatalf("审计写失败不得让 ingest 报错（主链照常）: %v", err)
	}
	rec, err := cmds.Get(ctx, "a-5")
	if err != nil || rec == nil || rec.Status != dockerstate.StatusFailed {
		t.Fatalf("结果回写必须完成（审计是观察者）: %+v %v", rec, err)
	}
	waitAudit(t, writer.done) // 写手仍被调用（失败在内部告警）—— 条目数不增
}

// TestAuditHookSkipsSystemEvents 钉住豁免：docker:events 常驻订阅（UserID=0）
// 不是用户任务，不入审计。
func TestAuditHookSkipsSystemEvents(t *testing.T) {
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{})
	ctx := context.Background()
	rec := &dockerstate.CmdRecord{Ref: "ev-1", DeviceID: 7, UserID: 0,
		Action: agentproto.DockerActionEvents, Status: dockerstate.StatusPending, CreatedAt: 1_000_000}
	if err := cmds.Create(ctx, rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: "ev-1", OK: true}); err != nil {
		t.Fatal(err)
	}
	// 事件名校不变：写手不被调用（豁免发生在挂钩入口）。没有信号可等，用短暂观测
	// 即可 —— 若被入账，snapshots 非空。
	time.Sleep(50 * time.Millisecond)
	if logs := writer.snapshots(); len(logs) != 0 {
		t.Fatalf("常驻订阅不当用户任务审计, got %+v", logs)
	}
}

// TestAuditEntryDesensitizesSummary 钉住敏感字段纪律照抄中间件：结论句虽来自
// 受控生成面，仍过一遍 DesensitizeJSON —— 同一份 sensitiveFieldSet。
func TestAuditEntryDesensitizesSummary(t *testing.T) {
	svc, writer, cmds := newAuditIngestEnv(t, taskDeviceLookup{})
	ctx := context.Background()
	if err := cmds.Create(ctx, auditRec("a-6"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	// 结论句装成含敏感键的 JSON：脱敏后 password 值必须是 ***。
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{
		Ref: "a-6", OK: false, Error: `{"password":"pw123","note":"fail"}`}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, writer.done)
	l := writer.snapshots()[0]
	if l.ErrorMsg == nil || !strings.Contains(*l.ErrorMsg, `"password":"***"`) ||
		strings.Contains(*l.ErrorMsg, "pw123") {
		t.Fatalf("结论句必须过中间件同源脱敏: got %v", l.ErrorMsg)
	}
}

// TestAuditMissingRecordNoEntry 钉住入口前置条件：命中不到记录（过期/伪造）时
// 不回写、也不入审计。
func TestAuditMissingRecordNoEntry(t *testing.T) {
	svc, writer, _ := newAuditIngestEnv(t, taskDeviceLookup{})
	if err := svc.CompleteDockerCmd(context.Background(), 7,
		&agentproto.DockerCmdResult{Ref: "no-such", OK: true}); err != nil {
		t.Fatal(err)
	}
	if logs := writer.snapshots(); len(logs) != 0 {
		t.Fatalf("无记录不上审计, got %+v", logs)
	}
}

// TestAuditorNilAndZeroUser 钉住挂钩自身的豁免边界：nil writer / nil rec /
// 常驻记录（UserID=0）都不动作。
func TestAuditorNilAndZeroUser(t *testing.T) {
	var nilAuditor *DockerCmdAuditor
	nilAuditor.RecordTerminal(auditRec("x")) // nil receiver 安全

	writer := newCaptureAuditWriter()
	a := NewDockerCmdAuditor(writer, nil, logger.NewNop())
	a.now = func() time.Time { return time.Unix(100, 0) }
	a.RecordTerminal(nil)                                           // nil rec
	a.RecordTerminal(&dockerstate.CmdRecord{Ref: "sys", UserID: 0}) // 系统记录
	time.Sleep(30 * time.Millisecond)
	if logs := writer.snapshots(); len(logs) != 0 {
		t.Fatalf("豁免路径不得入账, got %+v", logs)
	}
}
