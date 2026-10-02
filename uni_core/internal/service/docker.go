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

// DockerStatsHistoryReader 是容器 stats 留存的读取与清理能力面（由
// dockerstate.StatsHistoryStore 满足）：读面查询历史曲线，删除设备时连带清理。
type DockerStatsHistoryReader interface {
	History(ctx context.Context, deviceID uint64, containerID string) ([]dockerstate.StatsSample, error)
	Purge(ctx context.Context, deviceID uint64) error
}

// DockerService 是 docker 域的读面。
type DockerService struct {
	store   *dockerstate.Store
	devices DockerDeviceReader
	cfg     AgentConfigGetter
	log     logger.LoggerInterface
	// stats 是容器 stats 留存的读面（P2）。nil = 未装配：stats-history 端点给出
	// 500（装配错误早暴露，好过假装一条空曲线 —— 那会让「容器没有历史」和
	//「留存没接上」在页面上不可区分）。
	stats DockerStatsHistoryReader
	// now 可替换：陈旧度与年龄都是「相对现在」的结论，测试要一个确定的现在
	//（否则断言会随执行时刻漂移，跨秒边界上还会随机红）。
	now func() time.Time
}

// NewDockerService 构造读面。
func NewDockerService(store *dockerstate.Store, devices DockerDeviceReader,
	cfg AgentConfigGetter, log logger.LoggerInterface) *DockerService {
	return &DockerService{store: store, devices: devices, cfg: cfg, log: log, now: time.Now}
}

// WithStatsHistory 注入 stats 留存读面（P2）。装配在 wireup 一处完成。
func (s *DockerService) WithStatsHistory(h DockerStatsHistoryReader) *DockerService {
	s.stats = h
	return s
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
				// 快照键与 stats 留存一起清（与 PurgeDevice 同一条清理纪律）。
				if perr := s.store.Purge(ctx, id); perr != nil && s.log != nil {
					s.log.Warn("docker host purge failed", zap.Uint64("deviceId", id), zap.Error(perr))
				}
				if s.stats != nil {
					if perr := s.stats.Purge(ctx, id); perr != nil && s.log != nil {
						s.log.Warn("docker stats history purge failed", zap.Uint64("deviceId", id), zap.Error(perr))
					}
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
		//
		// 求和口径照抄 agent 给的数（六期对账后它已是 `docker system df` 的同口径：
		// 镜像合计 = 层存储、悬空可回收 = 执行 image:prune 真会释放的字节）——
		// 这里再算一遍会是第三份口径，而与 CLI 对不上时无从判断是哪一层算歪了。
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

// ── 跨主机资源清单（9a）：镜像/卷/网络/项目四页与容器统一表同一副骨架 ──────

// dockerResourceCap 是跨主机资源清单（四页）的条目上限。
//
// 与容器统一表的 dockerWorkloadCap 同值同理由（500 = 日常操作的工作集，截断
// 先报全量 Total）；四页共用一条常量而不是各写一份：它们与容器页是同一类
// 工作面板，各自为政时调一处就会让五张表的截断口径分家。
const dockerResourceCap = 500

// resourceRow 是跨主机聚合的中间行：最终条目 + 排序键。
// 排序键不进 DTO：「名称」只是排序依据（镜像取第一个 repoTag、其余取 Name），
// 不必出现在响应里；放在这里而不是另造四个带名字的结构体。
type resourceRow[T any] struct {
	hostID uint64
	name   string
	item   T
}

// aggregateResourceRows 跑完四页资源清单（镜像/卷/网络/项目）的公共骨架：
// hostRecords 枚举 → hostId 过滤 → 单台读失败 warn 跳过 → 逐条过筛 → 排序
// （hostId 升序、同主机按名称）→ 截断 500 但 total 如实报全量。
//
// 为什么抽出来而不是四个方法各抄一遍：这段骨架里全是**口径** —— 跳过政策
// （读失败 = 少几行、不编造「零条目」的假象）、stale 照常计入、排序先于截断、
// 截断报全量 —— 四份拷贝就是四次分岔机会（Workloads 已为「排序必须全序」写过
// 一次注释；再抄四遍，改一处就会漏掉三处）。每条清单的差异全在闭包里：
// 取哪类源（srcs）、怎么过筛（match）、排序取什么名字（name）、按主机的派生
// 数据（onHost）、条目怎么装配（row）。
//
// onHost 给「按主机、与行筛选无关」的派生数据一条与清单行**同一趟枚举**的通道
//（目前只有镜像页的可回收账目用它：disk 的覆盖范围必须与行的主机范围同源）。
// 为什么必须同趟：账目与行要来自同一次快照读 —— 分两趟枚举（再跑一次 hostRecords）
// 会在两次读之间漂移，行与账目就可能属于不同的主机集合；而且快照读是这条链上
// 唯一的 I/O，白翻一倍的读不值得。为什么是回调不是第二个返回值：只有镜像页需要
// 它，回吐全量 hostRecord 会诱导调用方自己再抄一遍跳过政策 —— 本函数抽出来正是
// 为了让那份政策只有一份。回调在主机过滤后、读失败/从未上报跳过之后按台触发一次，
// **不经 match 过筛**（派生账目说的是「这台主机有哪些可回收」，与「此刻在看哪些行」
// 无关）；nil = 不收集（其余三页没有这类派生数据）。回调与行循环同循环执行，
// 返回前不用额外对齐顺序 —— 但回调收集出来的切片顺序跟着枚举顺序走（Redis 集合
// 无序），调用方自己负责排序（与行同一纪律）。
//
// 为什么是自由函数而不是方法：Go 的方法不能带类型参数（四个条目类型不同，
// 泛型只能落在函数上）；s 只是被借来跑 hostRecords 枚举与读失败告警。
//
// 与 Workloads 的分工：容器统一表保留自己的专用方法（切片 2 已定稿，本切片
// 零改动）；两者共用的是 hostRecords 枚举与同一套纪律，不是同一份代码。
func aggregateResourceRows[T any, S any](
	s *DockerService,
	ctx context.Context,
	hostID uint64,
	srcs func(st *agentproto.DockerState) []S,
	match func(src S) bool,
	name func(src S) string,
	onHost func(hostID uint64, st *agentproto.DockerState),
	row func(hostID uint64, hostname string, src S) T,
) ([]T, int, error) {
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows := []resourceRow[T]{}
	for _, rec := range records {
		if hostID != 0 && rec.id != hostID {
			continue
		}
		if rec.readErr != nil {
			// 单台读失败不拖垮整张表（少几行好过整页 500），文案与 Hosts/Workloads 同一句。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			continue
		}
		if rec.env == nil {
			// 从未上报：没有资源事实可列（主机行与结论在 hosts/总览页给出）。
			continue
		}
		if onHost != nil {
			onHost(rec.id, &rec.env.State)
		}
		for _, src := range srcs(&rec.env.State) {
			if match != nil && !match(src) {
				continue
			}
			rows = append(rows, resourceRow[T]{hostID: rec.id, name: name(src), item: row(rec.id, rec.dev.Hostname, src)})
		}
	}
	// 排序在截断**之前**：截断砍掉的必须是排序后的尾部，否则砍谁取决于枚举
	// 运气（同一份数据两次请求会给出不同的前 500 行）。hostId 升序、同主机按
	// 名称 —— Redis 集合枚举顺序不保证，不排就是「按运气排序」（与 Workloads
	// 及其总览异常清单同一纪律）；stale 主机的条目照常计入（陈旧是「最后已知
	// 事实过期」，不是「事实不存在」，打折与否是读者的事）。
	slices.SortFunc(rows, func(a, b resourceRow[T]) int {
		if a.hostID != b.hostID {
			return cmp.Compare(a.hostID, b.hostID)
		}
		return cmp.Compare(a.name, b.name)
	})
	// total 如实报全量数，items 截断（与统一工作负载表同一截断口径）。
	total := len(rows)
	if len(rows) > dockerResourceCap {
		rows = rows[:dockerResourceCap]
	}
	// 空数组而非 null：与 Hosts/State/Overview/Workloads 的清单同一约定（前端少一层判空）。
	items := make([]T, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.item)
	}
	return items, total, nil
}

// imageResourceName 是镜像在统一表里的**排序名称**：第一个 repoTag（展示身份），
// 无标签的悬空镜像回落 ID（sha256 是它唯一稳定的身份）。排序必须**全序**且确定：
// 取的是与展示同一个值，读者看到的顺序就是服务端排的顺序。
func imageResourceName(i agentproto.DockerImage) string {
	if len(i.RepoTags) > 0 {
		return i.RepoTags[0]
	}
	return i.ID
}

// Images 返回跨主机镜像统一表：全部可管主机的镜像并成一张表，每行带「在哪台主机」。
//
// 过滤语义与前端镜像页的开关同一句（utils/snapshot.ts 的 filterImages）：
// keyword 对 repoTag 子串、大小写不敏感（多标签 join(" ") 后匹配）；dangling /
// unused 是三值指针布尔（nil=不过滤、true=取该侧、false=取其反侧，与设备列表
// Online 过滤同一句）。跨主机的跳过/排序/截断口径全在 aggregateResourceRows。
//
// 响应同时带 disk（主机范围内的可回收账目，供底栏「N 个可回收 · X」）—— 它与
// Items 的筛选/截断口径**无关**（只跟 hostId 收窄），两种口径的分工见
// response.DockerImageListResp.Disk 的注释。
func (s *DockerService) Images(ctx context.Context, q *request.DockerImageQuery) (*response.DockerImageListResp, error) {
	if q == nil {
		// handler 永远传非 nil；这里对 nil 的防御语义与 Workloads 一致，
		// 避免直接调用方的每个测试都先写一层判空。
		q = &request.DockerImageQuery{}
	}
	kw := strings.ToLower(q.Keyword)
	// disk 随主机的同趟枚举收集（见 aggregateResourceRows 的 onHost）：账目与行
	// 必须来自同一次快照读。只收「账目完整」的主机：快照可读但没有 df 数据
	//（旧版 agent / df 采集失败）的缺席而不是记零 ——「缺席 = 不知道」。
	disk := []response.DockerImageHostDiskItem{}
	items, total, err := aggregateResourceRows(s, ctx, q.HostID,
		func(st *agentproto.DockerState) []agentproto.DockerImage { return st.Images },
		func(i agentproto.DockerImage) bool {
			if q.Dangling != nil && i.Dangling != *q.Dangling {
				return false
			}
			if q.Unused != nil && i.InUse == *q.Unused {
				// unused 与 InUse 是一对反义词：数据侧与请求侧相等即不匹配
				//（unused=true 只要未被使用的、false 只要在用的）。
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(strings.Join(i.RepoTags, " ")), kw)
		},
		imageResourceName,
		func(hostID uint64, st *agentproto.DockerState) {
			du := st.DiskUsage
			if du == nil {
				return
			}
			// 计数与总览 DanglingImages、镜像表「可回收（无标签）」同一判据
			//（agent 的 Dangling 结论，前端不重算）；不经过 q 的过滤 ——
			// prune 回收的就是这台的可回收批，与「此刻在看哪些行」无关。
			n := 0
			for _, i := range st.Images {
				if i.Dangling {
					n++
				}
			}
			disk = append(disk, response.DockerImageHostDiskItem{
				HostID: hostID, DanglingCount: n, DanglingMB: du.ImagesDanglingMB,
			})
		},
		func(hostID uint64, hostname string, i agentproto.DockerImage) response.DockerImageListItem {
			// 条目换算复用 toImageItem（字段列全是它自己的纪律），归属两列是唯一新增。
			return response.DockerImageListItem{DockerImageItem: toImageItem(i), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	// hostRecords 的枚举顺序不保证（Redis 集合无序）：disk 与行同一纪律排序
	//（hostId 升序），否则同一份数据两次请求会给出不同的主机顺序。
	slices.SortFunc(disk, func(a, b response.DockerImageHostDiskItem) int {
		return cmp.Compare(a.HostID, b.HostID)
	})
	return &response.DockerImageListResp{Items: items, Total: total, Disk: disk}, nil
}

// Volumes 返回跨主机卷统一表：全部可管主机的数据卷并成一张表，每行带「在哪台主机」。
//
// 过滤语义与前端卷页的开关同一句（filterVolumes）：keyword 对卷名子串、
// 大小写不敏感；unused 三值指针布尔（同 Images）。
func (s *DockerService) Volumes(ctx context.Context, q *request.DockerVolumeQuery) (*response.DockerVolumeListResp, error) {
	if q == nil {
		q = &request.DockerVolumeQuery{}
	}
	kw := strings.ToLower(q.Keyword)
	items, total, err := aggregateResourceRows(s, ctx, q.HostID,
		func(st *agentproto.DockerState) []agentproto.DockerVolume { return st.Volumes },
		func(v agentproto.DockerVolume) bool {
			if q.Unused != nil && v.InUse == *q.Unused {
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(v.Name), kw)
		},
		func(v agentproto.DockerVolume) string { return v.Name },
		nil, // 卷页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, v agentproto.DockerVolume) response.DockerVolumeListItem {
			return response.DockerVolumeListItem{DockerVolumeItem: toVolumeItem(v), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerVolumeListResp{Items: items, Total: total}, nil
}

// Networks 返回跨主机网络统一表：全部可管主机的网络并成一张表，每行带「在哪台主机」。
//
// 过滤语义与前端网络页同一句（filterNetworks）：keyword 对网络名子串、
// 大小写不敏感；internal 三值指针布尔（internal=true 只要隔离网络）。
func (s *DockerService) Networks(ctx context.Context, q *request.DockerNetworkQuery) (*response.DockerNetworkListResp, error) {
	if q == nil {
		q = &request.DockerNetworkQuery{}
	}
	kw := strings.ToLower(q.Keyword)
	items, total, err := aggregateResourceRows(s, ctx, q.HostID,
		func(st *agentproto.DockerState) []agentproto.DockerNetwork { return st.Networks },
		func(n agentproto.DockerNetwork) bool {
			if q.Internal != nil && n.Internal != *q.Internal {
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(n.Name), kw)
		},
		func(n agentproto.DockerNetwork) string { return n.Name },
		nil, // 网络页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, n agentproto.DockerNetwork) response.DockerNetworkListItem {
			return response.DockerNetworkListItem{DockerNetworkItem: toNetworkItem(n), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerNetworkListResp{Items: items, Total: total}, nil
}

// Projects 返回跨主机项目统一表：全部可管主机的编排项目并成一张表，每行带「在哪台主机」。
//
// state 口径与容器统一表同一句：running / stopped，stopped = 一切非 running
// （项目态的 partial 归入 stopped —— 过滤只做二分，细分由条目上的 state 字段
// 承载，与总览 Projects.Running 的 KPI 口径同源）；非法值 400 结论句而不是
// 静默当不过滤（与 Workloads 同一句纪律）。
func (s *DockerService) Projects(ctx context.Context, q *request.DockerProjectQuery) (*response.DockerProjectListResp, error) {
	if q == nil {
		q = &request.DockerProjectQuery{}
	}
	if q.State != "" && q.State != "running" && q.State != "stopped" {
		return nil, apperror.BadRequest("参数错误: state 仅支持 running 或 stopped")
	}
	kw := strings.ToLower(q.Keyword)
	items, total, err := aggregateResourceRows(s, ctx, q.HostID,
		func(st *agentproto.DockerState) []agentproto.DockerProject { return st.Projects },
		func(p agentproto.DockerProject) bool {
			if q.State == "running" && p.State != "running" {
				return false
			}
			if q.State == "stopped" && p.State == "running" {
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(p.Name), kw)
		},
		func(p agentproto.DockerProject) string { return p.Name },
		nil, // 项目页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, p agentproto.DockerProject) response.DockerProjectListItem {
			return response.DockerProjectListItem{DockerProjectItem: toProjectItem(p), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerProjectListResp{Items: items, Total: total}, nil
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

// ContainerStatsHistory 返回一台主机上一个容器的 stats 留存序列（按 t 升序，
// 至多 dockerstate.StatsHistoryKeep 条 ≈ 30 分钟窗口）。
//
// 三条与 State 同族的取舍：
//   - 设备不存在 → 404（同一句话）：ID 打错时「设备不存在」比对着一页空曲线猜诚实；
//   - 容器无历史 → 200 + samples 空数组（而非 404/null）：「容器没有留存」是正常
//     答案（刚建、留存关闭、或容器死满 30 分钟后 TTL 收走了序列 —— 最后一种页面
//     上还有快照在，两者不该在状态码上混淆）；
//   - 字段口径照抄留存序列（与快照/实时流同源），映射只做命名转换（领域直陈 →
//     DTO 驼峰），不重算任何值。
func (s *DockerService) ContainerStatsHistory(ctx context.Context, deviceID uint64, containerID string) (*response.DockerStatsHistoryResp, error) {
	if _, err := s.devices.FindByID(ctx, deviceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("设备不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	if s.stats == nil {
		return nil, apperror.Internal("stats 留存未装配")
	}
	samples, err := s.stats.History(ctx, deviceID, containerID)
	if err != nil {
		return nil, apperror.Internal("读取 stats 历史失败", err)
	}
	// 空数组而非 null：与五清单同一约定（前端少一层判空）。
	out := &response.DockerStatsHistoryResp{Samples: make([]response.DockerStatsHistorySample, 0, len(samples))}
	for _, sm := range samples {
		out.Samples = append(out.Samples, response.DockerStatsHistorySample{
			T: sm.T, CPUPercent: sm.CPUPercent, MemUsageMB: sm.MemUsageMB, MemLimitMB: sm.MemLimitMB,
		})
	}
	return out, nil
}

// PurgeDevice 是设备删除时的连带清理。
//
// 不做它，docker:state:<id> 会永久占着 Redis，而 docker:hosts 里的成员会让已删设备
// 一直出现在主机切换器里（列表按集合枚举，不是按设备表）。stats 留存键一起清
// （它带 TTL，不清也只是晚 30 分钟消失 —— 但删除是明确动作，主动清掉比等兜底快）。
func (s *DockerService) PurgeDevice(ctx context.Context, deviceID uint64) error {
	if err := s.store.Purge(ctx, deviceID); err != nil {
		return err
	}
	if s.stats != nil {
		return s.stats.Purge(ctx, deviceID)
	}
	return nil
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
