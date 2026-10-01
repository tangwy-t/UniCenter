package agentproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── stats 流样本（监控面，container:stats）───────────────────────────────

// 样本 JSON 的线上升级形状必须与快照 DockerContainer 的同名字段一致（名字、单位、
// 舍入口径）：曲线与表格读同一套数据约定，任何一个 tag 写错都会让前端把「曲线」
// 与「读卡」两个数据源悄悄对不上。这里把全部 tag 逐字钉住。
func TestDockerStatsSampleJSONShape(t *testing.T) {
	s := DockerStatsSample{
		T: 1790000000000, CPUPercent: 12.34, MemUsageMB: 512.5, MemLimitMB: 1024,
		NetRXBytesSec: 3000, NetTXBytesSec: 1500,
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, key := range []string{
		`"t":1790000000000`,
		`"cpu_percent":12.34`,
		`"mem_usage_mb":512.5`,
		`"mem_limit_mb":1024`,
		`"net_rx_bytes_sec":3000`,
		`"net_tx_bytes_sec":1500`,
	} {
		if !strings.Contains(got, key) {
			t.Fatalf("样本 JSON 必须含 %s，实际 %s", key, got)
		}
	}
	var back DockerStatsSample
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back != s {
		t.Fatalf("样本 JSON 回环不一致: %+v vs %+v", back, s)
	}
}

// 样本校验：t 必须为正；任何读数为负都拒（负数是采集侧算错了，不是可展示的值）。
func TestDockerStatsSampleValidate(t *testing.T) {
	ok := DockerStatsSample{T: 1790000000000, CPUPercent: 1, MemUsageMB: 2, MemLimitMB: 3,
		NetRXBytesSec: 4, NetTXBytesSec: 5}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法样本必须通过: %v", err)
	}
	zero := DockerStatsSample{T: 0}
	if err := zero.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("t=0 必须报非法: %v", err)
	}
	for _, bad := range []DockerStatsSample{
		{CPUPercent: -0.1}, {MemUsageMB: -1}, {MemLimitMB: -1},
		{NetRXBytesSec: -1}, {NetTXBytesSec: -1},
	} {
		bad.T = 1
		if err := bad.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("负读数必须报非法: %+v → %v", bad, err)
		}
	}
}

// 样本放到**帧**里走还是完整链路（帧模型复用，不另起消息类型）：编码进 Data 的
// JSON 行必须能被解码回样本并通过校验 —— core 的 stats 端点按这个形状逐行解析。
func TestDockerStatsSampleRidesFrame(t *testing.T) {
	line, err := json.Marshal(DockerStatsSample{T: 1790000000000, CPUPercent: 7.5})
	if err != nil {
		t.Fatal(err)
	}
	f := &DockerFrame{SessionID: "sess-0123456789abcdef", Seq: 1, Data: append(line, '\n')}
	if err := f.Validate(); err != nil {
		t.Fatalf("帧必须合法: %v", err)
	}
	var s DockerStatsSample
	if err := json.Unmarshal(line, &s); err != nil || s.Validate() != nil {
		t.Fatalf("帧里样本行必须可解码: %v %v", err, s.Validate())
	}
}

// container:stats 的 options 契约：只有 target（容器名），target 缺了/非法都拒。
func TestDockerStatsActionOptions(t *testing.T) {
	if !IsDockerAction(DockerActionContainerStats) {
		t.Fatal("container:stats 必须在白名单")
	}
	if err := ValidateDockerCmdOptions(DockerActionContainerStats,
		&DockerCmdOptions{Target: "mysql"}); err != nil {
		t.Fatalf("合法 target 必须通过: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionContainerStats,
		&DockerCmdOptions{}); !errors.Is(err, ErrMissingField) {
		t.Fatalf("缺 target 必须报缺字段: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionContainerStats,
		&DockerCmdOptions{Target: "a/b"}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("带路径分隔符的 target 必须报非法: %v", err)
	}
}
