package tasks

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"time"
)

type HashStoreInterface interface {
	HMSet(ctx context.Context, key string, fields map[string]string) error
	HSet(ctx context.Context, key, field, value string) error
}

type ConfigRepoInterface interface {
	FindAllEnabled(ctx context.Context) ([]entity.SysConfig, error)
}

type DictTypeInterface interface {
	FindAllEnabled(ctx context.Context) ([]entity.SysDictType, error)
}

type DictDataRepoInterface interface {
	FindEnabledByTypeID(ctx context.Context, typeID uint64) ([]entity.SysDictData, error)
}

// DeleteBeforeRepo 是三类历史日志清理任务(job/login/operation log)共用的
// 仓储契约:三个任务都只依赖 DeleteBefore 一个方法,合并为单一接口,消除
// 三份同形声明(consumer-side 窄接口,单一方法已是最小面)。
type DeleteBeforeRepo interface {
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// ConfigProvider 配置提供者接口，用于获取配置值。
type ConfigProvider interface {
	GetInt(ctx context.Context, key string, defaultVal int) int
}
