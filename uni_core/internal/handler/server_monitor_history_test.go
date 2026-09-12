package handler

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/serverstats"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// TestServerHistoryAdapterPreservesJSONShape 适配器必须保形:
// serverHistorySnapshotFromStats(serverstats.Snapshot) 的 JSON 与
// 改造前直接序列化 serverstats.Snapshot 逐字节一致(前端契约零变化),
// 且 nil 指标字段两侧同缺省。
func TestServerHistoryAdapterPreservesJSONShape(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	src := &serverstats.Snapshot{
		WindowSeconds: 300,
		StepSeconds:   3,
		Buckets: []serverstats.Bucket{{
			Timestamp:  util.JSONTime(time.Unix(1800000, 0)),
			CPU:        f64(12.34),
			MemSys:     f64(512.5),
			HeapAlloc:  nil,
			SysMem:     f64(3072),
			Goroutines: f64(87),
			GCNum:      nil,
			GCPauseMs:  f64(0.42),
			Disk:       f64(31.25),
			Load1:      nil,
			Uptime:     nil,
		}},
	}

	oldBytes, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal old: %v", err)
	}
	newBytes, err := json.Marshal(serverHistorySnapshotFromStats(src))
	if err != nil {
		t.Fatalf("marshal dto: %v", err)
	}
	if !bytes.Equal(oldBytes, newBytes) {
		t.Fatalf("服务器历史 JSON 漂移:\nold=%s\nnew=%s", oldBytes, newBytes)
	}
}
