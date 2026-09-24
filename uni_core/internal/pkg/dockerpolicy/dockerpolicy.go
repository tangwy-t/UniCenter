// Package dockerpolicy 是 Docker 指令的**策略唯一事实源**：每个 action 需要哪个权限码、
// 服务端 sweep 的超时、以及它属于哪一期。
//
// 为什么把这三点放在一起而不是散在 handler/service：它们是同一张表的三列（spec §4.3.1），
// 而这张表的错误形态是**越权**（把 docker:delete 写成 docker:manage）而不是崩溃 ——
// 拆开写就看不见「同一行的三列是否自洽」。协议不装这张表：协议只管两端对一条消息的
// 共同理解，权限与超时是 core 的策略。
package dockerpolicy

import (
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// CurrentPhase 是**本构建已交付**的期次。
//
// 它是一道硬闸：`Phase > CurrentPhase` 的 action 在受理处直接 400「该操作尚未开放」，
// 而不是把它发给 agent 等一句「尚未开放」的结果 —— 后者会让页面出现「点了没反应」
// 的按钮（§11.0 分期控件矩阵：不渲染 ≠ 禁用，服务端这道闸是它的兜底）。
// 每期交付时改这一个常量。
const CurrentPhase = 1

// Policy 是一个 action 的策略行。
type Policy struct {
	Action string
	// Perm 是受理该指令所需的权限码。
	Perm string
	// Timeout 是 sweep 的终结时限（Session=true 时不适用）。
	Timeout time.Duration
	// Session 表示会话制指令（exec）：它的生命周期由流会话决定，不参与 sweep。
	Session bool
	// Phase 是该 action 的交付期次（1=查询面 2=操作面 3=交互面 4=配置编辑）。
	Phase int
}

// policies 是策略表的唯一枚举源（顺序 = spec §4.3.1 书写顺序）。
var policies = []Policy{
	{agentproto.DockerActionContainerStart, permission.PermDockerManage, 30 * time.Second, false, 2},
	{agentproto.DockerActionContainerStop, permission.PermDockerManage, 30 * time.Second, false, 2},
	{agentproto.DockerActionContainerRestart, permission.PermDockerManage, 60 * time.Second, false, 2},
	{agentproto.DockerActionContainerRemove, permission.PermDockerDelete, 60 * time.Second, false, 2},
	{agentproto.DockerActionContainerLogs, permission.PermDockerInspect, 30 * time.Second, false, 1},
	{agentproto.DockerActionContainerInspect, permission.PermDockerInspect, 30 * time.Second, false, 1},
	// exec 是会话制：它的「结果」是会话句柄，超时由流会话自己管（三期）。
	{agentproto.DockerActionContainerExec, permission.PermDockerExec, 0, true, 3},
	{agentproto.DockerActionImageRemove, permission.PermDockerDelete, 60 * time.Second, false, 2},
	{agentproto.DockerActionImagePrune, permission.PermDockerDelete, 120 * time.Second, false, 2},
	{agentproto.DockerActionImagePull, permission.PermDockerManage, 15 * time.Minute, false, 2},
	{agentproto.DockerActionImageTag, permission.PermDockerManage, 30 * time.Second, false, 2},
	{agentproto.DockerActionImageInspect, permission.PermDockerInspect, 30 * time.Second, false, 1},
	{agentproto.DockerActionImageSave, permission.PermDockerManage, 15 * time.Minute, false, 2},
	{agentproto.DockerActionImageLoad, permission.PermDockerManage, 15 * time.Minute, false, 2},
	{agentproto.DockerActionVolumeRemove, permission.PermDockerDelete, 60 * time.Second, false, 2},
	{agentproto.DockerActionVolumePrune, permission.PermDockerDelete, 120 * time.Second, false, 2},
	{agentproto.DockerActionNetworkRemove, permission.PermDockerDelete, 60 * time.Second, false, 2},
	{agentproto.DockerActionComposeUp, permission.PermDockerManage, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposeStop, permission.PermDockerManage, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposeStart, permission.PermDockerManage, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposeRestart, permission.PermDockerManage, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposePull, permission.PermDockerManage, 15 * time.Minute, false, 2},
	{agentproto.DockerActionComposeDown, permission.PermDockerDelete, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposeServiceScale, permission.PermDockerManage, 120 * time.Second, false, 2},
	{agentproto.DockerActionComposeServiceRemoveContainers, permission.PermDockerDelete, 60 * time.Second, false, 2},
	{agentproto.DockerActionComposeFileRead, permission.PermDockerInspect, 30 * time.Second, false, 1},
	{agentproto.DockerActionComposeFileWrite, permission.PermDockerConfig, 30 * time.Second, false, 4},
	{agentproto.DockerActionComposeFileValidate, permission.PermDockerConfig, 30 * time.Second, false, 4},
	{agentproto.DockerActionComposeFilePatch, permission.PermDockerConfig, 30 * time.Second, false, 4},
}

// All 返回策略表的浅拷贝（顺序 = 书写顺序）。
func All() []Policy {
	out := make([]Policy, len(policies))
	copy(out, policies)
	return out
}

// Lookup 按 action 查找策略。
func Lookup(action string) (Policy, bool) {
	for _, p := range policies {
		if p.Action == action {
			return p, true
		}
	}
	return Policy{}, false
}

// RequiredPerm 返回该 action 需要的权限码。
func RequiredPerm(action string) (string, bool) {
	p, ok := Lookup(action)
	return p.Perm, ok
}
