package dockerops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// maxLogBytes 是单次日志读取的字节上限（协议 result.payload 上限 256KB 的内侧余量）。
const maxLogBytes = 200 << 10

// sdkAdapter 是 DockerAPI 的**唯一** SDK 实现（SDK 升级只影响本文件）。
type sdkAdapter struct {
	cli *client.Client
}

// newSDKAdapter 构造 SDK 客户端。
//
// FromEnv 让 DOCKER_HOST / DOCKER_API_VERSION 等标准环境变量生效（现场可能把 sock
// 放在非默认位置）；WithAPIVersionNegotiation 让客户端按 daemon 的实际版本协商 ——
// 不协商时「客户端比 daemon 新」会直接报版本错误，而生产 daemon 是 26.1.4。
func newSDKAdapter() (*sdkAdapter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &sdkAdapter{cli: cli}, nil
}

// translateDockerError 把 SDK 错误折成**结论句**（它会出现在页面上）。
//
// 两类最常见原因必须分开说：sock 不可达（daemon 没跑）与权限不足（agent 不是 root、
// 也不在 docker 组）。SDK 对两者都可能给出 "Cannot connect to the Docker daemon"，
// 而处置方向完全相反 —— 一句糊话会让运维朝错误方向排查。
func translateDockerError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "permission denied"):
		return "无权访问 docker.sock（请让 agent 以 root 运行，或加入 docker 组）"
	case strings.Contains(msg, "no such file or directory"),
		strings.Contains(msg, "connection refused"),
		// daemon 没跑时客户端的原话就是这一句（它同时出现在「sock 不在默认位置」的场景），
		// 不给它一个分支就会掉进「无法连接 Docker」这种什么都没说的兜底。
		strings.Contains(msg, "Cannot connect to the Docker daemon"),
		strings.Contains(msg, "Is the docker daemon running"):
		return "无法连接 docker.sock（Docker 服务未运行？）"
	default:
		return "无法连接 Docker"
	}
}

func (a *sdkAdapter) Ping(ctx context.Context) error {
	if _, err := a.cli.Ping(ctx); err != nil {
		return errors.New(translateDockerError(err))
	}
	return nil
}

func (a *sdkAdapter) Containers(ctx context.Context) ([]ContainerInfo, error) {
	list, err := a.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		out = append(out, ContainerInfo{
			ID: c.ID, Name: primaryName(c.Names), Image: c.Image, ImageID: c.ImageID,
			State: string(c.State), Status: c.Status, Created: c.Created,
			Labels: c.Labels, Ports: toPorts(c.Ports), Mounts: toMounts(c.Mounts),
		})
	}
	return out, nil
}

// primaryName 取第一个名字并去掉 Docker 加的前导 '/'（Docker 的 Names 是 "/mysql"）。
func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (a *sdkAdapter) ContainerStats(ctx context.Context, id string) (StatsInfo, error) {
	r, err := a.cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return StatsInfo{}, err
	}
	defer r.Body.Close()
	var v container.StatsResponse
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return StatsInfo{}, err
	}
	return StatsInfo{
		CPUPercent: cpuPercent(v),
		MemUsageMB: mb(float64(v.MemoryStats.Usage)),
		MemLimitMB: mb(float64(v.MemoryStats.Limit)),
		NetRXBytes: sumNet(&v, func(n container.NetworkStats) uint64 { return n.RxBytes }),
		NetTXBytes: sumNet(&v, func(n container.NetworkStats) uint64 { return n.TxBytes }),
	}, nil
}

// cpuPercent 计算容器 CPU 占比。
//
// 与 docker CLI 同一算法：用 cpu_stats 与 precpu_stats 的差值。ContainerStatsOneShot
// 一次请求就带上了两者（daemon 记着上一个采样点），故**不**需要两次采样。
func cpuPercent(v container.StatsResponse) float64 {
	cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(v.CPUStats.SystemUsage) - float64(v.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	n := float64(v.CPUStats.OnlineCPUs)
	if n == 0 {
		n = float64(len(v.CPUStats.CPUUsage.PercpuUsage))
	}
	if n == 0 {
		n = 1
	}
	return round2(cpuDelta / sysDelta * n * 100)
}

// sumNet 汇总各网卡的某个计数器（整机口径：容器可能有多个网络）。
func sumNet(v *container.StatsResponse, pick func(container.NetworkStats) uint64) uint64 {
	var total uint64
	for _, n := range v.Networks {
		total += pick(n)
	}
	return total
}

func mb(b float64) float64 { return round2(b / (1024 * 1024)) }

func (a *sdkAdapter) Images(ctx context.Context) ([]ImageInfo, error) {
	list, err := a.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	out := make([]ImageInfo, 0, len(list))
	for _, im := range list {
		out = append(out, ImageInfo{ID: im.ID, RepoTags: im.RepoTags, RepoDigests: im.RepoDigests,
			SizeBytes: im.Size, Created: im.Created})
	}
	return out, nil
}

// Volumes 列出卷与用量。
//
// 用量取自 system df（verbose）—— `volume ls` 本身不含体积。df 不可用时退化为
// `volume ls`：**用量未知比列不出卷好得多**，页面把未知显示成「—」。
func (a *sdkAdapter) Volumes(ctx context.Context) ([]VolumeInfo, error) {
	if du, err := a.cli.DiskUsage(ctx, types.DiskUsageOptions{}); err == nil && len(du.Volumes) > 0 {
		out := make([]VolumeInfo, 0, len(du.Volumes))
		for _, v := range du.Volumes {
			vi := VolumeInfo{Name: v.Name, Driver: v.Driver}
			if v.UsageData != nil {
				size := v.UsageData.Size
				vi.SizeBytes = &size
			}
			out = append(out, vi)
		}
		return out, nil
	}
	res, err := a.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]VolumeInfo, 0, len(res.Volumes))
	for _, v := range res.Volumes {
		out = append(out, VolumeInfo{Name: v.Name, Driver: v.Driver})
	}
	return out, nil
}

func (a *sdkAdapter) Networks(ctx context.Context) ([]NetworkInfo, error) {
	list, err := a.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NetworkInfo, 0, len(list))
	for _, n := range list {
		out = append(out, NetworkInfo{
			Name: n.Name, Driver: n.Driver, Scope: n.Scope,
			Internal: n.Internal, ContainersCount: len(n.Containers),
		})
	}
	return out, nil
}

// ContainerInspect 读容器详情。
//
// v28 的 InspectResponse 把 ContainerJSONBase 以**指针**内嵌，State / Config /
// HostConfig / NetworkSettings 也全是指针：daemon 省略某一段时它们是 nil，直接解引用
// 会把 agent 整个打挂 —— 一次 inspect 不该有这个代价，故逐段判空。
func (a *sdkAdapter) ContainerInspect(ctx context.Context, name string) (ContainerDetail, error) {
	v, err := a.cli.ContainerInspect(ctx, name)
	if err != nil {
		return ContainerDetail{}, err
	}
	d := ContainerDetail{
		ID: v.ID, Name: strings.TrimPrefix(v.Name, "/"),
		ImageID: v.Image, Mounts: toMounts(v.Mounts),
	}
	if t, err := time.Parse(time.RFC3339Nano, v.Created); err == nil {
		d.Created = t.Unix()
	}
	if v.Config != nil {
		d.Image = v.Config.Image
		d.Env = v.Config.Env
		d.Labels = v.Config.Labels
		d.Entrypoint = []string(v.Config.Entrypoint)
		d.Cmd = []string(v.Config.Cmd)
	}
	if v.HostConfig != nil {
		d.RestartPolicy = string(v.HostConfig.RestartPolicy.Name)
	}
	if v.State != nil {
		d.State = string(v.State.Status)
		if v.State.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, v.State.StartedAt); err == nil {
				d.StartedAt = t.Unix()
			}
		}
		if v.State.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, v.State.FinishedAt); err == nil {
				d.FinishedAt = t.Unix()
			}
		}
		if v.State.ExitCode != 0 || string(v.State.Status) == "exited" {
			code := v.State.ExitCode
			d.ExitCode = &code
		}
		if v.State.Health != nil {
			d.Health = string(v.State.Health.Status)
		}
	}
	if v.NetworkSettings != nil {
		for name := range v.NetworkSettings.Networks {
			d.Networks = append(d.Networks, name)
		}
		for p, binding := range v.NetworkSettings.Ports {
			for _, b := range binding {
				host := 0
				if b.HostPort != "" {
					host, _ = strconv.Atoi(b.HostPort)
				}
				d.Ports = append(d.Ports, PortInfo{IP: b.HostIP, Private: int(p.Int()), Public: host, Type: p.Proto()})
			}
		}
	}
	return d, nil
}

// ContainerLogs 取尾部日志。
//
// 两个必须处理的 Docker 细节：
//  1. 日志流是**多路复用帧**（8 字节头：流类型 + 长度），直接读会把头字节混进文本，
//     故走 stdcopy 解复用；
//  2. 以 TTY 模式启动的容器**不是**多路复用（原样流），此时 stdcopy 会报
//     "unrecognized input header" —— 那不是错误，是另一种合法输出形态，故先试
//     解复用、失败则按原样用。
func (a *sdkAdapter) ContainerLogs(ctx context.Context, name string, tail int, since int64) (string, bool, error) {
	opts := container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail)}
	if since > 0 {
		opts.Since = time.Unix(since, 0).UTC().Format(time.RFC3339)
	}
	rc, err := a.cli.ContainerLogs(ctx, name, opts)
	if err != nil {
		return "", false, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxLogBytes+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(raw) > maxLogBytes
	if truncated {
		raw = raw[:maxLogBytes]
	}
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, bytes.NewReader(raw)); err == nil {
		return buf.String(), truncated, nil
	}
	return string(raw), truncated, nil
}

// ImageInspect 读镜像详情（元数据 + 分层历史）。
//
// v28 的镜像 config 是 dockerspec.DockerOCIImageConfig（内嵌 ocispec.ImageConfig），
// 字段名与容器的 container.Config 不完全一致，故逐字段显式搬。
func (a *sdkAdapter) ImageInspect(ctx context.Context, ref string) (ImageDetail, error) {
	v, err := a.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageDetail{}, err
	}
	hist, err := a.cli.ImageHistory(ctx, ref)
	if err != nil {
		return ImageDetail{}, err
	}
	d := ImageDetail{
		ID: v.ID, RepoTags: v.RepoTags, SizeBytes: v.Size, Architecture: v.Architecture, OS: v.Os,
	}
	if t, err := time.Parse(time.RFC3339Nano, v.Created); err == nil {
		d.Created = t.Unix()
	}
	if v.Config != nil {
		d.Labels = v.Config.Labels
		d.Env = v.Config.Env
		d.Entrypoint = v.Config.Entrypoint
		d.Cmd = v.Config.Cmd
		for p := range v.Config.ExposedPorts {
			d.ExposedPorts = append(d.ExposedPorts, p)
		}
	}
	// 历史**自下而上**：SDK 返回的是自上而下（最近的层在前），而页面的阅读顺序是
	// 「基础层 → 增量层」。反转放在 adapter 而不是展示层 —— 两种顺序在数据里都合法，
	// 只有一处决定才不会有「同一份数据两个看法」。
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		// EmptyLayer 在 v28 的 HistoryResponseItem 里**不存在**（daemon 的 JSON 也不带
		// empty_layer 键），只能按 daemon 的算法反推：元数据层（ENV/CMD 这类不落盘指令）
		// 的 Size 恒为 0，而非空层取的是 DiffSize / 快照用量。
		layer := ImageLayer{
			SizeBytes: h.Size, CreatedBy: h.CreatedBy, Comment: h.Comment,
			EmptyLayer: h.Size == 0,
		}
		layer.Created = h.Created
		d.History = append(d.History, layer)
	}
	return d, nil
}

// ComposeVersion 探测 compose 形态（两次命令行探测 + 纯函数解析）。
//
// 探测结果由调用方（Runtime）缓存并落盘：**主机上定下的 flavor 不再更换**（单一 flavor
// 纪律，§6.2）。故这个方法在进程内通常只被真正执行一次。
func (a *sdkAdapter) ComposeVersion(ctx context.Context) (string, string, error) {
	pluginOut, pluginErr := runCompose(ctx, "docker", "compose", "version")
	standaloneOut, standaloneErr := runCompose(ctx, "docker-compose", "version")
	flavor, version, ok := ParseComposeProbe(pluginOut, pluginErr, standaloneOut, standaloneErr)
	if !ok {
		return "", "", errors.New("未找到可用的 compose（既没有插件也没有独立二进制）")
	}
	return flavor, version, nil
}

// runCompose 执行一次 compose 版本探测。固定 argv、**不经 shell**（§10 执行纪律）。
func runCompose(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// round2 保留两位小数（页面上的百分比与 MB 只用到这个精度）。
func round2(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*100+0.5)) / 100
}

// toPorts 逐项映射 SDK 的端口映射到本域类型（字段同名同义，不做任何加工）。
func toPorts(in []container.Port) []PortInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]PortInfo, 0, len(in))
	for _, p := range in {
		out = append(out, PortInfo{
			IP:      p.IP,
			Private: int(p.PrivatePort),
			Public:  int(p.PublicPort),
			Type:    p.Type,
		})
	}
	return out
}

// toMounts 逐项映射 SDK 的挂载点到本域类型（字段同名同义，不做任何加工）。
func toMounts(in []container.MountPoint) []MountInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]MountInfo, 0, len(in))
	for _, m := range in {
		out = append(out, MountInfo{
			Type:        string(m.Type),
			Source:      m.Source,
			Destination: m.Destination,
			Mode:        m.Mode,
			RW:          m.RW,
		})
	}
	return out
}
