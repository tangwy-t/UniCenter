package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// DictTypeResp is the response DTO for a dictionary type.
type DictTypeResp struct {
	ID        uint64        `json:"id,string"`
	Code      string        `json:"code"`
	Name      string        `json:"name"`
	Status    int8          `json:"status"`
	Remark    string        `json:"remark"`
	CreatedAt util.JSONTime `json:"createdAt"`
	UpdatedAt util.JSONTime `json:"updatedAt"`
}
