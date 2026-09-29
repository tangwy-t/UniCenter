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
//
// 当前 = 4（四期配置编辑）：compose.file:write/validate/patch 已随 agent 0.5.4 落地，
// 全表 29 条 action 至此全部放行 —— 不存在「已登记但未交付」的段了。
const CurrentPhase = 4

// SessionSetupTimeout 是**会话制** action 的受理记录终结时限。
//
// 会话制指令的「结果」是会话句柄，它必须在建立阶段到达（agent 起会话、回
// result{session_id}）；建立之后记录已终结、生命周期交给流通道（10 分钟空闲
// 判定在流会话侧）。给 30 秒与 container:logs 同档：建立会话是本地操作，不需要
// 15 分钟级的窗口。
//
// 为什么不能用 0（Policy.Timeout 的「不适用」值）去 Create：sweep 的到期索引
// 会把「deadline = 现在」的 pending 记录在下一轮（≤5s）就终结成 timeout ——
// 那会在 result 到达前把一条**正在建立**的会话指令判成超时（result 迟到时
// 还会来回覆盖，用户看到的状态反复横跳）。
const SessionSetupTimeout = 30 * time.Second

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

// AcceptTimeout 返回受理记录的 sweep 终结时限（会话制用建立窗口，见 SessionSetupTimeout）。
func (p Policy) AcceptTimeout() time.Duration {
	if p.Session {
		return SessionSetupTimeout
	}
	return p.Timeout
}
