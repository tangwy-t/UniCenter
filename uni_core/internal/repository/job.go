package repository

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type JobRepo struct {
	db *gorm.DB
}

func NewJobRepository(db *gorm.DB) *JobRepo {
	return &JobRepo{db: db}
}

func (r *JobRepo) applyFilters(db *gorm.DB, query *request.JobQuery) *gorm.DB {
	if query.Name != "" {
		db = db.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.JobGroup != "" {
		db = db.Where("job_group = ?", query.JobGroup)
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	return db
}

func (r *JobRepo) FindPage(ctx context.Context, query *request.JobQuery) ([]entity.SysJob, int64, error) {
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysJob{}), query)
	return paginate[entity.SysJob](db, db.Order("id DESC"), query)
}

func (r *JobRepo) FindByID(ctx context.Context, id uint64) (*entity.SysJob, error) {
	var job entity.SysJob
	if err := r.db.WithContext(ctx).First(&job, id).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *JobRepo) FindAllEnabled(ctx context.Context) ([]entity.SysJob, error) {
	var jobs []entity.SysJob
	if err := r.db.WithContext(ctx).Where("status = ?", entity.JobStatusEnabled).Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *JobRepo) CheckNameExists(ctx context.Context, name, jobGroup string, excludeID uint64) (bool, error) {
	var count int64
	db := r.db.WithContext(ctx).Model(&entity.SysJob{}).Where("name = ? AND job_group = ?", name, jobGroup)
	if excludeID > 0 {
		db = db.Where("id != ?", excludeID)
	}
	if err := db.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *JobRepo) Create(ctx context.Context, job *entity.SysJob) error {
	return r.db.WithContext(ctx).Create(job).Error
}

func (r *JobRepo) Update(ctx context.Context, job *entity.SysJob) error {
	return r.db.WithContext(ctx).Model(&entity.SysJob{}).Where("id = ?", job.ID).Updates(job).Error
}

func (r *JobRepo) UpdateStatus(ctx context.Context, id uint64, status int8) error {
	return r.db.WithContext(ctx).Model(&entity.SysJob{}).Where("id = ?", id).Update("status", status).Error
}

func (r *JobRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysJob{}, id).Error
}
