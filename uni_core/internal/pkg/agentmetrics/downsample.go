package agentmetrics

import (
	"sort"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// Downsample 把一个 5min 桶内的原始样本（10s 粒度）聚合为宽表一行 + 4 张子表的明细行。
//
// 纯函数、无副作用、无 DB/Redis 依赖，便于表驱动单测。
//
// 聚合语义（spec §7.1，逐条落实）：
//   - gauge / rate / percent / 容量 → **均值**（nil 跳过，不当作 0）
//   - 单调量（uptime、agent 的 report_* 计数）→ **桶内 T 最大的样本值**（LAST）
//   - 温度 → **max**（nil = 读不到，不参与）
//   - last_report_error → **末非空串**
//   - samples → 桶内样本数
//   - 整机合计（磁盘总量/用量、磁盘 IO、网卡收发）→ 由**同桶明细 Σ** 推导
//   - 存量比值（disk_used_percent）→ **由 Σ 重算**（Σused/Σtotal×100），
//     绝不对明细的 used_percent 求平均（比值的平均 ≠ 比的之和，见 spec §7.1）
//
// 返回 ok=false 表示该桶无样本，调用方应**跳过整个写路径**（不写空行，spec §7.1）。
func Downsample(deviceID uint64, bucketTS int64, pts []agentproto.MetricsSample) (Wide, Subs, bool) {
	if len(pts) == 0 {
		return Wide{}, Subs{}, false
	}

	// 保证按 T 升序处理，使 LAST 语义正确（调用方给的多半已有序，这里不依赖它）
	ordered := make([]agentproto.MetricsSample, len(pts))
	copy(ordered, pts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].T < ordered[j].T })

	acc := newWideAcc()
	for i := range ordered {
		acc.add(&ordered[i])
	}
	return acc.finish(deviceID, bucketTS)
}

// wideAcc 是聚合累加器（纯内存）。
type wideAcc struct {
	samples int

	// 均值累加（nil 跳过）：key 用字段名，便于通用取值
	sums map[string]float64
	cnts map[string]int

	// LAST：桶内 T 最大样本的单调量与字符串
	lastT    int64
	lastInts map[string]int64
	lastErr  string
	maxTemp  *float64

	// 明细累加（按 name）
	disks   map[string]*diskAcc
	diskIO  map[string]*diskioAcc
	nics    map[string]*nicAcc
	sensors map[string]*sensorAcc
}

func newWideAcc() *wideAcc {
	return &wideAcc{
		sums:     map[string]float64{},
		cnts:     map[string]int{},
		lastInts: map[string]int64{},
		disks:    map[string]*diskAcc{},
		diskIO:   map[string]*diskioAcc{},
		nics:     map[string]*nicAcc{},
		sensors:  map[string]*sensorAcc{},
	}
}

func (a *wideAcc) avg(key string, v *float64) {
	if v == nil {
		return
	}
	a.sums[key] += *v
	a.cnts[key]++
}

func (a *wideAcc) add(p *agentproto.MetricsSample) {
	a.samples++

	// ── 均值类（nil 跳过）──────────────────────────────
	a.avg("cpu_used_percent", &p.CPUUsedPercent)
	a.avg("cpu_iowait", p.CPUIOWait)
	a.avg("load1", &p.Load1)
	a.avg("load5", &p.Load5)
	a.avg("load15", &p.Load15)
	a.avg("mem_used_percent", &p.MemUsedPercent)
	a.avg("mem_used_mb", &p.MemUsedMB)
	a.avg("mem_available_mb", &p.MemAvailableMB)
	a.avg("swap_used_percent", p.SwapUsedPercent)
	a.avg("swap_used_mb", p.SwapUsedMB)
	a.avg("agent_collect_duration_ms", &p.Agent.CollectDurationMs)
	a.avg("agent_mem_resident_mb", p.Agent.MemResidentMB)
	a.avg("agent_last_report_latency_ms", p.Agent.LastReportLatencyMs)

	for _, c := range []struct {
		key string
		v   int
	}{
		{"tcp_total", p.TCPTotal},
		{"tcp_established", p.TCPEstablished},
		{"tcp_listen", p.TCPListen},
		{"tcp_time_wait", p.TCPTimeWait},
		{"tcp_close_wait", p.TCPCloseWait},
		{"udp_total", p.UDPTotal},
		{"proc_count", p.ProcCount},
	} {
		a.sums[c.key] += float64(c.v)
		a.cnts[c.key]++
	}

	// ── LAST 类（按 T 最大）────────────────────────────
	if p.T >= a.lastT {
		a.lastT = p.T
		a.lastInts["uptime_sec"] = p.UptimeSec
		a.lastInts["agent_report_success_count"] = p.Agent.ReportSuccessCount
		a.lastInts["agent_report_error_count"] = p.Agent.ReportErrorCount
		a.lastInts["agent_ws_reconnect_count"] = p.Agent.WSReconnectCount
		if p.Agent.PendingBacklog != nil {
			a.lastInts["agent_pending_backlog"] = *p.Agent.PendingBacklog
		}
		if p.Agent.ReportDropCount != nil {
			a.lastInts["agent_report_drop_count"] = *p.Agent.ReportDropCount
		}
		if p.Agent.AgentUptimeSec != nil {
			a.lastInts["agent_agent_uptime_sec"] = *p.Agent.AgentUptimeSec
		}
		if p.Agent.LastReportError != "" {
			a.lastErr = p.Agent.LastReportError
		}
	}

	// ─ 明细累加 ───────────────────────────────────────
	for _, d := range p.Disks {
		e := a.disks[d.Mountpoint]
		if e == nil {
			e = &diskAcc{}
			a.disks[d.Mountpoint] = e
		}
		e.add(d)
	}
	for _, d := range p.DiskIO {
		e := a.diskIO[d.Name]
		if e == nil {
			e = &diskioAcc{}
			a.diskIO[d.Name] = e
		}
		e.add(d)
	}
	for _, n := range p.NICs {
		e := a.nics[n.Name]
		if e == nil {
			e = &nicAcc{}
			a.nics[n.Name] = e
		}
		e.add(n)
	}
	for _, s := range p.Sensors {
		e := a.sensors[s.Name]
		if e == nil {
			e = &sensorAcc{}
			a.sensors[s.Name] = e
		}
		e.add(s)
	}
}

func (a *wideAcc) mean(key string) *float64 {
	n := a.cnts[key]
	if n == 0 {
		return nil
	}
	v := util.Round2(a.sums[key] / float64(n))
	return &v
}

func (a *wideAcc) lastInt(key string) *int64 {
	v, ok := a.lastInts[key]
	if !ok {
		return nil
	}
	out := v
	return &out
}

func (a *wideAcc) finish(deviceID uint64, bucketTS int64) (Wide, Subs, bool) {
	if a.samples == 0 {
		return Wide{}, Subs{}, false
	}

	w := Wide{
		DeviceID: deviceID,
		BucketTS: bucketTS,
		Samples:  a.samples,

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

		TCPTotal:       intPtr(a.mean("tcp_total")),
		TCPEstablished: intPtr(a.mean("tcp_established")),
		TCPListen:      intPtr(a.mean("tcp_listen")),
		TCPTimeWait:    intPtr(a.mean("tcp_time_wait")),
		TCPCloseWait:   intPtr(a.mean("tcp_close_wait")),
		UDPTotal:       intPtr(a.mean("udp_total")),
		ProcCount:      intPtr(a.mean("proc_count")),
		UptimeSec:      a.lastInt("uptime_sec"),

		AgentCollectDurationMs:   a.mean("agent_collect_duration_ms"),
		AgentReportSuccessCount:  a.lastInt("agent_report_success_count"),
		AgentReportErrorCount:    a.lastInt("agent_report_error_count"),
		AgentWSReconnectCount:    a.lastInt("agent_ws_reconnect_count"),
		AgentMemResidentMB:       a.mean("agent_mem_resident_mb"),
		AgentPendingBacklog:      a.lastInt("agent_pending_backlog"),
		AgentLastReportLatencyMs: a.mean("agent_last_report_latency_ms"),
		AgentReportDropCount:     a.lastInt("agent_report_drop_count"),
		AgentUptimeSec:           a.lastInt("agent_agent_uptime_sec"),
	}
	if a.lastErr != "" {
		v := a.lastErr
		w.AgentLastReportError = &v
	}

	// ── 详解 + 整机合计 ────────────────────────────────
	var subs Subs
	var sumDiskTotal, sumDiskUsed float64
	var sumIORead, sumIOWrite, sumIOReadOps, sumIOWriteOps float64
	var sumNICRX, sumNICTX, sumNICRXp, sumNICTXp, sumNICRXe, sumNICTXe, sumNICRXd float64

	// 整机合计 = **同桶明细均值之和**（Σ over 资源的 per-resource 均值）
	for _, name := range sortedKeys(a.disks) {
		row := a.disks[name].finish(name)
		subs.Disks = append(subs.Disks, row)
		sumDiskTotal += deref(row.TotalGB)
		sumDiskUsed += deref(row.UsedGB)
	}
	for _, name := range sortedKeys(a.diskIO) {
		row := a.diskIO[name].finish(name)
		subs.DiskIO = append(subs.DiskIO, row)
		sumIORead += deref(row.ReadBytesPerSec)
		sumIOWrite += deref(row.WriteBytesPerSec)
		sumIOReadOps += deref(row.ReadOpsPerSec)
		sumIOWriteOps += deref(row.WriteOpsPerSec)
	}
	for _, name := range sortedKeys(a.nics) {
		row := a.nics[name].finish(name)
		subs.NICs = append(subs.NICs, row)
		sumNICRX += deref(row.RXBytesPerSec)
		sumNICTX += deref(row.TXBytesPerSec)
		sumNICRXp += deref(row.RXPacketsPerSec)
		sumNICTXp += deref(row.TXPacketsPerSec)
		sumNICRXe += deref(row.RXErrorsPerSec)
		sumNICTXe += deref(row.TXErrorsPerSec)
		sumNICRXd += deref(row.RXDroppedPerSec)
	}
	var maxTemp *float64
	for _, name := range sortedKeys(a.sensors) {
		e := a.sensors[name]
		subs.Sensors = append(subs.Sensors, SubRowSensor{Name: name, TemperatureC: e.max})
		if e.max != nil && (maxTemp == nil || *e.max > *maxTemp) {
			v := *e.max
			maxTemp = &v
		}
	}

	if len(subs.Disks) > 0 {
		w.DiskTotalGB = f64p(sumDiskTotal)
		w.DiskUsedGB = f64p(sumDiskUsed)
		// 存量比值：由 Σ 重算（不是对明细比值求平均）
		if sumDiskTotal > 0 {
			w.DiskUsedPercent = f64p(sumDiskUsed / sumDiskTotal * 100)
		}
	}
	if len(subs.DiskIO) > 0 {
		w.DiskIOReadBytesSec = f64p(sumIORead)
		w.DiskIOWriteBytesSec = f64p(sumIOWrite)
		w.DiskIOReadOpsSec = f64p(sumIOReadOps)
		w.DiskIOWriteOpsSec = f64p(sumIOWriteOps)
	}
	if len(subs.NICs) > 0 {
		w.NICRXBytesSec = f64p(sumNICRX)
		w.NICTXBytesSec = f64p(sumNICTX)
		w.NICRXPacketsSec = f64p(sumNICRXp)
		w.NICTXPacketsSec = f64p(sumNICTXp)
		w.NICRXErrorsSec = f64p(sumNICRXe)
		w.NICTXErrorsSec = f64p(sumNICTXe)
		w.NICRXDroppedSec = f64p(sumNICRXd)
	}
	w.MaxTemperatureC = maxTemp

	return w, subs, true
}

// ─ 明细累加器 ──────────────────────────────────────────

// diskAcc / diskioAcc / nicAcc 一律 **累加求和 + 计数**，finish 时取均值，
// 与宽表的聚合口径一致（早期版本写成「每次覆盖」，会让多样本桶静默取末值）。
type diskAcc struct {
	samples int
	fsType  string

	usedPercentSum, usedSum, totalSum, inodesSum float64
	usedPercentCnt, inodesCnt                    int
}

func (e *diskAcc) add(d agentproto.DiskMetric) {
	e.samples++
	if e.fsType == "" {
		e.fsType = d.FSType
	}
	e.usedPercentSum += d.UsedPercent
	e.usedPercentCnt++
	e.usedSum += d.UsedGB
	e.totalSum += d.TotalGB
	if d.InodesUsedPercent != 0 {
		e.inodesSum += d.InodesUsedPercent
		e.inodesCnt++
	}
}

func (e *diskAcc) finish(name string) SubRowDisk {
	out := SubRowDisk{Name: name, FSType: e.fsType}
	if e.samples == 0 {
		return out
	}
	out.UsedGB = f64p(e.usedSum / float64(e.samples))
	out.TotalGB = f64p(e.totalSum / float64(e.samples))
	if e.usedPercentCnt > 0 {
		out.UsedPercent = f64p(e.usedPercentSum / float64(e.usedPercentCnt))
	}
	if e.inodesCnt > 0 {
		out.InodesUsedPercent = f64p(e.inodesSum / float64(e.inodesCnt))
	}
	return out
}

type diskioAcc struct {
	samples                                               int
	readSum, writeSum, readOpsSum, writeOpsSum, ioTimeSum float64
	ioTimeCnt                                             int
}

func (e *diskioAcc) add(d agentproto.DiskIOMetric) {
	e.samples++
	e.readSum += d.ReadBytesPerSec
	e.writeSum += d.WriteBytesPerSec
	e.readOpsSum += d.ReadOpsPerSec
	e.writeOpsSum += d.WriteOpsPerSec
	if d.IOTimePercent != 0 {
		e.ioTimeSum += d.IOTimePercent
		e.ioTimeCnt++
	}
}

func (e *diskioAcc) finish(name string) SubRowDiskIO {
	out := SubRowDiskIO{Name: name}
	if e.samples == 0 {
		return out
	}
	n := float64(e.samples)
	out.ReadBytesPerSec = f64p(e.readSum / n)
	out.WriteBytesPerSec = f64p(e.writeSum / n)
	out.ReadOpsPerSec = f64p(e.readOpsSum / n)
	out.WriteOpsPerSec = f64p(e.writeOpsSum / n)
	if e.ioTimeCnt > 0 {
		out.IOTimePercent = f64p(e.ioTimeSum / float64(e.ioTimeCnt))
	}
	return out
}

type nicAcc struct {
	samples                         int
	rx, tx, rxp, txp, rxe, txe, rxd float64
}

func (e *nicAcc) add(n agentproto.NICMetric) {
	e.samples++
	e.rx += n.RXBytesPerSec
	e.tx += n.TXBytesPerSec
	e.rxp += n.RXPacketsPerSec
	e.txp += n.TXPacketsPerSec
	e.rxe += n.RXErrorsPerSec
	e.txe += n.TXErrorsPerSec
	e.rxd += n.RXDroppedPerSec
}

func (e *nicAcc) finish(name string) SubRowNIC {
	out := SubRowNIC{Name: name}
	if e.samples == 0 {
		return out
	}
	n := float64(e.samples)
	out.RXBytesPerSec = f64p(e.rx / n)
	out.TXBytesPerSec = f64p(e.tx / n)
	out.RXPacketsPerSec = f64p(e.rxp / n)
	out.TXPacketsPerSec = f64p(e.txp / n)
	out.RXErrorsPerSec = f64p(e.rxe / n)
	out.TXErrorsPerSec = f64p(e.txe / n)
	out.RXDroppedPerSec = f64p(e.rxd / n)
	return out
}

type sensorAcc struct{ max *float64 }

func (e *sensorAcc) add(s agentproto.SensorMetric) {
	if s.TemperatureC == nil {
		return // 读不到：保持 nil（不是 0）
	}
	if e.max == nil || *s.TemperatureC > *e.max {
		v := *s.TemperatureC
		e.max = &v
	}
}

// ─ 小工具 ───────────────────────────────────────────

func f64p(v float64) *float64 {
	out := util.Round2(v)
	return &out
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func intPtr(p *float64) *int64 {
	if p == nil {
		return nil
	}
	v := int64(util.Round2(*p))
	return &v
}

// sortedKeys 返回 map 的键升序（保证 Downsample 输出顺序稳定，便于测试与幂等写入）。
func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
