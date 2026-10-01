// Package dockerops 是 agent 侧的 Docker 操作面：资源快照采集、指令执行、流会话。
//
// 三条边界纪律：
//  1. **只有 adapter.go / compose.go 碰 SDK 与进程** —— 其余文件只依赖 DockerAPI 窄接口，
//     因为 CI 里没有 docker daemon，可测性全靠这条边界（spec §12.1「CI 盲区处置」）。
//  2. **协议上从不接受路径入参**：UI 只传项目名/文件名，路径由本包自己拼（§8 路径白名单）。
//  3. 面向用户的错误一律是**结论句**（中文、不含英文原文与内部术语）；原始细节走 Detail。
package dockerops

import (
	"context"
	"errors"
	"io"
)

// ── 本域数据类型（**不是** SDK 类型的别名）──────────────────────────────
//
// 为什么要再定义一套而不是直接用 SDK 的结构：SDK 的字段随版本增删，直接暴露会让
// 采集与执行逻辑被 SDK 升级牵动；本域类型只有我们真正用到的字段，且是「快照长什么样」
// 这个问题的直接答案。

// ContainerInfo 是容器列表项。
type ContainerInfo struct {
	ID      string
	Name    string // 已去掉 Docker 加的前导 '/'
	Image   string
	ImageID string
	State   string
	Status  string
	Created int64 // unix 秒
	Labels  map[string]string
	Ports   []PortInfo
	Mounts  []MountInfo
}

// PortInfo 是一条端口映射。
type PortInfo struct {
	IP      string
	Private int
	Public  int
	Type    string
}

// MountInfo 是一条挂载。
type MountInfo struct {
	Type        string
	Source      string
	Destination string
	Mode        string
	RW          bool
}

// StatsInfo 是容器的一次即时读数。
type StatsInfo struct {
	CPUPercent float64
	MemUsageMB float64
	MemLimitMB float64
	// NetRXBytes / NetTXBytes 是**自容器启动以来的累计值**：一次读数里没有「速率」
	// 这个量，速率只能由采集侧对两次读数求差（与 collect 包的网卡速率同一做法）。
	NetRXBytes uint64
	NetTXBytes uint64
}

// StatsSample 是 stats 流的一个采样点。
//
// 与 one-shot 的 StatsInfo **同口径计算**（同一套 cpuPercent/mb/sumNet 函数），
// 差异只在网络字段是**速率**（B/s）：流内相邻样本的累计计数求差。首样本的速率为 0
// （没有可求差的基线）—— 与 docker stats CLI 首行同口径。类型分开而不是在 StatsInfo
// 上加字段：一次读数的累计值与流的速率是两个语义，混在一个结构里会让两者都被误解。
type StatsSample struct {
	CPUPercent    float64
	MemUsageMB    float64
	MemLimitMB    float64
	NetRXBytesSec float64
	NetTXBytesSec float64
}

// EventItem 是 docker events 流的一条记录（订阅范围内：container/image/volume/network）。
//
// 与 StatsSample 同型：它是「流里的记录」而不是 SDK 类型别名（SDK 的 Message 带
// scope/全量 attributes，直透会牵动执行逻辑）；字段只有展示与归因需要的四样，
// 类型/动作的过滤**在 adapter 的订阅 filter 里就做掉**（daemon 侧过滤，agent 不还载
// swarm 与构建类的噪音）。T 不在其中 —— 与 stats 样本同纪律：时刻取会话时钟的
// 采集时刻，不信任 daemon 的挂钟。
type EventItem struct {
	Type      string
	Action    string
	ActorName string
	ActorID   string
	// ExitCode 是 die 事件的退出码（daemon Actor.Attributes["exitCode"]；只有 die
	// 事件会带上）。为什么事件流要专门带它：core 侧的通知联动按「exit ≠ 0 才算
	// 异常退出」过滤 —— 正常的 docker stop 收尾成 exit 0 的 die，不该吵醒任何
	// 人。nil = 不可考（非 die / daemon 没给 / 解析失败），core 侧不告警而不是猜测。
	ExitCode *int32
}

// ImageInfo 是镜像列表项。
type ImageInfo struct {
	ID        string
	RepoTags  []string
	SizeBytes int64
	Created   int64
}

// ── 创建面（container:create）的本域参数 ──────────────────────────────────
//
// 协议 options 里的一切字符串（端口 `8080:80/tcp`、挂载 `data:/db:ro`）都由
// **执行器**解析成这里的最终值：adapter 只见定型的字段，不必再把解析逻辑带进
// SDK 映射面（包边界纪律：SDK 类型不出 adapter）。

// ContainerCreateSpec 是创建容器的全部参数。
type ContainerCreateSpec struct {
	Image string
	Name  string
	// Env 是 KEY=VALUE 形态的环境变量（解析**不变形**：协议校验过的整条原样交出）。
	Env []string
	// Ports 是逐条解析好的端口绑定（协议默认 tcp）。
	Ports []CreatePortBinding
	// Mounts 是逐条解析好的挂载（源已定性为命名卷或宿主路径）。
	Mounts []CreateMount
	// RestartPolicy 是重启策略枚举值（no/on-failure/always/unless-stopped；"" = 不设）。
	RestartPolicy string
	// CPULimit 是核数（0 = 不限额）。
	CPULimit float64
	// MemLimitMB 是内存上限（MB；0 = 不限额）。
	MemLimitMB int
	// Network 是要接入的网络名（"" = docker 默认网桥）。
	Network string
}

// CreatePortBinding 是一条已解析的端口映射。
type CreatePortBinding struct {
	Host      int // 宿主端口
	Container int // 容器端口
	// Proto ∈ tcp | udp。
	Proto string
}

// CreateMount 是一条已解析的挂载。
type CreateMount struct {
	// Bind 为 true 时 Source 是宿主绝对路径；false 时是命名卷名
	//（与 docker CLI 同一分辨规则：以 '/' 开头的源是 bind）。
	Bind   bool
	Source string
	Dest   string
	// ReadOnly 由协议形态里的 `:ro` 表达。
	ReadOnly bool
}

// VolumeInfo 是卷列表项。
type VolumeInfo struct {
	Name   string
	Driver string
	// SizeBytes 为 nil 表示**用量未知**（部分驱动不提供），页面显示「—」而不是 0。
	SizeBytes *int64
}

// DiskUsageSummary 是 system df 的磁盘占用汇总（六期 6a：磁盘治理）。
//
// 刻意定义成本域形态而不是透传 SDK 的 types.DiskUsage：SDK 类型不出 adapter
// （包边界纪律），且 daemon 的 verbose 响应只有逐项明细（镜像/卷/缓存各一条记录，
// 预聚合的 TotalSize/Reclaimable 类型已废弃）—— 汇总求和发生在 adapter，字段就是
// 快照需要的四样。单位是字节（daemon 的原单位）；折 MB 发生在快照组装处
// （与镜像条目 SizeBytes→SizeMB 同一条边界）。
type DiskUsageSummary struct {
	// ImagesTotalBytes 是全部镜像占用合计（含共享层的重复计入，docker CLI 同口径）。
	ImagesTotalBytes int64
	// ImagesDanglingBytes 是悬空镜像（无标签 = image:prune 默认目标）占用合计。
	ImagesDanglingBytes int64
	// VolumesTotalBytes 是已知体积卷的求和（非 local 驱动 -1 未知哨兵排除在外，下界）。
	VolumesTotalBytes int64
	// BuildCacheBytes 是构建缓存占用合计（协议没有对应的 prune 动作，纯账面事实）。
	BuildCacheBytes int64
}

// NetworkInfo 是网络列表项。
type NetworkInfo struct {
	Name            string
	Driver          string
	Scope           string
	Internal        bool
	ContainersCount int
}

// ── P2·分发面（image:build / image:push）的本域参数 ────────────────────────
//
// 与 ContainerCreateSpec 同一包边界纪律：协议 options 里的字符串都由**执行器**
// 解析定型（转移目录拼装、tar 白名单、缺省值补齐），adapter 只见最终值 ——
// SDK 的 build 类型不出 adapter。

// BuildSpec 是 image:build 的全部参数（adapter 直传 daemon，不做二次解析）。
type BuildSpec struct {
	// ContextPath 是上下文 tar 的**绝对路径**（transferDir 内，执行器按读侧
	// 符号链接纪律拼装校验过）。
	ContextPath string
	// Dockerfile 是 Dockerfile 在上下文内的相对路径（执行器补齐过缺省 "Dockerfile"）；
	// 它就是 ImageBuildOptions.Dockerfile —— daemon 在上下文 tar 里解析它。
	Dockerfile string
	// Tag 是目标镜像引用（执行器补过 :latest）。
	Tag string
	// BuildArgs 是 build-args（键值已过协议白名单：POSIX 标识符键、值尺寸上限）。
	BuildArgs map[string]string
}

// BuildProgress 是 daemon 一条构建进度行的本域记录（SDK 类型不出 adapter）。
//
// 与 PullProgress 的差别只在**形状**：build 的 daemon 输出以文本行为主
// （BuildKit 步骤行与输出），故多一个 Stream 字段；ID/Status 保留给步骤标识与
// 状态文案。Current/Total 不进 build 记录 —— BuildKit 的层传输在 build 流里
// 是内嵌的进度行重绘（\r 行），逐字节解析它们会把折叠器拖进终端渲染语义，
// 文本行 + 步骤状态足以回答「构建走到哪一步」。
type BuildProgress struct {
	ID     string
	Status string
	Stream string
}

// ContainerDetail 是容器 inspect 的结果。
type ContainerDetail struct {
	ID            string
	Name          string
	Image         string
	ImageID       string
	Created       int64
	State         string
	StartedAt     int64
	FinishedAt    int64
	ExitCode      *int
	RestartPolicy string
	Env           []string
	Labels        map[string]string
	Ports         []PortInfo
	Mounts        []MountInfo
	Entrypoint    []string
	Cmd           []string
	Health        string
	Networks      []string
}

// ImageLayer 是镜像的一层（历史项）。
type ImageLayer struct {
	SizeBytes  int64
	Created    int64
	CreatedBy  string
	EmptyLayer bool
	Comment    string
}

// ImageDetail 是镜像 inspect 的结果（元数据 + 分层历史）。
type ImageDetail struct {
	ID           string
	RepoTags     []string
	SizeBytes    int64
	Created      int64
	Architecture string
	OS           string
	Labels       map[string]string
	Env          []string
	ExposedPorts []string
	Entrypoint   []string
	Cmd          []string
	// History 是分层历史，**自下而上**（基础层在前）。
	History []ImageLayer
}

// ImageAuth 是仓库认证（4c）：core 受理时按凭据库解出的**瞬时凭据**，
// 生命周期 = 本条指令执行期 —— 用完即弃，任何路径不落盘、不进帧、不进 result。
// nil = 无凭据（公共仓库或主机侧 docker login），与 4b 之前的拉取**逐字一致**。
//
// P2 起它是 pull 与 push 的**公共凭据面**（同一把凭据键、同一条 core 注入路径、同一
// 条 adapter 编码路径），名字从 PullAuth 扩成 ImageAuth 是为如实表达这一点。
//
// 刻意定义成本域类型而不是 SDK 的 types.AuthConfig 别名：SDK 类型不出 adapter
// （包边界纪律），adapter 内完成到 daemon RegistryAuth 头的映射。
type ImageAuth struct {
	// Registry 是仓库地址（ServerAddress；与凭据键 / options.registry 同一把键）。
	Registry string
	// Username / Password 是仓库登录三元组的另外两元。
	Username string
	Password string
}

// DockerAPI 是本包需要的 Docker 能力面。
//
// 全部方法返回的错误都可以上抛给调用方折成结果；**例外是 Ping**：它的错误必须是
// 可直接展示的结论句（见 adapter.translateDockerError），因为它会成为「该主机 Docker
// 不可用」的原因说明。
type DockerAPI interface {
	Ping(ctx context.Context) error
	Containers(ctx context.Context) ([]ContainerInfo, error)
	ContainerStats(ctx context.Context, id string) (StatsInfo, error)
	Images(ctx context.Context) ([]ImageInfo, error)
	// VolumesAndDf 返回卷清单与 df 磁盘占用汇总 —— 两者出自**同一次** system df 调用。
	//
	// 为什么是一个方法而不是 Volumes + DiskUsage 两个：卷体积本来就只能取自 df 的
	// verbose 响应（volume ls 不含体积），拆开会让每帧快照**多跑一趟** df —— 它是
	// daemon 侧较重的调用（要扫镜像存储、逐卷统计），30s 周期下白翻倍。df 失败时
	// 退化 volume ls（既有行为），df 汇总返回 nil —— 调用方据此如实说「数据不可用」
	// 而不是报 0。
	VolumesAndDf(ctx context.Context) (volumes []VolumeInfo, df *DiskUsageSummary, err error)
	Networks(ctx context.Context) ([]NetworkInfo, error)
	ContainerInspect(ctx context.Context, name string) (ContainerDetail, error)
	// ContainerLogs 取尾部日志；truncated 表示因尺寸上限被截断。
	ContainerLogs(ctx context.Context, name string, tail int, since int64) (lines string, truncated bool, err error)
	ImageInspect(ctx context.Context, ref string) (ImageDetail, error)
	// ComposeVersion 探测 compose 形态与版本（单一 flavor 纪律：只在进程内第一次调用时真正执行）。
	ComposeVersion(ctx context.Context) (flavor, version string, err error)

	// ── 二期：写操作（每条都只做一件事，权限与确认由 core 强制）──────────
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string) error
	// ContainerRestart 是 docker restart 的原子版本（**不是** stop+start 两次调用：
	// 两次调用之间容器可以被别人拉起来，且 stop 的宽限期会被算两遍）。
	ContainerRestart(ctx context.Context, id string) error
	// ContainerRemove 删除容器。force 对应 docker rm -f；**不动容器持有的卷**
	//（卷的删除是独立的 volume:remove，两段确认各自成立）。
	ContainerRemove(ctx context.Context, id string, force bool) error

	// ContainerCreate 创建容器（四支柱·创建面，4a）。spec 里的一切都已是**最终值**
	//（端口映射/环境/挂载等协议字符串的解析在执行器完成 —— 包边界纪律仍然是
	//「SDK 类型不出 adapter」）。返回新容器的完整 ID。
	//
	// 错误里可能包装两个哨兵（errors.Is 可判，执行器据此给结论句）：
	//   - errImageNotFound        本机没有这个镜像（daemon 404 + No such image）；
	//   - errContainerNameConflict 容器名已被占用（daemon 409）。
	// 哨兵的 Error() 只有 daemon 原文（进 result.detail），结论句由执行器翻译。
	ContainerCreate(ctx context.Context, spec ContainerCreateSpec) (id string, err error)

	ImageRemove(ctx context.Context, ref string, force bool) error
	// ImagePrune 清理：all=false 只清悬空（与 docker image prune 同口径）。
	ImagePrune(ctx context.Context, all bool) (freedBytes int64, err error)
	// ImagePull 拉取镜像（4b 起产出进度）：返回**拉取结束**（成功或失败）的最终错误，
	// 拉取期间 daemon 的进度行经 emit 逐条交出（emit 为 nil = 丢弃进度 —— 老链路）。
	//
	// auth（4c）是 core 随指令瞬时下发的仓库认证；nil = 不带凭据 —— 与 4b 之前的
	// 拉取逐字一致。到 daemon 的映射（RegistryAuth 头）在 adapter 内完成，
	// SDK 类型不出 adapter。
	ImagePull(ctx context.Context, ref string, auth *ImageAuth, emit func(PullProgress)) error
	// ImageBuild 构建镜像（P2·分发面）：contextPath 是已定型的上下文 tar 路径（执行器
	// 按 transferDir 纪律拼装过了，adapter 只做**穿越校验 + 流式发送**两件事 —— 校验
	// 先于发送，恶意 tar 到不了 daemon）；BuildSpec.Dockerfile 是上下文内相对路径、
	// Tag 是目标镜像引用、BuildArgs 是 build-args（键值都已过协议白名单）。
	//
	// 构建期间 daemon 的进度行经 emit 逐条交出（与 ImagePull 同一回调纪律）。
	// 返回**构建结束**的最终错误。
	//
	// 基础镜像的拉取**不注入认证**（4c 的凭据面只服务镜像本身的拉/推）：ARG/HTTP
	// 头注入秘密会永驻镜像分层历史 —— 需要私有基础镜像的主机走宿主侧 docker login
	// 或 buildx secrets（daemon 侧配置），与 4c 之前 pull 的自由度一致。
	ImageBuild(ctx context.Context, spec BuildSpec, emit func(BuildProgress)) error
	// ImagePush 推送镜像（P2·分发面）：auth 与 ImagePull 同一公共凭据面（nil = 无凭据，
	// 与 4b 之前的拉取自由度一致 —— 公共仓库或宿主侧 docker login）。推送期间
	// daemon 的进度行（与 pull **同一个** JSON 流形态）经 emit 逐条交出；
	// 返回**推送结束**的最终错误。
	ImagePush(ctx context.Context, ref string, auth *ImageAuth, emit func(PullProgress)) error
	ImageTag(ctx context.Context, src, dst string) error
	// ImageSave 把镜像写成 tar；path 由调用方按 transferDir 拼好。
	// 目标已存在且未要求覆盖时返回 alreadyExists=true（**不覆盖**，两段确认由上层承载）。
	ImageSave(ctx context.Context, ref, path string, overwrite bool) (alreadyExists bool, err error)
	ImageLoad(ctx context.Context, path string) error

	VolumeRemove(ctx context.Context, name string, force bool) error
	VolumePrune(ctx context.Context) (freedBytes int64, err error)

	NetworkRemove(ctx context.Context, name string) error

	// ── 三期：流会话（日志 follow / 终端）────────────────────────────────

	// ContainerLogsFollow 打开容器日志流（follow=true，tail/since 同一次性读）：
	// 返回的流是**已解复用**的文本（TTY 容器是裸流），由调用方 Close。
	ContainerLogsFollow(ctx context.Context, name string, tail int, since int64) (io.ReadCloser, error)
	// ContainerExecAttach 建一条 TTY exec 并挂接：argv **直传** daemon，不经 shell。
	ContainerExecAttach(ctx context.Context, name string, argv []string) (*ExecSession, error)

	// ── 监控面：stats 实时流 ──────────────────────────────────────────

	// ContainerStatsStream 打开容器 stats 流（stream=true）：返回的通道逐样本给
	// 出**已算好**的读数（CPU%、内存、网络速率），首个样本在连接建立后**立刻**到来
	//（daemon 流式 API 的第一条记录即当前读数，之后约每秒一条）；close() 关闭底层
	// 连接，通道关闭 = 流结束（容器停止/被删或 ctx 取消）。
	ContainerStatsStream(ctx context.Context, name string) (<-chan StatsSample, io.Closer, error)

	// ── 事件流（活动流，host 级）───────────────────────────────────────────

	// Events 打开 docker events 订阅（filter **固定**为 container/image/volume/network
	// 四类）：返回的通道逐事件给出已归约的记录；close() 结束订阅（取消底层连接），
	// 通道关闭 = 流结束（ctx 取消或 daemon 断开）。它是 host 级能力：没有容器参数，
	// 唯一入参是会话 ctx —— 取消即停的纪律与 stats 完全相同。
	Events(ctx context.Context) (<-chan EventItem, io.Closer, error)
}

// ExecSession 是一条已挂接的终端流（TTY 模式：daemon 侧不做 8 字节头的多路复用，
// 输出是裸字节流 —— 与一次性日志读取里「先试解复用」的启发式不同，流的字节读过就
// 回不去了，故 TTY 判定必须在挂接前由 inspect 得出，见 adapter）。
type ExecSession struct {
	// Reader 是 PTY 输出（stdout/stderr 已合流）。
	Reader io.Reader
	// Writer 是进程 stdin。
	Writer io.Writer
	// Resize 调整终端尺寸（daemon 侧发 SIGWINCH）。
	Resize func(ctx context.Context, cols, rows int) error
	// Close 断开挂接（幂等；daemon 侧据此结束该 exec 会话）。
	Close func() error
}

// ExecError 是执行失败的**结论句 + 排障细节**。
//
// 两个字段分开与协议一致的取向：结论句进 result.error（页面显示），细节进 result.detail
// （落库、排障用、不渲染）—— 把 stderr 原文直接显示给用户是另一类问题。
type ExecError struct {
	Msg    string
	Detail string
	// AlreadyExists 是 image:save 特有的「目标产物已存在」标志：它是**失败结论**
	//（拒绝覆盖），但服务端要把它透出给前端，用户确认后带 overwrite=true 重发
	//（两段确认复用 cmd/result，§7.5）。dispatcher 会把它原样搬到 result。
	AlreadyExists bool
}

func (e *ExecError) Error() string { return e.Msg }

// errImageNotFound / errContainerNameConflict 是 ContainerCreate 的两个**可预期**
// 失败哨兵（契约见 DockerAPI.ContainerCreate）：执行器按 errors.Is 判别后给出
// 不同的结论句。它们只作判定目标 —— 用户可见的文案在执行器，detail 用 daemon 原文。
var (
	errImageNotFound         = errors.New("image not found")
	errContainerNameConflict = errors.New("container name conflict")
)
