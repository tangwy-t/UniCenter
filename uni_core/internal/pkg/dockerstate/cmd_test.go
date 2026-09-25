package dockerstate

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// mrClient 指向 miniredis 的地址：newTestStore 返回的 *Store 里已有 rdb，
// 但 CmdStore 需要独立构造，故各建一个客户端（与 newTestStore 同源）。
func mrClient(t *testing.T, mr *miniredis.Miniredis) goredis.UniversalClient {
	t.Helper()
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
}

func rec(ref string, deviceID uint64) *CmdRecord {
	return &CmdRecord{Ref: ref, DeviceID: deviceID, Action: agentproto.DockerActionContainerLogs,
		Target: "mysql", UserID: 42, Perm: "docker:inspect", Status: StatusPending,
		CreatedAt: time.Now().UnixMilli()}
}

func TestCreateAndInflight(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()

	if err := cs.Create(ctx, rec("1001", 7), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := cs.Get(ctx, "1001")
	if err != nil || got == nil || got.Status != StatusPending {
		t.Fatalf("记录未建立: %+v %v", got, err)
	}
	// 归属与权限码必须落进记录：轮询与流接入都靠它做二次校验
	if got.UserID != 42 || got.Perm != "docker:inspect" || got.DeviceID != 7 {
		t.Fatalf("记录缺少归属/权限信息: %+v", got)
	}
	// 同 (device, action, target) 在飞 → 返回已有 ref（服务端据此 409）
	if ref, _ := cs.Inflight(ctx, 7, agentproto.DockerActionContainerLogs, "mysql"); ref != "1001" {
		t.Fatalf("在飞索引未建立，得到 %q", ref)
	}
	// 不同 target 不冲突
	if ref, _ := cs.Inflight(ctx, 7, agentproto.DockerActionContainerLogs, "redis"); ref != "" {
		t.Fatalf("不同目标的指令不该互斥，得到 %q", ref)
	}
}

func TestCompleteWritesResultAndCleansIndexes(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()
	_ = cs.Create(ctx, rec("1001", 7), 30*time.Second)

	res := &agentproto.DockerCmdResult{Ref: "1001", OK: true,
		Payload: []byte(`{"lines":"ok\n"}`)}
	if err := cs.Complete(ctx, rec("1001", 7), res); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.Get(ctx, "1001")
	if got.Status != StatusSucceeded || len(got.Payload) == 0 {
		t.Fatalf("结果未写入: %+v", got)
	}
	// TTL 从**结果写入**起算（不是从派发起算，否则 15 分钟的 save 会在结果落盘前就 404）
	if ttl := mr.TTL(CmdKeyPrefix + "1001"); ttl <= 0 || ttl > ResultTTL {
		t.Fatalf("结果必须带 %v 的 TTL，实际 %v", ResultTTL, ttl)
	}
	if ref, _ := cs.Inflight(ctx, 7, agentproto.DockerActionContainerLogs, "mysql"); ref != "" {
		t.Fatalf("完成后必须清在飞索引，得到 %q", ref)
	}
	if n, _ := mrClient(t, mr).ZCard(ctx, CmdDeadlineKey).Result(); n != 0 {
		t.Fatalf("完成后必须从到期索引移除，实际 %d 条", n)
	}
}

func TestDueRefsAndTimeout(t *testing.T) {
	_, mr := newTestStore(t)
	rdb := mrClient(t, mr)
	cs := NewCmdStore(rdb)
	ctx := context.Background()
	_ = cs.Create(ctx, rec("1001", 7), 30*time.Second)
	_ = cs.Create(ctx, rec("1002", 7), 30*time.Second)

	// 到期索引存的是**绝对**到期时刻（派发时刻 + timeout）。两次查询的 now 必须跨过那条
	// 真实截止线：31s 后两条都到线，29s 时两条都还没到 —— 计划原文写的「now+1s 即到期」
	// 与 30s 超时互相矛盾（1s 后不可能到期），故此处只调整查询时刻、保持语义不变。
	due, err := cs.DueRefs(ctx, time.Now().Add(31*time.Second), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("两条都该到期: %v %v", due, err)
	}
	if due, _ := cs.DueRefs(ctx, time.Now().Add(29*time.Second), 10); len(due) != 0 {
		t.Fatalf("未到期的不该出现在结果里: %v", due)
	}
	if err := cs.Timeout(ctx, rec("1001", 7)); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.Get(ctx, "1001")
	if got.Status != StatusTimeout || got.Error == "" {
		t.Fatalf("超时终结必须带结论句: %+v", got)
	}
	// 已终结的记录再 sweep 是空操作（幂等）
	if err := cs.Timeout(ctx, rec("1001", 7)); err != nil {
		t.Fatal(err)
	}
}

// 事实优先于推断：结果晚于 sweep 到达时，**结果**必须胜出（一条实际执行成功的指令
// 不该在页面上永远显示「超时」）。
func TestLateResultWinsOverTimeout(t *testing.T) {
	_, mr := newTestStore(t)
	cs := NewCmdStore(mrClient(t, mr))
	ctx := context.Background()
	_ = cs.Create(ctx, rec("1001", 7), time.Second)
	if err := cs.Timeout(ctx, rec("1001", 7)); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, rec("1001", 7), &agentproto.DockerCmdResult{Ref: "1001", OK: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.Get(ctx, "1001")
	if got.Status != StatusSucceeded {
		t.Fatalf("迟到的真实结果必须覆盖服务端的超时推断，实际 %s", got.Status)
	}
	// 反向：已完成的记录不该被 sweep 改写成超时
	_ = cs.Create(ctx, rec("1003", 7), time.Second)
	_ = cs.Complete(ctx, rec("1003", 7), &agentproto.DockerCmdResult{Ref: "1003", OK: false, Error: "读取容器日志失败"})
	if err := cs.Timeout(ctx, rec("1003", 7)); err != nil {
		t.Fatal(err)
	}
	got, _ = cs.Get(ctx, "1003")
	if got.Status != StatusFailed || got.Error != "读取容器日志失败" {
		t.Fatalf("sweep 不得覆盖真实结果，实际 %+v", got)
	}
}
