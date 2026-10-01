package dockerops

import (
	"context"
	"os"
	"os/exec"
	"strconv"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── compose 项目聚合日志（5a）：CLI 输出 → 一条带服务前缀的流 ──────────────
//
// 本文件是两条先例的合体：
//   - 数据面照抄 container:logs follow（stream.go 的 startLogFollow）：会话制、
//     逐块写会话缓冲、eof 收摊、取消即停 —— 帧纪律全在 sessions.go 的泵里；
//   - 执行面照抄 compose 写路径（compose_exec.go）：固定 argv + 零 shell +
//     路径白名单（composeConfigFilesOf 的全量文件）+ flavor 纪律。
//
// 与两条先例的三处刻意差异（全部来自「数据源是 CLI 进程而不是 daemon 流」）：
//   1. **stdout/stderr 合流进同一条管道**：CLI 的错误出口是 stderr（"no such
//      service: xxx"），只接 stdout 的话失败形态是「一条空流 + 莫名 eof」——
//      而人在终端里跑 compose logs 本来就是两路合着看的，合流是忠实而不是妥协；
//   2. **取消 = 杀进程**：会话 ctx 直通 exec.CommandContext，cancel 触发 SIGKILL
//     （与 logs 先例「cancel 关 daemon 连接」同一语义，落点不同）；
//   3. **非 follow 也是会话**：CLI 不传 --follow 时打完历史日志即退出，读循环
//      到 EOF 自然收摊 —— 协议上没有一次性取回的形态（见 DockerActionComposeLogs）。

// composeLogsArgv 折出 compose logs 的固定 argv（**不含**二进制名；plugin 形态的
// 首元素是 "compose"，与 compose_exec.go 的 composeArgv 同一拼法 —— 两种形态都
// 逐字钉在测试里，混起来会执行成 `docker-compose compose logs`）。
//
// flag 取值是三 flavor 公共子集（§6.1 纪律，已逐版本查证）：
//   - v1（1.29.2 源码 compose/cli/main.py）：--no-color / --follow / --timestamps /
//     --tail="all"|N —— **没有** --since/--until（v2 才有，故协议层拒绝 since）；
//   - v2（plugin 与独立二进制同源）：-f/--follow、-n/--tail、-t/--timestamps、
//     --since、--until —— 长形态两边都认，只用长形态。
//
// 为什么固定带 --timestamps：聚合流的阅读问题恰是「多服务的行谁先谁后」，时间戳
// 是行内自带的公共时间轴；不带它，跨服务交错顺序只能靠猜。tail 的语义与
// container:logs 对齐（0/缺省 = 100 行），CLI 缺省是 "all"（全量）—— 多服务项目
// 的全量日志轻易数十万行，喂进 64KB 帧上限的流只会是丢弃风暴。
//
// argv 里的一切可变片段（项目名/配置文件路径/tail 数字）都先过白名单或
// strconv —— 纵深防御与 compose_exec.go 同一纪律（协议层已校验）。
func composeLogsArgv(flavor, project string, configFiles []string, follow bool, tail int) []string {
	if tail <= 0 {
		tail = defaultLogTail
	}
	argv := make([]string, 0, 8+2*len(configFiles))
	if flavor == agentproto.DockerComposeFlavorPlugin {
		argv = append(argv, "compose")
	}
	argv = append(argv, "-p", project)
	for _, f := range configFiles {
		argv = append(argv, "-f", f)
	}
	argv = append(argv, "logs", "--timestamps", "--tail", strconv.Itoa(tail))
	if follow {
		argv = append(argv, "--follow")
	}
	return argv
}

// startComposeLogs 起一个聚合日志会话：解析配置文件 → 固定 argv → 起 CLI 进程 →
// 逐行搬进会话。执行器在 Do 里做完「建立」立刻返回（session_id 由 dispatcher 取走），
// 数据搬运在 go copyComposeLogs 里异步跑 —— 单 worker 派发器不能被一条日志流堵住
// （stream.go 的三条纪律之一）。
func (e *StreamExecutor) startComposeLogs(ctx context.Context, cmd *agentproto.DockerCmd) error {
	o := &cmd.Options
	// 目标名在拼 argv 之前再过一遍项目名白名单（纵深防御，与 composeNames 同一取向）：
	// 协议层已校验，这里保证「进入 argv 的每个可变片段都过白名单」在一处可见。
	project := o.Target
	if !agentproto.IsDockerProjectName(project) {
		return &ExecError{Msg: "项目名不合法"}
	}
	flavor := e.composeFlavorValue()
	bin, err := composeBinary(flavor)
	if err != nil {
		return err
	}
	// 配置文件路径只能来自本机事实（容器标签/项目索引），协议上从不接受路径入参
	// —— 路径白名单纪律（§8），与 compose 写路径共用同一份解析。
	configFiles, err := composeConfigFilesOf(ctx, e.api, project)
	if err != nil {
		return err
	}
	argv := composeLogsArgv(flavor, project, configFiles, o.Follow, o.Tail)

	sess, err := e.sessions.open(streamLog)
	if err != nil {
		return &ExecError{Msg: err.Error()}
	}
	// stdout/stderr 同接一条 os.Pipe：子进程直接持有写端 fd（exec 对 *os.File
	// 不引入用户态拷贝协程），两路在内核里按行原子交错 —— 终端里跑 compose logs
	// 看到的就是这个形态，失败文案（stderr）因此能到达用户而不是变成一条空流。
	pr, pw, err := os.Pipe()
	if err != nil {
		sess.cancelBy("pipe failed")
		return &ExecError{Msg: "打开项目日志流失败", Detail: err.Error()}
	}
	proc := e.composeExecValue()(sess.ctx, bin, argv...)
	proc.Stdout = pw
	proc.Stderr = pw
	if err := proc.Start(); err != nil {
		sess.cancelBy("compose logs start failed")
		pr.Close()
		pw.Close()
		return &ExecError{Msg: "打开项目日志流失败", Detail: err.Error()}
	}
	// 父进程侧的写端必须关掉：不关的话 CLI 退出后读循环等不到 EOF，会话永远收不了摊。
	pw.Close()
	e.remember(sess.ID())
	go e.copyComposeLogs(sess, pr, proc)
	return nil
}

// copyComposeLogs 把 CLI 输出搬进会话：逐块读 → sess.write（合帧/限速在泵里，
// 生产者不碰帧 —— copyLog 的同一纪律）；读尽 → 收进程尸（Wait）→ markEOF。
//
// Wait 的错误只进日志不进结果：CLI 的失败文案已经从 stderr 流给了用户（合流的
// 目的正在于此）；此处留痕是给 agent 侧排障的（"compose logs exited: signal killed"）。
// 退出纪律与 copyLog 相同：**只**标 EOF，eof 帧由泵发。
func (e *StreamExecutor) copyComposeLogs(sess *streamSession, pr *os.File, proc *exec.Cmd) {
	defer pr.Close()
	buf := make([]byte, logFrameBytes)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			sess.write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if werr := proc.Wait(); werr != nil {
		// 取消（用户关掉抽屉 → cancel → SIGKILL）是正常收尾，不刷 Warn；
		// 其余退出错误留一条日志，stderr 原文用户已经在流里看到了。
		if sess.ctx.Err() == nil && !sess.isClosing() {
			sess.mgr.cfg.log.Warn("compose logs process exited with error",
				"session", sess.ID(), "err", werr.Error())
		}
	}
	sess.markEOF()
}

// ── 执行器的 compose 依赖注入（与 WriteExecutor 的 exec 注入同一模式）────────

// SetComposeCLI 注入 compose:logs 的两样依赖：flavor 查询函数与 CLI 执行入口。
//
// flavor 走**函数**而不是存值（与 WriteExecutor.SetFlavor 的差别是有意的）：
// 探测可能在执行器构造之后才发生（probeComposeOnce 在首个快照循环里跑），
// 函数注入让 Runtime 的 flavor 成为唯一事实源 —— 探测到新 flavor 不需要
// 「记得通知第二个执行器」，漏通知的暗角从结构上不存在。execFn 供测试换替身
// （compose_exec.go 的 composeExecStub 同一模式）；nil 时用 exec.CommandContext。
func (e *StreamExecutor) SetComposeCLI(flavor func() string, execFn func(context.Context, string, ...string) *exec.Cmd) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.composeFlavor = flavor
	if execFn == nil {
		execFn = exec.CommandContext
	}
	e.composeExec = execFn
}

// composeFlavorValue 返回当前 compose 形态（空 = 尚未探测到 —— composeBinary
// 会把空值折成结论句「没有检测到可用的 compose」，不会去猜一个形态执行）。
func (e *StreamExecutor) composeFlavorValue() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.composeFlavor == nil {
		return ""
	}
	return e.composeFlavor()
}

// composeExecValue 返回 CLI 执行入口（未注入时退回 exec.CommandContext ——
// 与 WriteExecutor 构造函数的缺省同一取向）。
func (e *StreamExecutor) composeExecValue() func(context.Context, string, ...string) *exec.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.composeExec == nil {
		return exec.CommandContext
	}
	return e.composeExec
}
