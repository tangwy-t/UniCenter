package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

type ConfigResp struct {
	ID          uint64        `json:"id,string"`
	Name        string        `json:"name"`
	ConfigKey   string        `json:"configKey"`
	ConfigValue string        `json:"configValue"`
	ConfigType  string        `json:"configType"`
	Remark      string        `json:"remark"`
	Status      int8          `json:"status"`
	CreatedAt   util.JSONTime `json:"createdAt"`
	UpdatedAt   util.JSONTime `json:"updatedAt"`
}
