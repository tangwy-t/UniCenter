package request

import (
	"encoding/json"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/pkg/app"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// UserQuery holds the query parameters for paginated user listing.
type UserQuery struct {
	app.PageRequest
	Username       string  `form:"username"`
	RealName       string  `form:"realName"`
	Phone          string  `form:"phone"`
	Email          string  `form:"email"`
	Status         *int8   `form:"status"`
	DeptID         *uint64 `form:"deptId"`
	RoleID         *uint64 `form:"roleId"`         // 角色ID：过滤已分配该角色的用户（角色管理-分配用户）
	CreatedAtStart string  `form:"createdAtStart"` // 创建时间起（YYYY-MM-DD，含当天 00:00:00）
	CreatedAtEnd   string  `form:"createdAtEnd"`   // 创建时间止（YYYY-MM-DD，含当天 23:59:59）
}

// CreateUserReq is the request payload for creating a new user.
// DeptID uses util.JsonUint64 to accept both string and number JSON values,
// preserving snowflake-ID precision (strings round-trip losslessly; numbers
// beyond 2^53-1 are already lossy on the JS side before they reach us).
type CreateUserReq struct {
	Username string               `json:"username" binding:"required,min=3,max=64"`
	Password string               `json:"password" binding:"required,min=6,max=64"`
	RealName *string              `json:"realName"`
	Email    *string              `json:"email"`
	Phone    *string              `json:"phone"`
	DeptID   *util.JsonUint64     `json:"deptId"`
	RoleIDs  util.JsonUint64Slice `json:"roleIds"`
	Status   *int8                `json:"status"`
	Remark   *string              `json:"remark"`
	Nickname *string              `json:"nickname"`
	Gender   *string              `json:"gender"`
}

// UpdateUserReq is the request payload for updating an existing user's basic info.
type UpdateUserReq struct {
	ID       uint64           `json:"-"`
	RealName *string          `json:"realName"`
	Email    *string          `json:"email" binding:"omitempty,email"`
	Phone    *string          `json:"phone" binding:"omitempty,min=7,max=20"`
	DeptID   *util.JsonUint64 `json:"deptId"`
	Remark   *string          `json:"remark"`
	Nickname *string          `json:"nickname"`
	Gender   *string          `json:"gender"`
}

// UnmarshalJSON runs before gin's binding validation: pointers to empty
// strings are normalized to nil. validator's omitempty only skips nil
// pointers — a JSON `"email": ""` deserializes to a non-nil pointer to ""
// and would otherwise fail the email (or min=7) tag with a generic 400.
func (u *UpdateUserReq) UnmarshalJSON(data []byte) error {
	type alias UpdateUserReq
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	trimToNil := func(s *string) *string {
		if s != nil && strings.TrimSpace(*s) == "" {
			return nil
		}
		return s
	}
	a.Email = trimToNil(a.Email)
	a.Phone = trimToNil(a.Phone)
	*u = UpdateUserReq(a)
	return nil
}

// AssignRolesReq is the request payload for assigning (replacing) a user's roles.
type AssignRolesReq struct {
	RoleIDs util.JsonUint64Slice `json:"roleIds"`
}

// RoleUsersReq is the request payload for adding/removing users of a role
// (角色管理-分配用户, mounted under /roles/:id/users).
type RoleUsersReq struct {
	UserIDs util.JsonUint64Slice `json:"userIds" binding:"required,min=1"`
}

// ResetPasswordReq is the request payload for resetting a user's password.
type ResetPasswordReq struct {
	NewPassword string `json:"newPassword" binding:"required,min=6,max=64"`
}
