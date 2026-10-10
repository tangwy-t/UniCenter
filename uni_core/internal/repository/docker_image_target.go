package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DockerImageTargetRepo 是镜像目标版本声明的存储面（Wave 4c，表 docker_image_target）。
//
// 四条路径与声明的语义一一对应：
//   - Upsert：同 repoTag 覆盖（repo_tag 唯一键 + OnConflict 只刷值列），是**声明**；
//   - Delete：**硬删且幂等** —— 清除一个本来就没设的目标是成功（与「取消一个没设过的
//     目标」语义一致），故 **0 行不返回 ErrRecordNotFound**（与凭据删除的 404 语义刻意分家：
//     那个要向用户报「没有这条记录」，这个只是取消一个声明）；
//   - List：目标清单（按 repo_tag 升序，确定性次序）；
//   - FindByRepoTags：**批量**取多个 repoTag 的目标（漂移聚合一次性取全部，避免 N+1）。
type DockerImageTargetRepo struct {
	db *gorm.DB
}

// NewDockerImageTargetRepo 构造存储面。
func NewDockerImageTargetRepo(db *gorm.DB) *DockerImageTargetRepo {
	return &DockerImageTargetRepo{db: db}
}

// Upsert 声明/覆盖一个 repoTag 的目标（repo_tag 冲突时只刷目标值与更新时间）。
//
// DoUpdates 只列 target_digest / updated_at / updated_by：
//   - created_at / created_by 是**首次声明**的事实，重设目标不该把它们改写成
//     「上一次声明时刻」（updated_at 才是「这次改动」的时刻）；
//   - updated_by 取 excluded 值 —— 由审计回调在 Create 前从上下文填入行结构体，
//     冲突更新时随 excluded 生效；无登录用户（系统路径）则保持 NULL（不臆造）。
func (r *DockerImageTargetRepo) Upsert(ctx context.Context, row *entity.DockerImageTarget) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_tag"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"target_digest", "updated_at", "updated_by",
		}),
	}).Create(row).Error
}

// Delete 按 repoTag 硬删一条目标（幂等：命中 0 行也是成功）。
//
// 不返回 ErrRecordNotFound：清除一个本来就没设的目标等价于「取消一个没设过的
// 目标」，应当成功 —— 与凭据删除（删不存在的凭据 → 404）是两种语义，刻意分家。
func (r *DockerImageTargetRepo) Delete(ctx context.Context, repoTag string) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("repo_tag = ?", repoTag).
		Delete(&entity.DockerImageTarget{}).Error
}

// List 返回全部目标（按 repo_tag 升序 —— 确定性次序，不随插入/更新时刻抖动）。
func (r *DockerImageTargetRepo) List(ctx context.Context) ([]entity.DockerImageTarget, error) {
	var rows []entity.DockerImageTarget
	err := r.db.WithContext(ctx).Order("repo_tag ASC").Find(&rows).Error
	return rows, err
}

// FindByRepoTags **批量**取一组 repoTag 的目标，返回 repoTag → 目标行的映射
// （未声明的 tag 不出现）。漂移视图一次调用取全量标签的目标，杜绝逐行查询的 N+1。
//
// 空入参直接返回空映射：不生成 `IN ()`（那是语法错误），也省一次往返。
func (r *DockerImageTargetRepo) FindByRepoTags(ctx context.Context, repoTags []string) (map[string]entity.DockerImageTarget, error) {
	out := map[string]entity.DockerImageTarget{}
	if len(repoTags) == 0 {
		return out, nil
	}
	var rows []entity.DockerImageTarget
	if err := r.db.WithContext(ctx).Where("repo_tag IN ?", repoTags).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].RepoTag] = rows[i]
	}
	return out, nil
}