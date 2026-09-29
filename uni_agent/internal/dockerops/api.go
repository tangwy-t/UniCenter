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

// ImageInfo 是镜像列表项。
type ImageInfo struct {
	ID        string
	RepoTags  []string
	SizeBytes int64
	Created   int64
}

// VolumeInfo 是卷列表项。
type VolumeInfo struct {
	Name   string
	Driver string
	// SizeBytes 为 nil 表示**用量未知**（部分驱动不提供），页面显示「—」而不是 0。
	SizeBytes *int64
}

// NetworkInfo 是网络列表项。
type NetworkInfo struct {
	Name            string
	Driver          string
	Scope           string
	Internal        bool
	ContainersCount int
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
	Volumes(ctx context.Context) ([]VolumeInfo, error)
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

	ImageRemove(ctx context.Context, ref string, force bool) error
	// ImagePrune 清理：all=false 只清悬空（与 docker image prune 同口径）。
	ImagePrune(ctx context.Context, all bool) (freedBytes int64, err error)
	ImagePull(ctx context.Context, ref string) error
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
