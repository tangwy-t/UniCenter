package task

import (
	"context"
	"encoding/json"
)

// Task 定义可调度执行的定时任务接口。
type Task interface {
	// Name 返回任务唯一标识（如 "http-call"），用于注册表和 invoke_target 匹配。
	Name() string
	// DisplayName 返回任务展示名称（如 "HTTP 回调"），用于前端展示。
	DisplayName() string
	// Execute 执行任务，params 为 invoke_params JSON 原始数据。
	Execute(ctx context.Context, params json.RawMessage) error
}

// ParamValidator 可选接口：支持参数校验的 Task 实现此接口。
// Registry.List 据此将目标标记为带参数(hasParams)；创建/更新任务时可
// type assertion 取得该接口做服务端参数校验。
type ParamValidator interface {
	ValidateParams(params json.RawMessage) error
}

// ParamSchemaProvider 可选接口：提供参数 schema（用于前端动态表单）。
type ParamSchemaProvider interface {
	ParamSchema() map[string]any
}

// TargetInfo 是 GET /jobs/targets 返回的可用任务目标信息。
type TargetInfo struct {
	Target      string         `json:"target"`
	DisplayName string         `json:"displayName"`
	HasParams   bool           `json:"hasParams"`
	ParamSchema map[string]any `json:"paramSchema,omitempty"`
}
