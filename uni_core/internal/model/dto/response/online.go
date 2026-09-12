package response

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// OnlineSession 是聚合成一行的"逻辑会话"(同一用户同一设备)。
type OnlineSession struct {
	UserID     uint64        `json:"userId,string"`
	Username   string        `json:"username"`
	RealName   string        `json:"realName"`
	DeptName   string        `json:"deptName"`
	IP         string        `json:"ip"`
	Browser    string        `json:"browser"`
	OS         string        `json:"os"`
	LoginAt    util.JSONTime `json:"loginAt"`
	ExpireAt   util.JSONTime `json:"expireAt"`
	TokenCount int           `json:"tokenCount"`
}
