package dockerops

import (
	"context"
	"io"
	"sync"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 流通道的生产者（日志 follow / 终端）────────────────────────────────────
//
// 三条纪律（照 api.go 的包注释）：
//   - 会话的**建立**在这里，数据的搬运是「读 → 会话缓冲」，帧的发送与限速全在
//     sessions.go 的泵里 —— 生产者不直接碰协议帧，也就没法绕过帧纪律。
//   - 执行器的 Do **立刻返回**（只回 session_id）：会话本身异步跑。派发器是单 worker，
//     阻塞它等于让所有指令排在一条日志流后面。
//   - 不带 follow 的 container:logs 保持**一期的一次性路径**（ReadExecutor），一行不改。

// defaultExecCommand 是 container:exec 未给 command 时的 argv（协议冻结缺省：
// 缺省即 /bin/sh；core 不发 command 时 agent 自己补）。
var defaultExecCommand = []string{"/bin/sh"}

// StreamExecutor 建立流会话（dispatcher 的执行器之一，见 nodeExecutor）。
type StreamExecutor struct {
	api      DockerAPI
	sessions *SessionManager

	mu     sync.Mutex
	lastID string
}

// NewStreamExecutor 构造流执行器。
func NewStreamExecutor(api DockerAPI, sessions *SessionManager) *StreamExecutor {
	return &StreamExecutor{api: api, sessions: sessions}
}

// IsStreamAction 报告一条指令是否走流会话。
//
// container:logs 按 options.follow 分流：false/缺省 = 一次性读（一期路径，
// 结果直接带日志正文），true = 流会话。这个分流点是「既有行为不变」的守卫处。
func IsStreamAction(cmd *agentproto.DockerCmd) bool {
	if cmd == nil {
		return false
	}
	switch cmd.Action {
	case agentproto.DockerActionContainerExec:
		return true
	case agentproto.DockerActionContainerLogs:
		return cmd.Options.Follow
	default:
		return false
	}
}

// Do 建立会话并返回（payload 为空；session_id 由 dispatcher 从 takeSessionID 取）。
func (e *StreamExecutor) Do(_ context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	var err error
	switch cmd.Action {
	case agentproto.DockerActionContainerExec:
		err = e.startExec(cmd)
	case agentproto.DockerActionContainerLogs:
		err = e.startLogFollow(cmd)
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
