package agentmetrics

import (
	"testing"
	"time"
)

func wideForHour(ts int64, samples int, cpu float64, uptime int64) Wide {
	w := Wide{DeviceID: 1001, BucketTS: ts, Samples: samples}
	w.CPUUsedPercent = f64p(cpu)
	w.UptimeSec = &uptime
	w.TCPTotal = iptrLocal(100)
	w.TCPTimeWait = iptrLocal(7) // 5min-only 列
	w.AgentReportSuccessCount = &uptime
	w.MemUsedMB = f64p(1000)
	return w
}

func iptrLocal(v int64) *int64 { return &v }

func TestRollupWeightsMeanBySamples(t *testing.T) {
	// 两个 5min 桶：一个 30 个样本 cpu=10，一个 10 个样本 cpu=50
	// 未加权平均 = 30；按 samples 加权 = (10*30 + 50*10)/40 = 20
	rows := []Wide{
		wideForHour(3600, 30, 10, 100),
		wideForHour(3900, 10, 50, 200),
	}
	// 整机 Σ 列（磁盘 IO / 网卡合计）同一条加权路径：加权 250 ≠ 未加权 200 ≠ 相加 400
	rows[0].DiskIOReadBytesSec, rows[0].NICRXBytesSec = f64p(300), f64p(300)
	rows[1].DiskIOReadBytesSec, rows[1].NICRXBytesSec = f64p(100), f64p(100)

	w, info := RollupToHour(7200, rows)
	if info.RowCount != 2 {
		t.Fatalf("RowCount = %d, want 2", info.RowCount)
	}
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 20 {
		t.Fatalf("cpu 应为按 samples 加权的 20（未加权会是 30）, got %v", w.CPUUsedPercent)
	}
	if w.DiskIOReadBytesSec == nil || *w.DiskIOReadBytesSec != 250 {
		t.Fatalf("disk_io_read_bytes_sec 应按 samples 加权得 (300*30+100*10)/40=250（未加权 200、相加 400）, got %v", w.DiskIOReadBytesSec)
	}
	if w.NICRXBytesSec == nil || *w.NICRXBytesSec != 250 {
		t.Fatalf("nic_rx_bytes_sec 应按 samples 加权得 250（未加权 200、相加 400）, got %v", w.NICRXBytesSec)
	}
	if w.Samples != 40 {
		t.Fatalf("samples 应为 Σ=40, got %d", w.Samples)
	}
	if w.BucketTS != 7200 {
		t.Fatalf("1h 行的 bucket_ts 必须是传入的小时桶, got %d", w.BucketTS)
	}
}

func TestRollupTakesLastForMonotonicAndNilsFiveMinOnly(t *testing.T) {
	rows := []Wide{
		wideForHour(3600, 30, 10, 100),
		wideForHour(3900, 30, 20, 250),
	}
	w, _ := func() (Wide, RollupInfo) { return RollupToHour(7200, rows) }()
	if w.UptimeSec == nil || *w.UptimeSec != 250 {
		t.Fatalf("uptime 是单调量，应取最新 250, got %v", w.UptimeSec)
	}
	// LAST 必须按 bucket_ts 判定，与切片顺序无关：反向输入仍是 250（切片末元素是 100）
	rev, _ := RollupToHour(7200, []Wide{rows[1], rows[0]})
	if rev.UptimeSec == nil || *rev.UptimeSec != 250 {
		t.Fatalf("乱序输入下 uptime 仍应取 bucket_ts 最大的 250（不是切片末元素 100）, got %v", rev.UptimeSec)
	}
	if w.TCPTimeWait != nil {
		t.Fatal("tcp_time_wait 是 5min-only 列，1h 行必须置 nil")
	}
	if w.AgentReportSuccessCount != nil {
		t.Fatal("agent_* 是 5min-only 列，1h 行必须置 nil")
	}
	// 逐一核实**全部**剩余 5min-only 列（tcp_close_wait + 其余 agent_*）：
	// 1h 行不产这些列，任何一个被填上都说明实现越界写入了 5min 专属语义
	for _, tc := range []struct {
		name string
		set  bool
	}{
		{"tcp_close_wait", w.TCPCloseWait != nil},
		{"agent_collect_duration_ms", w.AgentCollectDurationMs != nil},
		{"agent_report_error_count", w.AgentReportErrorCount != nil},
		{"agent_ws_reconnect_count", w.AgentWSReconnectCount != nil},
		{"agent_last_report_error", w.AgentLastReportError != nil},
		{"agent_mem_resident_mb", w.AgentMemResidentMB != nil},
		{"agent_pending_backlog", w.AgentPendingBacklog != nil},
		{"agent_last_report_latency_ms", w.AgentLastReportLatencyMs != nil},
		{"agent_report_drop_count", w.AgentReportDropCount != nil},
		{"agent_uptime_sec", w.AgentUptimeSec != nil},
	} {
		if tc.set {
			t.Fatalf("%s 是 5min-only 列，1h 行必须置 nil", tc.name)
		}
	}
}

func TestRollupDetectsStaleness(t *testing.T) {
	// 期望 12 行 / 360 个样本；只给 7 行 → 需要 repair
	rows := make([]Wide, 0, 7)
	for i := 0; i < 7; i++ {
		rows = append(rows, wideForHour(3600+int64(i)*300, 30, 10, int64(100+i)))
	}
	_, info := RollupToHour(7200, rows)
	if info.RowCount != 7 || info.SampleSum != 210 {
		t.Fatalf("统计不符: rows=%d samples=%d", info.RowCount, info.SampleSum)
	}
	if !info.NeedRepair {
		t.Fatal("5min 行数不足（<12）时必须标记 NeedRepair，供协调器后续补算")
	}

	full := make([]Wide, 0, 12)
	for i := 0; i < 12; i++ {
		full = append(full, wideForHour(3600+int64(i)*300, 30, 10, int64(100+i)))
	}
	_, info2 := RollupToHour(7200, full)
	if info2.NeedRepair {
		t.Fatalf("12 行 / 360 样本不应标记 NeedRepair（rows=%d samples=%d）", info2.RowCount, info2.SampleSum)
	}
}

func TestRollupEmptyReturnsNotRepairable(t *testing.T) {
	w, info := RollupToHour(7200, nil)
	if info.RowCount != 0 || info.SampleSum != 0 {
		t.Fatalf("空输入统计应为 0: %+v", info)
	}
	if w.Samples != 0 {
		t.Fatal("空输入不应产出内容")
	}
}

func TestExpectedSamplesPerHour(t *testing.T) {
	if got := ExpectedSamplesPerHour(10 * time.Second); got != 360 {
		t.Fatalf("10s 上报间隔 → 每小时 360 个样本, got %d", got)
	}
	if got := ExpectedSamplesPerHour(2 * time.Second); got != 1800 {
		t.Fatalf("2s → 1800, got %d", got)
	}
	if got := ExpectedSamplesPerHour(0); got != 0 {
		t.Fatalf("非法间隔应返回 0（调用方据此跳过完整度判断）, got %d", got)
	}
}

func TestRollupRatioRecomputeFromSums(t *testing.T) {
	a := wideForHour(3600, 30, 10, 100)
	a.DiskTotalGB, a.DiskUsedGB = f64p(100), f64p(50) // 比值 50%
	b := wideForHour(3900, 30, 10, 100)
	b.DiskTotalGB, b.DiskUsedGB = f64p(300), f64p(270) // 比值 90%

	w, _ := RollupToHour(7200, []Wide{a, b})
	// 整机 Σ 列在 1h 行按 samples 加权平均（两行各 30 样本 → 等权均值）：
	// total=(100+300)/2=200、used=(50+270)/2=160。
	// 注意：这两列**不是**跨桶相加（那会给 400/320，等于把一台 400GB 的机器
	// 在 1h 图上放大成 4800GB）；与 Task 3 的 downsample 一致——5m 行的这两列
	// 本身已是「同桶明细 Σ 后再对样本取均值」的聚合值，不是可累加的计数器。
	if w.DiskTotalGB == nil || *w.DiskTotalGB != 200 || w.DiskUsedGB == nil || *w.DiskUsedGB != 160 {
		t.Fatalf("disk Σ 列应按 samples 加权平均: total=%v（want 200，跨桶相加会得 400）used=%v（want 160，相加会得 320）", w.DiskTotalGB, w.DiskUsedGB)
	}
	if w.DiskUsedPercent == nil || *w.DiskUsedPercent != 80 {
		t.Fatalf("1h 的 disk_used_percent 必须由 1h 的 Σ 列重算得 80（平均两条 5m 比值会是 70）, got %v", w.DiskUsedPercent)
	}
}
