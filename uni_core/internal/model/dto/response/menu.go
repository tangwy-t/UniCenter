package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// MenuResp is the response payload for a single menu record, supporting tree structure.
type MenuResp struct {
	ID        uint64        `json:"id,string"`
	ParentID  uint64        `json:"parentId,string"`
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Perms     string        `json:"perms"`
	Path      string        `json:"path"`
	Component string        `json:"component"`
	Icon      string        `json:"icon"`
	Sort      int           `json:"sort"`
	Visible   int8          `json:"visible"`
	Status    int8          `json:"status"`
	Children  []MenuResp    `json:"children,omitempty"`
	CreatedAt util.JSONTime `json:"createdAt"`
}
