package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DockerTaskHistoryRepo 是任务中心历史的存储面（8d，表 docker_task_history）。
//
// 写路径只有一条：CmdStore 终态钩子的 Upsert（service.DockerTaskHistoryRecorder）。
// ref 唯一键让「迟到的 result 覆盖 timeout」「重复 result 重放」都收敛到同一行 ——
// 而 DoUpdates **只更新终态事实列**（status/summary/finished_at），受理时刻与归属
// 列（ref/device_id/action/target/user_id/accepted_at）是受理时的事实，不允许被
// 后续写入改写（重放一条终态而已，不该把「什么时候受理、谁受理的」也重写一遍）。
//
// 读路径是合并读面的一半：FindTop 取「过滤后的前 N 条」（N 由调用方按页偏移 +
// 页大小 + 实时重叠上界算出，见 service.DockerTaskHistoryReader），Count 与
// CountByRefs 供合计口径（合并后总数 = 实时数 + 历史数 − 重叠数）。
//
// 清理面：DeleteBefore 按**受理时刻**删（与列表排序键同一根 —— 保留策略说的
// 「N 天」是「受理超过 N 天的任务」，不是「结束超过 N 天的任务」），软删（与
// 操作日志清理同款：历史表的清理是「不再展示」，硬删留给运维）。
type DockerTaskHistoryRepo struct {
	db *gorm.DB
}

// NewDockerTaskHistoryRepo 构造存储面。
func NewDockerTaskHistoryRepo(db *gorm.DB) *DockerTaskHistoryRepo {
	return &DockerTaskHistoryRepo{db: db}
}

// Upsert 幂等地写入一条终态历史（ref 冲突时只刷新终态事实列）。
func (r *DockerTaskHistoryRepo) Upsert(ctx context.Context, row *entity.DockerTaskHistory) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ref"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"status", "summary", "finished_at", "updated_at",
		}),
	}).Create(row).Error
}

// applyFilters 把任务面的过滤条件翻译成历史表的列条件。
//
// 与实时面的过滤**逐条对齐**（service/docker_tasks.go 的 collectLive），合并读面
// 才能拿同一把尺子量两边：
//   - hostId → device_id；
//   - action → action；
//   - status：done = 一切终态 = 本表的全部行（不加条件）；pending 在本表**恒空**
//     （本表只存终态）—— 这里显式落成「恒假」条件而不是靠调用方记得跳过，调用方
//     将来多一处读路径也不会查出「历史里的进行中任务」这种不存在的东西。
func (r *DockerTaskHistoryRepo) applyFilters(db *gorm.DB, q *request.DockerTasksQuery) *gorm.DB {
	if q == nil {
		return db
	}
	if q.HostID != 0 {
		db = db.Where("device_id = ?", q.HostID)
	}
	if q.Action != "" {
		db = db.Where("action = ?", q.Action)
	}
	if q.Status == "pending" {
		db = db.Where("1 = 0")
	}
	return db
}

// FindTop 取过滤后的前 limit 条历史（受理时刻降序；同毫秒按 ref 降序 —— 与
// 实时面的稳定次序同款：ref 是纳秒级单调串，降序 = 后受理在前）。
//
// 分页不由本方法做：合并读面要「实时 ∪ 历史」整体排序后再切页，故这里只按
// 上界取数（见 service 侧对 need 的推导），切页在内存里发生。
func (r *DockerTaskHistoryRepo) FindTop(ctx context.Context, q *request.DockerTasksQuery, limit int) ([]entity.DockerTaskHistory, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows := make([]entity.DockerTaskHistory, 0, min(limit, 256))
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.DockerTaskHistory{}), q)
	err := db.Order("accepted_at DESC, ref DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// Count 返回过滤后的历史总条数（合并读面合计口径的一半）。
func (r *DockerTaskHistoryRepo) Count(ctx context.Context, q *request.DockerTasksQuery) (int64, error) {
	var total int64
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.DockerTaskHistory{}), q)
	err := db.Count(&total).Error
	return total, err
}

// CountByRefs 返回这些 ref 在本表中的行数（合并去重的重叠数）。
//
// 不加过滤条件的原因：调用方传进来的 ref 来自**已过滤**的实时条目，而两边的
// 归属/动作/状态列出自同一条指令的同一份记录（同一时刻写入），历史行必然同样
// 命中过滤 —— 再过滤一遍只会多一次无用比较。ref 数量被实时枚举窗口封顶
// （CmdStore.RecentCmdKeep，一次查询里最多 300 个字面量）。
func (r *DockerTaskHistoryRepo) CountByRefs(ctx context.Context, refs []string) (int64, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	var total int64
	err := r.db.WithContext(ctx).Model(&entity.DockerTaskHistory{}).
		Where("ref IN ?", refs).Count(&total).Error
	return total, err
}

// DeleteBefore 删除受理时刻早于 before 的历史行（保留策略清理，返回删除行数）。
func (r *DockerTaskHistoryRepo) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("accepted_at < ?", before.UnixMilli()).
		Delete(&entity.DockerTaskHistory{})
	return res.RowsAffected, res.Error
}
