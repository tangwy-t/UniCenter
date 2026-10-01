package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// fakeCmdSender 是 DockerCmdSender 的替身。
//
// 它记录**解码后**的指令而不是原始消息：受理路径的契约是「agent 能解回一个
// DockerCmd」，断言原始字节只能证明「发出去了点什么」。offline 打开时返回
// agenthub.ErrDeviceOffline —— 受理流程必须把它折成「不受理、不发 ref」。
type fakeCmdSender struct {
	sent    []*agentproto.DockerCmd
	offline bool
}

func (f *fakeCmdSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	if f.offline {
		return agenthub.ErrDeviceOffline
	}
	var cmd agentproto.DockerCmd
	if err := msg.DecodeData(&cmd); err == nil {
		f.sent = append(f.sent, &cmd)
	}
	return nil
}

// 指令受理：权限码来自策略表、期次闸、确认档、在飞去重、离线 503。
func TestDockerCmdServiceSend(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cmds := dockerstate.NewCmdStore(rdb)
	store := dockerstate.NewStore(rdb)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(cmds, sender, store, nil)
	ctx := context.Background()

	// 一期只读：受理并下发
	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionContainerLogs, Target: "mysql"})
	if err != nil {
		t.Fatalf("只读指令应被受理: %v", err)
	}
	if ref == "" || len(sender.sent) != 1 {
		t.Fatalf("指令未下发: ref=%q sent=%d", ref, len(sender.sent))
	}
	// 同 (device, action, target) 在飞 → 409 语义
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionContainerLogs, Target: "mysql"}); err == nil {
		t.Fatal("在飞指令必须拒绝（否则同一目标会并发执行两条）")
	}
	// 四期（配置编辑）已交付：期次闸不再拦截，但「强确认」（照抄项目名）仍是硬要求。
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionComposeFileWrite, Target: "uni-center",
		Options: &request.DockerCmdOptionsReq{Content: "services: {}\n", BaseHash: strings.Repeat("a", 64)},
	}); err == nil || !strings.Contains(err.Error(), "缺少确认信息") {
		t.Fatalf("四期写的强确认必须被强制（缺 confirm 一律拒）: %v", err)
	}
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionComposeFileWrite, Target: "uni-center",
		Options: &request.DockerCmdOptionsReq{Content: "services: {}\n", BaseHash: strings.Repeat("a", 64)},
		Confirm: "uni-center",
	}); err != nil {
		t.Fatalf("四期写带正确确认应被受理: %v", err)
	}
	// 离线 → 503 语义
	sender.offline = true
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionImageInspect, Target: "mysql:8.0.22"}); err == nil {
		t.Fatal("agent 离线必须不受理（503）")
	}
}

// TestDockerCmdServiceStreamActions：三期放行后的四条受理纪律 ——
//   - exec 带着 argv 下发；logs{follow:true} 原样下发 follow；
//   - 会话制的受理记录用**建立窗口**（30s）而不是 0：0 会在下一轮 sweep 被判成 timeout；
//   - 同设备流会话满 3 条时 core **先拒**（结论句比 agent 的 errStreamLimit 更贴近用户动作）。
func TestDockerCmdServiceStreamActions(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := dockerstream.NewRegistry(nil)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(dockerstate.NewCmdStore(rdb), sender, dockerstate.NewStore(rdb), nil).
		WithStreamSessions(sessions)
	ctx := context.Background()

	follow := true
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionContainerLogs, Target: "mysql",
		Options: &request.DockerCmdOptionsReq{Follow: &follow},
	}); err != nil {
		t.Fatalf("三期日志 Follow 必须被受理: %v", err)
	}
	if got := sender.sent[0]; !got.Options.Follow {
		t.Fatalf("follow 位必须原样到达 agent: %+v", got.Options)
	}

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionContainerExec, Target: "mysql",
		Options: &request.DockerCmdOptionsReq{Command: []string{"/bin/sh", "-lc", "ls -la"}},
	})
	if err != nil {
		t.Fatalf("三期终端必须被受理: %v", err)
	}
	exec := sender.sent[1]
	if len(exec.Options.Command) != 3 || exec.Options.Command[2] != "ls -la" {
		t.Fatalf("argv 必须原样到达 agent: %+v", exec.Options.Command)
	}
	// 受理记录的到期时刻 = now + 建立窗口（不是 0、也不是 15 分钟级的旧值）。
	score, err := mr.ZScore(dockerstate.CmdDeadlineKey, ref)
	if err != nil {
		t.Fatalf("会话制受理必须登记到期索引: %v", err)
	}
	deadline := time.UnixMilli(int64(score))
	if d := time.Until(deadline); d < dockerpolicy.SessionSetupTimeout-time.Minute || d > dockerpolicy.SessionSetupTimeout+time.Minute {
		t.Fatalf("会话制受理时限应约为建立窗口 %v，实际 %v", dockerpolicy.SessionSetupTimeout, d)
	}

	// 上限：塞满 3 条会话后，第 4 条被 core 先拒（不落到 agent 的 errStreamLimit 上）。
	for i := 0; i < dockerstream.MaxSessionsPerDevice; i++ {
		if err := sessions.Register(dockerstream.Meta{
			SessionID: fmt.Sprintf("sess-%016d", i), DeviceID: 7, UserID: 42,
			Action: agentproto.DockerActionContainerExec, Ref: "1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(sender.sent)
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionContainerExec, Target: "mysql3",
	}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("流会话满 3 条必须被 core 先拒（带结论句）: %v", err)
	}
	if len(sender.sent) != before {
		t.Fatal("被上限拒绝的指令不得下发")
	}
}

// TestDockerCmdServiceComposeLogs（5a 聚合日志）：受理纪律 —— 会话制记录用
// **建立窗口**（Session=true 让 createsStreamSession 与 pre-check 不必认识这个
// action）；follow/tail 原样透传；since 在受理处就拒（协议层的字段归属校验先于
// 一切 —— 静默无视会让用户拿到「最近 100 行」却以为要的是「T 时刻以来」）；
// 流会话满 3 条时 core 先拒（聚合日志占的就是用户流槽位）。
func TestDockerCmdServiceComposeLogs(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := dockerstream.NewRegistry(nil)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(dockerstate.NewCmdStore(rdb), sender, dockerstate.NewStore(rdb), nil).
		WithStreamSessions(sessions)
	ctx := context.Background()

	follow, tail, since := true, 50, int64(1790000000)

	// since 被拒（400 语义：请求写错了，而不是发下去被无视）。
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionComposeLogs, Target: "uni-center",
		Options: &request.DockerCmdOptionsReq{Since: &since},
	}); err == nil || !strings.Contains(err.Error(), "指令参数不合法") {
		t.Fatalf("聚合日志带 since 必须被拒（CLI 公共子集没有 --since）: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("被拒的聚合日志不得下发")
	}

	// follow/tail 原样到达 agent；记录用建立窗口（会话制）。
	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionComposeLogs, Target: "uni-center",
		Options: &request.DockerCmdOptionsReq{Follow: &follow, Tail: &tail},
	})
	if err != nil {
		t.Fatalf("聚合日志必须被受理: %v", err)
	}
	got := sender.sent[0]
	if !got.Options.Follow || got.Options.Tail != 50 || got.Options.Target != "uni-center" {
		t.Fatalf("follow/tail/target 必须原样到达 agent: %+v", got.Options)
	}
	score, err := mr.ZScore(dockerstate.CmdDeadlineKey, ref)
	if err != nil {
		t.Fatalf("会话制受理必须登记到期索引: %v", err)
	}
	deadline := time.UnixMilli(int64(score))
	if d := time.Until(deadline); d < dockerpolicy.SessionSetupTimeout-time.Minute || d > dockerpolicy.SessionSetupTimeout+time.Minute {
		t.Fatalf("聚合日志的受理时限应约为建立窗口 %v，实际 %v", dockerpolicy.SessionSetupTimeout, d)
	}

	// 上限预检：聚合日志占用户流槽位 —— 满 3 条后先拒（结论句，不下发）。
	for i := 0; i < dockerstream.MaxSessionsPerDevice; i++ {
		if err := sessions.Register(dockerstream.Meta{
			SessionID: fmt.Sprintf("clog-%016d", i), DeviceID: 7, UserID: 42,
			Action: agentproto.DockerActionComposeLogs, Ref: ref,
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(sender.sent)
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionComposeLogs, Target: "other",
	}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("流会话满 3 条必须被 core 先拒: %v", err)
	}
	if len(sender.sent) != before {
		t.Fatal("被上限拒绝的聚合日志不得下发")
	}
}

// TestDockerCmdServiceConfirmNotBypassedByForce：二期放行后补测确认档 —— 复核的正是
// 这个缺口：confirm 校验在 Send 的受理序里对**所有**请求无条件执行，force 只是保护档
// 的开关，不能把它变成「无需确认」。否则带 force 的请求就能跳过「照抄一遍」这一层，
// 而 force 恰恰是保护档里最该被确认的动作。大小写不做模糊匹配（模糊匹配会让确认
// 退化成「随便填点东西」，确认档的价值在于稀缺）。
func TestDockerCmdServiceConfirmNotBypassedByForce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewDockerCmdService(dockerstate.NewCmdStore(rdb), &fakeCmdSender{}, dockerstate.NewStore(rdb), nil)
	ctx := context.Background()

	force := true
	cases := []struct {
		name    string
		action  string
		target  string
		confirm string
		force   bool
		wantErr bool
	}{
		{"prune 缺 DELETE 确认", agentproto.DockerActionImagePrune, "", "", false, true},
		{"prune 带 force 仍需 DELETE 确认", agentproto.DockerActionImagePrune, "", "", true, true},
		{"prune 确认大小写不符", agentproto.DockerActionImagePrune, "", "delete", false, true},
		{"compose:down 缺目标名确认 + force", agentproto.DockerActionComposeDown, "uni-center", "", true, true},
		{"prune 照抄 DELETE → 受理", agentproto.DockerActionImagePrune, "", "DELETE", false, false},
		{"compose:down 照抄目标名 → 受理", agentproto.DockerActionComposeDown, "uni-center", "uni-center", false, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &request.DockerCmdReq{Action: tc.action, Target: tc.target, Confirm: tc.confirm}
			if tc.force {
				req.Options = &request.DockerCmdOptionsReq{Force: &force}
			}
			// 每条用例一台独立设备：避免「同目标在飞」的 409 混进确认档的判定。
			_, err := svc.Send(ctx, 42, uint64(100+i), req)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "缺少确认信息") {
					t.Fatalf("强确认档缺确认必须拒绝（且与 force 无关）: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("带正确确认必须受理: %v", err)
			}
		})
	}
}

// TestDockerCmdServiceLookupSweepAndRef 覆盖轮询侧的三个约束：归属、payload 形态、
// sweep 终结，以及 ref 生成器的两个要求（十进制、可替换）。
func TestDockerCmdServiceLookupSweepAndRef(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cmds := dockerstate.NewCmdStore(rdb)
	store := dockerstate.NewStore(rdb)
	svc := NewDockerCmdService(cmds, &fakeCmdSender{}, store, nil)
	ctx := context.Background()

	// ① 缺省 ref 生成器：必须是十进制串（协议 isDecimalID 的硬要求 —— 非十进制
	// 会让 agent 把整帧判成载荷非法）。
	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionContainerInspect, Target: "mysql"})
	if err != nil {
		t.Fatal(err)
	}
	if ref == "" || strings.Trim(ref, "0123456789") != "" {
		t.Fatalf("ref 必须是十进制串: %q", ref)
	}
	// ② 归属：同一个 ref 换一台设备查 → 404（与不存在同码，否则 ref 成了探测
	// 「别的主机上有什么指令」的钥匙）。
	if _, err := svc.Lookup(ctx, 8, ref); err == nil {
		t.Fatal("设备不匹配必须拒绝（404）")
	}
	rec, err := svc.Lookup(ctx, 7, ref)
	if err != nil || rec.Status != dockerstate.StatusPending {
		t.Fatalf("刚受理的指令应为 pending: %+v %v", rec, err)
	}

	// ③ 结果形态：payload 必须原样透出（json.RawMessage），而不是 base64 串 ——
	// 前端要直接把它当日志文本/inspect 数据用。
	res := &agentproto.DockerCmdResult{Ref: ref, OK: true, Payload: []byte(`{"Id":"abc"}`)}
	if err := cmds.Complete(ctx, rec, res); err != nil {
		t.Fatal(err)
	}
	done, err := cmds.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	out := svc.Result(done)
	if out.Status != dockerstate.StatusSucceeded {
		t.Fatalf("结果状态不符: %+v", out)
	}
	b, err := json.Marshal(out)
	if err != nil || !strings.Contains(string(b), `"payload":{"Id":"abc"}`) {
		t.Fatalf("payload 必须原样透出: %s %v", b, err)
	}

	// ④ sweep：到期的 pending 必须被终结为 timeout，且在飞索引一并清掉
	//（清不掉的话同目标会永远 409，而列表里没有任何在跑的东西）。
	due := &dockerstate.CmdRecord{Ref: "1002", DeviceID: 7, Action: agentproto.DockerActionContainerLogs,
		Target: "mysql", UserID: 42, Perm: "docker:inspect"}
	if err := cmds.Create(ctx, due, -time.Minute); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep 必须终结到期指令: n=%d err=%v", n, err)
	}
	to, err := cmds.Get(ctx, "1002")
	if err != nil || to == nil || to.Status != dockerstate.StatusTimeout || to.Error == "" {
		t.Fatalf("到期指令必须落成 timeout 且带结论句: %+v %v", to, err)
	}
	if staleRef, _ := cmds.Inflight(ctx, 7, agentproto.DockerActionContainerLogs, "mysql"); staleRef != "" {
		t.Fatalf("终结后必须清在飞索引: %q", staleRef)
	}
	// ⑤ 不存在的 ref → 404（不是空响应：轮询端点靠它判断「已过期」）
	if _, err := svc.Lookup(ctx, 7, "9999"); err == nil {
		t.Fatal("不存在的 ref 必须拒绝（404）")
	}

	// ⑥ ref 生成器可替换（装配层可注入进程级雪花节点生成的十进制串）。
	svc.idGen = func() string { return "1234567890123456789" }
	got, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionComposeFileRead, Target: "uni-center"})
	if err != nil || got != "1234567890123456789" {
		t.Fatalf("idGen 未生效: %q %v", got, err)
	}

	// ⑦ 能力闸：agent 已自报 docker 不可用 → 不受理；**从未上报**不算不可用
	//（快照是周期事件，刚上线、第一帧还没到的 agent 完全可能收得下指令）。
	down := &agentproto.DockerState{T: time.Now().UnixMilli(), DockerOK: false, Error: "无法连接 docker.sock"}
	if err := store.Save(ctx, 9, down, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, 42, 9, &request.DockerCmdReq{Action: agentproto.DockerActionContainerLogs, Target: "mysql"}); err == nil {
		t.Fatal("docker 不可用的主机必须不受理")
	}
	if _, err := svc.Send(ctx, 42, 10, &request.DockerCmdReq{Action: agentproto.DockerActionContainerLogs, Target: "mysql2"}); err != nil {
		t.Fatalf("从未上报过快照的主机不该被能力闸拒绝: %v", err)
	}
}

// TestDockerToProtocolOptions 盯住 HTTP options → 协议 options 的三处语义：
// 指针解引用（显式 false 不能被缺省吞掉）、缺省留给协议、target 从顶层并进 options。
func TestDockerToProtocolOptions(t *testing.T) {
	tail, since, n, force, all := 200, int64(1700000000), 3, true, false
	overwrite, orphans, volumes := true, true, true
	req := &request.DockerCmdReq{
		Action: agentproto.DockerActionImageSave, Target: "mysql:8.0.22",
		Options: &request.DockerCmdOptionsReq{
			Tail: &tail, Since: &since, N: &n, Force: &force, Filename: "image.tar",
			Overwrite: &overwrite, All: &all, RemoveOrphans: &orphans, Volumes: &volumes,
			Content: "services: {}", BaseHash: strings.Repeat("a", 64),
			Src: "a:1", Dst: "b:1", Patch: json.RawMessage(`{"services":{"db":null}}`),
		},
	}
	opts := toProtocolOptions(req)
	if opts.Target != "mysql:8.0.22" {
		t.Fatalf("target 必须从顶层字段并进 options: %+v", opts)
	}
	if opts.Tail != 200 || opts.Since != 1700000000 || opts.N == nil || *opts.N != 3 {
		t.Fatalf("数值指针未解引用: %+v", opts)
	}
	if !opts.Force || !opts.Overwrite || opts.All || !opts.RemoveOrphans || !opts.Volumes {
		t.Fatalf("布尔指针未解引用（显式 false 必须保留）: %+v", opts)
	}
	if opts.Filename != "image.tar" || opts.Content != "services: {}" ||
		opts.BaseHash != strings.Repeat("a", 64) || opts.Src != "a:1" || opts.Dst != "b:1" {
		t.Fatalf("字符串字段未映射: %+v", opts)
	}
	if len(opts.Patch) != 1 || opts.Patch["services"] == nil {
		t.Fatalf("patch 必须解成协议侧的对象: %+v", opts.Patch)
	}
	// 协议载荷持有自己的值：改动请求体的指针不得影响已经组好的载荷（它要进消息）。
	n2 := 9
	req.Options.N = &n2
	if *opts.N != 3 {
		t.Fatal("协议载荷不该与请求体共享指针")
	}

	// 缺省：不传 options 与传空 options 都必须落到**协议的默认**。
	// 本函数替协议编一个 Tail=100，会让「用户没选」与「用户选了 100」再也分不开。
	for _, r := range []*request.DockerCmdReq{
		{Action: agentproto.DockerActionContainerLogs, Target: "mysql"},
		{Action: agentproto.DockerActionContainerLogs, Target: "mysql", Options: &request.DockerCmdOptionsReq{}},
	} {
		o := toProtocolOptions(r)
		if o.Tail != 0 || o.Since != 0 || o.N != nil || o.Force || o.Filename != "" {
			t.Fatalf("缺省必须留给协议（Tail 0 = 协议侧的默认 100 行）: %+v", o)
		}
		if err := agentproto.ValidateDockerCmdOptions(r.Action, o); err != nil {
			t.Fatalf("缺省组出来的 options 必须能过协议校验: %v", err)
		}
	}

	// 三期两个新字段：follow 是「显式 false 与缺席不同」的又一实例（指针解引用），
	// command 是切片，必须复制（协议载荷不该与请求体共享底层数组）。
	follow, noFollow := true, false
	cmdReq := &request.DockerCmdReq{
		Action: agentproto.DockerActionContainerExec, Target: "mysql",
		Options: &request.DockerCmdOptionsReq{Follow: &follow, Command: []string{"/bin/sh", "-lc", "top"}},
	}
	o3 := toProtocolOptions(cmdReq)
	if !o3.Follow || len(o3.Command) != 3 || o3.Command[2] != "top" {
		t.Fatalf("三期字段未映射: %+v", o3)
	}
	cmdReq.Options.Command[2] = "rm -rf /"
	if o3.Command[2] != "top" {
		t.Fatal("command 必须复制，不能与请求体共享底层数组")
	}
	o4 := toProtocolOptions(&request.DockerCmdReq{
		Action: agentproto.DockerActionContainerLogs, Target: "mysql",
		Options: &request.DockerCmdOptionsReq{Follow: &noFollow},
	})
	if o4.Follow {
		t.Fatal("显式 follow=false 必须保留 false（与缺席同值但语义不同，协议侧不受影响）")
	}

	// 解不开的 patch（不是 JSON 对象）不许被当成合法值发出去：保持空 → 协议校验拒绝 → 400。
	bad := toProtocolOptions(&request.DockerCmdReq{
		Action: agentproto.DockerActionComposeFilePatch, Target: "uni-center",
		Options: &request.DockerCmdOptionsReq{BaseHash: strings.Repeat("a", 64), Patch: json.RawMessage(`[1,2]`)},
	})
	if bad.Patch != nil {
		t.Fatalf("非法 patch 不该被编造出来: %+v", bad.Patch)
	}
	if err := agentproto.ValidateDockerCmdOptions(agentproto.DockerActionComposeFilePatch, bad); err == nil {
		t.Fatal("非法 patch 必须被协议校验拒掉（400），而不是静默丢弃后照发")
	}
}

// ── 拉取进度会话的预登记（4b）─────────────────────────────────────────────

// TestDockerCmdServiceRegistersPullProgressSession：受理 image:pull 时**同步**预登记
// 进度会话（句柄 = 协议派生的 pull_<ref>、Kind=Pull、发起人/设备/ref 齐全）——
// agent 收到指令即可开始拉取，它的帧先于任何 result 到达，届时注册表里必须有主人。
// 非拉取 action 不登记任何会话。
func TestDockerCmdServiceRegistersPullProgressSession(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := dockerstream.NewRegistry(nil)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(dockerstate.NewCmdStore(rdb), sender, dockerstate.NewStore(rdb), nil).
		WithStreamSessions(sessions)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull, Target: "nginx:latest",
	})
	if err != nil {
		t.Fatalf("拉取必须被受理: %v", err)
	}
	sess := sessions.Get(agentproto.DockerPullSessionID(ref))
	if sess == nil {
		t.Fatalf("受理拉取必须预登记进度会话（句柄 = %s）", agentproto.DockerPullSessionID(ref))
	}
	if sess.Kind() != dockerstream.KindPull || sess.UserID() != 42 || sess.DeviceID() != 7 ||
		sess.Action() != agentproto.DockerActionImagePull || sess.Ref() != ref {
		t.Fatalf("进度会话元数据不符: kind=%v user=%d dev=%d action=%s ref=%s",
			sess.Kind(), sess.UserID(), sess.DeviceID(), sess.Action(), sess.Ref())
	}

	// 非拉取动作不预登记（句柄空间只属于拉取）。
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionContainerStart, Target: "mysql",
	}); err != nil {
		t.Fatalf("启动必须被受理: %v", err)
	}
	if sessions.Len() != 1 {
		t.Fatalf("非拉取动作不得登记会话，注册表里应只有拉取会话: %d", sessions.Len())
	}

	// 在飞去重不受影响：同目标二次拉取还是 409（老语义）。
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull, Target: "nginx:latest",
	}); err == nil {
		t.Fatal("同目标二次拉取必须 409")
	}
}
