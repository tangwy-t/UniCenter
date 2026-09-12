package response

import (
	"encoding/json"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// 小写 list_class：DictItem 走缓存契约，snake_case 与 is_default 一致；
// 大写 ListClass：管理接口 DictDataResp 走 camelCase。
func TestDictItemListClassSerialization(t *testing.T) {
	item := DictItem{Label: "启用", Value: "1", ListClass: "success", Sort: 1}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	want := `{"label":"启用","value":"1","list_class":"success","is_default":false,"sort":1}`
	if string(data) != want {
		t.Fatalf("Marshal =\n  %s\nwant\n  %s", data, want)
	}
}

func TestDictDataRespListClassSerialization(t *testing.T) {
	at := util.JSONTime{}
	resp := DictDataResp{ID: 1, TypeID: 2, Label: "启用", Value: "1", ListClass: "success", CreatedAt: at, UpdatedAt: at}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	// 仅验证含 listClass 且 ID 保持字符串（时间等既有字段沿用现有测试断言）。
	if !json.Valid(data) {
		t.Fatal("not valid JSON")
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if m["listClass"] != "success" {
		t.Fatalf("listClass = %v, want success", m["listClass"])
	}
	if m["id"] != "1" { // ID 必须为字符串（雪花精度）
		t.Fatalf("id = %v (%T), want string \"1\"", m["id"], m["id"])
	}
}
