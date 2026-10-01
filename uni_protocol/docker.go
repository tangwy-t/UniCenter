package agentproto

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ── Docker 管理：快照 / 指令 / 流（spec 2026-09-23-docker-management-design §3）──
//
// 三条通道：
//
//	agent.docker.state   上行  一台主机的资源快照（周期 30s；Docker 不可达也要发）
//	core.docker.cmd      下行  一条操作指令（ref 关联）
//	agent.docker.result  上行  指令结果（ref 回关联，含一期只读 action 的数据）
//	agent.docker.frame   上行  流会话数据帧（日志 chunk / PTY 输出共用）
//	core.docker.frame    下行  流会话控制帧（input / resize / cancel）
//
// 载荷**对齐 Docker API 的核心子集**而不是透传原始 JSON：透传会把 Docker 各版本的
// 字段漂移直接带进前端，也会让载荷尺寸失控（一台主机的 /containers/json 原样可达数百 KB）。

// ── action 白名单（§3.3）────────────────────────────────────────────────
//
// 白名单**一次给全**（含二/三/四期）：它是载荷内的字符串常量，跨版本新增不破坏载荷，
// 而把全集放在协议里让两端对「合法 action」只有一个答案。哪些**本构建已实现**是
// 消费端的事实（agent 的 dispatcher 自己列），不进协议 —— 否则每加一期都要动协议。
const (
	// DockerActionContainerCreate 是创建容器（四支柱·创建面，4a）：消灭「拉了镜像
	// 跑不起来」的闭环断裂。与启停同级写操作（docker:manage）；它**没有 target** ——
	// 动作对象是「将要诞生的容器」而不是已存在的目标，必填项只有 options.image。
	// 名字冲突/镜像缺失由 agent 折成结论句（不自动拉取：拉取是独立长耗时 action）。
	//
	// 确认档是标准档：创建可逆（remove 即可），不需要「照抄什么」的强确认。
	DockerActionContainerCreate = "container:create"

	DockerActionContainerStart   = "container:start"
	DockerActionContainerStop    = "container:stop"
	DockerActionContainerRestart = "container:restart"
	DockerActionContainerRemove  = "container:remove"
	DockerActionContainerLogs    = "container:logs"
	DockerActionContainerInspect = "container:inspect"
	DockerActionContainerExec    = "container:exec"
	// DockerActionContainerStats 是 stats 实时流（监控面）：会话制 action，建立后
	// 数据走 agent.docker.frame（会话内逐样本一帧），权限与 container:logs 同档。
	DockerActionContainerStats = "container:stats"
	// DockerActionEvents 是事件实时流（docker events 订阅，总览页「活动流」的数据源，
	// 六期·监控面）。与 stats 一样是**会话制**（建立后数据走 agent.docker.frame，
	// 逐事件一条 JSON 行，见 DockerEventItem），但它是 **host 级** action：
	// 动作对象是「这台主机的 docker daemon」而不是某个容器/镜像 —— 每台主机只有一个
	// daemon，指令本身经由哪条 agent 连接下发就作用于哪台主机，故 target 恒为空、
	// Required 为空集（与 prune 类的「没有单一目标名」是同一形态：约束 = 显式空集）。
	//
	// 为什么叫 docker:events 而不是 host:events：白名单的命名文法一直是「资源前缀:
	// 动作」（container:/image:/volume:/network:/compose:），host: 会为一个成员新开一支
	// 资源家族；而事件流的语义恰是 `docker system events` —— daemon 本身就是这个动作
	// 的资源对象，docker: 前缀让「资源」升到域级而不是造一个新家族。
	//
	// 它是 **core 的常驻订阅**（core 自己是这条流的客户端，再扇出给多个 console 会话），
	// **不经用户的指令面**：dockerpolicy 表里没有它 —— 用户往 /docker/hosts/:id/cmds
	// 发它会被判「未知操作」，core 的常驻管理器自己在内部建 ref 并收发（§常驻管理器）。
	DockerActionEvents = "docker:events"

	DockerActionImageRemove  = "image:remove"
	DockerActionImagePrune   = "image:prune"
	DockerActionImagePull    = "image:pull"
	DockerActionImageTag     = "image:tag"
	DockerActionImageInspect = "image:inspect"
	DockerActionImageSave    = "image:save"
	DockerActionImageLoad    = "image:load"

	DockerActionVolumeRemove = "volume:remove"
	DockerActionVolumePrune  = "volume:prune"

	DockerActionNetworkRemove = "network:remove"

	DockerActionComposeUp      = "compose:up"
	DockerActionComposeStop    = "compose:stop"
	DockerActionComposeStart   = "compose:start"
	DockerActionComposeRestart = "compose:restart"
	DockerActionComposePull    = "compose:pull"
	DockerActionComposeDown    = "compose:down"

	DockerActionComposeServiceScale            = "compose.service:scale"
	DockerActionComposeServiceRemoveContainers = "compose.service:remove-containers"

	DockerActionComposeFileRead     = "compose.file:read"
	DockerActionComposeFileWrite    = "compose.file:write"
	DockerActionComposeFileValidate = "compose.file:validate"
	DockerActionComposeFilePatch    = "compose.file:patch"

	// DockerActionComposeLogs 是一个 compose 项目的**聚合日志**（五期 5a，后端半边）：
	// 排障时逐容器开日志，多服务项目一条条点既慢又看不出交错时序 —— 这里把
	// `docker compose -p <项目> logs` 的整段输出聚成**一条**带服务前缀的流。
	//
	// 形态对齐 container:logs 的流式分支（options.follow/tail，不发明新字段），差别
	// 有三点，全部来自「CLI 而不是 daemon API 是数据源」这一事实：
	//   - **天生会话制**：CLI 输出只能以流读取（follow=长流；非 follow=读完即 eof），
	//     没有一次性取回的形态，options.follow 只是决定是否给 CLI 传 --follow；
	//   - **since 不支持**：公共子集纪律（§6.1）—— compose v1 的 logs 没有 --since
	//     flag，带 since 的请求在这里被拒而不是静默无视（用户要「十分钟以来」而拿到
	//     「最近 100 行」是一个静默的错误答案，照 backup 的字段归属纪律显式拒绝）；
	//   - 输出**原文透传**：compose 的日志行本身已是 `<时间戳> <服务>  | 正文` 的
	//     聚合形态（CLI 侧带 --timestamps），协议不做二次解析。
	//
	// target 是项目名（复用 compose 项目名的白名单与校验）。确认档是标准档：读日志
	// 不改任何状态，保护档（force）对它没有意义。
	DockerActionComposeLogs = "compose:logs"
)

// 确认档的取值形态（§10 第 1 条）：标准档空、固定文本、照抄目标名、照抄文件名。
const (
	// DockerConfirmDelete 是固定文本确认值（prune / down 这类没有单一目标名的批量动作）。
	DockerConfirmDelete = "DELETE"
	// DockerConfirmTarget 是「照抄目标名」确认档（服务级动作照抄**服务名**）。
	DockerConfirmTarget = "target"
	// DockerConfirmFilename 是「照抄文件名」确认档（image:save 覆盖已有产物）。
	DockerConfirmFilename = "filename"
)

// compose 形态（§6.2 单一 flavor 纪律：探测一次，之后该主机始终用同一个）。
const (
	DockerComposeFlavorPlugin       = "plugin"
	DockerComposeFlavorStandaloneV2 = "standalone-v2"
	DockerComposeFlavorV1           = "v1"
)

// 流的控制帧 op（§3.1）。
const (
	DockerFrameOpInput  = "input"
	DockerFrameOpResize = "resize"
	DockerFrameOpCancel = "cancel"
)

// ── 尺寸上限 ────────────────────────────────────────────────────────────
const (
	// MaxDockerFrameDataBytes 是单帧数据的字节上限 = 日志合帧阈值（§3.1.2）。
	MaxDockerFrameDataBytes = 64 << 10
	// MaxDockerResultDetailBytes 是 result.detail 的上限（照升级域 NormalizeReasonDetail 先例）。
	MaxDockerResultDetailBytes = 2048
	// MaxDockerPayloadBytes 是 result.payload 的上限：日志与 yml 都在其中，
	// 且远低于协议单消息上限 1MB（留出信封与同帧其它字段的空间）。
	MaxDockerPayloadBytes = 256 << 10
	// MaxDockerComposeFileBytes 是 compose 文件内容的尺寸上限（§13：1MB，实测 14KB，留 70×）。
	MaxDockerComposeFileBytes = 1 << 20
	// MaxDockerStateEntries 是快照中每类资源的条目上限：一台主机上千容器不现实，
	// 而畸形/恶意的大数组会把单消息上限撑爆、让整帧被协议层拒掉。
	MaxDockerStateEntries = 2000
	// MaxDockerStringBytes 是快照里单个字符串字段的上限（名字/镜像引用/状态句）。
	MaxDockerStringBytes = 512
	// maxDockerLogTail 是 container:logs 的行数上限（UI 默认 100，留出「看全一点」的余地）。
	maxDockerLogTail = 10000
	// maxDockerScaleN 是 compose.service:scale 的实例数上限（防一次扩到上千）。
	maxDockerScaleN = 100
	// maxDockerTermSize 是终端 resize 的行列上限。
	maxDockerTermSize = 1000
	// maxDockerExecArgvItems / maxDockerExecArgvBytes 是 container:exec 的 argv 上限
	//（v1.2.3 冻结契约：数组 ≤32 项、每项 ≤256B、元素非空、不得含 NUL 或换行）。
	//
	// 为什么只有尺寸与形态约束：argv **直传 daemon，不经 shell**（协议上也不接受
	// 自由文本命令），故这里不存在「命令注入」这一面 —— 真正的护栏是 core 的
	// docker:exec 权限与 agent 侧的「不拼 shell」执行纪律。
	maxDockerExecArgvItems = 32
	maxDockerExecArgvBytes = 256
	// maxDockerRegistrySecretBytes 是仓库凭据密码的字节上限（4c）：密码会被加密封成
	// 短字符串存入凭据库、随消息瞬时下发到 agent，给一个与协议其它自由文本同档的
	// 上限挡住畸形/恶意的超长值（真实仓库密码远用不到 4KB）。
	maxDockerRegistrySecretBytes = 4096
	// maxDockerRegistryAddrBytes 是仓库地址的字节上限（主机名+端口最长的合法形态
	// 也远小于此，上限只为挡住畸形/恶意的超长字符串挤占凭据表索引）。
	maxDockerRegistryAddrBytes = 255

	// ── 四支柱·创建面（container:create）的 options 上限 ─────────────────
	//
	// maxDockerCreateListItems 是 ports/env/mounts 各自的条目上限（与 exec argv 的
	// 32 同族：表单里一次建容器用不到几十条，上限只为挡住畸形/恶意的巨数组）。
	maxDockerCreateListItems = 32
	// maxDockerCPULimit 是 cpu_limit 的核数上限：现场主机多为 8-16 核，32 的
	// 上限只为防手滑（把 1000 写进核数会让 daemon 给这台容器开一个
	// 「配额等于整机」的口子，而用户自以为只是设了个上限）。
	maxDockerCPULimit = 32
	// maxDockerMemLimitMB 是 mem_limit_mb 的上限（MB）：32GB。取 32GB 与 CPU 的
	// 32 核同档 —— 上限要的是「拦手滑」而不是「限制场景」；0 = 不限额（缺席形态，
	// 与 logs 的 tail=0 同语义）。
	maxDockerMemLimitMB = 32 << 10
)

// DockerActionSpec 是 action 的静态属性中**两端都必须一致**的那部分。
//
// 权限码、超时、期次不在协议里：那是 core 的策略（internal/pkg/dockerpolicy），
// 协议只管「一条消息怎么被双方同样地理解」。
type DockerActionSpec struct {
	Action string
	// Required 是必须有值的 options 字段名（措辞与 JSON tag 一致，供 400 提示直接引用）。
	Required []string
	// OneOf 是「组内字段**恰好出现一个**」的互斥约束（组间彼此独立）。
	//
	// 为什么不把组内字段塞进 Required：Required 的语义是「每个都必须有」，用它表达
	// 二选一会让两个不同的问题共用一列 —— 守卫测试（必填项必须能解析出非空值）
	// 也就无法分别断言「都缺」与「都给」这两种相反的非法形态。
	//
	// v1.2.4 的唯一用例：compose.file:write 的 content|backup —— 全文写与备份回滚
	// 是两条**互斥**的正文来源，都给时不猜优先级（拒绝），都缺时无从下手（拒绝）。
	OneOf [][]string
	// Confirm 是确认档形态：""（标准档）/ DockerConfirmDelete / DockerConfirmTarget /
	// DockerConfirmFilename。取值来源由 ExpectedDockerConfirm 统一给出。
	Confirm string
}

// dockerActionSpecs 是白名单的**唯一枚举源**（顺序 = §3.3 书写顺序）。
var dockerActionSpecs = []DockerActionSpec{
	// create 只有 image 必填：其余全是「给了就按白名单校验」的可选项。名字冲突
	// 由 agent 对照 daemon 的 409 折成结论句，这里只需保证「image 是个合法的镜像引用」。
	{Action: DockerActionContainerCreate, Required: []string{"image"}},
	{Action: DockerActionContainerStart, Required: []string{"target"}},
	{Action: DockerActionContainerStop, Required: []string{"target"}},
	{Action: DockerActionContainerRestart, Required: []string{"target"}},
	// 单删容器是**标准档**（docker:delete 即可）：protected 目标才升级到保护档（force）。
	{Action: DockerActionContainerRemove, Required: []string{"target"}},
	{Action: DockerActionContainerLogs, Required: []string{"target"}},
	{Action: DockerActionContainerInspect, Required: []string{"target"}},
	{Action: DockerActionContainerExec, Required: []string{"target"}},
	{Action: DockerActionContainerStats, Required: []string{"target"}},
	// events 是 host 级订阅（动作对象是 daemon 本身）：没有 target 也没有任何必填项 ——
	// Required 空集 + Confirm 标准档 = 一条零参数指令，「带上 target 也只是被无视」
	// 的宽松由校验机制的自然语义给出（validateDockerTarget 只在 target 非空时跑）。
	{Action: DockerActionEvents},

	{Action: DockerActionImageRemove, Required: []string{"target"}},
	{Action: DockerActionImagePrune, Confirm: DockerConfirmDelete},
	{Action: DockerActionImagePull, Required: []string{"target"}},
	{Action: DockerActionImageTag, Required: []string{"src", "dst"}},
	{Action: DockerActionImageInspect, Required: []string{"target"}},
	// 覆盖已有产物时确认值是**文件名**（§7.5 两段确认复用 cmd/result）。
	{Action: DockerActionImageSave, Required: []string{"target", "filename"}, Confirm: DockerConfirmFilename},
	{Action: DockerActionImageLoad, Required: []string{"filename"}},

	{Action: DockerActionVolumeRemove, Required: []string{"target"}},
	{Action: DockerActionVolumePrune, Confirm: DockerConfirmDelete},

	{Action: DockerActionNetworkRemove, Required: []string{"target"}},

	{Action: DockerActionComposeUp, Required: []string{"target"}, Confirm: DockerConfirmTarget},
	{Action: DockerActionComposeStop, Required: []string{"target"}, Confirm: DockerConfirmTarget},
	{Action: DockerActionComposeStart, Required: []string{"target"}},
	{Action: DockerActionComposeRestart, Required: []string{"target"}},
	{Action: DockerActionComposePull, Required: []string{"target"}},
	{Action: DockerActionComposeDown, Required: []string{"target"}, Confirm: DockerConfirmTarget},

	{Action: DockerActionComposeServiceScale, Required: []string{"target", "n"}, Confirm: DockerConfirmTarget},
	{Action: DockerActionComposeServiceRemoveContainers, Required: []string{"target"}, Confirm: DockerConfirmTarget},

	{Action: DockerActionComposeFileRead, Required: []string{"target"}},
	// 写路径的正文来源是**二选一**（v1.2.4）：content（全文写）或 backup（回滚到某份
	// 备份）。两者都不在 Required 里 —— 它们互斥，只能由 OneOf 表达（§9 两条保存路径）。
	{Action: DockerActionComposeFileWrite, Required: []string{"target", "base_hash"},
		OneOf: [][]string{{"content", "backup"}}, Confirm: DockerConfirmTarget},
	{Action: DockerActionComposeFileValidate, Required: []string{"target", "content"}},
	{Action: DockerActionComposeFilePatch, Required: []string{"target", "base_hash", "patch"}, Confirm: DockerConfirmTarget},

	// 聚合日志（5a）：target=项目名必填；follow/tail 与 container:logs 同名同义
	//（follow 只是 CLI 的 --follow 开关 —— 两种形态都是流会话，见常量注释）。
	{Action: DockerActionComposeLogs, Required: []string{"target"}},
}

// dockerOptionFields 是 DockerCmdOptions 的全部字段名（与 JSON tag 一一对应）。
var dockerOptionFields = []string{
	"target", "tail", "since", "n", "force", "filename", "overwrite", "all",
	"remove_orphans", "volumes", "content", "base_hash", "src", "dst", "patch",
	"follow", "command", "backup",
	// 四支柱·创建面（4a）。
	"image", "name", "ports", "env", "mounts", "restart_policy", "cpu_limit",
	"mem_limit_mb", "network", "start",
	// 4c 仓库凭据。
	"registry",
}

// AllDockerActions 返回全部合法 action（顺序 = 白名单书写顺序）。
func AllDockerActions() []string {
	out := make([]string, 0, len(dockerActionSpecs))
	for _, s := range dockerActionSpecs {
		out = append(out, s.Action)
	}
	return out
}

// LookupDockerAction 按 action 串查找规格。
func LookupDockerAction(action string) (DockerActionSpec, bool) {
	for _, s := range dockerActionSpecs {
		if s.Action == action {
			return s, true
		}
	}
	return DockerActionSpec{}, false
}

// IsDockerAction 报告 action 是否在白名单内。
func IsDockerAction(action string) bool {
	_, ok := LookupDockerAction(action)
	return ok
}

// ── 名字/引用校验（§4.3.1 的 options 校验段）──────────────────────────────

var (
	// 项目名：与 compose 的项目名规范化规则一致（首字符字母数字）。
	dockerProjectNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	// tar 文件名：**不含路径分隔符与 ..**（协议上不出现路径，就不存在路径逃逸面）。
	dockerTarFilenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.tar$`)
	// 镜像引用：仓库路径 + 可选 :tag + 可选 @sha256:digest。
	dockerImageRefRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$`)
	// 容器/卷/网络名：比项目名多了「允许首字符之外的更多符号」的余地不做扩展，保持一致。
	dockerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	// 备份令牌：YYYYMMDD-HHMMSS（agent 挂钟时间）。
	//
	// **形态就是全部约束**：令牌在协议上是不可解释的 opaque 值，agent 自己拼
	// `<配置文件>.bak-<令牌>` —— 路径分隔符/`..`/空白因此都不可能出现在这条通道上
	// （§8 路径白名单：协议上永远不出现路径）。
	dockerBackupTokenRe = regexp.MustCompile(`^\d{8}-\d{6}$`)
	// 仓库地址（4c）：主机名（label 形态，含点/横线/xhy，或 IP）+ 可选 `:端口`。
	// **不含协议头与镜像路径** —— RegistryAuth 的 ServerAddress 与凭据库的唯一键
	// 都是这个形态；带 scheme 或 "registry/img:tag" 会被判成形态非法而不是静默截断，
	// 否则「用户要的凭据」与「下发主机地址」可能被截成两把不同的键。
	dockerRegistryAddrRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?::([0-9]{1,5}))?$`)
)

// IsDockerProjectName 报告 s 是否是合法的 compose 项目名。
func IsDockerProjectName(s string) bool { return s != "" && dockerProjectNameRe.MatchString(s) }

// IsDockerTarFilename 报告 s 是否是合法的 tar 文件名（image:save/load 的唯一文件名口径）。
func IsDockerTarFilename(s string) bool { return s != "" && dockerTarFilenameRe.MatchString(s) }

// IsDockerImageRef 报告 s 是否是合法的镜像引用（含 digest 形态）。
func IsDockerImageRef(s string) bool { return s != "" && dockerImageRefRe.MatchString(s) }

// IsDockerRegistryAddr 报告 s 是否是合法的仓库地址（4c）：主机名/IP + 可选端口
// （1-65535），不带协议头与镜像路径（见 dockerRegistryAddrRe 的说明）。这是
// 「凭据能不能被安全地放进 RegistryAuth.ServerAddress」的形态闸。
func IsDockerRegistryAddr(s string) bool {
	if s == "" || len(s) > maxDockerRegistryAddrBytes {
		return false
	}
	m := dockerRegistryAddrRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	if m[1] != "" { // 端口是捕获组：形态对还要数值合法（1-65535）
		p, err := strconv.Atoi(m[1])
		return err == nil && p >= 1 && p <= 65535
	}
	return true
}

// IsDockerBackupToken 报告 s 是否是合法的备份令牌（yyyyMMdd-HHmmss）。
// 它是 compose.file:write 回滚模式的**唯一**现场输入，agent 据此重建备份文件路径。
func IsDockerBackupToken(s string) bool { return s != "" && dockerBackupTokenRe.MatchString(s) }

func isDockerName(s string) bool { return s != "" && dockerNameRe.MatchString(s) }

// SplitDockerProjectService 拆分「项目/服务」形态的 target（服务级 action 专用）。
func SplitDockerProjectService(target string) (project, service string, ok bool) {
	i := strings.IndexByte(target, '/')
	if i <= 0 || i == len(target)-1 {
		return "", "", false
	}
	project, service = target[:i], target[i+1:]
	if !IsDockerProjectName(project) || !isDockerName(service) {
		return "", "", false
	}
	return project, service, true
}

// IsDockerSessionID 报告 s 是否是合法的流会话 id：≥16 字节且只用 URL 安全字符。
//
// 下限 16 字节是硬要求（§10 第 5 条：crypto/rand ≥16B、**禁雪花**）——可预测即可枚举，
// 而枚举会话 id 等于劫持别人的日志/终端。
func IsDockerSessionID(s string) bool {
	if len(s) < 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// DockerPullSessionID 返回一条拉取指令的**进度会话句柄**：`"pull_" + ref`。
//
// 与普通流会话的两处刻意不同（4b，拉取进度）：
//   - **确定性派生**而不是随机：拉取是长耗时写操作，会话必须在 core **受理指令时**
//     就登记（agent 收到指令才可能开始拉取，届时再经 result 上报句柄就太晚了 ——
//     result 只在拉取**结束**时回）。两端用同一个派生函数，谁都不需要等谁的句柄；
//   - **公开不是问题**：会话 id 用于欺骗的对象价值为零 —— 它不授权任何事。帧归属
//     由 device 匹配校验（篡改会话 id 只会让帧被丢弃），接入由「记录发起人 + 权限码」
//     双闸（端点校验），进来的路径根本不认这个串。真正的句柄保密纪律仍由随机会话
//     id 承担（IsDockerSessionID 的说明），本函数**只用于拉取进度这一种形态**。
//
// 前缀 5 字符保证总长过 IsDockerSessionID 的 16 字节下限（ref 是 ≥19 位的十进制串），
// 即使未来 ref 生成器变短，5+11=16 仍然成立（ref 的最短合法形态也不会再短）。
func DockerPullSessionID(ref string) string { return "pull_" + ref }

// ── options 取值与校验 ──────────────────────────────────────────────────

// dockerOptionValue 按字段名取 options 的**字符串形态**值（"有值/无值" 足以做必填校验，
// 表单里也没有「零值有意义」的可选数值 —— tail=0 与缺席都表示「默认 100」）。
//
// 用名字索引而不是为每个字段写一遍 if：spec 的必填清单是**按名字**写的，
// 名字写错时这里返回空串 → 必填校验失败（红），而不是静默放行。
func dockerOptionValue(o *DockerCmdOptions, field string) string {
	if o == nil {
		return ""
	}
	switch field {
	case "target":
		return o.Target
	case "filename":
		return o.Filename
	case "src":
		return o.Src
	case "dst":
		return o.Dst
	case "base_hash":
		return o.BaseHash
	case "content":
		return o.Content
	case "tail":
		// 0 与缺席同义（都表示「默认 100 行」），故零值视为未填。
		if o.Tail == 0 {
			return ""
		}
		return strconv.Itoa(o.Tail)
	case "since":
		if o.Since == 0 {
			return ""
		}
		return strconv.FormatInt(o.Since, 10)
	case "force":
		return boolStr(o.Force)
	case "overwrite":
		return boolStr(o.Overwrite)
	case "all":
		return boolStr(o.All)
	case "remove_orphans":
		return boolStr(o.RemoveOrphans)
	case "volumes":
		return boolStr(o.Volumes)
	case "n":
		if o.N == nil {
			return ""
		}
		return strconv.Itoa(*o.N)
	case "patch":
		if len(o.Patch) == 0 {
			return ""
		}
		return "set"
	case "follow":
		return boolStr(o.Follow)
	case "command":
		// 数组的「有值/无值」：非空即已填（command 不在任何 Required 清单里，
		// 这一支只为「字段名必须可解析」的守卫生效）。
		if len(o.Command) == 0 {
			return ""
		}
		return "set"
	case "backup":
		return o.Backup
	case "image":
		return o.Image
	case "name":
		return o.Name
	case "ports":
		if len(o.Ports) == 0 {
			return ""
		}
		return "set"
	case "env":
		if len(o.Env) == 0 {
			return ""
		}
		return "set"
	case "mounts":
		if len(o.Mounts) == 0 {
			return ""
		}
		return "set"
	case "restart_policy":
		return o.RestartPolicy
	case "cpu_limit":
		// 0 与缺席同义（都表示「不限额」），故零值视为未填。
		if o.CPULimit == 0 {
			return ""
		}
		return strconv.FormatFloat(o.CPULimit, 'g', -1, 64)
	case "mem_limit_mb":
		if o.MemLimitMB == 0 {
			return ""
		}
		return strconv.Itoa(o.MemLimitMB)
	case "network":
		return o.Network
	case "start":
		// 指针字段：nil = 缺席；指向 false 也算「已填」（显式 false 与缺席不同义）。
		if o.Start == nil {
			return ""
		}
		return strconv.FormatBool(*o.Start)
	case "registry":
		return o.Registry
	default:
		// 未知字段名返回空串 = 必填校验失败：字段名写错是可发现的红灯，不是静默放行。
		return ""
	}
}

// boolStr 把布尔字段折成「有值/无值」：true 表示已填，false 与缺席同义
// （这些开关的零值语义都是「用默认行为」，故 false 不该被当成「显式要求 false」）。
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return ""
}

// ValidateDockerCmdOptions 按 action 校验 options 的必填项与取值形态。
//
// 这是**协议层**校验，两端都跑：core 受理时先跑（明显非法的请求直接 400，不进状态机），
// agent 执行前再跑（服务端可被绕过/伪造，agent 是最后一道）。
func ValidateDockerCmdOptions(action string, o *DockerCmdOptions) error {
	spec, ok := LookupDockerAction(action)
	if !ok {
		return decodeErr(StagePayload, "action", ErrInvalidPayload)
	}
	if o == nil {
		return decodeErr(StagePayload, "options", ErrMissingField)
	}
	for _, field := range spec.Required {
		if dockerOptionValue(o, field) == "" {
			return decodeErr(StagePayload, field, ErrMissingField)
		}
	}
	// 互斥组（v1.2.4）：恰好一个。都缺 = 缺字段（无从下手），都给 = 非法（不猜优先级）。
	for _, group := range spec.OneOf {
		filled := 0
		for _, field := range group {
			if dockerOptionValue(o, field) != "" {
				filled++
			}
		}
		if filled == 0 {
			return decodeErr(StagePayload, group[0], ErrMissingField)
		}
		if filled > 1 {
			return decodeErr(StagePayload, group[1], ErrInvalidPayload)
		}
	}
	// 取值形态：与必填分开 —— 空值已在上面拦掉，这里只管「给了但非法」。
	if o.Target != "" {
		if err := validateDockerTarget(action, o.Target); err != nil {
			return err
		}
	}
	if o.Filename != "" && !IsDockerTarFilename(o.Filename) {
		return decodeErr(StagePayload, "filename", ErrInvalidPayload)
	}
	if o.Src != "" && !IsDockerImageRef(o.Src) {
		return decodeErr(StagePayload, "src", ErrInvalidPayload)
	}
	if o.Dst != "" && !IsDockerImageRef(o.Dst) {
		return decodeErr(StagePayload, "dst", ErrInvalidPayload)
	}
	if o.BaseHash != "" && !IsSHA256Hex(o.BaseHash) {
		return decodeErr(StagePayload, "base_hash", ErrInvalidPayload)
	}
	if o.Tail < 0 || o.Tail > maxDockerLogTail {
		return decodeErr(StagePayload, "tail", ErrInvalidPayload)
	}
	if o.Since < 0 {
		return decodeErr(StagePayload, "since", ErrInvalidPayload)
	}
	// since 的归属（5a）：compose:logs 的 CLI 公共子集里没有 --since（v1 不支持，
	// §6.1 纪律只用三 flavor 都认的 flag）。挂在它身上必须**显式拒绝**而不是静默无视：
	// 用户要「T 时刻以来的日志」而拿到「最近 N 行」是一个静默的错误答案 ——
	// 字段归属纪律与 backup / registry 同一取向（归属唯一的字段，拒绝优于无视）。
	if o.Since > 0 && action == DockerActionComposeLogs {
		return decodeErr(StagePayload, "since", ErrInvalidPayload)
	}
	if o.N != nil && (*o.N < 0 || *o.N > maxDockerScaleN) {
		return decodeErr(StagePayload, "n", ErrInvalidPayload)
	}
	if len(o.Content) > MaxDockerComposeFileBytes {
		return decodeErr(StagePayload, "content", ErrInvalidPayload)
	}
	// container:exec 的 argv（v1.2.3）：尺寸 + 形态。缺席是合法的（缺省 ["/bin/sh"]），
	// 故这里只拒「给了但非法」。
	if len(o.Command) > maxDockerExecArgvItems {
		return decodeErr(StagePayload, "command", ErrInvalidPayload)
	}
	for _, arg := range o.Command {
		if arg == "" || len(arg) > maxDockerExecArgvBytes || strings.ContainsAny(arg, "\x00\n") {
			return decodeErr(StagePayload, "command", ErrInvalidPayload)
		}
	}
	// 备份令牌（v1.2.4）：只有 compose.file:write 的回滚模式用它，且形态必须严格是
	// yyyyMMdd-HHmmss。令牌是**不可解释**的 opaque 值（agent 自己拼备份文件路径），
	// 任何别的形态（路径分隔符、`..`、空白、非数字）都必须在协议层出不去 —— §8 的
	//「协议上永远不出现路径」就靠这一条 + 形态白名单共同成立。
	if o.Backup != "" {
		if action != DockerActionComposeFileWrite || !IsDockerBackupToken(o.Backup) {
			return decodeErr(StagePayload, "backup", ErrInvalidPayload)
		}
	}
	// 补丁形态（v1.2.4，compose.file:patch）：它是**,唯一一个形状由嵌套 JSON 决定的
	// options**，不收口的话会在 agent 的文本手术里炸出各种半懂状态。收口点放在协议层
	//（payload 的第一道闸），agent 侧仍有自己的拒绝分支（纵深防御）。
	if action == DockerActionComposeFilePatch {
		if err := validateDockerPatchShape(o.Patch); err != nil {
			return err
		}
	}
	// 创建面（4a）：create 专属字段只在 create 上校验，出现在别的 action 上则是
	// **字段归属错误**（照 backup 的先例：协议上字段归属唯一的 action，放行会让
	// 写错的字段静默变成 payload 里的噪音）。逐字段白名单见 validateDockerContainerCreate。
	if action == DockerActionContainerCreate {
		if err := validateDockerContainerCreate(o); err != nil {
			return err
		}
	} else if f := firstSetCreateField(o); f != "" {
		return decodeErr(StagePayload, f, ErrInvalidPayload)
	}
	// 仓库凭据引用（4c）：只属于 image:pull（同 backup 的字段归属纪律），
	// 形态必须是不带协议头与路径的仓库地址 ——「凭据键」与「RegistryAuth 的
	// ServerAddress」是同一个值，非法的键不能让它进入凭据查找。
	if o.Registry != "" {
		if action != DockerActionImagePull || !IsDockerRegistryAddr(o.Registry) {
			return decodeErr(StagePayload, "registry", ErrInvalidPayload)
		}
	}
	return nil
}

// dockerPatchSections 是 compose.file:patch 允许出现的段落（§9 的文档模型范围）。
//
// 收在三个段落：configs/secrets/x-* 这些不进四期的表单模型 —— 它们要改就该切 YML 模式
// 走全文 write，而不是让补丁带上一个 agent 不认识的手术面。
var dockerPatchSections = map[string]bool{"services": true, "networks": true, "volumes": true}

// ── 创建面（container:create）options 的逐字段校验 ─────────────────────────

var (
	// 端口映射条目：`宿主:容器[/协议]`。端口位**必须**是 1-65535 的数字形态 ——
	// 0（docker 的「随机分配」语义）刻意不进白名单：随机端口让「创建后去哪连」
	// 无从回答，与其在文档里解释例外的语义，不如直接不支持。
	// 协议非 tcp 即 udp（sctp 的宿主面几乎没人用，且多数前端/防火墙场景不支持）。
	// 两个端口号是捕获组（m[1]/m[2]，范围判定要用数值）。
	dockerPortBindRe = regexp.MustCompile(`^([1-9][0-9]{0,4}):([1-9][0-9]{0,4})(/(tcp|udp))?$`)
	// env 条目：`KEY=VALUE`。键是 POSIX 风格标识符（首字符字母/下划线）；值不含
	// NUL（会炸协议字符串）与换行（污染结果展示、也是伪造「多条记录」的常用手法）。
	dockerEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=[^\x00\n]*$`)
)

// restartPolicySet 是 restart_policy 的枚举白名单（空 = 缺席，由 docker 默认 no 接住）。
var restartPolicySet = map[string]bool{
	"": true, "no": true, "on-failure": true, "always": true, "unless-stopped": true,
}

// firstSetCreateField 返回第一个被填上的 create 专属字段名（全部缺席 = ""）。
// 只在 action != create 时被调用：挂在别的 action 上的 create 字段是归属错误，
// 必须显式拒绝而不是无视。
func firstSetCreateField(o *DockerCmdOptions) string {
	switch {
	case o.Image != "":
		return "image"
	case o.Name != "":
		return "name"
	case len(o.Ports) > 0:
		return "ports"
	case len(o.Env) > 0:
		return "env"
	case len(o.Mounts) > 0:
		return "mounts"
	case o.RestartPolicy != "":
		return "restart_policy"
	case o.CPULimit != 0:
		return "cpu_limit"
	case o.MemLimitMB != 0:
		return "mem_limit_mb"
	case o.Network != "":
		return "network"
	case o.Start != nil:
		return "start"
	}
	return ""
}

// validateDockerContainerCreate 校验 container:create 的 options **逐字段**。
//
// 为什么每个字段都要独立的白名单：这些值最终直传 daemon（SDK 不经 shell ——
// 注入面不在命令拼接，而在「把什么交给 daemon」），格式仍必须白名单化 —— daemon
// 侧的名字/引用自由度很大（env 值可含任意字节、挂载源可以是任意路径），不收敛的
// 后果是把「协议层的模糊非法」摊给每台主机的 daemon 去判，判法随版本漂移。
// 逐字段校验 = 逐字段失败结论句（decodeErr 带字段名，core 的 400 与 agent 的
// 拒绝都能指出是哪个字段）。
//
// create **没有 target**（动作对象是「将要诞生的容器」，语义都在 image/name）：
// 带 target 是字段归属错误，显式拒掉 —— 与「create 专属字段不许挂在别的 action
// 上」是同一纪律的两面，而不是 events 的「带上也放行」式宽松（events 的宽松管
// 的是 host 级动作的零参数形态，不是字段归属）。
func validateDockerContainerCreate(o *DockerCmdOptions) error {
	if o.Target != "" {
		return decodeErr(StagePayload, "target", ErrInvalidPayload)
	}
	if o.Image != "" && !IsDockerImageRef(o.Image) {
		return decodeErr(StagePayload, "image", ErrInvalidPayload)
	}
	if o.Name != "" && !isDockerName(o.Name) {
		return decodeErr(StagePayload, "name", ErrInvalidPayload)
	}
	if len(o.Ports) > maxDockerCreateListItems {
		return decodeErr(StagePayload, "ports", ErrInvalidPayload)
	}
	for _, p := range o.Ports {
		if err := validateDockerPortBind(p); err != nil {
			return err
		}
	}
	if len(o.Env) > maxDockerCreateListItems {
		return decodeErr(StagePayload, "env", ErrInvalidPayload)
	}
	for _, e := range o.Env {
		if err := validateDockerEnv(e); err != nil {
			return err
		}
	}
	if len(o.Mounts) > maxDockerCreateListItems {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	for _, m := range o.Mounts {
		if err := validateDockerMount(m); err != nil {
			return err
		}
	}
	if !restartPolicySet[o.RestartPolicy] {
		return decodeErr(StagePayload, "restart_policy", ErrInvalidPayload)
	}
	// NaN/Inf 必须与越界同拒：JSON 字面量带不进来（encoding/json 不接受），但协议
	// 校验是两端都要跑的纯函数 —— 防的是「程序拼出来的载荷」这一面，NaN 溜进来
	// 会在 agent 侧转成未定义的 int64 NanoCPUs 交给 daemon。
	if o.CPULimit != 0 && (math.IsNaN(o.CPULimit) || math.IsInf(o.CPULimit, 0) ||
		o.CPULimit < 0 || o.CPULimit > maxDockerCPULimit) {
		return decodeErr(StagePayload, "cpu_limit", ErrInvalidPayload)
	}
	if o.MemLimitMB != 0 && (o.MemLimitMB < 0 || o.MemLimitMB > maxDockerMemLimitMB) {
		return decodeErr(StagePayload, "mem_limit_mb", ErrInvalidPayload)
	}
	if o.Network != "" && !isDockerName(o.Network) {
		return decodeErr(StagePayload, "network", ErrInvalidPayload)
	}
	return nil
}

// validateDockerPortBind 校验一条端口映射（regex 匹配后补做范围判定：
// 正则在位数上拦 0 与畸形，65536-99999 这档越界由数值判定收掉）。
func validateDockerPortBind(s string) error {
	m := dockerPortBindRe.FindStringSubmatch(s)
	if m == nil {
		return decodeErr(StagePayload, "ports", ErrInvalidPayload)
	}
	host, err1 := strconv.Atoi(m[1])
	cont, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || host > 65535 || cont > 65535 {
		return decodeErr(StagePayload, "ports", ErrInvalidPayload)
	}
	return nil
}

// validateDockerEnv 校验一条环境变量：键是 POSIX 标识符，值不含 NUL/换行，
// 整条 ≤ MaxDockerStringBytes（快照字符串上限同档 —— 更长的值要么是误输
// 要么是想在协议通道里塞别的东西）。
func validateDockerEnv(s string) error {
	if len(s) > MaxDockerStringBytes || !dockerEnvRe.MatchString(s) {
		return decodeErr(StagePayload, "env", ErrInvalidPayload)
	}
	return nil
}

// validateDockerMount 校验一条挂载：`源:目的地[:ro]`。
//
// 源两种形态：绝对路径（bind）或命名卷名（volume），以是否 '/' 开头分辨 ——
// 这个分辨规则与 docker 的 CL 行为一致（含 '/' 的源在 `docker run -v` 里
// 也被当 bind 路径处理）。目的地必须绝对路径；模式只认 ro（rw 是默认形态，
// 不需要显式写出；z/Z 的 SELinux 标签超出本协议形态集）。
//
// 为什么允许 bind 源是任意绝对路径（协议上**第一次**出现路径入参）：挂载是
// create 的固有语义（命名卷与宿主目录二选一），而「挂哪个目录」正是
// docker:manage 权限本就覆盖的运维决定 —— 与 transferDir 的差别在于，这里的
// 路径**不是** agent 拼出来的产物路径，形态白名单（绝对、无 NUL、无 .. 段以外的
// 结构承诺）就是这面的护栏。
func validateDockerMount(s string) error {
	if len(s) > MaxDockerStringBytes {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	src, dst := parts[0], parts[1]
	if len(parts) == 3 && parts[2] != "ro" {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	if dst == "" || dst[0] != '/' || strings.ContainsAny(dst, "\x00\n") {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	if src == "" {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	if src[0] == '/' {
		if strings.ContainsAny(src, "\x00\n") {
			return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
		}
		return nil
	}
	if !isDockerName(src) {
		return decodeErr(StagePayload, "mounts", ErrInvalidPayload)
	}
	return nil
}

// validateDockerPatchShape 校验补丁的**形状**：段落白名单 + 「名字 → 键值映射 | null」。
//
// 值里的 null 是**删除该键**的语义（§9），故它是合法的叶子，不在这里拦。
// 顶层 null（想整段删光）不合法：删除要逐名表达（保持「只发被改过的键」的口径）。
func validateDockerPatchShape(patch map[string]any) error {
	for section, v := range patch {
		if !dockerPatchSections[section] {
			return decodeErr(StagePayload, "patch", ErrInvalidPayload)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return decodeErr(StagePayload, "patch", ErrInvalidPayload)
		}
		for _, entry := range m {
			if entry == nil {
				continue
			}
			if _, ok := entry.(map[string]any); !ok {
				return decodeErr(StagePayload, "patch", ErrInvalidPayload)
			}
		}
	}
	return nil
}

// validateDockerTarget 按 action 校验 target 的形态。
//
// 为什么不统一一条正则：target 的语义按 action 变化（项目名 / 镜像引用 / 项目+服务 /
// 容器名）。统一放宽会让「项目名里带斜杠」这类注入形态溜进来；统一收紧会把合法的
// 镜像 digest 挡在外面。
func validateDockerTarget(action, target string) error {
	switch action {
	case DockerActionComposeUp, DockerActionComposeStop, DockerActionComposeStart,
		DockerActionComposeRestart, DockerActionComposePull, DockerActionComposeDown,
		DockerActionComposeLogs,
		DockerActionComposeFileRead, DockerActionComposeFileWrite,
		DockerActionComposeFileValidate, DockerActionComposeFilePatch:
		if !IsDockerProjectName(target) {
			return decodeErr(StagePayload, "target", ErrInvalidPayload)
		}
	case DockerActionComposeServiceScale, DockerActionComposeServiceRemoveContainers:
		if _, _, ok := SplitDockerProjectService(target); !ok {
			return decodeErr(StagePayload, "target", ErrInvalidPayload)
		}
	case DockerActionImageRemove, DockerActionImagePull, DockerActionImageInspect,
		DockerActionImageSave:
		if !IsDockerImageRef(target) {
			return decodeErr(StagePayload, "target", ErrInvalidPayload)
		}
	default:
		// 容器名 / 卷名 / 网络名：宽松但不许空、控制字符与路径分隔符。
		if !isDockerName(target) {
			return decodeErr(StagePayload, "target", ErrInvalidPayload)
		}
	}
	return nil
}

// ExpectedDockerConfirm 返回该 action 在给定 options 下要求的 confirm 值（空 = 无需确认）。
func ExpectedDockerConfirm(action string, o *DockerCmdOptions) string {
	spec, ok := LookupDockerAction(action)
	if !ok {
		return ""
	}
	switch spec.Confirm {
	case DockerConfirmDelete:
		return DockerConfirmDelete
	case DockerConfirmTarget:
		// 例外：scale 只在**缩容到 0** 时要确认 —— 扩到 N>0 是可逆的常规操作，
		// 对它要求「照抄服务名」只会训练用户无脑确认（确认档的价值在于稀缺）。
		if action == DockerActionComposeServiceScale && o != nil && o.N != nil && *o.N > 0 {
			return ""
		}
		if o == nil {
			return ""
		}
		if _, svc, ok := SplitDockerProjectService(o.Target); ok {
			return svc
		}
		return o.Target
	case DockerConfirmFilename:
		// 「覆盖时强」（spec §4.3.1：image:save = 覆盖时强(文件名)，§7.5 两段确认）：
		// 只有**用户确认覆盖后的重发**（overwrite=true）才要求照抄文件名。
		//
		// 两段时序（复用 cmd/result，不另造机制）：
		//   1) 首次导出不带 confirm → agent 以 O_EXCL 独占创建；目标已存在时不执行、
		//      结果回 already_exists 标志；
		//   2) UI 问过用户后，带 overwrite=true + confirm=<文件名> 重发 → 才允许覆盖。
		//
		// 为什么第一段不能就要 confirm：首次导出是常规路径（目标文件通常并不存在），
		// 无条件要文件名会把「正常首次导出」挡成一次假失败（生产实测复现）；而且每次
		// 导出都抄一遍会把确认档训练成例行公事，稀缺性一失，第二段也就形同虚设。
		if o == nil || !o.Overwrite {
			return ""
		}
		return o.Filename
	default:
		return ""
	}
}

// CheckDockerConfirm 校验 confirm 字段。标准档带不带都放行；强确认档必须逐字匹配
// （大小写敏感：它对人而言是「照抄一遍」的动作，模糊匹配会让它退化成「随便填点东西」）。
func CheckDockerConfirm(action string, o *DockerCmdOptions, confirm string) error {
	want := ExpectedDockerConfirm(action, o)
	if want == "" {
		return nil
	}
	if confirm != want {
		return decodeErr(StagePayload, "confirm", ErrMissingField)
	}
	return nil
}

// NormalizeDockerDetail 截断排障细节（照升级域 NormalizeReasonDetail 先例）。
//
// 上限定在 2KB：detail 是排障线索（stderr 首行、HTTP 码），不是日志通道 ——
// 把整段 stderr 塞进协议消息会挤掉同帧的有效载荷，也会让它悄悄长成第二份日志。
func NormalizeDockerDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxDockerResultDetailBytes {
		return s
	}
	return s[:MaxDockerResultDetailBytes]
}

// ── 上行 1：agent.docker.state ──────────────────────────────────────────

// DockerState 是一台主机的 Docker 资源快照。
//
// **Docker 不可达也要发**（docker_ok=false + error 结论句 + 空清单）：这是能力信号 ——
// 前端据此显示「该主机 Docker 不可用」而不是渲染一堆点了没反应的按钮。
type DockerState struct {
	// T 是 agent 侧采集时刻（unix 毫秒）。陈旧度**不用它**算：agent 时钟可能偏
	//（.106 实测快 8 小时），core 用收到帧的服务端时刻判断。
	T int64 `json:"t"`
	// DockerOK 表示 docker.sock 可用。
	DockerOK bool `json:"docker_ok"`
	// Error 是 docker_ok=false 时的**结论句**（页面直接显示它）；成功时必须为空。
	Error string `json:"error,omitempty"`
	// Compose 是 compose 形态探测结果（单一 flavor 纪律，§6.2）。
	Compose *DockerComposeInfo `json:"compose,omitempty"`
	// 五项清单**不带 omitempty**：空数组是「确实没有」的显式表达，前端不必区分
	//「字段缺席」与「空清单」；docker_ok=false 时它们必须全部为空（Validate 强制）。
	Containers []DockerContainer `json:"containers"`
	Images     []DockerImage     `json:"images"`
	Volumes    []DockerVolume    `json:"volumes"`
	Networks   []DockerNetwork   `json:"networks"`
	Projects   []DockerProject   `json:"projects"`
	// DiskUsage 是 system df 的磁盘占用汇总（六期 6a：磁盘治理）。**nil = 这帧没有
	// df 数据**：df 调用失败（采集侧退化 volume ls 的路径）或老版本 agent 不发该字段。
	// 页面把 nil 显示成「数据不可用」而不是 0 —— 「没有数据」与「没有占用」是两个
	// 相反的结论（与 DockerVolume.SizeMB=nil 同一条纪律）。
	//
	// 纯增量（带 omitempty）：老 agent 不发、老 core 收到不认得，形状漂移守卫
	// （TestShapeDriftAdditiveOnly）只放行省略形态。
	DiskUsage *DockerDiskUsage `json:"disk_usage,omitempty"`
}

// DockerDiskUsage 是 system df 的磁盘占用汇总（六期 6a：磁盘治理的最小事实面）。
//
// **只存汇总数字，不存逐项明细**：快照是 30s 一帧的常态上报，df 明细（逐镜像/逐卷/
// 逐条缓存的占用）已经存在于五类清单里（镜像/卷条目各自带 size_mb），这里再搬一遍
// 只会把每帧撑大一倍；「按明细排查」是列表页的事，按需另查（image:inspect / 卷页）。
//
// 字段口径全部来自 daemon 的 verbose df 响应（SDK v28 的 types.DiskUsage）：
// Images 是 []*image.Summary（Size/RepoTags），Volumes 是 []*volume.Volume
// （UsageData.Size，-1=未知），BuildCache 是 []*build.CacheRecord（Size）；
// daemon **不**给预聚合的 TotalSize/Reclaimable（那些类型已标记废弃）—— 汇总在
// agent 侧求和，口径见各字段注释。
type DockerDiskUsage struct {
	// ImagesTotalMB 是全部镜像的占用合计（Σ image.Size，MB）。含共享层的重复计入
	// —— 与 docker CLI 的 SIZE 列同口径（docker 自己也这么加，共享只在逐镜像视图提示）。
	ImagesTotalMB float64 `json:"images_total_mb"`
	// ImagesDanglingMB 是悬空镜像的占用合计（Σ 无标签镜像的 Size，MB）。悬空判据
	// 与镜像清单的 Dangling 同源（无标签 = daemon 悬空过滤器 = image:prune 的默认
	// 目标集合）：这个数字就是「能安全收回多少」的镜像半边，口径对不上会多报
	// 一份并不存在的空间（见 isDanglingImage 的实测论证）。
	ImagesDanglingMB float64 `json:"images_dangling_mb"`
	// VolumesTotalMB 是数据卷占用合计（Σ 已知体积，MB）。**已知体积的求和**：非
	// local 驱动不提供体积（UsageData.Size=-1），排除在求和之外 —— 该合计是下界，
	// 与 docker CLI 的合计同口径；逐卷的未知态由卷清单的 size_mb=nil 如实保留。
	VolumesTotalMB float64 `json:"volumes_total_mb"`
	// BuildCacheMB 是构建缓存占用合计（Σ cache.Size，MB）。协议**没有**构建缓存的
	// prune 动作（白名单不含 builder prune）：这个数字是「花在哪」的账面事实，
	// 不是「能收回」的承诺 —— 面板不得把它标成可回收。
	BuildCacheMB float64 `json:"build_cache_mb"`
}

// DockerComposeInfo 是 compose 形态与版本。
type DockerComposeInfo struct {
	// Flavor ∈ plugin | standalone-v2 | v1。
	Flavor string `json:"flavor"`
	// Version 形如 v2.27.0 / 1.29.2。
	Version string `json:"version,omitempty"`
}

// DockerContainer 是快照里的一个容器。
type DockerContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	// State 是 Docker 的机器态（running/exited/created/restarting/paused/dead）。
	State string `json:"state"`
	// StatusText 是 docker ps 的原生状态句（"Up 16 hours"），展示层直接用。
	StatusText string `json:"status_text,omitempty"`
	// Created / StartedAt 是 unix 秒；未启动的容器 StartedAt 为 0。
	Created   int64 `json:"created,omitempty"`
	StartedAt int64 `json:"started_at,omitempty"`
	// CPUPercent 是即时 CPU 占比（%）。取不到读数时为 0，展示层显示「—」。
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsageMB    float64 `json:"mem_usage_mb"`
	MemLimitMB    float64 `json:"mem_limit_mb"`
	NetRXBytesSec float64 `json:"net_rx_bytes_sec"`
	NetTXBytesSec float64 `json:"net_tx_bytes_sec"`
	// ComposeProject / ComposeService 来自 compose 标签；裸容器为空
	//（.105 上 8+ 个裸容器与一个 compose 项目并存，两种都要能被管理）。
	ComposeProject string       `json:"compose_project,omitempty"`
	ComposeService string       `json:"compose_service,omitempty"`
	Ports          []DockerPort `json:"ports,omitempty"`
	// Protected 是 agent 按 sys.docker.protected 算好的**结论**（前端不重复实现判断）。
	Protected bool `json:"protected"`
}

// DockerPort 是一条端口映射。
type DockerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	// Type ∈ tcp|udp|sctp。
	Type string `json:"type,omitempty"`
}

// DockerImage 是快照里的一个镜像。
type DockerImage struct {
	ID       string   `json:"id"`
	RepoTags []string `json:"repo_tags,omitempty"`
	SizeMB   float64  `json:"size_mb"`
	Created  int64    `json:"created,omitempty"`
	InUse    bool     `json:"in_use"`
	// Dangling 是 <none>:<none> 标签（prune 的主目标）。
	Dangling bool `json:"dangling"`
	// InUseBy 是使用它的容器名列表（安全清理的依据；名字比 id 对运维有意义）。
	InUseBy []string `json:"in_use_by,omitempty"`
}

// DockerVolume 是快照里的一个卷。
type DockerVolume struct {
	Name   string `json:"name"`
	Driver string `json:"driver,omitempty"`
	// SizeMB 未知时为 nil（体积取自 Docker 的磁盘用量统计，部分驱动不提供）。
	SizeMB    *float64 `json:"size_mb,omitempty"`
	InUse     bool     `json:"in_use"`
	MountedBy []string `json:"mounted_by,omitempty"`
	// Protected 与容器条目同义：agent 按 sys.docker.protected 的 `volume:<名>`
	// 粒度算好的**结论**（前端不重复实现判断；卷页删除据此提示「受保护」）。
	//
	// 与容器条目的 Protected 不同，这里带 omitempty：它是 **v1.2.2 新增的可选字段**，
	// 老 agent 不发、老 core 收到也不认得，都不影响（形状漂移守卫只放行省略形态）。
	Protected bool `json:"protected,omitempty"`
}

// DockerNetwork 是快照里的一个网络。
type DockerNetwork struct {
	Name            string `json:"name"`
	Driver          string `json:"driver,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Internal        bool   `json:"internal"`
	ContainersCount int    `json:"containers_count"`
}

// DockerProject 是一个 compose 项目（由容器标签归纳，不调 compose CLI 发现）。
type DockerProject struct {
	Name string `json:"name"`
	// ConfigFiles 来自 com.docker.compose.project.config_files 标签；
	// 老版本 compose 无此标签 → 显示「未知(旧版)」而不是编造路径。
	ConfigFiles []string `json:"config_files,omitempty"`
	// State ∈ running | partial | stopped（由成员容器的运行态归纳）。
	State           string `json:"state,omitempty"`
	Services        int    `json:"services"`
	ContainersCount int    `json:"containers_count"`
	// Protected 与容器条目同义：agent 按 sys.docker.protected 的 `project:<名>`
	// 粒度算好的**结论**（项目页据此显示 🔒）。
	//
	// 服务粒度（`project:<项目>/<服务>`）不在这里：它已由成员容器条目的
	// Protected 承载（ContainerProtected 会做「项目/服务」成对命中）。
	// 本字段带 omitempty，理由同 DockerVolume.Protected（v1.2.2 纯增量）。
	Protected bool `json:"protected,omitempty"`
}

// Validate 校验快照信封。
func (s *DockerState) Validate() error {
	if s.T <= 0 {
		return decodeErr(StagePayload, "t", ErrInvalidPayload)
	}
	if s.DockerOK {
		if s.Error != "" {
			return decodeErr(StagePayload, "error", ErrInvalidPayload)
		}
	} else {
		if s.Error == "" {
			return decodeErr(StagePayload, "error", ErrMissingField)
		}
		// 不可达时不得携带清单与 df：否则页面会一边说不可用、一边列出上一次的容器
		//（「最后已知状态」要由服务端按 ReceivedAt 标注陈旧度，不能伪装成刚刚的）。
		if len(s.Containers)+len(s.Images)+len(s.Volumes)+len(s.Networks)+len(s.Projects) != 0 ||
			s.DiskUsage != nil {
			return decodeErr(StagePayload, "containers", ErrInvalidPayload)
		}
	}
	for _, c := range []struct {
		field string
		n     int
	}{
		{"containers", len(s.Containers)}, {"images", len(s.Images)},
		{"volumes", len(s.Volumes)}, {"networks", len(s.Networks)}, {"projects", len(s.Projects)},
	} {
		if c.n > MaxDockerStateEntries {
			return decodeErr(StagePayload, c.field, ErrInvalidPayload)
		}
	}
	if len(s.Error) > MaxDockerStringBytes {
		return decodeErr(StagePayload, "error", ErrInvalidPayload)
	}
	for i := range s.Containers {
		if err := s.Containers[i].validate(); err != nil {
			return err
		}
	}
	for i := range s.Images {
		if err := s.Images[i].validate(); err != nil {
			return err
		}
	}
	for i := range s.Volumes {
		if err := s.Volumes[i].validate(); err != nil {
			return err
		}
	}
	for i := range s.Networks {
		if err := s.Networks[i].validate(); err != nil {
			return err
		}
	}
	for i := range s.Projects {
		if err := s.Projects[i].validate(); err != nil {
			return err
		}
	}
	if s.Compose != nil {
		if err := s.Compose.validate(); err != nil {
			return err
		}
	}
	if s.DiskUsage != nil {
		return s.DiskUsage.validate()
	}
	return nil
}

// validate 校验 df 汇总：负数是采集侧算错了（daemon 的 UsageData.Size=-1「未知」
// 哨兵必须在求和前被排除，溜进来会把「未知」渲染成负数占用），协议不传输无意义的值。
func (d *DockerDiskUsage) validate() error {
	if d.ImagesTotalMB < 0 || d.ImagesDanglingMB < 0 || d.VolumesTotalMB < 0 || d.BuildCacheMB < 0 {
		return decodeErr(StagePayload, "disk_usage", ErrInvalidPayload)
	}
	return nil
}

func (c *DockerComposeInfo) validate() error {
	switch c.Flavor {
	case DockerComposeFlavorPlugin, DockerComposeFlavorStandaloneV2, DockerComposeFlavorV1:
	default:
		return decodeErr(StagePayload, "compose.flavor", ErrInvalidPayload)
	}
	return nil
}

func (c *DockerContainer) validate() error {
	if c.ID == "" || c.Name == "" || c.Image == "" {
		return decodeErr(StagePayload, "containers", ErrMissingField)
	}
	if c.State == "" {
		return decodeErr(StagePayload, "containers.state", ErrMissingField)
	}
	if strTooLong(c.ID) || strTooLong(c.Name) || strTooLong(c.Image) || strTooLong(c.StatusText) {
		return decodeErr(StagePayload, "containers", ErrInvalidPayload)
	}
	return nil
}

func (i *DockerImage) validate() error {
	if i.ID == "" || i.SizeMB < 0 {
		return decodeErr(StagePayload, "images", ErrInvalidPayload)
	}
	if strTooLong(i.ID) {
		return decodeErr(StagePayload, "images", ErrInvalidPayload)
	}
	return nil
}

func (v *DockerVolume) validate() error {
	if !isDockerName(v.Name) {
		return decodeErr(StagePayload, "volumes.name", ErrInvalidPayload)
	}
	return nil
}

func (n *DockerNetwork) validate() error {
	if !isDockerName(n.Name) {
		return decodeErr(StagePayload, "networks.name", ErrInvalidPayload)
	}
	return nil
}

func (p *DockerProject) validate() error {
	if !IsDockerProjectName(p.Name) {
		return decodeErr(StagePayload, "projects.name", ErrInvalidPayload)
	}
	return nil
}

func strTooLong(s string) bool { return len(s) > MaxDockerStringBytes }

// ── 下行 1：core.docker.cmd ─────────────────────────────────────────────

// DockerCmdOptions 是一条指令的参数（§4.3.1 总表是 options 归属的唯一事实源：
// target 也在其中，而不是 DockerCmd 的一级字段）。
type DockerCmdOptions struct {
	// Target 是操作目标（容器名/镜像引用/卷名/网络名/项目名/「项目/服务」）。
	Target string `json:"target,omitempty"`
	// Tail 是 container:logs / compose:logs 的尾部行数（0 = 默认 100）。
	Tail int `json:"tail,omitempty"`
	// Since 是 container:logs 的起始时刻（unix 秒；0 = 全部）。compose:logs 不支持它
	//（CLI 公共子集没有 --since，见 ValidateDockerCmdOptions 的归属拒绝）。
	Since int64 `json:"since,omitempty"`
	// N 是 compose.service:scale 的目标实例数（指针：0 = 缩到零，是合法且高危的值）。
	N *int `json:"n,omitempty"`
	// Force 是保护目标/强制删除的显式覆盖位（保护档要求 docker:exec 级权限，见 §10）。
	Force bool `json:"force,omitempty"`
	// Filename 是 image:save/load 的 tar 文件名（**只传名，不传路径**）。
	Filename string `json:"filename,omitempty"`
	// Overwrite 是 image:save 目标已存在时用户确认覆盖后的重发标记。
	Overwrite bool `json:"overwrite,omitempty"`
	// All 是 image:prune 是否清理全部未使用镜像（默认仅悬空）。
	All bool `json:"all,omitempty"`
	// RemoveOrphans 是 compose:up 是否回收孤儿容器。
	RemoveOrphans bool `json:"remove_orphans,omitempty"`
	// Volumes 是 compose:down 是否连带删除卷。
	Volumes bool `json:"volumes,omitempty"`
	// Content 是 compose.file:write/validate 的文件全文。
	Content string `json:"content,omitempty"`
	// BaseHash 是 compose.file:write/patch 的乐观锁基线（内容 sha256，64 位小写 hex）。
	BaseHash string `json:"base_hash,omitempty"`
	// Src / Dst 是 image:tag 的源与目标引用。
	Src string `json:"src,omitempty"`
	Dst string `json:"dst,omitempty"`
	// Patch 是 compose.file:patch 的增量文档 {services,networks,volumes}，只含被改过的键。
	Patch map[string]any `json:"patch,omitempty"`

	// ── v1.2.3 纯增量（三期流通道）─────────────────────────────────────
	//
	// Follow 是 container:logs 的流式标志：false/缺省 = 一次性取（既有行为不变），
	// true = 走流会话（agent 侧 ContainerLogs(Follow:true) + 帧整形）。
	// compose:logs 复用同一字段与含义的**CLI 半边**：true = 给 compose CLI 传
	// --follow（会话长流）；false/缺省 = 不传（CLI 打完即退出，会话读到 eof 收摊）——
	// 两种形态都是流会话，没有一次性取回的形态。
	Follow bool `json:"follow,omitempty"`
	// Command 是 container:exec 的 argv；缺省 ["/bin/sh"]（空数组与缺席同义）。
	//
	// 直传 daemon 的 argv（**不经 shell**）：数组 ≤32 项、每项 ≤256B、元素非空、
	// 不得含 NUL 或换行（ValidateDockerCmdOptions）。
	Command []string `json:"command,omitempty"`

	// ── v1.2.4 纯增量（四期配置编辑）───────────────────────────────────

	// Backup 是 compose.file:write 的**回滚模式**：值是备份令牌（yyyyMMdd-HHmmss，
	// 形态严格校验，见 IsDockerBackupToken），带它就不需要 content。
	//
	// 协议上**永远不出现路径**（§8 路径白名单）：agent 自己把令牌拼成
	// `<配置文件>.bak-<令牌>`。与 content 互斥（都给不猜优先级，都缺无从下手）。
	Backup string `json:"backup,omitempty"`

	// ── 四支柱·创建面（container:create 专属，4a）───────────────────────
	//
	// 这一组字段**只属于 container:create**（照 backup 的归属纪律：字段在协议上的
	// 归属唯一 —— 挂在别的 action 上会被校验拒绝而不是无视）。逐字段白名单校验
	// 在 ValidateDockerCmdOptions，adapter 直传 daemon 不经 shell，但字段格式必须
	// 收敛：把格式自由摊给每台主机的 daemon 去判，判法会随版本漂移。
	//
	// Image 是要创建的镜像引用（必填，见 dockerActionSpecs 的 Required）。
	Image string `json:"image,omitempty"`
	// Name 是容器名（不填 = docker 自动起名）。合法字符集与容器名同规（isDockerName）。
	Name string `json:"name,omitempty"`
	// Ports 是端口映射列表，每条形态 `宿主:容器[/tcp|udp]`（协议缺省 = tcp）。
	// 宿主的 0/随机端口刻意不支持：随机分配会让「创建后去哪连」无从回答。
	Ports []string `json:"ports,omitempty"`
	// Env 是环境变量列表，每条形态 `KEY=VALUE`（键 = POSIX 标识符；值不含 NUL
	// 与换行、整条 ≤ MaxDockerStringBytes）。
	Env []string `json:"env,omitempty"`
	// Mounts 是卷挂载列表，每条形态 `源:目的地[:ro]`：源是命名卷（isDockerName）或
	// 绝对路径（bind mount），目的地必须绝对路径，模式只认 ro。
	//
	// 为什么叫 mounts 而不是 volumes：volumes 已被 compose:down 的布尔字段占用 ——
	// 同一协议里一字两义是比换个名字糟得多的选择。
	Mounts []string `json:"mounts,omitempty"`
	// RestartPolicy 是重启策略枚举：no / on-failure / always / unless-stopped。
	RestartPolicy string `json:"restart_policy,omitempty"`
	// CPULimit 是 CPU 配额（核数）：0 = 不限额，上限 maxDockerCPULimit。
	CPULimit float64 `json:"cpu_limit,omitempty"`
	// MemLimitMB 是内存上限（MB）：0 = 不限额，上限 maxDockerMemLimitMB。
	MemLimitMB int `json:"mem_limit_mb,omitempty"`
	// Network 是要接入的网络名（不填 = docker 默认网桥）。
	Network string `json:"network,omitempty"`
	// Start 表示创建后是否立即启动：缺省 = true（创建并启动）。指针：显式 false
	// 与缺席**不同义**（false = 只创建不启动），值类型会让它被缺省值悄悄覆盖。
	Start *bool `json:"start,omitempty"`

	// ── 4c 私有仓库凭据（image:pull 专属）─────────────────────────────────
	//
	// Registry 是要用凭据的仓库地址（**不含协议头与镜像路径**，见
	// IsDockerRegistryAddr）。它只是凭据库的唯一键、**不携带任何密钥材料** ——
	// 密码由 core 受理时解出、随指令瞬时注入 DockerCmd.Auth（选型 A）；
	// 缺省 = 与 4b 之前的拉取逐字一致（公共仓库或主机侧 docker login）。
	//
	// 字段归属纪律照 backup 的先例：只属于 image:pull，挂在别的 action 上会被
	// 校验拒绝而不是无视。
	Registry string `json:"registry,omitempty"`
}

// DockerCmd 是一条操作指令。
type DockerCmd struct {
	// Ref 是服务端生成的十进制指令号（雪花），串起「受理 → 执行 → 结果 → 轮询」。
	Ref     string           `json:"ref"`
	Action  string           `json:"action"`
	Options DockerCmdOptions `json:"options"`
	// Confirm 是强确认档的确认值（标准档为空）。服务端校验，agent 二次校验。
	Confirm string `json:"confirm,omitempty"`
	// Auth 是 core 随 image:pull 指令**瞬时注入**的仓库认证（4c，选型 A）：
	// 凭据库的密码只在受理这一刻解出、只随这一条消息去往要拉取的那台主机，
	// agent 执行完毕即弃 —— 任何路径不落盘、不进进度帧、不进 result。
	//
	// nil = 无凭据：与 4b 之前的拉取**逐字一致**（公共仓库或主机侧 docker login）。
	// 协议只允许它出现在 image:pull 上，且 registry 必须与 options.registry
	// 同一把键（防「用户要的凭据」与「下发的凭据」漂移，见 Validate）。
	Auth *DockerRegistryAuth `json:"auth,omitempty"`
}

// DockerRegistryAuth 是仓库认证三元组（4c）。字段与 SDK 的 AuthConfig 一一对应，
// 但**SDK 类型不出协议**：到 agent 后的映射在 adapter 内完成（见
// uni_agent/internal/dockerops 的 adapter.ImagePull）。
//
// Password 是**瞬时秘密**：只存在于 core→agent 的这条指令消息里
// （通道是 TLS 隧道，与 exec/写文件同一条），不进任何存储、日志、帧与结果。
type DockerRegistryAuth struct {
	// Registry 是仓库地址（与 options.registry 同一把键，不含协议头与路径）。
	Registry string `json:"registry"`
	// Username 是仓库用户名（登录名）。上限同快照字符串档。
	Username string `json:"username"`
	// Password 是仓库密码（明文瞬时值；上限 maxDockerRegistrySecretBytes）。
	Password string `json:"password"`
}

// Validate 校验仓库认证三元组（DockerCmd.Validate 在 Auth 非空时调用）。
func (a *DockerRegistryAuth) Validate() error {
	if a == nil {
		return nil
	}
	if !IsDockerRegistryAddr(a.Registry) {
		return decodeErr(StagePayload, "auth.registry", ErrInvalidPayload)
	}
	if a.Username == "" || len(a.Username) > MaxDockerStringBytes {
		return decodeErr(StagePayload, "auth.username", ErrInvalidPayload)
	}
	if a.Password == "" || len(a.Password) > maxDockerRegistrySecretBytes {
		return decodeErr(StagePayload, "auth.password", ErrInvalidPayload)
	}
	return nil
}

// Validate 校验指令。
func (c *DockerCmd) Validate() error {
	if !isDecimalID(c.Ref) {
		return decodeErr(StagePayload, "ref", ErrMissingField)
	}
	if err := ValidateDockerCmdOptions(c.Action, &c.Options); err != nil {
		return err
	}
	// 4c：认证三元组只许挂在 image:pull 上（与 options.registry 的归属纪律一致）；
	// 且两个 registry 必须是同一把键 —— 漂移意味着「用户要的凭据」与「下发的凭据」
	// 不是同一份，agent 宁可判非法执行，也不拿着错凭据去 daemon 上试一遍。
	if c.Auth != nil {
		if c.Action != DockerActionImagePull {
			return decodeErr(StagePayload, "auth", ErrInvalidPayload)
		}
		if err := c.Auth.Validate(); err != nil {
			return err
		}
		if c.Options.Registry != c.Auth.Registry {
			return decodeErr(StagePayload, "auth.registry", ErrInvalidPayload)
		}
	}
	return nil
}

// ── 上行 2：agent.docker.result ─────────────────────────────────────────

// DockerCmdResult 是一条指令的执行结果。
type DockerCmdResult struct {
	Ref string `json:"ref"`
	OK  bool   `json:"ok"`
	// Error 是 ok=false 时的**结论句**（页面直接显示它）。
	Error string `json:"error,omitempty"`
	// Detail 是给人排障看的原始细节（stderr 片段、HTTP 码），≤2KB，落库但不渲染。
	Detail string `json:"detail,omitempty"`
	// SessionID 是建立流会话的指令回的会话句柄（一期只读 action 不产生它）。
	SessionID string `json:"session_id,omitempty"`
	// AlreadyExists 是 image:save 的「目标文件已存在」标志：UI 确认覆盖后带
	// overwrite=true 重发（两段确认复用 cmd/result，§7.5）。
	AlreadyExists bool `json:"already_exists,omitempty"`
	// Payload 是一期只读 action 的**结果数据**（日志文本 / inspect 数据 / yml 全文）。
	//
	// 为什么不让它们各自成一条消息：它们的生命周期与 result 完全一致（一次指令一个结果），
	// 单独的消息类型只会多几套 ref 关联与状态机；而 detail 是排障线索（2KB 截断），
	// 语义上不是数据通道。
	Payload []byte `json:"payload,omitempty"`
}

// Validate 校验结果。
func (r *DockerCmdResult) Validate() error {
	if !isDecimalID(r.Ref) {
		return decodeErr(StagePayload, "ref", ErrMissingField)
	}
	if r.OK {
		if r.Error != "" {
			return decodeErr(StagePayload, "error", ErrInvalidPayload)
		}
		if r.AlreadyExists {
			// 成功却带「文件已存在」：自相矛盾。放任它会让前端在成功的行上显示覆盖提示。
			return decodeErr(StagePayload, "already_exists", ErrInvalidPayload)
		}
		if len(r.Payload) > MaxDockerPayloadBytes {
			return decodeErr(StagePayload, "payload", ErrInvalidPayload)
		}
	} else {
		if r.Error == "" {
			return decodeErr(StagePayload, "error", ErrMissingField)
		}
		if len(r.Payload) != 0 {
			return decodeErr(StagePayload, "payload", ErrInvalidPayload)
		}
	}
	if len(r.Detail) > MaxDockerResultDetailBytes {
		return decodeErr(StagePayload, "detail", ErrInvalidPayload)
	}
	if r.SessionID != "" && !IsDockerSessionID(r.SessionID) {
		return decodeErr(StagePayload, "session_id", ErrInvalidPayload)
	}
	return nil
}

// ── 只读 action 的结果载荷 ──────────────────────────────────────────────

// DockerLogsPayload 是 container:logs 的结果数据。
type DockerLogsPayload struct {
	// Lines 是日志文本（stdout+stderr 合并，行尾 \n）。
	Lines string `json:"lines"`
	// Truncated 表示因行数上限被截断（页面只说「仅显示最近 N 行」）。
	Truncated bool `json:"truncated,omitempty"`
}

// DockerContainerInspectPayload 是 container:inspect 的结果数据（容器详情页的环境变量/配置 Tab）。
type DockerContainerInspectPayload struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	ImageID       string `json:"image_id,omitempty"`
	Created       int64  `json:"created,omitempty"`
	State         string `json:"state"`
	StartedAt     int64  `json:"started_at,omitempty"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	RestartPolicy string `json:"restart_policy,omitempty"`
	// Env 原样回传（管理员视图）：它常含口令类变量，故这一 Tab 的权限是 docker:inspect
	// 且页面明确标注「含敏感值」——不做掩码是因为掩码会让「这个变量到底配了什么」无从核对。
	Env        []string          `json:"env,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Ports      []DockerPort      `json:"ports,omitempty"`
	Mounts     []DockerMount     `json:"mounts,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Cmd        []string          `json:"cmd,omitempty"`
	// Health 是健康检查现状（无 healthcheck 时为空）。
	Health   string   `json:"health,omitempty"`
	Networks []string `json:"networks,omitempty"`
}

// DockerMount 是一条挂载。
type DockerMount struct {
	Type        string `json:"type,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	Mode        string `json:"mode,omitempty"`
	RW          bool   `json:"rw"`
}

// DockerImageInspectPayload 是 image:inspect 的结果数据（镜像详情页三个 Tab）。
type DockerImageInspectPayload struct {
	ID           string            `json:"id"`
	RepoTags     []string          `json:"repo_tags,omitempty"`
	SizeBytes    int64             `json:"size_bytes"`
	Created      int64             `json:"created,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	OS           string            `json:"os,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Env          []string          `json:"env,omitempty"`
	ExposedPorts []string          `json:"exposed_ports,omitempty"`
	Entrypoint   []string          `json:"entrypoint,omitempty"`
	Cmd          []string          `json:"cmd,omitempty"`
	// History 是分层历史，**自下而上**（与 docker history 的默认顺序相反 ——
	// 页面的阅读顺序是「基础层 → 增量层」，见 §11.4 草图）。
	History []DockerImageLayer `json:"history,omitempty"`
}

// DockerImageLayer 是镜像的一层。
type DockerImageLayer struct {
	SizeBytes int64  `json:"size_bytes"`
	Created   int64  `json:"created,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	// EmptyLayer 表示元数据层（ENV/CMD 这类不占空间、无对应文件系统层的指令）。
	EmptyLayer bool   `json:"empty_layer,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// DockerComposeFilePayload 是 compose.file:read 的结果数据。
//
// v1.2.4 起它也是 compose.file:write/patch 成功后的结果数据：保存收尾以 agent 回读的
// 内容为准（§8 的唯一事实源），前端据此刷新基线 hash 与备份历史。
type DockerComposeFilePayload struct {
	Content string `json:"content"`
	// Hash 是内容的 sha256（64 位小写 hex）：四期写路径的乐观锁基线，一期只读取它
	// 是为了让「读到的内容」与「保存时的基线」同源。
	Hash string `json:"hash"`
	// Path 是 agent 从 compose 标签解析出的真实文件路径（只读展示用；
	// 协议上从不接受路径入参 —— 路径白名单纪律，§8 四条防护之一）。
	Path string `json:"path,omitempty"`
	// Backups 是备份历史（最多 10 份，按时间倒序）。**v1.2.4 纯增量**：老 agent
	// 不发、老 core/前端忽略即可（形状漂移守卫只放行省略形态）。
	Backups []DockerComposeBackup `json:"backups,omitempty"`
}

// DockerComposeBackup 是一份配置备份（v1.2.4）。
//
// 它是**展示 + 回滚选择**的数据：token 回填给 compose.file:write 的 backup，
// 其余供「历史备份」视图展示与 diff。
type DockerComposeBackup struct {
	// Token 是令牌（yyyyMMdd-HHmmss）：agent 据此重建 <文件>.bak-<令牌>。
	Token string `json:"token"`
	// Hash 是该备份内容的 sha256（64 位小写 hex）——回滚前后比对「内容与备份一致」。
	Hash string `json:"hash"`
	// SizeBytes 是备份文件字节数（前端显示体积；也是 1MB 上限的事实核对点）。
	SizeBytes int64 `json:"size_bytes"`
	// At 是备份时刻（unix 秒）。
	At int64 `json:"at"`
}

// ── 流：上行数据帧 / 下行控制帧（一期只登记，不接线）─────────────────────

// DockerFrame 是流会话的一帧数据（日志 chunk 与 PTY 输出共用）。
//
// Data 用 []byte：JSON 编码自动得到 base64，PTY 的二进制控制序列与非 UTF-8 日志
// 不会被字符串编码悄悄替换成 U+FFFD（换成 string 就会，且无损不了）。
type DockerFrame struct {
	SessionID string `json:"session_id"`
	// Seq 是会话内单调递增序号（从 1 开始），供消费端检测缺口。
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data,omitempty"`
	// EOF 表示本会话的数据已发完（可与最后一帧数据同帧）。
	EOF bool `json:"eof,omitempty"`
}

// CoreDockerFrame 是流会话的控制帧。
type CoreDockerFrame struct {
	SessionID string `json:"session_id"`
	// Op ∈ input | resize | cancel。
	Op   string `json:"op"`
	Data []byte `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// Validate 校验上行数据帧。
func (f *DockerFrame) Validate() error {
	if !IsDockerSessionID(f.SessionID) {
		return decodeErr(StagePayload, "session_id", ErrInvalidPayload)
	}
	if f.Seq == 0 {
		return decodeErr(StagePayload, "seq", ErrMissingField)
	}
	if len(f.Data) > MaxDockerFrameDataBytes {
		return decodeErr(StagePayload, "data", ErrInvalidPayload)
	}
	return nil
}

// Validate 校验下行控制帧。
func (f *CoreDockerFrame) Validate() error {
	if !IsDockerSessionID(f.SessionID) {
		return decodeErr(StagePayload, "session_id", ErrInvalidPayload)
	}
	switch f.Op {
	case DockerFrameOpInput:
		if len(f.Data) == 0 {
			return decodeErr(StagePayload, "data", ErrMissingField)
		}
		if len(f.Data) > MaxDockerFrameDataBytes {
			return decodeErr(StagePayload, "data", ErrInvalidPayload)
		}
		if f.Cols != 0 || f.Rows != 0 {
			return decodeErr(StagePayload, "cols", ErrInvalidPayload)
		}
	case DockerFrameOpResize:
		if f.Cols <= 0 || f.Rows <= 0 || f.Cols > maxDockerTermSize || f.Rows > maxDockerTermSize {
			return decodeErr(StagePayload, "cols", ErrMissingField)
		}
	case DockerFrameOpCancel:
		if len(f.Data) != 0 {
			return decodeErr(StagePayload, "data", ErrInvalidPayload)
		}
	default:
		return decodeErr(StagePayload, "op", ErrInvalidPayload)
	}
	return nil
}

// ── stats 流的样本载荷（监控面）───────────────────────────────────────────

// DockerStatsSample 是 stats 流会话的一个采样点：agent 把每个样本编码成一条 JSON
// 放进帧的 Data（一条样本一行，行尾换行 —— 一帧可以装多条），core 的 stats 端点
// 逐行解码成字段转发给前端。它**不自成一条协议消息**：样本与帧共用生命周期
// （session_id / seq / eof），另起消息类型只会多一套会话管理。
//
// 字段与快照 DockerContainer 的同名字段**同一口径**（名字、单位、round2 舍入一致）：
// 抽屉里的实时曲线与表格里的快照读数必须对得上 —— 两个口径会变成「曲线一条线、
// 表格一个数」的永久疑问。差异只有两点：T 是采样时刻（曲线的 x 轴）；网络字段是
// **速率**（相邻样本求差，与快照采集同一算法）而不是累计值。
type DockerStatsSample struct {
	// T 是采样时刻（unix 毫秒，agent 时钟）。它不参与陈旧度判定（agent 时钟可能偏），
	// 只是曲线的 x 轴刻度，相邻样本的差才是节奏。
	T int64 `json:"t"`
	// CPUPercent 是 CPU 占比（%，round2）。与快照 cpu_percent 同一算法
	//（cpu_stats/precpu_stats 差值，docker CLI 同款）。
	CPUPercent float64 `json:"cpu_percent"`
	// MemUsageMB / MemLimitMB 是内存用量与上限（MB，round2）；不设上限时 Limit 为 0。
	MemUsageMB float64 `json:"mem_usage_mb"`
	MemLimitMB float64 `json:"mem_limit_mb"`
	// NetRXBytesSec / NetTXBytesSec 是网络速率（B/s）：相邻样本的累计计数求差。
	// **首样本为 0**（没有可求差的基线）—— 与 docker stats CLI 首行同口径。
	NetRXBytesSec float64 `json:"net_rx_bytes_sec"`
	NetTXBytesSec float64 `json:"net_tx_bytes_sec"`
}

// Validate 校验样本。
//
// 全部读数都不允许为负：负数是采集侧算错了（计数回绕、负间隔都必须被钳成 0），
// 放行它会让前端画出一条「负数流量/负内存」的曲线 —— 协议上不传输无意义的值。
func (s *DockerStatsSample) Validate() error {
	if s.T <= 0 {
		return decodeErr(StagePayload, "t", ErrInvalidPayload)
	}
	if s.CPUPercent < 0 || s.MemUsageMB < 0 || s.MemLimitMB < 0 ||
		s.NetRXBytesSec < 0 || s.NetTXBytesSec < 0 {
		return decodeErr(StagePayload, "stats", ErrInvalidPayload)
	}
	return nil
}

// ── 拉取进度的记录载荷（image:pull，4b 进度面）─────────────────────────────

// DockerPullProgressItem 是 image:pull 进度流会话的一个记录点：agent 把 daemon 的
// JSON 进度行折成本域字段、按窗口折叠后编码成一条 JSON 行放进帧 data（一行一条，
// 行尾换行）—— 与 DockerStatsSample 同一形态纪律：**不自成一条协议消息**，样本与
// 帧共用生命周期（session_id / seq / eof）。
//
// 字段口径照 daemon 的 jsonmessage 子集（仅进度面）：ID 是层 id（仓库层 digest）或
// 阶段标识；Status 是原文状态文案（"Pulling fs layer"/"Downloading"/"Extracting"…）；
// Current/Total 是该层的已传输/总字节（progressDetail，未知时两者为 0）；
// Done=true 是**终态项**（拉取成功，流的最后一条）；Error 是终态项的失败结论
// （daemon 的 errorDetail.message 原文）。终态项恰一条、恰在最后。
type DockerPullProgressItem struct {
	// T 是该记录点的挂钟（unix 毫秒，agent 时钟）。与 stats 样本同口径：只作时间轴
	// 刻度，不参与陈旧度判定。
	T int64 `json:"t"`
	// ID 是层 id / 阶段标识；消息类记录（"Pulling from …" 等没有层的行）留空。
	ID string `json:"id,omitempty"`
	// Status 是原文状态文案（页面直接显示）。
	Status string `json:"status,omitempty"`
	// Current / Total 是该层已传输与总字节；未知时为 0（daemon 的 progressDetail 缺席）。
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
	// Done 是成功的终态项（恰在流末一条）。
	Done bool `json:"done,omitempty"`
	// Error 是失败的终态项（恰在流末一条；内容为 daemon 的原始错误消息）。
	Error string `json:"error,omitempty"`
}

// Validate 校验一条拉取进度记录。
func (p *DockerPullProgressItem) Validate() error {
	if p.T <= 0 {
		return decodeErr(StagePayload, "t", ErrInvalidPayload)
	}
	if p.Done && p.Error != "" {
		// 同时带两个终态标记：采集侧状态机算错了，放行会让前端弹一对相反的结论。
		return decodeErr(StagePayload, "pull", ErrInvalidPayload)
	}
	if p.Current < 0 || p.Total < 0 || (p.Total > 0 && p.Current > p.Total) {
		return decodeErr(StagePayload, "pull", ErrInvalidPayload)
	}
	if strTooLong(p.ID) || strTooLong(p.Status) || strTooLong(p.Error) {
		return decodeErr(StagePayload, "pull", ErrInvalidPayload)
	}
	return nil
}

// ── events 流的记录载荷（活动流，六期·监控面）─────────────────────────────

// DockerEventType* 是事件订阅范围的四类资源。agent 把 daemon 的 Events API filter 到
// 这四个值 —— builder/plugin/service/secret 等 swarm 与构建类噪音**根本不下发**，
// core 与前端就不必各自写一份「哪些类型算数」的过滤。
const (
	DockerEventTypeContainer = "container"
	DockerEventTypeImage     = "image"
	DockerEventTypeVolume    = "volume"
	DockerEventTypeNetwork   = "network"
)

// IsDockerEventType 报告 t 是否在订阅范围内。
func IsDockerEventType(t string) bool {
	switch t {
	case DockerEventTypeContainer, DockerEventTypeImage, DockerEventTypeVolume, DockerEventTypeNetwork:
		return true
	}
	return false
}

// dockerEventActions 是事件动作的**前缀**白名单。
//
// 为什么按前缀而不是全串枚举：daemon 的 exec 系动作在线上形态带命令后缀
// （"exec_create: /bin/sh -c ls"），全串枚举会把每一个合法动作拆成无穷多份；
// 校验取「首个 ": " 之前的词元」∈ 集合 —— 「谁在说话」是协议的判断，说话的内容
// 是数据。集合照 `docker system events` 文档的动作全集收敛到本订阅范围会用到的子集。
var dockerEventActions = map[string]bool{
	"attach": true, "commit": true, "copy": true, "create": true, "destroy": true,
	"detach": true, "die": true, "exec_create": true, "exec_detach": true,
	"exec_die": true, "exec_start": true, "enable": true, "disable": true,
	"export": true, "health_status": true, "import": true, "kill": true,
	"load": true, "mount": true, "oom": true, "pause": true, "pull": true,
	"push": true, "reload": true, "remove": true, "rename": true, "resize": true,
	"restart": true, "save": true, "start": true, "stop": true, "tag": true,
	"top": true, "unmount": true, "unpause": true, "update": true,
}

// DockerEventItem 是事件流的一条记录（活动流的最小单元）。
//
// 与 DockerStatsSample 同一形态纪律：**不自成一条协议消息**，agent 把它编码成 JSON
// 放进帧的 data（一条一行，行尾换行），core 逐行解码、校验后注入 hostId/hostname
// 再转发。字段只取展示与归因需要的四样 —— daemon 事件里的 scope、全量 attributes
// 直接透传会被各版本的字段漂移带进前端，也会让载荷被无关内容撑大。
type DockerEventItem struct {
	// T 是 agent 采到该时刻的挂钟（unix 毫秒）。口径同 stats 样本的 T：
	// 参与陈旧度判定的是 core 的**接收时刻**（agent 时钟可能偏），T 只是时间轴刻度。
	T int64 `json:"t"`
	// Type ∈ container|image|volume|network（订阅范围，见 IsDockerEventType）。
	Type string `json:"type"`
	// Action 是事件动作（start/die/destroy/…；带后缀的 exec 系按前缀判定，
	// 见 dockerEventActions 的说明）。
	Action string `json:"action"`
	// ActorName 是动作主体名：容器/卷/网络名或镜像引用（daemon Actor.Attributes["name"]）。
	ActorName string `json:"actor_name,omitempty"`
	// ActorID 是主体短 id（daemon Actor.ID；镜像为 sha256:<hex> 形态）。
	ActorID string `json:"actor_id,omitempty"`
}

// Validate 校验一条事件记录。
//
// 上限复用 MaxDockerStringBytes（快照字符串上限）：主体名最长的是镜像引用，
// 与快照条目的名字字段同档即可 —— 更大的值要么是字段漂移要么是异常数据。
func (e *DockerEventItem) Validate() error {
	if e.T <= 0 {
		return decodeErr(StagePayload, "t", ErrInvalidPayload)
	}
	if !IsDockerEventType(e.Type) {
		return decodeErr(StagePayload, "type", ErrInvalidPayload)
	}
	token := e.Action
	if i := strings.Index(e.Action, ": "); i >= 0 {
		token = e.Action[:i]
	}
	if token == "" || !dockerEventActions[token] {
		return decodeErr(StagePayload, "action", ErrInvalidPayload)
	}
	if strTooLong(e.ActorName) || strTooLong(e.ActorID) {
		return decodeErr(StagePayload, "actor_name", ErrInvalidPayload)
	}
	return nil
}

// ── hello_ack 的 docker 配置块（§3.1.1）──────────────────────────────────

// DockerConfig 是 sys.docker.* 三键下发给 agent 的快照（经 hello_ack，**重连时生效**）。
//
// 为什么放在 hello_ack 而不是新开一条消息：它与 ReportInterval 是同一个生效模型
// （握手时下发、下次重连才对账），复用一个窗口比新造一条「订阅-推送」通道小得多；
// 而且离线设备一上线就自动拿到最新值，服务端不必记得「哪些设备还没收到」。
type DockerConfig struct {
	// ConfigVersion 单调递增：agent 与本地持久化的版本号比对，判断「配置变了」。
	//
	// 用计数器而不是配置值的哈希：值哈希感知不到「先改 A、又改回 A」这种回摆，
	// 而那种情况恰好是运维最需要看到「配置确实动过」的场景。
	ConfigVersion uint64 `json:"config_version"`
	// Protected 是受保护目标清单（csv，四种粒度：容器名 / project:名 /
	// project:名/服务 / volume:名）。
	Protected string `json:"protected,omitempty"`
	// TransferDir 是 image:save/load 产物的目录根（agent 只接受文件名，自己拼路径）。
	TransferDir string `json:"transfer_dir,omitempty"`
	// SnapshotInterval 是快照周期（秒），最小 10。
	SnapshotInterval int `json:"snapshot_interval,omitempty"`
}

// Validate 校验下行配置块。
func (c *DockerConfig) Validate() error {
	if c.SnapshotInterval != 0 && c.SnapshotInterval < 10 {
		return decodeErr(StagePayload, "snapshot_interval", ErrInvalidPayload)
	}
	if len(c.Protected) > 4096 {
		return decodeErr(StagePayload, "protected", ErrInvalidPayload)
	}
	if c.TransferDir != "" {
		if !strings.HasPrefix(c.TransferDir, "/") || strings.ContainsRune(c.TransferDir, 0) {
			return decodeErr(StagePayload, "transfer_dir", ErrInvalidPayload)
		}
		if len(c.TransferDir) > 1024 {
			return decodeErr(StagePayload, "transfer_dir", ErrInvalidPayload)
		}
	}
	return nil
}
