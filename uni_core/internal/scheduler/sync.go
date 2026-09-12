package scheduler

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/service"

	"go.uber.org/zap"
)

const eventJobChanged = "job.changed"
const eventConfigChanged = service.ConfigChangedChannel

// jobChangedMsg 是 Redis Pub/Sub 消息体。
type jobChangedMsg struct {
	Action    string `json:"action"` // "add" | "remove"
	ID        uint64 `json:"id"`
	UpdatedAt int64  `json:"updatedAt"` // unix timestamp ms
}

// handleJobChanged 处理单个任务变更消息。
func (s *Scheduler) handleJobChanged(msg jobChangedMsg) {
	switch msg.Action {
	case "add":
		job, err := s.repo.FindByID(context.Background(), msg.ID)
		if err != nil {
			s.logger.Error("scheduler: failed to find job for add", zap.Uint64("jobID", msg.ID), zap.Error(err))
			return
		}
		s.Remove(msg.ID)
		s.Add(job)
	case "remove":
		s.Remove(msg.ID)
	default:
		s.logger.Warn("scheduler: unknown pubsub action", zap.String("action", msg.Action))
	}
}

// loop 启动定期 DB 重同步，支持通过 resyncCh 热更新间隔。
func (s *Scheduler) loop(stopCh chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler loop panic recovered: restarting", zap.Any("panic", r))
			go s.loop(stopCh) // 单次 loadALL panic 不应杀进程
			return
		}
		s.ticker.Stop()
	}()

	for {
		select {
		case <-s.ticker.C:
			s.loadALL()
		case <-stopCh:
			return
		}
	}
}

// loadALL 从 DB 加载所有启用任务，增量更新 cron 引擎。
func (s *Scheduler) loadALL() {
	ctx := context.Background()
	jobs, err := s.repo.FindAllEnabled(ctx)
	if err != nil {
		s.logger.Error("scheduler: resync failed to load jobs", zap.Error(err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 构建 DB 中的 jobID 集合
	dbJobIDs := make(map[uint64]struct{}, len(jobs))
	for i := range jobs {
		dbJobIDs[jobs[i].ID] = struct{}{}
	}

	// 移除 cron 中但 DB 中已不存在的任务
	for jobID := range s.cronMap {
		if _, exists := dbJobIDs[jobID]; !exists {
			s.logger.Info("scheduler: resync removing stale job", zap.Uint64("jobID", jobID))
			s.cron.Remove(s.cronMap[jobID])
			delete(s.cronMap, jobID)
			delete(s.jobCrons, jobID)
		}
	}

	// 添加新任务,并刷新表达式已变更的任务。
	// Pub/Sub 是 fire-and-forget,消息丢失(重启/断连窗口)时 resync 是
	// 唯一的兜底;若只增删不比对表达式,改 cron 的更新会永远丢失。
	for i := range jobs {
		job := &jobs[i]
		if oldExpr, exists := s.jobCrons[job.ID]; exists {
			if oldExpr != job.CronExpression {
				s.logger.Info("scheduler: resync refreshing changed cron expression",
					zap.Uint64("jobID", job.ID),
					zap.String("old", oldExpr),
					zap.String("new", job.CronExpression))
				s.cron.Remove(s.cronMap[job.ID])
				delete(s.cronMap, job.ID)
				delete(s.jobCrons, job.ID)
			}
		}
		if _, exists := s.cronMap[job.ID]; !exists {
			s.addJob(job)
		}
	}

	s.logger.Info("scheduler: resync completed",
		zap.Int("activeJobs", len(s.cronMap)))
}

// BroadcastJobChanged 向 Redis Pub/Sub 广播任务变更。
func (s *Scheduler) BroadcastJobChanged(ctx context.Context, action string, id uint64) {
	msg := jobChangedMsg{
		Action:    action,
		ID:        id,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := s.broker.Publish(ctx, eventJobChanged, msg); err != nil {
		s.logger.Warn("scheduler: failed to publish job changed", zap.Error(err))
	}
}
