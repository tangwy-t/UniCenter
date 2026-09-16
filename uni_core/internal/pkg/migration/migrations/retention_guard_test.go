package migrations

import (
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// 本文件是**保留期单源交叉守卫**（Plan 2F / Task 2）。
//
// 保留期有**两个事实来源**，今天一致但没有任何断言把它们绑在一起：
//
//	① 编译期常量：agentmetrics.TableSpecs() 的 `Retention`（分区策略用 ——
//	   周/月分区的粒度选择、协调器预建与回收的视界都按它算）；
//	② 运行期配置：sys.agent.historyRetentionDays / sys.agent.metrics1hRetentionDays
//	   的**种子默认值**（v008 写入 sys_config，运行期可覆盖；分区协调器的
//	   withConfiguredRetention 与 RewindHours 的 rewindBoundHours 都读它）。
//
// 只改一处就会出现「各说一套」，而且**差异只在运行期显形**：
//
//	例：把 TableSpecs 的 5m 保留期从 30d 改成 7d 而忘了种子 →
//	   RewindHours(resolution=1h, hours=720) 按配置算出「可退 30 天」，
//	   但 5m 行在第 8 天就被分区协调器回收了 —— 回退会退进一段**永远算不出东西**
//	   的窗口（每轮 rollup 白扫 720 小时的 DB 读），且编译、单测、启动**全都不报错**。
//
// 守卫放在 migrations 包：配置种子的真值 deviceConfigDefinitions 在这里，
// 而本包同时能 import agentmetrics（agentmetrics 不反向依赖 migrations，无环）。
//
// 两个键名在这里以**字面量**重复一遍（service 包里的同名常量未导出，且迁移包
// 不应反向依赖 service）：键名被改名/删除时守卫会因「查不到条目」而红，
// 而不是静默通过 —— 静默通过正是这个守卫要消灭的失效模式。
const (
	guardKeyHistoryRetentionDays   = "sys.agent.historyRetentionDays"
	guardKeyMetrics1hRetentionDays = "sys.agent.metrics1hRetentionDays"
)

// seededDays 取配置项在 deviceConfigDefinitions 里的**种子默认值**（天）。
//
// 找不到该键、或值不是正整数天数，都直接 Fatal：这两种情形下「种子默认值」
// 这个事实来源已经不存在了，守卫必须红 —— 而不是拿 0 或某个兜底值把断言
// 变成一句永远为真的空话。
func seededDays(t *testing.T, key string) int {
	t.Helper()
	for _, c := range deviceConfigDefinitions {
		if c.Key != key {
			continue
		}
		n, err := strconv.Atoi(c.Value)
		if err != nil {
			t.Fatalf("配置 %s 的种子默认值 %q 不是整数天数：%v（RewindHours 的 rewindBoundHours "+
				"会 Atoi 失败并回落默认值，与分区策略脱节）", key, c.Value, err)
		}
		if n <= 0 {
			t.Fatalf("配置 %s 的种子默认值 = %d，必须为正天数（<=0 会被 withConfiguredRetention / "+
				"rewindBoundHours 当成非法值、各自回落各自的默认值）", key, n)
		}
		return n
	}
	t.Fatalf("配置键 %q 不在 deviceConfigDefinitions 里（被改名或删除？）：分区协调器与 "+
		"RewindHours 都按这个键名读运行期覆盖值，键名一改，两条逻辑会静默回落到各自的默认值，"+
		"与 TableSpecs 的编译期保留期脱节 —— 本守卫的存在就是为了让这件事不再静默", key)
	return 0
}

// specByTable 把 TableSpecs() 摊成「表名 → Spec」。
func specByTable(t *testing.T) map[string]agentmetrics.Spec {
	t.Helper()
	out := make(map[string]agentmetrics.Spec, len(agentmetrics.TableSpecs()))
	for _, s := range agentmetrics.TableSpecs() {
		if _, dup := out[s.Table]; dup {
			t.Fatalf("TableSpecs() 里表 %q 重复出现：分区协调器会对同一张表对账两次", s.Table)
		}
		out[s.Table] = s
	}
	return out
}

// fiveMinTierTables 是**共享 5m 保留期**的 5 张表：整机宽表 + 4 张资源子表。
// 它们的回收边界由同一个配置键（sys.agent.historyRetentionDays）决定。
func fiveMinTierTables() []string {
	return []string{
		entity.TableNameMetric5m,
		entity.TableNameMetricDisk,
		entity.TableNameMetricDiskIO,
		entity.TableNameMetricNIC,
		entity.TableNameMetricSensor,
	}
}

// TestRetentionDefaultsMatchTableSpecs 是本任务的核心守卫：把 TableSpecs() 的
// 编译期保留期与两个配置键的**种子默认值**逐一钉住（相等，不是「差不多」）。
//
// 反向验证（Plan 2F Step 2）：把 v008 的 sys.agent.historyRetentionDays 种子值
// 从 "30" 改成 "31" → 本测试必须变红（且是断言失败而非编译失败）。
func TestRetentionDefaultsMatchTableSpecs(t *testing.T) {
	const day = 24 * time.Hour

	historyDays := seededDays(t, guardKeyHistoryRetentionDays)
	metrics1hDays := seededDays(t, guardKeyMetrics1hRetentionDays)

	specs := specByTable(t)

	// 守卫必须覆盖 TableSpecs() 的**每一张**表：5 张 5m 档 + 1 张 1h 档。
	// 少一张就说明有人加了新表而没同步种子 —— 那张表的保留期将不受本守卫约束。
	if want := len(fiveMinTierTables()) + 1; len(specs) != want {
		t.Fatalf("TableSpecs() 有 %d 张表，本守卫只认得 %d 张（5 张 5m 档 + 1h 档）："+
			"新增指标表必须在这里登记，否则它的保留期不受任何交叉守卫约束（现有：%v）",
			len(specs), want, specTableNames(specs))
	}

	// ① 5m 档：5 张表的 Retention 都 == sys.agent.historyRetentionDays 种子默认值 × 24h。
	want5m := time.Duration(historyDays) * day
	for _, table := range fiveMinTierTables() {
		s, ok := specs[table]
		if !ok {
			t.Fatalf("TableSpecs() 缺少 5m 档的表 %q（这几张表必须一起被回收，保留期同源）", table)
		}
		if s.Retention != want5m {
			t.Fatalf("表 %s 的编译期 Retention = %v，而 %s 的种子默认值 = %d 天（= %v）："+
				"两处必须一致 —— 分区策略按前者算、RewindHours 的回退上界与协调器的视界按后者算，"+
				"不一致时回退会退进一段已被回收、永远算不出东西的窗口，且只在运行期显形",
				s.Table, s.Retention, guardKeyHistoryRetentionDays, historyDays, want5m)
		}
	}

	// ② 1h 档：Retention == sys.agent.metrics1hRetentionDays 种子默认值 × 24h。
	// 1h 档单独一个键（不是 historyRetentionDays）：趋势层的保留期与明细层不同步，
	// 拿错键会让「1h 表按 30 天回收」这种错配同样静默。
	s1h, ok := specs[entity.TableNameMetric1h]
	if !ok {
		t.Fatalf("TableSpecs() 缺少 1h 档的表 %q", entity.TableNameMetric1h)
	}
	if want := time.Duration(metrics1hDays) * day; s1h.Retention != want {
		t.Fatalf("表 %s 的编译期 Retention = %v，而 %s 的种子默认值 = %d 天（= %v）：两处必须一致",
			s1h.Table, s1h.Retention, guardKeyMetrics1hRetentionDays, metrics1hDays, want)
	}
}

// TestTableSpecsMatchEntityTableNameConstants 双向核对表清单：
// TableSpecs() 的表名集合 == entity 的 6 个 TableNameMetric* 常量集合。
//
// **双向**是关键：只查一个方向的话，多出来的那张表（TableSpecs 有、常量没有）
// 会静默通过 —— 那正是「新增分区表忘了登记」的形态。两张表名清单服务的是
// 不同消费者（分区策略 vs 仓储/钩子），任何一侧漂移都必须红。
func TestTableSpecsMatchEntityTableNameConstants(t *testing.T) {
	consts := []string{
		entity.TableNameMetric5m,
		entity.TableNameMetric1h,
		entity.TableNameMetricDisk,
		entity.TableNameMetricDiskIO,
		entity.TableNameMetricNIC,
		entity.TableNameMetricSensor,
	}

	inConsts := make(map[string]bool, len(consts))
	for _, name := range consts {
		inConsts[name] = true
	}
	specs := specByTable(t)

	// 方向一：TableSpecs() 里的每张表都必须是 entity 常量之一。
	for _, name := range specTableNames(specs) {
		if !inConsts[name] {
			t.Fatalf("TableSpecs() 里的表 %q 不在 entity 的 6 个 TableNameMetric* 常量里："+
				"分区策略引用了一张没有表名常量的表（拼写漂移或漏登记）", name)
		}
	}
	// 方向二：6 个常量每一个都必须出现在 TableSpecs() 里。
	for _, name := range consts {
		if _, ok := specs[name]; !ok {
			t.Fatalf("entity 常量 %q 不在 TableSpecs() 里：这张分区表没有分区策略"+
				"（分区协调器不会为它预建/回收分区，保留期形同虚设）", name)
		}
	}
	if len(specs) != len(consts) {
		t.Fatalf("表数 = TableSpecs %d / entity 常量 %d, want %d/%d（双向核对后数量也必须相等）",
			len(specs), len(consts), len(consts), len(consts))
	}
}

// specTableNames 返回排序后的表名（TableSpecs 是切片，但守卫内部用 map，
// 排序保证失败信息稳定可读）。
func specTableNames(specs map[string]agentmetrics.Spec) []string {
	out := make([]string, 0, len(specs))
	for name := range specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
