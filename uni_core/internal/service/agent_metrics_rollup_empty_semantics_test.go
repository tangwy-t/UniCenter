package service

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest/observer"
)

// 本文件钉住 Plan 2G Task 2 的两条「静默」修复（都在 rollup 侧）：
//
//	① `HoursSkipped` 的语义混淆 —— 两种「没有 5m 行」被混在同一个数里：
//	     **暂时空**（仍在 5m 档保留期内：设备离线、flush 滞后，等一等就可能出现）
//	     **永久空洞**（已超出保留期：5m 行已被分区回收，再也补不回来）。
//	   运维看到「skipped=200」时分不出该等还是该修 —— 而两者的处置完全相反。
//	   现在拆成互斥的两个计数器（HoursSkipped / HoursReclaimed，口径见 RollupStats）。
//
//	② `dropRepairHour` 的静默摘除 —— repair 集合里的某小时读回 0 行时，成员被悄悄摘掉：
//	   集合里再也看不到它，而它那行**残缺的 1h 行**会永久留在表里、再也不会被重算
//	   （残缺值好过空洞，是刻意的取舍）。摘除本身是对的，**不出声**是错的。
//
// 反向验证（两条**可编译**的变异）：
//   - 把 HoursReclaimed 也计进 HoursSkipped（两个计数器合并回一个）→ 断言 1 变红；
//   - 删掉 dropRepairHour 里那条 Warn → 断言 2 变红。

// ── 断言 1：两个「没有 5m 行」的小时按保留期分开计数 ──────────────

// TestRollupSplitsEmptyHourSemantics 是本修复的核心断言。
//
// 场景（两个空小时，一个在保留期内、一个已超出）：
//   - **repair 路径**上的 `base`：30 天前的小时，它的 5m 行已被分区回收（夹具显式删掉，
//     模拟回收）→ 永久空洞 → 必须进 HoursReclaimed；
//   - **普通区间**里的那个小时：仍在 5m 保留期内（只差几十秒），设备离线导致没有数据
//     → 暂时空 → 必须进 HoursSkipped。
//
// 修前这两者被混在一个数里（`HoursSkipped == 2`），本断言在那种实现下必然红。
//
// 时钟为什么取 `base + 30 天 + 3h`：保留期取**既有配置来源**
// （sys.agent.historyRetentionDays，缺省 30 天），故「超出保留期」必须真的把 now 推到
// 30 天之后 —— 而不是靠改配置把边界挪过来（那会掩盖「判据用的是哪个数」这件事）。
// 两个小时的保留期归属因此是**时间上的事实**：base 的结束时刻距今 2599260s ≥ 30 天，
// 而普通区间那个小时距今只有几十秒。
func TestRollupSplitsEmptyHourSemantics(t *testing.T) {
	now := rollupBaseTS + int64(defaultAgentHistoryRetentionDays)*86400 + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	// 前置：把 base 弄进 repair 集合（7/12 行 → 残缺 → NeedRepair）。
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if !f.inRepair(t, rollupBaseTS) {
		t.Fatal("前置：不完整小时必须已进 repair 集合")
	}

	// base 的 5m 行被回收（永久空洞的成因）；普通区间那个小时**没有**任何行。
	f.drop5mRange(t, rollupBaseTS, rollupBaseTS+3300)
	upper := f.svc.closedHourUpper()
	// 水位显式放在 upper−2h：普通区间恰好只剩 upper−1h 这一个小时（仍在保留期内），
	// 断言不被那 30 天的空区间摊薄成几百个小时。
	f.setCursor(t, upper-2*rollupHourSec)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats.HoursScanned != 2 {
		t.Fatalf("HoursScanned = %d, want 2（1 个 repair 小时 + 1 个普通区间小时）："+
			"扫到的小时数不对时，下面两个计数器的断言就不是在测「语义拆分」", stats.HoursScanned)
	}
	if stats.HoursSkipped != 1 {
		t.Fatalf("HoursSkipped = %d, want 1（只剩**仍在保留期内**的那一个小时）："+
			"修前这里会是 2 —— 两种「没有 5m 行」被混在一个数里，运维无从区分该等还是该修。stats=%+v",
			stats.HoursSkipped, stats)
	}
	if stats.HoursReclaimed != 1 {
		t.Fatalf("HoursReclaimed = %d, want 1（已超出 5m 保留期、永久补不回来的那个小时）。stats=%+v",
			stats.HoursReclaimed, stats)
	}
}

// TestRollupEmptyHourCountersAreMutuallyExclusiveAndComplete 钉住两个计数器的不变量：
// 互斥、且其和恒等于「本轮所有没有 5m 行的小时数」。
//
// 为什么值得单独一条：这个不变量是「拆一个数成两个数」这件事**唯一**的正确性判据 ——
// 只看两个单独的值，无法区分「拆得对」与「两边都多算了/漏算了」（例如把持久空洞同时记进
// 两个计数器，断言 1 照样能过）。这里用同一批小时分别验证：**和**不变、**交集**为空。
func TestRollupEmptyHourCountersAreMutuallyExclusiveAndComplete(t *testing.T) {
	now := rollupBaseTS + int64(defaultAgentHistoryRetentionDays)*86400 + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	// 三个小时：1 个 repair（已回收，永久空洞）+ 普通区间里的 2 个（都在保留期内）。
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	f.drop5mRange(t, rollupBaseTS, rollupBaseTS+3300)
	upper := f.svc.closedHourUpper()
	f.setCursor(t, upper-3*rollupHourSec)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats.HoursScanned != 3 {
		t.Fatalf("HoursScanned = %d, want 3（1 repair + 2 普通）", stats.HoursScanned)
	}
	if stats.HoursWritten != 0 {
		t.Fatalf("HoursWritten = %d, want 0（三个小时都没有 5m 行，不得写出 1h 行）", stats.HoursWritten)
	}
	if total := stats.HoursSkipped + stats.HoursReclaimed; total != stats.HoursScanned {
		t.Fatalf("HoursSkipped(%d) + HoursReclaimed(%d) = %d, want %d（= HoursScanned）："+
			"两个计数器必须**互斥且完整**地覆盖「没有 5m 行」的小时，否则拆出来的两个数"+
			"既不等于原来那个数、也无法解释差额从哪来", stats.HoursSkipped, stats.HoursReclaimed,
			total, stats.HoursScanned)
	}
	if stats.HoursSkipped != 2 || stats.HoursReclaimed != 1 {
		t.Fatalf("HoursSkipped/HoursReclaimed = %d/%d, want 2/1（2 个在保留期内 + 1 个已回收）",
			stats.HoursSkipped, stats.HoursReclaimed)
	}
}

// ── 断言 2：repair 成员被摘除时必须出声 ──────────────────

// TestRollupRepairDropIsLoudAndKeepsIncompleteHourRow：repair 集合里的小时读回 **0 行** 时
// → **至少一条 Warn**（正文含设备号与小时）+ 该小时被摘除（既有行为，本修复不改它）+
// 那行**残缺的 1h 行仍在且未被改动**。
//
// 最后一条是刻意的：宁可留一行残缺值，也不删数据（删除会让那段时间在 1h 表里变成空洞，
// 而 >30 天的窗口**只有 1h 表可查**）。代价是这行残缺值**永远不会再被重算**（5m 行已经没了），
// 所以运维必须知道它残了 —— 否则他会在控制台上看到一行「看起来正常、其实只统计了部分
// 数据」的 1h 值，而没有任何地方提示它残了。
//
// 为什么用 capture logger 而不是 fakeLogger（只数 Warn 条数）：要断的是**内容** ——
// 「哪台设备、哪个小时」是这条日志的全部价值（500 台设备 × 若干小时，没有这两个字段
// 就定位不到任何东西）。同时按字段与正文断言，两种取用方式都成立。
func TestRollupRepairDropIsLoudAndKeepsIncompleteHourRow(t *testing.T) {
	now := rollupBaseTS + int64(defaultAgentHistoryRetentionDays)*86400 + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if !f.inRepair(t, rollupBaseTS) {
		t.Fatal("前置：不完整小时必须已进 repair 集合")
	}
	before := f.hourRow(t, rollupBaseTS)
	if before == nil {
		t.Fatal("前置：不完整小时必须已写出残缺 1h 行")
	}
	const wantSamples = 7 * 30 // 7 个 5m 行 × 每行 30 个样本 —— 残缺值的具体读数
	if before.Samples != wantSamples {
		t.Fatalf("前置：残缺 1h 行的 samples = %d, want %d", before.Samples, wantSamples)
	}

	// 该小时的 5m 行被保留期回收（现实中由分区 TRUNCATE/DROP 完成）。
	f.drop5mRange(t, rollupBaseTS, rollupBaseTS+3300)

	// 换成观测 logger（zap observer，收集 Warn 及以上）：只换**观测面**，
	// 装配其余部分逐字相同（withRollupDeps 的契约）。
	lg, spy := newRawWindowSpy(t)
	f.withRollupDeps(rollupCfg{}, nil, lg)
	upper := f.svc.closedHourUpper()
	f.setCursor(t, upper-2*rollupHourSec)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}

	// ① 必须出声，且正文里有设备号与小时。
	warns := repairDropWarns(spy)
	if len(warns) < 1 {
		t.Fatalf("repair 成员被摘除必须至少一条 Warn（摘除是一次静默的放弃：集合里再也看不到"+
			"这个小时，而它的残缺 1h 行永久留存且不再重算）。实际 Warn：%v", spy.logs.All())
	}
	hit := warns[0]
	msg := hit.Message
	for _, want := range []string{
		strconv.FormatUint(rollupDevID, 10), // 设备号
		strconv.FormatInt(rollupBaseTS, 10), // 小时
		"摘除", "残缺", "不会再被重算",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Warn 正文必须写出 %q（没有设备号与小时就定位不到是哪台设备的哪个小时残了；"+
				"没有后果说明，运维就不知道那行 1h 值是永久残缺的）。实际：%s", want, msg)
		}
	}
	// 结构化字段：与正文同一对数（供检索/面板取值）。
	fields := hit.ContextMap()
	if got := fields["deviceId"]; got != rollupDevID {
		t.Fatalf("Warn 字段 deviceId = %v, want %d（zap.Uint64 在 observer 里以原值编解码）",
			got, rollupDevID)
	}
	if got := fields["hour"]; got != rollupBaseTS {
		t.Fatalf("Warn 字段 hour = %v, want %d", got, rollupBaseTS)
	}

	// ② 既有行为不变：成员被摘除（不可修的小时不得永久占位）。
	if f.inRepair(t, rollupBaseTS) {
		t.Fatal("5m 行已消失的小时留在 repair 集合里 = 永久占位（集合被不可修项填满）")
	}
	// ③ 残缺 1h 行**仍在**且未被改动（宁可留残缺值，也不删数据；5m 行没了就再也重算不出来）。
	after := f.hourRow(t, rollupBaseTS)
	if after == nil {
		t.Fatal("残缺 1h 行被删除了：>30 天窗口只有 1h 表可查，删掉就是永久空洞")
	}
	if after.Samples != before.Samples {
		t.Fatalf("残缺 1h 行被改写了：samples = %d, want %d（5m 行已回收，本轮什么也算不出来，"+
			"不得写出一个「看起来正常」的新值）", after.Samples, before.Samples)
	}
	// ④ 这个小时确属「永久空洞」（口径与断言 1 同一条判据）。
	if stats.HoursReclaimed != 1 {
		t.Fatalf("HoursReclaimed = %d, want 1（已超出保留期的 repair 小时）。stats=%+v",
			stats.HoursReclaimed, stats)
	}
}

// TestRollupRepairNormalExitIsSilent：**正常出队**（小时已补齐）不得打这条 Warn。
//
// 反方向的守卫：若把 Warn 挂在「摘除」这个动作上而不区分两种摘除，repair 集合的每一次
// 正常收敛都会刷一条「残缺行永久留存」—— 那会把真正的那条信号淹掉（本任务的全部意义）。
func TestRollupRepairNormalExitIsSilent(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	// 7 行 → 进 repair；补齐到 12 行 → 正常出队。
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if !f.inRepair(t, rollupBaseTS) {
		t.Fatal("前置：不完整小时必须已进 repair 集合")
	}
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[7:]...)

	lg, spy := newRawWindowSpy(t)
	f.withRollupDeps(rollupCfg{}, nil, lg)

	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if f.inRepair(t, rollupBaseTS) {
		t.Fatal("小时已补齐，repair 成员必须被移除（否则集合会被完成项占满）")
	}
	if warns := repairDropWarns(spy); len(warns) != 0 {
		t.Fatalf("补齐后的**正常出队**不得打 Warn，却打了 %d 条：%v（正常收敛刷 Warn 会把"+
			"「读回为空、残缺行永久留存」那条真信号淹掉）", len(warns), warns)
	}
}

// ─ 工具 ────────────────────────────────────────────

// repairDropWarns 从观测到的日志里筛出「repair 小时读回为空 → 摘除成员」那一条 Warn。
//
// 用**稳定前缀**（repairHourDropMarker）而不是整句匹配：文案可以调整，前缀是契约
// （运维也按它 grep/告警）。收日志的那一层已在 newRawWindowSpy 里设成 WarnLevel，
// 故这里不必再按级别筛 —— 但它**不等于**「所有 Warn 都是它」：筛前缀才能把本断言
// 与「repair 集合超限丢弃最老小时」那条既有 Warn 区分开。
func repairDropWarns(spy *rawWindowLogSpy) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, e := range spy.logs.All() {
		if strings.Contains(e.Message, repairHourDropMarker) {
			out = append(out, e)
		}
	}
	return out
}
