package repository

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type JobLogRepo struct {
	db *gorm.DB
}

func NewJobLogRepository(db *gorm.DB) *JobLogRepo {
	return &JobLogRepo{db: db}
}

func (r *JobLogRepo) applyFilters(db *gorm.DB, query *request.JobLogQuery) *gorm.DB {
	if query.JobID > 0 {
		db = db.Where("job_id = ?", query.JobID)
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	if query.StartTime != "" {
		db = db.Where("start_time >= ?", query.StartTime)
	}
	if query.EndTime != "" {
		db = db.Where("start_time <= ?", query.EndTime)
	}
	return db
}

func (r *JobLogRepo) FindPage(ctx context.Context, query *request.JobLogQuery) ([]entity.SysJobLog, int64, error) {
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysJobLog{}), query)
	return paginate[entity.SysJobLog](db, db.Order("id DESC"), query)
}

func (r *JobLogRepo) Create(ctx context.Context, log *entity.SysJobLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *JobLogRepo) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("start_time < ?", before).Delete(&entity.SysJobLog{})
	return result.RowsAffected, result.Error
}
