package tasks

import "github.com/tangwy-t/webmanager-server/internal/task"

// Deps 汇集各任务需要的依赖。装配点(wireup)只需提供依赖,
// 任务清单本身在此维护 —— 新增任务时改这个文件,不动 DI 装配代码。
type Deps struct {
	OpLogRepo    DeleteBeforeRepo
	LoginLogRepo DeleteBeforeRepo
	JobLogRepo   DeleteBeforeRepo
	ConfigRepo   ConfigRepoInterface
	DictTypeRepo DictTypeInterface
	DictDataRepo DictDataRepoInterface
	ConfigSvc    ConfigProvider
	CacheStore   HashStoreInterface
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
		NewConfigSyncTask(d.ConfigRepo, d.CacheStore),
		NewDictSyncTask(d.DictTypeRepo, d.DictDataRepo, d.CacheStore),
	}
}
