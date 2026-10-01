package dockerops

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sync"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 流通道的生产者（日志 follow / 终端 / 聚合日志）──────────────────────────
//
// 三条纪律（照 api.go 的包注释）：
//   - 会话的**建立**在这里，数据的搬运是「读 → 会话缓冲」，帧的发送与限速全在
//     sessions.go 的泵里 —— 生产者不直接碰协议帧，也就没法绕过帧纪律。
//   - 执行器的 Do **立刻返回**（只回 session_id）：会话本身异步跑。派发器是单 worker，
//     阻塞它等于让所有指令排在一条日志流后面。
//   - 不带 follow 的 container:logs 保持**一期的一次性路径**（ReadExecutor），一行不改。
//
// compose:logs（5a）的执行器在 compose_log_stream.go：它是「日志 follow 的会话
// 模型 × compose CLI 的固定 argv」的合体，归入本执行器是因为它同样是会话制 action。

// defaultExecCommand 是 container:exec 未给 command 时的 argv（协议冻结缺省：
// 缺省即 /bin/sh；core 不发 command 时 agent 自己补）。
var defaultExecCommand = []string{"/bin/sh"}

// StreamExecutor 建立流会话（dispatcher 的执行器之一，见 nodeExecutor）。
type StreamExecutor struct {
	api      DockerAPI
	sessions *SessionManager

	mu     sync.Mutex
	lastID string
	// composeFlavor / composeExec 是 compose:logs（5a 聚合日志）的 CLI 依赖，
	// 由 Runtime 经 SetComposeCLI 注入（flavor 走查询函数 —— 见注入函数的说明）；
	// 未注入时 flavor 为空 → compose:logs 回结论句，其余 action 不受影响。
	composeFlavor func() string
	composeExec   func(context.Context, string, ...string) *exec.Cmd
}

// NewStreamExecutor 构造流执行器。
func NewStreamExecutor(api DockerAPI, sessions *SessionManager) *StreamExecutor {
	return &StreamExecutor{api: api, sessions: sessions}
}

// IsStreamAction 报告一条指令是否走流会话。
//
// container:logs 按 options.follow 分流：false/缺省 = 一次性读（一期路径，
// 结果直接带日志正文），true = 流会话。这个分流点是「既有行为不变」的守卫处。
// container:stats **天生是会话制**（实时曲线没有一次性形态），无分流直接走流；
// docker:events 同理（事件订阅只有流这一种形态，且是 core 的常驻订阅）。
// compose:logs（5a）同样天生是会话制：数据源是 CLI 进程的输出，follow=false
// 只是「不传 --follow」（CLI 打完即退出、会话读到 eof 收摊），没有一次性取回
// 的形态 —— 分流点因此不存在，两种形态都走流（见 compose_log_stream.go）。
func IsStreamAction(cmd *agentproto.DockerCmd) bool {
	if cmd == nil {
		return false
	}
	switch cmd.Action {
	case agentproto.DockerActionContainerExec,
		agentproto.DockerActionContainerStats, agentproto.DockerActionEvents,
		agentproto.DockerActionComposeLogs:
		return true
	case agentproto.DockerActionContainerLogs:
		return cmd.Options.Follow
	default:
		return false
	}
}

// Do 建立会话并返回（payload 为空；session_id 由 dispatcher 从 takeSessionID 取）。
func (e *StreamExecutor) Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	var err error
	switch cmd.Action {
	case agentproto.DockerActionContainerExec:
		err = e.startExec(cmd)
	case agentproto.DockerActionContainerLogs:
		err = e.startLogFollow(cmd)
	case agentproto.DockerActionContainerStats:
		err = e.startStats(cmd)
	case agentproto.DockerActionEvents:
		err = e.startEvents(cmd)
	case agentproto.DockerActionComposeLogs:
		err = e.startComposeLogs(ctx, cmd)
	default:
		return nil, &ExecError{Msg: "该操作尚未开放"}
	}
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// takeSessionID 取走刚建立的会话句柄（dispatcher 写进 result.session_id）。
//
// 与写执行器的 takeNote 同一模式：单 worker 串行执行，执行完立刻取走，不会串台。
func (e *StreamExecutor) takeSessionID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.lastID
	e.lastID = ""
	return id
}

func (e *StreamExecutor) remember(id string) {
	e.mu.Lock()
	e.lastID = id
	e.mu.Unlock()
}

// startLogFollow 起一个日志 follow 会话：打开流 → 逐块写进会话 → 流终止发 eof。
func (e *StreamExecutor) startLogFollow(cmd *agentproto.DockerCmd) error {
	tail := cmd.Options.Tail
	if tail <= 0 {
		tail = defaultLogTail
	}
	sess, err := e.sessions.open(streamLog)
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	rc, err := e.api.ContainerLogsFollow(sess.ctx, cmd.Options.Target, tail, cmd.Options.Since)
	if err != nil {
		sess.cancelBy("open failed")
		return &ExecError{Msg: "读取容器日志失败", Detail: err.Error()}
	}
	sess.attachUpstream(rc.Close)
	e.remember(sess.ID())
	go e.copyLog(sess, rc)
	return nil
}

// copyLog 把日志流搬进会话。退出时**只**标 EOF：eof 帧由泵发（生产者直接收摊会
// 把 eof 帧抢先掐掉 —— 消费端就永远等不到流结束的信号）。
func (e *StreamExecutor) copyLog(sess *streamSession, rc io.ReadCloser) {
	buf := make([]byte, logFrameBytes)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			sess.write(buf[:n])
		}
		if err != nil {
			if err != io.EOF && sess.ctx.Err() == nil && !sess.isClosing() {
				sess.mgr.cfg.log.Warn("docker log stream ended with error",
					"session", sess.ID(), "err", err.Error())
			}
			sess.markEOF()
			return
		}
	}
}

// startExec 起一个终端会话：建 TTY exec → 挂接 → PTY 输出进会话。
func (e *StreamExecutor) startExec(cmd *agentproto.DockerCmd) error {
	argv := cmd.Options.Command
	if len(argv) == 0 {
		argv = defaultExecCommand
	}
	sess, err := e.sessions.open(streamPTY)
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	es, err := e.api.ContainerExecAttach(sess.ctx, cmd.Options.Target, argv)
	if err != nil {
		sess.cancelBy("exec attach failed")
		return &ExecError{Msg: "打开终端失败", Detail: err.Error()}
	}
	sess.attachExec(es)
	e.remember(sess.ID())
	go e.copyPTY(sess, es.Reader)
	return nil
}

// copyPTY 把 PTY 输出搬进会话（writePTY 会反压：会话缓冲满时这里停读，
// 容器侧的写随之阻塞 —— 终端不丢字节的代价是慢，而不是错）。
func (e *StreamExecutor) copyPTY(sess *streamSession, r io.Reader) {
	buf := make([]byte, ptyFrameBytes)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if !sess.writePTY(buf[:n]) {
				return // 会话已取消：上游由 teardown 关闭
			}
		}
		if err != nil {
			if err != io.EOF && sess.ctx.Err() == nil && !sess.isClosing() {
				sess.mgr.cfg.log.Warn("docker pty stream ended with error",
					"session", sess.ID(), "err", err.Error())
			}
			sess.markEOF()
			return
		}
	}
}

// startStats 起一个 stats 实时流会话：开流 → 逐样本成帧 → 流终止发 eof。
//
// **首样本不等采样周期**：daemon 的流式 stats 在连接建立后立刻给出当前读数，
// 抽屉打开即有数据（契约第 4 条）。失败即回结论句并收摊，不占会话槽位。
func (e *StreamExecutor) startStats(cmd *agentproto.DockerCmd) error {
	sess, err := e.sessions.open(streamStats)
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	ch, closer, err := e.api.ContainerStatsStream(sess.ctx, cmd.Options.Target)
	if err != nil {
		sess.cancelBy("stats open failed")
		return &ExecError{Msg: "读取容器性能数据失败", Detail: err.Error()}
	}
	sess.attachUpstream(closer.Close)
	e.remember(sess.ID())
	go e.copyStats(sess, ch)
	return nil
}

// copyStats 把样本流搬进会话：每样本编成一条 JSON 行（协议 DockerStatsSample + 换行）
// 写进会话缓冲 —— 帧的整形与限速仍在会话泵里（纪律与 copyLog 相同：生产者不碰帧）。
// 通道关闭（容器停止/被删/取消）即 markEOF。
func (e *StreamExecutor) copyStats(sess *streamSession, ch <-chan StatsSample) {
	for s := range ch {
		b, err := json.Marshal(agentproto.DockerStatsSample{
			T:             sess.mgr.cfg.now().UnixMilli(),
			CPUPercent:    s.CPUPercent,
			MemUsageMB:    s.MemUsageMB,
			MemLimitMB:    s.MemLimitMB,
			NetRXBytesSec: s.NetRXBytesSec,
			NetTXBytesSec: s.NetTXBytesSec,
		})
		if err != nil {
			// 该结构只有数值字段，编不出来的情况实际上不存在；真失败时结束会话
			// 而不是推一帧非法数据（core 的样本校验会把它整帧丢下）。
			sess.mgr.cfg.log.Warn("docker stats sample marshal failed", "session", sess.ID(), "err", err.Error())
			break
		}
		b = append(b, '\n')
		sess.write(b)
	}
	sess.markEOF()
}

// startEvents 起一个事件订阅会话（core 常驻管理器的流，活动流面板的数据源）：
// 打开 daemon 事件流 → 逐事件成帧。生命周期纪律与 stats 相同 —— 取消即停
// （会话 ctx 直通 Events 的订阅 ctx）、打开失败回结论句不占槽位；差别只有两点：
// 豁免空闲超时（平静主机没有事件是常态）、独立槽位（不占用户的 3 条流）。
func (e *StreamExecutor) startEvents(cmd *agentproto.DockerCmd) error {
	sess, err := e.sessions.open(streamEvents)
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	ch, closer, err := e.api.Events(sess.ctx)
	if err != nil {
		sess.cancelBy("events open failed")
		return &ExecError{Msg: "订阅 Docker 事件失败", Detail: err.Error()}
	}
	sess.attachUpstream(closer.Close)
	e.remember(sess.ID())
	go e.copyEvents(sess, ch)
	return nil
}

// copyEvents 把事件流搬进会话：每事件编成一条 JSON 行（协议 DockerEventItem + 换行）
// 写进会话缓冲 —— 帧的整形与限速仍在会话泵里（与 copyStats 同纪律：生产者不碰帧）。
// 通道关闭（取消/daemon 断开）即 markEOF。
//
// 写会话前先过协议校验：类型/动作白名单是两端共同的理解，agent 是**最后一道** ——
// daemon 版本更新冒出白名单外的动作时，在这里就把它拦下并留痕，而不是推一帧
// core 必将拒掉的记录（core 侧的整行丢弃仍然存在，两道闸互不依赖）。
func (e *StreamExecutor) copyEvents(sess *streamSession, ch <-chan EventItem) {
	for ev := range ch {
		item := agentproto.DockerEventItem{
			T:         sess.mgr.cfg.now().UnixMilli(),
			Type:      ev.Type,
			Action:    ev.Action,
			ActorName: ev.ActorName,
			ActorID:   ev.ActorID,
		}
		if err := item.Validate(); err != nil {
			sess.mgr.cfg.log.Warn("docker event dropped (invalid)", "session", sess.ID(), "err", err.Error())
			continue
		}
		b, err := json.Marshal(item)
		if err != nil {
			// 只有四个字符串/数值字段，编不出来的情况实际不存在；真失败时结束会话
			// 而不是推一帧非法数据（与 copyStats 同一取向）。
			sess.mgr.cfg.log.Warn("docker event marshal failed", "session", sess.ID(), "err", err.Error())
			break
		}
		b = append(b, '\n')
		sess.write(b)
	}
	sess.markEOF()
}
