package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     9,
		Description: "初始化设备指标后台任务与分区配置",
		Up:          seedAgentJobs,
	})
}

// agentJobDefinitions 是设备指标域的 4 个内置任务。
//
// **Name 必须与 task/tasks 里任务实例的 Name() 逐字一致**（由守卫测试钉住）——
// 不一致会让调度器按 invoke_target 找不到目标，任务静默不执行。
// 双向守卫：v009_seed_agent_jobs_test.go 的 TestV009InvokeTargetsMatchTaskNames
// 拿本切片的 Invoke 与 tasks.All() 里真实注册的任务名求集合相等。
//
// cron 的分工（写在这里，因为它是运维最常问的一件事）：
//   - flush `0 */5 * * * *`      每 5 分钟落 5min 档（唯一真值来源）；
//   - rollup `30 */5 * * * *`    与 flush **错开半分钟**：两者写同一批设备的
//     不同表（事务 A / 事务 B），同时起跑只会互相抢 DB 连接；
//   - backfill `0 15 * * * *`    每小时 15 分回放一次窗口（缺省 24h）；
//   - partition `0 0 4 * * *`    每天凌晨 4 点做分区对账（DDL 与业务高峰错开）。
//
// 四个任务都**不开机立即跑**：启动期的对齐由 wireup 的 bootstrap reconcile
// 负责（它在 scheduler 构造之前同步跑完），不靠 job 的 run_at_startup 兜底；
// 而 cron 最迟 5 分钟内就会补上第一轮。
var agentJobDefinitions = []jobDef{
	{Name: "设备指标落库", Cron: "0 */5 * * * *", Invoke: "agent-metrics-flush"},
	{Name: "设备指标1h回滚", Cron: "30 */5 * * * *", Invoke: "agent-metrics-rollup"},
	{Name: "设备指标补齐重放", Cron: "0 15 * * * *", Invoke: "agent-metrics-backfill",
		Params: `{"hours":24}`},
	{Name: "设备指标分区维护", Cron: "0 0 4 * * *", Invoke: "agent-metrics-partition"},
}

// agentJobConfigDefinitions 是分区协调器新增的两个配置键（v009 之前没有迁移种过它们）。
//
// 默认值与 service/agent_metrics_partition.go 的常量**同值**：
// defaultPartitionLockTTLSec = 600、defaultPartitionTableTimeoutSec = 60。
// 那两处是未导出的常量（服务侧读不到种子、种子也读不到服务），
// 故由 v009 的测试按字面量交叉钉住。
var agentJobConfigDefinitions = []configDef{
	{Key: "sys.agent.partitionLockTTL", Value: "600", Type: "N", Name: "分区对账锁租约",
		Remark: "分区协调器自有锁的租约(秒)：按一轮对账的上界(6表×单表超时+建表)取值", Enabled: true},
	{Key: "sys.agent.partitionTableTimeout", Value: "60", Type: "N", Name: "分区对账单表超时",
		Remark: "每张指标表一次分区对账的超时(秒)：单表卡住不得拖垮其余 5 张表", Enabled: true},
}

// seedAgentJobs 写入 4 个指标任务与 2 个分区配置。
//
// 与 v007 的种子（`seedJob`，版本化一次性迁移）的**关键差别**：本迁移面对的
// 是「真实库里可能已经存在同名 invoke_target」的库 —— 被手工重跑过、运维自己
// 建过、或历史上某次迁移落了一半。故这里按 **invoke_target 去重**：
//
//   - 已存在同名目标 → **跳过**（既不重复插入，也不覆写）；
//   - 缺失的 → 补齐。
//
// 为什么是「跳过」而不是「先删后插」或 UPSERT：覆写会把运维调过的 cron/状态
// 打回默认值，且这种回退**完全不可观测**（今天把凌晨 4 点的分区维护挪到业务
// 低谷，明天启动被无声改回去）。跳过则让「库里已有的那行」始终是权威。
//
// 软删的同名行（运维主动删掉的任务）**不复活**：查询走 GORM 的默认软删作用域，
// 即「只按**活跃**同名任务去重」—— 删掉的任务本该不再被调度，复活它等于绕过
// 运维的删除动作。（代价：若该任务确实需要，运维得手工重建。这是刻意选择：
// 宁可要一次显式的重建，也不要静默复活一个被删掉的任务。）
func seedAgentJobs(tx *gorm.DB) error {
	for _, def := range agentJobDefinitions {
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
			Concurrent:     util.Ptr[int8](entity.JobConcurrentAllowed),
			Status:         util.Ptr[int8](entity.JobStatusEnabled),
			RunAtStartup:   util.Ptr[int8](entity.JobRunAtStartupNo),
		}
		if def.Params != "" {
			job.InvokeParams = util.Ptr(def.Params)
		}
		if err := tx.Create(&job).Error; err != nil {
			return fmt.Errorf("v009 写入任务 %s: %w", def.Invoke, err)
		}
	}
	return seedAgentJobConfigs(tx)
}

// seedAgentJobConfigs 按 config_key 去重地补齐两个分区配置键。
//
// 与任务同样的理由：真实库里这两个键可能已经被手工补过（值是运维按自己的
// DDL 耗时调过的），种子的存在意义只是「别让它缺失」，不是「让它回到默认值」。
func seedAgentJobConfigs(tx *gorm.DB) error {
	for _, def := range agentJobConfigDefinitions {
		var n int64
		if err := tx.Model(&entity.SysConfig{}).
			Where("config_key = ?", def.Key).Count(&n).Error; err != nil {
			return fmt.Errorf("v009 查询配置 %s: %w", def.Key, err)
		}
		if n > 0 {
			continue
		}
		row := entity.SysConfig{
			Name:        def.Name,
			ConfigKey:   def.Key,
			ConfigValue: def.Value,
			ConfigType:  def.Type,
			Remark:      util.Ptr(def.Remark),
			Status:      util.Ptr[int8](boolToInt8(def.Enabled)),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("v009 写入配置 %s: %w", def.Key, err)
		}
	}
	return nil
}

// jobInvokeTargetExists 判断是否已有**活跃**的同名任务目标。
func jobInvokeTargetExists(tx *gorm.DB, invoke string) (bool, error) {
	var n int64
	if err := tx.Model(&entity.SysJob{}).
		Where("invoke_target = ?", invoke).Count(&n).Error; err != nil {
		return false, fmt.Errorf("v009 查询任务 %s: %w", invoke, err)
	}
	return n > 0, nil
}
