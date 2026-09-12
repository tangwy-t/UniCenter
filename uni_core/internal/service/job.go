package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"github.com/tangwy-t/webmanager-server/internal/task"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// TaskInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type TaskInterface interface {
	Get(name string) (task.Task, bool)
	Names() []string
	List() []task.TargetInfo
}

// SchedulerInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type SchedulerInterface interface {
	Add(job *entity.SysJob)
	Remove(jobID uint64)
	RunOnce(ctx context.Context, job *entity.SysJob) error
	GetNextRunTime(jobID uint64) *util.JSONTime
	BroadcastJobChanged(ctx context.Context, action string, id uint64)
	GetHealth() (running bool, nodeID string, totalJobs int, runningJobs int, uptime string)
}

// JobLogRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// JobLogRepositoryInterface 任务执行日志数据访问接口。
type JobLogRepositoryInterface interface {
	FindPage(ctx context.Context, query *request.JobLogQuery) ([]entity.SysJobLog, int64, error)
	Create(ctx context.Context, log *entity.SysJobLog) error
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// JobRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// JobRepositoryInterface 定时任务数据访问接口。
type JobRepositoryInterface interface {
	FindPage(ctx context.Context, query *request.JobQuery) ([]entity.SysJob, int64, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysJob, error)
	FindAllEnabled(ctx context.Context) ([]entity.SysJob, error)
	CheckNameExists(ctx context.Context, name, jobGroup string, excludeID uint64) (bool, error)
	Create(ctx context.Context, job *entity.SysJob) error
	Update(ctx context.Context, job *entity.SysJob) error
	UpdateStatus(ctx context.Context, id uint64, status int8) error
	Delete(ctx context.Context, id uint64) error
}

var cronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type JobService struct {
	repo      JobRepositoryInterface
	logRepo   JobLogRepositoryInterface
	scheduler SchedulerInterface
	registry  TaskInterface
	logger    logger.LoggerInterface
}

func NewJobService(
	repo JobRepositoryInterface,
	logRepo JobLogRepositoryInterface,
	sch SchedulerInterface,
	registry TaskInterface,
	logger logger.LoggerInterface,
) *JobService {
	return &JobService{
		repo:      repo,
		logRepo:   logRepo,
		scheduler: sch,
		registry:  registry,
		logger:    logger,
	}
}

func (s *JobService) FindPage(ctx context.Context, query *request.JobQuery) (*app.PageResponse, error) {
	list, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		s.logger.Error("JobService.FindPage failed", zap.Error(err))
		return nil, apperror.Internal("查询定时任务失败")
	}
	var resp []response.JobResp
	for i := range list {
		r := util.MapEntity[response.JobResp](&list[i], s.logger)
		r.NextRunTime = s.scheduler.GetNextRunTime(list[i].ID)
		resp = append(resp, r)
	}
	return app.NewPageResponse(resp, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *JobService) FindByID(ctx context.Context, id uint64) (*response.JobResp, error) {
	job, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "定时任务不存在")
	}
	resp := util.MapEntity[response.JobResp](job, s.logger)
	resp.NextRunTime = s.scheduler.GetNextRunTime(job.ID)
	return &resp, nil
}

func (s *JobService) Create(ctx context.Context, req *request.CreateJobReq) (uint64, error) {
	// 1. Validate invokeTarget
	if _, ok := s.registry.Get(req.InvokeTarget); !ok {
		return 0, apperror.BadRequest(fmt.Sprintf("调用目标不存在: '%s'，可用目标: %s",
			req.InvokeTarget, strings.Join(s.registry.Names(), ", ")))
	}

	// 2. Validate invokeParams JSON
	if req.InvokeParams != nil && *req.InvokeParams != "" {
		if !json.Valid([]byte(*req.InvokeParams)) {
			return 0, apperror.BadRequest("invokeParams 格式错误: 不是有效的 JSON")
		}
	}

	// 3. Validate cronExpression
	if _, err := cronParser.Parse(req.CronExpression); err != nil {
		return 0, apperror.BadRequest(fmt.Sprintf("cron 表达式格式错误: %v", err))
	}

	// 3.1 Validate runAtStartup
	if req.RunAtStartup != nil && *req.RunAtStartup != entity.JobRunAtStartupYes &&
		*req.RunAtStartup != entity.JobRunAtStartupNo {
		return 0, apperror.BadRequest("runAtStartup 参数错误: 仅支持 0(否) 或 1(是)")
	}

	// 4. Check name uniqueness
	exists, err := s.repo.CheckNameExists(ctx, req.Name, req.JobGroup, 0)
	if err != nil {
		return 0, apperror.Internal("检查任务名称失败")
	}
	if exists {
		return 0, apperror.Conflict("任务名称和分组组合已存在")
	}

	// 5. Build entity
	job := &entity.SysJob{}
	util.CopyEntity(job, req, s.logger)

	// 6. Create
	if err := s.repo.Create(ctx, job); err != nil {
		s.logger.Error("JobService.Create failed", zap.Error(err))
		return 0, apperror.Internal("创建定时任务失败")
	}

	// 7. Add to scheduler if enabled
	if job.Status != nil && *job.Status == entity.JobStatusEnabled {
		s.scheduler.Add(job)
		s.scheduler.BroadcastJobChanged(ctx, "add", job.ID)
	}

	s.logger.Info("job created", zap.Uint64("jobId", job.ID), zap.String("name", job.Name))
	return job.ID, nil
}

func (s *JobService) Update(ctx context.Context, id uint64, req *request.UpdateJobReq) error {
	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "定时任务不存在")
	}

	// 1. Validate invokeTarget
	if _, ok := s.registry.Get(req.InvokeTarget); !ok {
		return apperror.BadRequest(fmt.Sprintf("调用目标不存在: '%s'，可用目标: %s",
			req.InvokeTarget, strings.Join(s.registry.Names(), ", ")))
	}

	// 2. Validate invokeParams JSON
	if req.InvokeParams != nil && *req.InvokeParams != "" {
		if !json.Valid([]byte(*req.InvokeParams)) {
			return apperror.BadRequest("invokeParams 格式错误: 不是有效的 JSON")
		}
	}

	// 3. Validate cronExpression
	if _, err := cronParser.Parse(req.CronExpression); err != nil {
		return apperror.BadRequest(fmt.Sprintf("cron 表达式格式错误: %v", err))
	}

	// 3.1 Validate runAtStartup
	if req.RunAtStartup != nil && *req.RunAtStartup != entity.JobRunAtStartupYes &&
		*req.RunAtStartup != entity.JobRunAtStartupNo {
		return apperror.BadRequest("runAtStartup 参数错误: 仅支持 0(否) 或 1(是)")
	}

	// 4. Remove from scheduler first
	s.scheduler.Remove(id)

	// 5. Update entity
	util.CopyEntity(existing, req, s.logger)

	if err := s.repo.Update(ctx, existing); err != nil {
		s.logger.Error("JobService.Update failed", zap.Error(err))
		return apperror.Internal("更新定时任务失败")
	}

	// 6. Re-add to scheduler if status is enabled
	existing.Status = req.Status
	if existing.Status != nil && *existing.Status == entity.JobStatusEnabled {
		existing.CronExpression = req.CronExpression
		existing.InvokeTarget = req.InvokeTarget
		s.scheduler.Add(existing)
		s.scheduler.BroadcastJobChanged(ctx, "add", id)
	} else {
		s.scheduler.BroadcastJobChanged(ctx, "remove", id)
	}

	s.logger.Info("job updated", zap.Uint64("jobId", id))
	return nil
}

func (s *JobService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return translateNotFound(err, "定时任务不存在")
	}
	s.scheduler.Remove(id)
	s.scheduler.BroadcastJobChanged(ctx, "remove", id)
	if err := s.repo.Delete(ctx, id); err != nil {
		s.logger.Error("JobService.Delete failed", zap.Error(err))
		return apperror.Internal("删除定时任务失败")
	}
	s.logger.Info("job deleted", zap.Uint64("jobId", id))
	return nil
}

func (s *JobService) Pause(ctx context.Context, id uint64) error {
	job, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "任务不存在")
	}
	if job.Status == nil || *job.Status != entity.JobStatusEnabled {
		return apperror.BadRequest("任务未处于启用状态，无需暂停")
	}
	s.scheduler.Remove(id)
	s.scheduler.BroadcastJobChanged(ctx, "remove", id)
	if err := s.repo.UpdateStatus(ctx, id, entity.JobStatusPaused); err != nil {
		s.logger.Error("JobService.Pause failed", zap.Error(err))
		return apperror.Internal("暂停任务失败")
	}
	s.logger.Info("job paused", zap.Uint64("jobId", id))
	return nil
}

func (s *JobService) Resume(ctx context.Context, id uint64) error {
	job, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "任务不存在")
	}
	if job.Status == nil || *job.Status != entity.JobStatusPaused {
		return apperror.BadRequest("任务未处于暂停状态，无需恢复")
	}
	if err := s.repo.UpdateStatus(ctx, id, entity.JobStatusEnabled); err != nil {
		s.logger.Error("JobService.Resume failed", zap.Error(err))
		return apperror.Internal("恢复任务失败")
	}
	job.Status = util.Ptr[int8](entity.JobStatusEnabled)
	s.scheduler.Add(job)
	s.scheduler.BroadcastJobChanged(ctx, "add", id)
	s.logger.Info("job resumed", zap.Uint64("jobId", id))
	return nil
}

func (s *JobService) RunOnce(ctx context.Context, id uint64) error {
	job, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "任务不存在")
	}
	if err := s.scheduler.RunOnce(ctx, job); err != nil {
		s.logger.Error("JobService.RunOnce failed", zap.Error(err))
		return apperror.Internal("执行任务失败")
	}
	s.logger.Info("job executed manually", zap.Uint64("jobId", id))
	return nil
}

func (s *JobService) FindLogPage(ctx context.Context, query *request.JobLogQuery) (*app.PageResponse, error) {
	list, total, err := s.logRepo.FindPage(ctx, query)
	if err != nil {
		s.logger.Error("JobService.FindLogPage failed", zap.Error(err))
		return nil, apperror.Internal("查询任务日志失败")
	}
	var resp []response.JobLogResp
	for i := range list {
		resp = append(resp, util.MapEntity[response.JobLogResp](&list[i], s.logger))
	}
	return app.NewPageResponse(resp, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *JobService) DeleteLogsBefore(ctx context.Context, before time.Time) error {
	deleted, err := s.logRepo.DeleteBefore(ctx, before)
	if err != nil {
		s.logger.Error("JobService.DeleteLogsBefore failed", zap.Error(err))
		return apperror.Internal("清理任务日志失败")
	}
	s.logger.Info("job logs deleted before", zap.Time("before", before), zap.Int64("deleted", deleted))
	return nil
}

func (s *JobService) GetTargets() []response.JobTargetResp {
	targets := s.registry.List()
	result := make([]response.JobTargetResp, 0, len(targets))
	for _, t := range targets {
		result = append(result, response.JobTargetResp{
			Target:      t.Target,
			DisplayName: t.DisplayName,
			HasParams:   t.HasParams,
			ParamSchema: t.ParamSchema,
		})
	}
	return result
}

func (s *JobService) GetHealth() *response.JobHealthResp {
	running, nodeID, totalJobs, runningJobs, uptime := s.scheduler.GetHealth()
	return &response.JobHealthResp{
		SchedulerRunning: running,
		InstanceID:       nodeID,
		TotalJobs:        totalJobs,
		RunningJobs:      runningJobs,
		Uptime:           uptime,
	}
}
