package agentmetrics

import (
	"sort"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// AggregateResource 把原始样本按 `kind`+`name` 过滤并按 `bucketSec` 对齐分桶，
// 汇总成**单资源**的下钻曲线（D7：range ≤ 24h 的下钻走热层，不落 DB 子表）。
//
// 纯函数：不碰 Redis/DB，便于表驱动单测。
//
// 过滤：每个样本先在 `Disks`/`DiskIO`/`NICs`/`Sensors` 里挑出 name 命中的那一条
// （`kind` 决定看哪个列表）；**没命中的样本不进任何桶**。
//
// 分桶：`T(ms)/1000/bucketSec*bucketSec`，与整机趋势共用同一套桶栅格，
// 故切档时 1h（热层）与 7d（子表）的 `t` 落在同一网格上。
//
// 聚合口径**与 downsample.go 的 *Acc 完全一致**（同一次下钻在两个档位上必须给出
// 同一个值，否则前端切档会看到曲线跳变）：
//   - 速率 / 百分比 / 容量 → **均值**（缺的样本不参与，绝不当作 0）
//   - 温度 → **max**（nil 读数不参与）
//   - `samples` → 命中该资源的样本数
//   - **无命中样本的桶不产出**（稀疏：空桶不产行，前端必须用 `t` 定位）
func AggregateResource(kind, name string, pts []agentproto.MetricsSample, bucketSec int64) []ResourcePoint {
	if bucketSec <= 0 || name == "" || len(pts) == 0 {
		return nil
	}

	accs := make(map[int64]*resourceAcc, 8)
	for i := range pts {
		cols, ok := resourceColumns(kind, name, &pts[i])
		if !ok {
			continue // 该样本里没有这个资源 → 不参与任何桶（也不计入 samples）
		}
		bs := pts[i].T / 1000 / bucketSec * bucketSec
		a := accs[bs]
		if a == nil {
			a = &resourceAcc{}
			accs[bs] = a
		}
		a.add(cols)
	}
	if len(accs) == 0 {
		return nil
	}

	buckets := make([]int64, 0, len(accs))
	for bs := range accs {
		buckets = append(buckets, bs)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })

	out := make([]ResourcePoint, 0, len(buckets))
	for _, bs := range buckets {
		out = append(out, accs[bs].finish(bs))
	}
	return out
}

// resourceColumns 取出某个样本里 `kind`+`name` 命中资源的列值。
// 返回 ok=false 表示该样本没有这个资源（调用方跳过）。
//
// 键名与 4 张子表的列名**逐字一致**（device_metric_schema.go 的 DDL），
// 由 service 包的 TestResourceDrillColumnsMatchHotLayerAggregation 守卫。
func resourceColumns(kind, name string, s *agentproto.MetricsSample) (map[string]*float64, bool) {
	switch kind {
	case ResourceKindDisk:
		for i := range s.Disks {
			d := &s.Disks[i]
			if d.Mountpoint != name {
				continue
			}
			return map[string]*float64{
				"used_percent":        f64val(d.UsedPercent),
				"used_gb":             f64val(d.UsedGB),
				"total_gb":            f64val(d.TotalGB),
				"inodes_used_percent": optionalF64(d.InodesUsedPercent),
			}, true
		}
	case ResourceKindDiskIO:
		for i := range s.DiskIO {
			d := &s.DiskIO[i]
			if d.Name != name {
				continue
			}
			return map[string]*float64{
				"read_bytes_per_sec":  f64val(d.ReadBytesPerSec),
				"write_bytes_per_sec": f64val(d.WriteBytesPerSec),
				"read_ops_per_sec":    f64val(d.ReadOpsPerSec),
				"write_ops_per_sec":   f64val(d.WriteOpsPerSec),
				"io_time_percent":     optionalF64(d.IOTimePercent),
			}, true
		}
	case ResourceKindNIC:
		for i := range s.NICs {
			n := &s.NICs[i]
			if n.Name != name {
				continue
			}
			// 网卡各列在 proto 里都是必填（无 omitempty），0 是**真实值**，
			// 故一律 f64val（写路径的 nicAcc 也无条件累加）。
			return map[string]*float64{
				"rx_bytes_per_sec":   f64val(n.RXBytesPerSec),
				"tx_bytes_per_sec":   f64val(n.TXBytesPerSec),
				"rx_packets_per_sec": f64val(n.RXPacketsPerSec),
				"tx_packets_per_sec": f64val(n.TXPacketsPerSec),
				"rx_errors_per_sec":  f64val(n.RXErrorsPerSec),
				"tx_errors_per_sec":  f64val(n.TXErrorsPerSec),
				"rx_dropped_per_sec": f64val(n.RXDroppedPerSec),
			}, true
		}
	case ResourceKindSensor:
		for i := range s.Sensors {
			sm := &s.Sensors[i]
			if sm.Name != name {
				continue
			}
			cols := map[string]*float64{}
			if sm.TemperatureC != nil {
				cols["temperature_c"] = f64val(*sm.TemperatureC)
			}
			return cols, true // 命中资源但读数 nil → 桶存在、该列不出现（缺 ≠ 0）
		}
	}
	return nil, false
}

// optionalF64 处理 proto 里带 `omitempty` 的**可选列**：这类字段的 0 与「未采集」
// 在 JSON 往返后不可区分（agent 端零值不会序列化），故 0 一律视为缺（nil），
// **与写路径同口径**（downsample.go 的 diskAcc.inodesCnt / diskioAcc.ioTimeCnt
// 也只在非 0 时累加并计数，全 0 时该列在子表里是 NULL）。
func optionalF64(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return f64val(v)
}

func f64val(v float64) *float64 { return &v }

// resourceAcc 是单资源桶的累加器（均值 + 温度 max）。
type resourceAcc struct {
	samples int
	sums    map[string]float64
	cnts    map[string]int
	maxTemp *float64
}

func (a *resourceAcc) add(cols map[string]*float64) {
	a.samples++
	if a.sums == nil {
		a.sums = make(map[string]float64, 8)
		a.cnts = make(map[string]int, 8)
	}
	for k, v := range cols {
		if v == nil {
			continue // 缺 ≠ 0：不参与均值，也不产出 0
		}
		if k == "temperature_c" {
			if a.maxTemp == nil || *v > *a.maxTemp {
				out := *v
				a.maxTemp = &out
			}
			continue
		}
		a.sums[k] += *v
		a.cnts[k]++
	}
}

func (a *resourceAcc) finish(bucketSec int64) ResourcePoint {
	// Values **恒非 nil**（空 map 由 json 的 omitempty 省略）：若只在有均值列时才建
	// map，纯温度桶（cnts 为空、只有 maxTemp）会在写 temperature_c 时 panic
	// —— 实测被 TestAggregateResourceTemperatureUsesMaxAndSkipsNil 抓到。
	p := ResourcePoint{T: bucketSec, Samples: a.samples, Values: make(map[string]*float64, len(a.cnts)+1)}
	for k, n := range a.cnts {
		if n == 0 {
			continue
		}
		v := util.Round2(a.sums[k] / float64(n))
		p.Values[k] = &v
	}
	if a.maxTemp != nil {
		// 温度与写路径一致：取 max 后**不**四舍五入（sensorAcc 亦然）。
		v := *a.maxTemp
		p.Values["temperature_c"] = &v
	}
	return p
}
