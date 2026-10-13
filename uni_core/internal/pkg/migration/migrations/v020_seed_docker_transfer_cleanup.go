package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// v020 只为**一个定时任务**而存在：上传产物回收（docker-transfer-cleanup）。
//
// ── 为什么它必须独立成一个迁移（而不是塞进已有版本）───────────────
//
// 开发库早已越过 v019，迁移框架只跑「版本 > 当前最大版本」的迁移 —— 往已执行过的
// 迁移里追加定义对它们**完全无效**（v012/v014/v018 的注释把这条讲透了）。
// 任务种子必须走新版本号。
//
// ── 为什么这个任务不能省 ──────────────────────────────────────
//
// 上传产物（`build-ctx-<会话>.tar.gz`，落 agent 的 transferDir；构建上下文与载入
// 镜像共用同一条通道）此前由**每次上传开头**的那一下清扫回收。载入镜像复用这条通道
// 之后产物可到 GB 级（实测 520MB / 1.6GB / 2.87GB），而「传完就不再传」的机器再也
// 没有任何东西触发清扫 —— 产物会永久占着 agent 的盘。回收从此必须是一件**有自己
// 时刻表的事**：定时任务一律经任务表（sys_job）管理，不留在任何操作路径的暗线上。
func init() {
	migration.Register(migration.Migration{
		Version:     20,
		Description: "新增上传产物回收任务",
		Up:          seedDockerTransferCleanup,
	})
}

// dockerTransferCleanupJobDefinitions 是本迁移唯一的种子。
//
// cron `0 30 4 * * *` = 每天 04:30 一轮。为什么是这个时刻（已有的钟都在整点堆里）：
//
//   - 03:00 是三类日志清理 + 字典同步（四把钟撞在一起）；
//   - 03:30 是任务中心历史清理；
//   - 04:00 是设备指标的分区对账（DDL，最重的一把）；
//   - 04:30 落在这一串之后、上班之前：既避开 DDL 与清理的高峰，又留足「白天要用
//     上传产物之前」的时间差 —— 回收只删**超龄 24h**的产物，与当天的工作窗口
//     本来就不相交，选一个安静的时段只是不让它去和别的钟抢 Redis/DB。
//
// 一天一次足够：限龄是 24h，回收的粒度粗于「天」没有意义（产物不会在 24h 内
// 需要被清两次）；而 agent 侧的磁盘压力本身也是「天」量级的增长。
//
// Name/Invoke 必须与 task/tasks 里任务实例的 Name() **逐字一致**
//（不一致会让调度器按 invoke_target 找不到目标、任务静默不执行）——
// 由本文件的守卫测试钉住（与 v009/v014 同款）。
var dockerTransferCleanupJobDefinitions = []jobDef{
	{Name: "上传产物回收", Cron: "0 30 4 * * *", Invoke: "docker-transfer-cleanup"},
}

// seedDockerTransferCleanup 按 invoke_target 去重地补齐任务（与 v009/v014/v018 同款
// 取向：已存在的行一律跳过 —— 覆写会把运维调过的 cron/状态打回默认值且不可观测）。
func seedDockerTransferCleanup(tx *gorm.DB) error {
	for _, def := range dockerTransferCleanupJobDefinitions {
		exists, err := jobInvokeTargetExists(tx, def.Invoke)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		job := entity.SysJob{
			Name:           def.Name,
			JobGroup:       "system",
			CronExpression: def.Cron,
			InvokeTarget:   def.Invoke,
			// **不允许重入**（照 v014 巡检的口径）：本任务派发后要在窗口内收结果，
			// 一轮最长占用两分钟；上一轮还没跑完就再来一轮，只会对同一批主机重复
			// 下发（回收本身幂等，但白白多一轮派发与记录）。
			Concurrent:   util.Ptr[int8](entity.JobConcurrentDisallowed),
			Status:       util.Ptr[int8](entity.JobStatusEnabled),
			RunAtStartup: util.Ptr[int8](entity.JobRunAtStartupNo),
		}
		if def.Params != "" {
			job.InvokeParams = util.Ptr(def.Params)
		}
		if err := tx.Create(&job).Error; err != nil {
			return fmt.Errorf("v020 写入任务 %s: %w", def.Invoke, err)
		}
	}
	return nil
}
