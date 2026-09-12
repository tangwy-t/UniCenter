package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/robfig/cron/v3"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

type stubJobRepo struct {
	byID    *entity.SysJob
	byIDErr error
}

func (s *stubJobRepo) FindAllEnabled(context.Context) ([]entity.SysJob, error) { return nil, nil }
func (s *stubJobRepo) FindByID(context.Context, uint64) (*entity.SysJob, error) {
	return s.byID, s.byIDErr
}

func enabledJob(id uint64) *entity.SysJob {
	status := entity.JobStatusEnabled
	return &entity.SysJob{BaseEntity: entity.BaseEntity{ID: id}, Status: &status, CronExpression: "*/5 * * * * *"}
}

func newTestScheduler(repo JobRepository) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		cronMap:  make(map[uint64]cron.EntryID),
		jobCrons: make(map[uint64]string),
		logger:   logger.NewNop(),
		repo:     repo,
	}
}

// TestHandleJobChangedAdd enabled 任务 add 后应注册进 cronMap。
func TestHandleJobChangedAdd(t *testing.T) {
	s := newTestScheduler(&stubJobRepo{byID: enabledJob(9)})
	s.handleJobChanged(jobChangedMsg{Action: "add", ID: 9})

	s.mu.RLock()
	_, ok := s.cronMap[9]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("add 后任务未进入 cronMap")
	}
}

// TestHandleJobChangedAddFindError repo 查询失败不应注册任何任务。
func TestHandleJobChangedAddFindError(t *testing.T) {
	s := newTestScheduler(&stubJobRepo{byIDErr: errors.New("boom")})
	s.handleJobChanged(jobChangedMsg{Action: "add", ID: 9})

	s.mu.RLock()
	n := len(s.cronMap)
	s.mu.RUnlock()
	if n != 0 {
		t.Fatalf("FindByID 失败后 cronMap 应为空, got %d", n)
	}
}

// TestHandleJobChangedRemove remove 后任务从 cronMap 移除。
func TestHandleJobChangedRemove(t *testing.T) {
	s := newTestScheduler(&stubJobRepo{})
	s.Add(enabledJob(9))
	s.handleJobChanged(jobChangedMsg{Action: "remove", ID: 9})

	s.mu.RLock()
	_, ok := s.cronMap[9]
	s.mu.RUnlock()
	if ok {
		t.Fatal("remove 后任务仍在 cronMap")
	}
}

// TestHandleJobChangedUnknown 未知动作只告警,不影响既有注册。
func TestHandleJobChangedUnknown(t *testing.T) {
	s := newTestScheduler(&stubJobRepo{})
	s.Add(enabledJob(9))
	s.handleJobChanged(jobChangedMsg{Action: "mystery", ID: 9})

	s.mu.RLock()
	_, ok := s.cronMap[9]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("未知动作不应移除既有任务")
	}
}
