package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// MenuQuery holds the query parameters for menu listing.
type MenuQuery struct {
	Name   string `form:"name"`
	Status *int8  `form:"status"`
	Type   string `form:"type"`
}

// CreateMenuReq is the request payload for creating a new menu.
type CreateMenuReq struct {
	ParentID  *util.JsonUint64 `json:"parentId"`
	Name      string           `json:"name" binding:"required,min=1,max=64"`
	Type      string           `json:"type" binding:"required,oneof=dir menu btn"`
	Perms     *string          `json:"perms"`
	Path      *string          `json:"path"`
	Component *string          `json:"component"`
	Icon      *string          `json:"icon"`
	Sort      *int             `json:"sort"`
	Visible   *int8            `json:"visible"`
	Status    *int8            `json:"status"`
}

// UpdateMenuReq is the request payload for updating an existing menu.
type UpdateMenuReq struct {
	ID        uint64           `json:"-"`
	ParentID  *util.JsonUint64 `json:"parentId"`
	Name      string           `json:"name" binding:"required,min=1,max=64"`
	Type      string           `json:"type" binding:"required,oneof=dir menu btn"`
	Perms     *string          `json:"perms"`
	Path      *string          `json:"path"`
	Component *string          `json:"component"`
	Icon      *string          `json:"icon"`
	Sort      *int             `json:"sort"`
	Visible   *int8            `json:"visible"`
	Status    *int8            `json:"status"`
}

// MenuSortItem 是批量排序中的单个条目：直接指定菜单 ID 的目标排序值。
type MenuSortItem struct {
	ID   util.JsonUint64 `json:"id" binding:"required"`
	Sort int             `json:"sort"`
}

// UpdateMenuSortReq 是「保存排序」的批量请求：一次性提交多个菜单的排序值。
// 与 UpdateMenuReq 的「精确落位(shift)」语义不同，这里为直接覆盖赋值，
// 避免逐条 shift 导致用户排好的最终顺序被反复搬移。
type UpdateMenuSortReq struct {
	Items []MenuSortItem `json:"items" binding:"required,min=1,dive"`
}
