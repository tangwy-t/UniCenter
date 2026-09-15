package agentmetrics

import (
	"sort"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// MergeTrendPoints 把**原生栅格**上的邻接桶归并到 stepSec 栅格（「升档」）。
//
// 为什么必须有它（契约违反，实测）：spec §7.2 / §8 要求 `resolution_seconds` 是
// **实际生效**的栅格步长（30d → 15min、半年 → 2h），而升档此前只改了
// `TierSelection.Scale`、没有落到读取上 —— 于是 30d 请求仍返回 **8640 行**，
// `resolution_seconds` 却报 900：消费方按 t 定位时看到的步长与声明不符，
// 4000 桶上限也形同虚设。
//
// 为什么在 Go 侧合并而不是 SQL `GROUP BY (bucket_ts/step)*step`：逐列聚合语义
// **不同**（均值类要按 samples 加权、uptime 取 LAST、温度取 MAX、nil 不参与），
// 拆成多条聚合 SQL 会把「选表即选档」的单一读取路径打散，且 samples 加权在
// 跨行聚合里需要 `SUM(v*samples)/SUM(samples)` 两遍表达式，可读性与可测性都更差。
//
// 逐列语义与 Downsample / raw_agg **一致**：
//   - 均值类（gauge / rate / percent / 容量）→ 按 `Samples` **加权平均**，
//     nil 跳过（缺 ≠ 0，不当作 0）；
//   - 单调量 `uptime_sec` → **t 最大且非 nil** 的那个值（LAST；末尾的 nil 不该
//     抹掉前面已经采到的单调量）；
//   - `max_temperature_c` → **max**（nil 不参与）；
//   - `Samples` → 求和（1h 行的完整度信号必须跨桶累加）；
//   - **无输入点的桶不产出**（稀疏，与 spec §8「buckets 是稀疏的」一致），
//     绝不为了「填满栅格」合成空桶。
//
// 返回的桶按 `t` 升序，且每个桶的 `t` 都满足 `t % stepSec == 0`。
// stepSec <= 1（无需升档）或输入为空时原样返回。
func MergeTrendPoints(points []TrendPoint, stepSec int64) []TrendPoint {
	if stepSec <= 1 || len(points) == 0 {
		return points
	}

	// 先按 t 升序：输出的桶序与 LAST 语义都不能依赖调用方的行序
	// （DB 的 ORDER BY 只是当前实现细节，测试桩与热层不保证）。
	ordered := make([]TrendPoint, len(points))
	copy(ordered, points)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].T < ordered[j].T })

	out := make([]TrendPoint, 0, len(ordered))
	var cur *mergeAcc
	for i := range ordered {
		p := ordered[i]
		key := alignToStep(p.T, stepSec)
		if cur == nil || cur.t != key {
			if cur != nil {
				out = append(out, cur.finish())
			}
			cur = &mergeAcc{t: key}
		}
		cur.add(p)
	}
	out = append(out, cur.finish())
	return out
}

// alignToStep 把桶时间对齐到 stepSec 栅格（floor，与 SQL 的
// `(bucket_ts/step)*step` 语义一致）。
func alignToStep(t, stepSec int64) int64 {
	if stepSec <= 0 {
		return t
	}
	return t / stepSec * stepSec
}

// mergeAcc 是归并累加器：把同一个目标桶内的多个原生桶归并成一个 TrendPoint。
type mergeAcc struct {
	t       int64
	samples int

	// 加权累加：key = 值列名（与响应 available_metrics 的列名逐字一致）。
	sums map[string]float64
	wsum map[string]float64

	lastT   int64
	lastUp  *int64
	maxTemp *float64
}

// acc 累加一个均值列（懒建 map，避免 nil map 写入 panic）。
func (a *mergeAcc) acc(key string, v, w float64) {
	if a.sums == nil {
		a.sums = make(map[string]float64, len(trendPointColumns))
		a.wsum = make(map[string]float64, len(trendPointColumns))
	}
	a.sums[key] += v * w
	a.wsum[key] += w
}

// add 把一个原生桶并入当前目标桶。
func (a *mergeAcc) add(p TrendPoint) {
	a.samples += p.Samples

	// 权重：Samples 是完整度信号（1h 行 = 3600/reportInterval）。显式 metrics 不含
	// `samples` 时投影里没有它、值为 0 —— 此时退化为**等权**平均（权重 1），
	// 而不是把整桶当 0 权重算出 0/0。
	w := float64(p.Samples)
	if w <= 0 {
		w = 1
	}

	for _, c := range []struct {
		key string
		v   *float64
	}{
		{"cpu_used_percent", p.CPUUsedPercent},
		{"cpu_iowait", p.CPUIOWait},
		{"load1", p.Load1},
		{"load5", p.Load5},
		{"load15", p.Load15},
		{"mem_used_percent", p.MemUsedPercent},
		{"mem_used_mb", p.MemUsedMB},
		{"mem_available_mb", p.MemAvailableMB},
		{"swap_used_percent", p.SwapUsedPercent},
		{"swap_used_mb", p.SwapUsedMB},
		{"disk_total_gb", p.DiskTotalGB},
		{"disk_used_gb", p.DiskUsedGB},
		{"disk_used_percent", p.DiskUsedPercent},
		{"disk_io_read_bytes_sec", p.DiskIOReadBytesSec},
		{"disk_io_write_bytes_sec", p.DiskIOWriteBytesSec},
		{"nic_rx_bytes_sec", p.NICRXBytesSec},
		{"nic_tx_bytes_sec", p.NICTXBytesSec},
	} {
		if c.v == nil {
			continue // nil = 未采集，不参与加权（缺 ≠ 0）
		}
		a.acc(c.key, *c.v, w)
	}

	// 计数类（TCP / 进程数）在宽表里是「桶内均值」，故同样是加权均值；窄化为 int64。
	for _, c := range []struct {
		key string
		v   *int64
	}{
		{"tcp_total", p.TCPTotal},
		{"tcp_established", p.TCPEstablished},
		{"tcp_listen", p.TCPListen},
		{"proc_count", p.ProcCount},
	} {
		if c.v == nil {
			continue
		}
		a.acc(c.key, float64(*c.v), w)
	}

	// MAX：最高温（nil = 读不到，不参与）
	if p.MaxTemperatureC != nil && (a.maxTemp == nil || *p.MaxTemperatureC > *a.maxTemp) {
		v := *p.MaxTemperatureC
		a.maxTemp = &v
	}

	// LAST：单调量取 t 最大者（等号用 >= 让「同 t 的后一个」胜出，与输入顺序无关）
	if p.UptimeSec != nil && (a.lastUp == nil || p.T >= a.lastT) {
		a.lastT = p.T
		v := *p.UptimeSec
		a.lastUp = &v
	}
}

// mean 返回加权均值（四舍五入到 2 位小数，与 raw_agg / downsample 同精度约定）。
func (a *mergeAcc) mean(key string) *float64 {
	w := a.wsum[key]
	if w == 0 {
		return nil
	}
	v := util.Round2(a.sums[key] / w)
	return &v
}

// meanInt 把加权均值窄化回 *int64（TrendPoint 的计数字段是 *int64）。
func (a *mergeAcc) meanInt(key string) *int64 {
	p := a.mean(key)
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

// finish 产出目标桶的 TrendPoint。调用方保证 add 至少被调用过一次。
func (a *mergeAcc) finish() TrendPoint {
	return TrendPoint{
		T:       a.t,
		Samples: a.samples,

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

		TCPTotal:       a.meanInt("tcp_total"),
		TCPEstablished: a.meanInt("tcp_established"),
		TCPListen:      a.meanInt("tcp_listen"),
		ProcCount:      a.meanInt("proc_count"),
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
}
