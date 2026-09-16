package service

import (
	"strings"
	"testing"
)

// 本文件钉住「配额暂停」这条日志的**级别**（Plan 2F / Task 2 的可观测性收尾）。
//
// 为什么必须单独钉它：那条说明是本服务里**唯一**逐设备解释「这一轮为什么只写了 48 个小时」
// 的地方（stats.HoursDeferred 只给合计）。它原先停在 Debug 级，而部署的日志阈值是 info
// —— 于是这条读数在任何生产环境里都取不到，「被配额截断」与「工作量本来就这么大」在日志里
// 完全无法区分，正好把 Plan 2F 想补的可观测性又漏掉一层。
//
// 断言工具是 rewindLogSpy（agent_metrics_rewind_test.go）：它的 Debug 是**空实现**、
// Info 才被记录，故 `hasInfo()` 成立本身就等价于「级别 ≥ Info」—— 改回 Debug 会让本测试
// 立刻红（这正是本文件存在的意义：级别这种「没写进任何结构体」的决策，只能靠日志替身钉住）。
func TestRollupQuotaDeferralIsLoggedAtInfo(t *testing.T) {
	const sub = "配额用尽"

	t.Run("被配额截断的一轮：Info + 逐设备字段", func(t *testing.T) {
		f := newRollupFixture(t, quotaWindowNow, true, 0) // spy：数清这一轮写了几行
		spy := &rewindLogSpy{}
		f.withRollupDeps(rollupCfg{
			rescanHours: defaultRollupRescanHours,
			maxHours:    defaultRollupMaxHoursPerRound,
		}, nil, spy)
		seedQuotaWindow(t, f)
		// 水位放到回退目标上（= RewindHours(…, Resolution1h, 720) 之后的状态）。
		f.setCursor(t, quotaRewindTarget())

		stats := f.rollupRound(t)
		if stats.HoursDeferred <= 0 {
			t.Fatalf("stats = %+v, want HoursDeferred > 0（本用例的前提就是「被配额截断」）", stats)
		}

		line, ok := findInfo(spy, sub)
		if !ok {
			t.Fatalf("配额用尽的一轮必须在 **Info** 级留下一条含 %q 的逐设备说明"+
				"（Debug 在默认阈值下不可见 = 这条读数在生产里等于不存在）；实际记录的信息级日志 = %v",
				sub, spy.infos)
		}
		// 字段：设备号（哪台设备被截断）+ 配额与已处理小时（切了多少）+ 剩余小时（读数本身）。
		if got, ok := line.fields["deviceId"].(int64); !ok || uint64(got) != rollupDevID {
			t.Fatalf("配额说明缺 deviceId（配额是**每设备**的，没有它就无法定位是哪台设备积压）：%v", line.fields)
		}
		if got, ok := line.fields["quota"].(int64); !ok || int(got) != defaultRollupMaxHoursPerRound {
			t.Fatalf("配额说明的 quota = %v, want %d：先切掉的一半证据是「配额是多少」",
				line.fields["quota"], defaultRollupMaxHoursPerRound)
		}
		if got, ok := line.fields["hoursDeferred"].(int64); !ok || int(got) != stats.HoursDeferred {
			t.Fatalf("配额说明的 hoursDeferred = %v, want %d（= 本轮 stats 的同名字段）："+
				"日志与读数必须是同一个数，否则排障时会看到两个口径", line.fields["hoursDeferred"], stats.HoursDeferred)
		}
		if got, ok := line.fields["hoursProcessed"].(int64); !ok || int(got) != defaultRollupMaxHoursPerRound {
			t.Fatalf("配额说明的 hoursProcessed = %v, want %d", line.fields["hoursProcessed"], defaultRollupMaxHoursPerRound)
		}
	})

	t.Run("没被截断的一轮：不得出现这条 Info（不制造噪声）", func(t *testing.T) {
		f := newRollupFixture(t, quotaWindowNow, true, 0)
		spy := &rewindLogSpy{}
		f.withRollupDeps(rollupCfg{
			rescanHours: defaultRollupRescanHours,
			maxHours:    defaultRollupMaxHoursPerRound,
		}, nil, spy)
		// 水位已在最新（生产里的常态）：普通区间为空 → 配额根本没被用尽。
		f.setCursor(t, alignDownHour(quotaWindowNow))

		stats := f.rollupRound(t)
		if stats.HoursDeferred != 0 {
			t.Fatalf("stats = %+v, want HoursDeferred = 0（本用例的前提是「没被截断」）", stats)
		}
		if _, ok := findInfo(spy, sub); ok {
			t.Fatalf("没被配额截断的一轮不得打出 %q：把级别提到 Info 的代价必须是「只在真发生时打」，"+
				"否则每轮一条 Info 就是用噪声换可观测性；实际记录 = %v", sub, spy.infos)
		}
	})
}

// findInfo 在 rewindLogSpy 的信息级记录里找第一条消息含 substr 的日志。
func findInfo(spy *rewindLogSpy, substr string) (loggedLine, bool) {
	for _, line := range spy.infos {
		if strings.Contains(line.msg, substr) {
			return line, true
		}
	}
	return loggedLine{}, false
}
