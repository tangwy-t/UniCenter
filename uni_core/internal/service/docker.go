package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

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
// 只声明 FindByID：**按 id 找一台**（State / stats-history 的存在性检查）。
// 主机枚举需要的是另一个能力面（见 DockerHostDeviceReader）—— 按 id 集合批量取。
// 合成一个接口会让任务面/审计面（它们只需要按 id 找一台）被迫依赖批量方法。
type DockerDeviceReader interface {
	FindByID(ctx context.Context, id uint64) (*entity.Device, error)
}

// DockerHostDeviceReader 是**主机枚举**（hostRecords）需要的设备能力面：单台 + 批量。
//
// 批量那一枚是主机规模的 N+1 闸：枚举一百台可管主机时，逐台 FindByID 就是 100 条
// SELECT、逐台 store.Get 就是 100 次 Redis 往返 —— 全在同一个请求里。枚举因此只允许
// 两次批量读（MGet + FindByIDs），调用次数与主机数**无关**（守卫测试断言只调用一次）。
type DockerHostDeviceReader interface {
	DockerDeviceReader
	// FindByIDs 一次取回这批设备的行（一条 id IN (...)）；缺席 = 设备已删。
	FindByIDs(ctx context.Context, ids []uint64) ([]entity.Device, error)
}

// DockerStatsHistoryReader 是容器 stats 留存的读取与清理能力面（由
// dockerstate.StatsHistoryStore 满足）：读面查询历史曲线，删除设备时连带清理。
type DockerStatsHistoryReader interface {
	History(ctx context.Context, deviceID uint64, containerID string) ([]dockerstate.StatsSample, error)
	Purge(ctx context.Context, deviceID uint64) error
}

// DockerImageTargetRepo 是镜像目标版本声明的存储面（Wave 4c：唯一实现
// repository.DockerImageTargetRepo）。声明/清除/清单/批量取四法一门 —— 批量取
// （FindByRepoTags）是漂移聚合的 N+1 闸：一次取全部标签的目标，不逐行查询。
type DockerImageTargetRepo interface {
	Upsert(ctx context.Context, row *entity.DockerImageTarget) error
	Delete(ctx context.Context, repoTag string) error
	List(ctx context.Context) ([]entity.DockerImageTarget, error)
	FindByRepoTags(ctx context.Context, repoTags []string) (map[string]entity.DockerImageTarget, error)
}

// DockerHostDiskReader 是**宿主磁盘水位**的批量读面（Wave 4d：唯一实现
// repository.DockerHostDiskRepo）。它与 docker `system df` 的占用账目（快照里的
// DiskUsage）是两码事：这里读的是设备指标宽表（device_metric_5m）里**整机**的
// disk_used_percent / disk_total_gb —— 宿主磁盘的分子与分母，「这台机器快满了吗」
// 的数据面（口径见 response.DockerHostItem.DiskUsedPercent）。
//
// 只声明**一个批量方法**：LatestWideDisk 一次取全部 hostId 的**最近一条**宽表行
// （按 device_id 取 max(bucket_ts)），是 N+1 的闸 —— 逐台查会是每主机一条 SQL。
// 返回 map[deviceID]行（未上报指标的设备**不在 map 里**：缺席 = 不知道，不是 0）。
// 只取磁盘列，其余字段留 nil（宽表 45 列，搬全行没有收益）。
type DockerHostDiskReader interface {
	LatestWideDisk(ctx context.Context, deviceIDs []uint64) (map[uint64]entity.DeviceMetricWide, error)
}

// DockerService 是 docker 域的读面。
type DockerService struct {
	store   *dockerstate.Store
	devices DockerHostDeviceReader
	cfg     AgentConfigGetter
	log     logger.LoggerInterface
	// stats 是容器 stats 留存的读面（P2）。nil = 未装配：stats-history 端点给出
	// 500（装配错误早暴露，好过假装一条空曲线 —— 那会让「容器没有历史」和
	//「留存没接上」在页面上不可区分）。
	stats DockerStatsHistoryReader
	// targets 是镜像目标版本声明的存储面（Wave 4c）。nil = 未装配：三个目标端点
	// （Set/Clear/List）给出 500（装配错误早暴露）；漂移视图则**优雅降级**为无目标
	//（不产出 targetDigest/pendingHosts/alignedCount）—— 目标是对漂移的**增强**，
	// 不该因为目标面没接上就让整张漂移表打不开。
	targets DockerImageTargetRepo
	// hostDisk 是宿主磁盘水位的批量读面（Wave 4d）。nil = 未装配：主机条目/总览的
	// diskUsedPercent/diskTotalGb 一律缺省（omitempty）—— 水位是对主机条目的**增强**
	//（答「哪台快满」），不该因为指标仓储没接上就让主机清单/总览打不开。读取**失败**
	// 同样降级为缺省（warn 后当作「没有水位数据」）：读面的设计目的是「agent 全下线
	// 时页面照样打得开」，多一条硬依赖会把这条底线换成 500。
	hostDisk DockerHostDiskReader
	// now 可替换：陈旧度与年龄都是「相对现在」的结论，测试要一个确定的现在
	//（否则断言会随执行时刻漂移，跨秒边界上还会随机红）。
	now func() time.Time

	// ── 总览短缓存（见 Overview）─────────────────────────────────────
	// overviewCacheTTL 是生效的 TTL（<=0 = 整个短缓存关闭，逐次重算）；overviewEntry
	// 是唯一一份「全舰队总览」，overviewGroup 合并 TTL 到期瞬间的并发击穿。
	// 锁只保护 entry 的读写（一次字段读，不含任何 I/O），聚合在锁外跑。
	overviewCacheTTL time.Duration
	overviewMu       sync.Mutex
	overviewEntry    *dockerOverviewCacheEntry
	overviewGroup    singleflight.Group
}

// dockerOverviewCacheEntry 是短缓存里的一格：响应 + 算出来的时刻（取自 s.now）。
type dockerOverviewCacheEntry struct {
	resp *response.DockerOverviewResp
	at   time.Time
}

// dockerOverviewCacheTTLDefault 是总览短缓存的默认 TTL。
//
// 2 秒：够把「同一刻涌进来的并发刷新」并成一次聚合（前端 10 秒自动刷新，多客户端
// 同刻刷新是常态），又短到不会让读者看到过期事实 —— 陈旧结论本身按 90 秒量级的阈值
// 判定，2 秒的偏差改变不了任何一台主机的结论。**不得长于数秒**：把 TTL 拉到刷新间隔
// 之上，「自动刷新」就变成了看见同一份数据的错觉。
const dockerOverviewCacheTTLDefault = 2 * time.Second

// dockerOverviewCacheKey 是短缓存的 singleflight 去重键。整舰队只有这一份总览
//（该端点本就是 docker:list 的全局读，响应不含任何 per-user 维度），故是一枚常量。
const dockerOverviewCacheKey = "docker:overview"

// NewDockerService 构造读面。
func NewDockerService(store *dockerstate.Store, devices DockerHostDeviceReader,
	cfg AgentConfigGetter, log logger.LoggerInterface) *DockerService {
	return &DockerService{store: store, devices: devices, cfg: cfg, log: log, now: time.Now,
		overviewCacheTTL: dockerOverviewCacheTTLDefault}
}

// WithOverviewCache 覆盖总览短缓存的 TTL（<=0 = 关闭：与引入短缓存之前逐字一致地
// 逐次重算）。存在的理由有二：**回退**（总览一旦因为缓存给出反直觉的结果，关掉即可）
// 与**测试**（TTL 与 s.now 都可控，验过期不必 sleep）。装配期调用一次。
func (s *DockerService) WithOverviewCache(ttl time.Duration) *DockerService {
	s.overviewCacheTTL = ttl
	return s
}

// WithStatsHistory 注入 stats 留存读面（P2）。装配在 wireup 一处完成。
func (s *DockerService) WithStatsHistory(h DockerStatsHistoryReader) *DockerService {
	s.stats = h
	return s
}

// WithImageTargets 注入镜像目标版本声明的存储面（Wave 4c）。装配在 wireup 一处完成。
func (s *DockerService) WithImageTargets(r DockerImageTargetRepo) *DockerService {
	s.targets = r
	return s
}

// WithHostDiskMetrics 注入宿主磁盘水位的批量读面（Wave 4d）。装配在 wireup 一处完成。
func (s *DockerService) WithHostDiskMetrics(r DockerHostDiskReader) *DockerService {
	s.hostDisk = r
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
	// 宿主磁盘水位：一次批量取（无 N+1），缺席即缺省。
	disk := s.hostDiskWaterlines(ctx, records)
	for _, rec := range records {
		if rec.readErr != nil {
			// 单台读失败不拖垮整张清单（少一行好过整页 500）。不清理、也不编造
			// 「零容器零镜像」的假象 —— 那台机器的事实到总览页再以 error 字段如实呈现。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			continue
		}
		out.List = append(out.List, toHostItem(rec, onlineSince, interval, now, disk))
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
//
// **两次批量读，与主机数无关**（100 台规模下的硬约束）：可管主机集合 → 快照（一次
// pipeline，MGet）→ 设备行（一条 id IN (...)，FindByIDs）。此前这里是逐台
// `store.Get` + `devices.FindByID`：一百台 = 100 次 Redis GET + 100 条 SELECT，全在
// 同一个请求里 —— 而总览页被每 10 秒轮询一次。守卫测试钉住「批量各调用一次、逐台
// 调用为零」（1 台与 100 台两种规模都要成立）。
func (s *DockerService) hostRecords(ctx context.Context) ([]hostRecord, error) {
	ids, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, apperror.Internal("读取主机清单失败", err)
	}
	// 批量一：快照（一次 pipeline）。读失败的那几台缺席于 map、但要落在失败名单里
	// —— 「读不到」与「从未上报」是两句不同的话（见 dockerstate.MGetError）。
	envs, mgetErr := s.store.MGet(ctx, ids)
	failed := mgetFailures(mgetErr, ids)
	// 批量二：设备行（一条 id IN (...)）。
	devs, devErr := s.devices.FindByIDs(ctx, ids)
	var devByID map[uint64]*entity.Device
	if devErr != nil {
		// 读库失败（DB 抖动/超时）：**原地跳过**这一批。把瞬时故障当成「设备已删」
		// 去清理，会一次抹掉全部主机的快照键，页面从此空白到下一次上报。
		//（逐台版是「这台跳过」，批量版是「这批跳过」—— 同一句话、同一后果：
		// 不编造主机行、也不清理。）
		if s.log != nil {
			s.log.Warn("docker host device lookup failed",
				zap.Int("hosts", len(ids)), zap.Error(devErr))
		}
	} else {
		devByID = make(map[uint64]*entity.Device, len(devs))
		for i := range devs {
			devByID[devs[i].ID] = &devs[i]
		}
	}
	out := make([]hostRecord, 0, len(ids))
	for _, id := range ids {
		rec := hostRecord{id: id}
		if rerr, ok := failed[id]; ok {
			// 快照读失败**不是**「设备已删」：设备行照常查（否则总览页连主机名
			// 都给不出），呈现或不呈现由调用方裁决。
			rec.readErr = rerr
		} else {
			// 缺席 = 从未上报（集合里有它、快照键却被删/没写过），是正常态：
			// env 为 nil 且 readErr 为 nil。
			rec.env = envs[id]
		}
		if devErr != nil {
			continue // 整批设备行读失败：不编造行（与逐台版的「这台跳过」同一后果）
		}
		dev, ok := devByID[id]
		if !ok || dev == nil {
			// 设备确已删除（仓储未命中，或返回了空设备）而集合里还留着它：
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
			continue
		}
		rec.dev = dev
		out = append(out, rec)
	}
	return out, nil
}

// mgetFailures 把批量读的错误折成「哪台读失败 + 各自的原因」（nil = 全读到了）。
//
// 非 *dockerstate.MGetError 的意外错误（理论上不该出现）按「整批都读失败」处理：
// 与逐台 Get 的「每台都读失败」等价 —— 读面宁可把结论说成「读不到」，也不把整批打成
// 500（本读面的底线是 agent/Redis 抖动时页面照样打得开）。
func mgetFailures(err error, ids []uint64) map[uint64]error {
	if err == nil {
		return nil
	}
	var mge *dockerstate.MGetError
	if errors.As(err, &mge) {
		return mge.Failed
	}
	out := make(map[uint64]error, len(ids))
	for _, id := range ids {
		out[id] = err
	}
	return out
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

// hostDiskWaterlines 批量取这批主机的宿主磁盘水位（设备指标宽表的最近一条整机行）。
//
// **一次批量取**（LatestWideDisk 收全部 hostId）：逐台查是 N+1（主机数虽是个位数，
// 但契约与纪律按「不许 N+1」钉住 —— 有测试断言只调用一次）。未装配（nil）或读取失败
// 一律返回 nil：主机条目/总览据此把 diskUsedPercent/diskTotalGb 留空（缺省，不填 0）
// —— 水位是增强面，它缺席不该拖垮主机清单（见 DockerService.hostDisk 的注释）。
func (s *DockerService) hostDiskWaterlines(ctx context.Context, records []hostRecord) map[uint64]entity.DeviceMetricWide {
	if s.hostDisk == nil || len(records) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.id)
	}
	m, err := s.hostDisk.LatestWideDisk(ctx, ids)
	if err != nil {
		if s.log != nil {
			s.log.Warn("docker host disk waterline lookup failed", zap.Error(err))
		}
		return nil
	}
	return m
}

// toHostItem 把一台主机的枚举记录映射成主机条目。Hosts 与 Overview 共用：
// 主机条目在两张页面里必须说同一句话（同一 online/stale 结论、同一计数口径）。
//
// disk 是这批主机的宿主磁盘水位（按 deviceID 索引，见 hostDiskWaterlines）；缺席
// 的设备（未上报指标 / 读取失败 / 未装配）留空。注意它与下面 rec.env 里的 DockerOK/
// DiskUsage **不同源**：水位来自设备指标宽表（整机口径），与 docker 快照能否读到无关 ——
// 故即使这台主机的 docker 快照读失败（rec.readErr != nil），它的宿主磁盘水位照样带出
// （「这台机器的盘快满了吗」是宿主事实，不因 docker 读不到而消失）。
func toHostItem(rec hostRecord, onlineSince time.Time, interval int, now time.Time,
	disk map[uint64]entity.DeviceMetricWide) response.DockerHostItem {
	stale, _ := dockerstate.Stale(rec.env, now, interval)
	item := response.DockerHostItem{
		ID:        rec.id,
		Hostname:  rec.dev.Hostname,
		PrimaryIP: normalizedIP(rec.dev.PrimaryIP),
		// 平台级分组/标签：与设备列表同源（entity.Device），docker 侧不另存一份。
		// rec.dev 必非 nil（hostRecords 只在 dev==nil 时 continue），故直取安全。
		Group: rec.dev.GroupName,
		Tags:  rec.dev.Tags,
		// 与设备列表 toListItem 的在线判定逐字同句（阈值来源已在上面统一）。
		Online: rec.dev.LastSeenAt != nil && rec.dev.LastSeenAt.After(onlineSince),
		Stale:  stale,
	}
	// 宿主磁盘水位：复制指针值（不共享宽表行的指针 —— 与 toVolumeItem 同一纪律）。
	// 两列各自独立判空：一行只采到百分比没采到总量时也不编造分母。
	if w, ok := disk[rec.id]; ok {
		if w.DiskUsedPercent != nil {
			v := *w.DiskUsedPercent
			item.DiskUsedPercent = &v
		}
		if w.DiskTotalGB != nil {
			v := *w.DiskTotalGB
			item.DiskTotalGB = &v
		}
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

// toUnavailableHost 把一台「在范围内、但资源事实不可用」的主机折成清单条目。
// hostname 取自设备行（hostRecords 保证不可用主机的 rec.dev 非 nil —— 设备已删的
// 那类在枚举阶段就自愈清理掉了，不会走到这里）；reason 是结论句（前端直接显示）。
func toUnavailableHost(rec hostRecord, reason string) response.DockerUnavailableHostItem {
	return response.DockerUnavailableHostItem{
		HostID:   rec.id,
		Hostname: rec.dev.Hostname,
		Reason:   reason,
	}
}

// dockerStateReadFailed 是「快照读失败」在主机条目 error 字段里的结论句：
// 与 State() 的 500 文案同源，避免同一故障在两张页面说出两句话。
const dockerStateReadFailed = "读取资源快照失败"

// dockerStateNeverReported 是「从未上报」在不可用主机清单里的结论句：集合里有它、
// 快照键却没写过（与「读失败」是两句不同的话，也让「这台机器怎么了」说得清）。
const dockerStateNeverReported = "从未上报资源快照"

// dockerDefaultPageSize 是未传 pageSize 时生效的页大小。
//
// 为什么不是 app.PageRequest 的默认 10、也不是「全部返回」：10 对工作面板偏小
// （翻页过多）；「全部返回」等于把删掉的 500 硬截断换个形式复活（响应仍随舰队
// 规模线性膨胀，500 上限换个名字回来了）。50 = 可扫一屏又可控的日常操作页。
// 前端分页接线落地后会显式传参，此默认只服务「未接线」的调用方。
const dockerDefaultPageSize = 50

// dockerPageSizeMax 是单页条数上限的兜底。HTTP 绑定层 app.PageRequest 已封顶
// 100（并封顶页码 10000，防天文偏移）；这里再夹一次是因为本服务不止 HTTP 一个
// 调用方（直调会把页大小放大成无界）。值与 app.PageRequest 的 max 逐字一致。
const dockerPageSizeMax = 100

// dockerPageSlice 把 (page, pageSize) 收敛成切片窗口 [start, end) 并回吐**生效值**
// （响应要把生效的 page/pageSize 原样回显，所以不能只算边界）：
//   - page < 1 → 1；pageSize < 1 → dockerDefaultPageSize（不是 app 默认的 10）；
//   - pageSize > 上限 → 夹到上限；
//   - 越界页（start >= total）→ start=total、end=total（空窗口，Total 不变）——
//     与 /docker/tasks 的越界语义同句（并发清理后页码空了就返回空 items）。
func dockerPageSlice(total, page, pageSize int) (effPage, effSize, start, end int) {
	effPage = page
	if effPage < 1 {
		effPage = 1
	}
	effSize = pageSize
	if effSize < 1 {
		effSize = dockerDefaultPageSize
	}
	if effSize > dockerPageSizeMax {
		effSize = dockerPageSizeMax
	}
	start = (effPage - 1) * effSize
	if start > total {
		start = total
	}
	end = start + effSize
	if end > total {
		end = total
	}
	return effPage, effSize, start, end
}

// sortDockerRows 按排序白名单把 rows 就地排序。
//
// Docker 数据源是 Redis 快照切片（不是 DB 行），无法复用 GORM 的 paginate[T]，
// 故在白名单比较器闭包表这一层实现（与 repository/device.go 的 deviceSortClause
// 同族纪律，只是比较器在内存里而非 SQL 片段）：
//
//   - 默认（sortBy 空/未登记）＝**现状次序** —— hostID 升序、同主机按 name 升序，
//     与引入排序参数之前逐字一致；此时 sortOrder **不参与**（默认次序的方向是
//     语义的：反转它就不是「现状」了，也会让「不传排序」的回归点失守）；
//   - "hostId" / "name" 是每张表都具备的锚列（内建识别，无需各页登记）；
//     其余键由 keys 给出（各资源页的专有列：size/created/state/services…）；
//   - 登记键的方向仅 sortOrder=="asc" 生效、其余回落 desc（默认方向 desc——
//     与 deviceSortClause「sortOrder 非 asc 即 DESC」同一句）；
//   - 稳定次序锚：主键并列时以 hostID 升序、name 升序兜底 —— 保证全序确定，
//     页边界不随枚举运气抖动（与 device 的 id DESC 锚、/docker/tasks 的 ref 锚
//     同一纪律）。
func sortDockerRows[T any](rows []T, sortBy, sortOrder string,
	hostID func(T) uint64, name func(T) string,
	keys map[string]func(a, b T) int) {
	anchor := func(a, b T) int {
		if ha, hb := hostID(a), hostID(b); ha != hb {
			return cmp.Compare(ha, hb)
		}
		return cmp.Compare(name(a), name(b))
	}
	var primary func(a, b T) int
	switch sortBy {
	case "hostId":
		primary = func(a, b T) int { return cmp.Compare(hostID(a), hostID(b)) }
	case "name":
		primary = func(a, b T) int { return cmp.Compare(name(a), name(b)) }
	case "":
		// 未传 → 默认（现状次序），忽略 sortOrder。
		slices.SortFunc(rows, anchor)
		return
	default:
		c, ok := keys[sortBy]
		if !ok {
			// 未登记键（含注入尝试）→ 静默回落默认，不 400。
			slices.SortFunc(rows, anchor)
			return
		}
		primary = c
	}
	desc := sortOrder != "asc"
	slices.SortFunc(rows, func(a, b T) int {
		c := primary(a, b)
		if desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return anchor(a, b)
	})
}

// dockerOverviewAnomalyCap 是总览页异常清单的上限：控制塔的异常清单是**抽查**
// 不是全表 —— 收全表会让响应随舰队规模线性膨胀，而 50 条之外的内容读者会在
// 对应的主机清单页里看到（Total 字段会告诉他「还有更多」）。
const dockerOverviewAnomalyCap = 50

// Overview 返回控制塔总览：跨主机的航队 KPI、主机清单与异常容器清单。
//
// 三层：
//
//	A. 短缓存 + singleflight（本方法，见下）—— 只为**削峰**，不改答案；
//	B. computeOverview —— 聚合本身（取数 = hostRecords 的两次批量读）；
//	C. 响应装配 —— 字段与语义在这里，与引入缓存之前逐字一致。
//
// **为什么总览要短缓存**：它是被前端每 10 秒自动刷新的端点（views/overview.vue），
// N 个打开的页面 = N/10 次/秒的全量聚合。TTL 内合并成一次，TTL 到期瞬间由
// singleflight 合并并发击穿（N 个同时到期的请求只算一次）。键只有一枚（全舰队总览）
// —— 这个端点本就是 docker:list 的全局读，响应不含任何 per-user 维度，按用户分键
// 只会得到 N 份一模一样的聚合。TTL 只有数秒（不为提速体感，只为削峰；不得长于前端
// 的刷新间隔，否则「自动刷新」就成了看见同一份数据的错觉）。
//
// 缓存**不主动失效**（设备删除 / 快照上报都不清它）：TTL 只有数秒，一次变更的可见
// 延迟不参与任何裁决；主动失效会把「一处写入」变成遍布各处的缓存状态机，而它要挡的
// 只是同一刻的重复聚合。错误也**不入缓存**：下一次请求立刻重试，不把一次抖动钉成几秒
// 的故障。
//
// 返回的响应是**共享只读**的（TTL 内的多次调用拿到同一份对象）—— 调用方不得改写它
//（生产调用方只有 handler，它只做序列化）。
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
	if s.overviewCacheTTL <= 0 {
		// 关闭（WithOverviewCache(<=0)）：与引入短缓存之前逐字一致地逐次重算。
		// 连 singleflight 也一起去掉 —— 缓存关掉还把并发合并留着，会让「关闭」的
		// 语义变得半开半闭（同一个请求序列时而一次、时而多次）。
		return s.computeOverview(ctx)
	}
	if resp, ok := s.overviewFromCache(); ok {
		return resp, nil
	}
	v, err, _ := s.overviewGroup.Do(dockerOverviewCacheKey, func() (any, error) {
		// 双重检查：等在这枚 singleflight 上的并发者，可能在前一个领头算完并回填
		// 缓存后才被唤醒 —— 那就直接命中，不再白算一趟全量聚合。
		if resp, ok := s.overviewFromCache(); ok {
			return resp, nil
		}
		// 聚合用的是**领头那个请求的 ctx**（与 middleware/permission.go、
		// metricshistory/window.go 的既有 singleflight 用法同参）：代价是领头客户端
		// 断开会让跟随者一起拿到取消错误。本聚合只有两次批量读（毫秒级），这个窗口
		// 很小；换成 WithoutCancel 则会在领头断开后继续占着连接把活干完 —— 对一次
		// 「随时可以重来」的读来说，前者更划算。
		resp, err := s.computeOverview(ctx)
		if err != nil {
			return nil, err // 错误不入缓存（下一次请求立刻重试）
		}
		s.overviewToCache(resp)
		return resp, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*response.DockerOverviewResp), nil
}

// overviewFromCache 读短缓存：TTL 内命中即回吐（共享只读对象），过期/空缓存返回 false。
//
// 时刻取自 s.now()（与陈旧度同一枚可替换时钟）：测试据此把时钟往前拨就能验过期，
// 不必 sleep —— sleep 会让「几秒 TTL」在 -race 全量跑时变成随机的红。
// 锁只保护这一格字段的读（无 I/O），聚合在锁外跑。
func (s *DockerService) overviewFromCache() (*response.DockerOverviewResp, bool) {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	e := s.overviewEntry
	if e == nil || s.now().Sub(e.at) >= s.overviewCacheTTL {
		return nil, false
	}
	return e.resp, true
}

// overviewToCache 回填短缓存（只由 singleflight 的领头调用）。
func (s *DockerService) overviewToCache(resp *response.DockerOverviewResp) {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	s.overviewEntry = &dockerOverviewCacheEntry{resp: resp, at: s.now()}
}

// computeOverview 是总览的聚合本身（无缓存、无 singleflight）：取数 + 装配，
// 每次调用都是一趟完整重算。缓存/合并只在 Overview 那一层（见那里的注释）。
func (s *DockerService) computeOverview(ctx context.Context) (*response.DockerOverviewResp, error) {
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
	// 宿主磁盘水位：一次批量取（无 N+1），缺席即缺省 —— 磁盘面板「哪台快满」的
	// 分母就挂在这里的 hosts 条目上（与 Disk 占用账目不同源，见 response 的注释）。
	disk := s.hostDiskWaterlines(ctx, records)
	for _, rec := range records {
		item := toHostItem(rec, onlineSince, interval, now, disk)
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
//   - 默认排序 hostId 升序、同主机容器名升序（现状次序）：集合枚举顺序不保证
//     （Redis 集合无序），不排就变成「按运气排序」；sortBy 白名单可改（见
//     sortDockerRows），非法值静默回落默认；
//   - 排序在分页**之前**（先全序、再切片）：页边界才不随枚举运气抖动。
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
	// 范围内「有主机、但资源事实不可用」的主机（读失败 / 从未上报）：行被跳过，
	// 但「少的是谁、为什么」显式回吐（空数组而非 null）。
	unavailable := []response.DockerUnavailableHostItem{}
	for _, rec := range records {
		if q.HostID != 0 && rec.id != q.HostID {
			continue
		}
		if rec.readErr != nil {
			// 单台读失败不拖垮整张表（少几行好过整页 500），文案与 Hosts 同一句。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateReadFailed))
			continue
		}
		if rec.env == nil {
			// 从未上报：没有容器事实可列（主机行与结论在 hosts/总览页给出），
			// 但让读者知道这台主机在范围内、只是没有数据。
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateNeverReported))
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
	// 排序在分页**之前**（先全序再切片，页边界才稳定）；白名单/方向/稳定锚见
	// sortDockerRows。默认（不传 sortBy）＝现状次序（hostId 升序、同机按名称）。
	sortDockerRows(items, q.SortBy, q.SortOrder,
		func(it response.DockerWorkloadItem) uint64 { return it.HostID },
		func(it response.DockerWorkloadItem) string { return it.Name },
		map[string]func(a, b response.DockerWorkloadItem) int{
			"state": func(a, b response.DockerWorkloadItem) int { return cmp.Compare(a.State, b.State) },
			"image": func(a, b response.DockerWorkloadItem) int { return cmp.Compare(a.Image, b.Image) },
		})
	// total 如实报过滤后全量（分页前）；items 是本页（先排序后切片）。
	total := len(items)
	page, pageSize, start, end := dockerPageSlice(total, q.Page, q.PageSize)
	return &response.DockerWorkloadListResp{
		Items: items[start:end], Total: total, Page: page, PageSize: pageSize,
		UnavailableHosts: unavailable,
	}, nil
}

// ── 跨主机资源清单（9a）：镜像/卷/网络/项目四页与容器统一表同一副骨架 ──────

// resourceQuery 把四页资源清单共同的查询维度打个包（hostId 收窄 + 排序白名单 +
// 分页），避免 aggregateResourceRows 挂一长串裸参数。
type resourceQuery struct {
	hostID    uint64
	sortBy    string
	sortOrder string
	page      int
	pageSize  int
}

// resourceResult 是四页资源清单骨架的产物：本页条目 + 过滤后全量 + 生效分页 +
// 不可用主机清单。四个字段一一对应响应 DTO 的四组字段（形状一致，装配在方法里）。
type resourceResult[T any] struct {
	items            []T
	total            int
	page             int
	pageSize         int
	unavailableHosts []response.DockerUnavailableHostItem
}

// resourceRow 是跨主机聚合的中间行：最终条目 + 排序键。
// 排序键不进 DTO：「名称」只是排序依据（镜像取第一个 repoTag、其余取 Name），
// 不必出现在响应里；放在这里而不是另造四个带名字的结构体。
type resourceRow[T any] struct {
	hostID uint64
	name   string
	item   T
}

// 四页各自的「行」类型别名：让白名单比较器表（keys）的签名可读（否则每处都要
// 写 resourceRow[response.DockerImageListItem] 这类完整实例化）。
type (
	imageRow   = resourceRow[response.DockerImageListItem]
	volumeRow  = resourceRow[response.DockerVolumeListItem]
	networkRow = resourceRow[response.DockerNetworkListItem]
	projectRow = resourceRow[response.DockerProjectListItem]
)

// aggregateResourceRows 跑完四页资源清单（镜像/卷/网络/项目）的公共骨架：
// hostRecords 枚举 → hostId 过滤 → 单台读失败 warn 跳过 → 不可用主机入清单 →
// 逐条过筛 → 排序（白名单，默认 hostId 升序、同主机按名称）→ 分页（先排序后切片）
// 但 total 如实报过滤后全量。
//
// 为什么抽出来而不是四个方法各抄一遍：这段骨架里全是**口径** —— 跳过政策
// （读失败 = 少几行、不编造「零条目」的假象）、不可用主机清单、stale 照常计入、
// 排序先于分页、分页报全量 —— 四份拷贝就是四次分岔机会（Workloads 已为「排序
// 必须全序」写过一次注释；再抄四遍，改一处就会漏掉三处）。每条清单的差异全在
// 闭包里：取哪类源（srcs）、怎么过筛（match）、排序取什么名字（name）、该页的
// 专有排序列（keys）、按主机的派生数据（onHost）、条目怎么装配（row）。
//
// onHost 给「按主机、与行筛选无关」的派生数据一条与清单行**同一趟枚举**的通道
// （目前只有镜像页的可回收账目用它：disk 的覆盖范围必须与行的主机范围同源）。
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
// 只把「截断」换成分页，其余零改动）；两者共用的是 hostRecords 枚举与同一套
// 纪律，不是同一份代码。
func aggregateResourceRows[T any, S any](
	s *DockerService,
	ctx context.Context,
	q resourceQuery,
	srcs func(st *agentproto.DockerState) []S,
	match func(src S) bool,
	name func(src S) string,
	keys map[string]func(a, b resourceRow[T]) int,
	onHost func(hostID uint64, st *agentproto.DockerState),
	row func(hostID uint64, hostname string, src S) T,
) (resourceResult[T], error) {
	records, err := s.hostRecords(ctx)
	if err != nil {
		return resourceResult[T]{}, err
	}
	rows := []resourceRow[T]{}
	// 范围内「有主机、但资源事实不可用」的主机（读失败 / 从未上报）。
	unavailable := []response.DockerUnavailableHostItem{}
	for _, rec := range records {
		if q.hostID != 0 && rec.id != q.hostID {
			continue
		}
		if rec.readErr != nil {
			// 单台读失败不拖垮整张表（少几行好过整页 500），文案与 Hosts/Workloads 同一句。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateReadFailed))
			continue
		}
		if rec.env == nil {
			// 从未上报：没有资源事实可列（主机行与结论在 hosts/总览页给出），
			// 但让读者知道这台主机在范围内、只是没有数据。
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateNeverReported))
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
	// 排序在分页**之前**：切片砍掉的必须是排序后的尾部，否则砍谁取决于枚举
	// 运气（同一份数据两次请求会给出不同的前 N 行）。白名单/方向/稳定锚见
	// sortDockerRows —— 默认（不传 sortBy）＝hostId 升序、同主机按名称；stale
	// 主机的条目照常计入（陈旧是「最后已知事实过期」，不是「事实不存在」）。
	sortDockerRows(rows, q.sortBy, q.sortOrder,
		func(r resourceRow[T]) uint64 { return r.hostID },
		func(r resourceRow[T]) string { return r.name },
		keys)
	// total 如实报过滤后全量（分页前）。
	total := len(rows)
	page, pageSize, start, end := dockerPageSlice(total, q.page, q.pageSize)
	// 空数组而非 null：与 Hosts/State/Overview/Workloads 的清单同一约定（前端少一层判空）。
	pageRows := rows[start:end]
	items := make([]T, 0, len(pageRows))
	for _, r := range pageRows {
		items = append(items, r.item)
	}
	return resourceResult[T]{
		items: items, total: total, page: page, pageSize: pageSize,
		unavailableHosts: unavailable,
	}, nil
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
// Online 过滤同一句）。跨主机的跳过/排序/分页口径全在 aggregateResourceRows。
//
// 响应同时带 disk（主机范围内的可回收账目，供底栏「N 个可回收 · X」）—— 它与
// Items 的筛选/分页口径**无关**（只跟 hostId 收窄），两种口径的分工见
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
	res, err := aggregateResourceRows(s, ctx, resourceQuery{
		hostID: q.HostID, sortBy: q.SortBy, sortOrder: q.SortOrder, page: q.Page, pageSize: q.PageSize,
	},
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
		map[string]func(a, b imageRow) int{
			"size":    func(a, b imageRow) int { return cmp.Compare(a.item.SizeMB, b.item.SizeMB) },
			"created": func(a, b imageRow) int { return cmp.Compare(a.item.Created, b.item.Created) },
		},
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
	return &response.DockerImageListResp{
		Items: res.items, Total: res.total, Page: res.page, PageSize: res.pageSize,
		Disk: disk, UnavailableHosts: res.unavailableHosts,
	}, nil
}

// ImageDrift 返回跨主机镜像版本漂移视图：以 **repoTag** 为行，给出同一个标签在各
// 主机上的内容摘要分布（有几种摘要、各多少台主机、哪些是少数派）。
//
// 与「跨主机镜像清单」（Images）是两种口径：Images 的行是「一台主机上的一个镜像」；
// 本方法的行是「一个 repoTag 的舰队级分布」。因此不复用 aggregateResourceRows
// （那副骨架的行天然带 hostID，本视图的行不带）—— 复用它的枚举（hostRecords）与
// 分页原语（dockerPageSlice），排序另写（默认 distinctDigests 降序，见 sortDriftItems）。
//
// **统计范围口径**（keyword / group 与 unavailableHosts 的自洽，本文件的裁决）：
//
//   - **主机范围**只由 group 收窄。一台主机若分组不匹配，就**不在范围内**：既不参与
//     任何 tag 的聚合，也**不进 unavailableHosts**（它不是「不可用」，是「没在看」）。
//   - **主机范围内**读失败 / 从未上报的主机 → 进 unavailableHosts（与 Wave 2 四资源页
//     同一形态、同一判据、同一结论句），且**不计入任何 tag 的 hostCount**（不编造
//     「零镜像」的假象 —— 否则「9 台一致」会是 10 台里漏算 1 台）。
//   - **keyword 收窄的是标签行**（repoTag 子串），与主机范围无关：一个不可用主机
//     「有没有某个 tag」无从知晓，故 keyword 不把任何主机移出/移入 unavailableHosts。
//     换句话说：group 决定「主机范围」（进不进 unavailableHosts 的候选），
//     keyword 决定「看哪些标签行」，两者各司其职、互不污染对方的口径。
//
// 「按 repoTag 展平」：一个镜像条目可带多个 tag，每个 tag 各自计入一行
// （同一个 digest 出现在它挂的每一个 tag 行里）。
//
// 排序：默认 distinctDigests 降序（最不齐的排最前 —— 运维先处理它）、同值按
// repoTag 升序作稳定锚；白名单 repoTag/hostCount/distinctDigests，非法静默回落默认。
func (s *DockerService) ImageDrift(ctx context.Context, q *request.DockerImageDriftQuery) (*response.DockerImageDriftResp, error) {
	if q == nil {
		// handler 永远传非 nil；对 nil 的防御语义与其余读面一致，避免直调方先写判空。
		q = &request.DockerImageDriftQuery{}
	}
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, err
	}
	// 关键字只折叠一次（对每行的 repoTag 复用）。
	kw := strings.ToLower(q.Keyword)
	// tag → digest → 该 (tag,digest) 下的主机引用表（按 hostId 去重）。
	// 用嵌套 map 而不是一次成行的切片：同一 (tag,digest) 可能被多台主机命中，
	// 靠内层 map 的键天然去重（一台主机对同一 tag 只会有一个 digest，防御性保留）。
	tags := map[string]map[string]map[uint64]response.DockerImageDriftHostRef{}
	// 主机范围内「有主机、但资源事实不可用」的主机（读失败 / 从未上报）。
	unavailable := []response.DockerUnavailableHostItem{}
	// scopedHosts 是范围内**快照可用**的全部主机引用（含没有任何镜像的主机）——
	// 声明式目标版本（Wave 4c）算「待对齐」时的候选全集：一台主机对某个目标标签
	// 要么「摘要相符（已对齐）」要么「待对齐」（摘要不符 / 根本没有这个 tag）。
	scopedHosts := []response.DockerImageDriftHostRef{}
	for _, rec := range records {
		// 主机范围 = group 过滤（keyword 不参与 —— 见方法注释的范围口径）。
		if q.Group != "" && rec.dev.GroupName != q.Group {
			continue // 分组外：不在范围内，聚合与 unavailableHosts 都不涉及
		}
		if rec.readErr != nil {
			// 单台读失败不拖垮整张表（少几行好过整页 500），文案与其余读面同一句。
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateReadFailed))
			continue
		}
		if rec.env == nil {
			// 从未上报：范围内但没有镜像事实可聚合，如实标注而不是假装零镜像。
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateNeverReported))
			continue
		}
		ref := response.DockerImageDriftHostRef{
			HostID: rec.id, Hostname: rec.dev.Hostname, Group: rec.dev.GroupName,
		}
		// 范围内快照可用的主机一律进候选全集（哪怕一个镜像都没有）：算待对齐时，
		// 「这台机器上没有这个 tag」也是一种待对齐。
		scopedHosts = append(scopedHosts, ref)
		for _, img := range rec.env.State.Images {
			for _, tag := range img.RepoTags {
				byDigest, ok := tags[tag]
				if !ok {
					byDigest = map[string]map[uint64]response.DockerImageDriftHostRef{}
					tags[tag] = byDigest
				}
				hosts, ok := byDigest[img.ID]
				if !ok {
					hosts = map[uint64]response.DockerImageDriftHostRef{}
					byDigest[img.ID] = hosts
				}
				hosts[rec.id] = ref
			}
		}
	}

	// ── 声明式目标版本（Wave 4c）：**批量**取本页涉及的全部标签的目标，一次查询 ——
	// 逐标签查会是 N+1。targets 未装配（nil）时优雅降级为「无目标」（不产出下面三枚
	// 字段），目标面不该拖垮整张漂移表。
	targetsByTag := map[string]entity.DockerImageTarget{}
	if s.targets != nil && len(tags) > 0 {
		repoTags := make([]string, 0, len(tags))
		for tag := range tags {
			repoTags = append(repoTags, tag)
		}
		got, err := s.targets.FindByRepoTags(ctx, repoTags)
		if err != nil {
			return nil, imageTargetInternalErr(err)
		}
		targetsByTag = got
	}

	// 每个 tag 聚合成一行：多数派 / 少数派 / 主机数 / 摘要种数。
	items := []response.DockerImageDriftItem{}
	for tag, byDigest := range tags {
		// keyword 收窄的是标签行（大小写不敏感的子串）。
		if kw != "" && !strings.Contains(strings.ToLower(tag), kw) {
			continue
		}
		// hostCount = 有该 tag 的**不同主机数**（跨该 tag 的全部 digest 去重）。
		hostSet := map[uint64]struct{}{}
		for _, hosts := range byDigest {
			for id := range hosts {
				hostSet[id] = struct{}{}
			}
		}
		// 多数派：主机数最多的 digest；并列取 digest 字典序最小者（确定性锚）。
		dominantDigest := ""
		dominantCount := -1
		for digest, hosts := range byDigest {
			n := len(hosts)
			if n > dominantCount || (n == dominantCount && digest < dominantDigest) {
				dominantDigest, dominantCount = digest, n
			}
		}
		// 少数派：除多数派外的其余 digest，主机清单按 hostId 升序。
		outliers := []response.DockerImageDriftOutlier{}
		for digest, hosts := range byDigest {
			if digest == dominantDigest {
				continue
			}
			refs := make([]response.DockerImageDriftHostRef, 0, len(hosts))
			for _, h := range hosts {
				refs = append(refs, h)
			}
			slices.SortFunc(refs, func(a, b response.DockerImageDriftHostRef) int {
				return cmp.Compare(a.HostID, b.HostID)
			})
			outliers = append(outliers, response.DockerImageDriftOutlier{
				Digest: digest, HostCount: len(hosts), Hosts: refs,
			})
		}
		// 少数派之间：主机数降序、同值 digest 升序（确定性，不随 map 迭代次序抖动）。
		slices.SortFunc(outliers, func(a, b response.DockerImageDriftOutlier) int {
			if c := cmp.Compare(b.HostCount, a.HostCount); c != 0 {
				return c
			}
			return cmp.Compare(a.Digest, b.Digest)
		})
		row := response.DockerImageDriftItem{
			RepoTag: tag, HostCount: len(hostSet), DistinctDigests: len(byDigest),
			DominantDigest: dominantDigest, DominantHostCount: dominantCount,
			Outliers: outliers,
		}
		// 声明式目标版本（Wave 4c）：仅当该 tag 有声明时才产出三枚目标字段 ——
		// 绝不用多数派摘要臆造目标（那是观测事实，不是声明）。
		if tgt, ok := targetsByTag[tag]; ok {
			row.TargetDigest = tgt.TargetDigest
			// 已对齐 = 该 tag 的摘要 == 目标摘要的主机（byDigest[目标] 就是这批主机）。
			aligned := byDigest[tgt.TargetDigest]
			row.AlignedCount = len(aligned)
			// 待对齐 = 范围内快照可用的**其余**主机：摘要不符，或根本没有这个 tag
			//（对某个目标标签，一台主机非「对齐」即「待对齐」，没有第三种）。
			// 快照不可用的主机不算在内（进 unavailableHosts，其摘要无从知晓）。
			pending := make([]response.DockerImageDriftHostRef, 0, len(scopedHosts)-len(aligned))
			for _, h := range scopedHosts {
				if _, ok := aligned[h.HostID]; !ok {
					pending = append(pending, h)
				}
			}
			// 按 hostId 升序（scopedHosts 顺序来自集合枚举，不排就是按运气排）。
			slices.SortFunc(pending, func(a, b response.DockerImageDriftHostRef) int {
				return cmp.Compare(a.HostID, b.HostID)
			})
			row.PendingHosts = pending
		}
		items = append(items, row)
	}
	// 排序在分页**之前**（先全序再切片，页边界才稳定）。
	sortDriftItems(items, q.SortBy, q.SortOrder)
	total := len(items)
	page, pageSize, start, end := dockerPageSlice(total, q.Page, q.PageSize)
	return &response.DockerImageDriftResp{
		Items: items[start:end], Total: total, Page: page, PageSize: pageSize,
		UnavailableHosts: unavailable,
	}, nil
}

// sortDriftItems 按排序白名单把漂移行就地排序。
//
// 与 sortDockerRows 的分工：那个的默认次序是「现状次序 hostId 升序」（资源清单沿用），
// 本视图的默认次序是 **distinctDigests 降序**（最不齐的排最前）—— 默认方向不同，
// 且本视图的行不带 hostID/name 那对锚列，故另写（复用它的纪律而非代码）。
//
//   - 默认（sortBy 空/未登记）＝ distinctDigests 降序、repoTag 升序作稳定锚；
//     此时 sortOrder **不参与**（默认次序的方向是语义的）；
//   - 白名单 repoTag / hostCount / distinctDigests：方向仅 sortOrder=="asc" 生效、
//     其余回落 desc（与 sortDockerRows「非 asc 即 desc」同一句）；
//     并列时一律以 repoTag 升序兜底（全序确定，页边界不随 map 迭代运气抖动）。
func sortDriftItems(items []response.DockerImageDriftItem, sortBy, sortOrder string) {
	anchor := func(a, b response.DockerImageDriftItem) int { return cmp.Compare(a.RepoTag, b.RepoTag) }
	var primary func(a, b response.DockerImageDriftItem) int
	switch sortBy {
	case "repoTag":
		primary = func(a, b response.DockerImageDriftItem) int { return cmp.Compare(a.RepoTag, b.RepoTag) }
	case "hostCount":
		primary = func(a, b response.DockerImageDriftItem) int { return cmp.Compare(a.HostCount, b.HostCount) }
	case "distinctDigests":
		primary = func(a, b response.DockerImageDriftItem) int {
			return cmp.Compare(a.DistinctDigests, b.DistinctDigests)
		}
	default:
		// 未传 / 未登记键（含注入尝试）→ 默认：distinctDigests 降序，忽略 sortOrder。
		slices.SortFunc(items, func(a, b response.DockerImageDriftItem) int {
			if c := cmp.Compare(b.DistinctDigests, a.DistinctDigests); c != 0 {
				return c
			}
			return anchor(a, b)
		})
		return
	}
	desc := sortOrder != "asc"
	slices.SortFunc(items, func(a, b response.DockerImageDriftItem) int {
		c := primary(a, b)
		if desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return anchor(a, b)
	})
}

// ── 镜像目标版本声明（Wave 4c）────────────────────────────────────────

// dockerImageTargetRepoTagMax 是 repoTag 的长度上限（与实体列宽、请求校验同档）。
const dockerImageTargetRepoTagMax = 255

// dockerImageTargetDigestMax 是目标摘要的长度上限：`sha256:`（7）+ 64 位 hex = 71，
// 留余量到 128（容忍未来向更强算法/更长表示的演进，又不至于让乱塞的巨串落库）。
const dockerImageTargetDigestMax = 128

// dockerImageDigestPrefix 是目标摘要必须带的前缀。
//
// 漂移视图比对的 digest 是 docker 的镜像 ID（`sha256:<hex>`），故声明的目标摘要
// 也必须是同一形态 —— 强制前缀把「把 tag 名/半截 hex 填进摘要框」这类拼错变成
// 显式 400，而不是静默的「永远不会命中、永远待对齐」。
const dockerImageDigestPrefix = "sha256:"

// SetImageTarget 声明/覆盖一个 repoTag 的目标内容摘要（Wave 4c：POST /docker/image-targets）。
//
// **upsert 语义**：同 repoTag 覆盖（声明式目标版本没有「创建冲突」）。校验失败给
// 显式 400 结论句（用户输入校验报错，不走白名单静默回落）。
func (s *DockerService) SetImageTarget(ctx context.Context, req *request.DockerImageTargetSaveReq) (*response.DockerImageTargetItem, error) {
	if s.targets == nil {
		return nil, apperror.Internal("镜像目标存储未装配")
	}
	if req == nil {
		return nil, apperror.BadRequest("请求参数不合法")
	}
	repoTag := strings.TrimSpace(req.RepoTag)
	digest := strings.TrimSpace(req.TargetDigest)
	if repoTag == "" {
		return nil, apperror.BadRequest("镜像标签(repoTag)不能为空")
	}
	if len(repoTag) > dockerImageTargetRepoTagMax {
		return nil, apperror.BadRequest("镜像标签(repoTag)长度不能超过 255")
	}
	if digest == "" {
		return nil, apperror.BadRequest("目标摘要(targetDigest)不能为空")
	}
	if !strings.HasPrefix(digest, dockerImageDigestPrefix) {
		return nil, apperror.BadRequest("目标摘要(targetDigest)必须以 sha256: 开头")
	}
	if len(digest) > dockerImageTargetDigestMax {
		return nil, apperror.BadRequest("目标摘要(targetDigest)长度不合法")
	}
	row := &entity.DockerImageTarget{RepoTag: repoTag, TargetDigest: digest}
	if err := s.targets.Upsert(ctx, row); err != nil {
		return nil, imageTargetInternalErr(err)
	}
	// 回显的 UpdatedAt 取行结构体上的 autoUpdateTime 值（Create/覆盖都刷成「现在」）。
	return &response.DockerImageTargetItem{
		RepoTag: repoTag, TargetDigest: digest, UpdatedAt: row.UpdatedAt.Unix(),
	}, nil
}

// ClearImageTarget 清除某个 repoTag 的目标（Wave 4c：DELETE /docker/image-targets?repoTag=…）。
//
// **幂等**：清除一个本来就没设的目标是成功（与「取消一个没设过的目标」语义一致，
// 不返回 404）。repoTag 为空（连寻址键都没有）才是 400。
func (s *DockerService) ClearImageTarget(ctx context.Context, repoTag string) error {
	if s.targets == nil {
		return apperror.Internal("镜像目标存储未装配")
	}
	repoTag = strings.TrimSpace(repoTag)
	if repoTag == "" {
		return apperror.BadRequest("镜像标签(repoTag)不能为空")
	}
	if err := s.targets.Delete(ctx, repoTag); err != nil {
		return imageTargetInternalErr(err)
	}
	return nil
}

// ListImageTargets 返回全部已声明的镜像目标（Wave 4c：GET /docker/image-targets，docker:list）。
func (s *DockerService) ListImageTargets(ctx context.Context) (*response.DockerImageTargetListResp, error) {
	if s.targets == nil {
		return nil, apperror.Internal("镜像目标存储未装配")
	}
	rows, err := s.targets.List(ctx)
	if err != nil {
		return nil, imageTargetInternalErr(err)
	}
	items := make([]response.DockerImageTargetItem, 0, len(rows))
	for i := range rows {
		items = append(items, response.DockerImageTargetItem{
			RepoTag:      rows[i].RepoTag,
			TargetDigest: rows[i].TargetDigest,
			UpdatedAt:    rows[i].UpdatedAt.Unix(),
		})
	}
	return &response.DockerImageTargetListResp{Items: items, Total: len(items)}, nil
}

// imageTargetInternalErr 把存储错误折成「内部错误」结论句（原因进错误链由 app.Error 记日志）。
func imageTargetInternalErr(err error) error { return apperror.Internal("内部错误", err) }

// Volumes 返回跨主机卷统一表：全部可管主机的数据卷并成一张表，每行带「在哪台主机」。
//
// 过滤语义与前端卷页的开关同一句（filterVolumes）：keyword 对卷名子串、
// 大小写不敏感；unused 三值指针布尔（同 Images）。
func (s *DockerService) Volumes(ctx context.Context, q *request.DockerVolumeQuery) (*response.DockerVolumeListResp, error) {
	if q == nil {
		q = &request.DockerVolumeQuery{}
	}
	kw := strings.ToLower(q.Keyword)
	res, err := aggregateResourceRows(s, ctx, resourceQuery{
		hostID: q.HostID, sortBy: q.SortBy, sortOrder: q.SortOrder, page: q.Page, pageSize: q.PageSize,
	},
		func(st *agentproto.DockerState) []agentproto.DockerVolume { return st.Volumes },
		func(v agentproto.DockerVolume) bool {
			if q.Unused != nil && v.InUse == *q.Unused {
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(v.Name), kw)
		},
		func(v agentproto.DockerVolume) string { return v.Name },
		map[string]func(a, b volumeRow) int{
			// 无尺寸（SizeMB=nil，旧版 agent / 未采集）按 0 参与比较 —— 只求确定性，
			// 排序键不进响应，「不知道」的语义由条目上的 SizeMB=nil 承载。
			"size": func(a, b volumeRow) int { return cmp.Compare(derefSize(a.item.SizeMB), derefSize(b.item.SizeMB)) },
		},
		nil, // 卷页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, v agentproto.DockerVolume) response.DockerVolumeListItem {
			return response.DockerVolumeListItem{DockerVolumeItem: toVolumeItem(v), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerVolumeListResp{
		Items: res.items, Total: res.total, Page: res.page, PageSize: res.pageSize,
		UnavailableHosts: res.unavailableHosts,
	}, nil
}

// derefSize 把可空尺寸解成比较用的 float64（nil → 0，仅用于排序确定性）。
func derefSize(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
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
	res, err := aggregateResourceRows(s, ctx, resourceQuery{
		hostID: q.HostID, sortBy: q.SortBy, sortOrder: q.SortOrder, page: q.Page, pageSize: q.PageSize,
	},
		func(st *agentproto.DockerState) []agentproto.DockerNetwork { return st.Networks },
		func(n agentproto.DockerNetwork) bool {
			if q.Internal != nil && n.Internal != *q.Internal {
				return false
			}
			return kw == "" || strings.Contains(strings.ToLower(n.Name), kw)
		},
		func(n agentproto.DockerNetwork) string { return n.Name },
		map[string]func(a, b networkRow) int{
			"containers": func(a, b networkRow) int { return cmp.Compare(a.item.ContainersCount, b.item.ContainersCount) },
		},
		nil, // 网络页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, n agentproto.DockerNetwork) response.DockerNetworkListItem {
			return response.DockerNetworkListItem{DockerNetworkItem: toNetworkItem(n), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerNetworkListResp{
		Items: res.items, Total: res.total, Page: res.page, PageSize: res.pageSize,
		UnavailableHosts: res.unavailableHosts,
	}, nil
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
	res, err := aggregateResourceRows(s, ctx, resourceQuery{
		hostID: q.HostID, sortBy: q.SortBy, sortOrder: q.SortOrder, page: q.Page, pageSize: q.PageSize,
	},
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
		map[string]func(a, b projectRow) int{
			"state":    func(a, b projectRow) int { return cmp.Compare(a.item.State, b.item.State) },
			"services": func(a, b projectRow) int { return cmp.Compare(a.item.Services, b.item.Services) },
		},
		nil, // 项目页没有按主机的派生账目（onHost 的用途见 aggregateResourceRows）
		func(hostID uint64, hostname string, p agentproto.DockerProject) response.DockerProjectListItem {
			return response.DockerProjectListItem{DockerProjectItem: toProjectItem(p), HostID: hostID, Hostname: hostname}
		})
	if err != nil {
		return nil, err
	}
	return &response.DockerProjectListResp{
		Items: res.items, Total: res.total, Page: res.page, PageSize: res.pageSize,
		UnavailableHosts: res.unavailableHosts,
	}, nil
}

// ── 全局跨资源检索（Wave 4d：GET /docker/search）───────────────────────────

const (
	// dockerSearchGroupCapDefault 是每组默认限量：一次检索答「在哪」，不铺全量
	//（要看全量去对应清单页，那里有分页）。20 = 一屏扫得完，又不至于频繁触发截断。
	dockerSearchGroupCapDefault = 20
	// dockerSearchGroupCapMax 是每组限量（limit 参数）的上限，与 dockerPageSizeMax 同档。
	// 封顶而不是无界：无界就等于把「分页挡全量」那道闸从检索端点绕过去。
	dockerSearchGroupCapMax = 100
)

// 五个资源类型分组码（响应 groups[].kind 的取值；也是响应 groups 的固定发射顺序）。
const (
	dockerSearchKindContainers = "containers"
	dockerSearchKindImages     = "images"
	dockerSearchKindVolumes    = "volumes"
	dockerSearchKindNetworks   = "networks"
	dockerSearchKindProjects   = "projects"
)

// Search 是跨主机、跨资源类型的全局检索（Wave 4d）：跨 100 台、跨 5 类资源按
// **名称子串**找东西（分页已挡全量，逐页逐主机翻不可行）。
//
// 语义：
//   - keyword 对**名称**做大小写不敏感的子串匹配；容器**额外**匹配镜像名
//     （与 Workloads 的 keyword 同一句）。镜像按全部 repoTag join(" ") 后匹配
//     （与 Images 的 keyword 同一句：多标签任一命中即可）。
//   - **一次枚举、单次遍历**：同一趟 hostRecords 里把 5 类资源都过筛，**不**为每类
//     重跑一次快照读取（那样会把唯一的 I/O —— 快照读 —— 白翻五倍）。
//   - stale 主机的资源照常命中（陈旧是「最后已知事实过期」，不是「事实不存在」）；
//     读失败 / 从未上报的主机不进任何组，而进 unavailableHosts（不知道 ≠ 没有）。
//   - 每组：先按 hostId 升序、同主机按名称升序排序（全序、确定，与清单页同源），
//     再按 groupCap 截断；total 报截断前全量，truncated = total > groupCap
//     （**部分结果必须说出口** —— 本模块既有纪律，不靠前端猜）。
//
// **空 q 的裁决**：trim 后为空 → 直接返回 5 个空组（total=0 / truncated=false）与空
// unavailableHosts，**不枚举主机**、不报 400。理由：检索框在前端输入过程中会频繁落空
// （清空输入、光标全删），此刻报 400 会让输入过程处处见红；返回「合法的空结果」才是
// 「你还没问东西」的正确回答（也省掉一次无意义的全量枚举）。
//
// **limit 的裁决**：可选，<=0 / 未传 → dockerSearchGroupCapDefault；>上限 → 夹到
// dockerSearchGroupCapMax（**不报错** —— 与 pageSize 的绑定层封顶同一条「上限是
// 服务端的事」）。它是**单组**上限，不是结果总上限：每组独立截断、各自报 truncated。
func (s *DockerService) Search(ctx context.Context, keyword string, limit int) (*response.DockerSearchResp, error) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return &response.DockerSearchResp{
			Groups:           emptySearchGroups(),
			UnavailableHosts: []response.DockerUnavailableHostItem{},
		}, nil
	}
	groupCap := limit
	if groupCap <= 0 {
		groupCap = dockerSearchGroupCapDefault
	}
	if groupCap > dockerSearchGroupCapMax {
		groupCap = dockerSearchGroupCapMax
	}
	records, err := s.hostRecords(ctx)
	if err != nil {
		return nil, err
	}
	// 五类命中各自累积（空数组而非 null：与全站清单同一约定，前端少一层判空）。
	containers := []response.DockerSearchItem{}
	images := []response.DockerSearchItem{}
	volumes := []response.DockerSearchItem{}
	networks := []response.DockerSearchItem{}
	projects := []response.DockerSearchItem{}
	unavailable := []response.DockerUnavailableHostItem{}
	for _, rec := range records {
		if rec.readErr != nil {
			if s.log != nil {
				s.log.Warn("docker state read failed", zap.Uint64("deviceId", rec.id), zap.Error(rec.readErr))
			}
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateReadFailed))
			continue
		}
		if rec.env == nil {
			unavailable = append(unavailable, toUnavailableHost(rec, dockerStateNeverReported))
			continue
		}
		host := searchHostRef{hostID: rec.id, hostname: rec.dev.Hostname, group: rec.dev.GroupName}
		st := rec.env.State
		// 容器：名称**或**镜像名（与 Workloads 同一句）。id 给容器 ID（详情路由
		// `/docker/containers/:id` 吃它），hint 给镜像名。
		for _, c := range st.Containers {
			if strings.Contains(strings.ToLower(c.Name), kw) || strings.Contains(strings.ToLower(c.Image), kw) {
				containers = append(containers, host.searchItem(c.Name, c.ID, c.Image))
			}
		}
		// 镜像：全部 repoTag join(" ") 后匹配（与 Images 同一句）。展示名取首个 repoTag
		//（悬空无标签回落 ID，见 imageResourceName），id 给镜像 ID（详情路由
		// `/docker/image-detail/:id` 吃它），hint 也给镜像 ID（次要身份线索，
		// 不与展示名重复；悬空时展示名与 id 同值，是该镜像的真实身份）。
		for _, i := range st.Images {
			if !strings.Contains(strings.ToLower(strings.Join(i.RepoTags, " ")), kw) {
				continue
			}
			images = append(images, host.searchItem(imageResourceName(i), i.ID, i.ID))
		}
		// 卷：名称子串，id 给卷名（列表页吃 `?keyword=`，卷名即其稳定标识），hint 给驱动。
		for _, v := range st.Volumes {
			if strings.Contains(strings.ToLower(v.Name), kw) {
				volumes = append(volumes, host.searchItem(v.Name, v.Name, v.Driver))
			}
		}
		// 网络：名称子串，id 给网络名（列表页吃 `?keyword=`），hint 给驱动。
		for _, n := range st.Networks {
			if strings.Contains(strings.ToLower(n.Name), kw) {
				networks = append(networks, host.searchItem(n.Name, n.Name, n.Driver))
			}
		}
		// 项目：名称子串，id 给项目名（工作台路由 `/docker/projects/:name` 吃它），
		// hint 给服务数（项目**声明**的网元数，见 DockerProjectItem.Services）。
		for _, p := range st.Projects {
			if strings.Contains(strings.ToLower(p.Name), kw) {
				projects = append(projects, host.searchItem(p.Name, p.Name, strconv.Itoa(p.Services)))
			}
		}
	}
	// 每组：排序（hostId 升序、同主机按名称）→ 截断 → 报全量与 truncated。
	return &response.DockerSearchResp{
		Groups: []response.DockerSearchGroup{
			finalizeSearchGroup(dockerSearchKindContainers, sortSearchItems(containers), groupCap),
			finalizeSearchGroup(dockerSearchKindImages, sortSearchItems(images), groupCap),
			finalizeSearchGroup(dockerSearchKindVolumes, sortSearchItems(volumes), groupCap),
			finalizeSearchGroup(dockerSearchKindNetworks, sortSearchItems(networks), groupCap),
			finalizeSearchGroup(dockerSearchKindProjects, sortSearchItems(projects), groupCap),
		},
		UnavailableHosts: unavailable,
	}, nil
}

// searchHostRef 是一次遍历里「某台主机」的归属三列（避免每个条目重复取 rec.*）。
type searchHostRef struct {
	hostID   uint64
	hostname string
	group    string
}

// searchItem 造一条命中条目（归属三列 + 展示名 + 下钻标识 + 次要线索）。id 是详情
// 面能直接吃下的参数（各类型口径见 Search 的映射注释）；Group 空时 omitempty 让字段
// 缺席（未分组主机）——与其余读面同一形态。
func (h searchHostRef) searchItem(name, id, hint string) response.DockerSearchItem {
	return response.DockerSearchItem{
		Name: name, ID: id, HostID: h.hostID, Hostname: h.hostname, Group: h.group, Hint: hint,
	}
}

// emptySearchGroups 造 5 个空组（固定顺序）：空查询（q 空）与「五类都未命中」同形。
func emptySearchGroups() []response.DockerSearchGroup {
	return []response.DockerSearchGroup{
		{Kind: dockerSearchKindContainers, Items: []response.DockerSearchItem{}},
		{Kind: dockerSearchKindImages, Items: []response.DockerSearchItem{}},
		{Kind: dockerSearchKindVolumes, Items: []response.DockerSearchItem{}},
		{Kind: dockerSearchKindNetworks, Items: []response.DockerSearchItem{}},
		{Kind: dockerSearchKindProjects, Items: []response.DockerSearchItem{}},
	}
}

// sortSearchItems 按 hostId 升序、同主机按名称升序就地排序（复用清单页的默认次序纪律：
// 集合枚举顺序不保证，不排就是按运气排）。归属两列 + 名称即全部排序键。
func sortSearchItems(items []response.DockerSearchItem) []response.DockerSearchItem {
	sortDockerRows(items, "", "",
		func(it response.DockerSearchItem) uint64 { return it.HostID },
		func(it response.DockerSearchItem) string { return it.Name },
		nil)
	return items
}

// finalizeSearchGroup 把排好序的一组收敛成响应分组：截断到 cap，报截断前全量。
func finalizeSearchGroup(kind string, items []response.DockerSearchItem, cap int) response.DockerSearchGroup {
	total := len(items)
	truncated := total > cap
	if truncated {
		items = items[:cap]
	}
	if items == nil {
		items = []response.DockerSearchItem{}
	}
	return response.DockerSearchGroup{Kind: kind, Items: items, Total: total, Truncated: truncated}
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
