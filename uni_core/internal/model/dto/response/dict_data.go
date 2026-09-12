package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// DictDataResp is the response DTO for a dictionary data entry.
type DictDataResp struct {
	ID        uint64        `json:"id,string"`
	TypeID    uint64        `json:"typeId,string"`
	ListClass string        `json:"listClass"`
	Label     string        `json:"label"`
	Value     string        `json:"value"`
	IsDefault int8          `json:"isDefault"`
	Sort      int           `json:"sort"`
	Status    int8          `json:"status"`
	Remark    string        `json:"remark"`
	CreatedAt util.JSONTime `json:"createdAt"`
	UpdatedAt util.JSONTime `json:"updatedAt"`
}
