package response

import "time"

// FileResp 文件条目响应。
type FileResp struct {
	ID           uint64    `json:"id,string"`
	Name         string    `json:"name"`
	OriginalName string    `json:"originalName"`
	Size         int64     `json:"size"`
	MimeType     string    `json:"mimeType"`
	Ext          string    `json:"ext"`
	Category     string    `json:"category"`
	StorageType  string    `json:"storageType"`
	CreatedBy    uint64    `json:"createdBy,string,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// FileStatsResp 文件概览统计响应。
type FileStatsResp struct {
	Total          int64            `json:"total"`
	TotalSize      int64            `json:"totalSize"`
	WeekUploads    int64            `json:"weekUploads"`
	CategoryCounts map[string]int64 `json:"categoryCounts"`
}

// UploadFilesResp 上传响应。
type UploadFilesResp struct {
	Items []FileResp `json:"items"`
}
