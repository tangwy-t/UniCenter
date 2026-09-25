package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
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
	// 非一期 action → 尚未开放
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionContainerStart, Target: "mysql"}); err == nil ||
		!strings.Contains(err.Error(), "尚未开放") {
		t.Fatalf("二期 action 必须被期次闸挡住: %v", err)
	}
	// 强确认档：一期没有强确认 action，用二期接口走期次闸已挡住；这里直接验证协议层确认校验
	// 走 agent 侧（Task B4 已覆盖）。
	// 离线 → 503 语义
	sender.offline = true
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{Action: agentproto.DockerActionImageInspect, Target: "mysql:8.0.22"}); err == nil {
		t.Fatal("agent 离线必须不受理（503）")
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
