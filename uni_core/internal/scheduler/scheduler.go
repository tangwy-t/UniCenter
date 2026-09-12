package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/service"
	"go.uber.org/zap"
)

// Scheduler 管理所有定时任务的调度生命周期。
type Scheduler struct {
	mu       sync.RWMutex
	cron     *cron.Cron
	cronMap  map[uint64]cron.EntryID // jobID → cron entryID
	jobCrons map[uint64]string       // jobID → 当前注册的表达式(变更检测用)
	running  map[uint64]struct{}     // 当前正在执行的任务 ID 集合
	registry TaskInterface
	repo     JobRepository
	logRepo  JobLogRepository
	locker   Locker
	broker   BrokerInterface
	logger   logger.LoggerInterface
	nodeID   string
	cfgProv  ConfigGetterInterface
	started  bool
	startAt  time.Time
	stopCh   chan struct{}
	ticker   *time.Ticker
}

// NewScheduler 创建调度器实例。调度器的 Stop 钩子通过 lifecycle.Manager 注册为
// "scheduler"，关闭时由 LIFO 逆序调用。
// 构造中若启动失败返回 error,由调用方决定退出方式 —— 不在这里 os.Exit,
// 否则会跳过所有已注册组件的优雅清理。
func NewScheduler(
	registry TaskInterface,
	repo JobRepository,
	logRepo JobLogRepository,
	locker Locker,
	broker BrokerInterface,
	logger logger.LoggerInterface,
	cfgProv ConfigGetterInterface,
	lc lifecycle.ManagerInterface,
) (*Scheduler, error) {
	hostname, _ := os.Hostname()
	nodeID := fmt.Sprintf("%s:%d", hostname, os.Getpid())
	s := &Scheduler{
		cron:     cron.New(cron.WithSeconds(), cron.WithLocation(loadTimezone(cfgProv, logger))),
		cronMap:  make(map[uint64]cron.EntryID),
		jobCrons: make(map[uint64]string),
		running:  make(map[uint64]struct{}),
		registry: registry,
		repo:     repo,
		logRepo:  logRepo,
		locker:   locker,
		broker:   broker,
		logger:   logger,
		nodeID:   nodeID,
		cfgProv:  cfgProv,
		ticker:   time.NewTicker(time.Duration(cfgProv.GetInt(context.Background(), "sys.scheduler.resyncInterval", 60)) * time.Second),
	}
	if lc != nil {
		lc.RegisterTo("cleanup", "scheduler", func(context.Context) error { s.Stop(); return nil })
	}

	if err := s.Start(); err != nil {
		return nil, fmt.Errorf("scheduler: start failed: %w", err)
	}

	// 注册 config:changed PubSub Handler（热更新）
	s.broker.Subscribe(eventConfigChanged, func(ctx context.Context, eventType string, payload []byte) error {
		var msg service.ConfigChangedMsg
		if err := json.Unmarshal(payload, &msg); err != nil {
			s.logger.Warn("scheduler: failed to parse config:changed message", zap.Error(err))
			return nil
		}
		if strings.HasPrefix(msg.Key, "sys.scheduler.") {
			s.ReloadConfig(msg.Key)
		}
		return nil
	})

	// 注册 job:changed PubSub Handler
	s.broker.Subscribe(eventJobChanged, func(ctx context.Context, eventType string, payload []byte) error {
		var m jobChangedMsg
		if err := json.Unmarshal(payload, &m); err != nil {
			s.logger.Warn("scheduler: failed to parse pubsub message", zap.Error(err))
			return nil
		}
		s.handleJobChanged(m)
		return nil
	})

	return s, nil
}

// Start 启动调度器：加载 DB 任务、启动 cron 引擎、启动定期重同步。
func (s *Scheduler) Start() error {
	if !s.cfgProv.GetBool(context.Background(), "sys.scheduler.enabled", false) {
		s.logger.Info("scheduler is disabled, skipping start")
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("scheduler already started")
	}

	s.stopCh = make(chan struct{})
	stopCh := s.stopCh // capture for goroutine to avoid data race on s.stopCh

	// 1. 从 DB 加载所有启用的任务
	ctx := context.Background()
	jobs, err := s.repo.FindAllEnabled(ctx)
	if err != nil {
		s.logger.Error("scheduler: failed to load enabled jobs", zap.Error(err))
		return fmt.Errorf("scheduler: load enabled jobs: %w", err)
	}

	for i := range jobs {
		s.addJob(&jobs[i])
		if jobs[i].RunAtStartup != nil && *jobs[i].RunAtStartup == 1 {
			go s.execute(context.Background(), &jobs[i], entity.JobLogTriggerCron)
		}
	}

	// 2. 启动 cron 引擎
	s.cron.Start()

	// 3. 启动定期 DB 重同步
	go s.loop(stopCh)

	s.started = true
	s.startAt = time.Now()
	s.logger.Info("scheduler started",
		zap.String("nodeID", s.nodeID),
		zap.Int("loadedJobs", len(jobs)))
	return nil
}

// loadTimezone 从配置读取 cron 表达式的求值时区(默认跟随系统本地时区)。
// cron 表达式按本地时区解释是隐式契约:容器 TZ 与开发机不一致时,
// "每天 2 点" 的实际执行时间会整体漂移 —— 显式配置 sys.scheduler.timezone
// 可将调度时区固定下来(如 "Asia/Shanghai")。非法值回退 Local 并告警。
func loadTimezone(cfgProv ConfigGetterInterface, logger logger.LoggerInterface) *time.Location {
	name := cfgProv.GetString(context.Background(), "sys.scheduler.timezone", "Local")
	loc, err := time.LoadLocation(name)
	if err != nil {
		logger.Warn("scheduler: invalid sys.scheduler.timezone, falling back to Local",
			zap.String("timezone", name), zap.Error(err))
		return time.Local
	}
	return loc
}

// Stop 优雅关闭调度器：停止 cron 引擎，等待执行中任务完成。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	// started 在入口锁内即置 false:此前在函数尾部才置位,lifecycle 清理与
	// 配置热更(resetStatus)并发调用 Stop 会双双通过上方闸门,对同一
	// stopCh 二次 close 触发 panic。现在只有完成 true→false 翻转的那次
	// 调用有权 close,并发调用在闸门处直接返回。
	s.started = false
	ch := s.stopCh // capture under lock to avoid data race
	s.mu.Unlock()

	s.logger.Info("scheduler: stopping...")
	close(ch)

	// 停止 cron（不再触发新任务）
	ctx := s.cron.Stop()
	<-ctx.Done()

	// 等待执行中任务完成（最多 stopTimeout 秒）。
	// 注意：这里不能 select <-ch —— ch 已被 close（Stop 一开始就关掉了它），
	// 该分支会立即命中导致 goroutine 秒退、waitCh 永不关闭，Stop 便总是
	// 等满 stopTimeout 并误报 "forcing stop"。只轮询 running 计数即可。
	stopTimeout := time.Duration(s.cfgProv.GetInt(context.Background(), "sys.scheduler.stopTimeout", 30)) * time.Second
	waitCh := make(chan struct{})
	go func() {
		for {
			s.mu.RLock()
			count := len(s.running)
			s.mu.RUnlock()
			if count == 0 {
				close(waitCh)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-waitCh:
		s.logger.Info("scheduler: all running jobs completed")
	case <-time.After(stopTimeout):
		s.logger.Warn("scheduler: timeout waiting for running jobs, forcing stop")
	}

	s.logger.Info("scheduler stopped")
}

// ReloadConfig 触发配置热更新：根据 enabled 开关启停调度器，或通知 resync 循环重新读取配置。
func (s *Scheduler) ReloadConfig(key string) {
	switch key {
	case "sys.scheduler.enabled":
		s.resetStatus()
	case "sys.scheduler.resyncInterval":
		s.resetTicker()
	}
}

func (s *Scheduler) resetStatus() {
	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()

	enabled := s.cfgProv.GetBool(context.Background(), "sys.scheduler.enabled", false)

	if enabled && !started {
		s.logger.Info("scheduler: enabled by config reload, starting...")
		if err := s.Start(); err != nil {
			s.logger.Warn("scheduler: ReloadConfig start failed", zap.Error(err))
		}
	}

	if !enabled && started {
		s.logger.Info("scheduler: disabled by config reload, stopping...")
		s.Stop()
	}
}
func (s *Scheduler) resetTicker() {

	newInterval := s.cfgProv.GetInt(context.Background(), "sys.scheduler.resyncInterval", 60)
	s.ticker.Reset(time.Duration(newInterval) * time.Second)
}

// Add 添加一个任务到 cron 调度。
func (s *Scheduler) Add(job *entity.SysJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addJob(job)
}

// addJob 是内部方法，调用者必须持有锁。
func (s *Scheduler) addJob(job *entity.SysJob) {
	if job.Status == nil || *job.Status != entity.JobStatusEnabled {
		return
	}
	if _, exists := s.cronMap[job.ID]; exists {
		return
	}

	entryID, err := s.cron.AddFunc(job.CronExpression, s.createJobFunc(job))
	if err != nil {
		s.logger.Error("scheduler: failed to add cron job",
			zap.Uint64("jobID", job.ID),
			zap.String("cron", job.CronExpression),
			zap.Error(err))
		return
	}
	s.cronMap[job.ID] = entryID
	s.jobCrons[job.ID] = job.CronExpression
	s.logger.Info("scheduler: job added",
		zap.Uint64("jobID", job.ID),
		zap.String("name", job.Name),
		zap.String("cron", job.CronExpression))
}

// Remove 从 cron 调度中移除一个任务。
func (s *Scheduler) Remove(jobID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, exists := s.cronMap[jobID]; exists {
		s.cron.Remove(entryID)
		delete(s.cronMap, jobID)
		delete(s.jobCrons, jobID)
		s.logger.Info("scheduler: job removed", zap.Uint64("jobID", jobID))
	}
}

// RunOnce 手动立即执行一次任务。
func (s *Scheduler) RunOnce(ctx context.Context, job *entity.SysJob) error {
	return s.execute(ctx, job, entity.JobLogTriggerManual)
}

// GetNextRunTime 返回指定任务的下次执行时间。
func (s *Scheduler) GetNextRunTime(jobID uint64) *util.JSONTime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entryID, exists := s.cronMap[jobID]
	if !exists {
		return nil
	}
	entry := s.cron.Entry(entryID)
	t := entry.Next
	jt := util.JSONTime(t)
	return &jt
}

// createJobFunc 创建 job 的 cron 执行函数（闭包捕获 job 快照）。
func (s *Scheduler) createJobFunc(job *entity.SysJob) func() {
	return func() {
		s.execute(context.Background(), job, entity.JobLogTriggerCron)
	}
}

// GetHealth 返回调度器健康状态。
func (s *Scheduler) GetHealth() (running bool, nodeID string, totalJobs int, runningJobs int, uptime string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	running = s.started
	nodeID = s.nodeID
	totalJobs = len(s.cronMap)
	runningJobs = len(s.running)
	if s.started {
		uptime = time.Since(s.startAt).Truncate(time.Second).String()
	}
	return
}
