package collect

import (
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// netIOCounters 是 gopsutil net.IOCounters 的薄封装（便于测试替身与统一错误处理）。
func netIOCounters() ([]net.IOCountersStat, error) {
	return net.IOCounters(true)
}

// netCounter 是本文件内部的计数器视图，避免把 gopsutil 类型散到差量逻辑里。
type netCounter struct {
	rxBytes, txBytes     uint64
	rxPackets, txPackets uint64
	rxErrors, txErrors   uint64
	rxDropped            uint64
}

// collectTCP 采集 TCP/UDP 连接数。
//
// 优先直读 `/proc/net/{tcp,tcp6,udp,udp6}`：
// `net.Connections()` 会遍历**整个 /proc 下所有进程的 fd**，在进程多、
// 连接多的机器上单次调用可到数百毫秒，直接顶爆 spec 要求的采集预算
// （clamp(interval×0.5,1s,3s)）。直读只是读 4 个文件。
//
// 非 Linux 上回退到 net.Connections（Windows/Darwin 没有 /proc）。
func (c *Collector) collectTCP(s *agentproto.MetricsSample) {
	if counts, ok := procNetTCP(); ok {
		s.TCPTotal = counts.total
		s.TCPEstablished = counts.established
		s.TCPListen = counts.listen
		s.TCPTimeWait = counts.timeWait
		s.TCPCloseWait = counts.closeWait
		s.UDPTotal = counts.udp
	} else if conns, err := net.Connections("all"); err == nil {
		// 回退路径：一次遍历同时数出各类状态。
		//
		// conn.Type 是 **uint32**（syscall.SOCK_STREAM / SOCK_DGRAM），
		// 不是字符串 —— 早期写法按字符串比较会恒不匹配，把 UDP 也算进 TCP。
		var total, est, lis, tw, cw, udp int
		for _, conn := range conns {
			if conn.Type == syscall.SOCK_DGRAM {
				udp++
				continue
			}
			total++
			switch conn.Status {
			case "ESTABLISHED":
				est++
			case "LISTEN":
				lis++
			case "TIME_WAIT":
				tw++
			case "CLOSE_WAIT":
				cw++
			}
		}
		s.TCPTotal = total
		s.TCPEstablished = est
		s.TCPListen = lis
		s.TCPTimeWait = tw
		s.TCPCloseWait = cw
		s.UDPTotal = udp
	}

	// 进程数：gopsutil 的 Pids() 只读 /proc 目录项，代价可控。
	if pids, err := process.Pids(); err == nil {
		s.ProcCount = len(pids)
	}
}

// tcpCounts 是 /proc/net 解析结果。
type tcpCounts struct {
	total, established, listen, timeWait, closeWait, udp int
}

// procNetTCP 解析 /proc/net 下的 tcp/tcp6/udp/udp6。
//
// 返回 ok=false 表示「当前平台/环境读不到」（非 Linux、或权限受限），
// 此时调用方回退到 gopsutil。**不返回部分结果**：只读到 tcp 而读不到 tcp6
// 会让连接总数偏低，比明确失败更容易误导。
func procNetTCP() (tcpCounts, bool) {
	files := map[string]string{
		"tcp":  "/proc/net/tcp",
		"tcp6": "/proc/net/tcp6",
		"udp":  "/proc/net/udp",
		"udp6": "/proc/net/udp6",
	}
	var out tcpCounts
	read := 0
	for kind, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		read++
		isUDP := strings.HasPrefix(kind, "udp")
		for _, line := range strings.Split(string(b), "\n")[1:] { // 跳过表头
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			// /proc/net/tcp 的格式：sl local_address rem_address st ...
			// st 是 2 位十六进制状态码。
			state := fields[3]
			if isUDP {
				out.udp++
				continue
			}
			out.total++
			switch state {
			case "01": // ESTABLISHED
				out.established++
			case "0A": // LISTEN
				out.listen++
			case "06": // TIME_WAIT
				out.timeWait++
			case "08": // CLOSE_WAIT
				out.closeWait++
			}
		}
	}
	if read == 0 {
		return tcpCounts{}, false
	}
	return out, true
}

// collectSensors 采集温度传感器读数。
//
// 容器/虚拟机里通常读不到（没有 sensors 模块或 hwmon），此时保持空切片 →
// core 侧 max_temperature_c 为 nil → 前端显示「—」。这是**正确**行为：
// 编一个 0°C 上报会让人以为机器温度异常低。
func (c *Collector) collectSensors(s *agentproto.MetricsSample) {
	temps, err := host.SensorsTemperatures()
	if err != nil || len(temps) == 0 {
		return
	}
	out := make([]agentproto.SensorMetric, 0, len(temps))
	seen := make(map[string]bool, len(temps))
	for _, t := range temps {
		name := t.SensorKey
		if name == "" {
			name = t.SensorKey
		}
		if name == "" || seen[name] {
			continue // 同名传感器去重：core 侧按 name 聚合，重名会重复计权
		}
		seen[name] = true
		// 0 且无读数标记的条目视为「读不到」：部分驱动对不可用通道返回 0。
		if t.Temperature == 0 {
			continue
		}
		v := t.Temperature
		out = append(out, agentproto.SensorMetric{Name: name, TemperatureC: &v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	s.Sensors = out
}

// collectUptime 采集开机时长。
func (c *Collector) collectUptime(s *agentproto.MetricsSample) {
	if up, err := host.Uptime(); err == nil {
		s.UptimeSec = int64(up)
	}
}

// ── 排序辅助（稳定输出）───────────────────────────────

func sortStrings(v []string) { sort.Strings(v) }

func sortDisks(v []agentproto.DiskMetric) {
	sort.Slice(v, func(i, j int) bool { return v[i].Mountpoint < v[j].Mountpoint })
}

func sortDiskIO(v []agentproto.DiskIOMetric) {
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
}

// sanitize 在序列化前做最后一次清理（spec §6.1）。
//
// 为什么还要做一遍：上面各采集函数已经夹过，但字段多、来源杂，
// 漏一处就会让 checkPercent/checkNonNeg **拒绝整帧**（Validate 失败 = 丢整帧），
// 代价远大于这里多一次遍历。
func sanitize(s *agentproto.MetricsSample) {
	s.CPUUsedPercent = clampPercent(s.CPUUsedPercent)
	for i, v := range s.CPUPerCore {
		s.CPUPerCore[i] = clampPercent(v)
	}
	if s.CPUIOWait != nil {
		v := clampPercent(*s.CPUIOWait)
		s.CPUIOWait = &v
	}

	s.MemUsedPercent = clampPercent(s.MemUsedPercent)
	if s.MemUsedMB < 0 {
		s.MemUsedMB = 0
	}
	if s.MemAvailableMB < 0 {
		s.MemAvailableMB = 0
	}
	if s.SwapUsedPercent != nil {
		v := clampPercent(*s.SwapUsedPercent)
		s.SwapUsedPercent = &v
	}
	if s.SwapUsedMB != nil && *s.SwapUsedMB < 0 {
		z := 0.0
		s.SwapUsedMB = &z
	}

	for i := range s.Disks {
		d := &s.Disks[i]
		d.UsedPercent = clampPercent(d.UsedPercent)
		d.InodesUsedPercent = clampPercent(d.InodesUsedPercent)
		if d.UsedGB < 0 {
			d.UsedGB = 0
		}
		if d.TotalGB < 0 {
			d.TotalGB = 0
		}
		// 协议硬约束：used <= total（Validate 会拒整个挂载点）
		if d.UsedGB > d.TotalGB {
			d.UsedGB = d.TotalGB
		}
	}
	for i := range s.DiskIO {
		d := &s.DiskIO[i]
		d.ReadBytesPerSec = nonNeg(d.ReadBytesPerSec)
		d.WriteBytesPerSec = nonNeg(d.WriteBytesPerSec)
		d.ReadOpsPerSec = nonNeg(d.ReadOpsPerSec)
		d.WriteOpsPerSec = nonNeg(d.WriteOpsPerSec)
		d.IOTimePercent = clampPercent(d.IOTimePercent)
	}
	for i := range s.NICs {
		n := &s.NICs[i]
		n.RXBytesPerSec = nonNeg(n.RXBytesPerSec)
		n.TXBytesPerSec = nonNeg(n.TXBytesPerSec)
		n.RXPacketsPerSec = nonNeg(n.RXPacketsPerSec)
		n.TXPacketsPerSec = nonNeg(n.TXPacketsPerSec)
		n.RXErrorsPerSec = nonNeg(n.RXErrorsPerSec)
		n.TXErrorsPerSec = nonNeg(n.TXErrorsPerSec)
		n.RXDroppedPerSec = nonNeg(n.RXDroppedPerSec)
	}

	s.TCPTotal = nonNegInt(s.TCPTotal)
	s.TCPEstablished = nonNegInt(s.TCPEstablished)
	s.TCPListen = nonNegInt(s.TCPListen)
	s.TCPTimeWait = nonNegInt(s.TCPTimeWait)
	s.TCPCloseWait = nonNegInt(s.TCPCloseWait)
	s.UDPTotal = nonNegInt(s.UDPTotal)
	s.ProcCount = nonNegInt(s.ProcCount)
	if s.UptimeSec < 0 {
		s.UptimeSec = 0
	}
}

func nonNeg(v float64) float64 {
	if v < 0 || v != v { // v != v 检测 NaN
		return 0
	}
	return v
}

func nonNegInt(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
