package agentmetrics

import (
	"math"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

func ptr(v float64) *float64 { return &v }
func iptr(v int64) *int64    { return &v }

// mkSample 造一个可控样本：cpu 与 uptime 由参数给，其余给稳定值。
func mkSample(ts int64, cpu float64, uptime int64) agentproto.MetricsSample {
	s := agentproto.MetricsSample{T: ts, CPUUsedPercent: cpu, UptimeSec: uptime}
	s.Load1, s.Load5, s.Load15 = 1, 2, 3
	s.MemUsedPercent, s.MemUsedMB, s.MemAvailableMB = 50, 1000, 1000
	s.TCPTotal, s.TCPEstablished, s.TCPListen, s.UDPTotal = 10, 4, 1, 2
	s.ProcCount = 100
	return s
}

func TestDownsampleAveragesGauges(t *testing.T) {
	pts := []agentproto.MetricsSample{
		mkSample(1_000_000, 10, 100),
		mkSample(1_005_000, 20, 105),
		mkSample(1_010_000, 30, 110),
	}
	w, _, ok := Downsample(1001, 1_000_000, pts)
	if !ok {
		t.Fatal("有样本时 ok 必须为 true")
	}
	if w.Samples != 3 {
		t.Fatalf("samples = %d, want 3", w.Samples)
	}
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 20 {
		t.Fatalf("cpu 均值 = %v, want 20", w.CPUUsedPercent)
	}
	if w.Load1 == nil || *w.Load1 != 1 {
		t.Fatalf("load1 均值 = %v, want 1", w.Load1)
	}
}

func TestDownsampleUsesLastForMonotonic(t *testing.T) {
	// uptime 是单调量：必须取桶内 T 最大的样本，不能求均值
	pts := []agentproto.MetricsSample{
		mkSample(1_000_000, 10, 100),
		mkSample(1_010_000, 10, 110),
		mkSample(1_005_000, 10, 105), // 故意乱序
	}
	w, _, _ := Downsample(1001, 1_000_000, pts)
	if w.UptimeSec == nil || *w.UptimeSec != 110 {
		t.Fatalf("uptime 应取最新值 110（按 T 最大，不是切片末元素）, got %v", w.UptimeSec)
	}
}

func TestDownsampleSkipsNilInAggregation(t *testing.T) {
	// CPU 是必填（非指针），用可选的 cpu_iowait 验证 nil 不参与均值
	a := mkSample(1_000_000, 10, 100)
	a.CPUIOWait = ptr(10)
	b := mkSample(1_005_000, 10, 100) // CPUIOWait = nil
	c := mkSample(1_010_000, 10, 100)
	c.CPUIOWait = ptr(30)

	w, _, _ := Downsample(1001, 1_000_000, []agentproto.MetricsSample{a, b, c})
	if w.CPUIOWait == nil || *w.CPUIOWait != 20 {
		t.Fatalf("cpu_iowait 均值应为 (10+30)/2=20（nil 跳过）, got %v", w.CPUIOWait)
	}
}

func TestDownsampleEmptyReturnsNotOK(t *testing.T) {
	w, subs, ok := Downsample(1001, 1_000_000, nil)
	if ok {
		t.Fatal("无样本时 ok 必须为 false（调用方据此跳过整个写路径，不写空行）")
	}
	if w.Samples != 0 || len(subs.Disks) != 0 {
		t.Fatalf("无样本时不应产出内容: %+v", w)
	}
}

func TestDownsampleDiskTotalsAndRatioRecompute(t *testing.T) {
	s := mkSample(1_000_000, 10, 100)
	s.Disks = []agentproto.DiskMetric{
		{Mountpoint: "/", FSType: "ext4", TotalGB: 100, UsedGB: 50, UsedPercent: 50},
		{Mountpoint: "/data", FSType: "xfs", TotalGB: 300, UsedGB: 270, UsedPercent: 90},
	}
	w, subs, _ := Downsample(1001, 1_000_000, []agentproto.MetricsSample{s})

	if len(subs.Disks) != 2 {
		t.Fatalf("应产出 2 条磁盘明细, got %d", len(subs.Disks))
	}
	// 整机合计：Σtotal=400, Σused=320
	if w.DiskTotalGB == nil || *w.DiskTotalGB != 400 {
		t.Fatalf("disk_total_gb = %v, want 400", w.DiskTotalGB)
	}
	if w.DiskUsedGB == nil || *w.DiskUsedGB != 320 {
		t.Fatalf("disk_used_gb = %v, want 320", w.DiskUsedGB)
	}
	// 比值必须**由 Σ 重算**（320/400=80），不能平均明细比值（(50+90)/2=70）
	if w.DiskUsedPercent == nil || *w.DiskUsedPercent != 80 {
		t.Fatalf("disk_used_percent = %v, want 80（Σused/Σtotal，而非比值平均 70）", w.DiskUsedPercent)
	}
}

func TestDownsampleMachineTotalsSumRates(t *testing.T) {
	s := mkSample(1_000_000, 10, 100)
	s.DiskIO = []agentproto.DiskIOMetric{
		{Name: "nvme0n1", ReadBytesPerSec: 100, WriteBytesPerSec: 50},
		{Name: "sda", ReadBytesPerSec: 200, WriteBytesPerSec: 25},
	}
	s.NICs = []agentproto.NICMetric{
		{Name: "eth0", RXBytesPerSec: 1000, TXBytesPerSec: 500},
		{Name: "eth1", RXBytesPerSec: 300, TXBytesPerSec: 200},
	}
	s.Sensors = []agentproto.SensorMetric{
		{Name: "tctl", TemperatureC: ptr(54)},
		{Name: "nvme", TemperatureC: ptr(41)},
		{Name: "unknown"}, // 读不到 → nil，不参与 max
	}
	w, subs, _ := Downsample(1001, 1_000_000, []agentproto.MetricsSample{s})

	if w.DiskIOReadBytesSec == nil || *w.DiskIOReadBytesSec != 300 {
		t.Fatalf("整机 IO 读合计 = %v, want 300", w.DiskIOReadBytesSec)
	}
	if w.NICRXBytesSec == nil || *w.NICRXBytesSec != 1300 {
		t.Fatalf("整机网卡收合计 = %v, want 1300", w.NICRXBytesSec)
	}
	// 最高温取 max，nil 不参与
	if w.MaxTemperatureC == nil || *w.MaxTemperatureC != 54 {
		t.Fatalf("max_temperature_c = %v, want 54", w.MaxTemperatureC)
	}
	// 明细按 name 保留，且 nil 温度条目仍保留（表示「有该传感器但读不到」）
	if len(subs.Sensors) != 3 {
		t.Fatalf("传感器明细 = %d 条, want 3", len(subs.Sensors))
	}
	if subs.Sensors[2].TemperatureC != nil {
		t.Fatal("读不到的传感器温度必须保持 nil（不是 0）")
	}
}

func TestDownsampleTemperatureMaxIgnoresNil(t *testing.T) {
	a := mkSample(1_000_000, 10, 100)
	a.Sensors = []agentproto.SensorMetric{{Name: "t", TemperatureC: ptr(-5)}}
	b := mkSample(1_005_000, 10, 100)
	b.Sensors = []agentproto.SensorMetric{{Name: "t"}} // nil
	w, _, _ := Downsample(1001, 1_000_000, []agentproto.MetricsSample{a, b})
	if w.MaxTemperatureC == nil || *w.MaxTemperatureC != -5 {
		t.Fatalf("负温度必须保留（零下是合法值）, got %v", w.MaxTemperatureC)
	}
}

func TestDownsampleCarriesAgentCountersAsLast(t *testing.T) {
	a := mkSample(1_000_000, 10, 100)
	a.Agent = agentproto.AgentMetric{CollectDurationMs: 5, ReportSuccessCount: 10, ReportErrorCount: 0, LastReportError: "old", WSReconnectCount: 1}
	b := mkSample(1_010_000, 10, 100)
	b.Agent = agentproto.AgentMetric{CollectDurationMs: 15, ReportSuccessCount: 12, ReportErrorCount: 1, LastReportError: "new", WSReconnectCount: 2}

	w, _, _ := Downsample(1001, 1_000_000, []agentproto.MetricsSample{a, b})
	if w.AgentReportSuccessCount == nil || *w.AgentReportSuccessCount != 12 {
		t.Fatalf("成功计数应取最新 12, got %v", w.AgentReportSuccessCount)
	}
	if w.AgentCollectDurationMs == nil || math.Abs(*w.AgentCollectDurationMs-10) > 0.01 {
		t.Fatalf("采集耗时是 gauge，应取均值 10, got %v", w.AgentCollectDurationMs)
	}
	if w.AgentLastReportError == nil || *w.AgentLastReportError != "new" {
		t.Fatalf("last_report_error 应取末非空串, got %v", w.AgentLastReportError)
	}
}
