package tasks

import (
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
)

// Deps 汇集各任务需要的依赖。装配点(wireup)只需提供依赖,
// 任务清单本身在此维护 —— 新增任务时改这个文件,不动 DI 装配代码。
type Deps struct {
	OpLogRepo    DeleteBeforeRepo
	LoginLogRepo DeleteBeforeRepo
	JobLogRepo   DeleteBeforeRepo
	// DockerTaskRepo 是任务中心历史的清理面（8d）：只依赖 DeleteBefore
	// （与三类日志清理共用同一个窄接口，见 interfaces.go）。
	DockerTaskRepo DeleteBeforeRepo
	ConfigRepo     ConfigRepoInterface
	DictTypeRepo   DictTypeInterface
	DictDataRepo   DictDataRepoInterface
	ConfigSvc      ConfigProvider
	CacheStore     HashStoreInterface

	// ─ 设备指标域（Plan 2C）────────────────────────────
	// 三个后台服务的窄接口（消费方定义，见 interfaces.go）。
	// AgentFlush 由 flush 与 backfill 两个任务共用：它们是同一个服务实例的
	// 两个入口（落库 / 回退水位后落库）。
	AgentFlush     AgentFlushService
	AgentRollup    AgentRollupService
	AgentPartition AgentPartitionService
	// AgentUpgrade 供升级巡检任务使用（只读扫描 + 判超时，不含任何下发能力）。
	AgentUpgrade AgentUpgradeSweeper
	// 上传产物回收（docker-transfer-cleanup）的三个能力面：可管主机枚举、指令记录、
	// 下行送达。三者由 dockerstate.Store / dockerstate.CmdStore / agenthub.Hub 满足。
	DockerTransferHosts  DockerTransferHosts
	DockerTransferCmds   DockerTransferCmdStore
	DockerTransferSender DockerTransferSender
	// RequestIDGen 生成指令 ref（装配点传 service.NewRequestID）。
	//
	// 为什么必须共用同一个生成器：ref 与用户指令落在同一个键空间
	//（`docker:cmd:<ref>`），两个独立的计数器会把「同一纳秒、两计数器等值」的
	// 撞号窗口重新引进来 —— 撞号即「一条指令的记录被另一条覆盖」。
	// nil = 用本包内置的降级实现（测试与零值 Deps{})。
	RequestIDGen func() string
	// Log 供 4 个指标任务记录结构化读数（zap 字段）。允许为 nil：任务构造时
	// 退化成 logger.NewNop()，这样 All(Deps{}) 的零值路径不会 panic。
	Log logger.LoggerInterface
}

// All 返回全部可注册为定时任务的实例。
// 构造依赖集中在 Deps,清单与任务实现同包,消除跨包硬编码列表。
func All(d Deps) []task.Task {
	return []task.Task{
		NewHTTPCallTask(nil),
		NewDemoTask(),
		NewOpLogCleanupTask(d.OpLogRepo, d.ConfigSvc),
		NewLoginLogCleanupTask(d.LoginLogRepo, d.ConfigSvc),
		NewJobLogCleanupTask(d.JobLogRepo, d.ConfigSvc),
		// 任务中心历史清理（8d）：与三类日志清理同一形状（保留天数走配置）。
		NewDockerTaskCleanupTask(d.DockerTaskRepo, d.ConfigSvc),
		NewAgentUpgradePatrolTask(d.AgentUpgrade, d.Log),
		NewConfigSyncTask(d.ConfigRepo, d.CacheStore),
		NewDictSyncTask(d.DictTypeRepo, d.DictDataRepo, d.CacheStore),
		// 设备指标域（Name 必须与 v009 种子的 invoke_target 逐字一致：
		// 脱节会让调度器找不到目标、任务静默不执行）。
		NewAgentMetricsFlushTask(d.AgentFlush, d.Log),
		NewAgentMetricsRollupTask(d.AgentRollup, d.Log),
		NewAgentMetricsBackfillTask(d.AgentFlush, d.Log),
		NewAgentMetricsPartitionTask(d.AgentPartition, d.Log),
		// 上传产物回收（Name 必须与 v020 种子的 invoke_target 逐字一致）：
		// 向全部可管 docker 主机派发 system:transfer-cleanup。
		NewDockerTransferCleanupTask(d.DockerTransferHosts, d.DockerTransferCmds,
			d.DockerTransferSender, d.RequestIDGen, d.Log),
	}
}
