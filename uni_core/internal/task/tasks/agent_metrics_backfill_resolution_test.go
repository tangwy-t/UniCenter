package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// 本文件覆盖 `agent-metrics-backfill` 的 `resolution` 维度（Plan 2E 收尾）：
//
//	{"hours":24}                    → 回退 cursor_5m + 重放 5m（**与引入 resolution 之前逐字相同**）
//	{"hours":720,"resolution":"1h"}  → 只回退 cursor_1h，本轮不重放（下一轮 rollup 补出）
//	无法识别的 resolution            → 回落 5m（+ 一条 Warn：回落不许是静默的）
//
// 「不传 resolution 时行为与断言逐条不变」由**既有测试**守住（本文件不动它们）：
// TestAgentMetricsBackfillTaskForwardsParams / DefaultsToRetentionWindow / RejectsBadParams /
// PropagatesBootstrapError 都仍然绿，且它们断言的是同一条 BackfillOnce 路径。

// bootstrapRanBeforeRewind 检查 order 里最后一次 rewind 之前出现过 bootstrap
// （1h 支同样遵守「起点对齐先于回溯」的契约；与既有的 bootstrapRanBeforeBackfill 同形，
// 分开写是因为两者要钉的是**不同分支**的顺序）。
func (f *fakeAgentService) bootstrapRanBeforeRewind() bool {
	seenBootstrap := false
	for _, step := range f.order {
		switch step {
		case "bootstrap":
			seenBootstrap = true
		case "rewind":
			if !seenBootstrap {
				return false
			}
		}
	}
	return seenBootstrap
}

// TestAgentMetricsBackfillResolutionLiterals 钉住档位字面量的契约与回落规则。
//
// 为什么用字面量而不是 Resolution.String() 做解析入口（见 parseBackfillResolution 的注释）：
// invoke_params 是**调度器里的存量字符串**（v009 种子、运维手工改过的 job 行），
// 而 String() 是给人看的显示名 —— 拿显示名当解析入口，未来改一次显示名就会让所有存量
// params 静默回落 5m。本断言同时钉住「两个字面量与显示名一致」（否则运维会按日志去填错 params）。
func TestAgentMetricsBackfillResolutionLiterals(t *testing.T) {
	cases := []struct {
		in             string
		want           agentmetrics.Resolution
		wantRecognized bool
	}{
		{"", agentmetrics.Resolution5m, true},      // 缺省 = 5m = 既有行为（认得出，不是回落）
		{"5m", agentmetrics.Resolution5m, true},    // 显式 5m
		{"1h", agentmetrics.Resolution1h, true},    // 唯一进入「只回退」支的值
		{"1H", agentmetrics.Resolution5m, false},   // 大小写笔误 → 回落
		{"1h ", agentmetrics.Resolution5m, false},  // 前后空白 → 回落
		{"5min", agentmetrics.Resolution5m, false}, // 显示名之外的写法（"5min"）不是契约 → 回落
		{"60m", agentmetrics.Resolution5m, false},  // 任何其它写法 → 回落
	}
	for _, tc := range cases {
		got, recognized := parseBackfillResolution(tc.in)
		if got != tc.want || recognized != tc.wantRecognized {
			t.Fatalf("parseBackfillResolution(%q) = (%s, %v), want (%s, %v)",
				tc.in, got, recognized, tc.want, tc.wantRecognized)
		}
	}
	if got := agentmetrics.Resolution5m.String(); got != "5m" {
		t.Fatalf("Resolution5m.String() = %q, want %q（invoke_params 的存量字面量按它取）", got, "5m")
	}
	if got := agentmetrics.Resolution1h.String(); got != "1h" {
		t.Fatalf("Resolution1h.String() = %q, want %q（invoke_params 的存量字面量按它取）", got, "1h")
	}
}

// TestAgentMetricsBackfillTaskRewindsCursor1hWithoutReplay 是 1h 支的转发与日志断言：
//   - 调的是 `RewindHours(deviceIDs, Resolution1h, hours)`，**不是** BackfillOnce
//     （两支互斥：一支只回退、一支回退+重放）；
//   - Bootstrap 仍然先跑（前置契约两个分支一致）；
//   - 日志里必须能读出「档位 = 1h」「本轮不重放」——否则一条没有 bucketsWritten 的完成日志
//     看起来像「重放写了 0 个桶」，运维会以为补齐失败了。
func TestAgentMetricsBackfillTaskRewindsCursor1hWithoutReplay(t *testing.T) {
	fake := &fakeAgentService{rewindStats: service.RewindStats{
		Resolution: agentmetrics.Resolution1h, WindowHours: 720, DevicesScanned: 3, CursorsRewound: 2,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsBackfillTask(fake, lg)
	err := tk.Execute(context.Background(),
		json.RawMessage(`{"hours":720,"resolution":"1h","device_ids":[1001,1002]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.rewindCalls != 1 {
		t.Fatalf("RewindHours 调用次数 = %d, want 1", fake.rewindCalls)
	}
	if fake.backfillCalls != 0 {
		t.Fatalf("resolution=1h 不得走 5m 支（BackfillOnce 调用 %d 次）—— 那会顺手重放一遍 5m", fake.backfillCalls)
	}
	if fake.rewindResolution != agentmetrics.Resolution1h {
		t.Fatalf("透传档位 = %s, want 1h", fake.rewindResolution)
	}
	if fake.rewindHours != 720 {
		t.Fatalf("透传 hours = %d, want 720", fake.rewindHours)
	}
	if len(fake.rewindDeviceIDs) != 2 || fake.rewindDeviceIDs[0] != 1001 || fake.rewindDeviceIDs[1] != 1002 {
		t.Fatalf("透传 device_ids = %v, want [1001 1002]（device_ids 对两支同义：只缩小回退范围）",
			fake.rewindDeviceIDs)
	}
	if fake.bootstrapCalls != 1 || !fake.bootstrapRanBeforeRewind() {
		t.Fatalf("调用顺序 = %v：Bootstrap 必须先于 RewindHours（bootstrap=%d）", fake.order, fake.bootstrapCalls)
	}

	out := buf.String()
	for _, field := range []string{
		`"resolution":"1h"`, `"windowHours":720`, `"devicesScanned":3`, `"cursorsRewound":2`,
		`"replayed":false`,
	} {
		if !strings.Contains(out, field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
	if !strings.Contains(out, "下一轮 rollup") {
		t.Fatalf("日志必须写明「被退回的小时由下一轮 rollup 重算补出」，实际日志：%s", out)
	}
}

// TestAgentMetricsBackfillTaskResolutionFallbackKeeps5mPath 钉住「缺省/非法 → 保持现有 5m 行为」：
// 两种情形都必须走 BackfillOnce、不碰 RewindHours；非法值还要留下一条 Warn（回落不许静默）。
func TestAgentMetricsBackfillTaskResolutionFallbackKeeps5mPath(t *testing.T) {
	cases := []struct {
		name     string
		params   string
		wantWarn bool
	}{
		{"字段缺省", `{"hours":6,"device_ids":[1001]}`, false},
		{"显式 5m", `{"hours":6,"resolution":"5m"}`, false},
		{"无法识别（大小写笔误）", `{"hours":6,"resolution":"1H"}`, true},
		{"无法识别（其它写法）", `{"hours":6,"resolution":"hourly"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAgentService{backfillStats: service.BackfillStats{
				WindowHours: 6, CursorsRewound: 1, DevicesScanned: 1,
				Flush: service.FlushStats{BucketsWritten: 5},
			}}
			lg, buf := newCaptureLogger(t)

			tk := NewAgentMetricsBackfillTask(fake, lg)
			if err := tk.Execute(context.Background(), json.RawMessage(tc.params)); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if fake.backfillCalls != 1 || fake.backfillHours != 6 {
				t.Fatalf("BackfillOnce 调用 = %d 次 / hours=%d, want 1 次 / 6（回落必须走既有 5m 路径）",
					fake.backfillCalls, fake.backfillHours)
			}
			if fake.rewindCalls != 0 {
				t.Fatalf("RewindHours 调用次数 = %d, want 0（这不是 1h 支）", fake.rewindCalls)
			}
			out := buf.String()
			for _, field := range []string{`"resolution":"5m"`, `"windowHours":6`, `"bucketsWritten":5`} {
				if !strings.Contains(out, field) {
					t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
				}
			}
			if got := strings.Contains(out, "无法识别"); got != tc.wantWarn {
				t.Fatalf("「回落 5m」的 Warn 日志存在 = %v, want %v，实际日志：%s", got, tc.wantWarn, out)
			}
		})
	}
}

// TestAgentMetricsBackfillTaskRejectsBadParamsOnEveryResolution 钉住「params 校验先于分支」：
// hours 非法（<=0）时**无论 resolution 是什么**都要报错且不触碰服务 ——
// 否则一个写错的窗口会在 1h 支上静默变成「用满 30 天」，每轮退 30 天再让 rollup 重算一遍。
func TestAgentMetricsBackfillTaskRejectsBadParamsOnEveryResolution(t *testing.T) {
	bad := []string{
		`{"hours":0,"resolution":"1h"}`,
		`{"hours":-3,"resolution":"1h"}`,
		`{"hours":"720","resolution":"1h"}`, // 类型不符
		`{"hours":720,"resolution":"1h","device_ids":[0]}`,
		`{"hours":720,"resolution":`,
	}
	for _, raw := range bad {
		fake := &fakeAgentService{}
		tk := NewAgentMetricsBackfillTask(fake, nil)
		if err := tk.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("params %s 必须报错", raw)
		}
		if fake.bootstrapCalls != 0 || fake.backfillCalls != 0 || fake.rewindCalls != 0 {
			t.Fatalf("params %s 非法时不得触碰服务（bootstrap=%d backfill=%d rewind=%d）",
				raw, fake.bootstrapCalls, fake.backfillCalls, fake.rewindCalls)
		}
	}
}

// TestAgentMetricsBackfillTaskPropagatesRewindError 钉住 1h 支的错误上抛与读数（调度器要记 job_log）。
func TestAgentMetricsBackfillTaskPropagatesRewindError(t *testing.T) {
	errBoomRewind := errors.New("boom: rewind")
	fake := &fakeAgentService{rewindErr: errBoomRewind,
		rewindStats: service.RewindStats{Resolution: agentmetrics.Resolution1h, WindowHours: 720}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsBackfillTask(fake, lg)
	err := tk.Execute(context.Background(), json.RawMessage(`{"hours":720,"resolution":"1h"}`))
	if !errors.Is(err, errBoomRewind) {
		t.Fatalf("err = %v, want %v（调度器据此写 job_log 失败）", err, errBoomRewind)
	}
	out := buf.String()
	// 失败路径同样要留下档位与窗口：只有「什么时候、哪个档位、多大窗口」三者齐全，
	// 才能从 job_log 里还原这一轮到底想做什么。
	for _, field := range []string{`"resolution":"1h"`, `"windowHours":720`, `"replayed":false`} {
		if !strings.Contains(out, field) {
			t.Fatalf("失败日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
}
