package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// fakeRegistryResolver 是 RegistryAuthResolver 的替身：可编程返回凭据/未匹配/错误，
// 并记录请求过的仓库地址（断言「受理路径只查了它该查的键」）。
type fakeRegistryResolver struct {
	user, pass string
	found      bool
	err        error
	asked      []string
}

func (f *fakeRegistryResolver) ResolveAuth(_ context.Context, registry string) (string, string, bool, error) {
	f.asked = append(f.asked, registry)
	return f.user, f.pass, f.found, f.err
}

func newPullCmdService(t *testing.T) (*DockerCmdService, *fakeCmdSender, *dockerstate.CmdStore) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cmds := dockerstate.NewCmdStore(rdb)
	sender := &fakeCmdSender{}
	return NewDockerCmdService(cmds, sender, dockerstate.NewStore(rdb), nil), sender, cmds
}

// TestPullWithRegistryInjectsAuth：image:pull 带 registry → 受理时解出的三元组
// **随指令消息注入**（选型 A）：agent 侧能解回 DockerCmd.Auth，且 Auth 与
// options.registry 同一把键；解除的消息必须能过协议 Validate（两端同规）。
func TestPullWithRegistryInjectsAuth(t *testing.T) {
	svc, sender, cmds := newPullCmdService(t)
	resolver := &fakeRegistryResolver{found: true, user: "robot$ci", pass: "pull-secret-9"}
	svc = svc.WithRegistryAuth(resolver)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull,
		Target: "harbor.example.com/app:1.2",
		Options: &request.DockerCmdOptionsReq{
			Registry: "harbor.example.com",
		},
	})
	if err != nil {
		t.Fatalf("带凭据的拉取应被受理: %v", err)
	}
	if len(resolver.asked) != 1 || resolver.asked[0] != "harbor.example.com" {
		t.Fatalf("解析器只该被受理路径问一次该键: %v", resolver.asked)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("应下发恰一条指令: %d", len(sender.sent))
	}
	cmd := sender.sent[0]
	if cmd.Auth == nil {
		t.Fatal("带 registry 的拉取必须带注入的 Auth")
	}
	if cmd.Auth.Username != "robot$ci" || cmd.Auth.Password != "pull-secret-9" {
		t.Fatalf("注入的三元组与解析结果不一致: %+v", cmd.Auth)
	}
	if cmd.Auth.Registry != cmd.Options.Registry || cmd.Options.Registry != "harbor.example.com" {
		t.Fatalf("Auth.Registry 必须与 options.registry 同一把键: %+v / %+v", cmd.Auth, cmd.Options)
	}
	// 协议自校验：两端用同一把尺（agent 收这条消息能解、能过 Validate）。
	if err := cmd.Validate(); err != nil {
		t.Fatalf("下发的指令必须能过协议 Validate: %v", err)
	}
	// 指令记录（Redis docker:cmd:<ref>）结构上不存在 options/auth 槽位 ——
	// 密码不可能随轮询/审计记录落盘（序列化再 grep 一次,钉成回归）。
	rec, err := cmds.Get(ctx, ref)
	if err != nil || rec == nil {
		t.Fatalf("指令记录可读性异常: rec=%v err=%v", rec, err)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), "pull-secret-9") {
		t.Fatal("指令记录不得包含密码字面量（记录不存 options/auth —— 密码只在瞬时的下行消息里）")
	}
}

// TestPullWithoutRegistryKeepsLegacyBehavior：不传 registry 时与 4b **逐字一致** ——
// Auth 必须为 nil、消息照旧下发（零破坏回归钉）。
func TestPullWithoutRegistryKeepsLegacyBehavior(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	resolver := &fakeRegistryResolver{found: true, user: "u", pass: "p"}
	svc = svc.WithRegistryAuth(resolver)
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull,
		Target: "nginx:1.27",
	})
	if err != nil {
		t.Fatalf("无凭据拉取必须照旧受理: %v", err)
	}
	_ = ref
	if len(resolver.asked) != 0 {
		t.Fatalf("不传 registry 不得触碰凭据解析器: %v", resolver.asked)
	}
	cmd := sender.sent[0]
	if cmd.Auth != nil {
		t.Fatalf("无凭据拉取的 Auth 必须为 nil（4b 行为不变）,实际 %+v", cmd.Auth)
	}
	if cmd.Ref != ref || cmd.Options.Target != "nginx:1.27" {
		t.Fatalf("指令载荷与既有形态不一致: %+v", cmd)
	}
}

// TestPullRegistryUnmatchedConclusion：未匹配凭据给结论句「没有这个仓库的凭据」
// —— 400 语义,不建记录、不下发（删除即失效在受理处兑现）。
func TestPullRegistryUnmatchedConclusion(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	svc = svc.WithRegistryAuth(&fakeRegistryResolver{found: false})
	ctx := context.Background()

	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull,
		Target: "registry.example.com/app:1",
		Options: &request.DockerCmdOptionsReq{
			Registry: "registry.example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "没有这个仓库的凭据") {
		t.Fatalf("未匹配凭据必须给结论句「没有这个仓库的凭据」,实际 ref=%q err=%v", ref, err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("被拒的受理不得下发指令")
	}
}

// TestPullRegistryResolverErrorFailClosed：解析器报错（主密钥缺失/密文损坏）
// → 500 结论句,不下发 —— 与「未匹配」必须可区分（一个是请求问题,一个是服务端问题）。
func TestPullRegistryResolverErrorFailClosed(t *testing.T) {
	svc, sender, _ := newPullCmdService(t)
	svc = svc.WithRegistryAuth(&fakeRegistryResolver{err: errBoom{}})
	ctx := context.Background()
	ref, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull,
		Target: "registry.example.com/app:1",
		Options: &request.DockerCmdOptionsReq{
			Registry: "registry.example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "凭据不可用") {
		t.Fatalf("解析器报错必须给「凭据不可用」结论句,实际 ref=%q err=%v", ref, err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("被拒的受理不得下发指令")
	}
}

// errBoom 是解析器错误的哨兵替身。
type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// TestPullRegistryWithoutResolver：装配缺失时带 registry 的拉取不得静默降级
// 成无凭据拉取（那会把「私有镜像拉不动」藏进 daemon 的 401）。
func TestPullRegistryWithoutResolver(t *testing.T) {
	svc, sender, _ := newPullCmdService(t) // 刻意不 WithRegistryAuth
	ctx := context.Background()
	_, err := svc.Send(ctx, 42, 7, &request.DockerCmdReq{
		Action: agentproto.DockerActionImagePull,
		Target: "registry.example.com/app:1",
		Options: &request.DockerCmdOptionsReq{
			Registry: "registry.example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("解析器未装配必须显式 500,实际 %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("被拒的受理不得下发指令")
	}
}
