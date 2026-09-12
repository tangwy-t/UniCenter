package scheduler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/pubsub"
	"github.com/tangwy-t/webmanager-server/internal/task"
	"time"
)

type ConfigGetterInterface interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
	GetBool(ctx context.Context, key string, defaultVal bool) bool
}

type JobRepository interface {
	FindAllEnabled(ctx context.Context) ([]entity.SysJob, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysJob, error)
}

type JobLogRepository interface {
	Create(ctx context.Context, log *entity.SysJobLog) error
}

type BrokerInterface interface {
	Subscribe(eventType string, handler pubsub.Handler)
	Publish(ctx context.Context, eventType string, payload any) error
}

type Locker interface {
	TryLock(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key string, owner string) error
}

type TaskInterface interface {
	Get(name string) (task.Task, bool)
}
