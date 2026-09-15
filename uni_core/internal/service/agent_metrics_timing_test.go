package service

import (
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// TestGraceDerivationSharedByFlushAndRollup 钉住 S7 的「宽限推导只此一处」。
//
// 为什么这值得一条断言：宽限决定「当前桶/当前小时什么时候算闭」，而 5m 与 1h 是
// 同一条数据链的上下游（1h 的输入就是 5m 的产物）。两份实现漂移时的症状是
// 「某些小时被判成残缺 / 整小时空洞」，而两个服务各自的单测**都还是绿的**
// （各自只与自己的那份实现自洽）。故这里同时断言三件事：
//
//  1. 宽限 = 2 × 上报间隔（唯一推导式）；
//  2. 配置缺失/非法都回落到同一个默认值（10s → 20s 宽限）；
//  3. **两个服务用同一个宽限值**：它们的上界都必须等于 `alignDown(now − grace, 档位宽)`
//     —— 用同一个桩配置算出来的两个上界，只能由同一个 grace 推出。
func TestGraceDerivationSharedByFlushAndRollup(t *testing.T) {
	// now 同时是 300 与 3600 的整数倍，上界可直接逐字算出。
	now := time.Unix(1800000000, 0)

	cases := []struct {
		name      string
		reportSec int // 0 与负数走 fakeConfig 的「回落默认值」路径
		wantGrace time.Duration
	}{
		{"配置缺失（回落默认 10s）", 0, 20 * time.Second},
		{"种子值 10s", 10, 20 * time.Second},
		{"热更到 300s", 300, 600 * time.Second},
		{"非法值 -5 回落默认", -5, 20 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := fakeConfig{reportSec: c.reportSec}
			if got := agentCloseGrace(cfg); got != c.wantGrace {
				t.Fatalf("agentCloseGrace = %v, want %v（= 2 × 上报间隔）", got, c.wantGrace)
			}
			if got := 2 * agentReportInterval(cfg); got != c.wantGrace {
				t.Fatalf("2×agentReportInterval = %v, want %v（两个函数必须是同一条推导）", got, c.wantGrace)
			}

			flush := NewAgentMetricsFlushService(nil, nil, nil, cfg, nil).
				WithClock(func() time.Time { return now })
			rollup := NewAgentMetricsRollupService(nil, nil, cfg, nil).
				WithClock(func() time.Time { return now })

			wantFlush := alignDown(now.Add(-c.wantGrace).Unix(), resolutionSeconds(agentmetrics.Resolution5m))
			wantRollup := alignDown(now.Add(-c.wantGrace).Unix(), resolutionSeconds(agentmetrics.Resolution1h))
			if got := flush.closedBucketUpper(); got != wantFlush {
				t.Fatalf("flush 的已闭上界 = %d, want %d（= now − %v 对齐到 5min）", got, wantFlush, c.wantGrace)
			}
			if got := rollup.closedHourUpper(); got != wantRollup {
				t.Fatalf("rollup 的已闭上界 = %d, want %d（= now − %v 对齐到 1h；两个服务的宽限必须同源）",
					got, wantRollup, c.wantGrace)
			}
		})
	}
}
