package request

// CreateDictDataReq is the request body for creating a dictionary data entry.
type CreateDictDataReq struct {
	Label     string  `json:"label" binding:"required"`
	Value     string  `json:"value" binding:"required"`
	ListClass *string `json:"listClass" binding:"omitempty,oneof=primary success info warning danger"`
	IsDefault *int8   `json:"isDefault"`
	Sort      *int    `json:"sort"`
	Status    *int8   `json:"status"`
	Remark    *string `json:"remark"`
}

// UpdateDictDataReq is the request body for updating a dictionary data entry (all required).
type UpdateDictDataReq struct {
	Label     string  `json:"label" binding:"required"`
	Value     string  `json:"value" binding:"required"`
	ListClass *string `json:"listClass" binding:"omitempty,oneof=primary success info warning danger"`
	IsDefault *int8   `json:"isDefault"`
	Sort      *int    `json:"sort"`
	Status    *int8   `json:"status"`
	Remark    *string `json:"remark"`
}
