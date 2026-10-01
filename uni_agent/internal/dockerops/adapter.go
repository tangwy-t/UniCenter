package dockerops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
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

// ContainerStatsStream 打开容器 stats 实时流（stream=true）。
//
// 样本计算与 one-shot **同一套函数**（cpuPercent / mb / sumNet 直接复用），速率按
// 相邻样本的累计计数求差（rateDelta —— 与快照 collectStats 的公式逐项一致）。
// 首样本的速率恒为 0：这是 docker stats CLI 的同款行为（第一行没有可求差的基线），
// 而 CPU%、内存首样本就有真值 —— daemon 流式 API 的第一条记录带 precpu_stats，
// cpuPercent 的差值算法对首样本同样成立，这正是「抽屉打开即有数据」的根据。
func (a *sdkAdapter) ContainerStatsStream(ctx context.Context, name string) (<-chan StatsSample, io.Closer, error) {
	resp, err := a.cli.ContainerStats(ctx, name, true)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan StatsSample, 1)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		var prevRX, prevTX uint64
		var prevT time.Time
		for {
			var v container.StatsResponse
			if err := dec.Decode(&v); err != nil {
				// 流结束（容器停止/被删、ctx 取消、连接关闭都表现为读不到下一条记录）：
				// 通道关闭就是 eof 信号，错误细节不向外区分 —— 与日志流同纪律。
				return
			}
			rx := sumNet(&v, func(n container.NetworkStats) uint64 { return n.RxBytes })
			tx := sumNet(&v, func(n container.NetworkStats) uint64 { return n.TxBytes })
			s := StatsSample{
				CPUPercent: cpuPercent(v),
				MemUsageMB: mb(float64(v.MemoryStats.Usage)),
				MemLimitMB: mb(float64(v.MemoryStats.Limit)),
			}
			if !prevT.IsZero() {
				s.NetRXBytesSec = rateDelta(prevRX, rx, v.Read.Sub(prevT).Seconds())
				s.NetTXBytesSec = rateDelta(prevTX, tx, v.Read.Sub(prevT).Seconds())
			}
			prevRX, prevTX, prevT = rx, tx, v.Read
			select {
			case ch <- s:
			case <-ctx.Done():
				return // 会话取消：别把样本堵在一个没人读的通道上
			}
		}
	}()
	return ch, resp.Body, nil
}

// rateDelta 按相邻两次读数的累计值求速率（B/s）。口径与快照 collectStats 完全一致：
// 计数回绕（容器重启/网络重建后归零）或间隔异常时给 0 —— 编一个负数比给 0 更糟。
func rateDelta(prev, cur uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return round2(float64(cur-prev) / dt)
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
		out = append(out, ImageInfo{ID: im.ID, RepoTags: im.RepoTags, SizeBytes: im.Size, Created: im.Created})
	}
	return out, nil
}

// VolumesAndDf 列出卷与用量，并从**同一次** df 响应算出磁盘占用汇总（6a 磁盘治理）。
//
// 用量取自 system df（verbose）—— `volume ls` 本身不含体积。df 不可用时退化为
// `volume ls`：**用量未知比列不出卷好得多**，页面把未知显示成「—」；此时 df 汇总为
// nil（「数据不可用」），绝不把缺失当 0。
//
// df 成功即信任其卷清单（含空清单 —— 零卷主机的清单同样为空，老实现里「df 成功但
// 零卷时再跑一遍 volume ls」的绕路只复制出同一个空清单；真正要防的是 df 失败，那
// 走 ls 退化）。
func (a *sdkAdapter) VolumesAndDf(ctx context.Context) ([]VolumeInfo, *DiskUsageSummary, error) {
	du, err := a.cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		res, lerr := a.cli.VolumeList(ctx, volume.ListOptions{})
		if lerr != nil {
			return nil, nil, lerr
		}
		out := make([]VolumeInfo, 0, len(res.Volumes))
		for _, v := range res.Volumes {
			out = append(out, VolumeInfo{Name: v.Name, Driver: v.Driver})
		}
		return out, nil, nil
	}
	out := make([]VolumeInfo, 0, len(du.Volumes))
	var volumesTotal int64
	for _, v := range du.Volumes {
		vi := VolumeInfo{Name: v.Name, Driver: v.Driver}
		// Size>=0 才是已知体积：daemon 对非 local 驱动给 -1（「未知」哨兵），放进
		// SizeBytes 会被快照折成 0 MB —— 「未知」被渲染成「零占用」，正是卷体积
		// 字段自己注释里反对的事。未知保持 nil（页面「—」），求和也只加已知项。
		if v.UsageData != nil && v.UsageData.Size >= 0 {
			size := v.UsageData.Size
			vi.SizeBytes = &size
			volumesTotal += size
		}
		out = append(out, vi)
	}
	// df 的镜像与缓存明细只在 df 响应里有（镜像清单来自另一趟 /images/json）——
	// 汇总从**这一帧**的 df 算，三类数字出自同一时刻，不会被两次调用的时差弄出
	//「面板一个数、列表另一个数」的永久疑问。
	var imagesTotal, imagesDangling int64
	for _, im := range du.Images {
		imagesTotal += im.Size
		// 悬空判据 = 无标签：与 daemon 的悬空过滤器、image:prune 的目标集合同一口径
		//（见快照 isDanglingImage 的实测论证；CLI 的 dangling=false 展示口径相反，不采）。
		if len(im.RepoTags) == 0 {
			imagesDangling += im.Size
		}
	}
	var buildCache int64
	for _, c := range du.BuildCache {
		buildCache += c.Size
	}
	return out, &DiskUsageSummary{
		ImagesTotalBytes:    imagesTotal,
		ImagesDanglingBytes: imagesDangling,
		VolumesTotalBytes:   volumesTotal,
		BuildCacheBytes:     buildCache,
	}, nil
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

// ── 二期：写操作的 SDK 实现（只做一次调用，权限/确认/保护/路径都在上层）──────

func (a *sdkAdapter) ContainerStart(ctx context.Context, id string) error {
	return a.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (a *sdkAdapter) ContainerStop(ctx context.Context, id string) error {
	return a.cli.ContainerStop(ctx, id, container.StopOptions{})
}

func (a *sdkAdapter) ContainerRestart(ctx context.Context, id string) error {
	return a.cli.ContainerRestart(ctx, id, container.StopOptions{})
}

func (a *sdkAdapter) ContainerRemove(ctx context.Context, id string, force bool) error {
	// RemoveVolumes 保持 false：卷的删除必须走独立的 volume:remove（各自确认）。
	return a.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

// createSentinelError 是容器创建错误的哨兵包装：Unwrap 返回哨兵（执行器 errors.Is
// 判队），Error() 只带 daemon 原文（进 result.detail）。结论句不进这个类型 ——
// 文案是执行器的事，adapter 不决定用户看到哪句话。
type createSentinelError struct {
	sentinel, cause error
}

func (e *createSentinelError) Error() string { return e.cause.Error() }
func (e *createSentinelError) Unwrap() error { return e.sentinel }

// ContainerCreate 创建容器：Config/HostConfig 逐项映射（端口绑定、重启策略、
// 资源限额、网络、挂载），全部参数直传 daemon —— 不经 shell、不做任何字符串拼接
// 之外的加工。所有字段在执行器已被协议白名单校验过，这里是纯映射。
func (a *sdkAdapter) ContainerCreate(ctx context.Context, spec ContainerCreateSpec) (string, error) {
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          spec.Env,
		ExposedPorts: nat.PortSet{},
	}
	host := &container.HostConfig{
		Binds:        []string{},
		PortBindings: nat.PortMap{},
		Resources:    container.Resources{},
	}
	// 端口绑定：容器端口进 ExposedPorts（对应 docker run -p 的自动 expose），
	// 宿主端口进 PortBindings。
	for _, p := range spec.Ports {
		port := nat.Port(strconv.Itoa(p.Container) + "/" + p.Proto)
		cfg.ExposedPorts[port] = struct{}{}
		host.PortBindings[port] = []nat.PortBinding{{HostPort: strconv.Itoa(p.Host)}}
	}
	// 挂载：命名卷与 bind 都是 Binds 的一串（daemon 按源是否路径自动分辨；
	// 命名卷不存在时 daemon 全程自动创建 —— 与 docker run -v vol:/dst 同行为）。
	for _, m := range spec.Mounts {
		b := m.Source + ":" + m.Dest
		if m.ReadOnly {
			b += ":ro"
		}
		host.Binds = append(host.Binds, b)
	}
	switch spec.RestartPolicy {
	case "", "no": // 默认就是 disabled，不写出。空串 = 没设。
	default:
		host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy)}
	}
	if spec.Network != "" {
		host.NetworkMode = container.NetworkMode(spec.Network)
	}
	if spec.CPULimit > 0 {
		host.Resources.NanoCPUs = int64(spec.CPULimit * 1e9)
	}
	if spec.MemLimitMB > 0 {
		host.Resources.Memory = int64(spec.MemLimitMB) << 20
	}
	created, err := a.cli.ContainerCreate(ctx, cfg, host, nil, nil, spec.Name)
	if err != nil {
		return "", classifyCreateError(err)
	}
	return created.ID, nil
}

// classifyCreateError 把 daemon 的创建错误折成哨兵包装。两个可预期失败分别对号：
//   - 镜像缺失：404 且带 "No such image" —— 404 也可能是网络不存在（"network ...
//     not found"），只有原文对得上才翻成镜像哨兵，别把两种处置方向糊成一句；
//   - 名字冲突：409（create 上的 409 只有名字被占用一种来源）。
//
// 其余错误原样上抛，由 wrapDocker 兜成通用结论句。
func classifyCreateError(err error) error {
	msg := err.Error()
	if errdefs.IsNotFound(err) && strings.Contains(msg, "No such image") {
		return &createSentinelError{sentinel: errImageNotFound, cause: err}
	}
	if errdefs.IsConflict(err) {
		return &createSentinelError{sentinel: errContainerNameConflict, cause: err}
	}
	return err
}

func (a *sdkAdapter) ImageRemove(ctx context.Context, ref string, force bool) error {
	// PruneChildren 对应 docker rmi 的「连带删除子镜像」：不打它会有大量 <none> 残留，
	// 而残留会挤占磁盘正是用户点删除的理由。
	_, err := a.cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: force, PruneChildren: true})
	return err
}

// ImagePrune 清理镜像。
//
// 悬浮判定交给 daemon 的过滤器（dangling=!all）：`docker image prune`（默认）清的正是
// 这个集合，而本地按 RepoTags 判悬空与 daemon 口径有实测差异（见快照的 digest-only 用例）。
func (a *sdkAdapter) ImagePrune(ctx context.Context, all bool) (int64, error) {
	report, err := a.cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", strconv.FormatBool(!all))))
	if err != nil {
		return 0, err
	}
	return int64(report.SpaceReclaimed), nil
}

// ImagePull 拉取镜像（4b：进度产出；4c：按指令注入仓库认证）。
//
// **必须把响应体读到 EOF 再关闭**：拉取的实际工作在响应体流上完成，提前 Close 会让
// 拉取半途中断（镜像不完整），而调用方看到的却是「没有错误」（spec §7.5 的既有纪律，
// 4b 之后依然成立 —— 差别只在「读的过程顺带产出进度」）。
//
// emit 收 daemon 进度行的本域记录（拉取期间同步回调、绝不阻塞）；emit 为 nil 时走
// 老链路的 io.Discard 快路径（读满、丢弃、只回结论）。
//
// auth（4c）为 nil 时 PullOptions 保持空 —— 与一期/4b 逐字一致（公共仓库或主机侧
// docker login 自行解决）；非 nil 时折成 SDK 的 RegistryAuth 头（base64 的
// {username,password,serveraddress}）：明文密码只出现在发给 daemon 的这一个编码头里，
// 不回写任何日志/进度帧/结果。SDK 类型不出 adapter 的边界在这里收口。
func (a *sdkAdapter) ImagePull(ctx context.Context, ref string, auth *PullAuth, emit func(PullProgress)) error {
	opts := image.PullOptions{}
	if auth != nil {
		encoded, err := encodePullAuth(auth)
		if err != nil {
			return err
		}
		opts.RegistryAuth = encoded
	}
	rc, err := a.cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer rc.Close()
	if emit == nil {
		_, err = io.Copy(io.Discard, rc)
		return err
	}
	return consumePullStream(rc, emit)
}

// encodePullAuth 把本域 PullAuth 折成 daemon 认识的 X-Registry-Auth 头值
// （base64 的 {username,password,serveraddress}）。
//
// 提取成纯函数而不是内联在 ImagePull 里：包边界纪律说「只有 adapter 碰 SDK」，
// 而这段映射正是 4c 的关键路径 —— 必须让它在没有 docker daemon 的 CI 里
// 直接可测（adapter_test.go 用 DecodeAuthConfig 做往返断言），否则
// 「认证有没有真的按 SDK 口径折成头」就成了 CI 盲区。
func encodePullAuth(a *PullAuth) (string, error) {
	return registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      a.Username,
		Password:      a.Password,
		ServerAddress: a.Registry,
	})
}

// PullProgress 是 daemon 一条进度行的本域记录（SDK 的 jsonmessage 类型不出 adapter）。
//
// 字段即页面需要的全部：ID 是层 id（空 = 没有层的消息行，如 "Pulling from …"），
// Status 是原文状态文案，Current/Total 是该层已传输/总字节（progressDetail 缺失时
// 两者为 0 —— 「未知大小」与「0 字节」在协议上同形，前端显示「进行中」即可）。
type PullProgress struct {
	ID      string
	Status  string
	Current int64
	Total   int64
}

// pullJSONLine 是 daemon 拉取流的一行。刻意**自定义最小结构**而不是引用 SDK 的
// jsonmessage.JSONMessage —— 那个包在 SDK v28 已被标记废弃，且其字段随版本漂移；
// 本域只取进度面用得到的五个键，漂移在解码时表现为零值（记录照发，只是字段空），
// 而不是编译/行为两级断裂。
type pullJSONLine struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"`
	// ProgressDetail 是 {current,total}；用指针区分「缺失（未知）」与「0」。
	ProgressDetail *struct {
		Current int64 `json:"current,omitempty"`
		Total   int64 `json:"total,omitempty"`
	} `json:"progressDetail,omitempty"`
	ErrorDetail *struct {
		Message string `json:"message,omitempty"`
	} `json:"errorDetail,omitempty"`
	Error string `json:"error,omitempty"`
}

// consumePullStream 把 daemon 的 JSON 进度流逐行解析成 PullProgress 交给 emit，
// 直到 EOF 再收口。
//
// 错误行的处理照 docker CLI 的口径：先记下不立刻返回 —— 读满到 EOF 保证「放弃拉取」
// 的决定由上层在**流结束后**做，半途 abort 会把账号的并发拉取槽位挂在 daemon 上；
// EOF 时返回记下的错误（daemon 的错误行是它的**失败结论**，不是可忽略的噪音）。
// errorDetail.message 优先（最贴近原因），缺席时退回 error 串。
func consumePullStream(r io.Reader, emit func(PullProgress)) error {
	dec := json.NewDecoder(r)
	var pullErr error
	for {
		var line pullJSONLine
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return pullErr
			}
			// 流半途损坏：不是 daemon 会给出的形态（每行都是合法 JSON），返回它。
			if pullErr != nil {
				return pullErr
			}
			return err
		}
		if line.ErrorDetail != nil && line.ErrorDetail.Message != "" {
			if pullErr == nil {
				pullErr = errors.New(line.ErrorDetail.Message)
			}
			continue // 错误行不进进度流：它会被折成终态 Error 项（由执行器在流末恰发一条）
		}
		if line.Error != "" {
			if pullErr == nil {
				pullErr = errors.New(line.Error)
			}
			continue
		}
		p := PullProgress{ID: line.ID, Status: line.Status}
		if d := line.ProgressDetail; d != nil {
			p.Current, p.Total = d.Current, d.Total
		}
		emit(p)
	}
}

func (a *sdkAdapter) ImageTag(ctx context.Context, src, dst string) error {
	return a.cli.ImageTag(ctx, src, dst)
}

// ImageSave 把镜像导出成 tar 文件。
//
// 打开方式由 overwrite 决定，且**只有这两种形态**：
//   - overwrite=false：O_CREATE|O_EXCL —— 已存在（含符号链接）时返回 alreadyExists，
//     由上层折成「产物文件已存在，确认覆盖后重试」；绝不静默覆盖别人放的文件；
//   - overwrite=true：用户已在两段确认里明确要覆盖，此时才允许截断重写。
func (a *sdkAdapter) ImageSave(ctx context.Context, ref, path string, overwrite bool) (bool, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) && !overwrite {
			return true, nil
		}
		return false, err
	}
	defer f.Close()
	rc, err := a.cli.ImageSave(ctx, []string{ref})
	if err != nil {
		return false, err
	}
	defer rc.Close()
	if _, err := io.Copy(f, rc); err != nil {
		return false, err
	}
	return false, nil
}

// ImageLoad 从 tar 文件导入镜像。
//
// 与 ImagePull 同理：响应体要读到 EOF，否则导入可能半途而废（失败只表现为镜像不完整）。
func (a *sdkAdapter) ImageLoad(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	res, err := a.cli.ImageLoad(ctx, f)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		return err
	}
	return nil
}

func (a *sdkAdapter) VolumeRemove(ctx context.Context, name string, force bool) error {
	return a.cli.VolumeRemove(ctx, name, force)
}

// VolumePrune 清理卷。
//
// 空过滤器 = daemon 的默认口径（只清**匿名且未使用**的卷），与 spec §4.3.1 一致；
// 命名卷（含保护清单里的底座数据卷）不在其中 —— 这正是这里不需要 guard 的原因。
func (a *sdkAdapter) VolumePrune(ctx context.Context) (int64, error) {
	report, err := a.cli.VolumesPrune(ctx, filters.NewArgs())
	if err != nil {
		return 0, err
	}
	return int64(report.SpaceReclaimed), nil
}

func (a *sdkAdapter) NetworkRemove(ctx context.Context, name string) error {
	return a.cli.NetworkRemove(ctx, name)
}

// ── 三期：流会话的 SDK 实现 ───────────────────────────────────────────────

// ContainerLogsFollow 打开日志流（follow=true）。
//
// 与一次性读取的 ContainerLogs 不同，这里**不能**用「先试 stdcopy、失败按原样」的启发式：
// 流式读取读过的字节回不去，猜错一次就把 8 字节头混进用户看到的日志（或反过来吞掉正文）。
// 故先 inspect 拿 Config.Tty：TTY 容器是裸流，非 TTY 才是多路复用帧，各自只有一条路。
func (a *sdkAdapter) ContainerLogsFollow(ctx context.Context, name string, tail int, since int64) (io.ReadCloser, error) {
	opts := container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: strconv.Itoa(tail)}
	if since > 0 {
		opts.Since = time.Unix(since, 0).UTC().Format(time.RFC3339)
	}
	// TTY 判定放在开流**之前**：先开流再 inspect 也没问题，但错误路径会多一次 Close。
	v, err := a.cli.ContainerInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	rc, err := a.cli.ContainerLogs(ctx, name, opts)
	if err != nil {
		return nil, err
	}
	if v.Config != nil && v.Config.Tty {
		return rc, nil
	}
	// 非 TTY：用管道把解复用后的文本流出去（stdcopy 在两个写端合并 stdout/stderr）。
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, rc)
		_ = rc.Close()
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

// ContainerExecAttach 建 TTY exec 并挂接。
//
// 三个 Attach 位全开（stdin/stdout/stderr）：TTY 模式下 daemon 把 stderr 也合进同一流，
// 不开 AttachStderr 会丢掉错误输出。Cmd 是调用方校验过的 argv（缺省 ["/bin/sh"]）。
func (a *sdkAdapter) ContainerExecAttach(ctx context.Context, name string, argv []string) (*ExecSession, error) {
	created, err := a.cli.ContainerExecCreate(ctx, name, container.ExecOptions{
		Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true, Cmd: argv,
	})
	if err != nil {
		return nil, err
	}
	hj, err := a.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	return &ExecSession{
		Reader: hj.Reader,
		Writer: hj.Conn,
		Resize: func(ctx context.Context, cols, rows int) error {
			return a.cli.ContainerExecResize(ctx, created.ID, container.ResizeOptions{
				Height: uint(rows), Width: uint(cols),
			})
		},
		Close: func() error { hj.Close(); return nil },
	}, nil
}

// ── 事件流（docker events，host 级）───────────────────────────────────────

// eventsTypeFilter 是事件订阅的类型过滤：只收四类资源的动作。daemon 侧过滤而不是
// 收下再丢 —— 掉进通道的既有取舍就是在浪费带宽与通道槽位（与快照「只取本域字段」
// 同一取向）。
var eventsTypeFilter = filters.NewArgs(
	filters.Arg("type", agentproto.DockerEventTypeContainer),
	filters.Arg("type", agentproto.DockerEventTypeImage),
	filters.Arg("type", agentproto.DockerEventTypeVolume),
	filters.Arg("type", agentproto.DockerEventTypeNetwork),
)

// cancelCloser 把「取消订阅」折成 io.Closer：会话 teardown 走 attachUpstream 关上游，
// 事件流的「上游」就是派生出来的订阅 ctx（取消它，SDK 的读循环随之退出）。
type cancelCloser struct{ cancel context.CancelFunc }

func (c cancelCloser) Close() error { c.cancel(); return nil }

// Events 打开 docker events 订阅。
//
// SDK 的错误通道在两个时刻发错：**建立阶段**（query 拼错/连不上）与**流中途**
// （daemon 断开、body 读失败）。前一种要折成返回值（执行器据此回结论句、不占槽位）；
// 后一种与 stats/日志流同纪律 —— 通道关闭即结束，不向外区分错误细节。SDK 在返回前
// 已启动请求（内部等过 started 信号），故建立错误此刻已在容量 1 的错误通道里，
// 非阻塞取一次即可分辨「没连上」与「正常开始」。
func (a *sdkAdapter) Events(ctx context.Context) (<-chan EventItem, io.Closer, error) {
	subCtx, cancel := context.WithCancel(ctx)
	msgCh, errCh := a.cli.Events(subCtx, events.ListOptions{Filters: eventsTypeFilter})
	select {
	case err := <-errCh:
		cancel()
		return nil, nil, err
	default:
	}
	ch := make(chan EventItem, 1)
	go func() {
		defer close(ch)
		defer cancel()
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				select {
				case ch <- toEventItem(msg):
				case <-subCtx.Done():
					return
				}
			case _, ok := <-errCh:
				if !ok {
					return
				}
				// 流中途错误：与日志/stats 流的收尾同纪律 —— 通道关闭就是结束信号，
				// 错误细节不向外区分（重启后 core 的重订机制会自愈）。
				return
			}
		}
	}()
	return ch, cancelCloser{cancel}, nil
}

// toEventItem 把 SDK 事件折成本域记录：主体名取 Actor.Attributes["name"]
// （容器/卷/网络名、镜像引用是属性而不是 Message 的顶层字段）。字段漂移只在这一行
// 被拦 —— 本域之外不再出现 SDK 的 events 类型。
func toEventItem(msg events.Message) EventItem {
	return EventItem{
		Type:      string(msg.Type),
		Action:    string(msg.Action),
		ActorName: msg.Actor.Attributes["name"],
		ActorID:   msg.Actor.ID,
	}
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
