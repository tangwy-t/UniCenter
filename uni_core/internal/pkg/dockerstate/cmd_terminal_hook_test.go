package dockerstate

import (
	"context"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// TestTerminalHookOnComplete 钉住 8d 终态钩子的 Complete 侧：每次成功写入终态后
// 通知一次，交出的记录是**终态形态**（历史持久层的入账依据）。
func TestTerminalHookOnComplete(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()

	var got []*CmdRecord
	cs.WithTerminalHook(func(_ context.Context, rec *CmdRecord) { got = append(got, rec) })

	in := rec("1001", 7)
	if err := cs.Create(ctx, in, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Create（pending）不得触发终态钩子, got %d", len(got))
	}

	if err := cs.Complete(ctx, in, &agentproto.DockerCmdResult{Ref: "1001", OK: false, Error: "拉取失败"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("终态写入必须通知一次, got %d", len(got))
	}
	r := got[0]
	if r.Status != StatusFailed || r.Error != "拉取失败" || r.FinishedAt <= 0 {
		t.Fatalf("钩子收到的必须是终态形态（status/error/finished_at 已填）: %+v", r)
	}
	// 归属与受理时刻必须原样带出（历史行的列全在这里）
	if r.UserID != 42 || r.DeviceID != 7 || r.CreatedAt != in.CreatedAt {
		t.Fatalf("钩子记录缺少归属/受理时刻: %+v", r)
	}

	// 重复 result 重放：Complete 无从分辨旧状态（不读 Redis），照旧通知 ——
	// 幂等由实现方按 ref 兜底（契约见 TerminalHook 注释）。
	if err := cs.Complete(ctx, in, &agentproto.DockerCmdResult{Ref: "1001", OK: true}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Status != StatusSucceeded {
		t.Fatalf("重放通知照旧（幂等在下游），且必须是本次结论: %+v", got)
	}
}

// TestTerminalHookOnTimeout 钉住 Timeout 侧：只有真的发生「pending → timeout」
// 转换才通知，交出的是一份**终态拷贝**（入参记录的状态字段不被改动）。
func TestTerminalHookOnTimeout(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()

	var got []*CmdRecord
	cs.WithTerminalHook(func(_ context.Context, rec *CmdRecord) { got = append(got, rec) })

	in := rec("2002", 7)
	if err := cs.Create(ctx, in, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Timeout(ctx, in); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("超时终结必须通知一次, got %d", len(got))
	}
	r := got[0]
	if r.Status != StatusTimeout || r.Error != "指令超时未完成" || r.FinishedAt <= 0 {
		t.Fatalf("钩子收到的必须是终态拷贝（status=timeout + 结论句 + 终态时刻）: %+v", r)
	}
	// 入参记录**不得**被壳子改写：真相在 Redis 的原子脚本里，不是本地对象
	//（改写会让调用方以为「我手里这条已经是终态」，误导后续判断）。
	if in.Status != StatusPending || in.FinishedAt != 0 {
		t.Fatalf("Timeout 不得改写入参记录: %+v", in)
	}

	// 已终结的记录再 Timeout：脚本返回 0，不构成转换，不得惊动钩子。
	if err := cs.Timeout(ctx, in); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("重复 Timeout 不得再通知, got %d", len(got))
	}

	// 已完成的记录同样不构成转换（事实优先于推断）。
	done := rec("2003", 7)
	if err := cs.Create(ctx, done, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, done, &agentproto.DockerCmdResult{Ref: "2003", OK: true}); err != nil {
		t.Fatal(err)
	}
	before := len(got)
	if err := cs.Timeout(ctx, done); err != nil {
		t.Fatal(err)
	}
	if len(got) != before {
		t.Fatalf("已完成记录被 sweep 扫到不得触发钩子, got %d", len(got)-before)
	}
}

// TestSweepNotifiesEachTerminated 钉住 sweep 路径：每一轮被终结的记录各通知一次
// （8d 的批量场景 —— 服务端推断的 timeout 也是任务历史里的一行）。
func TestSweepNotifiesEachTerminated(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()

	refs := []string{}
	cs.WithTerminalHook(func(_ context.Context, rec *CmdRecord) { refs = append(refs, rec.Ref) })

	for _, ref := range []string{"3001", "3002"} {
		if err := cs.Create(ctx, rec(ref, 7), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	n, err := cs.Sweep(ctx, time.Now().Add(2*time.Second), 10)
	if err != nil || n != 2 {
		t.Fatalf("sweep 应终结 2 条: n=%d err=%v", n, err)
	}
	if len(refs) != 2 {
		t.Fatalf("每条终结都要通知（历史面靠它落地 timeout 行）: %v", refs)
	}
}

// TestTerminalHookNilSafe 钉住「未装配 = 行为与引入钩子之前逐字一致」：
// 不注入钩子时两条终态路径照常工作，不 panic。
func TestTerminalHookNilSafe(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()

	done := rec("4001", 7)
	if err := cs.Create(ctx, done, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, done, &agentproto.DockerCmdResult{Ref: "4001", OK: true}); err != nil {
		t.Fatal(err)
	}
	to := rec("4002", 7)
	if err := cs.Create(ctx, to, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Timeout(ctx, to); err != nil {
		t.Fatal(err)
	}
}
