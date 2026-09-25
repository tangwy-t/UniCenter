package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 域的**读面**：可管主机清单与资源快照（下发面在 docker_cmd.go）。
//
// 两面的边界不是「文件大小」而是**是否摸 agent 通道**：读面只碰 Redis 与设备表，
// 因此 agent 全下线时页面照样打得开（列表读最后一份快照 + 服务端算出的陈旧结论），
// 而下发面必须经 agenthub 才可能把指令送到设备上。合成一个类型会让「只想看列表」
// 的调用方被迫依赖整条下行链路。

// DockerDeviceReader 是读面需要的设备字段（由 repository.DeviceRepo 满足）。
//
// 只声明 FindByID：主机数是个位数（每台机器一个 agent），逐台查库足够，
// 不值得为一个「批量查 2 行」的优化去动仓储接口 —— 那会把本域的窄接口变成仓储的
// 新方法，让两个包互相牵制。
type DockerDeviceReader interface {
	FindByID(ctx context.Context, id uint64) (*entity.Device, error)
}

// DockerService 是 docker 域的读面。
type DockerService struct {
	store   *dockerstate.Store
	devices DockerDeviceReader
	cfg     AgentConfigGetter
	log     logger.LoggerInterface
	// now 可替换：陈旧度与年龄都是「相对现在」的结论，测试要一个确定的现在
	//（否则断言会随执行时刻漂移，跨秒边界上还会随机红）。
	now func() time.Time
}

// NewDockerService 构造读面。
func NewDockerService(store *dockerstate.Store, devices DockerDeviceReader,
	cfg AgentConfigGetter, log logger.LoggerInterface) *DockerService {
	return &DockerService{store: store, devices: devices, cfg: cfg, log: log, now: time.Now}
}

// snapshotInterval 返回当前快照周期（秒）。
//
// 它**不是**给 agent 用的（那是 hello_ack 里的 DockerConfig），而是给陈旧度判定与
// 响应里的 snapshotInterval 字段用：前端算「同步于 N 秒前」的文案与后端判 stale
// 必须同源，两边各自硬编码会让它们对「多久算陈旧」给出不同答案。
func (s *DockerService) snapshotInterval(ctx context.Context) int {
	secs := s.cfg.GetInt(ctx, ConfigDockerSnapshotInterval, 30)
	if secs < minSnapshotIntervalSec {
		// 非法值（小于协议下限、负数）按缺省处理：否则阈值会小到把一份**刚到的**
		// 快照判成陈旧，页面永远顶着「数据已过期」而运维查不出原因。
		return 30
	}
	return secs
}

// offlineThresholdSec 返回**实际生效**的离线判定阈值（秒）。
//
// 与 DeviceService.offlineThresholdSec 同源（同一配置键、同一缺省、同一钳制）：
// 「主机是否在线」在设备列表与 docker 主机清单里必须是同一句话 —— 两处各写一套
// 阈值，配置一热更就会出现「设备列表说在线、docker 页说离线」这种自相矛盾，
// 而用户会照着自己那一页的结论去排障。
func (s *DockerService) offlineThresholdSec(ctx context.Context) int {
	sec := s.cfg.GetInt(ctx, ConfigOfflineThreshold, 30)
	if sec <= 0 {
		// <=0 视为未配置/配置错误：回落缺省。不返回 0 —— 阈值 0 会让
		// last_seen_at >= now 恒不成立，等于把**所有**主机判成离线。
		sec = 30
	}
	return sec
}

// Hosts 返回可管主机清单。
//
// 枚举源是 **docker:hosts 集合**（不是设备表）：没上报过快照的机器不是「可管主机」，
// 列出来只会让人点进去看到空白页。设备删除时集合同步 SREM（见 PurgeDevice）。
func (s *DockerService) Hosts(ctx context.Context) (*response.DockerHostListResp, error) {
	ids, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, apperror.Internal("读取主机清单失败", err)
	}
	interval := s.snapshotInterval(ctx)
	now := s.now()
	// 阈值只取一次：同一响应里每台主机的 Online 必须共用一个「现在」，
	// 逐行取会在跨秒边界上让两台设备用上不同阈值。
	onlineSince := now.Add(-time.Duration(s.offlineThresholdSec(ctx)) * time.Second)

	out := &response.DockerHostListResp{
		List:             make([]response.DockerHostItem, 0, len(ids)),
		SnapshotInterval: interval,
	}
	for _, id := range ids {
		env, err := s.store.Get(ctx, id)
		if err != nil {
			// 单台读失败不拖垮整张清单（少一行好过整页 500），也**不清理** ——
			// 见下方设备查询失败分支的说明。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", id), zap.Error(err))
			}
			continue
		}
		dev, err := s.devices.FindByID(ctx, id)
		if err != nil || dev == nil {
			switch {
			case err != nil && !errors.Is(err, repository.ErrNotFound):
				// 读库失败（DB 抖动/超时）：**原地跳过**。把瞬时故障当成「设备已删」
				// 去清理，会一次抹掉全部主机的快照键，页面从此空白到下一次上报。
				if s.log != nil {
					s.log.Warn("docker host device lookup failed", zap.Uint64("deviceId", id), zap.Error(err))
				}
			default:
				// 设备确已删除（仓储的未命中哨兵，或返回了空设备）而集合里还留着它：
				// 顺手清理（自愈），否则每次列表都要跳过这一行，且键永久泄漏。
				if perr := s.store.Purge(ctx, id); perr != nil && s.log != nil {
					s.log.Warn("docker host purge failed", zap.Uint64("deviceId", id), zap.Error(perr))
				}
			}
			continue
		}
		stale, _ := dockerstate.Stale(env, now, interval)
		item := response.DockerHostItem{
			ID:        id,
			Hostname:  dev.Hostname,
			PrimaryIP: normalizedIP(dev.PrimaryIP),
			// 与设备列表 toListItem 的在线判定逐字同句（阈值来源已在上面统一）。
			Online: dev.LastSeenAt != nil && dev.LastSeenAt.After(onlineSince),
			Stale:  stale,
		}
		if env != nil {
			item.DockerOK = env.State.DockerOK
			item.Error = env.State.Error
			item.Containers = len(env.State.Containers)
			item.Images = len(env.State.Images)
			// LastSync 用 core **收到**帧的时刻（不是 state.t）：agent 时钟可能偏
			//（.106 实测快 8 小时），用载荷时间戳会让页面写出「同步于 8 小时前」
			// 这种荒谬结论，而排障的人会照着那个结论去查一个不存在的问题。
			item.LastSync = env.ReceivedAt / 1000
			if c := env.State.Compose; c != nil {
				item.ComposeFlavor = c.Flavor
				item.ComposeVersion = c.Version
			}
		}
		// env == nil（集合里有它、快照键却被删）：按「从未上报」处理 —— DockerOK=false、
		// 计数 0、LastSync=0，而不是把它伪装成一台「零容器零镜像」的正常主机。
		out.List = append(out.List, item)
	}
	return out, nil
}

// State 返回一台主机的完整快照（含陈旧结论）。
//
// 两处与 Hosts 不同的取舍：
//   - 设备不存在 → 404（而不是一份空快照）：ID 打错时给出「设备不存在」比让人
//     对着一页空白猜更诚实；
//   - 从未上报 → NeverReported=true，且五个清单都是**空数组而非 null**：
//     前端少一层判空，「从未上报」与「陈旧」也就能各说各的话。
func (s *DockerService) State(ctx context.Context, deviceID uint64) (*response.DockerStateResp, error) {
	if _, err := s.devices.FindByID(ctx, deviceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("设备不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	interval := s.snapshotInterval(ctx)
	now := s.now()
	env, err := s.store.Get(ctx, deviceID)
	if err != nil {
		return nil, apperror.Internal("读取资源快照失败", err)
	}
	stale, ageMs := dockerstate.Stale(env, now, interval)
	out := &response.DockerStateResp{
		Stale:         stale,
		AgeSeconds:    ageMs / 1000,
		NeverReported: env == nil,
		Containers:    []response.DockerContainerItem{},
		Images:        []response.DockerImageItem{},
		Volumes:       []response.DockerVolumeItem{},
		Networks:      []response.DockerNetworkItem{},
		Projects:      []response.DockerProjectItem{},
	}
	if env == nil {
		return out, nil
	}
	out.LastSync = env.ReceivedAt / 1000
	out.DockerOK = env.State.DockerOK
	out.Error = env.State.Error
	if c := env.State.Compose; c != nil {
		out.Compose = &response.DockerComposeInfoResp{Flavor: c.Flavor, Version: c.Version}
	}
	for _, c := range env.State.Containers {
		out.Containers = append(out.Containers, toContainerItem(c))
	}
	for _, i := range env.State.Images {
		out.Images = append(out.Images, toImageItem(i))
	}
	for _, v := range env.State.Volumes {
		out.Volumes = append(out.Volumes, toVolumeItem(v))
	}
	for _, n := range env.State.Networks {
		out.Networks = append(out.Networks, toNetworkItem(n))
	}
	for _, p := range env.State.Projects {
		out.Projects = append(out.Projects, toProjectItem(p))
	}
	return out, nil
}

// PurgeDevice 是设备删除时的连带清理。
//
// 不做它，docker:state:<id> 会永久占着 Redis，而 docker:hosts 里的成员会让已删设备
// 一直出现在主机切换器里（列表按集合枚举，不是按设备表）。
func (s *DockerService) PurgeDevice(ctx context.Context, deviceID uint64) error {
	return s.store.Purge(ctx, deviceID)
}

// ── 协议载荷 → HTTP 响应 的逐字段映射 ─────────────────────────────────
//
// 为什么手写而不是 JSON 往返/反射：两边字段名不同（snake_case ↔ camelCase），
// 往返映射要先解进 map 再按名字取值，字段改名时那是**静默丢字段**（页面上少一列，
// 不会有人报错）；手写时字段名写错是编译错误。代价是「漏写一个字段」也可能悄悄
// 发生，故每个映射函数都必须把源结构的字段**列全**，测试逐个字段断言。

func toContainerItem(c agentproto.DockerContainer) response.DockerContainerItem {
	item := response.DockerContainerItem{
		ID:             c.ID,
		Name:           c.Name,
		Image:          c.Image,
		State:          c.State,
		StatusText:     c.StatusText,
		Created:        c.Created,
		StartedAt:      c.StartedAt,
		CPUPercent:     c.CPUPercent,
		MemUsageMB:     c.MemUsageMB,
		MemLimitMB:     c.MemLimitMB,
		NetRXBytesSec:  c.NetRXBytesSec,
		NetTXBytesSec:  c.NetTXBytesSec,
		ComposeProject: c.ComposeProject,
		ComposeService: c.ComposeService,
		Protected:      c.Protected,
	}
	if len(c.Ports) > 0 {
		item.Ports = make([]response.DockerPortItem, 0, len(c.Ports))
		for _, p := range c.Ports {
			item.Ports = append(item.Ports, response.DockerPortItem{
				IP:          p.IP,
				PrivatePort: p.PrivatePort,
				PublicPort:  p.PublicPort,
				Type:        p.Type,
			})
		}
	}
	return item
}

func toImageItem(i agentproto.DockerImage) response.DockerImageItem {
	return response.DockerImageItem{
		ID:       i.ID,
		RepoTags: slices.Clone(i.RepoTags),
		SizeMB:   i.SizeMB,
		Created:  i.Created,
		InUse:    i.InUse,
		Dangling: i.Dangling,
		InUseBy:  slices.Clone(i.InUseBy),
	}
}

func toVolumeItem(v agentproto.DockerVolume) response.DockerVolumeItem {
	item := response.DockerVolumeItem{
		Name:      v.Name,
		Driver:    v.Driver,
		InUse:     v.InUse,
		MountedBy: slices.Clone(v.MountedBy),
	}
	if v.SizeMB != nil {
		// 复制值而不是共享指针：响应与 Redis 里那份快照不该引用同一块存储 ——
		// 本路径只读，但共享指针是「谁改了它」这类 bug 的温床。
		mb := *v.SizeMB
		item.SizeMB = &mb
	}
	return item
}

func toNetworkItem(n agentproto.DockerNetwork) response.DockerNetworkItem {
	return response.DockerNetworkItem{
		Name:            n.Name,
		Driver:          n.Driver,
		Scope:           n.Scope,
		Internal:        n.Internal,
		ContainersCount: n.ContainersCount,
	}
}

func toProjectItem(p agentproto.DockerProject) response.DockerProjectItem {
	return response.DockerProjectItem{
		Name:            p.Name,
		ConfigFiles:     slices.Clone(p.ConfigFiles),
		State:           p.State,
		Services:        p.Services,
		ContainersCount: p.ContainersCount,
	}
}
