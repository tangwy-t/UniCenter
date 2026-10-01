package dockerpolicy

import (
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 期望表**逐行照抄 spec §4.3.1**（权限码 / 超时 / 会话制）。
//
// 为什么要双源：这张表是「哪个 action 需要哪个权限」的唯一事实源，而它的错误形态是
// **越权**（把 delete 写成 manage）而不是崩溃 —— 只有一份独立的期望值才能看见它。
//（7c 删掉了期次列：CurrentPhase=5 且全表 ≤5，那道闸永不触发；权限/超时/会话制
// 三项是活语义，期望表随之收窄。）
func TestPolicyTableMatchesSpec(t *testing.T) {
	want := []struct {
		action  string
		perm    string
		timeout time.Duration
		// session 是会话制标记：会话制的「结果」是会话句柄，Timeout 不适用（0）。
		session bool
	}{
		{agentproto.DockerActionContainerCreate, permission.PermDockerManage, 30 * time.Second, false},
		{agentproto.DockerActionContainerStart, permission.PermDockerManage, 30 * time.Second, false},
		{agentproto.DockerActionContainerStop, permission.PermDockerManage, 30 * time.Second, false},
		{agentproto.DockerActionContainerRestart, permission.PermDockerManage, 60 * time.Second, false},
		{agentproto.DockerActionContainerRemove, permission.PermDockerDelete, 60 * time.Second, false},
		{agentproto.DockerActionContainerLogs, permission.PermDockerInspect, 30 * time.Second, false},
		{agentproto.DockerActionContainerInspect, permission.PermDockerInspect, 30 * time.Second, false},
		{agentproto.DockerActionContainerExec, permission.PermDockerExec, 0, true},
		{agentproto.DockerActionContainerStats, permission.PermDockerInspect, 0, true},
		{agentproto.DockerActionImageRemove, permission.PermDockerDelete, 60 * time.Second, false},
		{agentproto.DockerActionImagePrune, permission.PermDockerDelete, 120 * time.Second, false},
		{agentproto.DockerActionImagePull, permission.PermDockerManage, 15 * time.Minute, false},
		{agentproto.DockerActionImageTag, permission.PermDockerManage, 30 * time.Second, false},
		{agentproto.DockerActionImageInspect, permission.PermDockerInspect, 30 * time.Second, false},
		{agentproto.DockerActionImageSave, permission.PermDockerManage, 15 * time.Minute, false},
		{agentproto.DockerActionImageLoad, permission.PermDockerManage, 15 * time.Minute, false},
		// P2·分发闭环：build 30 分钟（冷缓存大工程）、push 15 分钟（网络长传输同 pull）。
		{agentproto.DockerActionImageBuild, permission.PermDockerManage, 30 * time.Minute, false},
		{agentproto.DockerActionImagePush, permission.PermDockerManage, 15 * time.Minute, false},
		{agentproto.DockerActionVolumeRemove, permission.PermDockerDelete, 60 * time.Second, false},
		{agentproto.DockerActionVolumePrune, permission.PermDockerDelete, 120 * time.Second, false},
		{agentproto.DockerActionNetworkRemove, permission.PermDockerDelete, 60 * time.Second, false},
		{agentproto.DockerActionComposeUp, permission.PermDockerManage, 120 * time.Second, false},
		{agentproto.DockerActionComposeStop, permission.PermDockerManage, 120 * time.Second, false},
		{agentproto.DockerActionComposeStart, permission.PermDockerManage, 120 * time.Second, false},
		{agentproto.DockerActionComposeRestart, permission.PermDockerManage, 120 * time.Second, false},
		{agentproto.DockerActionComposePull, permission.PermDockerManage, 15 * time.Minute, false},
		{agentproto.DockerActionComposeDown, permission.PermDockerDelete, 120 * time.Second, false},
		{agentproto.DockerActionComposeServiceScale, permission.PermDockerManage, 120 * time.Second, false},
		{agentproto.DockerActionComposeServiceRemoveContainers, permission.PermDockerDelete, 60 * time.Second, false},
		{agentproto.DockerActionComposeFileRead, permission.PermDockerInspect, 30 * time.Second, false},
		{agentproto.DockerActionComposeFileWrite, permission.PermDockerConfig, 30 * time.Second, false},
		{agentproto.DockerActionComposeFileValidate, permission.PermDockerConfig, 30 * time.Second, false},
		{agentproto.DockerActionComposeFilePatch, permission.PermDockerConfig, 30 * time.Second, false},
		// compose:logs（5a）：inspect 档（与 container:logs 同级）+ 会话制（结果 =
		// 会话句柄，follow 与非 follow 都是流）。
		{agentproto.DockerActionComposeLogs, permission.PermDockerInspect, 0, true},
	}
	if len(want) != len(All()) {
		t.Fatalf("策略表 %d 行，期望 %d 行（spec §4.3.1）", len(All()), len(want))
	}
	for _, w := range want {
		got, ok := Lookup(w.action)
		if !ok {
			t.Fatalf("策略表缺 %s", w.action)
		}
		if got.Perm != w.perm {
			t.Errorf("%s 的权限码 %q，期望 %q（越权风险）", w.action, got.Perm, w.perm)
		}
		if got.Session != w.session {
			t.Errorf("%s 的会话制标记 %v，期望 %v", w.action, got.Session, w.session)
		}
		if w.session {
			continue // 会话制无 sweep 超时
		}
		if got.Timeout != w.timeout {
			t.Errorf("%s 的超时 %v，期望 %v", w.action, got.Timeout, w.timeout)
		}
	}
}

// 与协议白名单**双向**一致：缺一条 → 该 action 永远 403（没有策略就取不到权限码）；
// 多一条 → 策略表里有一个协议不认识的幽灵动作。
//
// 唯一例外是 docker:events：它是 core 的**常驻内部订阅**（总览页活动流的
// 数据源），不进用户指令面 —— 用户在 /docker/hosts/:id/cmds 发它必须得到「未知操作」
// 而不是被下发。策略行缺席就是它的策略：没有权限码、没有 sweep 超时，
// 生命周期全部由 dockerevents 常驻管理器持有（引用计数 + 退避 + 对账）。
func TestPolicyCoversProtocolWhiteList(t *testing.T) {
	byAction := map[string]bool{}
	for _, p := range All() {
		byAction[p.Action] = true
	}
	for _, a := range agentproto.AllDockerActions() {
		if a == agentproto.DockerActionEvents {
			continue // 内部订阅（见函数注释）
		}
		if !byAction[a] {
			t.Fatalf("协议白名单里的 %s 没有策略行", a)
		}
	}
	for _, p := range All() {
		if !agentproto.IsDockerAction(p.Action) {
			t.Fatalf("策略表里的 %s 不在协议白名单", p.Action)
		}
	}
	// 反向钉住：策略表里**出现** events 就是把常驻订阅暴露给用户指令面（可以被人
	// 逐台下发），那正是「内部订阅」的定义所禁止的。
	if _, ok := Lookup(agentproto.DockerActionEvents); ok {
		t.Fatal("docker:events 不得有策略行（内部订阅不进用户指令面）")
	}
}

// 每个权限码都必须是注册过的（写错一个字符就是「所有请求 403」或「权限失效」）。
func TestPolicyPermissionsAreRegistered(t *testing.T) {
	all := map[string]bool{}
	for _, p := range permission.All() {
		all[p] = true
	}
	for _, p := range All() {
		if !all[p.Perm] {
			t.Fatalf("%s 的权限码 %q 未在 permission.All() 注册", p.Action, p.Perm)
		}
	}
}

// AcceptTimeout 是受理记录的 sweep 时限：会话制用建立窗口（SessionSetupTimeout），
// 其余沿用 Policy.Timeout。exec 的 Timeout=0 直接进 Create 会在下一轮 sweep 被
// 判成 timeout —— 这条断言盯住的正是那个「正在建立的会话被服务端提前判死」的坑。
func TestPolicyAcceptTimeout(t *testing.T) {
	if got := SessionSetupTimeout; got <= 0 || got > time.Minute {
		t.Fatalf("会话建立窗口应在 (0, 1m]，实际 %v", got)
	}
	exec, ok := Lookup(agentproto.DockerActionContainerExec)
	if !ok || !exec.Session {
		t.Fatal("exec 必须是会话制")
	}
	if got := exec.AcceptTimeout(); got != SessionSetupTimeout {
		t.Fatalf("会话制的受理时限应为建立窗口 %v，实际 %v", SessionSetupTimeout, got)
	}
	logs, _ := Lookup(agentproto.DockerActionContainerLogs)
	if got := logs.AcceptTimeout(); got != logs.Timeout {
		t.Fatalf("非会话制必须沿用策略超时 %v，实际 %v", logs.Timeout, got)
	}
}
