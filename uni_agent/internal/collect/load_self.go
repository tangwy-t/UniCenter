package collect

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// collectLoad 采集 1/5/15 分钟负载。
//
// 取数口径：直接读 `/proc/loadavg`，**不用** gopsutil 的 load.Avg()。
// 原因：`load.Avg()` 在 load 不支持的平台会返回错误，而契约里
// load1/5/15 是**非指针 float64**（不可缺失），必须给出 0。
// 直读文件让「读到」与「读不到（非 Linux）」两条路径都很明确。
//
// spec §6.1「Windows 无 loadavg → load=0.0」：0 在这里是**约定值**，
// 前端把它视作「本平台不提供该指标」而不画线。
func (c *Collector) collectLoad(s *agentproto.MetricsSample) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return // 非 Linux：留 0（契约要求非空，0 即「无此指标」）
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return
	}
	if v, ok := parseFloat(fields[0]); ok {
		s.Load1 = nonNeg(v)
	}
	if v, ok := parseFloat(fields[1]); ok {
		s.Load5 = nonNeg(v)
	}
	if v, ok := parseFloat(fields[2]); ok {
		s.Load15 = nonNeg(v)
	}
}

// parseFloat 解析并拒绝 NaN/±Inf —— 这类值一旦进入样本会被 Validate 拒掉整帧。
func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, !math.IsNaN(v) && !math.IsInf(v, 0)
}

// collectAgentSelf 采集 agent 自身的运行指标（spec §5.5 AgentMetric）。
//
// 这批数据的价值是「让 core 侧能看出 agent 自己出问题了」：
// 重连次数飙升 = 网络不稳；积压/丢弃 > 0 = 上报跟不上采集；
// 常驻内存增长 = 疑似泄漏。没有它们，agent 静默降级时页面上只表现为
// 「指标断档」，运维无法区分「机器没数据」与「agent 挂了」。
func (c *Collector) collectAgentSelf(s *agentproto.MetricsSample) {
	now := time.Now()

	c.mu.Lock()
	success, errCount := c.reportOK, c.reportErr
	reconnects := c.reconnects
	lastErr := c.lastReportErr
	lastLatency := c.lastReportLatencyMs
	collected := c.lastCollectMs
	started := c.startedAt
	c.mu.Unlock()

	var backlog int64
	if c.backlogFn != nil {
		backlog = c.backlogFn()
	}
	var drops int64
	if c.dropFn != nil {
		drops = c.dropFn()
	}

	m := agentproto.AgentMetric{
		CollectDurationMs:   collected,
		ReportSuccessCount:  success,
		ReportErrorCount:    errCount,
		LastReportError:     lastErr,
		WSReconnectCount:    reconnects,
		PendingBacklog:      &backlog,
		ReportDropCount:     &drops,
		LastReportLatencyMs: &lastLatency,
	}
	if mem := residentMB(); mem > 0 {
		m.MemResidentMB = &mem
	}
	if !started.IsZero() {
		up := int64(now.Sub(started).Seconds())
		m.AgentUptimeSec = &up
	}
	s.Agent = m
}

// residentMB 返回本进程常驻内存（MiB）。
//
// 读 /proc/self/statm 而不是 runtime.MemStats：前者是 RSS（真实占用），
// 后者是 Go 堆的统计口径，两者差着好几倍，用它看「内存涨没涨」会误判。
func residentMB() float64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, ok := parseFloat(fields[1])
	if !ok {
		return 0
	}
	// statm 的第二个字段是 RSS，单位是页。
	return math.Round(pages * float64(os.Getpagesize()) / (1024 * 1024))
}
