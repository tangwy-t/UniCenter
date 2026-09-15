package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
)

func init() {
	migration.Register(migration.Migration{
		Version:     10,
		Description: "修正设备指标补齐重放的 cron（与 flush 同分钟触发会导致水位回退被覆写）",
		Up:          correctAgentJobCrons,
	})
}

// ─ 为什么是 v010 而不是回头改 v009 ────────────────────────────
//
// 本仓库的迁移只前进（见 pkg/migration 包注释：**永不回头修改已发布的迁移文件**），
// 但「v009 是否已发布」其实**不是**这次的决定性依据 —— 真正的依据是**改动是否有效**：
//
//  1. v009 的 seedAgentJobs 对**已存在的同名 invoke_target 是「跳过」**（刻意的：
//     覆写会把运维调过的 cron 打回默认值，且不可观测，见 v009 的注释与
//     TestV009SeedPreservesExistingJobRow）。因此**改 v009 的 cron 字面量**对
//     「库里已经有那一行」的库没有任何效果 —— 而那恰恰是唯一有缺陷的库：
//     缺陷在 sys_job 的**数据**里，不在代码里。
//  2. 对「已经跑过 v009」的库（开发库、CI 库、任何一台起过本分支二进制的机器），
//     版本账本会让 v009 不再执行，改它更是彻底无效 —— 这与 v007 注释里
//     对 v007 的判断同源（「那个迁移在真实库上早已执行过，改它的行为没有任何效果」）。
//  3. 反向不成立：v010 对「还没跑过 v009」的全新库一样正确 —— v009 先种下旧值、
//     紧随其后的 v010 立刻把它修正到新值（同一次 migration.Run 内、按版本升序）。
//     所以 v010 在所有库上都得到同一个终态，而改 v009 只在全新库上有效。
//  4. 于是 v009 的字面量与它的注释保持原样（历史记录），cron 的**真值**由本文件
//     与 sys_job 表共同表达；两者不一致是有意的，并被 v010 的守卫测试钉住。
//
// ─ 修正的值与「必须错开分钟」这条不变量 ──────────────────────
//
// v009 把 backfill 种在 `0 15 * * * *`，而 flush 是 `0 */5 * * * *`（分钟 ∈ {0,5,…,55}）
// —— **分钟 15 同时命中两者**，于是每天 24 次在 :15:00 整同时唤醒：
//
//	flush 轮读到轮初水位 H → backfill 把水位回退到 T → flush 轮结束把水位写回高位。
//
// 修复分两层（缺一不可）：① 水位写入改为原子比较-写（agentmetrics.CursorStore），
// 让「本轮读到的水位被别人动过」这件事**可被检测**，flush 因此让位、不回写；
// ② 本迁移把 backfill 挪出 flush/rollup 的分钟，把并发窗口从「每 5 分钟必然出现」
// 降到「只有长轮次跨过 :17 时才会重叠」，而那种重叠已由 ① 兜住。
//
// 不变量（由 v010 的守卫测试逐条断言）：**游标族三个任务（flush/rollup/backfill）
// 不得在同一 (分钟, 秒) 上触发**，且 backfill 的分钟**不得是 5 的倍数**
// （5 的倍数 = flush 与 rollup 的分钟集合）。
//
// 为什么取 17 而不是「顺手 +5 到 20」：分钟 20 **仍然是 flush/rollup 的分钟**
// （`0 */5` 覆盖 0,5,…,20,…，rollup 还会在 :20:30 再起一轮）——
// `0 20 * * * *` 与 `0 15 * * * *` 是同一种碰撞，只是换了个分钟。
// 17 % 5 == 2，落在两个 flush 轮之间，且与 rollup 的 :15:30 / :20:30 都不重合。
//
// partition（`0 0 4 * * *`）**不参与**这条不变量：它的分钟 0 确实与 flush 重合，
// 但分区 DDL 与水位游标无关，spec §7.3 给它的规则是「维护紧随一轮 flush 之后」，
// 与「错开分钟」是另一条取向（见 §7.3 的 MDL 排队分析）。把它一并纳入只会制造
// 一条与设计相冲突的断言。
const (
	// backfillInvokeTarget 是被修正的任务（必须与 task/tasks 的 Name() 逐字一致，
	// 与 v009 的 invoke_target 同源）。
	backfillInvokeTarget = "agent-metrics-backfill"
	// backfillCronSeededByV009 是 v009 种下的**原始**（有缺陷的）字面量：
	// 只有**恰好等于它**的行才会被本迁移改写。
	backfillCronSeededByV009 = "0 15 * * * *"
	// backfillCronPaced 是修正后的值：分钟 17 ∉ {0,5,10,15,20,…}。
	backfillCronPaced = "0 17 * * * *"
)

// cronCorrection 是一条「把某个任务从**已知有缺陷的种子值**改到新值」的修正。
//
// 三个字段都要显式写出来（而不是只写 invoke + 新值）：`from` 是防御性的比较条件，
// 它决定了本迁移**只碰**那个已知有缺陷的值。把它藏在实现里会让「为什么这里有个
// 字面量」变成需要考古的事。
type cronCorrection struct {
	invoke string
	from   string
	to     string
}

// v010CronCorrections 是修正清单的唯一来源。
//
// 用切片而不是 map：迁移的执行顺序必须是**确定的**（与日志、错误定位、
// 「先看哪一条失败了」有关），map 的随机遍历顺序会让同一段代码的失败顺序每次都不同。
// 守卫测试拿它与 v009 的定义合成「库上最终生效的 cron」，再对四个任务的
// (分钟, 秒) 做错开性断言。
var v010CronCorrections = []cronCorrection{
	{invoke: backfillInvokeTarget, from: backfillCronSeededByV009, to: backfillCronPaced},
}

// correctAgentJobCrons 只把「**恰好还是 v009 种下的那个值**」的 backfill 行改到新值。
//
// 为什么必须带 `cron_expression = 旧值` 这个条件（而不是无条件按 invoke_target 更新）：
// 与 v009「已存在同名目标就跳过」的取向完全一致 —— 运维手工调过的 cron（例如把回放挪到
// 业务低谷）是**权威**，被迁移静默打回默认值是不可观测的破坏。只有那个**已知有缺陷的
// 种子值**才该被修正：它同时命中 flush 的分钟，且运维没有任何理由刻意选它。
//
// 软删的同名行**不动**（GORM 的默认软删作用域）：被运维删掉的任务本该不再被调度，
// 改它的 cron 只会制造「一条被删的行看起来被迁移维护过」的假象（同 v009 的取向）。
func correctAgentJobCrons(tx *gorm.DB) error {
	for _, c := range v010CronCorrections {
		res := tx.Model(&entity.SysJob{}).
			Where("invoke_target = ? AND cron_expression = ?", c.invoke, c.from).
			Update("cron_expression", c.to)
		if res.Error != nil {
			return fmt.Errorf("v010 修正任务 %s 的 cron: %w", c.invoke, res.Error)
		}
	}
	return nil
}
