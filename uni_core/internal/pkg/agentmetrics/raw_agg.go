package agentmetrics

import (
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// rawBucketAcc 是热层聚合的**桶累加器**：把落在同一个 step 桶内的多个原始样本
// 归并成一个 TrendPoint。
//
// 聚合口径与 downsample.go 的宽表一致（前端在热层/冷层应看到同形数据）：
//   - gauge / rate / percent / 容量 → **均值**（nil 跳过，不当作 0）
//   - 温度 → **max**（nil = 读不到，不参与）
//   - 单调量（uptime）→ 桶内**最大 T** 的样本值（LAST）
//   - samples → 桶内样本数
//   - 整机磁盘/网卡合计 → 由**同一样本内的明细 Σ** 先算整机值，再对整机值求均值
//     （不是对明细求两次均值，避免明细数量在桶内变化时产生偏差）
type rawBucketAcc struct {
	samples int

	sums map[string]float64
	cnts map[string]int

	lastT  int64
	lastUp *int64

	maxTemp *float64
}

// acc 累加一个样本的某个列（懒建 map，避免 nil map 写入 panic）。
func (a *rawBucketAcc) acc(key string, v float64) {
	if a.sums == nil {
		a.sums = make(map[string]float64, 16)
		a.cnts = make(map[string]int, 16)
	}
	a.sums[key] += v
	a.cnts[key]++
}

func (a *rawBucketAcc) add(p agentproto.MetricsSample) {
	a.samples++

	avg := func(key string, v *float64) {
		if v == nil {
			return // nil = 未采集，不参与均值（缺 ≠ 0）
		}
		a.acc(key, *v)
	}
	avg("cpu_used_percent", &p.CPUUsedPercent)
	avg("cpu_iowait", p.CPUIOWait)
	avg("load1", &p.Load1)
	avg("load5", &p.Load5)
	avg("load15", &p.Load15)
	avg("mem_used_percent", &p.MemUsedPercent)
	avg("mem_used_mb", &p.MemUsedMB)
	avg("mem_available_mb", &p.MemAvailableMB)
	avg("swap_used_percent", p.SwapUsedPercent)
	avg("swap_used_mb", p.SwapUsedMB)

	for _, c := range []struct {
		key string
		v   int
	}{
		{"tcp_total", p.TCPTotal},
		{"tcp_established", p.TCPEstablished},
		{"tcp_listen", p.TCPListen},
		{"proc_count", p.ProcCount},
	} {
		a.acc(c.key, float64(c.v))
	}

	// 单调量：桶内 T 最大的样本
	if p.T >= a.lastT {
		a.lastT = p.T
		v := p.UptimeSec
		a.lastUp = &v
	}

	// 整机磁盘合计（本样本内先 Σ，再对整机值求均值）
	if len(p.Disks) > 0 {
		var total, used float64
		for _, d := range p.Disks {
			total += d.TotalGB
			used += d.UsedGB
		}
		avg("disk_total_gb", &total)
		avg("disk_used_gb", &used)
		if total > 0 {
			pct := used / total * 100
			avg("disk_used_percent", &pct)
		}
	}

	// 整机磁盘 IO / 网卡收发合计
	if len(p.DiskIO) > 0 {
		var r, w float64
		for _, d := range p.DiskIO {
			r += d.ReadBytesPerSec
			w += d.WriteBytesPerSec
		}
		avg("disk_io_read_bytes_sec", &r)
		avg("disk_io_write_bytes_sec", &w)
	}
	if len(p.NICs) > 0 {
		var rx, tx float64
		for _, n := range p.NICs {
			rx += n.RXBytesPerSec
			tx += n.TXBytesPerSec
		}
		avg("nic_rx_bytes_sec", &rx)
		avg("nic_tx_bytes_sec", &tx)
	}

	// 最高温：只在有读数的传感器里取 max
	for _, sensor := range p.Sensors {
		if sensor.TemperatureC == nil {
			continue
		}
		if a.maxTemp == nil || *sensor.TemperatureC > *a.maxTemp {
			v := *sensor.TemperatureC
			a.maxTemp = &v
		}
	}
}

func (a *rawBucketAcc) mean(key string) *float64 {
	n := a.cnts[key]
	if n == 0 {
		return nil
	}
	v := util.Round2(a.sums[key] / float64(n))
	return &v
}

// finish 产出该桶的 TrendPoint。调用方必须保证 samples > 0。
func (a *rawBucketAcc) finish(bucketSec int64) TrendPoint {
	out := TrendPoint{
		T:               bucketSec,
		Samples:         a.samples,
		CPUUsedPercent:  a.mean("cpu_used_percent"),
		CPUIOWait:       a.mean("cpu_iowait"),
		Load1:           a.mean("load1"),
		Load5:           a.mean("load5"),
		Load15:          a.mean("load15"),
		MemUsedPercent:  a.mean("mem_used_percent"),
		MemUsedMB:       a.mean("mem_used_mb"),
		MemAvailableMB:  a.mean("mem_available_mb"),
		SwapUsedPercent: a.mean("swap_used_percent"),
		SwapUsedMB:      a.mean("swap_used_mb"),

		TCPTotal:       int64Ptr(a.mean("tcp_total")),
		TCPEstablished: int64Ptr(a.mean("tcp_established")),
		TCPListen:      int64Ptr(a.mean("tcp_listen")),
		ProcCount:      int64Ptr(a.mean("proc_count")),
		UptimeSec:      a.lastUp,

		DiskTotalGB:         a.mean("disk_total_gb"),
		DiskUsedGB:          a.mean("disk_used_gb"),
		DiskUsedPercent:     a.mean("disk_used_percent"),
		DiskIOReadBytesSec:  a.mean("disk_io_read_bytes_sec"),
		DiskIOWriteBytesSec: a.mean("disk_io_write_bytes_sec"),
		NICRXBytesSec:       a.mean("nic_rx_bytes_sec"),
		NICTXBytesSec:       a.mean("nic_tx_bytes_sec"),
		MaxTemperatureC:     a.maxTemp,
	}
	return out
}

// int64Ptr 把均值四舍五入回整数（TrendPoint 里的计数字段是 *int64）。
func int64Ptr(p *float64) *int64 {
	if p == nil {
		return nil
	}
	v := int64(util.Round2(*p))
	return &v
}
