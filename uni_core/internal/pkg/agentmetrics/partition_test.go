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

func TestPlanFromIsIdempotentWhenNothingMissing(t *testing.T) {
	spec := weeklySpec()
	bounds := Desired(spec, testNow, 2)
	existing := make([]string, 0, len(bounds))
	for _, b := range bounds {
		existing = append(existing, b.Name)
	}
	plan := PlanFrom(spec, existing, testNow, 2, 2)
	if len(plan.Create) != 0 || len(plan.Truncate) != 0 || len(plan.Drop) != 0 {
		t.Fatalf("现状满足期望时计划必须为空, got %+v", plan)
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
