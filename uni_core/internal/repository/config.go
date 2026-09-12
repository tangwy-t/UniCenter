package repository

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type ConfigRepo struct {
	db *gorm.DB
}

func NewConfigRepository(db *gorm.DB) *ConfigRepo {
	return &ConfigRepo{db: db}
}

func (r *ConfigRepo) applyFilters(db *gorm.DB, query *request.ConfigQuery) *gorm.DB {
	if query.ConfigKey != "" {
		db = db.Where("config_key LIKE ?", "%"+query.ConfigKey+"%")
	}
	if query.ConfigType != "" {
		db = db.Where("config_type = ?", query.ConfigType)
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	return db
}

func (r *ConfigRepo) FindPage(ctx context.Context, query *request.ConfigQuery) ([]entity.SysConfig, int64, error) {
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysConfig{}), query)
	return paginate[entity.SysConfig](db, db.Order("id ASC"), query)
}

func (r *ConfigRepo) FindByID(ctx context.Context, id uint64) (*entity.SysConfig, error) {
	var cfg entity.SysConfig
	if err := r.db.WithContext(ctx).First(&cfg, id).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *ConfigRepo) FindByKey(ctx context.Context, key string) (*entity.SysConfig, error) {
	var cfg entity.SysConfig
	if err := r.db.WithContext(ctx).Where("config_key = ?", key).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *ConfigRepo) CheckKeyExists(ctx context.Context, key string, excludeID uint64) (bool, error) {
	var count int64
	db := r.db.WithContext(ctx).Model(&entity.SysConfig{}).Where("config_key = ?", key)
	if excludeID > 0 {
		db = db.Where("id != ?", excludeID)
	}
	if err := db.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *ConfigRepo) Create(ctx context.Context, cfg *entity.SysConfig) error {
	return r.db.WithContext(ctx).Create(cfg).Error
}

func (r *ConfigRepo) Update(ctx context.Context, cfg *entity.SysConfig) error {
	return r.db.WithContext(ctx).Model(&entity.SysConfig{}).Where("id = ?", cfg.ID).Updates(cfg).Error
}

func (r *ConfigRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysConfig{}, id).Error
}

func (r *ConfigRepo) FindAllEnabled(ctx context.Context) ([]entity.SysConfig, error) {
	var list []entity.SysConfig
	if err := r.db.WithContext(ctx).Where("status = ?", entity.ConfigStatusEnabled).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
