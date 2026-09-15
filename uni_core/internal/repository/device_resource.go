package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DeviceResourceRepo 是资源维度表的数据访问层。
type DeviceResourceRepo struct {
	db *gorm.DB
}

func NewDeviceResourceRepository(db *gorm.DB) *DeviceResourceRepo {
	return &DeviceResourceRepo{db: db}
}

// UpsertSeen 批量幂等写入资源维度并刷新 last_seen_at。
// 冲突键 = (device_id, kind, name)；冲突时只更新 fs_type/last_seen_at/updated_at，
// **不覆盖 id**。写法沿用 repository/notice.go 的 clause.OnConflict 模式。
func (r *DeviceResourceRepo) UpsertSeen(ctx context.Context, rows []entity.DeviceResource, at time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].LastSeenAt = at
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "kind"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"fs_type", "last_seen_at", "updated_at"}),
	}).CreateInBatches(rows, 100).Error
}

// ListByDevice 枚举某设备某类资源，按 name 升序。
//
// staleBefore 是**枚举窗口的下界**（零值 = 不过滤）：非零时只返回
// `last_seen_at >= staleBefore` 的行 —— 超过窗口未再被观测到的资源**不再枚举**。
//
// 为什么过滤必须落在 SQL 层（S1，spec §8 明文）：spec 的原话是「已卸载的挂载点
// 会永久堆在 drill 下拉里」，所以过期资源要**掉出枚举结果**，而不是「返回给调用方
// 再让它自己筛」。此前这里忽略 staleBefore、返回全部行，于是 200 天前的挂载点
// 照样列出来 —— 契约没实现。
//
// 「窗口内但久未出现」的「已消失」标记由 service 用**另一个更短的阈值**判定
// （见 service/device.go 的 resourceStaleMarkDays）：两者同值时该标记会恒为 false
// （能枚举出来的必然都在窗口内），标记就彻底失去信息量。旧注释担心「SQL 过滤会让
// 已消失资源的 last_seen_at 丢失、无法展示」—— 那是把「过期不再枚举」与
// 「仍枚举但标 stale」两件事混成了一件；last_seen_at 仍随行返回，窗口内的资源
// 一个字段都不少。
func (r *DeviceResourceRepo) ListByDevice(ctx context.Context, deviceID uint64, kind string, staleBefore time.Time) ([]entity.DeviceResource, error) {
	q := r.db.WithContext(ctx).Where("device_id = ?", deviceID)
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if !staleBefore.IsZero() {
		q = q.Where("last_seen_at >= ?", staleBefore)
	}
	var out []entity.DeviceResource
	if err := q.Order("name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveID 把 (device_id, kind, name) 解析为 resource_id。
// 未命中返回 ErrNotFound 哨兵（errors.go）：调用方（drill 查询）据此返回**空结果**，
// 而把「DB 故障」留给 500 —— 与本包其它查询的未命中约定一致
// （哨兵包装了 gorm.ErrRecordNotFound，errors.Is(err, gorm.ErrRecordNotFound) 仍然成立）。
func (r *DeviceResourceRepo) ResolveID(ctx context.Context, deviceID uint64, kind, name string) (uint64, error) {
	var row entity.DeviceResource
	if err := r.db.WithContext(ctx).
		Select("id").
		Where("device_id = ? AND kind = ? AND name = ?", deviceID, kind, name).
		First(&row).Error; err != nil {
		return 0, notFoundOr(err)
	}
	return row.ID, nil
}

// DeleteByDevice 物理删除某设备的全部资源维度行（设备删除时调用）。
// 资源维度表无软删语义，且行数小，直接删。
func (r *DeviceResourceRepo) DeleteByDevice(ctx context.Context, deviceID uint64) error {
	return r.db.WithContext(ctx).
		Where("device_id = ?", deviceID).
		Delete(&entity.DeviceResource{}).Error
}
