package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// DeptResp is the response payload for a single department record, supporting tree structure.
type DeptResp struct {
	ID        uint64        `json:"id,string"`
	ParentID  uint64        `json:"parentId,string"`
	Ancestors string        `json:"ancestors"`
	Name      string        `json:"name"`
	Sort      int           `json:"sort"`
	Leader    string        `json:"leader"`
	Phone     string        `json:"phone"`
	Email     string        `json:"email"`
	Status    int8          `json:"status"`
	Children  []DeptResp    `json:"children,omitempty"`
	CreatedAt util.JSONTime `json:"createdAt"`
}
