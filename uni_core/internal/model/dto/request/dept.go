package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// CreateDeptReq is the request payload for creating a new department.
// ParentID uses util.JsonUint64 to accept both string and number JSON values,
// preserving snowflake-ID precision (strings round-trip losslessly; numbers
// beyond 2^53-1 are already lossy on the JS side before they reach us).
type CreateDeptReq struct {
	ParentID *util.JsonUint64 `json:"parentId"`
	Name     string           `json:"name" binding:"required,min=1,max=64"`
	Sort     *int             `json:"sort"`
	Leader   *string          `json:"leader"`
	Phone    *string          `json:"phone"`
	Email    *string          `json:"email"`
	Status   *int8            `json:"status"`
}

// UpdateDeptReq is the request payload for updating an existing department.
type UpdateDeptReq struct {
	ID       uint64           `json:"-"`
	ParentID *util.JsonUint64 `json:"parentId"`
	Name     string           `json:"name" binding:"required,min=1,max=64"`
	Sort     *int             `json:"sort"`
	Leader   *string          `json:"leader"`
	Phone    *string          `json:"phone"`
	Email    *string          `json:"email"`
	Status   *int8            `json:"status"`
}

// DeptQuery holds the query parameters for department tree listing.
type DeptQuery struct {
	Name   string `form:"name"`
	Status *int8  `form:"status"`
}

// DeptSortItem 是批量排序中的单个条目：直接指定部门 ID 的目标排序值。
type DeptSortItem struct {
	ID   util.JsonUint64 `json:"id" binding:"required"`
	Sort int             `json:"sort"`
}

// UpdateDeptSortReq 是「保存排序」的批量请求：一次性提交多个部门的排序值。
// 与 UpdateDeptReq 的「精确落位(shift)」语义不同，这里为直接覆盖赋值，
// 避免逐条 shift 导致用户排好的最终顺序被反复搬移（语义对齐菜单/角色的保存排序）。
type UpdateDeptSortReq struct {
	Items []DeptSortItem `json:"items" binding:"required,min=1,dive"`
}
