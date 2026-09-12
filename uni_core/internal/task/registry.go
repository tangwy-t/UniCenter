package task

import (
	"fmt"
	"sync"
)

// Registry 管理所有已注册的 Task 实现，线程安全。
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

// NewRegistry 创建注册表并注册所有内置 Task。
func NewRegistry(tasks ...Task) *Registry {
	r := &Registry{tasks: make(map[string]Task)}
	for _, t := range tasks {
		r.Register(t)
	}
	return r
}

// Register 注册一个 Task。若 name 已存在则 panic（开发期安全）。
func (r *Registry) Register(t Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, ok := r.tasks[name]; ok {
		panic(fmt.Sprintf("task: duplicate registration: %q", name))
	}
	r.tasks[name] = t
}

// Get 按名称查找 Task。
func (r *Registry) Get(name string) (Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[name]
	return t, ok
}

// List 返回所有已注册任务的 TargetInfo 列表（供 GET /jobs/targets）。
func (r *Registry) List() []TargetInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]TargetInfo, 0, len(r.tasks))
	for _, t := range r.tasks {
		info := TargetInfo{
			Target:      t.Name(),
			DisplayName: t.DisplayName(),
		}
		if _, ok := t.(ParamValidator); ok {
			info.HasParams = true
		}
		// 目标"带参数"不能只看 ParamValidator：只提供 paramSchema 而不实现
		// 参数校验的任务（如 http-call）同样带参数。此前漏判导致 hasParams=false，
		// 前端据此隐藏整个参数区，paramSchema 驱动的动态表单无法渲染。
		if ps, ok := t.(ParamSchemaProvider); ok {
			info.HasParams = true
			info.ParamSchema = ps.ParamSchema()
		}
		result = append(result, info)
	}
	return result
}

// Names 返回所有已注册任务的名称列表。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tasks))
	for name := range r.tasks {
		names = append(names, name)
	}
	return names
}
