// Package collect 实现 uni_agent 的指标采集：把本机真实状态采成
// agentproto.MetricsSample。
//
// 设计约束（来自 spec §6.1，这里逐条落地，不要「简化」掉）：
//
//   - **缺值 != 0**。指针字段为 nil 表示「采不到」（如非 Linux 无 iowait、
//     容器里读不到温度），消费端据此显示「—」。把采不到写成 0 会让图上出现
//     一条贴着 0 的假线，比缺数据更糟。
//   - **速率必须做差量**。/proc 里的 disk_io / 网卡是**单调累计计数器**，
//     直接上报会把「开机的总流量」当成「每秒流量」。差量需要基线，
//     故首帧整体省略该条目（不发 0）—— 0 是「真的没流量」，不是「还没基线」。
//   - **过滤伪设备**。loop/ram/zram/容器网桥这些不是运维关心的实体，
//     放进去会污染磁盘合计与网卡合计（Σ 双计）。
package collect

import (
	"math"
	"sort"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Options 是采集器的可调参数。零值即为默认（见 New）。
type Options struct {
	// Interval 是两次采集的间隔，决定速率差量的 dt。<=0 时用 10s。
	Interval time.Duration
}

// Collector 持有一份跨帧状态（速率基线、静态信息缓存），故**不是**无状态的：
// 每次 Sample 都依赖上一帧的计数器读数。
//
// 并发：Sample 由单一采集 goroutine 调用；Static 可被任意 goroutine 读取
// （用锁保护）。不要并发调用 Sample。
type Collector struct {
	interval time.Duration

	// 速率基线。磁盘与网卡**各一张表**：两者名字空间独立，
	// 共用一张会在同名项（如网卡 eth0 与某块盘）上互相覆盖，算出错误的速率。
	// 键用设备名而非路径：重启后 /dev/sda 可能变 /dev/sdb，用名更稳。
	mu      sync.Mutex
	prevIO  map[string]ioCounters
	prevNet map[string]ioCounters
	prevTS  time.Time

	// 静态信息只采一次（CPU 型号、内核、内存总量都在启动后不变）。
	staticOnce sync.Once
	static     *agentproto.Hello

	// 自监控计数（AgentMetric）。由采集/上报两侧共同写入，故同用 mu 保护。
	startedAt           time.Time
	reportOK            int64
	reportErr           int64
	reconnects          int64
	lastReportErr       string
	lastReportLatencyMs float64
	lastCollectMs       float64

	// 回调解耦：采集器不直接依赖 transport，避免包循环。
	backlogFn func() int64
	dropFn    func() int64
}

// ioCounters 是一份累计计数器快照。
type ioCounters struct {
	readBytes  uint64
	writeBytes uint64
	readOps    uint64
	writeOps   uint64
	// ioTimeMs 是该设备「累计有 IO 在进行」的毫秒数（/proc/diskstats 的 io_ticks）。
	// 差量 ÷ 经过时间 = %util，与 iostat 的口径一致。
	ioTimeMs  uint64
	rxBytes   uint64
	txBytes   uint64
	rxPackets uint64
	txPackets uint64
	rxErrors  uint64
	txErrors  uint64
	rxDropped uint64
}

// New 构造采集器。Options.Interval 同时是差量的期望 dt，故必须与上报周期一致。
func New(opts Options) *Collector {
	iv := opts.Interval
	if iv <= 0 {
		iv = 10 * time.Second
	}
	return &Collector{
		interval:  iv,
		prevIO:    make(map[string]ioCounters),
		prevNet:   make(map[string]ioCounters),
		startedAt: time.Now(),
	}
}

// Sample 采集一帧完整样本。
//
// 返回的 sample 已经过 sanitize + Validate；调用方可以直接上报。
// 若 Validate 失败，返回的 err 说明哪个字段不合法 —— 这属于**程序错误**
// （说明某处 sanitize 漏了），不要静默丢弃后继续当作正常。
func (c *Collector) Sample(now time.Time) (*agentproto.MetricsSample, error) {
	began := time.Now()
	s := &agentproto.MetricsSample{T: now.UnixMilli()}

	c.collectCPU(s)
	c.collectMemory(s)
	c.collectLoad(s)
	c.collectDisk(s)
	c.collectIOAndNICs(s, now)
	c.collectTCP(s)
	c.collectSensors(s)
	c.collectUptime(s)

	// 采集耗时必须在**采集之后、自监控之前**结算：AgentMetric 自己
	// 不能把「统计自身的开销」也算进本次采集里，否则这个数永远偏高。
	elapsed := float64(time.Since(began).Microseconds()) / 1000
	c.mu.Lock()
	c.lastCollectMs = math.Round(elapsed*100) / 100
	c.mu.Unlock()

	c.collectAgentSelf(s)

	sanitize(s)
	// 兜底：sanitize 之后仍不合法说明有字段没被覆盖到，直接冒泡而不是发出去。
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Static 返回 hello 用的静态信息（进程内只真正采一次）。
//
// 为什么单独一份：这些值在设备重启前不变，每帧带上是浪费；
// 而且 hello 只在注册/重连时发一次，正好匹配。
func (c *Collector) Static(agentVersion string) *agentproto.Hello {
	c.staticOnce.Do(func() {
		c.static = buildStatic(agentVersion)
	})
	// 拷贝一份返回：调用方会往 EnrollToken/AgentToken 里塞凭据，
	// 直接返回内部指针会让凭据残留到下一次调用。
	out := *c.static
	return &out
}

// sortedNames 返回排序后的 map 键，让采集结果**稳定有序**。
//
// 顺序重要：同一批数据每次顺序不同，会让 HTTP 响应/测试断言/前端图例抖动，
// 也会让「按名去重」的聚合出现非确定结果。
func sortedNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetBacklogSource 注入「待上报积压数」与「累计丢弃数」的读取回调。
//
// 用回调而不是直接持有 transport.Client：collect 是纯采集包，
// 让它 import transport 会形成 transport→collect→transport 的循环依赖。
func (c *Collector) SetBacklogSource(backlog, drops func() int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backlogFn = backlog
	c.dropFn = drops
}

// NoteReportResult 由上报侧调用，记录一次上报的成败与耗时。
//
// 放在这里（而不是 transport 里自记）：AgentMetric 是随样本一起上报的，
// 只有采集器持有它，两边各记一份必然会不一致。
func (c *Collector) NoteReportResult(err error, latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.reportErr++
		c.lastReportErr = err.Error()
		return
	}
	c.reportOK++
	c.lastReportErr = ""
	c.lastReportLatencyMs = math.Round(float64(latency.Microseconds())/1000*100) / 100
}

// NoteReconnect 由传输层在每次重新建立连接时调用。
func (c *Collector) NoteReconnect() {
	c.mu.Lock()
	c.reconnects++
	c.mu.Unlock()
}
