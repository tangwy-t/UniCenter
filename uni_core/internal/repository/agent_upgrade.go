package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// AgentUpgradeTaskRepo 是升级任务表的数据访问层。
//
// 计数**不落库**：CountsByTask 读时聚合（明细只有 10~100 行），
// 而增量计数是最容易跟明细对不上的代码。
type AgentUpgradeTaskRepo struct {
	db *gorm.DB
}

func NewAgentUpgradeTaskRepository(db *gorm.DB) *AgentUpgradeTaskRepo {
	return &AgentUpgradeTaskRepo{db: db}
}

func (r *AgentUpgradeTaskRepo) Create(ctx context.Context, t *entity.AgentUpgradeTask) error {
	return r.db.WithContext(ctx).Create(t).Error
}

func (r *AgentUpgradeTaskRepo) FindByID(ctx context.Context, id uint64) (*entity.AgentUpgradeTask, error) {
	var t entity.AgentUpgradeTask
	if err := r.db.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &t, nil
}

// FindPage 任务列表（filter 的 Running 用 EXISTS 子查询表达，
// 避免 join 明细导致的分页行数失真）。
func (r *AgentUpgradeTaskRepo) FindPage(ctx context.Context, q *request.AgentUpgradeTaskQuery) ([]entity.AgentUpgradeTask, int64, error) {
	apply := func(db *gorm.DB) *gorm.DB {
		if q.TargetVersion != "" {
			db = db.Where("target_version = ?", q.TargetVersion)
		}
		if q.Source != "" {
			db = db.Where("source = ?", q.Source)
		}
		if q.Running != nil {
			exists := "EXISTS (SELECT 1 FROM " + entity.TableNameAgentUpgradeAttempt +
				" a WHERE a.task_id = " + entity.TableNameAgentUpgradeTask + ".id AND a.state NOT IN ?)"
			args := []any{entity.AttemptTerminalStates()}
			if *q.Running {
				db = db.Where(exists, args...)
			} else {
				db = db.Where("NOT "+exists, args...)
			}
		}
		return db
	}
	countDB := apply(r.db.WithContext(ctx).Model(&entity.AgentUpgradeTask{}))
	dataDB := apply(r.db.WithContext(ctx).Model(&entity.AgentUpgradeTask{})).
		Order("created_at DESC, id DESC")
	return paginate[entity.AgentUpgradeTask](countDB, dataDB, q)
}

// CountsByTask 返回该任务明细的状态分布（读时聚合）。
//
// 返回 map[state]count，**包含零值状态吗？不含** —— 调用方按缺省为零处理；
// 缺省为零比补齐十个状态更不容易出错（后者要求两处清单永远同源）。
func (r *AgentUpgradeTaskRepo) CountsByTask(ctx context.Context, taskID uint64) (map[string]int, error) {
	type row struct {
		State string
		N     int
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}).
		Select("state, COUNT(*) AS n").
		Where("task_id = ?", taskID).
		Group("state").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, x := range rows {
		out[x.State] = x.N
	}
	return out, nil
}

// MarkFinishedIfSettled 在「无进行中且无等待」时写 finished_at（只在首次写入）。
//
// 为什么不做自动超时关闭：声明式目标对离线设备仍然生效 —— 一台离线设备三天后
// 上线照样会升上去，此时任务显示「未完成」是**事实**而不是卡住。
//
// 返回是否发生了写入，供调用方决定是否记日志。
func (r *AgentUpgradeTaskRepo) MarkFinishedIfSettled(ctx context.Context, taskID uint64, at time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Model(&entity.AgentUpgradeTask{}).
		Where("id = ? AND finished_at IS NULL", taskID).
		Where("NOT EXISTS (SELECT 1 FROM "+entity.TableNameAgentUpgradeAttempt+
			" a WHERE a.task_id = ? AND a.state NOT IN ?)", taskID, entity.AttemptTerminalStates()).
		Update("finished_at", at)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// AgentUpgradeAttemptRepo 是升级尝试明细的数据访问层。
//
// 它承载 D1 的两条不变式：
//   - 「一台设备同时至多一条未终结尝试」（SupersedeOpen / FindOpenByDevice 配对使用）；
//   - 「(device_id, request_id) 唯一」（DB 唯一索引兜底防重放）。
type AgentUpgradeAttemptRepo struct {
	db *gorm.DB
}

func NewAgentUpgradeAttemptRepository(db *gorm.DB) *AgentUpgradeAttemptRepo {
	return &AgentUpgradeAttemptRepo{db: db}
}

func (r *AgentUpgradeAttemptRepo) Create(ctx context.Context, a *entity.AgentUpgradeAttempt) error {
	return r.db.WithContext(ctx).Create(a).Error
}

// FindByRequestID 按 (设备, request_id) 取尝试；未命中返回 ErrNotFound。
func (r *AgentUpgradeAttemptRepo) FindByRequestID(ctx context.Context, deviceID uint64,
	requestID string) (*entity.AgentUpgradeAttempt, error) {
	var a entity.AgentUpgradeAttempt
	if err := r.db.WithContext(ctx).
		Where("device_id = ? AND request_id = ?", deviceID, requestID).
		First(&a).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &a, nil
}

// FindOpenByDevice 取该设备**唯一**的未终结尝试；没有则 ErrNotFound。
//
// 这是 D1 的读侧：hello_ack 下发指令与催办都复用它 —— 重连对账不算新尝试。
func (r *AgentUpgradeAttemptRepo) FindOpenByDevice(ctx context.Context,
	deviceID uint64) (*entity.AgentUpgradeAttempt, error) {
	var a entity.AgentUpgradeAttempt
	if err := r.db.WithContext(ctx).
		Where("device_id = ? AND state NOT IN ?", deviceID, entity.AttemptTerminalStates()).
		Order("id DESC").First(&a).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &a, nil
}

// FindOpenByDevices 批量取未终结尝试（设备列表页推导「升级中」用一次 IN 查询，
// 避免逐台 N+1）。
func (r *AgentUpgradeAttemptRepo) FindOpenByDevices(ctx context.Context,
	deviceIDs []uint64) (map[uint64]*entity.AgentUpgradeAttempt, error) {
	out := make(map[uint64]*entity.AgentUpgradeAttempt)
	if len(deviceIDs) == 0 {
		return out, nil
	}
	var rows []entity.AgentUpgradeAttempt
	if err := r.db.WithContext(ctx).
		Where("device_id IN ? AND state NOT IN ?", deviceIDs, entity.AttemptTerminalStates()).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		if _, dup := out[rows[i].DeviceID]; !dup { // 同一设备多行时取最新一条（不该发生，防御性）
			row := rows[i]
			out[row.DeviceID] = &row
		}
	}
	return out, nil
}

// SupersedeOpen 把该设备的未终结尝试置为 superseded（**重新下发的第一步**）。
//
// 必须在开新行**之前**调用：一台设备同时至多一条未终结。
// 返回被终结的行数（0 表示本来就没有未终结行，是正常的首次下发）。
func (r *AgentUpgradeAttemptRepo) SupersedeOpen(ctx context.Context, deviceID uint64, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}).
		Where("device_id = ? AND state NOT IN ?", deviceID, entity.AttemptTerminalStates()).
		Updates(map[string]any{
			"state":       entity.AttemptStateSuperseded,
			"reason_code": entity.AttemptReasonSuperseded,
			"finished_at": at,
		})
	return res.RowsAffected, res.Error
}

// UpdateReport 落一次状态上报（按 request_id 命中行）。
//
// 用 map 形式**无条件写 progress**：从 downloading 进入 verifying 时进度必须被清空
// （协议层已保证 verifying 不带 progress，这里负责把库里的旧值抹掉）——
// 留着 62% 会让页面显示「校验中 62%」这种不存在的状态。
func (r *AgentUpgradeAttemptRepo) UpdateReport(ctx context.Context, a *entity.AgentUpgradeAttempt) error {
	res := r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}).
		Where("id = ?", a.ID).
		Updates(map[string]any{
			"state":          a.State,
			"progress":       a.Progress,
			"reason_code":    a.ReasonCode,
			"reason_detail":  a.ReasonDetail,
			"last_report_at": a.LastReportAt,
			"started_at":     a.StartedAt,
			"finished_at":    a.FinishedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishOpen 按设备终结其未终结尝试（hello 归位：版本与目标比对后写终态）。
// 返回被终结的行数。
func (r *AgentUpgradeAttemptRepo) FinishOpen(ctx context.Context, deviceID uint64,
	state, reasonCode string, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}).
		Where("device_id = ? AND state NOT IN ?", deviceID, entity.AttemptTerminalStates()).
		Updates(map[string]any{
			"state":       state,
			"reason_code": reasonCode,
			"progress":    nil,
			"finished_at": at,
		})
	return res.RowsAffected, res.Error
}

// FindPageByTask 任务明细分页；filter 取 AttemptFilterActive / AttemptFilterFailed。
//
// 服务的顺序：先按 (started_at IS NULL) 把「等待设备上线」的行排后面？——不排。
// 明细顺序固定为 id ASC（= 下发顺序），因为运维读这张表是在问「这台机器到哪一步了」，
// 顺序稳定比「活跃的浮上来」更重要（后者会让正在变化的行在轮询中跳动位置）。
func (r *AgentUpgradeAttemptRepo) FindPageByTask(ctx context.Context, taskID uint64,
	q *request.AgentUpgradeAttemptQuery) ([]entity.AgentUpgradeAttempt, int64, error) {
	apply := func(db *gorm.DB) *gorm.DB {
		db = db.Where("task_id = ?", taskID)
		switch q.Filter {
		case request.AttemptFilterActive:
			db = db.Where("state NOT IN ?", entity.AttemptTerminalStates())
		case request.AttemptFilterFailed:
			db = db.Where("state IN ?", []string{
				entity.AttemptStateFailed, entity.AttemptStateRolledBack, entity.AttemptStateTimeout,
			})
		}
		if q.DeviceID > 0 {
			db = db.Where("device_id = ?", q.DeviceID)
		}
		return db
	}
	countDB := apply(r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}))
	dataDB := apply(r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{})).Order("id ASC")
	return paginate[entity.AgentUpgradeAttempt](countDB, dataDB, q)
}

// ListByDevice 该设备最近的升级记录（详情页「升级记录」）。
func (r *AgentUpgradeAttemptRepo) ListByDevice(ctx context.Context, deviceID uint64,
	limit int) ([]entity.AgentUpgradeAttempt, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []entity.AgentUpgradeAttempt
	if err := r.db.WithContext(ctx).
		Where("device_id = ?", deviceID).
		Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// VersionWasUsed 回滚余量守卫：该版本是否在任何尝试记录里出现过。
//
// 判断覆盖 to_version 与 from_version 两侧：一台设备从 0.1.0 升到 0.2.0 之后，
// 0.1.0 只出现在 from_version 里 —— 而它恰恰是最可能需要回滚回去的那个版本。
func (r *AgentUpgradeAttemptRepo) VersionWasUsed(ctx context.Context, version string) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&entity.AgentUpgradeAttempt{}).
		Where("to_version = ? OR from_version = ?", version, version).
		Limit(1).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindLastSucceededFrom 返回该设备**最近一次成功升级的起始版本** —— 也就是
// 「一键回滚」要回退到的目标。
//
// 由服务端推导而不是让运维回忆「这台机器升级前是什么版本」：升级出问题时人在
// 着急，把系统本该记住的事实推给人，就是设计缺陷。
// 没有成功历史（例如引导安装的机器）时返回空串与 nil（这不是错误）。
func (r *AgentUpgradeAttemptRepo) FindLastSucceededFrom(ctx context.Context,
	deviceID uint64) (string, error) {
	var row entity.AgentUpgradeAttempt
	err := r.db.WithContext(ctx).
		Where("device_id = ? AND state = ?", deviceID, entity.AttemptStateSucceeded).
		Order("id DESC").First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return row.FromVersion, nil
}

// FindStale 巡检：**已开始**但超过阈值没动静的未终结尝试。
//
// 「没动静」以 COALESCE(last_report_at, started_at) 衡量：还没收到过任何上报的行
// 从 started_at 起算（若 started_at 也为空 —— 设备从未上线 —— 则从 created_at 起算）。
// 只看已开始的行是刻意的：**等待设备上线**不属于「卡住」，不该被巡检判死。
func (r *AgentUpgradeAttemptRepo) FindStale(ctx context.Context, before time.Time,
	limit int) ([]entity.AgentUpgradeAttempt, error) {
	var rows []entity.AgentUpgradeAttempt
	if err := r.db.WithContext(ctx).
		Where("state NOT IN ?", entity.AttemptTerminalStates()).
		Where("COALESCE(last_report_at, started_at) IS NOT NULL").
		Where("COALESCE(last_report_at, started_at) < ?", before).
		Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
