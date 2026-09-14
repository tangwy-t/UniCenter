package agentproto

import "math"

// MetricsSample 是一次整机指标采样（agent 周期上报的载荷）。
//
// 约定：
//   - T 为采样时刻的 unix 毫秒。
//   - *_percent 一律 0-100（不是 0-1 小数）。
//   - *_mb / *_gb 一律二进制（MiB / GiB）。
//   - 速率字段一律 *_per_sec。
//   - 指针字段（*float64 / *int64）为 nil 表示「未采集」，绝不与 0 混淆。
type MetricsSample struct {
	T int64 `json:"t"`

	CPUUsedPercent float64   `json:"cpu_used_percent"`
	CPUPerCore     []float64 `json:"cpu_per_core,omitempty"`
	CPUIOWait      *float64  `json:"cpu_iowait,omitempty"`
	Load1          float64   `json:"load1"`
	Load5          float64   `json:"load5"`
	Load15         float64   `json:"load15"`

	MemUsedPercent  float64  `json:"mem_used_percent"`
	MemUsedMB       float64  `json:"mem_used_mb"`
	MemAvailableMB  float64  `json:"mem_available_mb"`
	SwapUsedPercent *float64 `json:"swap_used_percent,omitempty"`
	SwapUsedMB      *float64 `json:"swap_used_mb,omitempty"`

	Disks  []DiskMetric   `json:"disks,omitempty"`
	DiskIO []DiskIOMetric `json:"disk_io,omitempty"`
	NICs   []NICMetric    `json:"nics,omitempty"`

	TCPTotal       int `json:"tcp_total"`
	TCPEstablished int `json:"tcp_established"`
	TCPListen      int `json:"tcp_listen"`
	TCPTimeWait    int `json:"tcp_time_wait,omitempty"`
	TCPCloseWait   int `json:"tcp_close_wait,omitempty"`
	UDPTotal       int `json:"udp_total"`

	ProcCount int            `json:"proc_count"`
	Sensors   []SensorMetric `json:"sensors,omitempty"`
	UptimeSec int64          `json:"uptime_sec"`

	Agent AgentMetric `json:"agent"`
}

// DiskMetric 是单个挂载点的磁盘使用情况。
type DiskMetric struct {
	Mountpoint        string  `json:"mountpoint"`
	FSType            string  `json:"fs_type"`
	UsedPercent       float64 `json:"used_percent"`
	UsedGB            float64 `json:"used_gb"`
	TotalGB           float64 `json:"total_gb"`
	InodesUsedPercent float64 `json:"inodes_used_percent,omitempty"`
}

// DiskIOMetric 是单个块设备的 IO 速率（由累积计数器差量求得）。
type DiskIOMetric struct {
	Name             string  `json:"name"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
	ReadOpsPerSec    float64 `json:"read_ops_per_sec,omitempty"`
	WriteOpsPerSec   float64 `json:"write_ops_per_sec,omitempty"`
	IOTimePercent    float64 `json:"io_time_percent,omitempty"`
}

// NICMetric 是单张网卡的收发速率（由累积计数器差量求得）。
type NICMetric struct {
	Name            string  `json:"name"`
	RXBytesPerSec   float64 `json:"rx_bytes_per_sec"`
	TXBytesPerSec   float64 `json:"tx_bytes_per_sec"`
	RXPacketsPerSec float64 `json:"rx_packets_per_sec,omitempty"`
	TXPacketsPerSec float64 `json:"tx_packets_per_sec,omitempty"`
	RXErrorsPerSec  float64 `json:"rx_errors_per_sec,omitempty"`
	TXErrorsPerSec  float64 `json:"tx_errors_per_sec,omitempty"`
	RXDroppedPerSec float64 `json:"rx_dropped_per_sec,omitempty"`
}

// SensorMetric 是单个温度传感器读数；TemperatureC 为 nil 表示读不到。
type SensorMetric struct {
	Name         string   `json:"name"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
}

// AgentMetric 是 agent 对自身的监控（RED）。计数类均为「自进程启动累计」的单调值。
type AgentMetric struct {
	CollectDurationMs   float64  `json:"collect_duration_ms"`
	ReportSuccessCount  int64    `json:"report_success_count"`
	ReportErrorCount    int64    `json:"report_error_count"`
	LastReportError     string   `json:"last_report_error,omitempty"`
	WSReconnectCount    int64    `json:"ws_reconnect_count"`
	MemResidentMB       *float64 `json:"mem_resident_mb,omitempty"`
	PendingBacklog      *int64   `json:"pending_backlog,omitempty"`
	LastReportLatencyMs *float64 `json:"last_report_latency_ms,omitempty"`
	ReportDropCount     *int64   `json:"report_drop_count,omitempty"`
	AgentUptimeSec      *int64   `json:"agent_uptime_sec,omitempty"`
}

// Validate 校验采样样本的语义。结构合法但语义非法的情形在这里被拒。
func (s *MetricsSample) Validate() error {
	if s.T <= 0 {
		return decodeErr(StagePayload, "t", ErrInvalidTimestamp)
	}

	if err := checkPercent("cpu_used_percent", s.CPUUsedPercent); err != nil {
		return err
	}
	for _, v := range s.CPUPerCore {
		if err := checkPercent("cpu_per_core", v); err != nil {
			return err
		}
	}
	if err := checkPercentPtr("cpu_iowait", s.CPUIOWait); err != nil {
		return err
	}
	for _, c := range []struct {
		field string
		v     float64
	}{{"load1", s.Load1}, {"load5", s.Load5}, {"load15", s.Load15}} {
		if err := checkNonNeg(c.field, c.v); err != nil {
			return err
		}
	}

	if err := checkPercent("mem_used_percent", s.MemUsedPercent); err != nil {
		return err
	}
	if err := checkNonNeg("mem_used_mb", s.MemUsedMB); err != nil {
		return err
	}
	if err := checkNonNeg("mem_available_mb", s.MemAvailableMB); err != nil {
		return err
	}
	if err := checkPercentPtr("swap_used_percent", s.SwapUsedPercent); err != nil {
		return err
	}
	if err := checkNonNegPtr("swap_used_mb", s.SwapUsedMB); err != nil {
		return err
	}

	for _, d := range s.Disks {
		if d.Mountpoint == "" {
			return decodeErr(StagePayload, "disks.mountpoint", ErrMissingField)
		}
		if err := checkPercent("disks.used_percent", d.UsedPercent); err != nil {
			return err
		}
		if err := checkNonNeg("disks.used_gb", d.UsedGB); err != nil {
			return err
		}
		if err := checkNonNeg("disks.total_gb", d.TotalGB); err != nil {
			return err
		}
		if d.UsedGB > d.TotalGB {
			return decodeErr(StagePayload, "disks.used_gb", ErrInvalidPayload)
		}
		if err := checkPercent("disks.inodes_used_percent", d.InodesUsedPercent); err != nil {
			return err
		}
	}

	for _, d := range s.DiskIO {
		if d.Name == "" {
			return decodeErr(StagePayload, "disk_io.name", ErrMissingField)
		}
		for _, c := range []struct {
			field string
			v     float64
		}{
			{"disk_io.read_bytes_per_sec", d.ReadBytesPerSec},
			{"disk_io.write_bytes_per_sec", d.WriteBytesPerSec},
			{"disk_io.read_ops_per_sec", d.ReadOpsPerSec},
			{"disk_io.write_ops_per_sec", d.WriteOpsPerSec},
		} {
			if err := checkNonNeg(c.field, c.v); err != nil {
				return err
			}
		}
		if err := checkPercent("disk_io.io_time_percent", d.IOTimePercent); err != nil {
			return err
		}
	}

	for _, n := range s.NICs {
		if n.Name == "" {
			return decodeErr(StagePayload, "nics.name", ErrMissingField)
		}
		for _, c := range []struct {
			field string
			v     float64
		}{
			{"nics.rx_bytes_per_sec", n.RXBytesPerSec},
			{"nics.tx_bytes_per_sec", n.TXBytesPerSec},
			{"nics.rx_packets_per_sec", n.RXPacketsPerSec},
			{"nics.tx_packets_per_sec", n.TXPacketsPerSec},
			{"nics.rx_errors_per_sec", n.RXErrorsPerSec},
			{"nics.tx_errors_per_sec", n.TXErrorsPerSec},
			{"nics.rx_dropped_per_sec", n.RXDroppedPerSec},
		} {
			if err := checkNonNeg(c.field, c.v); err != nil {
				return err
			}
		}
	}

	for _, c := range []struct {
		field string
		v     int
	}{
		{"tcp_total", s.TCPTotal},
		{"tcp_established", s.TCPEstablished},
		{"tcp_listen", s.TCPListen},
		{"tcp_time_wait", s.TCPTimeWait},
		{"tcp_close_wait", s.TCPCloseWait},
		{"udp_total", s.UDPTotal},
		{"proc_count", s.ProcCount},
	} {
		if c.v < 0 {
			return decodeErr(StagePayload, c.field, ErrInvalidPayload)
		}
	}

	for _, sm := range s.Sensors {
		if sm.Name == "" {
			return decodeErr(StagePayload, "sensors.name", ErrMissingField)
		}
		// 允许负温度（零下），但 NaN/Inf 非法；nil = 读不到，合法。
		if sm.TemperatureC != nil {
			if err := checkFinite("sensors.temperature_c", *sm.TemperatureC); err != nil {
				return err
			}
		}
	}

	if s.UptimeSec < 0 {
		return decodeErr(StagePayload, "uptime_sec", ErrInvalidPayload)
	}

	if err := checkNonNeg("agent.collect_duration_ms", s.Agent.CollectDurationMs); err != nil {
		return err
	}
	if s.Agent.ReportSuccessCount < 0 {
		return decodeErr(StagePayload, "agent.report_success_count", ErrInvalidPayload)
	}
	if s.Agent.ReportErrorCount < 0 {
		return decodeErr(StagePayload, "agent.report_error_count", ErrInvalidPayload)
	}
	if s.Agent.WSReconnectCount < 0 {
		return decodeErr(StagePayload, "agent.ws_reconnect_count", ErrInvalidPayload)
	}
	if err := checkNonNegPtr("agent.mem_resident_mb", s.Agent.MemResidentMB); err != nil {
		return err
	}
	if err := checkNonNegIntPtr("agent.pending_backlog", s.Agent.PendingBacklog); err != nil {
		return err
	}
	if err := checkNonNegPtr("agent.last_report_latency_ms", s.Agent.LastReportLatencyMs); err != nil {
		return err
	}
	if err := checkNonNegIntPtr("agent.report_drop_count", s.Agent.ReportDropCount); err != nil {
		return err
	}
	if err := checkNonNegIntPtr("agent.agent_uptime_sec", s.Agent.AgentUptimeSec); err != nil {
		return err
	}
	return nil
}

func checkFinite(field string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return decodeErr(StagePayload, field, ErrUnrepresentable)
	}
	return nil
}

func checkPercent(field string, v float64) error {
	if err := checkFinite(field, v); err != nil {
		return err
	}
	if v < 0 || v > 100 {
		return decodeErr(StagePayload, field, ErrInvalidPayload)
	}
	return nil
}

func checkPercentPtr(field string, v *float64) error {
	if v == nil {
		return nil
	}
	return checkPercent(field, *v)
}

func checkNonNeg(field string, v float64) error {
	if err := checkFinite(field, v); err != nil {
		return err
	}
	if v < 0 {
		return decodeErr(StagePayload, field, ErrInvalidPayload)
	}
	return nil
}

func checkNonNegPtr(field string, v *float64) error {
	if v == nil {
		return nil
	}
	return checkNonNeg(field, *v)
}

func checkNonNegIntPtr(field string, v *int64) error {
	if v == nil {
		return nil
	}
	if *v < 0 {
		return decodeErr(StagePayload, field, ErrInvalidPayload)
	}
	return nil
}
