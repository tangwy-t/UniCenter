package tasks

import (
	"context"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
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

// AgentFlushService 是 flush（落库）与 backfill（回溯重放）两个任务共用的窄接口。
//
// 为什么两个任务共用一个接口：它们消费的是**同一个服务实例**的两个入口
// （落库 / 回退水位后落库），拆成两份只会让 wireup 把同一个实例填两次。
// 方法只列任务真正会调的四个 —— 消费方定义接口是仓库既有约定（同 DeleteBeforeRepo）。
type AgentFlushService interface {
	// Bootstrap 只为**缺失**的水位写起点（now−raw 保留期），不覆写已有水位。
	Bootstrap(ctx context.Context) error
	// FlushOnce 跑一轮全量落库（只向前推进水位）。
	FlushOnce(ctx context.Context) (service.FlushStats, error)
	// BackfillOnce 先把窗口内的 5m 水位回退到 now−hours，再跑一轮全量落库。
	BackfillOnce(ctx context.Context, deviceIDs []uint64, hours int) (service.BackfillStats, error)
	// RewindHours 只回退**指定档位**的水位（不重放）：resolution=1h 时把 cursor_1h
	// 退到 now−hours，被退回的那段小时由**下一轮 rollup** 重算补出 ——
	// 1h 的写路径只有 rollup 一条（flush 服务上没有能写 1h 行的方法）。
	RewindHours(ctx context.Context, deviceIDs []uint64, resolution agentmetrics.Resolution,
		hours int) (service.RewindStats, error)
}

// AgentRollupService 是 rollup（5m → 1h 回滚 + repair 重算）任务的窄接口。
type AgentRollupService interface {
	RollupOnce(ctx context.Context) (service.RollupStats, error)
}

// AgentPartitionService 是 partition（6 张指标表的分区对账）任务的窄接口。
type AgentPartitionService interface {
	Reconcile(ctx context.Context) (service.PartitionStats, error)
}

// AgentUpgradeSweeper 是升级巡检需要的窄接口（由 service.DeviceUpgradeService 实现）。
//
// 只暴露「扫一遍」这一个方法：巡检任务不该看得见下发的任何能力 ——
// 一个只会巡检的任务，不可能因为代码写错而把设备升了。
type AgentUpgradeSweeper interface {
	SweepStale(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}
