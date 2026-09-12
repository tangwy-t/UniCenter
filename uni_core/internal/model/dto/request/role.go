package request

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// RoleQuery holds the query parameters for paginated role listing.
type RoleQuery struct {
	app.PageRequest
	Name   string `form:"name"`
	Code   string `form:"code"`
	Status *int8  `form:"status"`
}

// CreateRoleReq is the request payload for creating a new role.
type CreateRoleReq struct {
	Name      string               `json:"name" binding:"required,min=1,max=64"`
	Code      string               `json:"code" binding:"required,min=1,max=64"`
	DataScope *int8                `json:"dataScope"`
	Sort      *int                 `json:"sort"`
	Status    *int8                `json:"status"`
	Remark    *string              `json:"remark"`
	MenuIDs   util.JsonUint64Slice `json:"menuIds"`
	DeptIDs   util.JsonUint64Slice `json:"deptIds"`
}

// UpdateRoleReq is the request payload for updating an existing role.
type UpdateRoleReq struct {
	ID        uint64               `json:"-"`
	Name      string               `json:"name" binding:"required,min=1,max=64"`
	Code      string               `json:"code" binding:"required,min=1,max=64"`
	DataScope *int8                `json:"dataScope"`
	Sort      *int                 `json:"sort"`
	Status    *int8                `json:"status"`
	Remark    *string              `json:"remark"`
	MenuIDs   util.JsonUint64Slice `json:"menuIds"`
	DeptIDs   util.JsonUint64Slice `json:"deptIds"`
}

// UpdateRoleStatusReq is the request payload for toggling a role's status
// from the list page's inline switch (0=停用 1=启用).
type UpdateRoleStatusReq struct {
	Status *int8 `json:"status" binding:"required,oneof=0 1"`
}

// RoleSortItem 是批量排序中的单个条目:直接指定角色 ID 的目标排序值。
// 与 UpdateRoleReq 不同,这里只覆盖 sort 字段,不触碰菜单/部门关联。
type RoleSortItem struct {
	ID   util.JsonUint64 `json:"id" binding:"required"`
	Sort int             `json:"sort"`
}

// UpdateRoleSortReq 是「保存排序」的批量请求:一次性提交多个角色的排序值,
// 逐条直接覆盖赋值(同菜单管理 UpdateMenuSortReq 语义)。
type UpdateRoleSortReq struct {
	Items []RoleSortItem `json:"items" binding:"required,min=1,dive"`
}
