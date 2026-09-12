package request

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// FileQuery 文件列表查询参数。
// SortBy/SortOrder 在 repository 层白名单收敛,杜绝 order by 注入。
type FileQuery struct {
	app.PageRequest
	Keyword   string `form:"keyword"`   // 文件名关键字(模糊匹配 name/original_name)
	Category  string `form:"category"`  // 分类筛选(image/video/audio/document/archive/code/other)
	SortBy    string `form:"sortBy"`    // createdAt | name | size
	SortOrder string `form:"sortOrder"` // asc | desc
}

// RenameFileReq 重命名文件请求(仅更新显示名,不改变物理文件)。
type RenameFileReq struct {
	Name string `json:"name" binding:"required,max=255"`
}

// DeleteFilesReq 批量删除文件请求。
// IDs 用 util.JsonUint64Slice:前端的 snowflake ID 是字符串(超出 JS 安全整数),
// 该类型同时接受 JSON 字符串与数字两种表示。
type DeleteFilesReq struct {
	IDs util.JsonUint64Slice `json:"ids" binding:"required,min=1,max=200"`
}
