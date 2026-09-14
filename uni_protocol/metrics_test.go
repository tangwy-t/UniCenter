package agentproto

import (
	"errors"
	"math"
	"testing"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func baseSample() *MetricsSample {
	return &MetricsSample{
		T:              1_700_000_000_000,
		CPUUsedPercent: 23.5,
		Load1:          0.8,
		MemUsedPercent: 61.2,
		MemUsedMB:      9800,
		MemAvailableMB: 6200,
		TCPTotal:       120,
		ProcCount:      310,
		UptimeSec:      86_400,
		Agent: AgentMetric{
			CollectDurationMs:  12.5,
			ReportSuccessCount: 900,
			ReportErrorCount:   0,
			WSReconnectCount:   1,
		},
	}
}

func TestMetricsSampleValid(t *testing.T) {
	if err := baseSample().Validate(); err != nil {
		t.Fatalf("合法样本不应失败: %v", err)
	}
	// 指针字段为 nil = 未采集，必须合法
	if err := baseSample().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsSampleMissingT(t *testing.T) {
	s := baseSample()
	s.T = 0
	if err := s.Validate(); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("T 缺失应报 ErrInvalidTimestamp，实际 %v", err)
	}
}

func TestMetricsSampleRejectsOutOfRangePercent(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*MetricsSample)
	}{
		{"cpu_used_percent > 100", func(s *MetricsSample) { s.CPUUsedPercent = 100.1 }},
		{"cpu_used_percent < 0", func(s *MetricsSample) { s.CPUUsedPercent = -0.1 }},
		{"mem_used_percent > 100", func(s *MetricsSample) { s.MemUsedPercent = 101 }},
		{"cpu_per_core 越界", func(s *MetricsSample) { s.CPUPerCore = []float64{10, 200} }},
		{"cpu_iowait 越界（指针）", func(s *MetricsSample) { s.CPUIOWait = f64(150) }},
		{"swap_used_percent 越界（指针）", func(s *MetricsSample) { s.SwapUsedPercent = f64(-1) }},
		{"disk used_percent 越界", func(s *MetricsSample) {
			s.Disks = []DiskMetric{{Mountpoint: "/", UsedPercent: 120}}
		}},
		{"disk used_gb > total_gb", func(s *MetricsSample) {
			s.Disks = []DiskMetric{{Mountpoint: "/", UsedGB: 600, TotalGB: 500}}
		}},
		{"nic 速率为负", func(s *MetricsSample) {
			s.NICs = []NICMetric{{Name: "eth0", RXBytesPerSec: -1}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := baseSample()
			c.mut(s)
			if err := s.Validate(); err == nil {
				t.Fatal("期望失败但通过了")
			}
		})
	}
}

func TestMetricsSampleRejectsNonFinite(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		s := baseSample()
		s.CPUUsedPercent = bad
		if err := s.Validate(); !errors.Is(err, ErrUnrepresentable) {
			t.Fatalf("值 %v 应报 ErrUnrepresentable，实际 %v", bad, err)
		}
	}
}

func TestMetricsSampleRequiresNames(t *testing.T) {
	s := baseSample()
	s.Disks = []DiskMetric{{Mountpoint: "", UsedGB: 1, TotalGB: 2}}
	if err := s.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("disk 缺 mountpoint 应报 ErrMissingField，实际 %v", err)
	}
	s = baseSample()
	s.NICs = []NICMetric{{Name: "", RXBytesPerSec: 1}}
	if err := s.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("nic 缺 name 应报 ErrMissingField，实际 %v", err)
	}
	s = baseSample()
	s.Sensors = []SensorMetric{{Name: "", TemperatureC: f64(40)}}
	if err := s.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("sensor 缺 name 应报 ErrMissingField，实际 %v", err)
	}
}

func TestSensorTemperatureMayBeNegativeButNilMeansUnavailable(t *testing.T) {
	s := baseSample()
	s.Sensors = []SensorMetric{{Name: "tctl", TemperatureC: f64(-5)}}
	if err := s.Validate(); err != nil {
		t.Fatalf("负温度应合法: %v", err)
	}
	s = baseSample()
	s.Sensors = []SensorMetric{{Name: "tctl", TemperatureC: nil}}
	if err := s.Validate(); err != nil {
		t.Fatalf("nil 温度（读不到）应合法: %v", err)
	}
}

func TestMetricsSampleRejectsNegativeCounters(t *testing.T) {
	s := baseSample()
	s.TCPTotal = -1
	if err := s.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("负计数应报 ErrInvalidPayload，实际 %v", err)
	}
	s = baseSample()
	s.Agent.PendingBacklog = i64(-1)
	if err := s.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("负 backlog 应报 ErrInvalidPayload，实际 %v", err)
	}
}
