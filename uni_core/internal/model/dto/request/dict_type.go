package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/app"

// DictTypeQuery holds pagination and filter parameters for dictionary type list queries.
type DictTypeQuery struct {
	app.PageRequest
	Name   string `form:"name"`
	Code   string `form:"code"`
	Status *int8  `form:"status"`
}

// CreateDictTypeReq is the request body for creating a dictionary type.
type CreateDictTypeReq struct {
	Code   string  `json:"code" binding:"required"`
	Name   string  `json:"name" binding:"required"`
	Status *int8   `json:"status"`
	Remark *string `json:"remark"`
}

// UpdateDictTypeReq is the request body for updating a dictionary type (all fields required).
type UpdateDictTypeReq struct {
	Name   string  `json:"name" binding:"required"`
	Code   string  `json:"code" binding:"required"`
	Status *int8   `json:"status"`
	Remark *string `json:"remark"`
}
