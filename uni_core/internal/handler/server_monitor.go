package handler

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"go.uber.org/zap"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/serverstats"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"github.com/tangwy-t/webmanager-server/internal/pkg/version"
)

// defaultSampleInterval 后台采样协程的默认采集节奏。
// 必须 >= 1s:gopsutil 的 cpu.Percent 靠两次调用的差值计算,
// 间隔不足会返回 0 或失真的百分比;2s 兼顾数据新鲜度与开销。
const defaultSampleInterval = 2 * time.Second

// ServerMonitorHandler exposes HTTP handler for server monitoring endpoints.
// 指标缓存策略:采样协程按固定节奏(默认 2s)主动采集一份完整快照,
// HTTP 请求直接读取最近快照,不再逐请求现场采集。这样:
//   - runtime.ReadMemStats 的 STW 暂停从"每次请求"降为"每采样周期"
//   - 不再重复读 /proc(/proc/meminfo、/proc/loadavg 等)
//   - CPU 采样间隔由采样协程节奏保证,不受请求频率/多标签页干扰
type ServerMonitorHandler struct {
	logger    logger.LoggerInterface
	startTime time.Time
	interval  time.Duration

	// snapshot 保存最近一次完整采集结果。发布后内容不再被修改
	// (每次采集都构造全新对象),多个请求并发 JSON 序列化是安全的。
	snapshot atomic.Pointer[response.ServerMonitorResp]

	stopCh    chan struct{}
	closeOnce sync.Once

	// store 历史采样滚动窗口;nil 时跳过历史追加、接口返回 500(测试友好)。
	store serverstats.Store
	// lastAppendErrAt 上次 Append 告警的 unix 纳秒:失败节流,避免每 2s 刷屏。
	lastAppendErrAt atomic.Int64
}

// NewServerMonitorHandler creates a new ServerMonitorHandler.
// 构造时调用 cpu.Percent 建立基线(累计与每核各自独立打点),随后启动
// 采样协程;首个样本在第一个采样周期发布,之前的请求走同步兜底采集。
// 可选 sampleInterval 参数仅供测试注入更短节奏,生产使用默认 2s。
func NewServerMonitorHandler(logger logger.LoggerInterface, store serverstats.Store, sampleInterval ...time.Duration) *ServerMonitorHandler {
	interval := defaultSampleInterval
	if len(sampleInterval) > 0 && sampleInterval[0] > 0 {
		interval = sampleInterval[0]
	}
	h := &ServerMonitorHandler{
		logger:    logger,
		startTime: time.Now(),
		interval:  interval,
		stopCh:    make(chan struct{}),
		store:     store,
	}
	// gopsutil requires at least 1 second between calls to compute delta.
	_, _ = cpu.Percent(0, false)
	_, _ = cpu.Percent(0, true)

	go h.run()
	return h
}

// run 是采样协程主循环,按固定节奏刷新缓存快照。
func (h *ServerMonitorHandler) run() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.collectAndPublish()
		case <-h.stopCh:
			return
		}
	}
}

// Close 停止采样协程(幂等,可注册到 lifecycle 关闭钩子)。
func (h *ServerMonitorHandler) Close() {
	h.closeOnce.Do(func() {
		close(h.stopCh)
	})
}

// CachedSnapshot 返回最近一次缓存快照;首个采样周期前为 nil。
func (h *ServerMonitorHandler) CachedSnapshot() *response.ServerMonitorResp {
	return h.snapshot.Load()
}

// ServeHTTP handles GET /api/v1/monitor/server/stats.
// @Summary      服务器监控
// @Description  返回服务器运行信息：版本、CPU、内存、Goroutine、GC、磁盘
// @Tags         服务器监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.ServerMonitorResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /monitor/server/stats [get]
func (h *ServerMonitorHandler) ServeHTTP(c *gin.Context) {
	snap := h.snapshot.Load()
	if snap == nil {
		// 极端时序兜底(进程刚启动、第一个采样周期之前):同步采集一次。
		// 理论上不会与采样协程产生数据竞争:snapshot 是无锁原子替换。
		h.collectAndPublish()
		snap = h.snapshot.Load()
	}
	if snap == nil {
		// 理论不可达(collectAndPublish 无条件发布);保留空响应兜底。
		app.Success(c, response.ServerMonitorResp{})
		return
	}
	app.Success(c, *snap)
}

// GetHistory handles GET /api/v1/monitor/server/history.
// @Summary      服务器监控时序
// @Description  返回指定时间窗口内按 step 聚合的服务器指标时序(CPU/内存/GC/磁盘等),支撑前端趋势图
// @Tags         服务器监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        window  query     string  false  "时间窗口,如 15m,1h(默认 15m,范围 1m~24h)"
// @Param        step    query     string  false  "桶粒度,如 3s,10s(默认 window/180,最小 1s)"
// @Success      200     {object}  app.Response{data=response.ServerHistorySnapshot}  "查询成功"
// @Failure      400     {object}  app.Response  "参数无效"
// @Failure      401     {object}  app.Response  "未登录"
// @Failure      403     {object}  app.Response  "无权限"
// @Router       /monitor/server/history [get]
func (h *ServerMonitorHandler) GetHistory(c *gin.Context) {
	windowStr := c.Query("window")
	var window time.Duration
	if windowStr == "" {
		window = 15 * time.Minute
	} else {
		parsed, err := time.ParseDuration(windowStr)
		if err != nil {
			app.Error(c, apperror.BadRequest("参数错误: window 格式无效，示例: 15m, 1h"))
			return
		}
		window = parsed
	}
	if window < time.Minute || window > 24*time.Hour {
		app.Error(c, apperror.BadRequest("参数错误: window 取值范围为 1m ~ 24h"))
		return
	}

	stepStr := c.Query("step")
	var step time.Duration
	if stepStr == "" {
		step = window / 180
		if step < time.Second {
			step = time.Second
		}
	} else {
		parsed, err := time.ParseDuration(stepStr)
		if err != nil || parsed < time.Second || parsed > window {
			app.Error(c, apperror.BadRequest("参数错误: step 格式无效，取值范围为 1s ~ window"))
			return
		}
		step = parsed
	}

	if h.store == nil {
		app.Error(c, apperror.Internal("服务器监控历史存储未配置"))
		return
	}
	snap, err := h.store.Query(c.Request.Context(), window, step)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, serverHistorySnapshotFromStats(snap))
}

// serverHistorySnapshotFromStats 把存储层快照适配为 HTTP 契约 DTO
// (字段与 JSON 形状逐一对应,不产生线上行为变化)。
func serverHistorySnapshotFromStats(src *serverstats.Snapshot) *response.ServerHistorySnapshot {
	dst := &response.ServerHistorySnapshot{
		WindowSeconds: src.WindowSeconds,
		StepSeconds:   src.StepSeconds,
		Buckets:       make([]response.ServerHistoryPoint, 0, len(src.Buckets)),
	}
	for _, b := range src.Buckets {
		dst.Buckets = append(dst.Buckets, response.ServerHistoryPoint{
			Timestamp:  b.Timestamp,
			CPU:        b.CPU,
			MemSys:     b.MemSys,
			HeapAlloc:  b.HeapAlloc,
			SysMem:     b.SysMem,
			Goroutines: b.Goroutines,
			GCNum:      b.GCNum,
			GCPauseMs:  b.GCPauseMs,
			Disk:       b.Disk,
			Load1:      b.Load1,
			Uptime:     b.Uptime,
		})
	}
	return dst
}

// collectAndPublish 采集一轮完整指标并发布为新快照。
func (h *ServerMonitorHandler) collectAndPublish() {
	elapsed := time.Since(h.startTime)

	// Call runtime.ReadMemStats once so both Memory and GC collectors
	// share the same snapshot, avoiding a second STW pause.
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	resp := response.ServerMonitorResp{
		Server:     h.collectServerInfo(elapsed),
		Host:       h.collectHost(),
		CPU:        h.collectCPU(),
		Memory:     h.collectMemory(&memStats),
		Goroutines: h.collectGoroutines(),
		GC:         h.collectGC(&memStats),
		Disk:       h.collectDisk(),
	}

	h.snapshot.Store(&resp)
	h.appendHistory(&resp)
}

// appendHistory 把本轮采样追加到 Redis 历史滚动窗口。
// 失败节流 Warn(≥30s 一次):历史缺失不影响快照接口,不阻塞采集循环。
func (h *ServerMonitorHandler) appendHistory(resp *response.ServerMonitorResp) {
	if h.store == nil {
		return
	}
	p := serverstats.Point{
		T:          time.Now().UnixMilli(),
		CPU:        util.Coalesce(resp.CPU.UsagePercent),
		MemSys:     coalesceSystemMem(resp.Memory.System),
		HeapAlloc:  resp.Memory.HeapAllocMB,
		SysMem:     resp.Memory.SysMB,
		Goroutines: uint64(resp.Goroutines.Count),
		GCNum:      resp.GC.NumGC,
		GCPauseMs:  resp.GC.LastPauseMs,
		Disk:       resp.Disk.UsagePercent,
		Uptime:     resp.Server.UptimeSeconds,
	}
	if resp.CPU.Load != nil {
		p.Load1 = resp.CPU.Load.Load1
	}
	if err := h.store.Append(context.Background(), p); err != nil {
		now := time.Now().UnixNano()
		if now-h.lastAppendErrAt.Load() >= 30*time.Second.Nanoseconds() {
			h.logger.Warn("failed to append server history point", zap.Error(err))
			h.lastAppendErrAt.Store(now)
		}
	}
}

// coalesceSystemMem 系统内存信息缺失时返回 0。
func coalesceSystemMem(v *response.SystemMemoryInfo) float64 {
	if v == nil {
		return 0
	}
	return v.UsedPercent
}

// collectServerInfo gathers version, build time, commit hash, and uptime.
func (h *ServerMonitorHandler) collectServerInfo(elapsed time.Duration) response.ServerInfo {
	return response.ServerInfo{
		Version:       version.Version,
		BuildTime:     version.BuildTime,
		CommitHash:    version.CommitHash,
		StartTime:     h.startTime.Format("2006-01-02 15:04:05"),
		Uptime:        util.FormatDuration(elapsed),
		UptimeSeconds: int64(elapsed.Seconds()),
	}
}

// collectHost gathers host identity, boot time, and system uptime.
// goVersion/arch/pid always succeed; the gopsutil-backed fields are best-effort.
func (h *ServerMonitorHandler) collectHost() response.HostInfo {
	info := response.HostInfo{
		GoVersion: runtime.Version(),
		Arch:      runtime.GOARCH,
		Pid:       os.Getpid(),
	}

	hostInfo, err := host.Info()
	if err != nil {
		h.logger.Warn("failed to collect host info", zap.Error(err))
		info.Error = "host info collection failed"
	} else {
		info.Hostname = hostInfo.Hostname
		info.OS = hostInfo.OS
		info.Platform = hostInfo.Platform
		info.PlatformVersion = hostInfo.PlatformVersion
		info.KernelVersion = hostInfo.KernelVersion
		info.KernelArch = hostInfo.KernelArch
	}

	uptime, err := host.Uptime()
	if err != nil {
		h.logger.Warn("failed to collect system uptime", zap.Error(err))
	} else {
		info.UptimeSeconds = int64(uptime)
		info.BootTime = time.Now().Add(-time.Duration(uptime) * time.Second).Format("2006-01-02 15:04:05")
	}

	return info
}

// collectCPU gathers CPU usage percentage using gopsutil.
func (h *ServerMonitorHandler) collectCPU() response.CPUInfo {
	info := response.CPUInfo{
		NumCPU: runtime.NumCPU(),
	}
	percentages, err := cpu.Percent(0, false)
	if err != nil {
		h.logger.Warn("failed to collect CPU usage", zap.Error(err))
		info.Error = "cpu percent collection failed"
		return info
	}
	if len(percentages) == 0 {
		info.Error = "cpu percent returned empty"
		return info
	}
	p := util.Round2(percentages[0])
	info.UsagePercent = &p

	// 每核心使用率，用于前端核心热力图，失败不影响整体响应。
	perCore, err := cpu.Percent(0, true)
	if err != nil {
		h.logger.Warn("failed to collect per-core CPU usage", zap.Error(err))
	} else {
		info.PerCore = make([]float64, 0, len(perCore))
		for _, v := range perCore {
			info.PerCore = append(info.PerCore, util.Round2(v))
		}
	}

	// 系统负载均值（1/5/15 分钟），Windows/受限环境下为 nil。
	avg, err := load.Avg()
	if err != nil {
		h.logger.Warn("failed to collect load average", zap.Error(err))
	} else {
		l1 := util.Round2(avg.Load1)
		l5 := util.Round2(avg.Load5)
		l15 := util.Round2(avg.Load15)
		info.Load = &response.LoadInfo{Load1: &l1, Load5: &l5, Load15: &l15}
	}
	return info
}

// collectMemory gathers memory allocation stats from a pre-collected runtime.MemStats snapshot.
func (h *ServerMonitorHandler) collectMemory(m *runtime.MemStats) response.MemoryInfo {

	alloc := util.BytesToMB(m.Alloc)
	totalAlloc := util.BytesToMB(m.TotalAlloc)
	sys := util.BytesToMB(m.Sys)
	heapAlloc := util.BytesToMB(m.HeapAlloc)
	heapSys := util.BytesToMB(m.HeapSys)

	info := response.MemoryInfo{
		AllocMB:      alloc,
		TotalAllocMB: totalAlloc,
		SysMB:        sys,
		HeapAllocMB:  heapAlloc,
		HeapSysMB:    heapSys,
	}

	// 系统物理内存概览（总量/已用/可用/使用率），用于内存水位展示。
	vm, err := mem.VirtualMemory()
	if err != nil {
		h.logger.Warn("failed to collect system memory", zap.Error(err))
	} else {
		info.System = &response.SystemMemoryInfo{
			TotalMB:     util.BytesToMB(vm.Total),
			UsedMB:      util.BytesToMB(vm.Used),
			AvailableMB: util.BytesToMB(vm.Available),
			UsedPercent: util.Round2(vm.UsedPercent),
		}
	}

	// 交换分区（未配置或未启用时为 nil）。
	swap, err := mem.SwapMemory()
	if err != nil {
		h.logger.Warn("failed to collect swap memory", zap.Error(err))
	} else {
		info.Swap = &response.SwapMemoryInfo{
			TotalMB:     util.BytesToMB(swap.Total),
			UsedMB:      util.BytesToMB(swap.Used),
			FreeMB:      util.BytesToMB(swap.Free),
			UsedPercent: util.Round2(swap.UsedPercent),
		}
	}

	return info
}

// collectGoroutines gathers the current goroutine count.
func (h *ServerMonitorHandler) collectGoroutines() response.GoroutineInfo {
	count := runtime.NumGoroutine()
	return response.GoroutineInfo{
		Count: count,
	}
}

// collectGC gathers GC statistics from a pre-collected runtime.MemStats snapshot.
func (h *ServerMonitorHandler) collectGC(m *runtime.MemStats) response.GCInfo {

	numGC := m.NumGC
	pauseTotalMs := util.Round2(float64(m.PauseTotalNs) / 1e6)
	var lastPauseMs *float64
	if numGC > 0 {
		// PauseNs is a circular buffer of 256 entries; the most recent pause
		// is at index (numGC+255) % 256.
		idx := (numGC + 255) % 256
		lpm := util.Round2(float64(m.PauseNs[idx]) / 1e6)
		lastPauseMs = &lpm
	}

	return response.GCInfo{
		NumGC:        numGC,
		PauseTotalMs: &pauseTotalMs,
		LastPauseMs:  lastPauseMs,
	}
}

// collectDisk gathers disk usage for the current working directory.
// 字节数获取是平台相关的:Unix 走 statfs(2),Windows 走
// GetDiskFreeSpaceExW,分别位于 server_monitor_disk_unix.go 与
// server_monitor_disk_windows.go(syscall.Statfs 在 Windows 不存在,
// 此前未做平台分离导致 Windows 无法编译)。
func (h *ServerMonitorHandler) collectDisk() response.DiskInfo {
	info := response.DiskInfo{}

	wd, err := os.Getwd()
	if err != nil {
		h.logger.Warn("failed to get working directory for disk stats", zap.Error(err))
		info.Error = "getwd failed"
		return info
	}
	info.Path = wd

	totalBytes, freeBytes, err := diskUsage(wd)
	if err != nil {
		h.logger.Warn("failed to get disk usage", zap.Error(err))
		info.Error = "disk usage query failed"
		return info
	}

	usedBytes := totalBytes - freeBytes

	totalGB := util.Round2(float64(totalBytes) / 1e9)
	usedGB := util.Round2(float64(usedBytes) / 1e9)
	freeGB := util.Round2(float64(freeBytes) / 1e9)
	var usagePercent float64
	if totalBytes > 0 {
		usagePercent = util.Round2(float64(usedBytes) / float64(totalBytes) * 100)
	}

	info.TotalGB = totalGB
	info.UsedGB = usedGB
	info.FreeGB = freeGB
	info.UsagePercent = usagePercent
	return info
}
