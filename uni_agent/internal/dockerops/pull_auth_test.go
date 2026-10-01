package dockerops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/registry"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── image:pull 的仓库认证（4c）────────────────────────────────────────────

// pullCmdWithAuth 编一条带仓库认证的 image:pull 指令（协议已校验 Auth 只能
// 出现在 pull 上、registry 与 options.registry 同键 —— 这里是合规形态）。
func pullCmdWithAuth() *agentproto.DockerCmd {
	return &agentproto.DockerCmd{
		Ref:    pullRef,
		Action: agentproto.DockerActionImagePull,
		Options: agentproto.DockerCmdOptions{
			Target:   "harbor.example.com/app:1.2",
			Registry: "harbor.example.com",
		},
		Auth: &agentproto.DockerRegistryAuth{
			Registry: "harbor.example.com",
			Username: "robot$ci",
			Password: "agent-side-secret-77",
		},
	}
}

// TestEncodePullAuthRoundTrip：ImageAuth（P2 起 pull/push 的公共凭据面）→ SDK AuthConfig → X-Registry-Auth 头
// 的解码往返 —— 认证三元组逐字段无损（本包唯一碰 SDK 的地方，没有 daemon 也要测到）。
func TestEncodePullAuthRoundTrip(t *testing.T) {
	encoded, err := encodeImageAuth(&ImageAuth{
		Registry: "harbor.example.com:8443", Username: "robot$ci", Password: "s3/p@ss:with:colons",
	})
	if err != nil {
		t.Fatalf("encodeImageAuth: %v", err)
	}
	got, err := registry.DecodeAuthConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeAuthConfig: %v", err)
	}
	if got.ServerAddress != "harbor.example.com:8443" || got.Username != "robot$ci" || got.Password != "s3/p@ss:with:colons" {
		t.Fatalf("认证三元组往返不一致: %+v", got)
	}
}

// TestPullImageAuthPassesToAdapter：执行器把注入的认证**原样**交给 adapter
// （stubAPI 记录 pullAuths）；无凭据指令保持 nil（与 4b 逐字一致）。
//
// 7c 起会话管理器是写执行器的必接依赖（构造契约见 SetSessions），本测试与
// pull_progress_test 的折叠测试同用 newPullFixture —— 断言的仍是「认证在
// 指令 → adapter 这一条瞬时路径上」的保真，进度帧只是伴随产物。
func TestPullImageAuthPassesToAdapter(t *testing.T) {
	api := &stubAPI{}
	w, _, _, _ := newPullFixture(api)

	if _, err := w.Do(context.Background(), pullCmdWithAuth()); err != nil {
		t.Fatalf("带凭据拉取应成功: %v", err)
	}
	if len(api.pullAuths) != 1 || api.pullAuths[0] == nil {
		t.Fatalf("adapter 必须收到认证: %+v", api.pullAuths)
	}
	got := api.pullAuths[0]
	if got.Registry != "harbor.example.com" || got.Username != "robot$ci" || got.Password != "agent-side-secret-77" {
		t.Fatalf("传给 adapter 的认证与指令不一致: %+v", got)
	}

	// 无凭据 → nil（4b 回归：core 不注入 auth 时,执行器也不该造出一个）。
	if _, err := w.Do(context.Background(), pullCmd()); err != nil {
		t.Fatalf("无凭据拉取应成功: %v", err)
	}
	if len(api.pullAuths) != 2 || api.pullAuths[1] != nil {
		t.Fatalf("无凭据拉取传给 adapter 的认证必须是 nil: %+v", api.pullAuths)
	}
}

// TestPullAuthNeverLeaksIntoFramesOrResult：密码零泄漏钉（4c 硬约束）——
// 带凭据拉取的**进度帧**与**结果 error/detail** 都不含密码字面量。
//
// 认证只在「指令 → adapter → daemon 编码头」这一条瞬时路径上；帧与 result
// 是结构化的进度/结论,任何泄漏都是本包自己把密码抄进去 —— 这条测试
// 让这种抄写立刻变红。
func TestPullAuthNeverLeaksIntoFramesOrResult(t *testing.T) {
	const secret = "agent-side-secret-77"
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pullCh: ch}
	w, _, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pullCmdWithAuth())
		done <- err
	}()

	// 一条消息行 + 两条层进度 —— 模拟真实 daemon 流水（daemon 不回显认证）。
	ch <- PullProgress{Status: "Pulling from harbor.example.com/app"}
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 1, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	ch <- PullProgress{ID: "aaa", Status: "Downloading", Current: 2, Total: 100}
	close(ch)

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("拉取应成功: %v", err)
	}
	// 等终态项与 eof 冲刷出来。
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 2 })

	for i, f := range sink.all() {
		if strings.Contains(string(f.Data), secret) {
			t.Fatalf("第 %d 帧泄漏密码字面量: %s", i, f.Data)
		}
	}
	// 认证三元组本身也不该出现在任何帧里（不只是密码 —— 用户名也不能有）。
	for i, f := range sink.all() {
		if strings.Contains(string(f.Data), `"username"`) || strings.Contains(string(f.Data), "robot$ci") {
			t.Fatalf("第 %d 帧携带认证字段: %s", i, f.Data)
		}
	}
}

// TestPullAuthFailureResultCarriesNoSecret：失败路径的结论句/detail 同样不含
// 密码 —— daemon 原文（pullErr）会进 detail,但认证绝不混入本包生成的任何文本
// （结果文本要么是结论句、要么是 daemon 原文,若出现密码必是本包从 auth 抄写,这里钉死）。
func TestPullAuthFailureResultCarriesNoSecret(t *testing.T) {
	const secret = "fail-path-secret"
	ch := make(chan PullProgress, 2)
	api := &stubAPI{pullCh: ch, pullErr: errors.New("denied: requested access to the resource is denied")}
	w, _, _, _ := newPullFixture(api)

	cmd := pullCmdWithAuth()
	cmd.Auth.Password = secret
	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), cmd)
		done <- err
	}()
	ch <- PullProgress{Status: "Downloading"}
	close(ch)

	err := pullResultOf(t, done)
	if err == nil {
		t.Fatal("拉取应失败（denied）")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("失败结论/详情不得包含密码字面量: %v", err)
	}
	// 结果文本必须是结论句 + daemon 原文的折叠（wrapDocker 的既有口径）。
	if !strings.Contains(err.Error(), "拉取镜像失败") {
		t.Fatalf("结论句照旧: %v", err)
	}
}

// TestPullAuthNormalizedByCoreNotAgent：agent 不归一化 registry —— core 的
// 凭据库已按规范化键解出,agent 只管把同一把键折进 ServerAddress（译码保真）。
func TestPullAuthNormalizedByCoreNotAgent(t *testing.T) {
	api := &stubAPI{}
	w, _, _, _ := newPullFixture(api)
	cmd := pullCmdWithAuth()
	cmd.Options.Registry = "Harbor.Example.COM"
	cmd.Auth.Registry = "Harbor.Example.COM"

	if _, err := w.Do(context.Background(), cmd); err != nil {
		t.Fatalf("带凭据拉取应成功: %v", err)
	}
	if got := api.pullAuths[0]; got.Registry != "Harbor.Example.COM" {
		t.Fatalf("agent 必须保真透传 registry（不自行归一化）: %+v", got)
	}
}
