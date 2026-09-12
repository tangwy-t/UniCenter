package tasks

import (
	"context"
	"encoding/json"
	"fmt"
)

// DemoTask 是用于验证调度管道正常工作的演示任务。
type DemoTask struct{}

// NewDemoTask 创建 Demo 任务实例。
func NewDemoTask() *DemoTask {
	return &DemoTask{}
}

func (t *DemoTask) Name() string        { return "demo" }
func (t *DemoTask) DisplayName() string { return "演示任务" }

func (t *DemoTask) Execute(_ context.Context, params json.RawMessage) error {
	msg := "demo task executed"
	if len(params) > 0 {
		msg = fmt.Sprintf("demo task executed with params: %s", string(params))
	}
	// 仅输出到 stdout，实际生产环境使用 logger
	fmt.Println(msg)
	return nil
}
