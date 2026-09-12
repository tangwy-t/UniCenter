package service

import (
	"encoding/json"
	"testing"
)

// TestConfigChangedMsgJSON 载荷类型必须保持旧地图序列化形状:
// 发布侧与订阅侧(scheduler/config 热更新)共享同一 JSON 字段名。
func TestConfigChangedMsgJSON(t *testing.T) {
	// 旧发布形:map[string]string{"key": key}
	oldB, err := json.Marshal(map[string]string{"key": "sys.scheduler.enabled"})
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	newB, err := json.Marshal(ConfigChangedMsg{Key: "sys.scheduler.enabled"})
	if err != nil {
		t.Fatalf("marshal ConfigChangedMsg: %v", err)
	}
	if string(oldB) != string(newB) {
		t.Fatalf("JSON = %s, want %s", newB, oldB)
	}

	var back ConfigChangedMsg
	if err := json.Unmarshal(oldB, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Key != "sys.scheduler.enabled" {
		t.Fatalf("Key = %q, want sys.scheduler.enabled", back.Key)
	}
}
