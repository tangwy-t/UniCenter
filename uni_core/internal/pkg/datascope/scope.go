package datascope

// Data scope constants
const (
	ScopeAll          int8 = 1 // 全部数据权限
	ScopeCustom       int8 = 2 // 自定义部门权限
	ScopeDept         int8 = 3 // 本部门权限
	ScopeDeptAndBelow int8 = 4 // 本部门及以下权限
	ScopeSelf         int8 = 5 // 仅本人权限
)

// Dimension type constants
const (
	DimRole = "role"
)
