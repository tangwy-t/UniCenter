package service

import (
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// 本文件是**保留期的第三个事实来源**的守卫（Plan 2F / Task 2 的收尾）。
//
// 保留期实际有三个来源，今天都是 30 / 180，但只有前两个被断言绑在一起：
//
//	① agentmetrics.TableSpecs() 的**编译期常量** —— 分区策略用（粒度选择、预建与回收视界）；
//	② v008 种子的 sys.agent.historyRetentionDays = "30" / sys.agent.metrics1hRetentionDays = "180"
//	   —— 运行期覆盖的**种子值**（写进 sys_config 的那一行）；
//	③ 本包的 defaultAgentHistoryRetentionDays = 30 / defaultAgentMetrics1hRetentionDays = 180
//	   —— withConfiguredRetention 与 rewindBoundHours 里 GetInt 的 **def**，即**配置键缺失
//	   或非法（<=0）时真正生效的值**。
//
// 上一轮的守卫（migrations/retention_guard_test.go 的 TestRetentionDefaultsMatchTableSpecs）
// 钉的是 ①↔②。③ 是**未导出**的，那个守卫看不见它；而它才是配置出问题时唯一还在起作用的值：
// 把 ③ 改成 31（或 90），② 与 ① 全都没变、编译/启动/既有测试全绿，但只要运行期的配置行
// 被删掉或填成 0，分区协调器就会按 31 天回收 5m 分区，而 RewindHours 的 rewindBoundHours
// 会按同一个 31 天算回退上界 —— 两者自洽地错，谁也不会红。这正是本文件要消灭的失效模式。
//
// 为什么守卫放在**本包**（而不是把常量导出后并进 migrations 的守卫里）：
//   - ③ 就在本包，本包同时能读 ①（service 已经 import agentmetrics）；这条守卫守的正是
//     ③ 的**语义**（「配置缺失时生效的值」），放在它身边、并用**真实代码路径**
//     （withConfiguredRetention / rewindBoundHours，而不是重述一遍常量）来断言，最结实；
//   - 反过来把常量导出、在 migrations 里绑，代价是把两个内部常量变成跨包 API，而收益只是
//     把同一组等式换个地方写；改动面更大、且离开「配置缺失时生效」这个语义现场。
//
// ② 在这里只能以**字面量镜像**出现：v008 的 deviceConfigDefinitions 未导出，而本包不能
// import migrations（migrations → task/tasks → service，成环）。镜像不会因此变松 —— 它被两条
// 断言从两侧夹住：本文件断言 镜像 == ①，migrations 的守卫断言 ① == 真实种子；
// 于是「镜像 ≠ 真实种子」不可能同时通过（任何一侧漂移都会先红一条）。
const (
	// v008 种子值的字面量镜像（见 migrations/v008_seed_device.go 的 deviceConfigDefinitions）。
	seedHistoryRetentionDaysMirror   = 30
	seedMetrics1hRetentionDaysMirror = 180
	// 种子写下的**键名**镜像：本包读错键名时，读到的就是一个不存在的键 —— 配置覆盖静默失效、
	// 只剩默认值生效，而这在运行期完全看不出来（GetInt 拿到 def 照常返回）。
	seedKeyHistoryRetentionDaysMirror   = "sys.agent.historyRetentionDays"
	seedKeyMetrics1hRetentionDaysMirror = "sys.agent.metrics1hRetentionDays"
)

// TestRetentionDefaultsMatchAllThreeSources 断言 5m 档与 1h 档的**三个来源两两相等**。
//
// 反向验证（可编译、且必须是断言失败）：把 defaultAgentHistoryRetentionDays 从 30 改成 31
// → 本测试红（③ 与 ①/② 不再相等）。不得用编译失败冒充。
func TestRetentionDefaultsMatchAllThreeSources(t *testing.T) {
	const day = 24 * time.Hour

	specs := make(map[string]agentmetrics.Spec, len(agentmetrics.TableSpecs()))
	for _, s := range agentmetrics.TableSpecs() {
		specs[s.Table] = s
	}

	cases := []struct {
		name string
		// tables 是共用同一把保留期键的表（5m 档 5 张：整机宽表 + 4 张资源子表）。
		tables      []string
		seedDays    int    // ②
		defaultDays int    // ③
		seedKey     string // ② 写下的键名
		configKey   string // ③ 所在服务读的键名
	}{
		{
			name:        "5m 档",
			tables:      fiveMinRetentionTables(),
			seedDays:    seedHistoryRetentionDaysMirror,
			defaultDays: defaultAgentHistoryRetentionDays,
			seedKey:     seedKeyHistoryRetentionDaysMirror,
			configKey:   configAgentHistoryRetentionDays,
		},
		{
			name:        "1h 档",
			tables:      []string{entity.TableNameMetric1h},
			seedDays:    seedMetrics1hRetentionDaysMirror,
			defaultDays: defaultAgentMetrics1hRetentionDays,
			seedKey:     seedKeyMetrics1hRetentionDaysMirror,
			configKey:   configAgentMetrics1hRetentionDays,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// ②③：种子值与「配置缺失时生效的值」必须逐字相等。
			if c.seedDays != c.defaultDays {
				t.Fatalf("%s：v008 种子值 = %d 天，而本包 GetInt 的默认值 = %d 天："+
					"两者必须一致 —— 配置行缺失/非法时生效的**只有**后者，不一致就等于"+
					"「种子说一套、真跑起来另一套」", c.name, c.seedDays, c.defaultDays)
			}
			// 键名：本包读的键必须就是种子写下的键（读错键 = 配置覆盖静默失效）。
			if c.configKey != c.seedKey {
				t.Fatalf("%s：本包读的配置键 = %q，而 v008 种子的键 = %q："+
					"读一个不存在的键时 GetInt 照常返回默认值，运行期完全看不出来（覆盖静默失效）",
					c.name, c.configKey, c.seedKey)
			}

			// 空配置替身：任何键都回落调用方给的 def —— 这就是生产里「配置键缺失」的语义。
			// 用它而不是直接比常量：withConfiguredRetention 是真正决定回收边界的那个函数，
			// 断言它**算出来**的保留期，才连「键名读错/分支取错变量」这类实现错误一起钉住。
			missingKey := &AgentMetricsPartitionService{cfg: partitionConfig{}}
			// 非法值（<=0）：withConfiguredRetention 与 rewindBoundHours 都回落默认值，
			// 故「填成 0」与「没有这一行」必须落到同一个保留期上。
			illegal := map[string]int{c.configKey: 0}

			for _, table := range c.tables {
				spec, ok := specs[table]
				if !ok {
					t.Fatalf("TableSpecs() 缺表 %q：本守卫按表清单核对保留期，缺表即漏守", table)
				}
				wantSeed := time.Duration(c.seedDays) * day
				wantDefault := time.Duration(c.defaultDays) * day

				// ①↔② 与 ①↔③：编译期常量必须等于两个运行期来源换算出来的时长。
				if spec.Retention != wantSeed || spec.Retention != wantDefault {
					t.Fatalf("表 %s 的编译期 Retention = %v，而种子值 = %d 天（= %v）、"+
						"服务默认值 = %d 天（= %v）：①/②/③ 必须三者相等 —— 分区策略按 ① 算、"+
						"回收视界与 rewindBoundHours 按 ②/③ 算，不一致时回退会退进一段已被回收、"+
						"永远算不出东西的窗口，且只在运行期显形",
						table, spec.Retention, c.seedDays, wantSeed, c.defaultDays, wantDefault)
				}

				// 真实路径：配置键缺失 → 生效的保留期。
				if got := missingKey.withConfiguredRetention(spec).Retention; got != wantDefault {
					t.Fatalf("表 %s 在**配置键缺失**时的实际保留期 = %v，want %v（= %d 天）："+
						"这是配置被删/写错那一天真正生效的值，它必须与 ①/② 一致",
						table, got, wantDefault, c.defaultDays)
				}
				// 真实路径：配置键被填成 0 → 同一个保留期（非法值回落默认值，不是「清空数据」）。
				illegalSvc := &AgentMetricsPartitionService{cfg: partitionConfig{vals: illegal}}
				if got := illegalSvc.withConfiguredRetention(spec).Retention; got != wantDefault {
					t.Fatalf("表 %s 在配置 = 0（非法）时的实际保留期 = %v，want %v："+
						"非法值必须回落默认值 —— 回落成 0 会让 Desired 的窗口收缩到只剩当前周期、"+
						"每一轮把几乎全部分区判成过期（那不是「清空数据」的授权，只是配置写错了）",
						table, got, wantDefault)
				}
			}

			// rewindBoundHours 读的是同一把 5m 档键（**不是**本档位的 1h 键）：
			// 1h 档的回退上界 = 它重算所依赖的那份数据（5m 行）的保留期。
			// 断言的是真实函数算出来的小时数，故「键取错一档」也会红。
			flush := &AgentMetricsFlushService{cfg: partitionConfig{}}
			res := agentmetrics.Resolution1h
			wantBound := seedHistoryRetentionDaysMirror * 24
			if got := flush.rewindBoundHours(res); got != wantBound {
				t.Fatalf("rewindBoundHours(%s) = %d 小时，want %d（= 5m 保留期 %d 天 × 24）："+
					"1h 档的回退上界由**5m 行的保留期**决定，这里若与 ①②③ 脱节，"+
					"回退会越过真实的回收边界、退出一段白扫 720 小时的空窗口",
					res, got, wantBound, seedHistoryRetentionDaysMirror)
			}
		})
	}
}

// fiveMinRetentionTables 是**共享 5m 保留期**的 5 张表（整机宽表 + 4 张资源子表）。
//
// 与 migrations 守卫里的 fiveMinTierTables 是同一条事实：5 张表一起被回收、保留期同源。
// 这里重新列一遍是有意的（不是复制常量）：任一侧漏登记一张表，都说明「新增指标表忘了
// 接进保留期口径」，而那种缺口在运行期只表现为「某张表的分区永远不被回收」。
func fiveMinRetentionTables() []string {
	return []string{
		entity.TableNameMetric5m,
		entity.TableNameMetricDisk,
		entity.TableNameMetricDiskIO,
		entity.TableNameMetricNIC,
		entity.TableNameMetricSensor,
	}
}
