package response

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeviceMetricsRespJSONShape(t *testing.T) {
	cpu := 23.5
	resp := DeviceMetricsResp{
		RangeSeconds:      86400,
		ResolutionSeconds: 30,
		Source:            "redis",
		AvailableMetrics:  []string{"cpu_used_percent", "load1"},
		Buckets: []DeviceMetricPoint{
			{T: 1_700_000_000, CPUUsedPercent: &cpu},
			{T: 1_700_000_030}, // 该桶全空：所有列省略
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	// 契约：桶时间字段名是 t，且是数字（unix 秒），不是 RFC3339 串
	if !strings.Contains(s, `"t":1700000000`) {
		t.Fatalf("桶时间必须是数字 unix 秒的 t 字段: %s", s)
	}
	// 契约：nil 列必须**省略**而不是 emit null（前端据「键不存在」显示「—」）
	if strings.Contains(s, `"load1":null`) || strings.Contains(s, `"cpu_used_percent":null`) {
		t.Fatalf("nil 列必须省略，不得 emit null: %s", s)
	}
	// 契约：空桶仍然出现在 buckets 里（图中空隙由 t 定位，不靠下标）
	if strings.Count(s, `"t":`) != 2 {
		t.Fatalf("空桶也必须在 buckets 中出现（靠 t 定位，禁止按下标推时间）: %s", s)
	}
	// 契约：available_metrics 必须显式给出（区分「未采集」与「该档位无此列」）
	if !strings.Contains(s, `"available_metrics"`) {
		t.Fatalf("必须返回 available_metrics: %s", s)
	}
}

func TestDeviceRespJSONShapeOmitsSecrets(t *testing.T) {
	// Go 不允许用「提升字段」作复合字面量的键，故显式写出内嵌结构体。
	resp := DeviceResp{DeviceListItem: DeviceListItem{ID: 1001, Hostname: "web-01", Status: 1, Online: true}}
	b, _ := json.Marshal(resp)
	s := string(b)
	for _, forbidden := range []string{"tokenHash", "token_hash", "agentToken"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("设备响应**绝不能**包含 %s: %s", forbidden, s)
		}
	}
}
