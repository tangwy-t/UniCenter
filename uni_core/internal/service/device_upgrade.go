package service

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// UpgradeDeviceRepository 是本服务用到的设备仓储方法集。
type UpgradeDeviceRepository interface {
	FindByID(ctx context.Context, id uint64) (*entity.Device, error)
	FindByIDs(ctx context.Context, ids []uint64) ([]entity.Device, error)
	FindByUpgradeFilter(ctx context.Context, q *request.DeviceQuery, onlineSince time.Time) ([]entity.Device, error)
	SetUpgradeTarget(ctx context.Context, id uint64, target string) error
	SetUpgradeTerminal(ctx context.Context, id uint64, state int8, reasonCode string, at time.Time) error
}

// UpgradeAttemptRepository 是升级尝试明细的方法集。
type UpgradeAttemptRepository interface {
	Create(ctx context.Context, a *entity.AgentUpgradeAttempt) error
	FindByRequestID(ctx context.Context, deviceID uint64, requestID string) (*entity.AgentUpgradeAttempt, error)
	FindOpenByDevice(ctx context.Context, deviceID uint64) (*entity.AgentUpgradeAttempt, error)
	FindOpenByDevices(ctx context.Context, deviceIDs []uint64) (map[uint64]*entity.AgentUpgradeAttempt, error)
	ListByDevice(ctx context.Context, deviceID uint64, limit int) ([]entity.AgentUpgradeAttempt, error)
	FindPageByTask(ctx context.Context, taskID uint64, q *request.AgentUpgradeAttemptQuery) ([]entity.AgentUpgradeAttempt, int64, error)
	FindLastSucceededFrom(ctx context.Context, deviceID uint64) (string, error)
	FindStale(ctx context.Context, before time.Time, limit int) ([]entity.AgentUpgradeAttempt, error)
	FinishByID(ctx context.Context, id uint64, state, reasonCode string, at time.Time) (int64, error)
	SupersedeOpen(ctx context.Context, deviceID uint64, at time.Time) (int64, error)
	UpdateReport(ctx context.Context, a *entity.AgentUpgradeAttempt) error
	FinishOpen(ctx context.Context, deviceID uint64, state, reasonCode string, at time.Time) (int64, error)
}

// UpgradeTaskRepository 是任务面的方法集（建任务 / 列表 / 明细聚合 / 收口）。
type UpgradeTaskRepository interface {
	Create(ctx context.Context, t *entity.AgentUpgradeTask) error
	FindByID(ctx context.Context, id uint64) (*entity.AgentUpgradeTask, error)
	FindPage(ctx context.Context, q *request.AgentUpgradeTaskQuery) ([]entity.AgentUpgradeTask, int64, error)
	CountsByTask(ctx context.Context, taskID uint64) (map[string]int, error)
	MarkFinishedIfSettled(ctx context.Context, taskID uint64, at time.Time) (bool, error)
}

// UpgradeReleaseRepository 是发布物查询面（对账解析产物 / 下发前判断影响面）。
//
// 写面（上传 / 发布 / 删除）在 AgentReleaseService，不在这里：本服务是**升级编排**，
// 它只需要读产物元数据。
type UpgradeReleaseRepository interface {
	FindByPlatform(ctx context.Context, version, os, arch string, onlyPublished bool) (*entity.AgentRelease, error)
	ListByVersion(ctx context.Context, version string) ([]entity.AgentRelease, error)
}

const (
	// ConfigTargetVersion 是**全站目标版本**（空 = 关闭全站升级）。
	//
	// 默认空是安全相关的：一旦有值，所有设备（含之后新注册的）都会自动对齐到它。
	ConfigTargetVersion = "sys.agent.targetVersion"
	// ConfigReleaseMaxSize 是发布物上传上限（与文件模块的上限分开，见 v013 迁移注释）。
	ConfigReleaseMaxSize = "sys.agent.release.maxSize"
	// DefaultReleaseMaxSize 与 v013 种子的默认值一致（由迁移测试按字面量交叉钉住）。
	DefaultReleaseMaxSize = 200 * 1024 * 1024

	// progressMinInterval / progressMinStep 是**下载进度落库的节流阈值**：
	// 阶段跃迁立即写；纯百分比至少间隔 5 秒或跨过 10 个百分点才写一次。
	// 按上报频率逐条写库等于取消节流（一条 25MB 内网下载只有几秒，值当）。
	progressMinInterval = 5 * time.Second
	progressMinStep     = 10
)

// DeviceUpgradeService 是升级域的唯一写入口（设计 §3.1 边界规矩 4）。
//
// 它同时服务两侧：
//   - **agent 通道**（本文件的方法）：握手对账、状态上报落库；
//   - **控制台 HTTP**（同包内其余方法）：下发目标、发布物管理、任务查询。
//
// 两边共用同一条「生效目标」与「一次尝试」的口径，故必须是同一个对象 ——
// 拆成两个服务就会出现两份 ResolveTargetVersion，那正是要避免的分叉。
type DeviceUpgradeService struct {
	devices  UpgradeDeviceRepository
	attempts UpgradeAttemptRepository
	tasks    UpgradeTaskRepository
	releases UpgradeReleaseRepository
	cfg      AgentConfigGetter
	// setter / notifier / actors 都是可选装配：setter 缺省时全站目标不可改（返回错误），
	// notifier 缺省时催办不发生（设备仍会在下次重连对账），actors 缺省时任务里不记发起人
	// （操作日志仍有记录）。三者都不是上报主链路的依赖。
	setter   UpgradeConfigSetter
	notifier UpgradeNotifier
	actors   UpgradeActorResolver
	log      logger.LoggerInterface
}

func NewDeviceUpgradeService(devices UpgradeDeviceRepository, attempts UpgradeAttemptRepository,
	tasks UpgradeTaskRepository, releases UpgradeReleaseRepository, cfg AgentConfigGetter,
	setter UpgradeConfigSetter, notifier UpgradeNotifier, actors UpgradeActorResolver,
	log logger.LoggerInterface) *DeviceUpgradeService {
	return &DeviceUpgradeService{devices: devices, attempts: attempts, tasks: tasks,
		releases: releases, cfg: cfg, setter: setter, notifier: notifier, actors: actors, log: log}
}

// ResolveTargetVersion 返回设备的**生效目标版本**（空串 = 无目标）。
//
//	effectiveTarget(device) = device.TargetAgentVersion ?? sys.agent.targetVersion
//
// **读时计算、不落库**，并且是全站唯一实现（hello_ack 指令解析、列表展示、
// 汇总统计都走这里）—— 两处各写一份必然在「全站目标改了、设备级还是旧的」这类
// 边界上分叉，而那种分叉在页面上表现为「有的设备升有的不升」这种无从解释的现象。
//
// 语义三态：设备级有值 = 设备指定（覆盖全站）；设备级空 + 全站有值 = 跟随全站；
// 两者都空 = 无目标。
func (s *DeviceUpgradeService) ResolveTargetVersion(ctx context.Context, dev *entity.Device) string {
	if dev == nil {
		return ""
	}
	if dev.TargetAgentVersion != "" {
		return dev.TargetAgentVersion
	}
	return s.cfg.GetString(ctx, ConfigTargetVersion, "")
}

// ── agent 通道：对账与上报 ────────────────────────────────────────────

// ReconcileOnHello 是握手对账（实现 agenthub.UpgradeCoordinator）。
//
// 两件事，顺序不能换：
//  1. **归位**：把上一次尝试按设备现在报回的版本判终态（成功 / 已回滚 / 意外版本）。
//     必须在解析指令之前 —— 否则「升级完重连」会被当成「还在升」，指令又下一遍。
//  2. **解析指令**：无目标、或目标等于当前版本、或该平台没有已发布产物 → 返回 nil。
//
// 幂等：重复调用只会重复得出同一个结论（归位后没有未终结行就不再判）。
func (s *DeviceUpgradeService) ReconcileOnHello(ctx context.Context, deviceID uint64,
	h *agentproto.Hello) (*agentproto.UpgradeDirective, error) {
	dev, err := s.devices.FindByID(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	now := time.Now()

	if open, err := s.attempts.FindOpenByDevice(ctx, deviceID); err == nil && open != nil {
		s.settleAttemptOnHello(ctx, dev, open, h.AgentVersion, now)
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	target := s.ResolveTargetVersion(ctx, dev)
	if target == "" || target == h.AgentVersion {
		// 无目标（或目标就是当前版本 = 固定/已达成）：不下发指令。
		return nil, nil
	}

	// **先确认产物存在，再决定要不要开尝试行**。
	//
	// 顺序不能反：若先建行再查产物，「有目标但该平台没产物」的设备会留下一条
	// 永远没开工的 pending 行 —— 页面据此把它显示成「升级中」，而真相是
	// 「没有可升级的程序文件」，两者对运维是完全不同的事（一个是等着，一个是缺失）。
	rel, err := s.releases.FindByPlatform(ctx, target, dev.OS, dev.Arch, true)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// 不是故障，但必须**可解释**：设备会一直不升，控制台汇总把这类计入「无产物」。
			s.log.Info("agent upgrade skipped: no published artifact for platform",
				zap.Uint64("device_id", deviceID), zap.String("version", target),
				zap.String("os", dev.OS), zap.String("arch", dev.Arch))
			return nil, nil
		}
		return nil, err
	}

	// 复用未终结尝试（D1）：**重连对账不算新尝试**，否则一次网络闪断就会在任务里
	// 多出一行。只有在没有未终结行时才兜底开行 —— 覆盖「设备首次上线时全站目标
	// 已经生效」这类下发时并不存在的设备（新注册设备自动对齐）。
	open, err := s.attempts.FindOpenByDevice(ctx, deviceID)
	if errors.Is(err, repository.ErrNotFound) {
		// **不自愈式重试**：最近一次尝试若已因失败/回滚/超时终结，且目标仍是同一个
		// 版本，就不能在这里自动再开一行 —— 否则「升不上去 → 回滚 → 再升」会变成
		// 每几分钟一轮的无限循环（设备侧的 10 分钟退避只是第二道防线，不该指望它）。
		// 重新下发（操作员点重试 / 换目标）走的是「终结旧行 + 开新行」的正规路径。
		if last := s.latestAttempt(ctx, deviceID); suppressesAutoRetry(last, target) {
			s.log.Info("agent upgrade not re-issued: previous attempt failed for same target",
				zap.Uint64("device_id", deviceID), zap.String("target", target),
				zap.String("last_state", last.State), zap.String("last_reason", last.ReasonCode))
			return nil, nil
		}
		open = &entity.AgentUpgradeAttempt{
			DeviceID:    deviceID,
			FromVersion: h.AgentVersion,
			ToVersion:   target,
			RequestID:   newRequestID(),
			State:       entity.AttemptStatePending,
			CreatedAt:   now,
		}
		if err := s.attempts.Create(ctx, open); err != nil {
			return nil, err
		}
		s.log.Info("agent upgrade attempt opened on hello",
			zap.Uint64("device_id", deviceID), zap.String("to", target))
	} else if err != nil {
		return nil, err
	}

	return &agentproto.UpgradeDirective{
		// 指令里带的必须**就是行上的那个值**：状态上报按 (device_id, request_id)
		// 归属，若指令改用别的值（例如 attempt.id），上报就找不到行、被当作陈旧丢弃
		// —— 现象是「设备明明在升级，页面上什么都不动」，而且没有任何报错。
		RequestID:     open.RequestID,
		TargetVersion: open.ToVersion,
		SHA256:        rel.SHA256,
		SizeBytes:     rel.SizeBytes,
	}, nil
}

// settleAttemptOnHello 按设备报回的版本给未终结尝试判终态。
//
// 判定顺序有讲究（`!started` 必须在 from 比较之前）：
//
//	① 版本 == 目标        → 成功（唯一裁决点，见设计 §4）
//	② 还没开始（pending） → 什么都不做：设备刚上线、版本仍是原版本，这是正常过程
//	③ 版本 == 原版本      → **本地自动回滚**（它起来过、又退回去了）
//	④ 其余                → 版本既非目标也非原版本（有人手工换过二进制）
//
// ② 若放在 ③ 之后，一台「首次注册时 agent_version 为空」的设备在 pending 阶段
// 就会被误判成回滚。
func (s *DeviceUpgradeService) settleAttemptOnHello(ctx context.Context, dev *entity.Device,
	open *entity.AgentUpgradeAttempt, agentVersion string, now time.Time) {
	switch {
	case agentVersion == open.ToVersion:
		s.finishAttempt(ctx, dev, entity.AttemptStateSucceeded, "", now)
	case !attemptStarted(open):
		// 等待设备开工：保持未终结，指令解析会复用这一行。
	case agentVersion == open.FromVersion:
		s.finishAttempt(ctx, dev, entity.AttemptStateRolledBack,
			agentproto.ReasonNotConnectedAfterUpgrade, now)
	default:
		s.finishAttempt(ctx, dev, entity.AttemptStateFailed,
			entity.AttemptReasonUnexpectedVersion, now)
	}
}

// finishAttempt 终结一次尝试，并把结论同步到设备行的终态字段。
//
// 失败只记日志不上抛：这是握手路径上的辅助动作，升级域的写失败不该让设备连不上。
func (s *DeviceUpgradeService) finishAttempt(ctx context.Context, dev *entity.Device,
	state, reasonCode string, now time.Time) {
	// 先取一次未终结行（为的是它的 TaskID，供下面收口任务用）。
	var taskID *uint64
	if open, err := s.attempts.FindOpenByDevice(ctx, dev.ID); err == nil && open != nil {
		taskID = open.TaskID
	}
	n, err := s.attempts.FinishOpen(ctx, dev.ID, state, reasonCode, now)
	if err != nil {
		s.log.Warn("agent upgrade attempt finish failed",
			zap.Uint64("device_id", dev.ID), zap.String("state", state), zap.Error(err))
		return
	}
	if n == 0 {
		return // 已被并发路径终结（巡检/新下发），不重复记
	}
	s.log.Info("agent upgrade attempt settled",
		zap.Uint64("device_id", dev.ID), zap.String("state", state), zap.String("reason", reasonCode))
	s.syncDeviceTerminal(ctx, dev.ID, state, reasonCode, now)
	s.settleTask(ctx, taskID, now)
	// 任务收口在**两条终结路径**上都要做（状态上报 / hello 归位）：只做一条会让
	// 「由 hello 裁定的成功」永远收不了口（任务列表一直显示未完成）——
	// 端到端验收实测到了这个不一致。
}

// syncDeviceTerminal 把终态写进设备行（只有失败与回滚需要落库；
// 「已达成」也写一份，用于回答「上次成功升级是什么时候」）。
func (s *DeviceUpgradeService) syncDeviceTerminal(ctx context.Context, deviceID uint64,
	state, reasonCode string, now time.Time) {
	var devState int8
	switch state {
	case entity.AttemptStateSucceeded:
		devState = entity.DeviceUpgradeAchieved
	case entity.AttemptStateRolledBack:
		devState = entity.DeviceUpgradeRolledBack
	case entity.AttemptStateFailed, entity.AttemptStateTimeout:
		devState = entity.DeviceUpgradeFailed
	default:
		return // 中间态不落库（device 行只存终态）
	}
	if err := s.devices.SetUpgradeTerminal(ctx, deviceID, devState, reasonCode, now); err != nil {
		s.log.Warn("device upgrade terminal sync failed",
			zap.Uint64("device_id", deviceID), zap.Error(err))
	}
}

// ReportUpgradeStatus 落一次状态上报（实现 agenthub.UpgradeCoordinator）。
//
// 四条纪律：
//   - 命中不到未终结行 → **丢弃**（陈旧/伪造上报没有可归属的尝试）且不报错；
//   - 阶段跃迁立即落库；纯进度按节流（5 秒 / 10 个百分点）；
//   - `last_report_at` 随落库一起写，不单独为非写窗口更新 —— 为精度毫秒的时间戳
//     每次写库等于取消节流，而「最近上报」精确到 5 秒对页面与巡检都够用；
//   - 终态（failed / rolled_back）同步设备行终态并尝试收口任务。
func (s *DeviceUpgradeService) ReportUpgradeStatus(ctx context.Context, deviceID uint64,
	st *agentproto.UpgradeStatus) error {
	a, err := s.attempts.FindByRequestID(ctx, deviceID, st.RequestID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.log.Info("agent upgrade status dropped: no matching attempt",
				zap.Uint64("device_id", deviceID), zap.String("request_id", st.RequestID))
			return nil
		}
		return err
	}
	if entity.IsAttemptTerminal(a.State) {
		s.log.Info("agent upgrade status dropped: attempt already finished",
			zap.Uint64("device_id", deviceID), zap.String("state", a.State))
		return nil
	}
	if a.ToVersion != st.TargetVersion {
		// 口令对不上的上报说明设备在按另一次下发执行（例如它还没收到新的目标）。
		// 落库会让页面出现「目标 A、进度来自 B」的错位，故丢弃并记录。
		s.log.Warn("agent upgrade status dropped: target mismatch",
			zap.Uint64("device_id", deviceID),
			zap.String("attempt_to", a.ToVersion), zap.String("reported_to", st.TargetVersion))
		return nil
	}

	now := time.Now()
	if a.StartedAt == nil && st.State != entity.AttemptStatePending {
		a.StartedAt = &now
	}
	if !s.shouldPersist(a, st) {
		return nil
	}
	a.State = st.State
	a.Progress = st.Progress
	a.ReasonCode = st.ReasonCode
	a.ReasonDetail = agentproto.NormalizeReasonDetail(st.ReasonDetail)
	a.LastReportAt = &now
	if entity.IsAttemptTerminal(st.State) {
		a.FinishedAt = &now
	}
	if err := s.attempts.UpdateReport(ctx, a); err != nil {
		return err
	}
	if entity.IsAttemptTerminal(st.State) {
		s.log.Info("agent upgrade reported terminal state",
			zap.Uint64("device_id", deviceID), zap.String("state", st.State),
			zap.String("reason", st.ReasonCode))
		s.syncDeviceTerminal(ctx, deviceID, st.State, st.ReasonCode, now)
		s.settleTask(ctx, a.TaskID, now)
	}
	return nil
}

// shouldPersist 判断这条上报值不值得写库（节流）。
func (s *DeviceUpgradeService) shouldPersist(a *entity.AgentUpgradeAttempt,
	st *agentproto.UpgradeStatus) bool {
	if a.State != st.State || a.ReasonCode != st.ReasonCode {
		return true // 阶段跃迁 / 原因变化：立即写
	}
	if st.Progress == nil {
		// 同一阶段且无百分比（verifying/installing/restarting 各只发一次）：
		// 重复上报没有新信息，不值得写。
		return false
	}
	if a.LastReportAt == nil {
		return true
	}
	if time.Since(*a.LastReportAt) >= progressMinInterval {
		return true
	}
	prev := 0
	if a.Progress != nil {
		prev = *a.Progress
	}
	return abs(*st.Progress-prev) >= progressMinStep
}

// settleTask 尝试给任务收口（无进行中且无等待时写 finished_at）。
func (s *DeviceUpgradeService) settleTask(ctx context.Context, taskID *uint64, now time.Time) {
	if taskID == nil {
		return // 本地自愈回滚不属于任何任务
	}
	done, err := s.tasks.MarkFinishedIfSettled(ctx, *taskID, now)
	if err != nil {
		s.log.Warn("upgrade task settle failed", zap.Uint64("task_id", *taskID), zap.Error(err))
		return
	}
	if done {
		s.log.Info("upgrade task settled", zap.Uint64("task_id", *taskID))
	}
}

// attemptStarted 报告一次尝试是否已经**开工**（收到过任何非 pending 的状态）。
func attemptStarted(a *entity.AgentUpgradeAttempt) bool {
	return a.StartedAt != nil || a.State != entity.AttemptStatePending
}

// latestAttempt 取该设备最近一条尝试；取不到返回 nil（调用方按「没有历史」处理）。
func (s *DeviceUpgradeService) latestAttempt(ctx context.Context,
	deviceID uint64) *entity.AgentUpgradeAttempt {
	rows, err := s.attempts.ListByDevice(ctx, deviceID, 1)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// suppressesAutoRetry 判定「最近一次尝试」是否应当抑制对同一目标的自动重开。
//
// 抑制的条件（三者同时成立）：目标是同一个版本、尝试已终结、且**不是成功**。
// 成功无需抑制（成功时目标等于当前版本，压根走不到这里）。
//
// 这是防「升级失败 → 回滚 → 自动再升」死循环的关键：设备侧虽有 10 分钟退避，
// 但那只是把循环周期拉长；真正的开关必须在服务端 —— 失败之后要不要再试，
// 是**人的决定**（控制台的重试按钮），而不是系统的默认行为。
func suppressesAutoRetry(last *entity.AgentUpgradeAttempt, target string) bool {
	if last == nil || last.ToVersion != target {
		return false
	}
	if !entity.IsAttemptTerminal(last.State) {
		return false
	}
	return last.State != entity.AttemptStateSucceeded
}

// newRequestID 生成一次尝试的 request_id。
//
// 形态必须是**十进制串**（协议 isDecimalID 的硬要求，为的是让 JS 侧不丢精度）。
// 取值是「纳秒时间戳 + 进程内自增序号」—— 不追求可读，只要唯一：
//   - 进程内：自增序号保证绝不重复（两台设备在同一纳秒被下发也不冲突）；
//   - 跨进程：多实例是已声明的限制，且 (device_id, request_id) 唯一索引兜底。
//
// 为什么不用 attempt.id：ID 由 GORM 雪花回调在 Create 时生成，而 request_id 是
// not null 列，必须在插入前就有值；「插入后再回填」要两次写库，而 request_id 的
// 全部用途只是「把上报归属到某一行尝试」。
func newRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) +
		strconv.FormatUint(requestIDSeq.Add(1), 10)
}

var requestIDSeq atomic.Uint64

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// boolToInt8 把布尔翻成 0/1（与 migrations 包的同名辅助同义，包内私有）。
func boolToInt8(b bool) int8 {
	if b {
		return 1
	}
	return 0
}
