package agentproto

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSnapshotCoversAllPayloadDTOs(t *testing.T) {
	snap := Snapshot()
	for _, name := range []string{"Hello", "HelloAck", "MetricsSample", "DiskMetric", "DiskIOMetric", "NICMetric", "SensorMetric", "AgentMetric"} {
		if _, ok := snap[name]; !ok {
			t.Fatalf("形状快照缺少 DTO %q", name)
		}
	}
	if n := len(snap["Hello"]); n == 0 {
		t.Fatal("Hello 的字段形状为空")
	}
}

func TestSnapshotCapturesPointerAndOmitEmpty(t *testing.T) {
	var byName = map[string]FieldShape{}
	for _, f := range Snapshot()["MetricsSample"] {
		byName[f.JSON] = f
	}
	cpu, ok := byName["cpu_used_percent"]
	if !ok {
		t.Fatal("缺少 cpu_used_percent")
	}
	if cpu.Pointer || cpu.OmitEmpty {
		t.Fatalf("cpu_used_percent 不得是指针也不得 omitempty（0 是真实值）: %+v", cpu)
	}
	iowait, ok := byName["cpu_iowait"]
	if !ok {
		t.Fatal("缺少 cpu_iowait")
	}
	if !iowait.Pointer || !iowait.OmitEmpty {
		t.Fatalf("cpu_iowait 必须是指针 + omitempty（缺 ≠ 0）: %+v", iowait)
	}
}

func TestSnapshotJSONIsStable(t *testing.T) {
	a, err := SnapshotJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := SnapshotJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("SnapshotJSON 输出不稳定（同一进程内两次调用不一致）")
	}
}

// 字段级约束：json tag 非空、无重复、snake_case；指针字段必须 omitempty（「缺 ≠ 0」要能省略）。
func TestJSONTagConventions(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)
	for dto, fields := range Snapshot() {
		seen := map[string]bool{}
		for _, f := range fields {
			if f.JSON == "" {
				t.Errorf("%s.%s 缺少 json tag", dto, f.Name)
				continue
			}
			if !snake.MatchString(f.JSON) {
				t.Errorf("%s.%s 的 json tag 不是 snake_case: %q", dto, f.Name, f.JSON)
			}
			if seen[f.JSON] {
				t.Errorf("%s 出现重复 json tag %q", dto, f.JSON)
			}
			seen[f.JSON] = true

			if f.Pointer && !f.OmitEmpty {
				t.Errorf("%s.%s 是指针却没加 omitempty（未采集的 nil 必须能省略）", dto, f.Name)
			}
		}
	}
}

// 未知字段必须被容忍（前向兼容）：信封与载荷里出现新版本新增的可选字段时，
// 解码必须成功且已知字段保持完好——这是「新增可选字段不破坏旧端」的机制性证据。
func TestUnknownFieldsAreTolerated(t *testing.T) {
	raw := []byte(`{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":{"future_field":123},"future_top":"x"}`)
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("信封出现未知字段时不应失败: %v", err)
	}
	if m.Type != TypeAgentHeartbeat {
		t.Fatal("已知字段被未知字段污染")
	}
	var hb Heartbeat
	if err := m.DecodeData(&hb); err != nil {
		t.Fatalf("载荷出现未知字段时不应失败: %v", err)
	}

	raw2 := []byte(`{"v":1,"id":"2","type":"agent.report.metrics","ts":1,"data":{` +
		`"t":1,"cpu_used_percent":50,"future_metric":9.9,` +
		`"load1":0,"load5":0,"load15":0,"mem_used_percent":0,"mem_used_mb":0,"mem_available_mb":0,` +
		`"tcp_total":0,"tcp_established":0,"tcp_listen":0,"udp_total":0,"proc_count":0,"uptime_sec":0,` +
		`"agent":{"collect_duration_ms":0,"report_success_count":0,"report_error_count":0,"ws_reconnect_count":0}}}`)
	m2, err := Decode(raw2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTyped(m2)
	if err != nil {
		t.Fatalf("带未知指标的载荷不应失败: %v", err)
	}
	s, ok := got.(*MetricsSample)
	if !ok {
		t.Fatalf("类型不符: %T", got)
	}
	if s.CPUUsedPercent != 50 {
		t.Fatalf("已知字段必须完好: %+v", s)
	}
}

// golden 文件锁死每个消息的规范线上形态。
func TestGoldenFilesRoundTrip(t *testing.T) {
	dir := filepath.Join("testdata", "golden", "v1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("golden 目录不可读（必须先创建）: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("golden 目录为空")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			m, err := Decode(raw)
			if err != nil {
				t.Fatalf("golden 文件必须能被当前解码器接受（向后兼容）: %v", err)
			}
			// 形状稳定：重新编码后再解码，字段级一致
			out, err := m.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			m2, err := Decode(out)
			if err != nil {
				t.Fatal(err)
			}
			if m.V != m2.V || m.ID != m2.ID || m.Type != m2.Type || m.TS != m2.TS {
				t.Fatalf("信封字段在回环中变化: %+v vs %+v", m, m2)
			}
		})
	}
}
