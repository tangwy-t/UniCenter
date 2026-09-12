package entity

import "github.com/tangwy-t/webmanager-server/internal/pkg/datascope"

// ScopeEntities 列出所有需要数据权限自动注入的实体。
//
// 此前这份清单硬编码在 cmd/server/main.go:新增实现了 DataScopeRules 的
// 实体时极易忘记同步 main 里的 RegisterEntity 调用 —— 漏注册的实体查询
// 不会报错,只是静默地失去 scope 过滤(IDOR 缺口)。清单收进 entity 包后,
// 注册随实体定义走,main 只负责装配。
//
// 键取自各实体的 TableName(),不再手写字符串,消除表名的二次硬编码。
// 新增实体:实现 DataScopeRules 并加入本切片,两步都在 entity 包内完成。
var ScopeEntities = []datascope.DataScopeable{
	SysUser{},
	SysDept{},
	SysFile{},
	SysNoticeUser{},
	SysOperationLog{},
	SysLoginLog{},
	SysMenu{},
}
