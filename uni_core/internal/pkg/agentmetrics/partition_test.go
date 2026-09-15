package agentmetrics

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 14, 7, 30, 0, 0, time.UTC)

func weeklySpec() Spec {
	return Spec{Table: "device_metric_5m", Granularity: GranularityWeek, Retention: 30 * 24 * time.Hour}
}
func monthlySpec() Spec {
	return Spec{Table: "device_metric_1h", Granularity: GranularityMonth, Retention: 180 * 24 * time.Hour}
}

func TestDesiredBoundariesAreStrictlyIncreasing(t *testing.T) {
	for _, spec := range []Spec{weeklySpec(), monthlySpec()} {
		bounds := Desired(spec, testNow, 3)
		if len(bounds) < 4 {
			t.Fatalf("%s: 分区数 = %d, 太少", spec.Table, len(bounds))
		}
		for i := 1; i < len(bounds); i++ {
			if bounds[i].Lower != bounds[i-1].Upper {
				t.Fatalf("%s: 分区 %d/%d 边界不连续: %d vs %d",
					spec.Table, i-1, i, bounds[i-1].Upper, bounds[i].Lower)
			}
			if bounds[i].Upper <= bounds[i-1].Upper {
				t.Fatalf("%s: 上界未严格递增（MySQL 会报 ERROR 1463）", spec.Table)
			}
		}
	}
}

func TestDesiredGuardsBothEnds(t *testing.T) {
	bounds := Desired(weeklySpec(), testNow, 2)
	first, last := bounds[0], bounds[len(bounds)-1]
	if first.Name != GuardPartitionName || first.Lower != 0 {
		t.Fatalf("最左必须是下界守卫分区 p_min 且从 0 开始, got %+v", first)
	}
	if last.Name != SentinelPartitionName || last.Upper <= 0 {
		t.Fatalf("最右必须是上界哨兵分区 p_max, got %+v", last)
	}
}

func TestDesiredCoversRetentionAndHorizon(t *testing.T) {
	// 30d 保留 + 周分区 → 至少覆盖现在往前 30 天，且往前必须多留 1 个周期
	spec := weeklySpec()
	bounds := Desired(spec, testNow, 2)
	oldest := bounds[1] // 跳过 p_min
	if d := testNow.Sub(time.Unix(oldest.Lower, 0)); d < spec.Retention {
		t.Fatalf("最老分区起点距今 %v, 未覆盖保留期 %v", d, spec.Retention)
	}
	// 哨兵之前必须有覆盖未来的分区
	future := bounds[len(bounds)-2]
	if future.Upper <= testNow.Unix() {
		t.Fatalf("必须预建未来分区, got 上界 %d <= now %d", future.Upper, testNow.Unix())
	}
}

func TestWorstCaseRetentionIncludesOnePeriod(t *testing.T) {
	// 这正是 spec §7.3 记录的缺陷：分区粒度决定最坏保留期。
	// 30d 保留 + 周分区 → 最坏 37 天；180d + 月分区 → 最坏 210 天。
	if got := WorstCaseRetention(weeklySpec()); got != 37*24*time.Hour {
		t.Fatalf("周分区最坏保留 = %v, want 37d", got)
	}
	if got := WorstCaseRetention(monthlySpec()); got < 209*24*time.Hour || got > 212*24*time.Hour {
		t.Fatalf("月分区最坏保留 = %v, want ≈210d", got)
	}
}

// boundNames 抽出分区名清单（现状 = 期望清单的构造顺序）。
func boundNames(bounds []Bound) []string {
	out := make([]string, 0, len(bounds))
	for _, b := range bounds {
		out = append(out, b.Name)
	}
	return out
}

// TestPlanFromIsIdempotentWhenNothingMissing 逐日遍历 7 个星期几。
//
// 为什么必须遍历 7 天（评审 F1）：`Desired` 原先用「从 cur 往前
// periodsFor(retention)+1 个周期」定窗口起点，最老的那个期望分区**本身已部分过期**
// （其上界 <= now-保留期），于是 existing == Desired 时协调器仍会输出
// Truncate=[p_2026_w33] —— 周三~周日天天重复下发同一条 TRUNCATE（实测）。
// 修法是把窗口起点改为**保留期截止点所在周期**：cutoff 落在首项分区内部，
// 因此每个期望分区的上界都严格大于 cutoff，任意星期几都幂等。
func TestPlanFromIsIdempotentWhenNothingMissing(t *testing.T) {
	// testNow = 2026-09-14（周一）07:30 UTC；逐日 +1d 覆盖周一→周日。
	for _, spec := range []Spec{weeklySpec(), monthlySpec()} {
		for i := 0; i < 7; i++ {
			now := testNow.AddDate(0, 0, i)
			bounds := Desired(spec, now, 2)
			plan := PlanFrom(spec, boundNames(bounds), now, 2, 2)
			if len(plan.Create) != 0 || len(plan.Truncate) != 0 || len(plan.Drop) != 0 {
				t.Fatalf("%s %s(%s): 现状满足期望时计划必须为空, got %+v",
					spec.Table, now.Format("2006-01-02"), now.Weekday(), plan)
			}
		}
	}
}

// TestPlanFromReclaimsPartitionsThatSlidOutOfWindow 证明「回收仍可达」。
//
// 修 F1 时窗口起点由 now 侧改为 cutoff 侧，窗口整体**前移**了一个周期的量级，
// 但回收路径必须不受影响：滑出窗口的老分区由 §5 的 boundFromName 反解路径负责。
// 这里把 now 推后 5 周，断言：
//  1. 出现了 Truncate 或 Drop；
//  2. 下界守卫 p_min 与上界哨兵 p_max 永不在其中；
//  3. 至少有一个被回收的分区名**不在**新期望窗口里 —— 证明走的是 boundFromName
//     反解，而不是恰好在窗口内的分区被顺带判过期。
func TestPlanFromReclaimsPartitionsThatSlidOutOfWindow(t *testing.T) {
	spec := weeklySpec()
	existing := boundNames(Desired(spec, testNow, 2))

	later := testNow.AddDate(0, 0, 7*5) // +5 周
	plan := PlanFrom(spec, existing, later, 2, 2)
	if len(plan.Truncate)+len(plan.Drop) == 0 {
		t.Fatalf("now 推后 5 周必须有回收动作（保留期形同虚设 = §5 的回归）, 现状=%v", existing)
	}
	for _, name := range append(append([]string{}, plan.Truncate...), plan.Drop...) {
		if name == GuardPartitionName || name == SentinelPartitionName {
			t.Fatalf("守卫分区与哨兵分区永不回收, 却出现在计划里: %s", name)
		}
	}
	inWindow := make(map[string]bool)
	for _, name := range boundNames(Desired(spec, later, 2)) {
		inWindow[name] = true
	}
	outOfWindow := 0
	for _, name := range append(append([]string{}, plan.Truncate...), plan.Drop...) {
		if !inWindow[name] {
			outOfWindow++
		}
	}
	if outOfWindow == 0 {
		t.Fatal("回收必须覆盖已滑出期望窗口的老分区（只能由分区名反解得到），否则只是窗口内误判")
	}
	// 保留边界语义：最老的 hold=2 个只 TRUNCATE（保留分区名继续对账），更老的才 DROP。
	if len(plan.Truncate) != 2 || len(plan.Drop) == 0 {
		t.Fatalf("hold=2 应产出 2 条 Truncate + 若干 Drop, got truncate=%v drop=%v", plan.Truncate, plan.Drop)
	}
}

func TestPlanFromCreatesMissingAndReclaimsExpired(t *testing.T) {
	spec := weeklySpec()
	bounds := Desired(spec, testNow, 2)
	// 现状：缺最后两个分区
	existing := make([]string, 0, len(bounds))
	for i := 0; i < len(bounds)-2; i++ {
		existing = append(existing, bounds[i].Name)
	}
	plan := PlanFrom(spec, existing, testNow, 2, 2)
	if len(plan.Create) != 2 {
		t.Fatalf("应补 2 个分区, got %d (%+v)", len(plan.Create), plan.Create)
	}
	// 回收：把 now 推到很久以后，所有业务分区都过期
	later := testNow.AddDate(2, 0, 0)
	planLater := PlanFrom(spec, existing, later, 2, 2)
	if len(planLater.Truncate)+len(planLater.Drop) == 0 {
		t.Fatal("推到 2 个月后必须有回收动作")
	}
	for _, name := range append(append([]string{}, planLater.Truncate...), planLater.Drop...) {
		if name == GuardPartitionName || name == SentinelPartitionName {
			t.Fatalf("守卫分区与哨兵分区永不回收, 却出现在计划里: %s", name)
		}
	}
}

func TestInlineClausePerDialect(t *testing.T) {
	bounds := Desired(monthlySpec(), testNow, 2)

	mysql, err := InlineClause("mysql", monthlySpec(), bounds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mysql, "PARTITION BY RANGE COLUMNS(bucket_ts)") {
		t.Fatalf("mysql 必须内联分区子句, got %q", mysql)
	}
	if !strings.Contains(mysql, "MAXVALUE") {
		t.Fatal("mysql 分区子句必须含哨兵 MAXVALUE")
	}

	// PG 用声明式分区，父表要 PARTITION BY，但分区是独立子表 → 内联子句为空
	pg, err := InlineClause("postgres", monthlySpec(), bounds)
	if err != nil {
		t.Fatal(err)
	}
	if pg != "" {
		t.Fatalf("postgres 的内联分区子句必须为空（分区走 ChildDDL）, got %q", pg)
	}

	// sqlite 不支持分区 → 降级为普通表（测试栈就是 sqlite，必须能跑）
	lite, err := InlineClause("sqlite", monthlySpec(), bounds)
	if err != nil {
		t.Fatal(err)
	}
	if lite != "" {
		t.Fatalf("sqlite 必须降级为普通表, got %q", lite)
	}
}

func TestChildDDLOnlyForPostgres(t *testing.T) {
	bounds := Desired(monthlySpec(), testNow, 1)
	mysql, _ := ChildDDL("mysql", monthlySpec(), bounds)
	if len(mysql) != 0 {
		t.Fatalf("mysql 不需要子表 DDL, got %d 条", len(mysql))
	}
	pg, err := ChildDDL("postgres", monthlySpec(), bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(pg) != len(bounds) {
		t.Fatalf("postgres 子表 DDL 条数 = %d, want %d", len(pg), len(bounds))
	}
	if !strings.Contains(pg[0], "PARTITION OF "+monthlySpec().Table) {
		t.Fatalf("PG 子表必须是 PARTITION OF 形式: %q", pg[0])
	}
}

func TestAddPartitionDDLUsesReorganizeWhenSentinelExists(t *testing.T) {
	spec := weeklySpec()
	missing := Desired(spec, testNow, 4)[len(Desired(spec, testNow, 4))-2 : len(Desired(spec, testNow, 4))-1]

	withSentinel, err := AddPartitionDDL("mysql", spec, missing, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withSentinel) != 1 || !strings.Contains(withSentinel[0], "REORGANIZE PARTITION "+SentinelPartitionName) {
		t.Fatalf("有哨兵时必须用 REORGANIZE 拆哨兵（MySQL 只能高端追加会报 1463）, got %v", withSentinel)
	}

	without, err := AddPartitionDDL("mysql", spec, missing, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(without) != 1 || !strings.Contains(without[0], "ADD PARTITION") {
		t.Fatalf("无哨兵时应 ADD PARTITION, got %v", without)
	}
}

func TestReclaimDDLDistinguishesTruncateAndDrop(t *testing.T) {
	spec := weeklySpec()
	stmts, err := ReclaimDDL("mysql", spec, []string{"p_2026_w20"}, []string{"p_2026_w10"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, "TRUNCATE PARTITION p_2026_w20") {
		t.Fatalf("Truncate 必须生成 TRUNCATE PARTITION: %v", stmts)
	}
	if !strings.Contains(joined, "DROP PARTITION p_2026_w10") {
		t.Fatalf("Drop 必须生成 DROP PARTITION: %v", stmts)
	}
}

// TestReclaimDDLRefusesUnboundedDeleteOnFallbackDialect 守卫降级方言（评审 F2）。
//
// 缺陷背景：非 mysql/postgres 方言下 ReclaimDDL 原先生成 `DELETE FROM <表>` ——
// **没有 WHERE**，等价于清空整张指标表（实测 sqlite 分支返回
// `[DELETE FROM device_metric_5m]` 且 err=nil）。spec §7.3 明令
// 「逐行 DELETE 降级等于把本设计作废，不应作为静默兜底」，降级必须走
// 「分块 find-then-delete（DELETE ... LIMIT 1000 循环）」。故这里断言：
// 非空回收请求在降级方言下必须**报错**，且**绝不返回未加限定条件的 DELETE FROM**。
func TestReclaimDDLRefusesUnboundedDeleteOnFallbackDialect(t *testing.T) {
	spec := weeklySpec()
	truncateOnly := []string{"p_2026_w20"}
	dropOnly := []string{"p_2026_w10"}

	for _, dialect := range []string{"sqlite", "", "mssql"} { // "" = 空方言串（未知）
		cases := []struct {
			what     string
			truncate []string
			drop     []string
		}{
			{"truncate 非空", truncateOnly, nil},
			{"drop 非空", nil, dropOnly},
			{"两者都非空", truncateOnly, dropOnly},
		}
		for _, c := range cases {
			stmts, err := ReclaimDDL(dialect, spec, c.truncate, c.drop)
			if err == nil {
				t.Fatalf("方言 %q %s: 必须返回错误（否则会退化成清空整表）, got %v", dialect, c.what, stmts)
			}
			if len(stmts) != 0 {
				t.Fatalf("方言 %q %s: 报错时不得返回任何语句, got %v", dialect, c.what, stmts)
			}
			// 绝不包含未加限定条件的 DELETE FROM（无论出现在哪条语句里）。
			for _, s := range stmts {
				if strings.Contains(strings.ToUpper(s), "DELETE FROM") {
					t.Fatalf("方言 %q: 降级路径不得生成无 WHERE 的 DELETE: %q", dialect, s)
				}
			}
		}
		// 无可回收时仍是合法的空操作（降级方言下协调器不该因为「没事可做」而报错）。
		stmts, err := ReclaimDDL(dialect, spec, nil, nil)
		if err != nil || len(stmts) != 0 {
			t.Fatalf("方言 %q 无回收请求时应为空操作, got stmts=%v err=%v", dialect, stmts, err)
		}
	}

	// mysql / postgres 分支一字未动（回归）。
	my, err := ReclaimDDL("mysql", spec, truncateOnly, dropOnly)
	if err != nil || len(my) != 2 {
		t.Fatalf("mysql 分区回收不受影响: stmts=%v err=%v", my, err)
	}
	pg, err := ReclaimDDL("postgres", spec, truncateOnly, dropOnly)
	if err != nil || len(pg) != 2 {
		t.Fatalf("postgres 分支不受影响: stmts=%v err=%v", pg, err)
	}
}

func TestTableSpecsCoversSixTablesWithExpectedGranularity(t *testing.T) {
	specs := TableSpecs()
	if len(specs) != 6 {
		t.Fatalf("指标表应为 6 张, got %d", len(specs))
	}
	for _, s := range specs {
		if s.Table == "" || s.Retention <= 0 {
			t.Fatalf("规格不完整: %+v", s)
		}
		if s.Table == "device_metric_1h" {
			if s.Granularity != GranularityMonth || s.Retention != 180*24*time.Hour {
				t.Fatalf("1h 表应为月分区/180d, got %+v", s)
			}
			continue
		}
		if s.Granularity != GranularityWeek || s.Retention != 30*24*time.Hour {
			t.Fatalf("%s 应为周分区/30d（月粒度会把最坏保留拉到 62 天）, got %+v", s.Table, s)
		}
	}
}
