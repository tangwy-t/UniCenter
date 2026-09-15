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

// FindByID 按主键查设备。未命中返回 ErrNotFound 哨兵（而非裸的
// gorm.ErrRecordNotFound）：service 层必须能分辨「设备不存在」（404）与
// 「DB 故障」（500），而哨兵与原始错误之间 errors.Is 是**单向**的
// （见 errors.go 的 notFoundOr）。errors.Is(err, gorm.ErrRecordNotFound) 仍然成立。
func (r *DeviceRepo) FindByID(ctx context.Context, id uint64) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &d, nil
}

// UpdateEnroll 按主键更新「注册时上报的库存字段 + 轮换后的 token_hash」。
//
// 只更新列出的列：**不动 status**（停用机器重新 enroll 不自动启用，
// 否则会绕过后台的管理意图），也不动 last_seen_at（那是 Touch 的职责）。
// 用 map 形式的 Updates 是为了让「零值也要写入」（如 hostname 变空）也生效 ——
// struct 形式的 Updates 会跳过零值字段。
func (r *DeviceRepo) UpdateEnroll(ctx context.Context, d *entity.Device) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", d.ID).
		Updates(map[string]any{
			"hostname":      d.Hostname,
			"os":            d.OS,
			"arch":          d.Arch,
			"kernel":        d.Kernel,
			"agent_version": d.AgentVersion,
			"platform":      d.Platform,
			"platform_ver":  d.PlatformVer,
			"cpu_model":     d.CPUModel,
			"cpu_cores":     d.CPUCores,
			"mem_total_mb":  d.MemTotalMB,
			"boot_time":     d.BootTime,
			"token_hash":    d.TokenHash,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByInstanceID 按 agent 落盘指纹查设备（enroll 幂等键）。
// 未命中返回 ErrNotFound 哨兵（语义与 FindByID 一致）。
func (r *DeviceRepo) FindByInstanceID(ctx context.Context, instanceID string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&d).Error; err != nil {
		return nil, notFoundOr(err)
	}
	return &d, nil
}

// FindByTokenHash 按 sha256(agent_token) 查设备（鉴权路径）。
// 未命中返回 ErrNotFound 哨兵：鉴权把它映射成「token 无效」，而 DB 故障必须
// 留成 500 —— 否则一次数据库抖动会伪装成「所有 agent 的 token 都失效」。
func (r *DeviceRepo) FindByTokenHash(ctx context.Context, hash string) (*entity.Device, error) {
	var d entity.Device
	if err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&d).Error; err != nil {
		return nil, notFoundOr(err)
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
//
// 未命中返回 ErrNotFound 哨兵（而非任意错误）：调用方据此回 404，
// 同时把「DB 故障」留给 500 —— 见 errors.go 的说明。
func (r *DeviceRepo) SetStatus(ctx context.Context, id uint64, status int8) error {
	res := r.db.WithContext(ctx).Model(&entity.Device{}).
		Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 软删（BaseEntity 带 gorm.DeletedAt）。
// 指标行与 Redis 键的清理由 service 层负责编排（见 spec §7.3）。
// 未命中返回 ErrNotFound 哨兵（语义同 SetStatus）。
func (r *DeviceRepo) Delete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&entity.Device{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
