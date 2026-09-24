package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 命令面：下发 / 预览 / 全站目标 / 汇总（设计 §7、§11）──────────────
//
// 所有下发入口（单台 / 多选 / 按筛选 / 全站）**共用同一条路径**：
// classify（分类与跳过）→ applyPlan（建任务 + 终结旧行 + 开新行 + 写目标 + 催办）。
// 分成两条路径就会在「跳过哪几类」「什么时候写目标」上分叉，而分叉的症状是
// 「同一个操作在不同入口下影响面不同」——极难排查。

// UpgradeNotifier 把升级指令推给在线设备（由 wireup 用 agenthub.Hub 适配）。
//
// 升级域**不许直接摸 socket**（设计 §3.1 边界规矩）：Hub 属于 agent 通道，
// 让它被 HTTP 侧直接调用会让两条链路的生命周期纠缠在一起。
type UpgradeNotifier interface {
	// NotifyUpgrade 推送一次升级指令。返回错误表示没送达（设备离线或背压丢弃），
	// 调用方**只记日志**：声明式目标会在设备下次重连的 hello_ack 里生效。
	NotifyUpgrade(ctx context.Context, deviceID uint64, d *agentproto.UpgradeDirective) error
}

// UpgradeActorResolver 把发起人的用户 ID 解析成用户名（UserRepo 满足）。
//
// 任务里存的是用户名**快照**而不是外键：升级记录要能回答「谁在什么时候做了什么」，
// 而用户名会改、用户会被删 —— 联表取当前用户名会让历史记录随时间变形甚至变空白。
type UpgradeActorResolver interface {
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
}

// UpgradeConfigSetter 写配置键（全站目标版本走它，ConfigService 满足）。
type UpgradeConfigSetter interface {
	SetString(ctx context.Context, key, value string) error
}

// 设备在下发时的分类结论（先匹配先算，保证计数确定）。
const (
	skipNone            = ""
	skipDisabled        = "disabled"
	skipUnsupported     = "unsupported"
	skipAlreadyOnTarget = "already_on_target"
	skipNoArtifact      = "no_artifact"
)

// releaseIndex 是「(os, arch) → 产物」的索引：一次查询换逐设备判断，避免 N+1。
type releaseIndex map[string]*entity.AgentRelease

func releaseKey(osName, arch string) string { return osName + "/" + arch }

// upgradePlan 是一次下发的分类结果（**只读**，不含任何写入）。
type upgradePlan struct {
	// devices 是实际会下发的设备（可升级且需要升级）。
	devices []entity.Device
	skip    response.DeviceUpgradeSkip
	// matched 是参与分类的全部设备数（= len(devices) + skip.Total()）。
	matched  int64
	releases releaseIndex
	version  string
}

// classify 给一批设备分类：哪些下发、哪些因为什么跳过。
//
// 判定顺序（**先匹配先算**，顺序变了计数就变）：
//  1. 停用：连不上，下发没有意义；
//  2. 不支持远程升级（agent 自报位为 0，例如现场存量 0.1.0）；
//  3. 已在该版本：无需动。
//     ——**注意这一条不会写设备级目标**：一次全站/筛选下发里，已在该版本的设备
//     应当继续**跟随全站**；若顺手给它写个设备级目标，就等于把它钉死，
//     下次改全站目标时它跟不上，而「跟随全站」的台数会悄悄变成 0。
//  4. 该平台无已发布产物：升了也无从下载。
//
// 离线**不是**跳过理由：声明式目标会在设备重连的 hello_ack 里生效 ——
// 那正是选声明式而不是一次性推送的全部意义。
func (s *DeviceUpgradeService) classify(ctx context.Context, devices []entity.Device,
	version string) (*upgradePlan, error) {
	idx, err := s.releaseIndexFor(ctx, version)
	if err != nil {
		return nil, err
	}
	plan := &upgradePlan{releases: idx, version: version, matched: int64(len(devices))}
	for i := range devices {
		d := devices[i]
		switch {
		case d.Status != entity.DeviceStatusEnabled:
			plan.skip.Disabled++
		case d.AgentUpgradeSupported != 1:
			plan.skip.Unsupported++
		case d.AgentVersion == version:
			plan.skip.AlreadyOnTarget++
		case idx[releaseKey(d.OS, d.Arch)] == nil:
			plan.skip.NoArtifact++
		default:
			plan.devices = append(plan.devices, d)
		}
	}
	return plan, nil
}

// releaseIndexFor 读该版本的已发布产物并建索引。
func (s *DeviceUpgradeService) releaseIndexFor(ctx context.Context, version string) (releaseIndex, error) {
	rows, err := s.releases.ListByVersion(ctx, version)
	if err != nil {
		return nil, err
	}
	idx := make(releaseIndex, len(rows))
	for i := range rows {
		idx[releaseKey(rows[i].OS, rows[i].Arch)] = &rows[i]
	}
	return idx, nil
}

// applyPlan 落地一次下发：建任务 → 逐台（终结旧行 + 开新行 + 写目标 + 催办）。
//
// 逐台写而不是批量 SQL：一次批量下发是低频操作（人工动作），几十条 UPDATE 不构成
// 问题；而把「终结旧行 → 开新行 → 写目标 → 催办」压成一条 SQL 会让每一步的失败
// 都无法单独观测。
func (s *DeviceUpgradeService) applyPlan(ctx context.Context, plan *upgradePlan,
	source, actor string) (uint64, error) {
	if len(plan.devices) == 0 {
		return 0, nil // 无可下发的设备：**不建空任务**（空任务只会污染任务列表）
	}
	task := &entity.AgentUpgradeTask{
		TargetVersion: plan.version,
		Source:        source,
		Actor:         actor,
		Total:         len(plan.devices),
	}
	if err := s.tasks.Create(ctx, task); err != nil {
		return 0, apperror.Internal("创建升级任务失败", err)
	}

	now := time.Now()
	for i := range plan.devices {
		dev := plan.devices[i]
		// ① 终结该设备的未终结尝试（D1：一台设备同时至多一条未终结）。
		if _, err := s.attempts.SupersedeOpen(ctx, dev.ID, now); err != nil {
			s.log.Warn("supersede open attempt failed", zap.Uint64("device_id", dev.ID), zap.Error(err))
		}
		// ② 开新行。
		attempt := &entity.AgentUpgradeAttempt{
			TaskID:      &task.ID,
			DeviceID:    dev.ID,
			FromVersion: dev.AgentVersion,
			ToVersion:   plan.version,
			RequestID:   newRequestID(),
			State:       entity.AttemptStatePending,
			CreatedAt:   now,
		}
		if err := s.attempts.Create(ctx, attempt); err != nil {
			s.log.Warn("create attempt failed", zap.Uint64("device_id", dev.ID), zap.Error(err))
			continue
		}
		// ③ 写目标（声明式事实源）。
		if err := s.devices.SetUpgradeTarget(ctx, dev.ID, plan.version); err != nil {
			s.log.Warn("set upgrade target failed", zap.Uint64("device_id", dev.ID), zap.Error(err))
		}
		// ④ 催办（在线才有意义；失败只记日志）。
		rel := plan.releases[releaseKey(dev.OS, dev.Arch)]
		if rel == nil {
			continue // classify 已保证不为 nil，这里是防御
		}
		if s.notifier == nil {
			continue
		}
		err := s.notifier.NotifyUpgrade(ctx, dev.ID, &agentproto.UpgradeDirective{
			RequestID:     attempt.RequestID,
			TargetVersion: attempt.ToVersion,
			SHA256:        rel.SHA256,
			SizeBytes:     rel.SizeBytes,
		})
		if err != nil && !errors.Is(err, ErrDeviceOffline) {
			s.log.Warn("upgrade nudge failed",
				zap.Uint64("device_id", dev.ID), zap.Error(err))
		}
	}
	s.log.Info("agent upgrade dispatched",
		zap.String("version", plan.version), zap.String("source", source),
		zap.Int("dispatched", len(plan.devices)), zap.Uint64("task_id", task.ID))
	return task.ID, nil
}

// SetDeviceTarget 是**单台**下发的完整路径（pin = 固定在当前版本）。
//
// pin=true 时即使版本与当前相同也写设备级目标 —— 这是「固定」的全部语义：
// 让这台设备不再跟随全站。普通下发不传 pin（见 DeviceUpgradeTargetRequest.Pin 的说明）。
func (s *DeviceUpgradeService) SetDeviceTarget(ctx context.Context, deviceID uint64,
	version string, pin bool, actorID uint64) (*response.DeviceUpgradeDispatchResp, error) {
	actor := s.actorName(ctx, actorID)
	if !agentproto.IsSemver(version) {
		return nil, apperror.BadRequest("版本号格式不正确")
	}
	dev, err := s.devices.FindByID(ctx, deviceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("设备不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	plan, err := s.classify(ctx, []entity.Device{*dev}, version)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	// 固定意图：目标是设备级写入，与「是否需要升级」解耦 —— 设备已在该版本上时
	// 不该升级，但仍要把它钉住。
	if pin {
		if err := s.devices.SetUpgradeTarget(ctx, deviceID, version); err != nil {
			return nil, apperror.Internal("写入升级目标失败", err)
		}
	}
	taskID, err := s.applyPlan(ctx, plan, entity.AgentUpgradeSourceManual, actor)
	if err != nil {
		return nil, err
	}
	resp := &response.DeviceUpgradeDispatchResp{
		TargetVersion: version,
		Dispatched:    int64(len(plan.devices)),
		Skip:          plan.skip,
	}
	if taskID != 0 {
		resp.TaskID = strconv.FormatUint(taskID, 10)
	}
	return resp, nil
}

// ClearDeviceTarget 清空设备级目标（= 恢复跟随全站）。
//
// 不建任务、不发指令：这只是一个「不再钉住」的声明，设备侧的当前版本不需要改变。
func (s *DeviceUpgradeService) ClearDeviceTarget(ctx context.Context, deviceID uint64) error {
	if _, err := s.devices.FindByID(ctx, deviceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.NotFound("设备不存在")
		}
		return apperror.Internal("内部错误", err)
	}
	if err := s.devices.SetUpgradeTarget(ctx, deviceID, ""); err != nil {
		return apperror.Internal("清空升级目标失败", err)
	}
	return nil
}

// Preview 是「按筛选/多选下发」的影响面预演（下发前给人看的数字）。
func (s *DeviceUpgradeService) Preview(ctx context.Context,
	req *request.DeviceBatchUpgradeRequest) (*response.DeviceUpgradePreviewResp, error) {
	if !agentproto.IsSemver(req.Version) {
		return nil, apperror.BadRequest("版本号格式不正确")
	}
	devices, err := s.resolveTargets(ctx, req)
	if err != nil {
		return nil, err
	}
	plan, err := s.classify(ctx, devices, req.Version)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	return &response.DeviceUpgradePreviewResp{
		TargetVersion: req.Version,
		Matched:       plan.matched,
		WillUpgrade:   int64(len(plan.devices)),
		Skip:          plan.skip,
	}, nil
}

// Dispatch 执行一次批量下发（多选或按筛选）。
//
// 按筛选路径的**防呆**：调用方必须先 Preview 并把它给出的命中台数作为
// expectedCount 一起提交；服务端重新求值后不一致就 409 —— 两次之间设备集合变了
// （新注册、被删、被停用）意味着影响面已经不是操作者确认过的那个，
// 静默照做或静默缩小都是错的。
func (s *DeviceUpgradeService) Dispatch(ctx context.Context,
	req *request.DeviceBatchUpgradeRequest, source string, actorID uint64) (*response.DeviceUpgradeDispatchResp, error) {
	actor := s.actorName(ctx, actorID)
	if !agentproto.IsSemver(req.Version) {
		return nil, apperror.BadRequest("版本号格式不正确")
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		return nil, apperror.BadRequest("请选择设备或筛选条件")
	}
	if len(req.IDs) > 0 && req.Filter != nil {
		return nil, apperror.BadRequest("设备列表与筛选条件只能二选一")
	}
	devices, err := s.resolveTargets(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Filter != nil {
		if req.ExpectedCount == nil {
			return nil, apperror.BadRequest("缺少影响面确认")
		}
		if int64(len(devices)) != *req.ExpectedCount {
			// 文案只讲结论：不说「expectedCount 不匹配」这种字段名。
			return nil, apperror.Conflict("筛选结果已变化，请重新确认")
		}
	}
	plan, err := s.classify(ctx, devices, req.Version)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	taskID, err := s.applyPlan(ctx, plan, source, actor)
	if err != nil {
		return nil, err
	}
	resp := &response.DeviceUpgradeDispatchResp{
		TargetVersion: req.Version,
		Dispatched:    int64(len(plan.devices)),
		Skip:          plan.skip,
	}
	if taskID != 0 {
		resp.TaskID = strconv.FormatUint(taskID, 10)
	}
	return resp, nil
}

// resolveTargets 把请求解析成设备集合（ids 或 筛选，二者已由调用方保证只有一个）。
func (s *DeviceUpgradeService) resolveTargets(ctx context.Context,
	req *request.DeviceBatchUpgradeRequest) ([]entity.Device, error) {
	switch {
	case len(req.IDs) > 0:
		devices, err := s.devices.FindByIDs(ctx, []uint64(req.IDs))
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		return devices, nil
	case req.Filter != nil:
		devices, err := s.devices.FindByUpgradeFilter(ctx, req.Filter, s.onlineSince(ctx))
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		return devices, nil
	default:
		return nil, apperror.BadRequest("请选择设备或筛选条件")
	}
}

// SetGlobalTarget 设置/清除全站目标版本。
//
// 三条纪律：
//   - 版本必须**已有已发布产物**（否则全站设备都会因「无产物」跳过，
//     设了等于没设，而页面上看不出为什么）；
//   - 只影响**跟随全站**的设备（设备级指定的一律不动，并给出台数）；
//   - 变更与下发在同一个动作里完成（写配置键 + 建任务 + 逐台催办）。
func (s *DeviceUpgradeService) SetGlobalTarget(ctx context.Context, version string,
	actorID uint64) (*response.DeviceUpgradeGlobalResp, error) {
	actor := s.actorName(ctx, actorID)
	if version != "" && !agentproto.IsSemver(version) {
		return nil, apperror.BadRequest("版本号格式不正确")
	}
	if version != "" {
		// 先确认该版本**已有已发布产物**再写配置：否则全站设备都会因「无产物」跳过，
		// 设了等于没设，而页面上看不出为什么（目标在那儿、一台都不动）。
		rows, err := s.releases.ListByVersion(ctx, version)
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		if len(rows) == 0 {
			return nil, apperror.BadRequest("该版本还没有可用的程序文件")
		}
	}
	if err := s.setter.SetString(ctx, ConfigTargetVersion, version); err != nil {
		return nil, err
	}
	resp := &response.DeviceUpgradeGlobalResp{TargetVersion: version}
	if version == "" {
		return resp, nil // 关闭全站升级：没有任何设备需要因此改变版本
	}

	devices, err := s.devices.FindByUpgradeFilter(ctx, &request.DeviceQuery{}, s.onlineSince(ctx))
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	// 只挑「跟随全站」的设备：设备级指定的一律不动。
	followers := make([]entity.Device, 0, len(devices))
	for i := range devices {
		if devices[i].TargetAgentVersion != "" {
			resp.PinnedDevices++
			continue
		}
		followers = append(followers, devices[i])
	}
	plan, err := s.classify(ctx, followers, version)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	taskID, err := s.applyPlan(ctx, plan, entity.AgentUpgradeSourceGlobal, actor)
	if err != nil {
		return nil, err
	}
	resp.Affected = int64(len(plan.devices))
	if taskID != 0 {
		resp.TaskID = strconv.FormatUint(taskID, 10)
	}
	return resp, nil
}

// Summary 是「当前状态」视角：按生效目标分桶 + 版本分布 + 跟随/固定台数。
//
// 台数不大（几百台）时全量载入内存分类是最简单且最快的方式：三次查询
// （设备 / 未终结尝试 / 各目标版本的产物）+ 一次内存遍历，比让 SQL 做等效聚合
// 更容易保证与「读时推导」的口径一致（页面上每一行的相位就是这里算出来的）。
func (s *DeviceUpgradeService) Summary(ctx context.Context) (*response.DeviceUpgradeSummaryResp, error) {
	devices, err := s.devices.FindByUpgradeFilter(ctx, &request.DeviceQuery{}, s.onlineSince(ctx))
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	ids := make([]uint64, 0, len(devices))
	for i := range devices {
		ids = append(ids, devices[i].ID)
	}
	open, err := s.attempts.FindOpenByDevices(ctx, ids)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}

	global := s.cfg.GetString(ctx, ConfigTargetVersion, "")
	resp := &response.DeviceUpgradeSummaryResp{
		Total:               int64(len(devices)),
		GlobalTargetVersion: global,
	}
	buckets := map[string]*response.DeviceUpgradeBucket{}
	versionCount := map[string]int64{}
	// 产物索引按目标版本缓存（同一目标只查一次库）。
	idxCache := map[string]releaseIndex{}

	for i := range devices {
		dev := devices[i]
		versionCount[dev.AgentVersion]++
		target := dev.TargetAgentVersion
		if target == "" {
			target = global
		}
		if dev.TargetAgentVersion != "" {
			resp.PinnedDevices++
		}
		// 桶按目标版本聚合（空目标 = 无目标桶）。
		bucket := buckets[target]
		if bucket == nil {
			bucket = &response.DeviceUpgradeBucket{TargetVersion: target}
			buckets[target] = bucket
		}
		bucket.Total++

		if target == "" {
			// 无目标：不参与任何相位计数（页面显示「未设置目标」）。
			continue
		}
		switch {
		case dev.AgentVersion == target:
			bucket.Achieved++
		case dev.AgentUpgradeSupported != 1:
			bucket.Unsupported++
		case dev.Status != entity.DeviceStatusEnabled:
			bucket.Disabled++
		default:
			idx, ok := idxCache[target]
			if !ok {
				idx, err = s.releaseIndexFor(ctx, target)
				if err != nil {
					return nil, apperror.Internal("内部错误", err)
				}
				idxCache[target] = idx
			}
			if idx[releaseKey(dev.OS, dev.Arch)] == nil {
				bucket.NoArtifact++
				continue
			}
			switch {
			case open[dev.ID] != nil:
				// **等待开工 ≠ 升级中**：下发时插的就是 pending 行（设备可能还没上线），
				// 只有收到过第一份状态上报才算真的在跑。两者混在一起会让运维去等一台
				// 根本没开始、甚至永远不上线的机器。
				if open[dev.ID].State == entity.AttemptStatePending {
					bucket.Pending++
				} else {
					bucket.Running++
				}
			case dev.AgentUpgradeState == entity.DeviceUpgradeFailed:
				bucket.Failed++
			case dev.AgentUpgradeState == entity.DeviceUpgradeRolledBack:
				bucket.RolledBack++
			default:
				bucket.Pending++
			}
		}
	}

	// 顺序稳定：有目标的桶按版本新→旧，无目标桶放最后 —— 每次进页面顺序都变
	// 会让人以为数据在动。
	for _, b := range buckets {
		resp.Buckets = append(resp.Buckets, *b)
	}
	sort.Slice(resp.Buckets, func(i, j int) bool {
		a, b := resp.Buckets[i], resp.Buckets[j]
		if a.TargetVersion == "" || b.TargetVersion == "" {
			return b.TargetVersion == "" && a.TargetVersion != ""
		}
		return a.TargetVersion > b.TargetVersion
	})
	for v, n := range versionCount {
		resp.VersionDistribution = append(resp.VersionDistribution,
			response.DeviceVersionCount{Version: v, Count: n})
	}
	sort.Slice(resp.VersionDistribution, func(i, j int) bool {
		a, b := resp.VersionDistribution[i], resp.VersionDistribution[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Version > b.Version
	})
	return resp, nil
}

// onlineSince 复用设备列表的在线阈值口径（sys.agent.offlineThreshold），
// 使「按筛选下发」与列表页筛出的集合完全一致。常量与实现见 device.go。
func (s *DeviceUpgradeService) onlineSince(ctx context.Context) time.Time {
	sec := s.cfg.GetInt(ctx, ConfigOfflineThreshold, 30)
	if sec <= 0 {
		sec = 30
	}
	return time.Now().Add(-time.Duration(sec) * time.Second)
}

// actorName 把用户 ID 解析成用户名快照（失败时返回空串并记日志）。
//
// 解析失败**不阻断下发**：任务里少一个显示名比「点了升级没反应」轻得多，
// 而运营的处置动作（谁发起的）另有操作日志兜底。
func (s *DeviceUpgradeService) actorName(ctx context.Context, actorID uint64) string {
	if actorID == 0 || s.actors == nil {
		return ""
	}
	u, err := s.actors.FindByID(ctx, actorID)
	if err != nil || u == nil {
		s.log.Warn("resolve upgrade actor failed", zap.Uint64("user_id", actorID), zap.Error(err))
		return ""
	}
	return u.Username
}

// ErrDeviceOffline 由 notifier 实现返回，表示设备当前没连（**不是故障** ——
// 因而调用方只在日志里降一级，不需要断连、重试或向用户报错）。
var ErrDeviceOffline = errors.New("device offline")

// staleAfter 是巡检判超时的阈值（设计 §12 的时间常数表）。
//
// 取 15 分钟的依据：它必须显著大于任何一个正常阶段的时长 —— 下载在内网是秒级、
// 替换是毫秒级、本地自愈的试用期是 180 秒（含探测预算约 5 分钟）。定成 15 分钟
// 意味着「只有真的卡住才会被判超时」，不会误伤慢设备。
const staleAfter = 15 * time.Minute

// SweepStale 把「已开工但长时间没动静」的尝试判超时（由巡检任务每分钟调用）。
//
// 三条语义：
//   - **只看已开工的行**（FindStale 的 SQL 条件）：「等待设备上线」不是卡住，
//     声明式目标对离线设备仍然生效，判它超时等于把「还没轮到」说成「失败」；
//   - 终结用**带守卫的按行更新**（FinishByID）：与状态上报/hello 归位并发时，
//     后到的那条影响 0 行 → 跳过（不重复记终态、不覆盖别人的结论）；
//   - 终态同步到设备行并尝试收口任务 —— 与另外两条终结路径一致。
func (s *DeviceUpgradeService) SweepStale(ctx context.Context, olderThan time.Duration,
	limit int) (int, error) {
	if olderThan <= 0 {
		olderThan = staleAfter
	}
	now := time.Now()
	rows, err := s.attempts.FindStale(ctx, now.Add(-olderThan), limit)
	if err != nil {
		return 0, err
	}
	swept := 0
	for i := range rows {
		a := rows[i]
		n, err := s.attempts.FinishByID(ctx, a.ID, entity.AttemptStateTimeout,
			entity.AttemptReasonTimeout, now)
		if err != nil {
			s.log.Warn("sweep stale attempt failed", zap.Uint64("attempt_id", a.ID), zap.Error(err))
			continue
		}
		if n == 0 {
			continue // 已被并发路径终结（上报/归位）：不重复记
		}
		swept++
		s.log.Info("agent upgrade attempt timed out",
			zap.Uint64("device_id", a.DeviceID), zap.String("to", a.ToVersion),
			zap.String("state", a.State))
		s.syncDeviceTerminal(ctx, a.DeviceID, entity.AttemptStateTimeout,
			entity.AttemptReasonTimeout, now)
		s.settleTask(ctx, a.TaskID, now)
	}
	return swept, nil
}
