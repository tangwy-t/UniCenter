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
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── P2·分发闭环（image:build / image:push）的 core 受理面测试 ─────────────────
//
// 覆盖三条线：进度会话预登记（build_<ref>/push_<ref> 与 4b 同款时序）、推送的
// 4c 凭据注入（与拉取同一 ResolveAuth 读口、同一零泄漏纪律）、build 四个选项的
// 透传与协议层校验（core 受托付的只是「原样传递 + 在受理前验形态」）。

// TestBuildPushProgressSessionsPreRegistered：受理 build/push 时按派生句柄预登记
// 进度会话（Kind 与前缀、元数据齐全）—— 帧在指令 pending 期间就能找到主人；
// 非进度动作不登记。与 4b 的拉取预登记同一条时序论证。
func TestBuildPushProgressSessionsPreRegistered(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := dockerstream.NewRegistry(nil)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(dockerstate.NewCmdStore(rdb), sender, dockerstate.NewStore(rdb), nil).
		WithStreamSessions(sessions)
	ctx := context.Background()

	buildRef, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImageBuild,
		Options: &request.DockerCmdOptionsReq{
			Context: "app.tar.gz", Dockerfile: "docker/Dockerfile.prod", Tag: "app:1",
			Args: map[string]string{"NODE_ENV": "prod"},
		},
	})
	if err != nil {
		t.Fatalf("构建必须被受理: %v", err)
	}
	build := sessions.Get(agentproto.DockerBuildSessionID(buildRef))
	if build == nil {
		t.Fatalf("受理构建必须预登记进度会话（句柄 = %s）", agentproto.DockerBuildSessionID(buildRef))
	}
	if build.Kind() != dockerstream.KindBuild || build.UserID() != 42 || build.DeviceID() != 7 ||
		build.Action() != agentproto.DockerActionImageBuild || build.Ref() != buildRef {
		t.Fatalf("构建进度会话元数据不符: kind=%v user=%d dev=%d action=%s ref=%s",
			build.Kind(), build.UserID(), build.DeviceID(), build.Action(), build.Ref())
	}

	pushRef, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePush, Target: "harbor.example.com/app:1",
	})
	if err != nil {
		t.Fatalf("推送必须被受理: %v", err)
	}
	push := sessions.Get(agentproto.DockerPushSessionID(pushRef))
	if push == nil {
		t.Fatalf("受理推送必须预登记进度会话（句柄 = %s）", agentproto.DockerPushSessionID(pushRef))
	}
	if push.Kind() != dockerstream.KindPush || push.Action() != agentproto.DockerActionImagePush ||
		push.Ref() != pushRef || push.DeviceID() != 7 {
		t.Fatalf("推送进度会话元数据不符: kind=%v action=%s ref=%s dev=%d",
			push.Kind(), push.Action(), push.Ref(), push.DeviceID())
	}

	// 两个派生句柄与拉取句柄互不共串（前缀族是帧路由的唯一依据）。
	if sessions.Get(agentproto.DockerPullSessionID(buildRef)) != nil {
		t.Fatal("build 的 ref 不得登记出 pull 句柄")
	}
}

// TestBuildOptionsPassThroughAndValidation：build 四个选项原样到达 agent 载荷；
// 非法 context 在协议校验层被 400（core 不代替 agent 消化形态问题）。
func TestBuildOptionsPassThroughAndValidation(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImageBuild,
		Options: &request.DockerCmdOptionsReq{
			Context: "src.tar.gz", Dockerfile: "docker/Dockerfile.prod", Tag: "app:1",
			Args: map[string]string{"NODE_ENV": "prod", "A_B": "v"},
		},
	})
	if err != nil {
		t.Fatalf("build 必须被受理: %v", err)
	}
	got := sender.sent[0]
	if got.Ref != ref || got.Options.Context != "src.tar.gz" ||
		got.Options.Dockerfile != "docker/Dockerfile.prod" || got.Options.Tag != "app:1" ||
		got.Options.Args["NODE_ENV"] != "prod" || got.Options.Args["A_B"] != "v" {
		t.Fatalf("build options 必须原样到达 agent: %+v", got.Options)
	}
	if got.Auth != nil {
		t.Fatal("build 不注入任何凭据")
	}

	// 非法 context（路径形态）在协议校验层 400 —— 与「unknown action」一样不下发。
	before := len(sender.sent)
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action:  agentproto.DockerActionImageBuild,
		Options: &request.DockerCmdOptionsReq{Context: "../evil.tar", Tag: "app:1"},
	}); err == nil || !strings.Contains(err.Error(), "指令参数不合法") {
		t.Fatalf("路径形态 context 必须 400: %v", err)
	}
	if len(sender.sent) != before {
		t.Fatal("被拒的 build 不得下发")
	}
}

// TestBuildRejectsRegistryOption：registry 是 pull/push 的凭据键（4c），挂在 build
// 上是字段归属错误 —— 受理处 400 且**不触碰凭据解析器**（构建的基础镜像走宿主侧
// docker login，凭据面不服务它）。
func TestBuildRejectsRegistryOption(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	resolver := &fakeRegistryResolver{found: true, user: "u", pass: "p"}
	svc = svc.WithRegistryAuth(resolver)
	ctx := context.Background()

	// 非法 target + registry —— 归属校验先拦掉 registry，但 target 的拒绝更早；
	// 稳妥起见只带 registry（build 没有 target）。
	if _, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImageBuild,
		Options: &request.DockerCmdOptionsReq{
			Context: "app.tar", Tag: "app:1", Registry: "harbor.example.com",
		},
	}); err == nil || !strings.Contains(err.Error(), "指令参数不合法") {
		t.Fatalf("build 带 registry 必须 400（字段归属），实际 %v", err)
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("build 的 registry 不得触碰凭据解析器: %v", resolver.asked)
	}
	if len(sender.sent) != 0 {
		t.Fatal("被拒的 build 不得下发")
	}
}

// TestPushWithRegistryInjectsAuth：image:push 带 registry → 受理时解出的三元组
// **随指令消息瞬时注入**（与拉取同一个 ResolveAuth 读口、同一条注入路径）——
// Auth 与 options.registry 同一把键、消息能过协议 Validate、指令记录不含密码字面量
// （零泄漏复扫：serialize 再 grep）。
func TestPushWithRegistryInjectsAuth(t *testing.T) {
	svc, sender, cmds := newPullCmdService(t)
	resolver := &fakeRegistryResolver{found: true, user: "robot$ci", pass: "push-secret-11"}
	svc = svc.WithRegistryAuth(resolver)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePush,
		Target: "app:1",
		Options: &request.DockerCmdOptionsReq{
			Registry: "harbor.example.com",
		},
	})
	if err != nil {
		t.Fatalf("带凭据的推送应被受理: %v", err)
	}
	if len(resolver.asked) != 1 || resolver.asked[0] != "harbor.example.com" {
		t.Fatalf("解析器只该被受理路径问一次该键: %v", resolver.asked)
	}
	cmd := sender.sent[0]
	if cmd.Auth == nil || cmd.Auth.Username != "robot$ci" || cmd.Auth.Password != "push-secret-11" {
		t.Fatalf("注入的三元组与解析结果不一致: %+v", cmd.Auth)
	}
	if cmd.Auth.Registry != cmd.Options.Registry || cmd.Options.Registry != "harbor.example.com" {
		t.Fatalf("Auth.Registry 必须与 options.registry 同一把键: %+v / %+v", cmd.Auth, cmd.Options)
	}
	// 两端同规：agent 收到这条消息能解、能过 Validate（含 auth 归属允许 push）。
	if err := cmd.Validate(); err != nil {
		t.Fatalf("下发的推送指令必须能过协议 Validate: %v", err)
	}
	// 密码只存在于瞬时的下行消息 —— 指令记录（Redis）没有它的槽位。
	rec, err := cmds.Get(ctx, ref)
	if err != nil || rec == nil {
		t.Fatalf("指令记录可读性异常: rec=%v err=%v", rec, err)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), "push-secret-11") {
		t.Fatal("指令记录不得包含密码字面量（零泄漏复扫）")
	}
}

// TestPushWithoutRegistryKeepsLegacyBehavior：不传 registry 的推送 Auth=nil、
// 不触碰解析器 —— 公共仓库/宿主侧 docker login 的自由度与 4b 之前拉取一致。
func TestPushWithoutRegistryKeepsLegacyBehavior(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	resolver := &fakeRegistryResolver{found: true, user: "u", pass: "p"}
	svc = svc.WithRegistryAuth(resolver)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePush, Target: "app:1",
	})
	if err != nil {
		t.Fatalf("无凭据推送必须受理: %v", err)
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("不传 registry 不得触碰凭据解析器: %v", resolver.asked)
	}
	cmd := sender.sent[0]
	if cmd.Auth != nil || cmd.Ref != ref || cmd.Options.Target != "app:1" {
		t.Fatalf("无凭据推送载荷不符: %+v", cmd)
	}
}

// TestPushRegistryUnmatchedAndFailureConclusions：推送带 registry 的三个失败结论
// 与拉取同款同句（未匹配 400「没有这个仓库的凭据」/ 解析器错误 500「凭据不可用」
// / 未装配 500「未装配」），全部不下发。
func TestPushRegistryUnmatchedAndFailureConclusions(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		resolve *fakeRegistryResolver
		wantMsg string
	}{
		{"未匹配", &fakeRegistryResolver{found: false}, "没有这个仓库的凭据"},
		{"解析器错误", &fakeRegistryResolver{err: errBoom{}}, "凭据不可用"},
		{"未装配", nil, "未装配"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, sender, _ := newPullCmdService(t)
			if c.resolve != nil {
				svc = svc.WithRegistryAuth(c.resolve)
			}
			_, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
				Action: agentproto.DockerActionImagePush,
				Target: "app:1",
				Options: &request.DockerCmdOptionsReq{
					Registry: "registry.example.com",
				},
			})
			if err == nil || !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("推送带 registry 失败结论应为 %q，实际 %v", c.wantMsg, err)
			}
			if len(sender.sent) != 0 {
				t.Fatal("被拒的受理不得下发指令")
			}
		})
	}
}

// TestStreamSweepBuildPushReconciliation：构建/推送进度会话的终态对账与拉取
// **同一规则**（会话寿命 = 指令寿命）：pending 留（静默构建/排队是常态，一条
// cancel 都不许发），终态即移除（不发 cancel —— agent 侧同名会话要么已随 eof
// 收摊要么从未打开）。
func TestStreamSweepBuildPushReconciliation(t *testing.T) {
	f := newStreamFixture(t, 10*time.Minute)
	cases := []struct {
		action string
		ref    string
		sid    string
		kind   dockerstream.Kind
	}{
		{agentproto.DockerActionImageBuild, "1790000000000000002",
			agentproto.DockerBuildSessionID("1790000000000000002"), dockerstream.KindBuild},
		{agentproto.DockerActionImagePush, "1790000000000000003",
			agentproto.DockerPushSessionID("1790000000000000003"), dockerstream.KindPush},
	}
	for _, c := range cases {
		if err := f.reg.Register(dockerstream.Meta{
			SessionID: c.sid, DeviceID: 7, UserID: 42,
			Action: c.action, Ref: c.ref, Kind: c.kind, CreatedAt: *f.now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pending := true
	f.svc.WithPullPending(func(_ context.Context, ref string) (bool, error) {
		return pending, nil
	})
	ctx := context.Background()

	*f.now = f.now.Add(30 * time.Hour)
	if n, err := f.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("pending 的构建/推送会话不该被触碰: n=%d err=%v", n, err)
	}
	for _, c := range cases {
		if f.reg.Get(c.sid) == nil {
			t.Fatalf("pending 的 %s 会话必须留在注册表里", c.kind)
		}
	}
	if len(f.sender.snapshot()) != 0 {
		t.Fatalf("pending 的构建/推送会话不得收到 cancel: %+v", f.sender.snapshot())
	}

	pending = false
	if n, err := f.svc.Sweep(ctx); err != nil || n != 2 {
		t.Fatalf("终态指令的构建/推送会话必须被回收: n=%d err=%v", n, err)
	}
	for _, c := range cases {
		if f.reg.Get(c.sid) != nil {
			t.Fatalf("终态指令的 %s 会话必须从注册表移除", c.kind)
		}
	}
	if len(f.sender.snapshot()) != 0 {
		t.Fatal("回收不得发 cancel")
	}
}
