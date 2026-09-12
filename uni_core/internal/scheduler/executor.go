package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
)

// execute 执行单个任务：锁 → 执行 → 重试 → 记日志。
func (s *Scheduler) execute(ctx context.Context, job *entity.SysJob, triggerType int8) error {
	// 手动触发(RunOnce)传入的是 HTTP 请求 ctx:客户端断开即取消。
	// 若不剥离取消信号,锁释放与执行日志会因 ctx 取消而失败——锁悬挂
	// 至 TTL,跨实例阻塞该任务。WithoutCancel 保留 ctx 值(trace 等),
	// 剥离取消;执行超时由下方 WithTimeout 独立控制。
	ctx = context.WithoutCancel(ctx)

	jobID := job.ID
	lockKey := fmt.Sprintf("job:lock:%d", jobID)

	// 并发检查
	if job.Concurrent != nil && *job.Concurrent == entity.JobConcurrentDisallowed {
		s.mu.RLock()
		_, running := s.running[jobID]
		s.mu.RUnlock()
		if running {
			s.logger.Warn("scheduler: job skipped (concurrent disallowed, still running)",
				zap.Uint64("jobID", jobID), zap.String("name", job.Name))
			return nil
		}
	}

	// 执行超时控制
	maxExecSeconds := s.cfgProv.GetInt(ctx, "sys.scheduler.maxExecutionTime", 300)
	maxExec := time.Duration(maxExecSeconds) * time.Second
	if maxExec <= 0 {
		maxExec = 30 * time.Minute
	}

	// TryLock 获取分布式锁。锁 TTL 必须覆盖最大执行时间（外加缓冲）：
	// 若 TTL < 执行上限，长任务执行中锁就会过期，其他实例 TryLock 成功，
	// 同一任务跨实例并发重叠，互斥彻底失效。
	lockTTL := time.Duration(s.cfgProv.GetInt(ctx, "sys.scheduler.lockTTL", 60)) * time.Second
	if minTTL := maxExec + 30*time.Second; lockTTL < minTTL {
		lockTTL = minTTL
	}
	ok, err := s.locker.TryLock(ctx, lockKey, s.nodeID, lockTTL)
	if err != nil {
		s.logger.Error("scheduler: lock error", zap.Uint64("jobID", jobID), zap.Error(err))
		return err
	}
	if !ok {
		s.logger.Info("scheduler: job skipped (another instance running)", zap.Uint64("jobID", jobID))
		return nil
	}

	// defer: 释放锁（Locker 内部校验 owner）
	defer func() {
		if err := s.locker.Unlock(ctx, lockKey, s.nodeID); err != nil {
			s.logger.Error("scheduler: failed to release lock", zap.String("key", lockKey), zap.Error(err))
		}
	}()

	// 标记正在执行
	s.mu.Lock()
	s.running[jobID] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, jobID)
		s.mu.Unlock()
	}()

	execCtx, cancel := context.WithTimeout(ctx, maxExec)
	defer cancel()

	startTime := time.Now()

	// panic 恢复
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler: job panicked",
				zap.Uint64("jobID", jobID),
				zap.String("name", job.Name),
				zap.Any("panic", r),
				zap.String("stack", string(debug.Stack())))
			s.writeLog(ctx, job, triggerType, startTime, time.Now(),
				entity.JobLogStatusFailed, fmt.Errorf("panic: %v", r))
		}
	}()

	// 查找 Task
	t, ok := s.registry.Get(job.InvokeTarget)
	if !ok {
		err := fmt.Errorf("task not found: %s", job.InvokeTarget)
		s.writeLog(ctx, job, triggerType, time.Now(), time.Now(), entity.JobLogStatusFailed, err)
		return err
	}

	// 重试循环
	retryCount := 0
	if job.RetryCount != nil {
		retryCount = *job.RetryCount
	}
	// 硬上限
	maxRetryCount := s.cfgProv.GetInt(ctx, "sys.scheduler.maxRetryCount", 3)
	if retryCount > maxRetryCount {
		retryCount = maxRetryCount
	}

	retryInterval := time.Duration(0)
	if job.RetryInterval != nil {
		retryInterval = time.Duration(*job.RetryInterval) * time.Second
	}

	var lastErr error
retryLoop:
	for i := 0; i <= retryCount; i++ {
		if i > 0 && retryInterval > 0 {
			select {
			case <-time.After(retryInterval):
			case <-execCtx.Done():
				lastErr = execCtx.Err()
				break retryLoop
			}
		}

		var params json.RawMessage
		if job.InvokeParams != nil {
			params = json.RawMessage([]byte(*job.InvokeParams))
		}
		lastErr = t.Execute(execCtx, params)
		if lastErr == nil {
			endTime := time.Now()
			s.writeLog(ctx, job, triggerType, startTime, endTime, entity.JobLogStatusSuccess, nil)
			return nil
		}
		s.logger.Warn("scheduler: job execution failed, retrying",
			zap.Uint64("jobID", jobID),
			zap.String("name", job.Name),
			zap.Int("attempt", i+1),
			zap.Error(lastErr))
	}

	endTime := time.Now()
	s.writeLog(ctx, job, triggerType, startTime, endTime, entity.JobLogStatusFailed, lastErr)
	return lastErr
}

// writeLog 写入执行日志。
func (s *Scheduler) writeLog(ctx context.Context, job *entity.SysJob, triggerType int8, startTime, endTime time.Time, status int8, execErr error) {
	costMs := int(endTime.Sub(startTime).Milliseconds())
	log := &entity.SysJobLog{
		JobID:        job.ID,
		JobName:      job.Name,
		JobGroup:     job.JobGroup,
		InvokeTarget: job.InvokeTarget,
		TriggerType:  triggerType,
		StartTime:    startTime,
		EndTime:      &endTime,
		CostTime:     &costMs,
		Status:       status,
	}
	if execErr != nil {
		log.ErrorMsg = util.Ptr[string](execErr.Error())
	}
	if err := s.logRepo.Create(ctx, log); err != nil {
		s.logger.Error("scheduler: failed to write job log",
			zap.Uint64("jobID", job.ID),
			zap.Error(err))
	}
}
