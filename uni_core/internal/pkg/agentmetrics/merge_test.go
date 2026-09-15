package agentmetrics

import (
	"reflect"
	"strings"
	"testing"
)

// TestTrendPointColumnsMirrorStructJSONTags 是 S3 的**漂移守卫**：
// 清单必须与 TrendPoint 的 json tag 逐列相等（除 `t`）。
//
//   - 清单多一列 → 又把「表里有、响应装不下」的列报成可用（S3 的原缺陷）；
//   - 清单少一列 → 真能承接的列被谎报成「该档无此指标」，前端会显示
//     「该档位无此指标」而实际有数据。
//
// 反射读 tag 而不手写第二份清单：手写清单本身就会漂移。
func TestTrendPointColumnsMirrorStructJSONTags(t *testing.T) {
	rt := reflect.TypeOf(TrendPoint{})
	want := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" {
			t.Fatalf("TrendPoint.%s 缺 json tag（列名必须显式声明）", f.Name)
		}
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if name == "t" {
			continue // t 是桶键，不是可用指标列（由响应契约的 t 承载）
		}
		want = append(want, name)
	}

	got := TrendPointColumns()
	if len(got) != len(want) {
		t.Fatalf("TrendPointColumns() 有 %d 列, TrendPoint 承接 %d 列:\n got=%v\nwant=%v",
			len(got), len(want), got, want)
	}
	set := make(map[string]bool, len(got))
	for _, c := range got {
		if set[c] {
			t.Fatalf("TrendPointColumns() 有重复列 %q", c)
		}
		set[c] = true
	}
	for _, c := range want {
		if !set[c] {
			t.Fatalf("TrendPoint 承接 %q 但 TrendPointColumns() 未登记（谎报「该档无此指标」）:\n got=%v\nwant=%v",
				c, got, want)
		}
	}
	// 反向：清单多出的列必然是「声明了永远不会有值的列」（S3 的原缺陷）
	for _, c := range got {
		found := false
		for _, w := range want {
			if w == c {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("TrendPointColumns() 声明了 %q，但 TrendPoint 没有字段承接它（恒为 nil 的误导列）", c)
		}
	}

	// 副本语义：调用方改了返回值不得污染清单
	got[0] = "hacked"
	if TrendPointColumns()[0] == "hacked" {
		t.Fatal("TrendPointColumns() 必须返回副本（内部清单不得被调用方改写）")
	}
}

func TestMergeTrendPointsAlignsToStepGrid(t *testing.T) {
	pts := []TrendPoint{
		{T: 900, Samples: 1, CPUUsedPercent: ptr(1)},
		{T: 1200, Samples: 1, CPUUsedPercent: ptr(2)},
		{T: 1800, Samples: 1, CPUUsedPercent: ptr(3)},
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 2 {
		t.Fatalf("归并后桶数 = %d, want 2（900 与 1800）: %+v", len(got), got)
	}
	for i, b := range got {
		if b.T%900 != 0 {
			t.Fatalf("第 %d 桶 t=%d 未对齐到 900 栅格", i, b.T)
		}
	}
	if got[0].T != 900 || got[1].T != 1800 {
		t.Fatalf("桶时间 = [%d %d], want [900 1800]", got[0].T, got[1].T)
	}
}

// TestMergeTrendPointsWeightsGaugesBySamples：均值类必须**按 Samples 加权**，
// 不是简单平均 —— 1h 行的 samples 是完整度信号，低完整度的桶不该与前一个桶等权。
func TestMergeTrendPointsWeightsGaugesBySamples(t *testing.T) {
	pts := []TrendPoint{
		{T: 0, Samples: 30, CPUUsedPercent: ptr(10), MemUsedMB: ptr(1000)},
		{T: 300, Samples: 90, CPUUsedPercent: ptr(20), MemUsedMB: ptr(2000)},
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	// (10*30 + 20*90) / 120 = 17.5；简单平均会是 15
	if got[0].CPUUsedPercent == nil || *got[0].CPUUsedPercent != 17.5 {
		t.Fatalf("cpu 加权均值 = %v, want 17.5（(10*30+20*90)/120；简单平均 15 是错的）",
			got[0].CPUUsedPercent)
	}
	if got[0].MemUsedMB == nil || *got[0].MemUsedMB != 1750 {
		t.Fatalf("mem_used_mb 加权均值 = %v, want 1750", got[0].MemUsedMB)
	}
	if got[0].Samples != 120 {
		t.Fatalf("samples = %d, want 120（求和，完整度信号必须累加）", got[0].Samples)
	}
}

// TestMergeTrendPointsNoSamplesFallsBackToEqualWeight：显式 metrics 不含 samples 时
// 投影里没有该列（Samples=0），此时必须退化为等权平均，不能 0/0 得到 NaN/nil。
func TestMergeTrendPointsNoSamplesFallsBackToEqualWeight(t *testing.T) {
	pts := []TrendPoint{
		{T: 0, CPUUsedPercent: ptr(10)},
		{T: 300, CPUUsedPercent: ptr(20)},
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	if got[0].CPUUsedPercent == nil || *got[0].CPUUsedPercent != 15 {
		t.Fatalf("无 samples 权重时必须等权平均为 15, got %v（nil 说明算了 0/0）", got[0].CPUUsedPercent)
	}
	if got[0].Samples != 0 {
		t.Fatalf("samples = %d, want 0（未投影就是未采集，不得编造）", got[0].Samples)
	}
}

// TestMergeTrendPointsLastSemanticsForMonotonic：uptime 是单调量，必须取 t 最大的值。
func TestMergeTrendPointsLastSemanticsForMonotonic(t *testing.T) {
	// 乱序输入：归并不依赖调用方行序
	pts := []TrendPoint{
		{T: 900, Samples: 1, UptimeSec: iptr(100)},
		{T: 1200, Samples: 1, UptimeSec: iptr(110)},
		{T: 1200, Samples: 1}, // 同 t 的末尾行没有 uptime：不得抹掉已采到的 110
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	if got[0].UptimeSec == nil || *got[0].UptimeSec != 110 {
		t.Fatalf("uptime 应取 t 最大且非 nil 的值 110（不是均值、也不是被末尾 nil 抹掉）, got %v",
			got[0].UptimeSec)
	}
}

// TestMergeTrendPointsTakesMaxTemperature：温度取 max，nil 不参与。
func TestMergeTrendPointsTakesMaxTemperature(t *testing.T) {
	pts := []TrendPoint{
		{T: 0, Samples: 1, MaxTemperatureC: ptr(-5)},
		{T: 300, Samples: 1}, // nil：读不到，不参与
		{T: 600, Samples: 1, MaxTemperatureC: ptr(41)},
		{T: 600, Samples: 1, MaxTemperatureC: ptr(20)},
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	if got[0].MaxTemperatureC == nil || *got[0].MaxTemperatureC != 41 {
		t.Fatalf("max_temperature_c = %v, want 41（取 max，不是均值 18.67）", got[0].MaxTemperatureC)
	}
}

// TestMergeTrendPointsKeepsNilInsteadOfZero：缺 ≠ 0 —— 全 nil 的列归并后仍是 nil，
// 部分 nil 的列只在非 nil 上加权。
func TestMergeTrendPointsKeepsNilInsteadOfZero(t *testing.T) {
	pts := []TrendPoint{
		{T: 0, Samples: 10, CPUUsedPercent: ptr(10)}, // cpu_iowait = nil
		{T: 300, Samples: 30, CPUUsedPercent: ptr(20), CPUIOWait: ptr(8)},
	}
	got := MergeTrendPoints(pts, 900)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	if got[0].CPUIOWait == nil || *got[0].CPUIOWait != 8 {
		t.Fatalf("cpu_iowait = %v, want 8（只在非 nil 的 8 上加权，nil 不得当 0 拉低均值）", got[0].CPUIOWait)
	}
	if got[0].Load1 != nil || got[0].SwapUsedMB != nil || got[0].NICTXBytesSec != nil {
		t.Fatal("全 nil 的列归并后必须仍是 nil（缺 ≠ 0）")
	}
}

// TestMergeTrendPointsSkipsEmptyBucketsAndHandlesEdges：无输入点的桶不产出（稀疏）；
// 空输入/null 步长/step<=1 时不得 panic、不得改变形状。
func TestMergeTrendPointsSkipsEmptyBucketsAndHandlesEdges(t *testing.T) {
	got := MergeTrendPoints([]TrendPoint{
		{T: 0, Samples: 1, CPUUsedPercent: ptr(1)},
		{T: 1800, Samples: 1, CPUUsedPercent: ptr(2)},
	}, 900)
	if len(got) != 2 {
		t.Fatalf("桶数 = %d, want 2（中间的空桶不得被合成）: %+v", len(got), got)
	}
	if got[1].T != 1800 {
		t.Fatalf("第二桶 t = %d, want 1800", got[1].T)
	}

	if out := MergeTrendPoints(nil, 900); out != nil {
		t.Fatalf("空输入必须原样返回 nil, got %+v", out)
	}
	single := []TrendPoint{{T: 7, Samples: 1}}
	if out := MergeTrendPoints(single, 0); len(out) != 1 || out[0].T != 7 {
		t.Fatalf("stepSec<=0 必须原样返回, got %+v", out)
	}
	if out := MergeTrendPoints(single, 1); len(out) != 1 || out[0].T != 7 {
		t.Fatalf("stepSec==1 是恒等变换, got %+v", out)
	}
}
