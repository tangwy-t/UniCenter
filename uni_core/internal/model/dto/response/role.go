package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// RoleResp is the response payload for a single role record.
type RoleResp struct {
	ID        uint64               `json:"id,string"`
	Name      string               `json:"name"`
	Code      string               `json:"code"`
	DataScope int8                 `json:"dataScope"`
	Sort      int                  `json:"sort"`
	Status    int8                 `json:"status"`
	Remark    string               `json:"remark"`
	MenuIDs   util.JsonUint64Slice `json:"menuIds"`
	DeptIDs   util.JsonUint64Slice `json:"deptIds"`
	CreatedAt util.JSONTime        `json:"createdAt"`
}

// RoleOptionResp 角色下拉选项(白名单接口 /roles/all 的响应):
// 仅含选择器所需字段,不携带 menuIds/deptIds 等授权明细——
// 该接口对任意登录用户开放,授权拓扑不应随之下沉。
type RoleOptionResp struct {
	ID     uint64 `json:"id,string"`
	Name   string `json:"name"`
	Code   string `json:"code"`
	Sort   int    `json:"sort"`
	Status int8   `json:"status"`
}
