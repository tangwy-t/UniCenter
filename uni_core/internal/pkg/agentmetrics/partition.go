package agentmetrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// periodStart 返回 t 所在分区周期的 UTC 起点（周分区以周一为起点）。
func periodStart(t time.Time, g Granularity) time.Time {
	t = t.UTC()
	if g == GranularityMonth {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	// Go 的 Weekday 是 Sunday=0；换算成「距离本周一的天数」。
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}

// addPeriod 在周期粒度上做日期加减（用 AddDate 以正确处理月底与闰年）。
func addPeriod(t time.Time, g Granularity, n int) time.Time {
	if g == GranularityMonth {
		return t.AddDate(0, n, 0)
	}
	return t.AddDate(0, 0, 7*n)
}

// PartitionName 返回周期的确定性分区名。名字是**对账键**：协调器只按名字
// 比对存在性，不解析 DB 里的描述串（MySQL 的 RANGE COLUMNS 描述是元组串，解析脆弱）。
func PartitionName(t time.Time, g Granularity) string {
	if g == GranularityMonth {
		return fmt.Sprintf("p_%04d_%02d", t.Year(), int(t.Month()))
	}
	y, w := t.ISOWeek()
	return fmt.Sprintf("p_%04d_w%02d", y, w)
}

// isoWeekStart 返回 ISO 年 year 第 week 周的周一 00:00（UTC）。
func isoWeekStart(year, week int) time.Time {
	// 1 月 4 日必然落在 ISO 第 1 周内（ISO 8601 的规定）。
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7 // 周一 = 0
	return jan4.AddDate(0, 0, -offset+7*(week-1))
}

// boundFromName 是 PartitionName 的逆运算：把确定性分区名反解为时间区间。
//
// 回收判定需要**已滑出期望窗口的老分区**的真实上界，而它们不在 Desired 的结果里，
// 只能由名字反解。解析后用 PartitionName 回写比对（往返一致才认），因此
// 非本粒度命名的分区（人工建的、历史遗留的、周序号越界的）一律返回 ok=false，
// 交给调用方保持不动 —— 绝不擅自回收不认识的分区。
func boundFromName(name string, g Granularity) (Bound, bool) {
	var start time.Time
	if g == GranularityMonth {
		var y, m int
		if n, err := fmt.Sscanf(name, "p_%d_%d", &y, &m); err != nil || n != 2 || m < 1 || m > 12 {
			return Bound{}, false
		}
		start = time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
	} else {
		var y, w int
		if n, err := fmt.Sscanf(name, "p_%d_w%d", &y, &w); err != nil || n != 2 || w < 1 || w > 53 {
			return Bound{}, false
		}
		start = isoWeekStart(y, w)
	}
	if PartitionName(start, g) != name {
		return Bound{}, false // 往返不一致：不是本包生成的名字
	}
	return Bound{Name: name, Lower: start.Unix(), Upper: addPeriod(start, g, 1).Unix()}, true
}

// periodsFor 返回覆盖 d 至少需要多少个 g 粒度的周期。
func periodsFor(d time.Duration, g Granularity) int {
	if g == GranularityMonth {
		const month = 31 * 24 * time.Hour // 上界估计：保证覆盖不欠
		n := int(d / month)
		if n < 1 {
			n = 1
		}
		return n
	}
	n := int(d / (7 * 24 * time.Hour))
	if n < 1 {
		n = 1
	}
	return n
}

// WorstCaseRetention 返回该策略下一条数据的**最坏**存活时长 = 保留期 + 一个分区周期。
// 这是容量估算与测试的直接依据（spec §7.3 的量化缺陷）。
func WorstCaseRetention(spec Spec) time.Duration {
	if spec.Granularity == GranularityMonth {
		return spec.Retention + 30*24*time.Hour
	}
	return spec.Retention + 7*24*time.Hour
}

// Desired 返回按上界严格递增排序的完整期望分区清单，
// 首项是下界守卫分区、末项是上界哨兵分区，中间是 [now-(保留期+1周期), now+horizon) 的业务分区。
func Desired(spec Spec, now time.Time, horizon int) []Bound {
	if horizon < 1 {
		horizon = 1
	}
	cur := periodStart(now, spec.Granularity)
	lookback := periodsFor(spec.Retention, spec.Granularity) + 1
	first := addPeriod(cur, spec.Granularity, -lookback)
	last := addPeriod(cur, spec.Granularity, horizon)

	out := make([]Bound, 0, lookback+horizon+2)
	out = append(out, Bound{Name: GuardPartitionName, Lower: 0, Upper: first.Unix()})
	for p := first; p.Before(last); p = addPeriod(p, spec.Granularity, 1) {
		nxt := addPeriod(p, spec.Granularity, 1)
		out = append(out, Bound{Name: PartitionName(p, spec.Granularity), Lower: p.Unix(), Upper: nxt.Unix()})
	}
	out = append(out, Bound{Name: SentinelPartitionName, Lower: last.Unix(), Upper: math.MaxInt64})
	return out
}

// PlanFrom 对比现状（分区名清单）与期望，产出幂等维护计划。
//
// hold 是最老分区里「只 TRUNCATE 不 DROP」的个数：保留边界以便按名字继续对账，
// 同时把行清空；再往前的分区才 DROP 释放空间。
func PlanFrom(spec Spec, existing []string, now time.Time, horizon, hold int) Plan {
	if hold < 0 {
		hold = 0
	}
	want := Desired(spec, now, horizon)
	have := make(map[string]bool, len(existing))
	for _, n := range existing {
		have[n] = true
	}

	var plan Plan
	for _, b := range want {
		if !have[b.Name] {
			plan.Create = append(plan.Create, b)
		}
	}

	// 回收：业务分区（排除守卫与哨兵）中上界 <= now - 保留期 的，按上界升序。
	//
	// 必须扫**现状**（existing）而不是期望清单：期望窗口随 now 前移，滑出窗口的
	// 老分区不会再出现在 want 里，若按 want 迭代，它们永远不会被回收 ——
	// 每过一个周期就永久泄漏一个（空的）分区。窗口外的老分区上界只能由
	// 确定性分区名反解（boundFromName）。
	cutoff := now.Add(-spec.Retention)
	wantByName := make(map[string]Bound, len(want))
	for _, b := range want {
		wantByName[b.Name] = b
	}
	expired := make([]Bound, 0)
	for _, name := range existing {
		if name == GuardPartitionName || name == SentinelPartitionName {
			continue // 守卫与哨兵永不回收
		}
		b, ok := wantByName[name]
		if !ok {
			// 已滑出期望窗口的老分区：按分区名反解区间
			b, ok = boundFromName(name, spec.Granularity)
			if !ok {
				continue // 不符合本粒度命名（人工/历史遗留）：不擅自回收
			}
		}
		if time.Unix(b.Upper, 0).Before(cutoff) || time.Unix(b.Upper, 0).Equal(cutoff) {
			expired = append(expired, b)
		}
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].Upper < expired[j].Upper })
	for i, b := range expired {
		if i < hold {
			plan.Truncate = append(plan.Truncate, b.Name)
			continue
		}
		plan.Drop = append(plan.Drop, b.Name)
	}
	return plan
}

// InlineClause 返回可内联进 CREATE TABLE 的分区子句；该方言不需要/不支持时返回空串。
//   - mysql：RANGE COLUMNS(bucket_ts) + 内联全部分区定义（含 p_min 与 p_max）
//   - postgres：分区是独立子表，走 ChildDDL；父表只需要 PARTITION BY（由调用方补）
//   - sqlite / 其它：不支持分区 → 返回空串（普通表，协调器走 DELETE 降级路径）
func InlineClause(dialect string, spec Spec, bounds []Bound) (string, error) {
	switch dialect {
	case "mysql":
		parts := make([]string, 0, len(bounds))
		for _, b := range bounds {
			parts = append(parts, fmt.Sprintf("PARTITION %s VALUES LESS THAN (%s)", b.Name, mysqlUpper(b.Upper)))
		}
		return "PARTITION BY RANGE COLUMNS(bucket_ts) (" + strings.Join(parts, ", ") + ")", nil
	case "postgres", "sqlite", "":
		return "", nil
	default:
		return "", nil
	}
}

func mysqlUpper(upper int64) string {
	if upper >= math.MaxInt64 {
		return "MAXVALUE"
	}
	return fmt.Sprintf("%d", upper)
}

// ChildDDL 返回需要单独执行的分区创建语句。
// 只有 PostgreSQL 需要（声明式分区）；MySQL 内联、sqlite 不支持。
func ChildDDL(dialect string, spec Spec, bounds []Bound) ([]string, error) {
	if dialect != "postgres" {
		return nil, nil
	}
	out := make([]string, 0, len(bounds))
	for _, b := range bounds {
		out = append(out, fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
			b.Name, spec.Table, pgLower(b.Lower), pgUpper(b.Upper)))
	}
	return out, nil
}

func pgLower(v int64) string { return fmt.Sprintf("%d", v) }

func pgUpper(upper int64) string {
	if upper >= math.MaxInt64 {
		return "MAXVALUE"
	}
	return fmt.Sprintf("%d", upper)
}

// AddPartitionDDL 返回补齐分区的语句。
//
// MySQL 只能把分区追加在**高端**，而我们把哨兵放在最高位 —— 因此有哨兵时
// 必须用 REORGANIZE 把哨兵拆成「新分区 + 新哨兵」（实测空哨兵约 0.86s）。
// 没有哨兵时才用 ADD PARTITION。
func AddPartitionDDL(dialect string, spec Spec, missing []Bound, hasSentinel bool) ([]string, error) {
	if len(missing) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(missing))
	switch dialect {
	case "mysql":
		for _, b := range missing {
			if !hasSentinel {
				out = append(out, fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN (%s))",
					spec.Table, b.Name, mysqlUpper(b.Upper)))
				continue
			}
			out = append(out, fmt.Sprintf(
				"ALTER TABLE %s REORGANIZE PARTITION %s INTO (PARTITION %s VALUES LESS THAN (%s), PARTITION %s VALUES LESS THAN (MAXVALUE))",
				spec.Table, SentinelPartitionName, b.Name, mysqlUpper(b.Upper), SentinelPartitionName))
		}
	case "postgres":
		for _, b := range missing {
			out = append(out, fmt.Sprintf(
				"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
				b.Name, spec.Table, pgLower(b.Lower), pgUpper(b.Upper)))
		}
	default:
		// sqlite / 其它：不支持分区，交由调用方走降级路径。
	}
	return out, nil
}

// ReclaimDDL 返回回收语句：
//   - truncate：TRUNCATE PARTITION，保留边界、清空行、瞬时（PG 用 DELETE 降级）
//   - drop：DROP PARTITION，彻底释放空间（PG 由调用方改用 DETACH CONCURRENTLY）
func ReclaimDDL(dialect string, spec Spec, truncate, drop []string) ([]string, error) {
	out := make([]string, 0, len(truncate)+len(drop))
	for _, n := range truncate {
		switch dialect {
		case "mysql":
			out = append(out, fmt.Sprintf("ALTER TABLE %s TRUNCATE PARTITION %s", spec.Table, n))
		case "postgres":
			out = append(out, fmt.Sprintf("DELETE FROM %s", n))
		default:
			out = append(out, fmt.Sprintf("DELETE FROM %s", spec.Table))
		}
	}
	for _, n := range drop {
		switch dialect {
		case "mysql":
			out = append(out, fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", spec.Table, n))
		case "postgres":
			out = append(out, fmt.Sprintf("DROP TABLE IF EXISTS %s", n))
		default:
			_ = n
		}
	}
	return out, nil
}
