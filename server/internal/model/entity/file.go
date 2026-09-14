package entity

import (
	"strings"

	"github.com/tangwy-t/WebManagerFramework/server/internal/pkg/datascope/rule"
)

type SysFile struct {
	BaseEntity
	Name         string  `gorm:"column:name;size:256;not null"       json:"name"`
	OriginalName string  `gorm:"column:original_name;size:256;not null" json:"originalName"`
	Path         string  `gorm:"column:path;size:512;not null"       json:"path"`
	URL          *string `gorm:"column:url;size:512"                 json:"url"`
	Size         *int64  `gorm:"column:size;default:0"               json:"size"`
	MimeType     *string `gorm:"column:mime_type;size:128"           json:"mimeType"`
	Ext          *string `gorm:"column:ext;size:16"                  json:"ext"`
	StorageType  string  `gorm:"column:storage_type;size:32;not null;default:local" json:"storageType"`
	Bucket       string  `gorm:"column:bucket;size:128;not null;default:''"          json:"bucket"`
	StorageKey   string  `gorm:"column:storage_key;size:512;not null;default:''"     json:"storageKey"`
}

func (SysFile) TableName() string { return "sys_file" }

// DataScopeRules implements rule.DataScopeable.
func (SysFile) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "created_by", DimensionType: "dept", Via: &rule.ScopeVia{Table: "sys_user", Select: "id", Where: "dept_id"}},
		{Column: "created_by", DimensionType: "self"},
	}
}

// ── 文件分类(扩展名 → 分类) ───────────────────────────────────────────
// 分类是文件管理 UI 的汇总/筛选衍生维度,不落库。
// 分类清单需与前端 web/src/modules/system-file/file-meta.ts 保持一致;
// 新增分类常量时同步扩展 FileStatsRow 的计数字段(stats CASE 列别名)。

const (
	FileCategoryImage    = "image"
	FileCategoryVideo    = "video"
	FileCategoryAudio    = "audio"
	FileCategoryDocument = "document"
	FileCategoryArchive  = "archive"
	FileCategoryCode     = "code"
	FileCategoryOther    = "other"
)

// FileCategoryExts 分类 → 扩展名(含点、小写)。
var FileCategoryExts = map[string][]string{
	FileCategoryImage:    {".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp", ".ico", ".avif"},
	FileCategoryVideo:    {".mp4", ".avi", ".mov", ".mkv", ".webm", ".flv", ".wmv", ".m4v", ".rmvb"},
	FileCategoryAudio:    {".mp3", ".wav", ".flac", ".aac", ".ogg", ".wma", ".m4a", ".amr"},
	FileCategoryDocument: {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".csv", ".rtf", ".odt"},
	FileCategoryArchive:  {".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz"},
	FileCategoryCode:     {".js", ".ts", ".jsx", ".tsx", ".vue", ".py", ".go", ".java", ".c", ".cpp", ".h", ".html", ".css", ".scss", ".json", ".xml", ".yml", ".yaml", ".sql", ".sh", ".toml"},
}

// FileCategories 按声明顺序返回全部分类常量,供统计输出稳定的有序键。
var FileCategories = []string{
	FileCategoryImage,
	FileCategoryVideo,
	FileCategoryAudio,
	FileCategoryDocument,
	FileCategoryArchive,
	FileCategoryCode,
	FileCategoryOther,
}

// AllKnownExts 返回全部分类扩展名的去重清单。
// 供"其他"分类的反向筛选/统计使用:ext 为 NULL 或不在已知扩展名内的文件归属 other。
func AllKnownExts() []string {
	seen := make(map[string]struct{}, 64)
	var out []string
	for _, exts := range FileCategoryExts {
		for _, ext := range exts {
			if _, ok := seen[ext]; !ok {
				seen[ext] = struct{}{}
				out = append(out, ext)
			}
		}
	}
	return out
}

var fileExtCategoryIndex = func() map[string]string {
	index := make(map[string]string, 64)
	for category, exts := range FileCategoryExts {
		for _, ext := range exts {
			index[ext] = category
		}
	}
	return index
}()

// CategoryOfExt 由扩展名推导文件分类;未知或空扩展名归入 other。
func CategoryOfExt(ext string) string {
	if category, ok := fileExtCategoryIndex[strings.ToLower(ext)]; ok {
		return category
	}
	return FileCategoryOther
}

// FileStatsRow 是文件概览统计聚合行(repository.FileRepo.Stats 的 Scan 目标)。
// 分类计数列别名约定 "<category>_count",新增分类常量需同步扩展字段。
type FileStatsRow struct {
	Total         int64
	TotalSize     int64
	WeekUploads   int64
	ImageCount    int64
	VideoCount    int64
	AudioCount    int64
	DocumentCount int64
	ArchiveCount  int64
	CodeCount     int64
	OtherCount    int64
}

// CategoryCounts 返回分类 → 计数的映射(供响应 DTO 组装)。
func (r FileStatsRow) CategoryCounts() map[string]int64 {
	return map[string]int64{
		FileCategoryImage:    r.ImageCount,
		FileCategoryVideo:    r.VideoCount,
		FileCategoryAudio:    r.AudioCount,
		FileCategoryDocument: r.DocumentCount,
		FileCategoryArchive:  r.ArchiveCount,
		FileCategoryCode:     r.CodeCount,
		FileCategoryOther:    r.OtherCount,
	}
}
