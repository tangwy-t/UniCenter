package agentproto

import (
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
	DockerActionContainerStart   = "container:start"
	DockerActionContainerStop    = "container:stop"
	DockerActionContainerRestart = "container:restart"
	DockerActionContainerRemove  = "container:remove"
	DockerActionContainerLogs    = "container:logs"
	DockerActionContainerInspect = "container:inspect"
	DockerActionContainerExec    = "container:exec"

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
)

// DockerActionSpec 是 action 的静态属性中**两端都必须一致**的那部分。
//
// 权限码、超时、期次不在协议里：那是 core 的策略（internal/pkg/dockerpolicy），
// 协议只管「一条消息怎么被双方同样地理解」。
type DockerActionSpec struct {
	Action string
	// Required 是必须有值的 options 字段名（措辞与 JSON tag 一致，供 400 提示直接引用）。
	Required []string
	// Confirm 是确认档形态：""（标准档）/ DockerConfirmDelete / DockerConfirmTarget /
	// DockerConfirmFilename。取值来源由 ExpectedDockerConfirm 统一给出。
	Confirm string
}

// dockerActionSpecs 是白名单的**唯一枚举源**（顺序 = §3.3 书写顺序）。
var dockerActionSpecs = []DockerActionSpec{
	{Action: DockerActionContainerStart, Required: []string{"target"}},
	{Action: DockerActionContainerStop, Required: []string{"target"}},
	{Action: DockerActionContainerRestart, Required: []string{"target"}},
	// 单删容器是**标准档**（docker:delete 即可）：protected 目标才升级到保护档（force）。
	{Action: DockerActionContainerRemove, Required: []string{"target"}},
	{Action: DockerActionContainerLogs, Required: []string{"target"}},
	{Action: DockerActionContainerInspect, Required: []string{"target"}},
	{Action: DockerActionContainerExec, Required: []string{"target"}},

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
	{Action: DockerActionComposeFileWrite, Required: []string{"target", "content", "base_hash"}, Confirm: DockerConfirmTarget},
	{Action: DockerActionComposeFileValidate, Required: []string{"target", "content"}},
	{Action: DockerActionComposeFilePatch, Required: []string{"target", "base_hash", "patch"}, Confirm: DockerConfirmTarget},
}

// dockerOptionFields 是 DockerCmdOptions 的全部字段名（与 JSON tag 一一对应）。
var dockerOptionFields = []string{
	"target", "tail", "since", "n", "force", "filename", "overwrite", "all",
	"remove_orphans", "volumes", "content", "base_hash", "src", "dst", "patch",
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
)

// IsDockerProjectName 报告 s 是否是合法的 compose 项目名。
func IsDockerProjectName(s string) bool { return s != "" && dockerProjectNameRe.MatchString(s) }

// IsDockerTarFilename 报告 s 是否是合法的 tar 文件名（image:save/load 的唯一文件名口径）。
func IsDockerTarFilename(s string) bool { return s != "" && dockerTarFilenameRe.MatchString(s) }

// IsDockerImageRef 报告 s 是否是合法的镜像引用（含 digest 形态）。
func IsDockerImageRef(s string) bool { return s != "" && dockerImageRefRe.MatchString(s) }

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
	if o.N != nil && (*o.N < 0 || *o.N > maxDockerScaleN) {
		return decodeErr(StagePayload, "n", ErrInvalidPayload)
	}
	if len(o.Content) > MaxDockerComposeFileBytes {
		return decodeErr(StagePayload, "content", ErrInvalidPayload)
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
		if o == nil {
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
		// 不可达时不得携带清单：否则页面会一边说不可用、一边列出上一次的容器
		//（「最后已知状态」要由服务端按 ReceivedAt 标注陈旧度，不能伪装成刚刚的）。
		if len(s.Containers)+len(s.Images)+len(s.Volumes)+len(s.Networks)+len(s.Projects) != 0 {
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
		return s.Compose.validate()
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
	// Tail 是 container:logs 的尾部行数（0 = 默认 100）。
	Tail int `json:"tail,omitempty"`
	// Since 是 container:logs 的起始时刻（unix 秒；0 = 全部）。
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
}

// DockerCmd 是一条操作指令。
type DockerCmd struct {
	// Ref 是服务端生成的十进制指令号（雪花），串起「受理 → 执行 → 结果 → 轮询」。
	Ref     string           `json:"ref"`
	Action  string           `json:"action"`
	Options DockerCmdOptions `json:"options"`
	// Confirm 是强确认档的确认值（标准档为空）。服务端校验，agent 二次校验。
	Confirm string `json:"confirm,omitempty"`
}

// Validate 校验指令。
func (c *DockerCmd) Validate() error {
	if !isDecimalID(c.Ref) {
		return decodeErr(StagePayload, "ref", ErrMissingField)
	}
	return ValidateDockerCmdOptions(c.Action, &c.Options)
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
type DockerComposeFilePayload struct {
	Content string `json:"content"`
	// Hash 是内容的 sha256（64 位小写 hex）：四期写路径的乐观锁基线，一期只读取它
	// 是为了让「读到的内容」与「保存时的基线」同源。
	Hash string `json:"hash"`
	// Path 是 agent 从 compose 标签解析出的真实文件路径（只读展示用；
	// 协议上从不接受路径入参 —— 路径白名单纪律，§8 四条防护之一）。
	Path string `json:"path,omitempty"`
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
