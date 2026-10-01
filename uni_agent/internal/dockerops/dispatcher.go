package dockerops

import (
	"context"
	"errors"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// dispatchQueueCap 是待执行队列深度。
//
// 取值 8 是「够用且不会让用户等太久」：单 worker 串行，队列里 8 条轻量指令约需
// 数秒到数十秒；再多就该直接告诉用户「忙」而不是排一条看不到尽头的队。
const dispatchQueueCap = 8

// dispatchTimeoutCap 是执行时限的兜底（core 的策略表在 agent 侧没有副本 ——
// agent 用自己的保守上限，比 core 的 sweep 略短，好让「agent 自己先超时」优先发生）。
const dispatchTimeoutCap = 5 * time.Minute

// implementedActions 是本构建**已实现**的 action（一期只读 + 二期写操作 + 三期流会话
// + 四期配置编辑 + 五期监控面与聚合日志）。
//
// 与协议白名单分开：白名单是「合法 action 全集」（含二/三/四期），这里是
// 「这一版 agent 能做的」。差集里的 action 收到时回「该操作尚未开放」——
// 与「未知操作」是两个不同的结论（前者等升级，后者是 core 写错了）。
var implementedActions = map[string]bool{
	// 一期：四个只读
	agentproto.DockerActionContainerInspect: true,
	agentproto.DockerActionContainerLogs:    true,
	agentproto.DockerActionImageInspect:     true,
	agentproto.DockerActionComposeFileRead:  true,

	// 二期 B1：容器 / 镜像 / 卷 / 网络的写操作
	agentproto.DockerActionContainerCreate:  true, // 四支柱·创建面（4a）
	agentproto.DockerActionContainerStart:   true,
	agentproto.DockerActionContainerStop:    true,
	agentproto.DockerActionContainerRestart: true,
	agentproto.DockerActionContainerRemove:  true,
	agentproto.DockerActionImageRemove:      true,
	agentproto.DockerActionImagePrune:       true,
	agentproto.DockerActionImagePull:        true,
	agentproto.DockerActionImageTag:         true,
	agentproto.DockerActionImageSave:        true,
	agentproto.DockerActionImageLoad:        true,
	agentproto.DockerActionVolumeRemove:     true,
	agentproto.DockerActionVolumePrune:      true,
	agentproto.DockerActionNetworkRemove:    true,

	// 二期 B2：compose 项目与网元操作（固定 argv，见 compose_exec.go）
	agentproto.DockerActionComposeUp:                      true,
	agentproto.DockerActionComposeStop:                    true,
	agentproto.DockerActionComposeStart:                   true,
	agentproto.DockerActionComposeRestart:                 true,
	agentproto.DockerActionComposePull:                    true,
	agentproto.DockerActionComposeDown:                    true,
	agentproto.DockerActionComposeServiceScale:            true,
	agentproto.DockerActionComposeServiceRemoveContainers: true,

	// 三期：流会话（日志 follow / 终端）。两者都是**会话制**：执行器立刻回
	// session_id，数据与控制在会话里异步跑（帧纪律见 sessions.go）。
	// 注意 container:logs 的一次性路径（follow=false）仍走一期只读执行器，行为不变。
	agentproto.DockerActionContainerExec: true,

	// 监控面：stats 实时流。会话制同 exec（执行器立刻回 session_id，样本在会话里
	// 持续上行）；数据帧里装的是一条条 JSON 样本行（协议 DockerStatsSample）。
	agentproto.DockerActionContainerStats: true,

	// 六期·监控面：事件流（docker events 订阅）。会话制同 stats —— 执行器立刻回
	// session_id，事件在会话里持续上行（帧里一条 JSON 记录一行，协议 DockerEventItem）。
	// 它是 core 的常驻订阅，host 级（无 target），由 core 的常驻管理器建立/取消。
	agentproto.DockerActionEvents: true,

	// 五期 5a：compose 项目聚合日志（compose_log_stream.go）。会话制（follow 与
	// 非 follow 都是流 —— CLI 输出没有一次性取回的形态），权限与 container:logs
	// 同档（docker:inspect）。
	agentproto.DockerActionComposeLogs: true,

	// 四期：配置编辑三条路径（validate/write/patch）。共用收尾链见
	// compose_file_write.go：乐观锁 → 预检 → 备份 → 原子写 → 回读。
	agentproto.DockerActionComposeFileValidate: true,
	agentproto.DockerActionComposeFileWrite:    true,
	agentproto.DockerActionComposeFilePatch:    true,
}

// writeTimeouts 是操作面 action 的执行时限**镜像**（权威表在 core 的 dockerpolicy，
// 值必须一致；逐行对齐的守卫见 write_test.go 的 TestWriteTimeoutsMirrorSpec）。
//
// 为什么 agent 也要有一份：dispatcher 的兜底上限是 5 分钟，而 image:pull/save/load
// （以及 compose:pull）的总表超时是 15 分钟 —— 不覆盖的话它们会被提前判死，
// 表现为「用户在页面上看到超时，而 daemon 其实还在拉」。
//
// 表按 spec §4.3.1 的二期行一次补齐；四期三条（配置编辑，30s）随之加入：
// 不覆盖不会判死（30s < 5 分钟兜底），但两端的超时口径会不一致（core 先 sweep 成
// timeout 而 agent 还在跑），镜像的意义就是消除这种「同一个词两个答案」。
var writeTimeouts = map[string]time.Duration{
	agentproto.DockerActionContainerCreate:  30 * time.Second, // 创建面（4a）：本地一次 create +（按需）一次 start
	agentproto.DockerActionContainerStart:   30 * time.Second,
	agentproto.DockerActionContainerStop:    30 * time.Second,
	agentproto.DockerActionContainerRestart: 60 * time.Second,
	agentproto.DockerActionContainerRemove:  60 * time.Second,

	agentproto.DockerActionImageRemove: 60 * time.Second,
	agentproto.DockerActionImagePrune:  120 * time.Second,
	agentproto.DockerActionImagePull:   15 * time.Minute,
	agentproto.DockerActionImageTag:    30 * time.Second,
	agentproto.DockerActionImageSave:   15 * time.Minute,
	agentproto.DockerActionImageLoad:   15 * time.Minute,

	agentproto.DockerActionVolumeRemove: 60 * time.Second,
	agentproto.DockerActionVolumePrune:  120 * time.Second,

	agentproto.DockerActionNetworkRemove: 60 * time.Second,

	agentproto.DockerActionComposeUp:      120 * time.Second,
	agentproto.DockerActionComposeStop:    120 * time.Second,
	agentproto.DockerActionComposeStart:   120 * time.Second,
	agentproto.DockerActionComposeRestart: 120 * time.Second,
	agentproto.DockerActionComposePull:    15 * time.Minute,
	agentproto.DockerActionComposeDown:    120 * time.Second,

	agentproto.DockerActionComposeServiceScale:            120 * time.Second,
	agentproto.DockerActionComposeServiceRemoveContainers: 60 * time.Second,

	// 四期：配置编辑（写入是本地文件操作 + 一次 config -q 预检，30s 足够）。
	agentproto.DockerActionComposeFileValidate: 30 * time.Second,
	agentproto.DockerActionComposeFileWrite:    30 * time.Second,
	agentproto.DockerActionComposeFilePatch:    30 * time.Second,
}

// actionTimeout 取一条指令的执行时限：二期表优先，其余用兜底。
func actionTimeout(action string) time.Duration {
	if t, ok := writeTimeouts[action]; ok {
		return t
	}
	return dispatchTimeoutCap
}

// Executor 执行一条**已通过校验与确认**的指令，返回结果载荷（nil = 无数据面）。
type Executor interface {
	Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error)
}

// successNoter 是执行器**可选**的成功附注出口。
//
// 为什么需要它：save/load 的产物目录回退（spec §7.5）与 prune 的释放量（§7.6）
// 是「成功但必须留痕」的信息，而 result 里唯一的自由文本槽是 detail（payload 是
// 结构化数据面）。dispatcher 单 worker 串行执行，「执行完取一次附注」不会与下一条串台。
type successNoter interface {
	takeNote() string
}

// sessionNamer 是执行器**可选**的会话句柄出口（与 successNoter 同一模式）。
//
// 为什么走可选接口而不是改 Executor 的签名：只有流会话的执行器会产生 session_id，
// 而 Executor 是读/写/流三个执行器共用的窄接口 —— 为一条路径给全体加返回值，
// 会让另外两个执行器各自写一遍「我返回空串」的噪音。
type sessionNamer interface {
	takeSessionID() string
}

// Dispatcher 是**单 worker** 的指令分派器（§10「每设备指令串行执行」）。
//
// 为什么串行：compose up/pull/save 天然互相冲突（同一项目的两次 up 会互踩），
// 而并行的收益（用户同时点两个按钮）在这个界面里不值一提；串行还让「同一目标上
// 两条指令」的竞态在架构上不存在 —— 服务端的 409 只是第二道锁。
type Dispatcher struct {
	jobs chan *agentproto.DockerCmd
	exec Executor
	send func(*agentproto.DockerCmdResult) error
	log  Logger
	// onSettled 是执行落定回调（可选）。Runtime 用它实现「写操作成功后立即采帧」；
	// 单 worker 内同步调用，故实现必须**非阻塞**（照 OnConnected 的纪律）。
	onSettled func(action string, ok bool)
}

// NewDispatcher 构造分派器。
func NewDispatcher(exec Executor, send func(*agentproto.DockerCmdResult) error, log Logger) *Dispatcher {
	if log == nil {
		log = nopLogger{}
	}
	return &Dispatcher{jobs: make(chan *agentproto.DockerCmd, dispatchQueueCap), exec: exec, send: send, log: log}
}

// SetOnSettled 注入执行落定回调：每条指令**恰好调用一次**（含被拒的路径），
// 如实报告 action 与成败。回调在 worker goroutine 里同步执行，必须非阻塞。
func (d *Dispatcher) SetOnSettled(fn func(action string, ok bool)) { d.onSettled = fn }

// Handle 受理一条指令，报告是否入队。**非阻塞**：它在连接读循环里被调用。
//
// 队列满时返回 false（由调用方立刻回一句「指令排队已满，请稍后重试」），
// 而不是阻塞读循环或无限排队 —— 对用户而言「等 30 秒才开始执行」比「现在告诉我忙」更糟。
func (d *Dispatcher) Handle(ctx context.Context, cmd *agentproto.DockerCmd) bool {
	select {
	case d.jobs <- cmd:
		return true
	default:
		return false
	}
}

// Run 是 worker 主循环（由 Runtime 在自己的 goroutine 里跑）。
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-d.jobs:
			res := d.execute(ctx, cmd)
			if err := d.send(res); err != nil {
				d.log.Warn("docker result send failed", "ref", res.Ref, "err", err.Error())
			}
		}
	}
}

// ExecuteNow 同步执行队列里的**指定 ref**（仅测试用：让表驱动用例不必起 worker 循环）。
//
// 它按受理顺序依次执行到命中 ref 为止 —— 与 Run 的执行顺序一致，故表驱动的断言
// 与生产路径是同一套串行语义。
func (d *Dispatcher) ExecuteNow(ctx context.Context, ref string) {
	for {
		select {
		case cmd := <-d.jobs:
			res := d.execute(ctx, cmd)
			_ = d.send(res)
			if cmd.Ref == ref {
				return
			}
		default:
			return
		}
	}
}

// execute 执行一条指令并把它折成 result。
//
// 拒绝顺序刻意如此：未登记 action → 未实现 action → 参数 → 确认。反过来的话，
// 「未知 action」会被参数校验先拦掉并报成「缺 target」，而那正是排障最容易走错的一步。
//
// 结果用命名返回值 + defer 收口：落定回调要对**所有**返回路径恰好触发一次
// （含提前拒绝），把回调散在各 return 前迟早会漏掉某一条新加的路径。
func (d *Dispatcher) execute(ctx context.Context, cmd *agentproto.DockerCmd) (res *agentproto.DockerCmdResult) {
	res = &agentproto.DockerCmdResult{Ref: cmd.Ref}
	if d.onSettled != nil {
		defer func() { d.onSettled(cmd.Action, res.OK) }()
	}
	if !agentproto.IsDockerAction(cmd.Action) {
		res.Error = "未知操作"
		return res
	}
	if !implementedActions[cmd.Action] {
		res.Error = "该操作尚未开放"
		return res
	}
	if err := agentproto.ValidateDockerCmdOptions(cmd.Action, &cmd.Options); err != nil {
		res.Error = "指令参数不合法"
		res.Detail = agentproto.NormalizeDockerDetail(err.Error())
		return res
	}
	// 确认档的**二次校验**：core 已经校验过，但 core 可被绕过/伪造，agent 是最后一道。
	if err := agentproto.CheckDockerConfirm(cmd.Action, &cmd.Options, cmd.Confirm); err != nil {
		res.Error = "缺少确认信息"
		return res
	}
	ectx, cancel := context.WithTimeout(ctx, actionTimeout(cmd.Action))
	defer cancel()
	payload, err := d.exec.Do(ectx, cmd)
	// 附注**无论成败都要取走**：失败路径留下的附注（如 save 先回退目录、随后又失败）
	// 若留在执行器里，会串到**下一条**指令的结果上（读 action 不清附注，
	// 串台只会在读结果上现形）。
	note := ""
	if n, ok := d.exec.(successNoter); ok {
		note = n.takeNote()
	}
	if err != nil {
		var ee *ExecError
		if errors.As(err, &ee) {
			res.Error = ee.Msg
			res.Detail = agentproto.NormalizeDockerDetail(ee.Detail)
			// already_exists 是「失败但要让前端问一句」的特殊结论（image:save 两段确认），
			// 必须透到 result 才能走完第二段。
			res.AlreadyExists = ee.AlreadyExists
		} else {
			// 非 ExecError 的失败：结论句用通用措辞，原文进 detail ——
			// 直接把内部错误串当结论句显示给用户是另一类问题（页面文案纪律）。
			res.Error = "执行失败"
			res.Detail = agentproto.NormalizeDockerDetail(err.Error())
		}
		return res
	}
	res.OK = true
	res.Payload = payload
	// 会话句柄（仅流会话的 action 有）：core 把它写进 cmds/:ref 的结果，前端据此接流。
	if n, ok := d.exec.(sessionNamer); ok {
		res.SessionID = n.takeSessionID()
	}
	// 成功路径的附注（目录回退 / prune 释放量）：只在有话说时写 detail。
	if note != "" {
		res.Detail = agentproto.NormalizeDockerDetail(note)
	}
	return res
}
