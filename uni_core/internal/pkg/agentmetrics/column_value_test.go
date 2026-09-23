package agentmetrics

import (
	"reflect"
	"testing"
)

// 本文件守卫 ColumnValue / LatestToWatermark 与结构体的字段清单不漂移。
//
// 为什么必须有这条守卫：ColumnValue 里的 switch 是「列名 → 字段」的第三份
// 映射（另两份是 trendPointColumns 与 mergeAcc.add）。三份映射只要有一份漏改，
// 症状是**静默的**：
//   - ColumnValue 漏登记某列 → 总览页少画一条曲线，且该列在
//     available_metrics 里仍然出现（因为 trendPointColumns 有它），
//     前端会显示「有这一列但整列无数据」—— 看起来像「设备没采集」，
//     实际是后端没读取。这是最容易被误判成环境问题的一类缺陷。
//   - LatestToWatermark 漏登记某列 → 快照卡片显示「—」，同样静默。
//
// 故用反射两向比对：列清单里每一列都必须能被 ColumnValue 取到，
// 且取值结果与直接读字段一致。

// TestColumnValueCoversTrendPointColumns 遍历**权威列清单**
// trendPointColumns，逐列断言 ColumnValue 承接它，且取到的值与反射读字段一致。
func TestColumnValueCoversTrendPointColumns(t *testing.T) {
	// 构造一个所有字段都有唯一非零值的 TrendPoint，让「取错字段」无所遁形。
	p := TrendPoint{
		T: 1700000000,
		// 每个字段给一个互不相同的值：若 ColumnValue 的 case 绑定错字段，
		// 断言立刻失败（而不是因为「恰好都是 0」而蒙混过关）。
		CPUUsedPercent: f64p(1), CPUIOWait: f64p(2), Load1: f64p(3), Load5: f64p(4), Load15: f64p(5),
		MemUsedPercent: f64p(6), MemUsedMB: f64p(7), MemAvailableMB: f64p(8),
		SwapUsedPercent: f64p(9), SwapUsedMB: f64p(10),
		TCPTotal: iptr(11), TCPEstablished: iptr(12), TCPListen: iptr(13),
		ProcCount: iptr(14), UptimeSec: iptr(15),
		DiskTotalGB: f64p(16), DiskUsedGB: f64p(17), DiskUsedPercent: f64p(18),
		DiskIOReadBytesSec: f64p(19), DiskIOWriteBytesSec: f64p(20),
		NICRXBytesSec: f64p(21), NICTXBytesSec: f64p(22),
		MaxTemperatureC: f64p(23),
		Samples:         24,
	}
	// 列名 → 期望值（与上面的赋值一一对应）。
	want := map[string]float64{
		"cpu_used_percent": 1, "cpu_iowait": 2, "load1": 3, "load5": 4, "load15": 5,
		"mem_used_percent": 6, "mem_used_mb": 7, "mem_available_mb": 8,
		"swap_used_percent": 9, "swap_used_mb": 10,
		"tcp_total": 11, "tcp_established": 12, "tcp_listen": 13,
		"proc_count": 14, "uptime_sec": 15,
		"disk_total_gb": 16, "disk_used_gb": 17, "disk_used_percent": 18,
		"disk_io_read_bytes_sec": 19, "disk_io_write_bytes_sec": 20,
		"nic_rx_bytes_sec": 21, "nic_tx_bytes_sec": 22,
		"max_temperature_c": 23,
	}

	for _, col := range TrendPointColumns() {
		if col == "samples" {
			// samples 是完整度信号（归并权重），**不是**可绘制的指标列：
			// 它刻意不出现在 ColumnValue 里。总览的 available_metrics 也
			// 不会包含它（除非调用方显式请求，而此时它是普通数值列）。
			// 这里显式跳过并留下记录，避免后来者以为「漏了一列」。
			continue
		}
		exp, ok := want[col]
		if !ok {
			t.Fatalf("列 %q 在 trendPointColumns 里，但本测试没有为它登记期望值："+
				"新增列时必须同时更新 ColumnValue、mergeAcc.add 与本表", col)
		}
		got, carried := ColumnValue(p, col)
		if !carried {
			t.Fatalf("ColumnValue 不承接列 %q —— 该列在 trendPointColumns 里存在，"+
				"会在 available_metrics 里出现却永远取不到值（总览页表现为「整列无数据」）", col)
		}
		if got == nil {
			t.Fatalf("列 %q 取到 nil，但测试样本该字段非空（绑定到错误字段？）", col)
		}
		if *got != exp {
			t.Fatalf("列 %q = %v, want %v（ColumnValue 绑定到了错误的字段）", col, *got, exp)
		}
	}
}

// TestColumnValueRejectsUnknown 未知列必须返回 carried=false，
// 让调用方能区分「该档不产此列」（应报 400）与「该桶无数据」（画空洞）。
func TestColumnValueRejectsUnknown(t *testing.T) {
	for _, col := range []string{"", "bucket_ts", "not_a_column", "CPU_USED_PERCENT", "tcp_time_wait"} {
		if _, carried := ColumnValue(TrendPoint{}, col); carried {
			t.Fatalf("列 %q 不应被承接（tcp_time_wait 属于「宽表有、TrendPoint 装不下」的列）", col)
		}
	}
}

// TestColumnValueNilPassthrough 未采集的列必须透传 nil，
// **绝不**变成 0 —— 这是全项目最硬的口径（见 DeviceMetricPoint 注释）。
func TestColumnValueNilPassthrough(t *testing.T) {
	var p TrendPoint
	for _, col := range []string{"cpu_used_percent", "load1", "tcp_total", "uptime_sec", "max_temperature_c"} {
		got, carried := ColumnValue(p, col)
		if !carried {
			t.Fatalf("列 %q 应被承接", col)
		}
		if got != nil {
			t.Fatalf("零值 TrendPoint 的列 %q 应为 nil（未采集），实得 %v", col, *got)
		}
	}
}

// TestLatestToWatermarkCoversFields 水位投影必须覆盖 LatestSummary 的
// **每一个**指标字段。漏一个 → 总览的快照卡片静默显示「—」。
//
// 用反射读 LatestSummary 的字段名来驱动断言，故新增水位字段而忘了在
// LatestToWatermark 登记时，本测试会直接点出字段名。
func TestLatestToWatermarkCoversFields(t *testing.T) {
	// 每个字段给唯一值，且与列名一一对应（列名 = 字段的 json tag 去掉 omitempty）。
	w := &LatestSummary{
		T:              1700000000000,
		CPUUsedPercent: f64p(1), Load1: f64p(2),
		MemUsedPercent: f64p(3), MemUsedMB: f64p(4),
		DiskTotalGB: f64p(5), DiskUsedGB: f64p(6), DiskUsedPercent: f64p(7),
		NICRXBytesSec: f64p(8), NICTXBytesSec: f64p(9),
		MaxTemperatureC: f64p(10),
	}
	got := LatestToWatermark(w)

	// 用反射枚举结构体字段，读每个字段的 json tag 作为期望列名。
	rt := reflect.TypeOf(*w)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		// 去掉 ",omitempty"
		name := tag
		if idx := indexByte(tag, ','); idx >= 0 {
			name = tag[:idx]
		}
		if name == "t" {
			// t 是采样时刻（由 watermarkAt 单独承接为 unix 秒），不是指标列。
			continue
		}
		if _, ok := got[name]; !ok {
			t.Fatalf("字段 %s（json tag %q）未出现在 LatestToWatermark 的输出里 —— "+
				"总览页的快照图表会静默显示「—」", f.Name, tag)
		}
	}
	if len(got) != 10 {
		t.Fatalf("水位列数 = %d, want 10（实得键集 %v）", len(got), got)
	}
}

// TestLatestToWatermarkNilAndEmpty nil 水位与空水位都必须返回 nil，
// 而不是一个空 map —— 让消费方用一次 nil 判断就能显示「无快照」。
func TestLatestToWatermarkNilAndEmpty(t *testing.T) {
	if got := LatestToWatermark(nil); got != nil {
		t.Fatalf("nil 水位应返回 nil，实得 %v", got)
	}
	if got := LatestToWatermark(&LatestSummary{T: 1700000000000}); got != nil {
		t.Fatalf("全空水位应返回 nil（而非空 map），实得 %v", got)
	}
}

// ── 小工具（避免引入 strings 只为一次 IndexByte）──────────────

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
