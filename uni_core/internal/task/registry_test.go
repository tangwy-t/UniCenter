package task

import (
	"context"
	"encoding/json"
	"testing"
)

// 各类任务桩：仅用于验证 List() 的 hasParams/paramSchema 判定逻辑。

type bareTask struct{}

func (bareTask) Name() string                                       { return "bare" }
func (bareTask) DisplayName() string                                { return "无参数任务" }
func (bareTask) Execute(_ context.Context, _ json.RawMessage) error { return nil }

// schemaTask 只提供 paramSchema、不实现参数校验（对应 http-call 现状）。
type schemaTask struct{ bareTask }

func (schemaTask) Name() string        { return "schema" }
func (schemaTask) DisplayName() string { return "带 Schema 任务" }

func (schemaTask) ParamSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string"},
		},
		"required": []string{"url"},
	}
}

// validatorTask 只实现参数校验、不提供 schema。
type validatorTask struct{ bareTask }

func (validatorTask) Name() string        { return "validator" }
func (validatorTask) DisplayName() string { return "带校验任务" }

func (validatorTask) ValidateParams(_ json.RawMessage) error { return nil }

func infoByName(infos []TargetInfo, name string) (TargetInfo, bool) {
	for _, i := range infos {
		if i.Target == name {
			return i, true
		}
	}
	return TargetInfo{}, false
}

func TestRegistryListHasParamsForSchemaProvider(t *testing.T) {
	r := NewRegistry(schemaTask{})

	info, ok := infoByName(r.List(), "schema")
	if !ok {
		t.Fatal("schema 任务未出现在 List 结果中")
	}
	if !info.HasParams {
		t.Fatal("仅提供 ParamSchema 的任务应被标记 hasParams=true（否则前端不渲染动态参数表单）")
	}
	if info.ParamSchema == nil {
		t.Fatal("ParamSchema 不应为空")
	}
}

func TestRegistryListHasParamsForValidator(t *testing.T) {
	r := NewRegistry(validatorTask{})

	info, ok := infoByName(r.List(), "validator")
	if !ok {
		t.Fatal("validator 任务未出现在 List 结果中")
	}
	if !info.HasParams {
		t.Fatal("实现 ParamValidator 的任务应被标记 hasParams=true")
	}
	if info.ParamSchema != nil {
		t.Fatal("无 schema 任务不应返回 ParamSchema")
	}
}

func TestRegistryListBareTaskHasNoParams(t *testing.T) {
	r := NewRegistry(bareTask{})

	info, ok := infoByName(r.List(), "bare")
	if !ok {
		t.Fatal("bare 任务未出现在 List 结果中")
	}
	if info.HasParams {
		t.Fatal("无参数任务不应被标记 hasParams=true")
	}
	if info.ParamSchema != nil {
		t.Fatal("无参数任务不应返回 ParamSchema")
	}
}
