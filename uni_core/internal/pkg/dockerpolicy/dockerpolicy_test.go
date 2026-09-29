package dockerpolicy

import (
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 期望表**逐行照抄 spec §4.3.1**（权限码 / 超时 / 期次）。
//
// 为什么要双源：这张表是「哪个 action 需要哪个权限」的唯一事实源，而它的错误形态是
// **越权**（把 delete 写成 manage）而不是崩溃 —— 只有一份独立的期望值才能看见它。
func TestPolicyTableMatchesSpec(t *testing.T) {
	want := []struct {
		action  string
		perm    string
		timeout time.Duration
		phase   int
	}{
		{agentproto.DockerActionContainerStart, permission.PermDockerManage, 30 * time.Second, 2},
		{agentproto.DockerActionContainerStop, permission.PermDockerManage, 30 * time.Second, 2},
		{agentproto.DockerActionContainerRestart, permission.PermDockerManage, 60 * time.Second, 2},
		{agentproto.DockerActionContainerRemove, permission.PermDockerDelete, 60 * time.Second, 2},
		{agentproto.DockerActionContainerLogs, permission.PermDockerInspect, 30 * time.Second, 1},
		{agentproto.DockerActionContainerInspect, permission.PermDockerInspect, 30 * time.Second, 1},
		{agentproto.DockerActionContainerExec, permission.PermDockerExec, 0, 3},
		{agentproto.DockerActionImageRemove, permission.PermDockerDelete, 60 * time.Second, 2},
		{agentproto.DockerActionImagePrune, permission.PermDockerDelete, 120 * time.Second, 2},
		{agentproto.DockerActionImagePull, permission.PermDockerManage, 15 * time.Minute, 2},
		{agentproto.DockerActionImageTag, permission.PermDockerManage, 30 * time.Second, 2},
		{agentproto.DockerActionImageInspect, permission.PermDockerInspect, 30 * time.Second, 1},
		{agentproto.DockerActionImageSave, permission.PermDockerManage, 15 * time.Minute, 2},
		{agentproto.DockerActionImageLoad, permission.PermDockerManage, 15 * time.Minute, 2},
		{agentproto.DockerActionVolumeRemove, permission.PermDockerDelete, 60 * time.Second, 2},
		{agentproto.DockerActionVolumePrune, permission.PermDockerDelete, 120 * time.Second, 2},
		{agentproto.DockerActionNetworkRemove, permission.PermDockerDelete, 60 * time.Second, 2},
		{agentproto.DockerActionComposeUp, permission.PermDockerManage, 120 * time.Second, 2},
		{agentproto.DockerActionComposeStop, permission.PermDockerManage, 120 * time.Second, 2},
		{agentproto.DockerActionComposeStart, permission.PermDockerManage, 120 * time.Second, 2},
		{agentproto.DockerActionComposeRestart, permission.PermDockerManage, 120 * time.Second, 2},
		{agentproto.DockerActionComposePull, permission.PermDockerManage, 15 * time.Minute, 2},
		{agentproto.DockerActionComposeDown, permission.PermDockerDelete, 120 * time.Second, 2},
		{agentproto.DockerActionComposeServiceScale, permission.PermDockerManage, 120 * time.Second, 2},
		{agentproto.DockerActionComposeServiceRemoveContainers, permission.PermDockerDelete, 60 * time.Second, 2},
		{agentproto.DockerActionComposeFileRead, permission.PermDockerInspect, 30 * time.Second, 1},
		{agentproto.DockerActionComposeFileWrite, permission.PermDockerConfig, 30 * time.Second, 4},
		{agentproto.DockerActionComposeFileValidate, permission.PermDockerConfig, 30 * time.Second, 4},
		{agentproto.DockerActionComposeFilePatch, permission.PermDockerConfig, 30 * time.Second, 4},
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
		if got.Phase != w.phase {
			t.Errorf("%s 的期次 %d，期望 %d", w.action, got.Phase, w.phase)
		}
		if w.action == agentproto.DockerActionContainerExec {
			if !got.Session {
				t.Error("exec 是会话制指令（无 sweep 超时）")
			}
			continue
		}
		if got.Timeout != w.timeout {
			t.Errorf("%s 的超时 %v，期望 %v", w.action, got.Timeout, w.timeout)
		}
	}
}

// 与协议白名单**双向**一致：缺一条 → 该 action 永远 403（没有策略就取不到权限码）；
// 多一条 → 策略表里有一个协议不认识的幽灵动作。
func TestPolicyCoversProtocolWhiteList(t *testing.T) {
	byAction := map[string]bool{}
	for _, p := range All() {
		byAction[p.Action] = true
	}
	for _, a := range agentproto.AllDockerActions() {
		if !byAction[a] {
			t.Fatalf("协议白名单里的 %s 没有策略行", a)
		}
	}
	for _, p := range All() {
		if !agentproto.IsDockerAction(p.Action) {
			t.Fatalf("策略表里的 %s 不在协议白名单", p.Action)
		}
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

// CurrentPhase 与**已交付期的 action 集合**一致：少放行一条 = 页面缺一个该有的按钮；
// 多放行一条（尚未交付的期）= 页面会出现点不通的按钮（服务端期次闸会 400）。
//
// 四期把期次放到 4：断言随之升级为「全表 29 条都在 live 里」—— 配置编辑
// （compose.file:write/validate/patch）已随 agent 0.5.4 落地，交付边界推到全表尽头，
// 此后新增 action 才需要重新审视这条断言。
func TestCurrentPhaseActions(t *testing.T) {
	if CurrentPhase != 4 {
		t.Fatalf("四期应把 CurrentPhase 定为 4，实际 %d", CurrentPhase)
	}
	live := map[string]bool{}
	for _, p := range All() {
		if p.Phase <= CurrentPhase {
			live[p.Action] = true
		}
	}
	// 一期只读四条 + 二期操作面全量（启停/删除/prune/pull/tag/save/load/compose 操作）
	// + 三期会话制（exec；日志 Follow 复用一期 action，由 options.follow 表达）
	// + 四期配置编辑（validate/write/patch —— 回滚也走 write，由 options.backup 表达）。
	for _, a := range []string{
		agentproto.DockerActionContainerInspect, agentproto.DockerActionContainerLogs,
		agentproto.DockerActionImageInspect, agentproto.DockerActionComposeFileRead,
		agentproto.DockerActionContainerStart, agentproto.DockerActionContainerStop,
		agentproto.DockerActionContainerRestart, agentproto.DockerActionContainerRemove,
		agentproto.DockerActionImageRemove, agentproto.DockerActionImagePrune,
		agentproto.DockerActionImagePull, agentproto.DockerActionImageTag,
		agentproto.DockerActionImageSave, agentproto.DockerActionImageLoad,
		agentproto.DockerActionVolumeRemove, agentproto.DockerActionVolumePrune,
		agentproto.DockerActionNetworkRemove,
		agentproto.DockerActionComposeUp, agentproto.DockerActionComposeStop,
		agentproto.DockerActionComposeStart, agentproto.DockerActionComposeRestart,
		agentproto.DockerActionComposePull, agentproto.DockerActionComposeDown,
		agentproto.DockerActionComposeServiceScale,
		agentproto.DockerActionComposeServiceRemoveContainers,
		agentproto.DockerActionContainerExec,
		agentproto.DockerActionComposeFileValidate,
		agentproto.DockerActionComposeFileWrite,
		agentproto.DockerActionComposeFilePatch,
	} {
		if !live[a] {
			t.Fatalf("已交付期的 action 应放行 %s", a)
		}
	}
	// 全表放行：协议白名单与本表双向一致（TestPolicyCoversProtocolWhiteList）之外的
	// 额外断言 —— live 必须覆盖 All()，没有「登记了却期次未到」的幽灵行。
	if len(live) != len(All()) {
		t.Fatalf("期次 4 之后 live 应覆盖全表 %d 条，实际 %d 条", len(All()), len(live))
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
