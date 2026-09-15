package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DeviceRepo 是设备表的数据访问层。类型名沿用既有 *Repo 命名（见 RoleRepo/NoticeRepo）。
type DeviceRepo struct {
	db *gorm.DB
}

func NewDeviceRepository(db *gorm.DB) *DeviceRepo { return &DeviceRepo{db: db} }

// Create 插入设备。instance_id / token_hash 的唯一约束冲突由调用方
// 用 database.IsDuplicateKey 判定（enroll 幂等依赖它）。
func (r *DeviceRepo) Create(ctx context.Context, d *entity.Device) error {
	return r.db.WithContext(ctx).Create(d).Error
}

// FindByInstanceID 按 agent 落盘指纹查设备（enroll 幂等键）。
func (r *DeviceRepo) FindByInstanceID(ctx context.Context, instanceID string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// FindByTokenHash 按 sha256(agent_token) 查设备（鉴权路径）。
func (r *DeviceRepo) FindByTokenHash(ctx context.Context, hash string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// Touch 只更新 last_seen_at 一列，避免整行写放大（该路径每 report 周期触发）。
func (r *DeviceRepo) Touch(ctx context.Context, id uint64, at time.Time) error {
	return r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).
		UpdateColumn("last_seen_at", at).Error
}

// applyFilters 把列表筛选条件链到给定会话上（与 config.go/file.go 的
// applyFilters 同款：纯函数式地把 db 链下去，不共享语句状态）。
func (r *DeviceRepo) applyFilters(db *gorm.DB, q *request.DeviceQuery, onlineSince time.Time) *gorm.DB {
	if q.Hostname != "" {
		db = db.Where("hostname LIKE ?", "%"+q.Hostname+"%")
	}
	if q.Status != nil {
		db = db.Where("status = ?", *q.Status)
	}
	if q.Online != nil {
		if *q.Online {
			db = db.Where("last_seen_at IS NOT NULL AND last_seen_at >= ?", onlineSince)
		} else {
			db = db.Where("last_seen_at IS NULL OR last_seen_at < ?", onlineSince)
		}
	}
	return db
}

// FindPage 分页查询。
//
// onlineSince 是在线阈值折算出的时间点（service 依 sys.agent.offlineThreshold 计算）：
//   - Online == nil  → 不过滤
//   - Online == true → last_seen_at >= onlineSince
//   - Online == false→ last_seen_at < onlineSince 或 IS NULL
//
// Count 与取页走 pagination.go 的公共 paginate[T]（本包 11 个仓储的统一入口）：
// countDB 不带 Order/Offset/Limit，dataDB 才链排序；两个会话相互独立，
// 避免 GORM 语句状态在 COUNT 与 SELECT 之间串味（评审 F4：device 曾是唯一
// 手写 Count + Offset/Limit/Find 的例外）。
func (r *DeviceRepo) FindPage(ctx context.Context, q *request.DeviceQuery, onlineSince time.Time) ([]entity.Device, int64, error) {
	countDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), q, onlineSince)
	dataDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.Device{}), q, onlineSince).
		Order("last_seen_at DESC, id DESC")
	return paginate[entity.Device](countDB, dataDB, q)
}

// SetStatus 设置启停态（管理侧属性，与在线状态正交）。
func (r *DeviceRepo) SetStatus(ctx context.Context, id uint64, status int8) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete 软删（BaseEntity 带 gorm.DeletedAt）。
// 指标行与 Redis 键的清理由 service 层负责编排（见 spec §7.3）。
func (r *DeviceRepo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&entity.Device{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
