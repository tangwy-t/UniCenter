package repository

import (
	"context"
	"errors"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type DictTypeRepo struct {
	db *gorm.DB
}

// NewDictTypeRepository returns a DictTypeRepository backed by the given *gorm.DB.
func NewDictTypeRepository(db *gorm.DB) *DictTypeRepo {
	return &DictTypeRepo{db: db}
}

func (r *DictTypeRepo) applyFilters(db *gorm.DB, query *request.DictTypeQuery) *gorm.DB {
	if query.Name != "" {
		db = db.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.Code != "" {
		db = db.Where("code LIKE ?", "%"+query.Code+"%")
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	return db
}

func (r *DictTypeRepo) FindPage(ctx context.Context, query *request.DictTypeQuery) ([]entity.SysDictType, int64, error) {
	countDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysDictType{}), query)
	dataDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysDictType{}), query).Order("id DESC")
	return paginate[entity.SysDictType](countDB, dataDB, query)
}

func (r *DictTypeRepo) FindByID(ctx context.Context, id uint64) (*entity.SysDictType, error) {
	var dt entity.SysDictType
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&dt).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &dt, nil
}

func (r *DictTypeRepo) FindByCode(ctx context.Context, code string) (*entity.SysDictType, error) {
	var dt entity.SysDictType
	err := r.db.WithContext(ctx).Where("code = ?", code).First(&dt).Error
	if err != nil {
		return nil, err
	}
	return &dt, nil
}

func (r *DictTypeRepo) Create(ctx context.Context, dt *entity.SysDictType) error {
	return r.db.WithContext(ctx).Create(dt).Error
}

func (r *DictTypeRepo) Update(ctx context.Context, dt *entity.SysDictType) error {
	return r.db.WithContext(ctx).Model(&entity.SysDictType{}).Where("id = ?", dt.ID).Select("code", "name", "status").Updates(dt).Error
}

func (r *DictTypeRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysDictType{}, id).Error
}

// DeleteTypeTx deletes all dict data of a type together with the type itself
// in one transaction. Without this, DeleteByTypeID could succeed while
// Delete fails, leaving an empty type stub and skipping cache invalidation.
func (r *DictTypeRepo) DeleteTypeTx(ctx context.Context, typeID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("type_id = ?", typeID).Delete(&entity.SysDictData{}).Error; err != nil {
			return err
		}
		return tx.Delete(&entity.SysDictType{}, typeID).Error
	})
}

func (r *DictTypeRepo) FindAllEnabled(ctx context.Context) ([]entity.SysDictType, error) {
	var types []entity.SysDictType
	err := r.db.WithContext(ctx).Where("status = ?", entity.DictTypeStatusEnabled).Find(&types).Error
	return types, err
}
