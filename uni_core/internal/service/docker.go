package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
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
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, err
	}
	interval := s.snapshotInterval(ctx)
	now := s.now()
	// 阈值只取一次：同一响应里每台主机的 Online 必须共用一个「现在」，
	// 逐行取会在跨秒边界上让两台设备用上不同阈值。
	onlineSince := now.Add(-time.Duration(s.offlineThresholdSec(ctx)) * time.Second)

	out := &response.DockerHostListResp{
		List:             make([]response.DockerHostItem, 0, len(records)),
		SnapshotInterval: interval,
	}
	for _, rec := range records {
		if rec.readErr != nil {
			// 单台读失败不拖垮整张清单（少一行好过整页 500）。不清理、也不编造
			// 「零容器零镜像」的假象 —— 那台机器的事实到总览页再以 error 字段如实呈现。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			continue
		}
		out.List = append(out.List, toHostItem(rec, onlineSince, interval, now))
	}
	return out, nil
}

// hostRecords 枚举可管主机并读出各自的中间事实：快照信封 + 设备行。
//
// 「设备已删 → Purge 自愈、设备查库失败 → 告警并跳过」这两条口径与 Hosts 原先的
// 循环逐字同参，搬出来是因为 Overview 也要走同一趟枚举 —— 枚举口径（清理解除队列、
// 跳过政策、告警文案）有两份代码，就会有一半主机消失在总览页而没人报错。
//
// 唯一留给调用方裁决的是**快照读失败**（rec.readErr）：清单页的取舍是「少一行」
// （读失败在 Hosts 里 warn 后跳过），总览页的取舍是「行还在、error 如实」——
// 两者都要真的读过才知道该选哪边。
func (s *DockerService) hostRecords(ctx context.Context) ([]hostRecord, error) {
	ids, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, apperror.Internal("读取主机清单失败", err)
	}
	out := make([]hostRecord, 0, len(ids))
	for _, id := range ids {
		rec := hostRecord{id: id}
		env, err := s.store.Get(ctx, id)
		if err != nil {
			// 快照读失败**不是**「设备已删」：设备行照常查（否则总览页连主机名
			// 都给不出），呈现或不呈现由调用方裁决。
			rec.readErr = err
		} else {
			rec.env = env
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
		rec.dev = dev
		out = append(out, rec)
	}
	return out, nil
}

// hostRecord 是枚举一台主机的中间事实（快照信封 + 设备行 + 读结论）。
type hostRecord struct {
	id uint64
	// env 是快照信封；readErr 非 nil 或从未上报时为 nil —— 两者靠 readErr 区分：
	// env==nil 且无错是「从未上报」（集合里有它、快照键却被删/没写过），是正常态。
	env *dockerstate.Envelope
	dev *entity.Device
	// readErr 是快照**读失败**（键值损坏/Redis 抖动），不是业务性「无数据」。
	readErr error
}

// toHostItem 把一台主机的枚举记录映射成主机条目。Hosts 与 Overview 共用：
// 主机条目在两张页面里必须说同一句话（同一 online/stale 结论、同一计数口径）。
func toHostItem(rec hostRecord, onlineSince time.Time, interval int, now time.Time) response.DockerHostItem {
	stale, _ := dockerstate.Stale(rec.env, now, interval)
	item := response.DockerHostItem{
		ID:        rec.id,
		Hostname:  rec.dev.Hostname,
		PrimaryIP: normalizedIP(rec.dev.PrimaryIP),
		// 与设备列表 toListItem 的在线判定逐字同句（阈值来源已在上面统一）。
		Online: rec.dev.LastSeenAt != nil && rec.dev.LastSeenAt.After(onlineSince),
		Stale:  stale,
	}
	if rec.readErr != nil {
		// 读失败如实标注（与 State() 的「读取资源快照失败」同一句话）：不编造
		// DockerOK=true、也不编造计数 —— 不知道就是不知道。
		item.Error = dockerStateReadFailed
		return item
	}
	if rec.env != nil {
		item.DockerOK = rec.env.State.DockerOK
		item.Error = rec.env.State.Error
		item.Containers = len(rec.env.State.Containers)
		item.Images = len(rec.env.State.Images)
		// LastSync 用 core **收到**帧的时刻（不是 state.t）：agent 时钟可能偏
		//（.106 实测快 8 小时），用载荷时间戳会让页面写出「同步于 8 小时前」
		// 这种荒谬结论，而排障的人会照着那个结论去查一个不存在的问题。
		item.LastSync = rec.env.ReceivedAt / 1000
		if c := rec.env.State.Compose; c != nil {
			item.ComposeFlavor = c.Flavor
			item.ComposeVersion = c.Version
		}
	}
	// env == nil（集合里有它、快照键却被删）：按「从未上报」处理 —— DockerOK=false、
	// 计数 0、LastSync=0，而不是把它伪装成一台「零容器零镜像」的正常主机。
	return item
}

// dockerStateReadFailed 是「快照读失败」在主机条目 error 字段里的结论句：
// 与 State() 的 500 文案同源，避免同一故障在两张页面说出两句话。
const dockerStateReadFailed = "读取资源快照失败"

// dockerOverviewAnomalyCap 是总览页异常清单的上限：控制塔的异常清单是**抽查**
// 不是全表 —— 收全表会让响应随舰队规模线性膨胀，而 50 条之外的内容读者会在
// 对应的主机清单页里看到（Total 字段会告诉他「还有更多」）。
const dockerOverviewAnomalyCap = 50

// dockerWorkloadCap 是统一工作负载表的上限：控制塔的工作面板要的是**一眼可扫**，
// 不是全量转储 —— 收全表会让响应随舰队规模线性膨胀。与总览异常清单的 50 不同
// （那边是抽查），这边是 500（日常操作的工作集），但「截断先报全量（Total）」
// 两边同一句；500 条之外的用 hostId/state/keyword 收窄去看。
const dockerWorkloadCap = 500

// Overview 返回控制塔总览：跨主机的航队 KPI、主机清单与异常容器清单。
//
// 三条与 Hosts/State 不同的取舍（每一条都是「为什么」级的决策）：
//
//  1. 部分聚合优于整体失败：单台快照读失败只记 warn 并**继续**，那台主机仍列进
//     hosts（error 字段如实标注，行还在），只是它的资源不参与 KPI —— 没有数据
//     还把它算进去，会稀释全局计数；
//  2. stale 主机的数据**照常计入 KPI**：陈旧意味着「最后已知事实过期了」，
//     不是「事实不存在」。控制塔要的是「过期的真相」，由条目上的 stale 标记
//     告诉读者打折看；
//  3. 异常清单只收非 running 容器，按主机排序、上限 50 条：fleet 负责全量计数
//     （含 running/stopped 的数学恒等式 Total=Running+Stopped），清单负责给
//     运维一个按主机排好的「去哪儿看」入口。
func (s *DockerService) Overview(ctx context.Context) (*response.DockerOverviewResp, error) {
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, err
	}
	interval := s.snapshotInterval(ctx)
	now := s.now()
	onlineSince := now.Add(-time.Duration(s.offlineThresholdSec(ctx)) * time.Second)

	var fleet response.DockerOverviewFleet
	// 空数组而非 null：与 State() 的五清单同一约定（前端少一层判空）。
	anomalies := []response.DockerOverviewAnomalyItem{}
	out := &response.DockerOverviewResp{
		Hosts:     make([]response.DockerHostItem, 0, len(records)),
		Anomalies: response.DockerOverviewAnomalies{Items: anomalies},
	}
	for _, rec := range records {
		item := toHostItem(rec, onlineSince, interval, now)
		fleet.Hosts.Total++
		if item.DockerOK {
			fleet.Hosts.DockerOK++
		}
		if rec.env == nil {
			// 从未上报 / 读失败：没有资源事实可聚合（主机的行与结论已在 hosts 里）。
			out.Hosts = append(out.Hosts, item)
			continue
		}
		st := rec.env.State
		// 磁盘账目（6a）与计数类 KPI 同一循环里收：df 汇总只对**有 df 数据**的主机
		// 求和（disk.hosts 如实计数「报了磁盘账的主机数」，缺报主机不折算成零）；
		// 悬空镜像/未用卷的**计数**来自五类清单（与镜像页、卷页的判据同一源），
		// df 只补「占用字节」这一半。
		var dangling, unusedVols int
		if du := st.DiskUsage; du != nil {
			fleet.Disk.Hosts++
			fleet.Disk.ImagesTotalMB += du.ImagesTotalMB
			fleet.Disk.ImagesDanglingMB += du.ImagesDanglingMB
			fleet.Disk.VolumesTotalMB += du.VolumesTotalMB
			fleet.Disk.BuildCacheMB += du.BuildCacheMB
		}
		for _, c := range st.Containers {
			fleet.Containers.Total++
			if c.State == "running" {
				fleet.Containers.Running++
			}
			if c.Protected {
				fleet.Containers.Protected++
			}
			if c.State != "running" {
				anomalies = append(anomalies, response.DockerOverviewAnomalyItem{
					ID:         c.ID,
					HostID:     rec.id,
					Hostname:   rec.dev.Hostname,
					Name:       c.Name,
					Image:      c.Image,
					State:      c.State,
					StatusText: c.StatusText,
					Protected:  c.Protected,
				})
			}
		}
		fleet.Images.Total += len(st.Images)
		for _, i := range st.Images {
			if !i.InUse {
				fleet.Images.Unused++
			}
			if i.Dangling {
				dangling++
			}
		}
		fleet.Volumes.Total += len(st.Volumes)
		for _, v := range st.Volumes {
			if !v.InUse {
				fleet.Volumes.Unused++
				unusedVols++
			}
		}
		fleet.Networks.Total += len(st.Networks)
		for _, p := range st.Projects {
			fleet.Projects.Total++
			if p.State == "running" {
				fleet.Projects.Running++
			}
		}
		// 主机行的磁盘账目在 append 前挂好（值拷贝进切片后再改局部 item 不会生效）。
		// st.DiskUsage=nil 时保持 nil：那台的「磁盘数据不可用」由前端如实渲染，
		// 不编零值账目（与该行 error 字段的取舍同一句话）。
		if du := st.DiskUsage; du != nil {
			item.Disk = &response.DockerHostDiskItem{
				ImagesMB: du.ImagesTotalMB, VolumesMB: du.VolumesTotalMB, BuildCacheMB: du.BuildCacheMB,
				ImagesDanglingMB: du.ImagesDanglingMB, DanglingImages: dangling, UnusedVolumes: unusedVols,
			}
		}
		out.Hosts = append(out.Hosts, item)
	}
	fleet.Containers.Stopped = fleet.Containers.Total - fleet.Containers.Running

	// 排序：主机 id 升序（同机按容器名）。集合本身顺序不保证（Redis 集合无序），
	// 不排会让「按主机排序」变成「按运气排序」。
	slices.SortFunc(anomalies, func(a, b response.DockerOverviewAnomalyItem) int {
		if a.HostID != b.HostID {
			return cmp.Compare(a.HostID, b.HostID)
		}
		return cmp.Compare(a.Name, b.Name)
	})
	out.Anomalies.Total = len(anomalies)
	if len(anomalies) > dockerOverviewAnomalyCap {
		anomalies = anomalies[:dockerOverviewAnomalyCap]
	}
	out.Anomalies.Items = anomalies
	out.Fleet = fleet
	return out, nil
}

// Workloads 返回跨主机统一工作负载表：全部可管主机的容器并成一张表，
// 每行带上「在哪台主机」（hostId/hostname）。
//
// 三条取舍全部与 Overview/Hosts 同族（枚举口径见 hostRecords 的注释）：
//
//   - 单台快照读失败记 warn 后**跳过**（与 Hosts 同款）：统一表不编造「零容器」
//     的假象 —— 那台主机的故障由总览页的 error 字段如实呈现，这里少几行；
//   - stale 主机的容器**照常计入**：陈旧是「最后已知事实过期」，不是「事实不
//     存在」，打折与否是读者的事（主机行的 stale 标记在 hosts/总览页）；
//   - 排序 hostId 升序、同主机容器名升序：集合枚举顺序不保证（Redis 集合
//     无序），不排就变成「按运气排序」（与总览异常清单同一排序纪律）。
func (s *DockerService) Workloads(ctx context.Context, q *request.DockerWorkloadQuery) (*response.DockerWorkloadListResp, error) {
	if q == nil {
		q = &request.DockerWorkloadQuery{}
	}
	if q.State != "" && q.State != "running" && q.State != "stopped" {
		// 400 结论句而不是静默当不过滤：静默会让用户以为「筛了但没生效」。
		return nil, apperror.BadRequest("参数错误: state 仅支持 running 或 stopped")
	}
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, err
	}
	// 关键字只折叠一次（每行的 name/image 两处 Contains 都在它之上做），
	// 而不是每个容器对同一个词重复 ToLower。
	kw := strings.ToLower(q.Keyword)
	// 空数组而非 null：与 Hosts/State/Overview 的清单同一约定（前端少一层判空）。
	items := []response.DockerWorkloadItem{}
	for _, rec := range records {
		if q.HostID != 0 && rec.id != q.HostID {
			continue
		}
		if rec.readErr != nil {
			// 单台读失败不拖垮整张表（少几行好过整页 500），文案与 Hosts 同一句。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			continue
		}
		if rec.env == nil {
			// 从未上报：没有容器事实可列（主机行与结论在 hosts/总览页给出）。
			continue
		}
		for _, c := range rec.env.State.Containers {
			// stopped = 一切非 running（与总览 Stopped=Total-Running 同一句）。
			if q.State == "running" && c.State != "running" {
				continue
			}
			if q.State == "stopped" && c.State == "running" {
				continue
			}
			if kw != "" &&
				!strings.Contains(strings.ToLower(c.Name), kw) &&
				!strings.Contains(strings.ToLower(c.Image), kw) {
				continue
			}
			// 条目换算复用 toContainerItem（字段列全是它自己的纪律），
			// 归属两列是统一表唯一的新增 —— 平行造字段会在加列时静默漏掉。
			items = append(items, response.DockerWorkloadItem{
				DockerContainerItem: toContainerItem(c),
				HostID:              rec.id,
				Hostname:            rec.dev.Hostname,
			})
		}
	}
	// 排序在截断**之前**：截断砍掉的必须是「排序后的尾部」，否则砍谁取决于
	// 枚举运气（同一份数据两次请求会给出不同的前 500 行）。
	slices.SortFunc(items, func(a, b response.DockerWorkloadItem) int {
		if a.HostID != b.HostID {
			return cmp.Compare(a.HostID, b.HostID)
		}
		return cmp.Compare(a.Name, b.Name)
	})
	// total 如实报全量数，items 截断（与总览 anomalies 同一截断口径）。
	out := &response.DockerWorkloadListResp{Total: len(items)}
	if len(items) > dockerWorkloadCap {
		items = items[:dockerWorkloadCap]
	}
	out.Items = items
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
		Protected: v.Protected,
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
		Protected:       p.Protected,
	}
}
