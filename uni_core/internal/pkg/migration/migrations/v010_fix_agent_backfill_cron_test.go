package migrations

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
)

// ── cron 解析（测试侧独立实现，不复用生产代码）────────────────

// cronSlots 把 6 字段 cron（秒 分 时 日 月 周）的「秒」「分」两个字段展开成
// 一天的 (分, 秒) 触发点集合；同时断言「时」字段是 `*`（本守卫只比较分钟+秒，
// 小时固定值会让这个比较失去意义，故直接失败）。
//
// 只支持三种字段形态：`*`、`*/N`、固定值 —— 种子里恰好只用这三种。
// 遇到别的形态（`a-b`、列举、`a/N`）直接 Fatal：静默当成 0 会让「错开」这条断言
// 在种子改形态时悄悄失去意义。
func cronSlots(t *testing.T, expr string) map[[2]int]bool {
	t.Helper()
	parts := strings.Fields(expr)
	if len(parts) != 6 {
		t.Fatalf("cron %q 不是 6 字段（秒 分 时 日 月 周）", expr)
	}
	if parts[2] != "*" {
		t.Fatalf("cron %q 的「时」字段 = %q，本守卫要求 *: 只比较「分+秒」的前提是"+
			"任务每天每个小时都按同一分钟触发", expr, parts[2])
	}
	expand := func(field, name string) []int {
		if field == "*" {
			out := make([]int, 0, 60)
			for i := 0; i < 60; i++ {
				out = append(out, i)
			}
			return out
		}
		if strings.HasPrefix(field, "*/") {
			step := atoiField(t, expr, name, strings.TrimPrefix(field, "*/"))
			if step <= 0 || step > 60 {
				t.Fatalf("cron %q 的%s字段步长 = %d 非法", expr, name, step)
			}
			out := make([]int, 0, 60/step+1)
			for i := 0; i < 60; i += step {
				out = append(out, i)
			}
			return out
		}
		return []int{atoiField(t, expr, name, field)}
	}
	mins := expand(parts[1], "分")
	secs := expand(parts[0], "秒")
	out := make(map[[2]int]bool, len(mins)*len(secs))
	for _, m := range mins {
		if m > 59 {
			t.Fatalf("cron %q 的分 = %d 越界", expr, m)
		}
		for _, s := range secs {
			if s > 59 {
				t.Fatalf("cron %q 的秒 = %d 越界", expr, s)
			}
			out[[2]int{m, s}] = true
		}
	}
	return out
}

func atoiField(t *testing.T, expr, name, field string) int {
	t.Helper()
	if field == "" {
		t.Fatalf("cron %q 的%s字段为空", expr, name)
	}
	n := 0
	for _, ch := range field {
		if ch < '0' || ch > '9' {
			t.Fatalf("cron %q 的%s字段 %q 含非数字（本守卫只支持 * / */N / 固定值）", expr, name, field)
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// slotsOf 把「(分,秒) 集合」写成可读的列表（断言失败时用）。
func slotsOf(slots map[[2]int]bool) string {
	out := make([]string, 0, len(slots))
	for m := 0; m < 60; m++ {
		for s := 0; s < 60; s++ {
			if slots[[2]int{m, s}] {
				out = append(out, "分="+itoaSmall(m)+" 秒="+itoaSmall(s))
			}
		}
	}
	return strings.Join(out, "、")
}

// effectiveAgentJobCrons 合成「库上最终生效的 cron」= v009 的定义 + v010 的修正。
//
// 为什么要有这一步：v009 种下的 backfill cron 是**有缺陷的旧值**，而修正落在 v010；
// 只读 v009 的定义（或只读 v010）都只能看到一半的真相。
func effectiveAgentJobCrons() map[string]string {
	out := make(map[string]string, len(agentJobDefinitions))
	for _, j := range agentJobDefinitions {
		out[j.Invoke] = j.Cron
	}
	for _, c := range v010CronCorrections {
		if out[c.invoke] == c.from {
			out[c.invoke] = c.to
		}
	}
	return out
}

// TestV010IsRegistered 钉住版本注册（重复版本会在 Register 里 panic，
// 漏注册则整条修正静默不执行）。
func TestV010IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 10 {
			found = true
			if m.Up == nil {
				t.Fatal("v010 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v010 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

// TestV010BackfillCronIsOffsetFromCursorFamily 是「四个任务必须错开分钟」这条不变量的守卫。
//
// 不变量（精确表述，不是「四个都不同分钟」这种过度简化）：
//  1. **游标族**三个任务（flush / rollup / backfill）的 (分钟, 秒) 两两不同
//     —— flush 与 rollup 刻意共享分钟、差 30 秒（两者写不同表，错开半分钟只为不抢连接）；
//  2. **backfill 的分钟不得是 5 的倍数**，因为分钟 ∈ {0,5,…,55} 正是 flush 与 rollup
//     的分钟集合：撞上就意味着「水位回退」与「水位推进」会在同一秒并发
//     （缺陷本体：flush 用轮初旧游标把回退覆盖回去）。
//
// partition 不参与：它的 DDL 与游标无关，spec §7.3 给它的规则是「紧随一轮 flush 之后」，
// 与「错开分钟」是不同取向（分钟 0 重合是设计选择）。
func TestV010BackfillCronIsOffsetFromCursorFamily(t *testing.T) {
	crons := effectiveAgentJobCrons()

	// 游标族三个任务的 invoke_target 用**字面量**写（不复用 v009 的定义）：
	// 本守卫要独立复核「这三个任务分别是谁」，复用定义会让改名/写错也自洽。
	cursorFamily := []string{"agent-metrics-flush", "agent-metrics-rollup", backfillInvokeTarget}

	slots := map[string]map[[2]int]bool{}
	for _, invoke := range cursorFamily {
		expr, ok := crons[invoke]
		if !ok {
			t.Fatalf("游标族任务 %s 不在种子清单里", invoke)
		}
		slots[invoke] = cronSlots(t, expr)
	}
	// ① 游标族两两不得撞在同一个 (分, 秒) 上。
	// flush 与 rollup 刻意共享分钟、差 30 秒（写不同表，错开半分钟只为不抢连接），
	// 故「分钟不同」不是不变量，**触发时刻不同**才是。
	for i := 0; i < len(cursorFamily); i++ {
		for j := i + 1; j < len(cursorFamily); j++ {
			a, b := cursorFamily[i], cursorFamily[j]
			for slot := range slots[a] {
				if slots[b][slot] {
					t.Fatalf("游标族任务在同一时刻触发（分=%d 秒=%d）：%s(%s) 与 %s(%s) —— "+
						"水位回退（backfill）与水位推进（flush/rollup）会在同一秒并发",
						slot[0], slot[1], a, crons[a], b, crons[b])
				}
			}
		}
	}
	// ② backfill 的分钟集合与 flush/rollup 的分钟集合**完全不相交**
	//（比 ① 更强，也是本迁移存在的理由：把并发窗口从「每 5 分钟必然出现」降下来）。
	backfillMins := map[int]bool{}
	for slot := range slots[backfillInvokeTarget] {
		backfillMins[slot[0]] = true
	}
	for _, other := range []string{"agent-metrics-flush", "agent-metrics-rollup"} {
		for slot := range slots[other] {
			if backfillMins[slot[0]] {
				t.Fatalf("backfill(%s) 的分钟 %d 落在 %s(%s) 的分钟集合里 —— "+
					"两者会同时起跑（缺陷本体：flush 用轮初旧游标把 backfill 的回退覆写回去）",
					crons[backfillInvokeTarget], slot[0], other, crons[other])
			}
		}
	}
	// ③ backfill 的秒必须固定在 0（整分钟触发，便于运维对齐日志）。
	for slot := range slots[backfillInvokeTarget] {
		if slot[1] != 0 {
			t.Fatalf("backfill(%s) 的秒 = %d, want 0", crons[backfillInvokeTarget], slot[1])
		}
	}
	if got := slots[backfillInvokeTarget]; len(got) != 1 {
		t.Fatalf("backfill 每天应只在唯一的 (分,秒) 上触发，实际 %d 个：%s",
			len(got), slotsOf(got))
	}

	// 修正必须是**真的改了值**：若 v010 的 to 恰好等于 from（复制粘贴写错），
	// 上面那些断言仍然全绿，而缺陷原封不动。
	for _, c := range v010CronCorrections {
		if c.from == c.to {
			t.Fatalf("v010 对 %s 的修正没有改变任何东西（from == to == %q）", c.invoke, c.to)
		}
		if got := crons[c.invoke]; got != c.to {
			t.Fatalf("%s 的生效 cron = %q, want %q", c.invoke, got, c.to)
		}
	}

	// 被修正的旧值必须**确实是** v009 种下的那个（否则 v010 的 WHERE 条件永远不命中，
	// 修正静默无效 —— 这是最容易发生且最难发现的写法错误）。
	if seeded := v009CronOf(t, backfillInvokeTarget); seeded != backfillCronSeededByV009 {
		t.Fatalf("v009 里 %s 的 cron = %q，而 v010 认的旧值是 %q —— "+
			"两者不一致会让修正条件永不命中", backfillInvokeTarget, seeded, backfillCronSeededByV009)
	}
}

// v009CronOf 从 v009 的种子定义里取某个任务的 cron（v009 的清单是唯一来源）。
func v009CronOf(t *testing.T, invoke string) string {
	t.Helper()
	for _, j := range agentJobDefinitions {
		if j.Invoke == invoke {
			return j.Cron
		}
	}
	t.Fatalf("v009 的种子里没有 %s", invoke)
	return ""
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ── 修正的落库行为 ──────────────────────────────────────────

// seedV009ThenV010 在测试库里按真实顺序跑 v009、再跑 v010（不经 runner，
// 便于展示「v009 先种下旧值」这一步）。
func seedV009ThenV010(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("v009 seedAgentJobs: %v", err)
	}
	if err := correctAgentJobCrons(db); err != nil {
		t.Fatalf("v010 correctAgentJobCrons: %v", err)
	}
}

// TestV010CorrectsSeededBackfillCron 钉住修正的**主路径**：
// v009 种下的旧值必须被改成新值，且只有那一行被碰。
func TestV010CorrectsSeededBackfillCron(t *testing.T) {
	db := newAgentSeedTestDB(t)
	seedV009ThenV010(t, db)

	got := readJobs(t, db)
	if len(got) != len(agentJobDefinitions) {
		t.Fatalf("任务数 = %d, want %d（v010 只改 cron，不增删行）", len(got), len(agentJobDefinitions))
	}
	row := got[backfillInvokeTarget]
	if row.CronExpression != backfillCronPaced {
		t.Fatalf("%s 的 cron = %q, want %q（v009 种下的碰撞值必须被修正）",
			backfillInvokeTarget, row.CronExpression, backfillCronPaced)
	}
	// 其余三个任务一行都不许改（尤其是 flush：它才是被碰撞的那一方，
	// 把 flush 挪走会改变「每 5 分钟一轮」这条对外承诺）。
	unchanged := map[string]string{
		"agent-metrics-flush":     "0 */5 * * * *",
		"agent-metrics-rollup":    "30 */5 * * * *",
		"agent-metrics-partition": "0 0 4 * * *",
	}
	for invoke, want := range unchanged {
		if got[invoke].CronExpression != want {
			t.Fatalf("%s 的 cron = %q, want %q（v010 不得顺手改动其它任务）",
				invoke, got[invoke].CronExpression, want)
		}
	}
}

// TestV010PreservesOperatorCustomizedCron 钉住「只改已知有缺陷的种子值」。
//
// 运维手工调过的 cron（例如把回放挪到业务低谷）是权威：迁移把它打回默认值是不可观测的
// 破坏，且与 v009「已存在同名目标就跳过」的取向直接冲突。
func TestV010PreservesOperatorCustomizedCron(t *testing.T) {
	db := newAgentSeedTestDB(t)
	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("v009 seedAgentJobs: %v", err)
	}

	// 运维把 backfill 挪到凌晨 5 点（一个**不是** v009 种子值的值）。
	if err := db.Model(&entity.SysJob{}).
		Where("invoke_target = ?", backfillInvokeTarget).
		Update("cron_expression", "0 0 5 * * *").Error; err != nil {
		t.Fatalf("模拟运维改 cron: %v", err)
	}

	if err := correctAgentJobCrons(db); err != nil {
		t.Fatalf("v010 correctAgentJobCrons: %v", err)
	}
	if got := readJobs(t, db)[backfillInvokeTarget].CronExpression; got != "0 0 5 * * *" {
		t.Fatalf("运维调过的 cron 被 v010 覆写成 %q —— 迁移只该修正**已知有缺陷的种子值**", got)
	}
}

// TestV010DoesNotReviveSoftDeletedRow 钉住软删行不被维护：
// 被运维删掉的任务本该不再被调度，改它的 cron 只会制造「被删的行也有人维护」的假象。
func TestV010DoesNotReviveSoftDeletedRow(t *testing.T) {
	db := newAgentSeedTestDB(t)
	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("v009 seedAgentJobs: %v", err)
	}
	if err := db.Where("invoke_target = ?", backfillInvokeTarget).Delete(&entity.SysJob{}).Error; err != nil {
		t.Fatalf("软删 backfill: %v", err)
	}

	if err := correctAgentJobCrons(db); err != nil {
		t.Fatalf("v010 correctAgentJobCrons: %v", err)
	}
	var row entity.SysJob
	if err := db.Unscoped().Where("invoke_target = ?", backfillInvokeTarget).First(&row).Error; err != nil {
		t.Fatalf("读回软删行: %v", err)
	}
	if row.CronExpression != backfillCronSeededByV009 {
		t.Fatalf("软删行的 cron 被改成 %q：v010 不得碰被删掉的行", row.CronExpression)
	}
}

// TestV010IsIdempotent 钉住重跑幂等：第二次执行影响 0 行（旧值已经不存在了）。
func TestV010IsIdempotent(t *testing.T) {
	db := newAgentSeedTestDB(t)
	seedV009ThenV010(t, db)
	first := readJobs(t, db)

	if err := correctAgentJobCrons(db); err != nil {
		t.Fatalf("二次 correctAgentJobCrons: %v", err)
	}
	second := readJobs(t, db)
	if len(second) != len(first) {
		t.Fatalf("二次修正后任务数 = %d, want %d", len(second), len(first))
	}
	for target, before := range first {
		after := second[target]
		if after.ID != before.ID || after.CronExpression != before.CronExpression {
			t.Fatalf("%s 在重跑后被改动（id %d→%d, cron %q→%q）",
				target, before.ID, after.ID, before.CronExpression, after.CronExpression)
		}
	}
	if got := second[backfillInvokeTarget].CronExpression; got != backfillCronPaced {
		t.Fatalf("重跑后 %s 的 cron = %q, want %q", backfillInvokeTarget, got, backfillCronPaced)
	}
}

// TestV010RunsThroughMigrationRunner 走真实执行路径：migration.Run 会按版本账本
// 依次跑 v009、v010，且 sys_job 的终态必须是「修正后的 cron」。
//
// 为什么不能只测「裸跑两个 Up」：那样测不出「v010 的版本号/注册顺序是否让它在 v009
// 之后执行」—— 而顺序错了（例如版本号写成 8）会让 v010 在 v009 之前跑，
// 于是它改不到任何行，缺陷静默保留。
func TestV010RunsThroughMigrationRunner(t *testing.T) {
	db := newAgentSeedTestDB(t)

	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("migration.Run: %v", err)
	}
	got := readJobs(t, db)
	if len(got) != len(jobDefinitions)+len(agentJobDefinitions) {
		t.Fatalf("sys_job 总数 = %d, want %d", len(got), len(jobDefinitions)+len(agentJobDefinitions))
	}
	if got[backfillInvokeTarget].CronExpression != backfillCronPaced {
		t.Fatalf("runner 跑完后 %s 的 cron = %q, want %q（v010 必须在 v009 之后执行）",
			backfillInvokeTarget, got[backfillInvokeTarget].CronExpression, backfillCronPaced)
	}

	// 再跑一次 runner：版本账本让 v009/v010 都不再执行，终态一动不动。
	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("二次 migration.Run: %v", err)
	}
	if again := readJobs(t, db)[backfillInvokeTarget].CronExpression; again != backfillCronPaced {
		t.Fatalf("二次 runner 后 cron = %q, want %q", again, backfillCronPaced)
	}
}

// TestV010CorrectionsTargetRealSeededJobs 钉住修正清单与 v009 种子清单的一致性：
// `invoke_target` 写错（或修正的是 v009 没种过的任务）会让修正**永远 0 命中**，
// 而所有「库上终态」断言会以「cron 没改」的形式失败 —— 这条把它变成一条直白的守卫。
func TestV010CorrectionsTargetRealSeededJobs(t *testing.T) {
	if len(v010CronCorrections) == 0 {
		t.Fatal("v010 的修正清单为空：本迁移什么也不做")
	}
	for _, c := range v010CronCorrections {
		if v009CronOf(t, c.invoke) == "" {
			t.Fatalf("v010 的修正清单里有 v009 没种过的 invoke_target: %s", c.invoke)
		}
	}
}
