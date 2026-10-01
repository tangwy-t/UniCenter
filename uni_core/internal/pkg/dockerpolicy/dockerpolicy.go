// Package dockerpolicy 是 Docker 指令的**策略唯一事实源**：每个 action 需要哪个权限码、
// 服务端 sweep 的超时、以及它是否是会话制指令。
//
// 为什么把这三点放在一起而不是散在 handler/service：它们是同一张表的列（spec §4.3.1），
// 而这张表的错误形态是**越权**（把 docker:delete 写成 docker:manage）而不是崩溃 ——
// 拆开写就看不见「同一行的各列是否自洽」。协议不装这张表：协议只管两端对一条消息的
// 共同理解，权限与超时是 core 的策略。
//
// 历史注：这张表曾有一列「交付期次」（Phase）与 CurrentPhase 常量组成的发布节奏
// 脚手架 —— 7c 已删：CurrentPhase=5 且全表 action 的期次都 ≤5，那道闸**永不触发**，
// 纯属死代码；权限/会话制/时限三列是活语义，保留。
package dockerpolicy

import (
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

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
}

// policies 是策略表的唯一枚举源（顺序 = spec §4.3.1 书写顺序）。
var policies = []Policy{
	// 创建面（四支柱之一，4a）：权限与启停同级（docker:manage —— 创建不删不停
	// 任何现存目标）；30 秒本地操作档（一次 create + 一次
	// start，不自动拉镜像 —— 拉取走独立的 image:pull 15 分钟档）。
	{agentproto.DockerActionContainerCreate, permission.PermDockerManage, 30 * time.Second, false},
	{agentproto.DockerActionContainerStart, permission.PermDockerManage, 30 * time.Second, false},
	{agentproto.DockerActionContainerStop, permission.PermDockerManage, 30 * time.Second, false},
	{agentproto.DockerActionContainerRestart, permission.PermDockerManage, 60 * time.Second, false},
	{agentproto.DockerActionContainerRemove, permission.PermDockerDelete, 60 * time.Second, false},
	{agentproto.DockerActionContainerLogs, permission.PermDockerInspect, 30 * time.Second, false},
	{agentproto.DockerActionContainerInspect, permission.PermDockerInspect, 30 * time.Second, false},
	// exec 是会话制：它的「结果」是会话句柄，超时由流会话自己管。
	{agentproto.DockerActionContainerExec, permission.PermDockerExec, 0, true},
	// stats 实时流（监控面）：权限与 logs 同档（inspect —— 只看不碰），
	// 会话制同 exec（「结果」是会话句柄，生命周期交给流通道）。
	{agentproto.DockerActionContainerStats, permission.PermDockerInspect, 0, true},
	{agentproto.DockerActionImageRemove, permission.PermDockerDelete, 60 * time.Second, false},
	{agentproto.DockerActionImagePrune, permission.PermDockerDelete, 120 * time.Second, false},
	{agentproto.DockerActionImagePull, permission.PermDockerManage, 15 * time.Minute, false},
	{agentproto.DockerActionImageTag, permission.PermDockerManage, 30 * time.Second, false},
	{agentproto.DockerActionImageInspect, permission.PermDockerInspect, 30 * time.Second, false},
	{agentproto.DockerActionImageSave, permission.PermDockerManage, 15 * time.Minute, false},
	{agentproto.DockerActionImageLoad, permission.PermDockerManage, 15 * time.Minute, false},
	// P2·分发闭环。build/push 归 **docker:manage 档**（与 pull/save/load 同一分发
	// 家族）：构建的产物是一枚镜像（权限威胁等同于拉取 —— 「代码进得来」由
	// docker:manage 持有者负责，任何镜像被 run 时都会执行其内容）；构建容器在
	// buildkit 沙箱内跑、不接触宿主运行时资源，「在宿主上执行任意命令」的
	// docker:exec 档不适用。确认档标准（协议 dockerActionSpecs）。
	// 超时：构建 30 分钟（冷缓存下的大工程可以合法地长于 pull/save 的 15 分钟档；
	// agent 的 writeTimeouts 同值，逐行对齐守卫在两端各自钉住）；推送 15 分钟
	//（与拉取同为网络长传输）。
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

	// compose:logs（5a 聚合日志）：权限与 container:logs 同档（inspect —— 只读流，
	// 不碰任何目标；看日志与看快照同属「只看」）。会话制同 stats —— 它的「结果」
	// 是会话句柄（follow 与非 follow 都是流：CLI 输出没有一次性取回的形态，非
	// follow 只是读完即 eof），生命周期交给流通道；受理记录用建立窗口
	//（SessionSetupTimeout），不参与普通写指令的 15 分钟档 sweep。
	{agentproto.DockerActionComposeLogs, permission.PermDockerInspect, 0, true},
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
