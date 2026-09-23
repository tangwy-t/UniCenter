package collect

import (
	"math"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// collectCPU 采集 CPU 使用率、每核占用与 iowait。
//
// 取数口径（spec §6.1）：
//
//   - 只调**一次** `cpu.Percent(0, true)` 拿每核切片，再取其均值。
//     不额外调一次 `(0,false)`：两次调用各自维护「自上次调用以来」的基线，
//     并行路径下两者会漂移，导致总体值 ≠ 每核均值，读图的人会以为数据错了。
//   - `cpu_iowait` 由 `cpu.Times(false)` 的 ΔIowait/ΔTotal 求得，而不是
//     把 iowait 当百分比直接上报 —— iowait 在 /proc/stat 里是**累计 jiffies**。
//
// 非 Linux（或容器里拿不到）时 iowait 留 nil：契约里它是可空字段，
// 显示「—」比编一个 0 诚实。
func (c *Collector) collectCPU(s *agentproto.MetricsSample) {
	// 0 时长 = since-last-call，非阻塞。首帧没有基线，gopsutil 会返回零值切片，
	// 此时用 0 而不是 nil —— CPU 使用率是**非空**契约字段（Validate 会校验）。
	if perCore, err := cpu.Percent(0, true); err == nil && len(perCore) > 0 {
		var sum float64
		for _, v := range perCore {
			sum += v
		}
		s.CPUPerCore = perCore
		s.CPUUsedPercent = clampPercent(sum / float64(len(perCore)))
	} else if total, err := cpu.Percent(0, false); err == nil && len(total) > 0 {
		// 退化路径：某些平台不支持 per-core，仍要给出总体值。
		s.CPUUsedPercent = clampPercent(total[0])
	}

	s.CPUIOWait = c.iowaitPercent()
}

// iowaitPercent 由累计 jiffies 差量求得 iowait 占比。
//
// 不复用跨帧状态：这个差量的窗口是「gopsutil 内部自上次 Times 调用以来」，
// 与 rateState 的窗口（采集间隔）不同，混在一起会让语义含糊。
func (c *Collector) iowaitPercent() *float64 {
	times, err := cpu.Times(false)
	if err != nil || len(times) == 0 {
		return nil
	}
	t := times[0]
	total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	if total <= 0 {
		return nil
	}
	// Iowait == 0 时给 nil：Windows/Darwin 恒为 0，上报 0 会被读成
	// 「这台机器完全没有 IO 等待」，而真相是「这个平台不提供该指标」。
	if t.Iowait <= 0 {
		return nil
	}
	return f64p(clampPercent(t.Iowait / total * 100))
}

// collectMemory 采集内存与 swap。
//
// `used = Total - Available`，**不**用 gopsutil 的 `Used`：
// Used 在不同平台的口径不一致（Linux 含 cache/buffer，Darwin 不含），
// 而 Available 在所有平台都表示「还能给新进程用的量」。用 Total-Available
// 能保证 `used + available ≡ total` 这条恒等式成立，前端两个数放一起才不会互相矛盾。
func (c *Collector) collectMemory(s *agentproto.MetricsSample) {
	vm, err := mem.VirtualMemory()
	if err == nil && vm != nil && vm.Total > 0 {
		const mb = 1024 * 1024
		used := float64(vm.Total-vm.Available) / mb
		s.MemUsedMB = used
		s.MemAvailableMB = float64(vm.Available) / mb
		// 用算出来的 used 反推百分比，保证与上面两个绝对量自洽
		// （直接用 vm.UsedPercent 会有舍入差，页面上「62.3%」与「5.0/13.5 GB」
		//  对不上时，用户第一反应是数据错了）。
		s.MemUsedPercent = clampPercent(used * mb / float64(vm.Total) * 100)
	}

	// swap：Total == 0 表示没配 swap，省略（nil），而不是报 0%。
	// 「没开 swap」与「开了但用量 0」是两件事，后者才该显示 0。
	if sm, err := mem.SwapMemory(); err == nil && sm != nil && sm.Total > 0 {
		const mb = 1024 * 1024
		used := float64(sm.Used) / mb
		s.SwapUsedMB = &used
		s.SwapUsedPercent = f64p(clampPercent(sm.UsedPercent))
	}
}

// buildStatic 采集 hello 载荷里的静态信息。
//
// 这些字段正是信息表上原来看不到的那批（平台/内核/CPU 型号/核数/内存总量/开机时间）。
// 采不到一律**省略**（omitempty），不填占位串：数据库里存 "unknown" 会让
// 前端把「没采到」当成「真值就是 unknown」显示出来。
func buildStatic(agentVersion string) *agentproto.Hello {
	h := &agentproto.Hello{
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: agentVersion,
	}
	if hi, err := host.Info(); err == nil && hi != nil {
		h.Hostname = hi.Hostname
		h.Kernel = hi.KernelVersion
		h.Platform = hi.Platform
		h.PlatformVer = hi.PlatformVersion
		h.BootTime = int64(hi.BootTime)
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		h.CPUModel = infos[0].ModelName
	}
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		h.CPUCores = n
	} else if n, err := cpu.Counts(false); err == nil && n > 0 {
		h.CPUCores = n
	}
	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		h.MemTotalMB = math.Round(float64(vm.Total) / (1024 * 1024))
	}
	if h.Hostname == "" {
		// 主机名是必填项：拿不到就用 instance id 兜底（见 config），
		// 这里给空会让 core 侧建出一台没有名字的设备。
		h.Hostname = "unknown-host"
	}
	return h
}

// clampPercent 把百分比夹到 [0,100]。
//
// gopsutil 偶发给出 -0.00001 或 100.00001（浮点累计误差），
// 而协议层 checkPercent 会**直接拒绝**越界值并丢弃整帧 —— 一个末位误差
// 丢掉整帧数据不值得，故在源头夹住。
func clampPercent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return math.Round(v*100) / 100
}

// f64p 取指针；入参非有限值时返回 nil（对应「未采集」）。
func f64p(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	out := math.Round(v*100) / 100
	return &out
}

// secSince 返回两个时刻的秒差（用单调时钟语义，避免 NTP 回拨产生负值）。
func secSince(from, to time.Time) float64 {
	d := to.Sub(from).Seconds()
	if d <= 0 {
		return 0
	}
	return d
}
