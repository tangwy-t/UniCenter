package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// UserResp is the response payload for a single user record.
type UserResp struct {
	ID            uint64         `json:"id,string"`
	Username      string         `json:"username"`
	RealName      string         `json:"realName"`
	Nickname      string         `json:"nickname"`
	Gender        string         `json:"gender"`
	Email         string         `json:"email"`
	Phone         string         `json:"phone"`
	Avatar        string         `json:"avatar"`
	DeptID        uint64         `json:"deptId,string"`
	DeptName      string         `json:"deptName"`
	Status        int8           `json:"status"`
	Remark        string         `json:"remark"`
	LastLoginTime *util.JSONTime `json:"lastLoginTime"`
	CreatedAt     util.JSONTime  `json:"createdAt"`
	RoleNames     []string       `json:"roleNames"`
	RoleIDs       []string       `json:"roleIds"`
}
