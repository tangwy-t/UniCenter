package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// AgentReleaseRepo 是 agent 发布物（agent_release）的数据访问层。
//
// 字节不在库里：本表只存 storage.Backend 的 key 与 sha256（设计 §3.1 边界规矩）。
type AgentReleaseRepo struct {
	db *gorm.DB
}

func NewAgentReleaseRepository(db *gorm.DB) *AgentReleaseRepo { return &AgentReleaseRepo{db: db} }

func (r *AgentReleaseRepo) Create(ctx context.Context, rel *entity.AgentRelease) error {
	return r.db.WithContext(ctx).Create(rel).Error
}

// FindByID 按主键查发布物。未命中返回 ErrNotFound 哨兵（语义同 DeviceRepo）。
func (r *AgentReleaseRepo) FindByID(ctx context.Context, id uint64) (*entity.AgentRelease, error) {
	var rel entity.AgentRelease
	if err := r.db.WithContext(ctx).First(&rel, id).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &rel, nil
}

// FindByPlatform 按 (version, os, arch) 取产物；onlyPublished=true 时忽略草稿。
//
// 两个语义要分清：
//   - **选目标 / 下发指令**只认已发布（草稿可能是不完整的上传）→ onlyPublished=true；
//   - **设备下载**不校验 status（撤回只影响新下发，已指向该版本的设备要继续能拉，
//     否则撤回会打断正在进行的升级）→ onlyPublished=false。
func (r *AgentReleaseRepo) FindByPlatform(ctx context.Context, version, os, arch string,
	onlyPublished bool) (*entity.AgentRelease, error) {
	q := r.db.WithContext(ctx).Where("version = ? AND os = ? AND arch = ?", version, os, arch)
	if onlyPublished {
		q = q.Where("status = ?", entity.AgentReleasePublished)
	}
	var rel entity.AgentRelease
	if err := q.First(&rel).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &rel, nil
}

// FindByStorageKey 反查（删除存储对象前确认真实归属，防「key 对不上却删了别人」）。
func (r *AgentReleaseRepo) FindByStorageKey(ctx context.Context, key string) (*entity.AgentRelease, error) {
	var rel entity.AgentRelease
	if err := r.db.WithContext(ctx).Where("storage_key = ?", key).First(&rel).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &rel, nil
}

// List 返回全部发布物，新版本在前（发布物量级是「每版本×平台几行」，故不分页）。
//
// 排序先按 created_at DESC 再按 id DESC：同一批上传的多平台产物时间戳可能相同，
// 只按时间排序在两次请求间可能给出不同顺序（分页/列表跳动），id 兜底保证稳定。
func (r *AgentReleaseRepo) List(ctx context.Context) ([]entity.AgentRelease, error) {
	var list []entity.AgentRelease
	if err := r.db.WithContext(ctx).
		Order("created_at DESC, id DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListByVersion 返回该版本的全部**已发布**产物（各平台一行）。
//
// 用途：判断「选中设备里每个平台是否都有产物」——只要有一台设备的平台缺产物，
// 那次下发就会被拒绝（而不是静默跳过那台，让人以为升了）。
func (r *AgentReleaseRepo) ListByVersion(ctx context.Context, version string) ([]entity.AgentRelease, error) {
	var list []entity.AgentRelease
	if err := r.db.WithContext(ctx).
		Where("version = ? AND status = ?", version, entity.AgentReleasePublished).
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// PublishedVersions 返回已发布过的版本号（去重、按首次出现的新旧排序）。
//
// 版本下拉的数据源：只列已发布，草稿不出现（草稿态的存在就是为了不让半成品被选中）。
func (r *AgentReleaseRepo) PublishedVersions(ctx context.Context) ([]string, error) {
	var versions []string
	if err := r.db.WithContext(ctx).Model(&entity.AgentRelease{}).
		Distinct().Where("status = ?", entity.AgentReleasePublished).
		Order("version DESC").Pluck("version", &versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// SetStatus 改发布状态。publishedAt 仅在置为已发布时写入（撤回时置空）。
func (r *AgentReleaseRepo) SetStatus(ctx context.Context, id uint64, status int8, publishedAt *time.Time) error {
	res := r.db.WithContext(ctx).Model(&entity.AgentRelease{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "published_at": publishedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 物理删除一行发布物（本表无软删：删除与否是「回滚余量」这条业务规则
// 在 service 层判定的，判定通过就该真的删掉，不留一份「已删但还在」的幽灵行）。
// 存储对象由 service 编排清理。
func (r *AgentReleaseRepo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&entity.AgentRelease{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
