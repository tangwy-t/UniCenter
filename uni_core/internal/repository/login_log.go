package repository

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type LoginLogRepo struct {
	db *gorm.DB
}

// NewLoginLogRepository returns a LoginLogRepository backed by the given *gorm.DB.
func NewLoginLogRepository(db *gorm.DB) *LoginLogRepo {
	return &LoginLogRepo{db: db}
}

func (r *LoginLogRepo) applyFilters(db *gorm.DB, query *request.LoginLogQuery) *gorm.DB {
	if query.Username != "" {
		db = db.Where("username LIKE ?", "%"+query.Username+"%")
	}
	if query.IP != "" {
		db = db.Where("ip = ?", query.IP)
	}
	if query.Code != nil {
		db = db.Where("code = ?", *query.Code)
	}
	if query.StartTime != "" {
		if t, err := time.Parse("2006-01-02", query.StartTime); err == nil {
			db = db.Where("login_time >= ?", t)
		}
	}
	if query.EndTime != "" {
		if t, err := time.Parse("2006-01-02", query.EndTime); err == nil {
			db = db.Where("login_time < ?", t.Add(24*time.Hour))
		}
	}
	return db
}

func (r *LoginLogRepo) FindPage(ctx context.Context, query *request.LoginLogQuery) ([]entity.SysLoginLog, int64, error) {
	countDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysLoginLog{}), query)
	dataDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysLoginLog{}), query).Order("id DESC")
	return paginate[entity.SysLoginLog](countDB, dataDB, query)
}

func (r *LoginLogRepo) Create(ctx context.Context, log *entity.SysLoginLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *LoginLogRepo) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("login_time < ?", before).Delete(&entity.SysLoginLog{})
	return result.RowsAffected, result.Error
}
