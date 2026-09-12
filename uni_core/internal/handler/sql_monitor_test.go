package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// TestToSQLStatsResponse_WireFormatUnchanged 锁定 /monitor/sql/stats 的
// 线上 JSON 形状。
//
// handler 此前直接把 database.StatsSnapshot 交给 app.Success 序列化,
// 改造后改为映射到 response.SQLStatsSnapshot(目的是让 apigen 覆盖该接口)。
// 映射是纯搬运,但**字段名/类型一旦漂移,前端页面会静默拿不到数据**
// (前端按 snake_case 读取,不匹配就是 undefined,不报错)。
// 因此这里用真实的 database 结构体构造输入,断言序列化后的键集合与取值。
func TestToSQLStatsResponse_WireFormatUnchanged(t *testing.T) {
	ts := util.JSONTime(time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC))
	snap := &database.StatsSnapshot{
		Global: database.GlobalSnapshot{
			Count:           10,
			AvgMs:           1.5,
			MaxMs:           9,
			MinMs:           0.1,
			P50Ms:           1,
			P95Ms:           5,
			P99Ms:           8,
			ErrorCount:      2,
			SlowCount:       3,
			SlowThresholdMs: 200,
		},
		ByTable: map[string]database.DimSnapshot{
			"sys_user": {Count: 7, AvgMs: 2, MaxMs: 4, MinMs: 1, P50Ms: 2, P95Ms: 3, P99Ms: 4},
		},
		ByOperation: map[string]database.DimSnapshot{
			"SELECT": {Count: 6, AvgMs: 1, MaxMs: 3, MinMs: 0.5, P50Ms: 1, P95Ms: 2, P99Ms: 3},
		},
		SlowQueries: []database.QueryEntry{
			{
				Timestamp:  ts,
				DurationMs: 250.5,
				SQL:        "SELECT * FROM sys_user",
				Table:      "sys_user",
				Operation:  "SELECT",
				IsError:    true,
				IsSlow:     true,
			},
		},
	}

	raw, err := json.Marshal(toSQLStatsResponse(snap))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 顶层键
	for _, k := range []string{"global", "by_table", "by_operation", "slow_queries"} {
		if _, ok := got[k]; !ok {
			t.Errorf("顶层缺少字段 %q; got=%v", k, raw)
		}
	}

	global, _ := got["global"].(map[string]any)
	for _, k := range []string{
		"count", "avg_ms", "max_ms", "min_ms", "p50_ms", "p95_ms", "p99_ms",
		"error_count", "slow_count", "slow_threshold_ms",
	} {
		if _, ok := global[k]; !ok {
			t.Errorf("global 缺少字段 %q; got=%v", k, global)
		}
	}

	q, _ := got["slow_queries"].([]any)
	if len(q) != 1 {
		t.Fatalf("slow_queries 长度 = %d, want 1", len(q))
	}
	entry, _ := q[0].(map[string]any)
	for _, k := range []string{
		"timestamp", "duration_ms", "sql", "table", "operation", "is_error", "is_slow",
	} {
		if _, ok := entry[k]; !ok {
			t.Errorf("slow_queries[0] 缺少字段 %q; got=%v", k, entry)
		}
	}

	// 时间戳格式必须与 util.JSONTime 一致("2006-01-02 15:04:05"),
	// 顺序/格式变化会让前端解析出 Invalid Date。
	if want := "2026-02-03 04:05:06"; entry["timestamp"] != want {
		t.Errorf("timestamp = %v, want %q", entry["timestamp"], want)
	}

	// is_error / is_slow 无 omitempty:即使为 false 也必须出现,
	// 否则前端 `entry.is_error` 得到 undefined 而非 false。
	if v, ok := entry["is_error"]; !ok || v != true {
		t.Errorf("is_error = %v (存在=%v), want true", v, ok)
	}
}

// TestToSQLStatsResponse_NilSafe handler 在统计器未注入时不应 panic。
func TestToSQLStatsResponse_NilSafe(t *testing.T) {
	if got := toSQLStatsResponse(nil); got != nil {
		t.Errorf("nil 输入应返回 nil,got %+v", got)
	}
}

// TestToSQLStatsResponse_EmptyMapsNotNull 空统计应序列化为 {} 而非 null,
// 保持与改造前一致(前端对 by_table 直接做 Object.entries)。
func TestToSQLStatsResponse_EmptyMapsNotNull(t *testing.T) {
	raw, err := json.Marshal(toSQLStatsResponse(&database.StatsSnapshot{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"by_table", "by_operation"} {
		if got[k] == nil {
			t.Errorf("%s 为 null,应为 {}", k)
		}
	}
	if got["slow_queries"] == nil {
		t.Error("slow_queries 为 null,应为 []")
	}
}

// 编译期断言:DTO 类型确实在 response 包中(apigen 只解析该目录)。
var _ = response.SQLStatsSnapshot{}
var _ = response.SQLQueryEntry{}
var _ = response.SQLDimStats{}
var _ = response.SQLGlobalStats{}
