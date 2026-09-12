package repository

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type DictDataRepo struct {
	db *gorm.DB
}

// NewDictDataRepository returns a DictDataRepository backed by the given *gorm.DB.
func NewDictDataRepository(db *gorm.DB) *DictDataRepo {
	return &DictDataRepo{db: db}
}

func (r *DictDataRepo) FindByTypeID(ctx context.Context, typeID uint64) ([]entity.SysDictData, error) {
	var data []entity.SysDictData
	err := r.db.WithContext(ctx).Where("type_id = ?", typeID).Order("sort ASC").Find(&data).Error
	return data, err
}

func (r *DictDataRepo) FindByID(ctx context.Context, id uint64) (*entity.SysDictData, error) {
	var dd entity.SysDictData
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&dd).Error
	if err != nil {
		return nil, err
	}
	return &dd, nil
}

func (r *DictDataRepo) Create(ctx context.Context, dd *entity.SysDictData) error {
	return r.db.WithContext(ctx).Create(dd).Error
}

func (r *DictDataRepo) Update(ctx context.Context, dd *entity.SysDictData) error {
	selects := []string{"label", "value", "list_class", "is_default", "sort", "status", "remark"}
	return r.db.WithContext(ctx).Model(&entity.SysDictData{}).Where("id = ?", dd.ID).Select(selects).Updates(dd).Error
}

func (r *DictDataRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysDictData{}, id).Error
}

func (r *DictDataRepo) DeleteByTypeID(ctx context.Context, typeID uint64) error {
	return r.db.WithContext(ctx).Where("type_id = ?", typeID).Delete(&entity.SysDictData{}).Error
}

func (r *DictDataRepo) ClearDefault(ctx context.Context, typeID uint64) error {
	return r.db.WithContext(ctx).Model(&entity.SysDictData{}).Where("type_id = ?", typeID).Update("is_default", 0).Error
}

func (r *DictDataRepo) FindEnabledByTypeID(ctx context.Context, typeID uint64) ([]entity.SysDictData, error) {
	var data []entity.SysDictData
	err := r.db.WithContext(ctx).
		Where("type_id = ? AND status = ?", typeID, entity.DictDataStatusEnabled).
		Order("sort ASC").
		Find(&data).Error
	return data, err
}

// CreateWithDefaultTx wraps ClearDefault+Create in a transaction.
// Caller must NOT share the ctx transaction with other writes.
func (r *DictDataRepo) CreateWithDefaultTx(ctx context.Context, dd *entity.SysDictData) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.SysDictData{}).Where("type_id = ?", dd.TypeID).Update("is_default", 0).Error; err != nil {
			return err
		}
		return tx.Create(dd).Error
	})
}

// UpdateWithDefaultTx wraps ClearDefault+Update in a transaction.
func (r *DictDataRepo) UpdateWithDefaultTx(ctx context.Context, dd *entity.SysDictData) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.SysDictData{}).Where("type_id = ?", dd.TypeID).Update("is_default", 0).Error; err != nil {
			return err
		}
		selects := []string{"label", "value", "list_class", "is_default", "sort", "status", "remark"}
		return tx.Model(&entity.SysDictData{}).Where("id = ?", dd.ID).Select(selects).Updates(dd).Error
	})
}
