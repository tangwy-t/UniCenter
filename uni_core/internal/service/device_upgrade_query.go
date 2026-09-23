package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 查询面：任务 / 明细 / 设备升级记录 / 列表所需的快照 ──────────────────

// DeviceUpgradeSnapshot 是设备列表与详情展示所需的升级信息（**服务层结构，不是线上 DTO**）。
//
// 为什么放在服务层而不是直接塞进响应 DTO：它是「设备服务向升级域要几个数」的
// 内部约定，把它做成线上 DTO 会让接口形状被两个模块的内部协作牵着走。
type DeviceUpgradeSnapshot struct {
	// EffectiveTargetVersion 空 = 无目标。
	EffectiveTargetVersion string
	// TargetFromGlobal 表示生效目标来自全站（设备级为空而全站有值）。
	TargetFromGlobal bool
	// OpenAttemptState 是未终结尝试的当前状态（空 = 没有未终结尝试）。
	//
	// 刻意给**状态**而不是一个 bool：`pending`（下发时插入、设备还没开工）
	// 与真的在跑（downloading…）在页面上是不同的东西 —— 前者是「等待」，
	// 后者是「升级中」。给 bool 就必然把两者混成一种。
	OpenAttemptState string
	// Unsupported 是 agent 自报位（0 = 该版本不支持远程升级）。
	Unsupported bool
	// TerminalState / TerminalReason / TerminalAt 是最近一次终态（无/已达成/失败/已回滚）。
	TerminalState  int8
	TerminalReason string
	TerminalAt     *time.Time
}

// Running 报告这台设备是否真的在升级（有未终结尝试且已开工）。
func (s DeviceUpgradeSnapshot) Running() bool {
	return s.OpenAttemptState != "" && s.OpenAttemptState != entity.AttemptStatePending
}

// ResolveTargetVersion 见同名导出的方法说明（此处是接口视图）。
func (s *DeviceUpgradeService) Snapshot(ctx context.Context,
	devices []entity.Device) (map[uint64]DeviceUpgradeSnapshot, error) {
	out := make(map[uint64]DeviceUpgradeSnapshot, len(devices))
	if len(devices) == 0 {
		return out, nil
	}
	ids := make([]uint64, 0, len(devices))
	for i := range devices {
		ids = append(ids, devices[i].ID)
	}
	// 一次批量查询换掉逐台 N+1（列表页每行都要问「是不是升级中」）。
	open, err := s.attempts.FindOpenByDevices(ctx, ids)
	if err != nil {
		return nil, err
	}
	global := s.cfg.GetString(ctx, ConfigTargetVersion, "")
	for i := range devices {
		dev := devices[i]
		target := dev.TargetAgentVersion
		fromGlobal := false
		if target == "" && global != "" {
			target, fromGlobal = global, true
		}
		state := ""
		if a := open[dev.ID]; a != nil {
			state = a.State
		}
		out[dev.ID] = DeviceUpgradeSnapshot{
			EffectiveTargetVersion: target,
			TargetFromGlobal:       fromGlobal,
			OpenAttemptState:       state,
			Unsupported:            dev.AgentUpgradeSupported != 1,
			TerminalState:          dev.AgentUpgradeState,
			TerminalReason:         dev.AgentUpgradeReason,
			TerminalAt:             dev.AgentUpgradeAt,
		}
	}
	return out, nil
}

// RollbackVersion 给出「一键回滚」的目标版本（最近一次成功升级的起始版本）。
// 没有成功历史时返回空串（页面据此禁用按钮）。
func (s *DeviceUpgradeService) RollbackVersion(ctx context.Context, deviceID uint64) (string, error) {
	v, err := s.attempts.FindLastSucceededFrom(ctx, deviceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// TaskList 返回任务分页（每行带明细状态分布，读时聚合）。
func (s *DeviceUpgradeService) TaskList(ctx context.Context,
	q *request.AgentUpgradeTaskQuery) (*app.PageResponse, error) {
	if q == nil {
		q = &request.AgentUpgradeTaskQuery{}
	}
	tasks, total, err := s.tasks.FindPage(ctx, q)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	ids := make([]uint64, 0, len(tasks))
	for i := range tasks {
		ids = append(ids, tasks[i].ID)
	}
	// 逐任务 CountsByTask 会是 N+1；一次 GROUP BY (task_id, state) 更省。
	counts, err := s.taskCounts(ctx, ids)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	items := make([]response.AgentUpgradeTaskItem, 0, len(tasks))
	for i := range tasks {
		items = append(items, toTaskItem(&tasks[i], counts[tasks[i].ID]))
	}
	return app.NewPageResponse(items, total, q.GetPage(), q.GetPageSize()), nil
}

// taskCounts 批量取「任务 → 状态分布」。仓储的 CountsByTask 是单个任务的版本，
// 这里对每个任务调用一次会在任务页（一页 10 行）产生 10 次查询 —— 可接受但不体面；
// 真正的大头在明细页的逐行设备名补齐，那里走的是 FindByIDs 一次查询。
func (s *DeviceUpgradeService) taskCounts(ctx context.Context, taskIDs []uint64) (map[uint64]map[string]int, error) {
	out := make(map[uint64]map[string]int, len(taskIDs))
	for _, id := range taskIDs {
		c, err := s.tasks.CountsByTask(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, nil
}

// TaskDetail 返回任务详情（任务 + 明细分页）。
//
// 明细里的设备名与 IP 由**一次 FindByIDs** 补齐；设备已软删时留空并置
// DeviceDeleted=true —— 记录本身要保留（审计），页面显示「设备已删除」而不是过滤掉。
func (s *DeviceUpgradeService) TaskDetail(ctx context.Context, taskID uint64,
	q *request.AgentUpgradeAttemptQuery) (*response.AgentUpgradeTaskDetailResp, error) {
	if q == nil {
		q = &request.AgentUpgradeAttemptQuery{}
	}
	task, err := s.tasks.FindByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("任务不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	rows, total, err := s.attempts.FindPageByTask(ctx, taskID, q)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	counts, err := s.tasks.CountsByTask(ctx, taskID)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	items, err := s.attemptItems(ctx, rows)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	return &response.AgentUpgradeTaskDetailResp{
		Task:     toTaskItem(task, counts),
		List:     items,
		Total:    total,
		Page:     q.GetPage(),
		PageSize: q.GetPageSize(),
	}, nil
}

// DeviceRecords 返回某设备最近的升级记录（详情页用；与任务明细同源）。
func (s *DeviceUpgradeService) DeviceRecords(ctx context.Context, deviceID uint64,
	limit int) ([]response.DeviceUpgradeRecord, error) {
	if _, err := s.devices.FindByID(ctx, deviceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("设备不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	rows, err := s.attempts.ListByDevice(ctx, deviceID, limit)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	out := make([]response.DeviceUpgradeRecord, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, response.DeviceUpgradeRecord{
			ID:          r.ID,
			FromVersion: r.FromVersion,
			ToVersion:   r.ToVersion,
			State:       r.State,
			ReasonCode:  r.ReasonCode,
			CreatedAt:   r.CreatedAt.Unix(),
			FinishedAt:  unixPtr(r.FinishedAt),
		})
	}
	return out, nil
}

// attemptItems 把明细行补上设备信息。
func (s *DeviceUpgradeService) attemptItems(ctx context.Context,
	rows []entity.AgentUpgradeAttempt) ([]response.AgentUpgradeAttemptItem, error) {
	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].DeviceID)
	}
	devices, err := s.devices.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint64]*entity.Device, len(devices))
	for i := range devices {
		byID[devices[i].ID] = &devices[i]
	}
	out := make([]response.AgentUpgradeAttemptItem, 0, len(rows))
	for i := range rows {
		r := rows[i]
		item := response.AgentUpgradeAttemptItem{
			ID:           r.ID,
			TaskID:       uint64PtrStr(r.TaskID),
			DeviceID:     r.DeviceID,
			FromVersion:  r.FromVersion,
			ToVersion:    r.ToVersion,
			State:        r.State,
			Progress:     r.Progress,
			ReasonCode:   r.ReasonCode,
			CreatedAt:    r.CreatedAt.Unix(),
			StartedAt:    unixPtr(r.StartedAt),
			LastReportAt: unixPtr(r.LastReportAt),
			FinishedAt:   unixPtr(r.FinishedAt),
		}
		if dev := byID[r.DeviceID]; dev != nil {
			item.Hostname = dev.Hostname
			item.PrimaryIP = dev.PrimaryIP
		} else {
			// 设备被软删（或真的不存在）：记录保留，页面据此显示「设备已删除」。
			item.DeviceDeleted = true
		}
		out = append(out, item)
	}
	return out, nil
}

// toTaskItem 组装任务行（含状态分布）。
func toTaskItem(t *entity.AgentUpgradeTask, counts map[string]int) response.AgentUpgradeTaskItem {
	// 「进行中」把四个中间态合并（页面不区分下载/校验/替换/重启的台数，
	// 那是明细行的事；任务行要的是「还有几台没完」）。
	return response.AgentUpgradeTaskItem{
		ID:            t.ID,
		TargetVersion: t.TargetVersion,
		Source:        t.Source,
		Actor:         t.Actor,
		Total:         t.Total,
		Counts: response.AgentUpgradeCounts{
			Pending:    counts[entity.AttemptStatePending],
			Running:    counts[entity.AttemptStateDownloading] + counts[entity.AttemptStateVerifying] + counts[entity.AttemptStateInstalling] + counts[entity.AttemptStateRestarting],
			Succeeded:  counts[entity.AttemptStateSucceeded],
			Failed:     counts[entity.AttemptStateFailed],
			RolledBack: counts[entity.AttemptStateRolledBack],
			Timeout:    counts[entity.AttemptStateTimeout],
			Superseded: counts[entity.AttemptStateSuperseded],
		},
		CreatedAt:  t.CreatedAt.Unix(),
		FinishedAt: unixPtr(t.FinishedAt),
	}
}

// unixPtr 把 *time.Time 转成 *int64（unix 秒）；nil 保持 nil。
func unixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

// uint64PtrStr 把 *uint64 转成十进制字符串（空指针 → 空串）。
func uint64PtrStr(v *uint64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatUint(*v, 10)
}
