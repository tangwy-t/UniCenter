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

// implementedActions 是本构建**已实现**的 action（一期：四个只读）。
//
// 与协议白名单分开：白名单是「合法 action 全集」（含二/三/四期），这里是
// 「这一版 agent 能做的」。差集里的 action 收到时回「该操作尚未开放」——
// 与「未知操作」是两个不同的结论（前者等升级，后者是 core 写错了）。
var implementedActions = map[string]bool{
	agentproto.DockerActionContainerInspect: true,
	agentproto.DockerActionContainerLogs:    true,
	agentproto.DockerActionImageInspect:     true,
	agentproto.DockerActionComposeFileRead:  true,
}

// Executor 执行一条**已通过校验与确认**的指令，返回结果载荷（nil = 无数据面）。
type Executor interface {
	Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error)
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
}

// NewDispatcher 构造分派器。
func NewDispatcher(exec Executor, send func(*agentproto.DockerCmdResult) error, log Logger) *Dispatcher {
	if log == nil {
		log = nopLogger{}
	}
	return &Dispatcher{jobs: make(chan *agentproto.DockerCmd, dispatchQueueCap), exec: exec, send: send, log: log}
}

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
func (d *Dispatcher) execute(ctx context.Context, cmd *agentproto.DockerCmd) *agentproto.DockerCmdResult {
	res := &agentproto.DockerCmdResult{Ref: cmd.Ref}
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
	ectx, cancel := context.WithTimeout(ctx, dispatchTimeoutCap)
	defer cancel()
	payload, err := d.exec.Do(ectx, cmd)
	if err != nil {
		var ee *ExecError
		if errors.As(err, &ee) {
			res.Error = ee.Msg
			res.Detail = agentproto.NormalizeDockerDetail(ee.Detail)
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
	return res
}
