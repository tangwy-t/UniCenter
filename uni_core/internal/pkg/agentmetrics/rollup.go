package agentmetrics

import (
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// RollupInfo 描述一次回滚的完整度，供协调器判断是否需要 repair。
//
// NeedRepair 是「1h 行会陈旧」这个隐患的检测量（spec §7.3 / 评审 R-W3）：
// 某个小时的 5m 行可能在小时关闭**之后**才落库（分区修复后重试、限速回填、
// 同桶 UPSERT 覆盖），若「关闭时算一次、不再重算」，该小时的 1h 行会永久停在
// 残缺值且**完全不可见**。
type RollupInfo struct {
	RowCount   int  // 参与回滚的 5min 行数（完整小时应为 12）
	SampleSum  int  // Σsamples（完整小时应为 ExpectedSamplesPerHour）
	NeedRepair bool // 完整度不足 → 协调器应在后续轮次重算该小时
}

// ExpectedSamplesPerHour 返回一个完整小时应有的原始样本数（reportInterval 的倒数 × 3600）。
// 非法间隔返回 0（调用方据此跳过完整度判断）。
func ExpectedSamplesPerHour(reportInterval time.Duration) int {
	if reportInterval <= 0 {
		return 0
	}
	return int(time.Hour / reportInterval)
}

// completeHourRows 是「完整小时应有多少个 5min 桶」。
const completeHourRows = 12

// RollupToHour 把同一小时内的 12 个 5min 宽行回滚为 1h 宽行。
//
// 规则（spec §7.1）：
//   - 均值类 → **按 samples 加权**：Σ(v×samples)/Σsamples（未加权会让「1 个样本的桶」
//     与「30 个样本的桶」等权，出现偏差）
//   - 单调量（uptime / agent 计数）→ 取 bucket_ts 最大的那一行的值（LAST）
//   - 5min-only 列（tcp_time_wait / tcp_close_wait / agent_*）→ **置 nil**
//   - 整机合计的 Σ 列（disk_total_gb / disk_used_gb / io / nic）→ 按 samples 加权平均
//   - 存量比值（disk_used_percent）→ **由 1h 的 Σ 列重算**，绝不对 5m 的比值求平均
//   - 温度 → max
//
// rows 为空时返回零值 Wide 与零值 RollupInfo（调用方跳过写路径）。
func RollupToHour(bucketTS int64, rows []Wide) (Wide, RollupInfo) {
	info := RollupInfo{RowCount: len(rows)}
	if len(rows) == 0 {
		return Wide{}, info
	}

	// 按 bucket_ts 升序（LAST 语义依赖它）
	ordered := make([]Wide, len(rows))
	copy(ordered, rows)
	sortWideByBucket(ordered)

	var (
		sumSamples int
		maxTemp    *float64               // 温度取 max（与 Wide 的变量名 out 区分开）
		weighted   = map[string]float64{} // Σ(v × samples)
		weightCnt  = map[string]int{}     // Σsamples（仅有值的行）
	)

	addW := func(key string, v *float64, samples int) {
		if v == nil || samples <= 0 {
			return
		}
		weighted[key] += *v * float64(samples)
		weightCnt[key] += samples
	}

	for i := range ordered {
		r := &ordered[i]
		s := r.Samples
		info.SampleSum += s
		sumSamples += s

		addW("cpu_used_percent", r.CPUUsedPercent, s)
		addW("cpu_iowait", r.CPUIOWait, s)
		addW("load1", r.Load1, s)
		addW("load5", r.Load5, s)
		addW("load15", r.Load15, s)
		addW("mem_used_percent", r.MemUsedPercent, s)
		addW("mem_used_mb", r.MemUsedMB, s)
		addW("mem_available_mb", r.MemAvailableMB, s)
		addW("swap_used_percent", r.SwapUsedPercent, s)
		addW("swap_used_mb", r.SwapUsedMB, s)
		addW("tcp_total", intToF(r.TCPTotal), s)
		addW("tcp_established", intToF(r.TCPEstablished), s)
		addW("tcp_listen", intToF(r.TCPListen), s)
		addW("udp_total", intToF(r.UDPTotal), s)
		addW("proc_count", intToF(r.ProcCount), s)
		addW("agent_collect_duration_ms", r.AgentCollectDurationMs, s)

		addW("disk_total_gb", r.DiskTotalGB, s)
		addW("disk_used_gb", r.DiskUsedGB, s)
		addW("disk_io_read_bytes_sec", r.DiskIOReadBytesSec, s)
		addW("disk_io_write_bytes_sec", r.DiskIOWriteBytesSec, s)
		addW("disk_io_read_ops_sec", r.DiskIOReadOpsSec, s)
		addW("disk_io_write_ops_sec", r.DiskIOWriteOpsSec, s)
		addW("nic_rx_bytes_sec", r.NICRXBytesSec, s)
		addW("nic_tx_bytes_sec", r.NICTXBytesSec, s)
		addW("nic_rx_packets_sec", r.NICRXPacketsSec, s)
		addW("nic_tx_packets_sec", r.NICTXPacketsSec, s)
		addW("nic_rx_errors_sec", r.NICRXErrorsSec, s)
		addW("nic_tx_errors_sec", r.NICTXErrorsSec, s)
		addW("nic_rx_dropped_sec", r.NICRXDroppedSec, s)

		if r.MaxTemperatureC != nil {
			if maxTemp == nil || *r.MaxTemperatureC > *maxTemp {
				v := *r.MaxTemperatureC
				maxTemp = &v
			}
		}
	}

	wmean := func(key string) *float64 {
		n := weightCnt[key]
		if n == 0 {
			return nil
		}
		v := util.Round2(weighted[key] / float64(n))
		return &v
	}

	last := &ordered[len(ordered)-1]
	out := Wide{
		DeviceID: last.DeviceID,
		BucketTS: bucketTS,
		Samples:  sumSamples,

		CPUUsedPercent:  wmean("cpu_used_percent"),
		CPUIOWait:       wmean("cpu_iowait"),
		Load1:           wmean("load1"),
		Load5:           wmean("load5"),
		Load15:          wmean("load15"),
		MemUsedPercent:  wmean("mem_used_percent"),
		MemUsedMB:       wmean("mem_used_mb"),
		MemAvailableMB:  wmean("mem_available_mb"),
		SwapUsedPercent: wmean("swap_used_percent"),
		SwapUsedMB:      wmean("swap_used_mb"),

		TCPTotal:       intPtr(wmean("tcp_total")),
		TCPEstablished: intPtr(wmean("tcp_established")),
		TCPListen:      intPtr(wmean("tcp_listen")),
		UDPTotal:       intPtr(wmean("udp_total")),
		ProcCount:      intPtr(wmean("proc_count")),
		// 单调量取最新
		UptimeSec: last.UptimeSec,

		// 5min-only 列在 1h 行刻意留 nil（tcp_time_wait / tcp_close_wait / agent_*）

		DiskTotalGB:         wmean("disk_total_gb"),
		DiskUsedGB:          wmean("disk_used_gb"),
		DiskIOReadBytesSec:  wmean("disk_io_read_bytes_sec"),
		DiskIOWriteBytesSec: wmean("disk_io_write_bytes_sec"),
		DiskIOReadOpsSec:    wmean("disk_io_read_ops_sec"),
		DiskIOWriteOpsSec:   wmean("disk_io_write_ops_sec"),
		NICRXBytesSec:       wmean("nic_rx_bytes_sec"),
		NICTXBytesSec:       wmean("nic_tx_bytes_sec"),
		NICRXPacketsSec:     wmean("nic_rx_packets_sec"),
		NICTXPacketsSec:     wmean("nic_tx_packets_sec"),
		NICRXErrorsSec:      wmean("nic_rx_errors_sec"),
		NICTXErrorsSec:      wmean("nic_tx_errors_sec"),
		NICRXDroppedSec:     wmean("nic_rx_dropped_sec"),
		MaxTemperatureC:     maxTemp,
	}

	// 存量比值：由 1h 的 Σ 列重算（不能平均 5m 的比值）
	if out.DiskTotalGB != nil && out.DiskUsedGB != nil && *out.DiskTotalGB > 0 {
		out.DiskUsedPercent = f64p(*out.DiskUsedGB / *out.DiskTotalGB * 100)
	}

	info.NeedRepair = info.RowCount < completeHourRows
	return out, info
}

// ─ 小工具（rollup 专用）──────────────────────────────

func intToF(p *int64) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}

func sortWideByBucket(rows []Wide) {
	// 插入排序足够（最多 12 个元素），且避免为 []Wide 再引 sort.Slice 的反射开销
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].BucketTS < rows[j-1].BucketTS; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
