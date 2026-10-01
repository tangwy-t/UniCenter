package dockerops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// defaultTransferDir 是产物目录的内置默认值。
//
// **来源**：spec §4.3.1 的 v015 种子 `sys.docker.transferDir=/var/lib/uni_agent/transfer`
// （§7.5 定案：不用 /tmp，因为它是世界可写的符号链接攻击温床，而 agent 以 root 运行）。
// 为什么执行器里还要带一份：hello_ack 只在重连时下发配置，agent 可能在「还没拿到配置」
// 的窗口里收到 save/load —— 没有默认值就只能拒绝，而默认值恰好与服务端种子同值。
// 目录不可用时 transferPath 仍会回退 /tmp 并在结果里注明（§7.5 的兜底）。
const defaultTransferDir = "/var/lib/uni_agent/transfer"

// WriteExecutor 执行二期写操作（容器/镜像/卷/网络的启停删除与 prune/pull/tag/save/load）。
//
// 三条硬纪律集中在本文件：
//  1. **保护目标默认拒绝**（guard）：core 已按 force 校验过权限，但 core 可被绕过/伪造，
//     agent 是最后一道 —— 而「uni-center 把自己删了」没有第二次机会；
//  2. **路径只由 transferDir + 文件名拼**（transferPath）：协议上从不接受路径入参；
//  3. 面向用户的错误是**结论句**，daemon 的原始错误只进 detail。
type WriteExecutor struct {
	api DockerAPI
	// exec 是固定 argv 的执行入口（B2 的 compose CLI 用它；零 shell、参数以列表交出）。
	// 由 runtime 注入 exec.CommandContext，测试可换成替身。
	exec func(ctx context.Context, name string, args ...string) *exec.Cmd
	// scanner 是 image:scan 的 trivy 执行器（P3·安全面）。复用同一个 exec 注入
	//（compose CLI 与 trivy 是同一族的「主机级 CLI」），探测（LookPath）单独
	// 注入 —— 测试要能独立伪造「未安装」形态。见 image_scan.go。
	scanner *trivyScanner

	// mu 守住可热更新的配置与「本次执行的附带说明」：
	// 配置会在 hello_ack 后变化（Runtime 调 Set*），而 Do 在分派器 worker 里并发读。
	mu          sync.Mutex
	protected   *ProtectedList
	transferDir string
	flavor      string
	note        string
	// sessions 是流会话管理器（4b 拉取进度用，P2 起 build/push 共用）：**必须非 nil**
	// —— 由 Runtime.New 构造会话管理器后经 SetSessions 注入（见 runtime.go 的装配
	// 顺序）。7c 删掉了「nil = 一期黑盒回退」的宽容分支：进度透出已是写路径的
	// 常设依赖而不是增强，装配缺失属于缺陷，fail fast（panic）好过静默黑盒。
	sessions *SessionManager
	// now 是挂钟注入（四期备份令牌取它）：测试要能确定性地产生「同秒连续保存」这类
	// 时序，而 Runtime 的 Deps.Now 已经是全模块统一的时钟入口。
	now func() time.Time
}

// NewWriteExecutor 构造写执行器。
//
// protected/transferDir/flavor 是**初始值**：配置在下发后仍会变化，Runtime 必须继续
// 调用 Set* 同步（构造时拷一份定死会让「配置页里刚加的保护」对执行器无效）。
func NewWriteExecutor(api DockerAPI, protected *ProtectedList, transferDir string, flavor string,
	execCommand func(context.Context, string, ...string) *exec.Cmd) *WriteExecutor {
	if protected == nil {
		protected = ParseProtected("")
	}
	if transferDir == "" {
		// 空 = 尚未收到 hello_ack 的配置，按 spec 的内置默认落盘位置走（见 defaultTransferDir）。
		transferDir = defaultTransferDir
	}
	if execCommand == nil {
		execCommand = exec.CommandContext
	}
	return &WriteExecutor{api: api, protected: protected, transferDir: transferDir, flavor: flavor,
		exec: execCommand, scanner: newTrivyScanner(execCommand), now: time.Now}
}

// SetNow 注入时钟（备份令牌用它；Runtime 传 Deps.Now 保持全模块同一时钟入口）。
func (e *WriteExecutor) SetNow(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	e.mu.Lock()
	e.now = now
	e.mu.Unlock()
}

// SetSessions 注入流会话管理器（4b 拉取进度；P2 起 build/push 共用）。
//
// 契约：**必须非 nil** —— 传 nil 直接 panic。为什么不留「nil = 黑盒回退」：生产
// 装配（Runtime.New）永远注入（audit 已核实），nil 只可能出现在装配缺陷或测试
// 忘了接替身的场合 —— 那两种情形都该在第一发 image:pull 之前现形，而不是把
// 「进度悄悄没有」的黑盒形态带到线上。由 Runtime.New 调用（构造晚于执行器，
// 见 runtime.go）。
func (e *WriteExecutor) SetSessions(m *SessionManager) {
	if m == nil {
		panic("dockerops: WriteExecutor 的流会话管理器必须非 nil（Runtime.New 必经注入；测试请接会话替身）")
	}
	e.mu.Lock()
	e.sessions = m
	e.mu.Unlock()
}

// sessionsValue 返回当前的会话管理器；未注入即 panic（同 SetSessions 的契约 ——
// 「从未调用 SetSessions」与「注入了 nil」是同一种装配缺陷）。
func (e *WriteExecutor) sessionsValue() *SessionManager {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessions == nil {
		panic("dockerops: WriteExecutor 未注入流会话管理器（New 之后必须 SetSessions；生产由 Runtime.New 完成）")
	}
	return e.sessions
}

// nowValue 返回当前挂钟（未注入时用 time.Now）。
func (e *WriteExecutor) nowValue() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}

// SetProtected 替换保护清单（配置下发时由 Runtime 调用）。
//
// 可变而不是构造时定死：清单是**拒绝依据**，构造时拷贝会让更新后的清单失效，
// 症状是「配置页里加了保护，操作照做」——本模块最不可逆的失败模式。
func (e *WriteExecutor) SetProtected(p *ProtectedList) {
	if p == nil {
		p = ParseProtected("")
	}
	e.mu.Lock()
	e.protected = p
	e.mu.Unlock()
}

// SetTransferDir 替换产物目录（空 = 回到内置默认）。
func (e *WriteExecutor) SetTransferDir(dir string) {
	if dir == "" {
		dir = defaultTransferDir
	}
	e.mu.Lock()
	e.transferDir = dir
	e.mu.Unlock()
}

// SetFlavor 记录 compose 形态（单一 flavor 纪律；B2 的 compose argv 用它选二进制）。
func (e *WriteExecutor) SetFlavor(flavor string) {
	e.mu.Lock()
	e.flavor = flavor
	e.mu.Unlock()
}

func (e *WriteExecutor) protectedList() *ProtectedList {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.protected
}

func (e *WriteExecutor) transferDirValue() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.transferDir
}

// flavorValue 返回当前 compose 形态（空 = 尚未探测到）。
// B2 的 compose argv 用它选二进制与子命令形态（见 compose_exec.go）。
func (e *WriteExecutor) flavorValue() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.flavor
}

// scannerValue 返回 trivy 扫描器。扫描器在构造时建立（与 exec 同一注入点），
// 只读字段（execFn/lookPath）不再有热更面，故不走 mu —— 它与 exec 同一待遇。
func (e *WriteExecutor) scannerValue() *trivyScanner {
	return e.scanner
}

// setNote / takeNote 是「成功路径的附带说明」出口：dispatcher 在一条指令成功后会
// 取一次（见 dispatcher.execute 的 successNoter）写进 result.detail。
// 成功但需要留痕的信息（产物回退 /tmp、prune 释放量）在 result 里只有 detail 这个自由文本槽，
// 而 payload 是结构化数据面。
func (e *WriteExecutor) setNote(s string) {
	e.mu.Lock()
	e.note = s
	e.mu.Unlock()
}

// takeNote 返回并清空说明（空串 = 本次执行没有附注）。
func (e *WriteExecutor) takeNote() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.note
	e.note = ""
	return n
}

// Do 分发到具体的写操作。与 ReadExecutor 同签名（dispatcher 的收口不变）。
func (e *WriteExecutor) Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	e.setNote("")
	o := &cmd.Options
	switch cmd.Action {
	case agentproto.DockerActionContainerCreate:
		// 创建面（4a）：新容器没有可 guard 的目标（保护清单管的是**现存**容器等
		// 的停删重建，create 的对象还不存在）——守卫不适用，说明见 container_create.go。
		return e.createContainer(ctx, o)

	case agentproto.DockerActionContainerStart:
		if err := e.guard(ctx, e.guardContainer(ctx, o.Target), o.Force); err != nil {
			return nil, err
		}
		if err := e.api.ContainerStart(ctx, o.Target); err != nil {
			return nil, wrapDocker("启动容器失败", err)
		}
		return nil, nil

	case agentproto.DockerActionContainerStop:
		if err := e.guard(ctx, e.guardContainer(ctx, o.Target), o.Force); err != nil {
			return nil, err
		}
		if err := e.api.ContainerStop(ctx, o.Target); err != nil {
			return nil, wrapDocker("停止容器失败", err)
		}
		return nil, nil

	case agentproto.DockerActionContainerRestart:
		if err := e.guard(ctx, e.guardContainer(ctx, o.Target), o.Force); err != nil {
			return nil, err
		}
		if err := e.api.ContainerRestart(ctx, o.Target); err != nil {
			return nil, wrapDocker("重启容器失败", err)
		}
		return nil, nil

	case agentproto.DockerActionContainerRemove:
		if err := e.guard(ctx, e.guardContainer(ctx, o.Target), o.Force); err != nil {
			return nil, err
		}
		if err := e.api.ContainerRemove(ctx, o.Target, o.Force); err != nil {
			return nil, wrapDocker("删除容器失败", err)
		}
		return nil, nil

	case agentproto.DockerActionImageRemove:
		// 保护清单只有容器/项目/服务/卷四种粒度，没有镜像 —— 删除镜像不经过 guard。
		if err := e.api.ImageRemove(ctx, o.Target, o.Force); err != nil {
			return nil, wrapDocker("删除镜像失败", err)
		}
		return nil, nil

	case agentproto.DockerActionImagePrune:
		freed, err := e.api.ImagePrune(ctx, o.All)
		if err != nil {
			return nil, wrapDocker("清理镜像失败", err)
		}
		e.setNote(fmt.Sprintf("space_reclaimed=%d bytes", freed))
		return marshalWritePayload(&writeSpacePayload{SpaceReclaimedBytes: freed})

	case agentproto.DockerActionImagePull:
		// 4b：拉取照旧是**同步写指令**（受理 → 轮询 → 终态结论句的语义不动），
		// 差别只在执行期间经流会话透出进度（pullImage 里做完会话接线、终态项与
		// eof 对齐 —— 见 pull_progress.go 的选型说明）。cmd.Ref 进方法：进度会话的
		// 句柄由 ref 派生（core 受理时预登记的就是它）。
		// 4c：core 随指令注入的仓库认证一并透传（无凭据 = nil，与 4b 逐字一致）。
		return nil, e.pullImage(ctx, cmd.Ref, o.Target, imageAuthOf(cmd))

	case agentproto.DockerActionImageBuild:
		// P2：构建是同一形态的长耗时写指令（受理 → 轮询 → 终态结论句），进度走
		// build_<ref> 派生会话（buildImage 里完成接线），上下文 tar 的安全校验
		//（穿越/尺寸/条目/Dockerfile 存在性）在 adapter 的 scanBuildContext
		// —— 恶意上下文到不了 daemon。
		return nil, e.buildImage(ctx, cmd.Ref, o)

	case agentproto.DockerActionImagePush:
		// P2：推送与拉取完全同源 —— 进度走 push_<ref> 派生会话，凭据走 4c 的
		// 公共注入面（imageAuthOf 与 pull 同一函数；无凭据 = nil，与 4b 之前
		// 拉取的自由度一致）。
		return nil, e.pushImage(ctx, cmd.Ref, o.Target, imageAuthOf(cmd))

	case agentproto.DockerActionImageScan:
		// P3·安全面：扫描是只读语义（不 guard —— 没有可破坏的目标），但它要在
		// 主机上跑 trivy 分钟级（15 分钟执行档，超时口径见 image_scan.go），报告
		// 是 image:inspect 同族的 result.payload 数据面。任务中心的 pending
		// 期可见性由 cmd 通道天然提供（trivy json 模式中途无可流的增量，进度流
		// 刻意不建 —— 见协议 DockerActionImageScan 的取舍注释）。
		return e.scanImage(ctx, cmd)

	case agentproto.DockerActionImageTag:
		if err := e.api.ImageTag(ctx, o.Src, defaultImageTag(o.Dst)); err != nil {
			return nil, wrapDocker("给镜像打标签失败", err)
		}
		return nil, nil

	case agentproto.DockerActionImageSave:
		path, err := e.transferPath(o.Filename, false)
		if err != nil {
			return nil, err
		}
		already, err := e.api.ImageSave(ctx, o.Target, path, o.Overwrite)
		if err != nil {
			return nil, wrapDocker("导出镜像失败", err)
		}
		if already {
			// 两段确认的第二段：服务端把 already_exists 透出，前端问过覆盖后带
			// overwrite=true 重发（§7.5）——**不覆盖**是这里的默认动作。
			return nil, &ExecError{Msg: "产物文件已存在，确认覆盖后重试", AlreadyExists: true}
		}
		return e.transferPayload(path)

	case agentproto.DockerActionImageLoad:
		path, err := e.transferPath(o.Filename, true)
		if err != nil {
			return nil, err
		}
		if err := e.api.ImageLoad(ctx, path); err != nil {
			return nil, wrapDocker("导入镜像失败", err)
		}
		return e.transferPayload(path)

	case agentproto.DockerActionVolumeRemove:
		// 卷只按卷名判定（volume: 粒度）：卷没有 compose 标签可看。
		if err := e.guard(ctx, protectedTarget{volume: o.Target}, o.Force); err != nil {
			return nil, err
		}
		if err := e.api.VolumeRemove(ctx, o.Target, o.Force); err != nil {
			return nil, wrapDocker("删除卷失败", err)
		}
		return nil, nil

	case agentproto.DockerActionVolumePrune:
		// 空过滤器 = daemon 默认口径（只清匿名未使用卷）：命名卷——包括保护清单里的——
		// 不在其中，故这里没有可 guard 的目标（与镜像清理同理）。
		freed, err := e.api.VolumePrune(ctx)
		if err != nil {
			return nil, wrapDocker("清理卷失败", err)
		}
		e.setNote(fmt.Sprintf("space_reclaimed=%d bytes", freed))
		return marshalWritePayload(&writeSpacePayload{SpaceReclaimedBytes: freed})

	case agentproto.DockerActionNetworkRemove:
		if err := e.api.NetworkRemove(ctx, o.Target); err != nil {
			return nil, wrapDocker("删除网络失败", err)
		}
		return nil, nil

	// ── 二期 B2：compose 项目与网元操作 ─────────────────────────────────
	// argv 构建与执行在 compose_exec.go（固定 argv、flavor 纪律、公共子集）。
	case agentproto.DockerActionComposeUp, agentproto.DockerActionComposeStop,
		agentproto.DockerActionComposeStart, agentproto.DockerActionComposeRestart,
		agentproto.DockerActionComposePull, agentproto.DockerActionComposeDown,
		agentproto.DockerActionComposeServiceScale,
		agentproto.DockerActionComposeServiceRemoveContainers:
		return nil, e.doCompose(ctx, cmd)

	// ── 四期：配置编辑三条写路径 ─────────────────────────────────────
	// 预检/全文写/回滚/按键合并与收尾全在 compose_file_write.go（同一收尾链）。
	case agentproto.DockerActionComposeFileValidate,
		agentproto.DockerActionComposeFileWrite,
		agentproto.DockerActionComposeFilePatch:
		return e.doComposeFile(ctx, cmd)

	default:
		// 走到这里 = action 已登记「已实现」却没有分支（开发期错误），或三/四期的
		// action（exec / compose.file:write 等）尚未落地。结论句与 dispatcher 的
		// 未实现档一致，不暴露内部分支结构。
		return nil, &ExecError{Msg: "该操作尚未开放"}
	}
}

// ── 保护强制 ────────────────────────────────────────────────────────────

// protectedTarget 把「这次操作的目标」解析成保护清单里的四种粒度。
//
// 容器类要看 compose 标签（容器名 / 项目 / 项目+服务），卷类只看卷名 —— 清单的
// 四种粒度各自对应一条解析路径，混用会让「project:x/服务」这类条目永不命中。
type protectedTarget struct {
	container string
	project   string
	service   string
	volume    string
}

// guard 在 protected 目标上默认拒绝：删除/停止/重建一律要求 force。
//
// 为什么在 agent 也判一次（core 侧已按 force 校验了权限）：core 可能被绕过
// （直连 agent 的旧明文入口在 T1 之后已封，但纵深防御不嫌多），而**保护底座**是本模块
// 最不可逆的失败模式 —— 「uni-center 把自己删了」没有第二次机会。
func (e *WriteExecutor) guard(ctx context.Context, t protectedTarget, force bool) error {
	if force {
		return nil
	}
	p := e.protectedList()
	if p.IsEmpty() {
		return nil
	}
	hit := (t.container != "" && p.ContainerProtected(t.container, t.project, t.service)) ||
		(t.volume != "" && p.Volume(t.volume)) ||
		// compose 网元级操作没有容器名可查（t.container 为空），服务粒度必须在这里
		// **成对**命中 —— 漏掉这一条会让 `project:<项目>/<服务>` 对网元操作永不生效。
		(t.project != "" && t.service != "" && p.Service(t.project, t.service)) ||
		(t.project != "" && t.service == "" && p.Project(t.project))
	if !hit {
		return nil
	}
	return &ExecError{
		Msg:    "这是受保护的目标，需要显式的强制确认才能操作",
		Detail: "protected target: " + describeTarget(t),
	}
}

// guardContainer 解析一个容器操作的目标：容器名 + 它标签里的 compose 项目/服务。
//
// 容器类操作**必须**看 compose 标签：清单里的 `project:<名>` 与 `project:<名>/<服务>`
// 两个粒度只能靠标签命中，只拿容器名去比会让这两条永不生效。
// 取不到容器（列表失败/已被删）时退化为「只有容器名」：名字粒度仍然生效，
// 而操作随后会在 daemon 侧得到真实错误 —— 不把「读不到标签」变成一句误导性的拒绝。
func (e *WriteExecutor) guardContainer(ctx context.Context, name string) protectedTarget {
	t := protectedTarget{container: name}
	if e.protectedList().IsEmpty() {
		return t // 清单为空时不必付一次 daemon 往返
	}
	cs, err := e.api.Containers(ctx)
	if err != nil {
		return t
	}
	// 先按名字、再按 ID 找：两趟是为了「目标是 ID」时也能取到**规范容器名** ——
	// 保护清单的容器粒度写的是名字，不换算的话按 ID 发来的操作会漏掉名字粒度。
	for _, c := range cs {
		if c.Name == name {
			t.project = c.Labels[composeProjectLabel]
			t.service = c.Labels[composeServiceLabel]
			return t
		}
	}
	for _, c := range cs {
		if c.ID == name {
			t.container = c.Name
			t.project = c.Labels[composeProjectLabel]
			t.service = c.Labels[composeServiceLabel]
			return t
		}
	}
	return t
}

// describeTarget 把命中形态写成排障可见的描述（只进 detail，不进结论句）。
func describeTarget(t protectedTarget) string {
	switch {
	case t.volume != "":
		return "volume:" + t.volume
	case t.service != "":
		return "project:" + t.project + "/" + t.service
	case t.project != "":
		return "project:" + t.project
	default:
		return "container:" + t.container
	}
}

// ── 路径纪律 ────────────────────────────────────────────────────────────

// transferPath 把「文件名」拼成产物绝对路径，并做三件事：
//  1. **只接受文件名**（协议层已按正则校验，这里再挡一次路径分隔符与 ..）；
//  2. 目录不存在/不可写时回退 /tmp 并在成功结果的 detail 里注明（spec §7.5：
//     .106 没有 /data，配置目录在本机不可用时**不报错中断**）；
//  3. load 侧用 EvalSymlinks 解析后确认仍在目录内（防符号链接逃逸）。
func (e *WriteExecutor) transferPath(filename string, forRead bool) (string, error) {
	// 反斜杠在 Linux 上不是分隔符，但它是另一套文件名的分隔符 —— 一律拒绝，
	// 免得「文件名」在两个平台上不是同一个东西。
	if !agentproto.IsDockerTarFilename(filename) ||
		filepath.Base(filename) != filename || strings.ContainsRune(filename, '\\') {
		return "", &ExecError{Msg: "文件名不合法（只能填文件名，不能带路径）"}
	}
	dir := e.transferDirValue()
	if forRead {
		return e.transferReadPath(dir, filename)
	}
	if !writableDir(dir) {
		fallback := os.TempDir()
		e.setNote(fmt.Sprintf("产物目录不可用（%s），已回退到 %s", dir, fallback))
		return filepath.Join(fallback, filename), nil
	}
	return filepath.Join(dir, filename), nil
}

// transferReadPath 解析 load 侧的真实路径：解析符号链接后必须仍落在目录内。
//
// 目录本身也走 EvalSymlinks（部署目录可能是符号链接，如 /data → 别的盘），
// 比较用「解析后的目录」而不是原字符串 —— 否则一个合法的符号链接部署会被误判成逃逸。
func (e *WriteExecutor) transferReadPath(dir, filename string) (string, error) {
	if !readableDir(dir) {
		fallback := os.TempDir()
		e.setNote(fmt.Sprintf("产物目录不可用（%s），已从 %s 读取", dir, fallback))
		dir = fallback
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", &ExecError{Msg: "找不到产物目录", Detail: err.Error()}
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, filename))
	if err != nil {
		return "", &ExecError{Msg: "找不到这个产物文件", Detail: err.Error()}
	}
	rel, err := filepath.Rel(realDir, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", &ExecError{Msg: "产物文件不在允许的目录内"}
	}
	return real, nil
}

// readableDir 报告目录存在且是目录。
func readableDir(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// writableDir 报告目录存在且可写。
//
// 「可写」不靠模式位判断（agent 以 root 运行，模式位说明不了能不能写），而是
// **实际创建一个探针文件再删掉**：这是唯一同时覆盖「权限不足」与「只读挂载」的判据。
func writableDir(dir string) bool {
	if !readableDir(dir) {
		return false
	}
	f, err := os.CreateTemp(dir, ".uni_agent_probe_*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// ── 载荷与错误 ──────────────────────────────────────────────────────────

// writeSpacePayload 是 prune 类操作的成功载荷（前端据此显示「本次释放」）。
type writeSpacePayload struct {
	SpaceReclaimedBytes int64 `json:"space_reclaimed_bytes"`
}

// transferPayload 是 image:save/load 的成功载荷（spec §7.5：两步指令的 result
// 都带产物路径与大小）。
type transferPayload struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// transferPayload 生成产物载荷。体积取不到时报 0（save 失败早就返回了；
// 这里的 0 只可能出现在替身/文件被立刻移走的场景，不该把成功翻成失败）。
func (e *WriteExecutor) transferPayload(path string) ([]byte, error) {
	p := transferPayload{Path: path}
	if fi, err := os.Stat(path); err == nil {
		p.SizeBytes = fi.Size()
	}
	return marshalWritePayload(&p)
}

func marshalWritePayload(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, wrapDocker("生成执行结果失败", err)
	}
	return b, nil
}

// wrapDocker 把 SDK/daemon 的错误折成**结论句 + 原始细节**：
// 结论句给页面（中文、不含字段名与内部术语），detail 给排障（daemon 原文）。
func wrapDocker(msg string, err error) *ExecError {
	return &ExecError{Msg: msg, Detail: err.Error()}
}

// defaultImageTag 给不带 tag 的目标引用补 :latest（与 docker tag 的命令行行为一致）。
//
// 判断只看**最后一段路径**有没有 ':'：注册表主机带端口时（reg:5000/img）冒号在前面的段里，
// 那不是 tag；digest 形态（repo@sha256:…）不可再补 tag，原样交出。
func defaultImageTag(dst string) string {
	if strings.ContainsRune(dst, '@') {
		return dst
	}
	seg := dst
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	if seg == "" || strings.ContainsRune(seg, ':') {
		return dst
	}
	return dst + ":latest"
}
