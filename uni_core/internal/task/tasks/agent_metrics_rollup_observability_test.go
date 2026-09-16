package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// 本文件钉住 rollup 任务**交给调度器的两个可观测读数**（Plan 2F / Task 2 的收尾）：
//
//	hoursDeferred   —— 本轮是否被「每设备每轮小时配额」截断（0 当且仅当没被截断）；
//	rewindsCaughtUp —— 本轮是否追平了「回退待追平」标记（运维执行过 RewindHours 之后，
//	                   **唯一**能证明它生效的读数）。
//
// 为什么必须在任务层断言：这两个读数由 service 累加，但「被人看见」的落点只有任务层这一行
// 日志 —— 上一版把它们加进 RollupStats 之后**没有**加进任务的 zap 字段列表，于是服务侧的
// 配额暂停说明停在 Debug（默认阈值 info 下不可见）、追平计数一行日志都没有：从一次 job 执行
// 留下的日志里既看不出「这一轮被配额切了一刀」，也看不出「回退有没有追平」。
//
// 断言粒度说明：capture logger（见 agent_metrics_test.go 的 newCaptureLogger）输出的是一行
// 一条 JSON，故这里既按**字段**断言（值真的落进去了），也按**级别**断言（默认可见性），
// 而不是只做一次 strings.Contains（那既验不出级别，也把「字段」与「正文里恰好出现的数字」
// 混为一谈）。
//
// 反向验证：把 hoursDeferred / rewindsCaughtUp 两个 zap 字段从
// agent_metrics_rollup.go 的 fields 里删掉（仍然可编译）→ 本文件的两个正例断言与普通轮
// 的「字段值为 0」断言同时变红（日志里再也找不到这两个键）。

// ── 1) 配额截断：hoursDeferred > 0 必须落进 job 日志（且默认可见）──

func TestAgentMetricsRollupTaskExposesQuotaDeferralInJobLog(t *testing.T) {
	// 48 = 生产缺省配额（defaultRollupMaxHoursPerRound）；672 = 720 − 48，即「一次 30 天
	// 回退被配额切成的剩余部分」。两个数字取自 HoursDeferred 的口径（(upper−水位)/3600）。
	fake := &fakeAgentService{rollupStats: service.RollupStats{
		HoursScanned: 48, HoursWritten: 48, HoursDeferred: 672,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsRollupTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v（配额用尽是主动暂停，不得变成错误）", err)
	}

	lines := capturedLines(t, buf)
	// 本轮完成的读数行必须同时带上两个新字段：hoursDeferred 非零 = 被截断，
	// rewindsCaughtUp 必须显式写 0（缺字段与「恰好没发生」在采集端是两件事）。
	done := mustLineWith(t, lines, "本轮完成")
	if got, ok := logInt(done, "hoursDeferred"); !ok || got != 672 {
		t.Fatalf("本轮完成日志的 hoursDeferred = %v(存在=%v), want 672：配额截断的唯一读数是它，"+
			"缺了它则「只写了 48 小时」既可能是配额上限、也可能是全部工作量。实际日志：\n%s",
			done["hoursDeferred"], ok, buf.String())
	}
	if got, ok := logInt(done, "rewindsCaughtUp"); !ok || got != 0 {
		t.Fatalf("本轮完成日志的 rewindsCaughtUp = %v(存在=%v), want 0（未被截断的轮次该字段也必须在，"+
			"否则采集端无法区分「没追平」与「这个版本还没有这个字段」）。实际日志：\n%s",
			done["rewindsCaughtUp"], ok, buf.String())
	}
	// 级别：落在 Info 上才算「默认可见」（Debug 在部署的 info 阈值下等于没有）。
	if lvl := logLevel(done); lvl != "info" {
		t.Fatalf("承载 hoursDeferred 的日志级别 = %q, want info：Debug 级在默认阈值下不可见，"+
			"这条读数的全部意义就是被人（与采集端）看见", lvl)
	}
}

// ── 2) 回退追平：rewindsCaughtUp > 0 必须落进 job 日志（且默认可见）──

func TestAgentMetricsRollupTaskExposesRewindCatchUpInJobLog(t *testing.T) {
	// 追平轮：一整段回退终于走完（HoursDeferred = 0 是追平成立的条件，见 rewindCaughtUp）。
	fake := &fakeAgentService{rollupStats: service.RollupStats{
		HoursScanned: 720, HoursWritten: 720, RewindsCaughtUp: 2,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsRollupTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	lines := capturedLines(t, buf)
	done := mustLineWith(t, lines, "本轮完成")
	if got, ok := logInt(done, "rewindsCaughtUp"); !ok || got != 2 {
		t.Fatalf("本轮完成日志的 rewindsCaughtUp = %v(存在=%v), want 2：运维回退之后，"+
			"没有这个读数就无从判断回退有没有生效（服务侧的逐设备「回退目标已追平」带设备号与目标，"+
			"本行给的是本轮合计 —— 两者互补、不重复）。实际日志：\n%s",
			done["rewindsCaughtUp"], ok, buf.String())
	}
	if got, ok := logInt(done, "hoursDeferred"); !ok || got != 0 {
		t.Fatalf("追平轮的 hoursDeferred = %v(存在=%v), want 0（配额没截断是追平成立的前提）。实际日志：\n%s",
			done["hoursDeferred"], ok, buf.String())
	}
	if lvl := logLevel(done); lvl != "info" {
		t.Fatalf("承载 rewindsCaughtUp 的日志级别 = %q, want info（见上一条的理由）", lvl)
	}
}

// ── 3) 普通轮：两个字段都是 0，且**不产生**多余的 Info/Warn ──

// 反方向的守卫：把读数暴露出去**不等于**每轮多打几行。未截断、未追平的一轮只能有
// 恰好一行 Info（本轮完成），既没有额外的 Info 叙事行，也不能有 Warn —— 那正是
// 「加了可观测性之后日志被噪声淹没」的失效模式。
func TestAgentMetricsRollupTaskQuietOnOrdinaryRound(t *testing.T) {
	fake := &fakeAgentService{rollupStats: service.RollupStats{
		HoursScanned: 5, HoursWritten: 4, HoursRepaired: 1, HoursSkipped: 1,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsRollupTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	lines := capturedLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("普通轮打了 %d 行日志，want 1（只有「本轮完成」）：可观测性读数只能随既有那一行落下，"+
			"每轮多打一行 Info/Warn 等于用噪声换可观测性。实际日志：\n%s", len(lines), buf.String())
	}
	if lvl := logLevel(lines[0]); lvl != "info" {
		t.Fatalf("普通轮唯一的日志级别 = %q, want info", lvl)
	}
	for _, rec := range lines {
		if lvl := logLevel(rec); lvl != "info" {
			t.Fatalf("普通轮不得出现 %s 级日志（未截断、未追平、无错误）：%v", lvl, rec)
		}
	}
	for key, want := range map[string]int{"hoursDeferred": 0, "rewindsCaughtUp": 0} {
		if got, ok := logInt(lines[0], key); !ok || got != want {
			t.Fatalf("普通轮的 %s = %v(存在=%v), want %d：为 0 也必须显式落字段（缺字段 = 采集端"+
				"读不到「这一轮没有发生这件事」）。实际日志：\n%s", key, lines[0][key], ok, want, buf.String())
		}
	}
}

// ─ 4) 失败轮：两个读数在 Error 行上同样必须在 ──

// 部分失败的一轮同样要能读出「被截断了多少 / 有没有追平」：那一行才是调度器与告警真正
// 会盯的日志（job 状态 failed + 这条 Error），若读数只挂在成功分支上，最需要它的场景恰好没有。
func TestAgentMetricsRollupTaskKeepsReadingsOnFailureLine(t *testing.T) {
	fake := &fakeAgentService{
		rollupStats: service.RollupStats{HoursScanned: 48, HoursWritten: 47, HoursDeferred: 672, Errors: 1},
		rollupErr:   errBoomRollup,
	}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsRollupTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err == nil {
		t.Fatal("服务错误必须上抛（调度器按它记 failed）")
	}

	lines := capturedLines(t, buf)
	fail := mustLineWith(t, lines, "本轮部分失败")
	if got, ok := logInt(fail, "hoursDeferred"); !ok || got != 672 {
		t.Fatalf("失败轮的 hoursDeferred = %v(存在=%v), want 672（失败与截断是两个正交的读数，"+
			"少了它就无法判断该先处理哪一个）。实际日志：\n%s", fail["hoursDeferred"], ok, buf.String())
	}
	if got, ok := logInt(fail, "rewindsCaughtUp"); !ok || got != 0 {
		t.Fatalf("失败轮的 rewindsCaughtUp = %v(存在=%v), want 0。实际日志：\n%s",
			fail["rewindsCaughtUp"], ok, buf.String())
	}
}

// ── 工具：把捕获到的 JSON 日志摊成可逐字段断言的记录 ──────────────

// capturedLines 把 capture logger 的输出拆成一行一条记录（JSON object → map）。
//
// 用 map 而不是字符串匹配：本文件的断言同时要看**字段名**、**字段值**与**级别**，
// 字符串匹配没法区分 `"hoursDeferred":0` 与正文里恰好出现的 0，也验不出级别。
func capturedLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, 2)
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("捕获到的日志不是一行一条 JSON（%v）：%s", err, raw)
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		t.Fatal("一条日志都没捕到 —— capture logger 没接上（那么本文件的断言全是空话）")
	}
	return out
}

// mustLineWith 取唯一一条正文含 substr 的记录；没有或有多条都直接失败。
func mustLineWith(t *testing.T, lines []map[string]any, substr string) map[string]any {
	t.Helper()
	var found map[string]any
	n := 0
	for _, rec := range lines {
		msg, _ := rec["msg"].(string)
		if strings.Contains(msg, substr) {
			found = rec
			n++
		}
	}
	if n != 1 {
		t.Fatalf("正文含 %q 的日志有 %d 条，want 1（实际日志行数=%d）：%v", substr, n, len(lines), lines)
	}
	return found
}

// logLevel 取 zap JSON 的 level 字段（ProductionEncoderConfig 的小写形式："info"/"warn"/"error"）。
func logLevel(rec map[string]any) string {
	lvl, _ := rec["level"].(string)
	return lvl
}

// logInt 取一个整数字段：zap 的 zap.Int 编码成 JSON number，解出来是 float64。
//
// 第二个返回值是「字段是否存在」—— 缺字段与值为 0 必须能区分（前者是这个版本没暴露该读数，
// 后者是这一轮没发生这件事）。
func logInt(rec map[string]any, key string) (int, bool) {
	raw, ok := rec[key]
	if !ok {
		return 0, false
	}
	f, ok := raw.(float64)
	if !ok {
		return 0, true
	}
	return int(f), true
}
