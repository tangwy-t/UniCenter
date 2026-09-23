package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// 设备状态与在线判定用到的配置键。
const (
	ConfigOfflineThreshold = "sys.agent.offlineThreshold"
)

// 资源「消失」判定的**两个**阈值（spec §8 的两条要求是两条，不是一个数）。
//
//	① 「`/resources` 用 `last_seen_at` 给出 `stale` 标记」——让前端看出「已消失」；
//	② 「设过期（如 90d 未出现即不再枚举）」——不再堆在 drill 下拉里。
//
// 为什么必须分成两个（S1，spec 明文）：若两者共用一个阈值，② 的 SQL 过滤
// 会把所有该标记的行先滤掉，① 的 `Stale` 就恒为 false、彻底失去信息量。
// 实测的旧行为更糟：① 与 ② 都没实现——过滤被忽略（staleBefore 传了不用），
// 200 天前的挂载点照样枚举。
const (
	// resourceEnumWindowDays 是**枚举窗口**：超过它未再被观测到的资源不再枚举。
	resourceEnumWindowDays = 90
	// resourceStaleMarkDays 是 Stale 标记阈值：仍在枚举窗口内、但已超过它未再
	// 出现的资源标 Stale（前端显示「可能已消失」）。
	// 取 1 天：flush 每 5min 一轮，仍存在的资源每轮都会刷新 last_seen_at，
	// 连续一天没刷新基本等于已卸载；同时容忍设备最长一天的离线。
	resourceStaleMarkDays = 1
)

// DeviceService 是设备域的读写服务。
type DeviceService struct {
	repo      DeviceRepository
	resources DeviceResourceRepository
	raw       DeviceRedisPurger
	latest    DeviceLatestReader
	cfg       AgentConfigGetter
	// upgrade 是升级域的信息面（生效目标 / 未终结尝试 / 终态 / 一键回滚版本）。
	//
	// 可空：nil 时设备响应里不带升级字段（升级域未装配的部署形态仍要能工作）。
	// 之所以由设备服务去**问**升级域而不是自己算：生效目标与「一次尝试」的口径
	// 只有升级域那一份（ResolveTargetVersion），这里再算一遍就是第二份实现。
	upgrade DeviceUpgradeInfoProvider
	log     logger.LoggerInterface
}

// DeviceUpgradeInfoProvider 是设备服务需要的升级域信息（由 DeviceUpgradeService 实现）。
type DeviceUpgradeInfoProvider interface {
	ResolveTargetVersion(ctx context.Context, dev *entity.Device) string
	Snapshot(ctx context.Context, devices []entity.Device) (map[uint64]DeviceUpgradeSnapshot, error)
	RollbackVersion(ctx context.Context, deviceID uint64) (string, error)
}

// 消费方窄接口。
//
// 注意：DeviceService **只管管理域**（列表/详情/资源/启停/删除）。
// 指标查询（Metrics/ResourceMetrics）属于 AgentMetricsQueryService（Task 8），
// handler 层注入两个依赖，因此这里不出现任何指标查询能力。
type DeviceRepository interface {
	FindByID(ctx context.Context, id uint64) (*entity.Device, error)
	FindPage(ctx context.Context, q *request.DeviceQuery, onlineSince time.Time) ([]entity.Device, int64, error)
	SetStatus(ctx context.Context, id uint64, status int8) error
	Delete(ctx context.Context, id uint64) error
}

type DeviceResourceRepository interface {
	ListByDevice(ctx context.Context, deviceID uint64, kind string, staleBefore time.Time) ([]entity.DeviceResource, error)
	DeleteByDevice(ctx context.Context, deviceID uint64) error
}

// DeviceRedisPurger 删除设备时的 Redis 连带清理能力（spec §7.3：否则 key 永久泄漏）。
type DeviceRedisPurger interface {
	Purge(ctx context.Context, deviceID uint64) error
}

// DeviceLatestReader 读水位（列表页用 GetMany 一次取整页）。
type DeviceLatestReader interface {
	GetMany(ctx context.Context, deviceIDs []uint64) (map[uint64]*agentmetrics.LatestSummary, error)
}

func NewDeviceService(repo DeviceRepository, resources DeviceResourceRepository,
	raw DeviceRedisPurger, latest DeviceLatestReader, cfg AgentConfigGetter,
	upgrade DeviceUpgradeInfoProvider, log logger.LoggerInterface) *DeviceService {
	return &DeviceService{repo: repo, resources: resources, raw: raw, latest: latest,
		cfg: cfg, upgrade: upgrade, log: log}
}

// offlineThresholdSec 返回**实际生效**的离线判定阈值（秒）。
//
// 抽成独立函数是为了让「响应里下发的 offlineThresholdSec」与「online 的判定」
// 出自**同一次配置读取、同一套缺省与钳制**。若各算一遍，一旦 cfg 在两次读之间
// 热更（阈值是可热更配置），响应就会出现「online=true 但阈值说它该离线」这种
// 自相矛盾的结果 —— 而调用方（前端文案）会照着那个阈值给用户解释。
func (s *DeviceService) offlineThresholdSec(ctx context.Context) int {
	sec := s.cfg.GetInt(ctx, ConfigOfflineThreshold, 30)
	if sec <= 0 {
		// <=0 视为未配置/配置错误：回落 30。不返回 0 —— 阈值 0 会让
		// last_seen_at >= now 恒不成立，等于把**所有**设备判成离线。
		sec = 30
	}
	return sec
}

// onlineSince 把「离线阈值（秒）」折算成时间点：last_seen_at >= 它即为在线。
func (s *DeviceService) onlineSince(ctx context.Context) time.Time {
	return time.Now().Add(-time.Duration(s.offlineThresholdSec(ctx)) * time.Second)
}

// List 返回分页列表，并**一次 pipeline** 拼上 latest 水位（不扫指标表）。
//
// onlineSince 在本方法内**只算一次**：它既用于列表过滤（交给仓储），
// 也用于逐行的 Online 判定与 toListItem。取两次会在跨秒边界上让
// 「SQL 过滤用的阈值」与「响应里 Online 用的阈值」不一致。
func (s *DeviceService) List(ctx context.Context, q *request.DeviceQuery) (*app.PageResponse, error) {
	if q == nil {
		q = &request.DeviceQuery{}
	}
	since := s.onlineSince(ctx)
	list, total, err := s.repo.FindPage(ctx, q, since)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}

	ids := make([]uint64, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	watermarks, err := s.latest.GetMany(ctx, ids)
	if err != nil {
		// 水位失败不阻断列表：置空即可（前端显示「—」）
		s.log.Warn("device latest batch read failed")
		watermarks = nil
	}

	// 升级信息**批量**取（一次查询算全页的「是不是升级中」），失败不阻断列表：
	// 升级字段缺失比整个列表打不开轻得多（与水位失败同一取向）。
	snaps := s.upgradeSnapshots(ctx, list)
	items := make([]response.DeviceListItem, 0, len(list))
	for i := range list {
		items = append(items, s.toListItem(&list[i], watermarks[list[i].ID], since, snaps[list[i].ID]))
	}

	page, size := q.GetPage(), q.GetPageSize()
	return app.NewPageResponse(items, total, page, size), nil
}

// findDevice 读设备并把仓储错误映射成 AppError。
//
// **必须分辨未命中与故障**（S5）：`errors.Is(err, repository.ErrNotFound)`（仓储
// 未命中哨兵）→ 404 设备不存在；其它任何错误（连接断开、超时、约束冲突…）→
// 500 Internal 且**带上 cause**。曾把任何错误都映射成 NotFound，于是 DB 故障会
// 伪装成 404 —— 运营看到「设备不存在」去排查设备，真正的问题却在数据库。
//
// 三处存在性检查（GetByID / Resources / Delete）共用本函数，避免再出现
// 「其中一处改了口径、另一处没改」的漂移（S5 的现状正是只有 SetStatus/Delete
// 用了哨兵，其余是 `err != nil → NotFound`）。
func (s *DeviceService) findDevice(ctx context.Context, id uint64) (*entity.Device, error) {
	d, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("设备不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	if d == nil {
		// 桩/异常实现返回 (nil, nil)：语义上就是未命中（真仓储用哨兵表达）。
		return nil, apperror.NotFound("设备不存在")
	}
	return d, nil
}

// GetByID 返回设备详情（含水位）。
func (s *DeviceService) GetByID(ctx context.Context, id uint64) (*response.DeviceResp, error) {
	d, err := s.findDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	watermarks, _ := s.latest.GetMany(ctx, []uint64{id})
	// 阈值只读一次，同时喂给 online 判定与响应字段（见 offlineThresholdSec 的说明）。
	thresholdSec := s.offlineThresholdSec(ctx)
	snaps := s.upgradeSnapshots(ctx, []entity.Device{*d})
	item := s.toListItem(d, watermarks[id], time.Now().Add(-time.Duration(thresholdSec)*time.Second), snaps[id])
	// 一键回滚目标：由升级域从升级记录推导（「升级前是什么版本」不该让运维回忆）。
	var rollback string
	if s.upgrade != nil {
		if v, err := s.upgrade.RollbackVersion(ctx, id); err == nil {
			rollback = v
		}
	}
	detail := response.DeviceResp{
		DeviceListItem:  item,
		RollbackVersion: rollback,
		Platform:        d.Platform, PlatformVer: d.PlatformVer, Kernel: d.Kernel,
		CPUModel: d.CPUModel, CPUCores: d.CPUCores, MemTotalMB: d.MemTotalMB,
		BootTime: d.BootTime, CreatedAt: d.CreatedAt.Unix(),
		// 观测到的来源 IP（服务端取值，见 entity.Device.PrimaryIP 与 handler）。
		PrimaryIP: normalizedIP(d.PrimaryIP),
		// 与 online 判定同源的阈值，供 UI 解释「多久没上报算离线」。
		OfflineThresholdSec: thresholdSec,
	}
	return &detail, nil
}

// Resources 枚举某设备的资源（drill 下拉数据源），按 last_seen_at 标记 stale。
//
// 两个阈值分别生效（spec §8，见上方常量说明）：
//   - 超过 resourceEnumWindowDays 未出现的资源**不再枚举**（由仓储的 SQL 过滤执行）；
//   - 枚举出来的行里，超过 resourceStaleMarkDays 未出现的标 Stale=true，
//     前端据此标注「已消失」——不删除行，历史仍可追溯。
func (s *DeviceService) Resources(ctx context.Context, id uint64, kind string) (*response.DeviceResourcesResp, error) {
	if _, err := s.findDevice(ctx, id); err != nil {
		return nil, err
	}
	// 两个阈值都只算一次：仓储过滤与逐行 Stale 判定必须用同一组时间点。
	now := time.Now()
	enumSince := now.AddDate(0, 0, -resourceEnumWindowDays)
	staleBefore := now.AddDate(0, 0, -resourceStaleMarkDays)
	rows, err := s.resources.ListByDevice(ctx, id, kind, enumSince)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	list := make([]response.DeviceResourceItem, 0, len(rows))
	for _, r := range rows {
		list = append(list, response.DeviceResourceItem{
			Name: r.Name, Kind: r.Kind,
			LastSeenAt: r.LastSeenAt.Unix(),
			Stale:      r.LastSeenAt.Before(staleBefore),
		})
	}
	return &response.DeviceResourcesResp{List: list}, nil
}

// Enable / Disable 切换管理侧启停态（与在线状态正交）。
//
// 错误映射**必须分辨未命中与故障**（Task 9 上报的观察点）：
//   - `repository.ErrNotFound`（仓储未命中哨兵）→ 404 设备不存在；
//   - 其它任何错误（连接断开、超时、约束冲突…）→ 500 Internal。
//
// 曾把**任何**错误都映射成 NotFound，于是 DB 故障会伪装成 404 ——
// 运营看到「设备不存在」去排查设备，真正的问题却在数据库。
func (s *DeviceService) Enable(ctx context.Context, id uint64) error {
	return s.setStatus(ctx, id, entity.DeviceStatusEnabled)
}

func (s *DeviceService) Disable(ctx context.Context, id uint64) error {
	return s.setStatus(ctx, id, entity.DeviceStatusDisabled)
}

func (s *DeviceService) setStatus(ctx context.Context, id uint64, status int8) error {
	if err := s.repo.SetStatus(ctx, id, status); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.NotFound("设备不存在")
		}
		return apperror.Internal("内部错误", err)
	}
	return nil
}

// Delete 软删设备，并**连带清理** Redis 侧与资源维度行。
//
// 顺序很重要：先确认设备存在（否则 404），再清 Redis/资源，最后软删 ——
// 这样即使中途失败，设备仍在（可重试），不会出现「设备没了但 key 还在」的孤儿状态。
//
// 存在性检查走 findDevice：未命中 → 404，其它错误 → 500（不得伪装成 404）。
func (s *DeviceService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.findDevice(ctx, id); err != nil {
		return err
	}
	if err := s.raw.Purge(ctx, id); err != nil {
		s.log.Warn("device redis purge failed")
	}
	if err := s.resources.DeleteByDevice(ctx, id); err != nil {
		return apperror.Internal("内部错误", err)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperror.Internal("内部错误", err)
	}
	return nil
}

// normalizedIP 归一化来源 IP，仅做**展示层**清洗。
//
// 处理两种真实会让 UI 显示奇怪的情况：
//  1. IPv4-mapped IPv6：Go 的 net 在双栈监听下常给出 `::ffff:192.168.1.5`。
//     原样展示既长又容易让人以为设备走的是 IPv6。用 To4() 归一成点分十进制。
//  2. 杂散空白：代理头（X-Forwarded-For）可能带空格。TrimSpace 掉。
//
// **刻意不做**的事：
//   - 不解析多段 XFF 取「第一个」——那是 Gin ClientIP/trustedProxies 的职责，
//     在这一层再解析会与它形成第二份可信边界实现（安全敏感的重复实现）。
//   - 不校验合法性并抛错：IP 只用于展示，解析失败就原样返回 txt，
//     宁可显示一个怪字符串，也不要把「有值」变成「空值」。
func normalizedIP(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 只需要对付带端口的少数情况；SplitHostPort 对裸 IP 会报错，此时用原串。
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		s = host
	}
	if ip := net.ParseIP(s); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return s
}

// toListItem 把实体 + 水位投影成列表项。
//
// onlineSince 由调用方算好传入：同一请求内在线阈值只取一次配置，
// 避免「列表里两台设备用了不同阈值」（跨秒边界时会真的不一致）。
func (s *DeviceService) toListItem(d *entity.Device, w *agentmetrics.LatestSummary,
	onlineSince time.Time, snap DeviceUpgradeSnapshot) response.DeviceListItem {
	item := response.DeviceListItem{
		ID: d.ID, Hostname: d.Hostname, OS: d.OS, Arch: d.Arch,
		AgentVersion: d.AgentVersion, Status: d.Status,
		Online: d.LastSeenAt != nil && d.LastSeenAt.After(onlineSince),
		// 升级字段全部来自升级域的同一份快照（含读时推导的相位）。
		AgentUpgradeSupported: d.AgentUpgradeSupported == 1,
		TargetVersion:         snap.EffectiveTargetVersion,
		TargetFromGlobal:      snap.TargetFromGlobal,
		UpgradePhase:          upgradePhaseOf(d, snap),
		UpgradeResult:         snap.TerminalState,
		UpgradeReason:         snap.TerminalReason,
		UpgradeAt:             unixPtr(snap.TerminalAt),
	}
	if d.LastSeenAt != nil {
		v := d.LastSeenAt.Unix()
		item.LastSeenAt = &v
	}
	if w != nil {
		item.CPUUsedPercent = w.CPUUsedPercent
		item.MemUsedPercent = w.MemUsedPercent
		item.DiskUsedPercent = w.DiskUsedPercent
		sec := w.T / 1000
		item.WatermarkAt = &sec
	}
	return item
}

// upgradeSnapshots 取一批设备的升级快照（升级域未装配时返回空 map，字段自然为空）。
func (s *DeviceService) upgradeSnapshots(ctx context.Context,
	devices []entity.Device) map[uint64]DeviceUpgradeSnapshot {
	if s.upgrade == nil || len(devices) == 0 {
		return nil
	}
	snaps, err := s.upgrade.Snapshot(ctx, devices)
	if err != nil {
		s.log.Warn("device upgrade snapshot failed")
		return nil
	}
	return snaps
}

// upgradePhaseOf 推导设备在列表上的升级相位（**读时计算、不落库**，设计 §3.4）。
//
// 三态：achieved（版本与生效目标一致）/ running（有未终结尝试且在跑）/ pending（有目标待升）。
// 无目标时返回空串（页面显示「—」，而不是伪造一个「待升级」）。
//
// 注意 running 与 pending 的区分：尝试存在但仍是 pending（设备还没上线或没开工）
// 属于**待升级**，把它显示成「升级中」会让人去等一台根本没开始的机器。
func upgradePhaseOf(d *entity.Device, snap DeviceUpgradeSnapshot) string {
	if snap.EffectiveTargetVersion == "" {
		return ""
	}
	if d.AgentVersion == snap.EffectiveTargetVersion {
		return "achieved"
	}
	if snap.Running() {
		return "running"
	}
	return "pending"
}
