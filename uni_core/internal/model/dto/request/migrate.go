package request

import "github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"

// MigrateFilesReq is the request body for file migration.
type MigrateFilesReq struct {
	FromStorage        string               `json:"fromStorage" binding:"required"`
	ToStorage          string               `json:"toStorage" binding:"required"`
	IDs                util.JsonUint64Slice `json:"ids"`
	DeleteAfterMigrate bool                 `json:"deleteAfterMigrate"`
}
