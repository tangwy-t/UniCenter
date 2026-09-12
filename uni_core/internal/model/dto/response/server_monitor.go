package response

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// ServerMonitorResp is the top-level response for /api/v1/monitor/server.
type ServerMonitorResp struct {
	Server     ServerInfo    `json:"server"`
	Host       HostInfo      `json:"host"`
	CPU        CPUInfo       `json:"cpu"`
	Memory     MemoryInfo    `json:"memory"`
	Goroutines GoroutineInfo `json:"goroutines"`
	GC         GCInfo        `json:"gc"`
	Disk       DiskInfo      `json:"disk"`
}

// ServerInfo holds version and uptime information.
type ServerInfo struct {
	Version       string `json:"version"`
	BuildTime     string `json:"buildTime"`
	CommitHash    string `json:"commitHash"`
	StartTime     string `json:"startTime"`
	Uptime        string `json:"uptime"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	Error         string `json:"error,omitempty"`
}

// HostInfo holds host-level identity and uptime information.
// Every field is best-effort: on failure the field stays empty and Error explains why.
type HostInfo struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platformVersion"`
	KernelVersion   string `json:"kernelVersion"`
	KernelArch      string `json:"kernelArch"`
	GoVersion       string `json:"goVersion"`
	Arch            string `json:"arch"`
	Pid             int    `json:"pid"`
	BootTime        string `json:"bootTime"`
	UptimeSeconds   int64  `json:"uptimeSeconds"`
	Error           string `json:"error,omitempty"`
}

// CPUInfo holds CPU usage information.
type CPUInfo struct {
	NumCPU       int       `json:"numCPU"`
	UsagePercent *float64  `json:"usagePercent"`
	PerCore      []float64 `json:"perCore,omitempty"`
	Load         *LoadInfo `json:"load,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// LoadInfo holds the 1/5/15 minute system load averages.
type LoadInfo struct {
	Load1  *float64 `json:"load1"`
	Load5  *float64 `json:"load5"`
	Load15 *float64 `json:"load15"`
	Error  string   `json:"error,omitempty"`
}

// MemoryInfo holds process memory statistics in megabytes.
type MemoryInfo struct {
	AllocMB      float64           `json:"allocMB"`
	TotalAllocMB float64           `json:"totalAllocMB"`
	SysMB        float64           `json:"sysMB"`
	HeapAllocMB  float64           `json:"heapAllocMB"`
	HeapSysMB    float64           `json:"heapSysMB"`
	System       *SystemMemoryInfo `json:"system,omitempty"`
	Swap         *SwapMemoryInfo   `json:"swap,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// SystemMemoryInfo holds physical memory statistics in megabytes.
type SystemMemoryInfo struct {
	TotalMB     float64 `json:"totalMB"`
	UsedMB      float64 `json:"usedMB"`
	AvailableMB float64 `json:"availableMB"`
	UsedPercent float64 `json:"usedPercent"`
}

// SwapMemoryInfo holds swap partition statistics in megabytes.
type SwapMemoryInfo struct {
	TotalMB     float64 `json:"totalMB"`
	UsedMB      float64 `json:"usedMB"`
	FreeMB      float64 `json:"freeMB"`
	UsedPercent float64 `json:"usedPercent"`
}

// GoroutineInfo holds goroutine count.
type GoroutineInfo struct {
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// GCInfo holds garbage collection statistics.
type GCInfo struct {
	NumGC        uint32   `json:"numGC"`
	PauseTotalMs *float64 `json:"pauseTotalMs"`
	LastPauseMs  *float64 `json:"lastPauseMs"`
	Error        string   `json:"error,omitempty"`
}

// DiskInfo holds disk usage information for the working directory.
type DiskInfo struct {
	Path         string  `json:"path"`
	TotalGB      float64 `json:"totalGB"`
	UsedGB       float64 `json:"usedGB"`
	FreeGB       float64 `json:"freeGB"`
	UsagePercent float64 `json:"usagePercent"`
	Error        string  `json:"error,omitempty"`
}

// ServerHistoryPoint 服务器监控历史时序的单桶指标。
// 指针字段为 nil 时 JSON 省略:表示桶内没有任何采样点提供该指标
// (与改造前的 serverstats.Bucket 完全一致)。
type ServerHistoryPoint struct {
	Timestamp  util.JSONTime `json:"timestamp"`
	CPU        *float64      `json:"cpu,omitempty"`
	MemSys     *float64      `json:"memSys,omitempty"`
	HeapAlloc  *float64      `json:"heapAlloc,omitempty"`
	SysMem     *float64      `json:"sysMem,omitempty"`
	Goroutines *float64      `json:"goroutines,omitempty"`
	GCNum      *uint32       `json:"gcNum,omitempty"`
	GCPauseMs  *float64      `json:"gcPauseMs,omitempty"`
	Disk       *float64      `json:"disk,omitempty"`
	Load1      *float64      `json:"load1,omitempty"`
	Uptime     *int64        `json:"uptime,omitempty"`
}

// ServerHistorySnapshot 服务器监控历史时序查询响应(HTTP 契约)。
type ServerHistorySnapshot struct {
	WindowSeconds int64                `json:"window_seconds"`
	StepSeconds   int64                `json:"step_seconds"`
	Buckets       []ServerHistoryPoint `json:"buckets"`
}
