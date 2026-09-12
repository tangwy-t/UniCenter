package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/app"

type ConfigQuery struct {
	app.PageRequest
	ConfigKey  string `form:"configKey"`
	ConfigType string `form:"configType"`
	Status     *int8  `form:"status"`
}

type CreateConfigReq struct {
	Name        string  `json:"name" binding:"required,max=64"`
	ConfigKey   string  `json:"configKey" binding:"required,max=128"`
	ConfigValue string  `json:"configValue" binding:"required"`
	ConfigType  string  `json:"configType" binding:"required,oneof=S N B J"`
	Remark      *string `json:"remark"`
	Status      *int8   `json:"status"`
}

type UpdateConfigReq struct {
	Name        string  `json:"name" binding:"required,max=64"`
	ConfigValue string  `json:"configValue" binding:"required"`
	ConfigType  string  `json:"configType" binding:"required,oneof=S N B J"`
	Remark      *string `json:"remark"`
	Status      *int8   `json:"status"`
}
