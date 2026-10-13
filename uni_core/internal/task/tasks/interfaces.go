package tasks

import (
	"context"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
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

// ── 上传产物回收（docker-transfer-cleanup）的三个能力面 ─────────────────────
//
// 三份窄接口而不是一份「大 docker 面」：任务真正要的只有「枚举可管主机」「建/读一条
// 指令记录」「把指令送出去」这三件事（与 dockerevents 常驻管理器的分解同型）——
// 它拿不到下发面的其余能力（受理校验、审计、流会话），也就不可能因为代码写错而
// 从这条路上做出别的动作。三者的实现分别是 dockerstate.Store / dockerstate.CmdStore
// / agenthub.Hub（装配见 wireup）。

// DockerTransferHosts 是可管 docker 主机的枚举面（由 dockerstate.Store 满足）。
//
// Hosts 给「有哪些主机报过快照」的集合，Get 给单台的快照信封。
// **判据是「这台报过快照」而不是快照里的 docker_ok**：本动作不碰 daemon（agent 侧
// 只是 ReadDir + unlink），而它要救的包括「daemon 挂了 + 盘被产物占满」那种局面 ——
// 用 daemon 健康当闸会把最需要回收的那台挡在门外（事件流常驻订阅用 docker_ok 是因为
// 它真的在订 daemon，两者不是同一件事）。
type DockerTransferHosts interface {
	Hosts(ctx context.Context) ([]uint64, error)
	Get(ctx context.Context, deviceID uint64) (*dockerstate.Envelope, error)
}

// DockerTransferCmdStore 是指令记录面（由 dockerstate.CmdStore 满足）。
//
// 两个方法各有不可替代的理由：
//   - Create：**必须建记录**——agent 的 result 回写只认「记录存在」的 ref
//     （不建记录的 result 会被当「过期或伪造」丢掉），而结果正是本任务唯一的战果来源；
//   - Get：收本轮的终态结果（逐台 succeeded/failed/timeout，见任务里的收结果一段）。
type DockerTransferCmdStore interface {
	Create(ctx context.Context, rec *dockerstate.CmdRecord, timeout time.Duration) error
	Get(ctx context.Context, ref string) (*dockerstate.CmdRecord, error)
}

// DockerTransferSender 是下行送达面（由 agenthub.Hub 满足）。
// ErrDeviceOffline 是**逐台**结论，不是整轮的结论（见任务里的错误处置口径）。
type DockerTransferSender interface {
	SendToDevice(deviceID uint64, msg *agentproto.Message) error
}
