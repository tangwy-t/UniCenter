package agentmetrics

import (
	"sort"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ─ 夹具 ────────────────────────────────────────────
//
// 注意：包内已有 ptr / iptr / mkSample / f64p（后者是 downsample.go 的生产函数），
// 这里的夹具取独立名字，避免与既有测试冲突。

func drillDisk(mount string, usedPercent, usedGB, totalGB, inodes float64) agentproto.DiskMetric {
	return agentproto.DiskMetric{
		Mountpoint: mount, FSType: "ext4",
		UsedPercent: usedPercent, UsedGB: usedGB, TotalGB: totalGB,
		InodesUsedPercent: inodes,
	}
}

func drillIO(name string, read, write, readOps, writeOps, ioTime float64) agentproto.DiskIOMetric {
	return agentproto.DiskIOMetric{
		Name: name, ReadBytesPerSec: read, WriteBytesPerSec: write,
		ReadOpsPerSec: readOps, WriteOpsPerSec: writeOps, IOTimePercent: ioTime,
	}
}

func drillNIC(name string, v ...float64) agentproto.NICMetric {
	n := agentproto.NICMetric{Name: name}
	if len(v) > 0 {
		n.RXBytesPerSec, n.TXBytesPerSec = v[0], v[1]
	}
	if len(v) > 2 {
		n.RXPacketsPerSec, n.TXPacketsPerSec = v[2], v[3]
	}
	if len(v) > 4 {
		n.RXErrorsPerSec, n.TXErrorsPerSec, n.RXDroppedPerSec = v[4], v[5], v[6]
	}
	return n
}

// checkResourceVal 断言某桶的某列存在且等于 want（nil 即失败 —— 缺 ≠ 0）。
func checkResourceVal(t *testing.T, p ResourcePoint, col string, want float64) {
	t.Helper()
	got, ok := p.Values[col]
	if !ok || got == nil {
		t.Fatalf("t=%d 的桶缺少列 %q（values=%v）", p.T, col, p.Values)
	}
	if *got != want {
		t.Fatalf("t=%d 的 %s = %v, want %v", p.T, col, *got, want)
	}
}

func resourceValueKeys(p ResourcePoint) []string {
	out := make([]string, 0, len(p.Values))
	for k, v := range p.Values {
		if v != nil {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ── 过滤 + 均值 ──────────────────────────────────────

// TestAggregateResourceFiltersByNameAndAverages：先按 kind+name 挑出该资源，
// 同桶内求均值；**别的资源的值不得串进来**。
func TestAggregateResourceFiltersByNameAndAverages(t *testing.T) {
	const bucketSec = 300
	pts := []agentproto.MetricsSample{
		// 1000s / 1010s 同属 (1000/300)*300 = 900 这个桶
		{T: 1_000_000, Disks: []agentproto.DiskMetric{drillDisk("/", 10, 10, 100, 5)}},
		{T: 1_010_000, Disks: []agentproto.DiskMetric{
			drillDisk("/data", 99, 90, 100, 0), // 不得污染 "/"
			drillDisk("/", 20, 20, 100, 15),
		}},
	}

	got := AggregateResource(ResourceKindDisk, "/", pts, bucketSec)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1（两个样本同桶）: %+v", len(got), got)
	}
	p := got[0]
	if p.T != 900 {
		t.Fatalf("桶时间 = %d, want 900（T/1000/bucketSec*bucketSec 对齐）", p.T)
	}
	if p.Samples != 2 {
		t.Fatalf("samples = %d, want 2（命中该资源的样本数）", p.Samples)
	}
	checkResourceVal(t, p, "used_percent", 15) // mean(10,20)，不含 /data 的 99
	checkResourceVal(t, p, "used_gb", 15)
	checkResourceVal(t, p, "total_gb", 100)
	checkResourceVal(t, p, "inodes_used_percent", 10) // mean(5,15)
}

// TestAggregateResourceTemperatureUsesMaxAndSkipsNil：温度取 max，nil 读数不参与；
// 桶内一次有效读数都没有时不产出该列（缺 ≠ 0，也不产出 0）。
func TestAggregateResourceTemperatureUsesMaxAndSkipsNil(t *testing.T) {
	pts := []agentproto.MetricsSample{
		{T: 600_000, Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: nil}}},
		{T: 610_000, Sensors: []agentproto.SensorMetric{
			{Name: "coretemp", TemperatureC: ptr(41.5)},
			{Name: "nvme", TemperatureC: ptr(70)}, // 别的传感器不得串进来
		}},
		{T: 620_000, Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: ptr(39)}}},
	}

	got := AggregateResource(ResourceKindSensor, "coretemp", pts, 300)
	if len(got) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(got))
	}
	if got[0].Samples != 3 {
		t.Fatalf("samples = %d, want 3（nil 读数也算「该资源命中」）", got[0].Samples)
	}
	checkResourceVal(t, got[0], "temperature_c", 41.5) // max(41.5,39)，nil 跳过

	// 全部读数都是 nil → 该桶不产出 temperature_c（而不是 0）
	allNil := []agentproto.MetricsSample{{T: 600_000, Sensors: []agentproto.SensorMetric{{Name: "coretemp"}}}}
	got = AggregateResource(ResourceKindSensor, "coretemp", allNil, 300)
	if len(got) != 1 {
		t.Fatalf("命中资源的样本必须产桶, got %d", len(got))
	}
	if v, ok := got[0].Values["temperature_c"]; ok {
		t.Fatalf("无有效读数不得产出 temperature_c, got %v", *v)
	}
}

// TestAggregateResourceBucketsAreSparseAndAligned：空桶不产行（与整机趋势一致）。
func TestAggregateResourceBucketsAreSparseAndAligned(t *testing.T) {
	pts := []agentproto.MetricsSample{
		{T: 1_000_000, Disks: []agentproto.DiskMetric{drillDisk("/", 10, 1, 10, 1)}}, // 1000s → 900
		{T: 1_600_000, Disks: []agentproto.DiskMetric{drillDisk("/", 30, 3, 10, 3)}}, // 1600s → 1500
	}
	got := AggregateResource(ResourceKindDisk, "/", pts, 300)
	if len(got) != 2 {
		t.Fatalf("桶数 = %d, want 2（中间的空桶不产出）", len(got))
	}
	if got[0].T != 900 || got[1].T != 1500 {
		t.Fatalf("桶时间 = [%d %d], want [900 1500]（升序、对齐到 bucketSec）", got[0].T, got[1].T)
	}
	checkResourceVal(t, got[1], "used_percent", 30)
}

func TestAggregateResourceNoMatchReturnsEmpty(t *testing.T) {
	pts := []agentproto.MetricsSample{
		{T: 1_000_000, Disks: []agentproto.DiskMetric{drillDisk("/", 10, 1, 10, 1)}},
	}
	if got := AggregateResource(ResourceKindDisk, "/nope", pts, 300); len(got) != 0 {
		t.Fatalf("资源名不匹配必须返回空, got %+v", got)
	}
	if got := AggregateResource(ResourceKindNIC, "/", pts, 300); len(got) != 0 {
		t.Fatalf("kind 不匹配必须返回空, got %+v", got)
	}
	if got := AggregateResource("gpu", "0", pts, 300); len(got) != 0 {
		t.Fatalf("未知 kind 必须返回空而不是 panic, got %+v", got)
	}
	if got := AggregateResource(ResourceKindDisk, "/", nil, 300); len(got) != 0 {
		t.Fatalf("无样本必须返回空, got %+v", got)
	}
	// bucketSec <= 0 不得除零
	if got := AggregateResource(ResourceKindDisk, "/", pts, 0); len(got) != 0 {
		t.Fatalf("bucketSec=0 必须返回空（不得除零）, got %+v", got)
	}
}

// TestAggregateResourceExposesKindColumns：每种 kind 产出的键集**恰好**是该子表的
// 值列集（service 侧的 available_metrics 就来自子表登记，两档必须逐字一致）。
func TestAggregateResourceExposesKindColumns(t *testing.T) {
	cases := []struct {
		kind     string
		name     string
		payload  agentproto.MetricsSample
		wantKeys []string
	}{
		{
			kind: ResourceKindDisk, name: "/",
			payload: agentproto.MetricsSample{T: 1_000_000,
				Disks: []agentproto.DiskMetric{drillDisk("/", 1, 2, 3, 4)}},
			wantKeys: []string{"inodes_used_percent", "total_gb", "used_gb", "used_percent"},
		},
		{
			kind: ResourceKindDiskIO, name: "sda",
			payload: agentproto.MetricsSample{T: 1_000_000,
				DiskIO: []agentproto.DiskIOMetric{drillIO("sda", 1, 2, 3, 4, 5)}},
			wantKeys: []string{"io_time_percent", "read_bytes_per_sec", "read_ops_per_sec",
				"write_bytes_per_sec", "write_ops_per_sec"},
		},
		{
			kind: ResourceKindNIC, name: "eth0",
			payload: agentproto.MetricsSample{T: 1_000_000,
				NICs: []agentproto.NICMetric{drillNIC("eth0", 1, 2, 3, 4, 5, 6, 7)}},
			wantKeys: []string{"rx_bytes_per_sec", "rx_dropped_per_sec", "rx_errors_per_sec",
				"rx_packets_per_sec", "tx_bytes_per_sec", "tx_errors_per_sec", "tx_packets_per_sec"},
		},
		{
			kind: ResourceKindSensor, name: "coretemp",
			payload: agentproto.MetricsSample{T: 1_000_000,
				Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: ptr(42)}}},
			wantKeys: []string{"temperature_c"},
		},
	}

	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			got := AggregateResource(c.kind, c.name, []agentproto.MetricsSample{c.payload}, 300)
			if len(got) != 1 {
				t.Fatalf("%s: 桶数 = %d, want 1", c.kind, len(got))
			}
			if got[0].Samples != 1 {
				t.Fatalf("%s: samples = %d, want 1", c.kind, got[0].Samples)
			}
			keys := resourceValueKeys(got[0])
			if len(keys) != len(c.wantKeys) {
				t.Fatalf("%s: 值列集 = %v, want %v", c.kind, keys, c.wantKeys)
			}
			for i := range keys {
				if keys[i] != c.wantKeys[i] {
					t.Fatalf("%s: 值列集 = %v, want %v", c.kind, keys, c.wantKeys)
				}
			}
		})
	}
}

// TestAggregateResourceZeroOnlyForOptionalColumns：proto 里带 omitempty 的可选列
// （inodes_used_percent / io_time_percent）的 0 与「未采集」在 JSON 往返后不可区分，
// 写路径也是这么处理的（diskAcc.inodesCnt / diskioAcc.ioTimeCnt 只在非 0 时累加）——
// 热层必须同口径，否则同一次下钻在 1h 与 7d 会给出不同的值。
//
// 反例同样重要：**网卡各列的 0 是真实值**（写路径无条件累计），不得被当成缺失。
func TestAggregateResourceZeroOnlyForOptionalColumns(t *testing.T) {
	disk := AggregateResource(ResourceKindDisk, "/",
		[]agentproto.MetricsSample{{T: 1_000_000, Disks: []agentproto.DiskMetric{drillDisk("/", 50, 5, 10, 0)}}}, 300)
	if len(disk) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(disk))
	}
	if v, ok := disk[0].Values["inodes_used_percent"]; ok {
		t.Fatalf("inodes 全 0 必须视为未采集（不产出该列）, got %v", *v)
	}
	checkResourceVal(t, disk[0], "used_percent", 50)

	io := AggregateResource(ResourceKindDiskIO, "sda",
		[]agentproto.MetricsSample{{T: 1_000_000, DiskIO: []agentproto.DiskIOMetric{drillIO("sda", 1, 2, 3, 4, 0)}}}, 300)
	if v, ok := io[0].Values["io_time_percent"]; ok {
		t.Fatalf("io_time 全 0 必须视为未采集, got %v", *v)
	}
	checkResourceVal(t, io[0], "read_bytes_per_sec", 1)

	nic := AggregateResource(ResourceKindNIC, "eth0",
		[]agentproto.MetricsSample{{T: 1_000_000, NICs: []agentproto.NICMetric{drillNIC("eth0", 1, 2, 3, 4, 0, 0, 0)}}}, 300)
	checkResourceVal(t, nic[0], "rx_errors_per_sec", 0)  // 0 是真实值（缺 ≠ 0，但 0 ≠ 缺）
	checkResourceVal(t, nic[0], "rx_dropped_per_sec", 0) // 同上：恒有值的列不得被省略
	checkResourceVal(t, nic[0], "rx_bytes_per_sec", 1)
}
