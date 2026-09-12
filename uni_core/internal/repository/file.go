package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

// FileRepo 是文件元数据仓库。SysFile 已注册进数据权限实体清单
// (entity.ScopeEntities),查询/更新/删除自动注入 scope 条件。
type FileRepo struct {
	db *gorm.DB
}

// NewFileRepository returns a FileRepo backed by the given *gorm.DB.
func NewFileRepository(db *gorm.DB) *FileRepo {
	return &FileRepo{db: db}
}

// fileSortClause 将排序参数收敛为白名单 SQL 片段(防 order by 注入)。
func fileSortClause(sortBy, sortOrder string) string {
	column := "created_at"
	switch sortBy {
	case "name":
		column = "name"
	case "size":
		column = "size"
	}
	direction := " DESC"
	if sortOrder == "asc" {
		direction = " ASC"
	}
	return column + direction
}

// applyFilters 附加关键字/分类筛选;两者可叠加。
func (r *FileRepo) applyFilters(db *gorm.DB, q *request.FileQuery) *gorm.DB {
	if q.Keyword != "" {
		like := "%" + q.Keyword + "%"
		db = db.Where("name LIKE ? OR original_name LIKE ?", like, like)
	}
	if q.Category != "" {
		if q.Category == entity.FileCategoryOther {
			// "其他" = 无扩展名或不在任何已知分类内的文件(反向匹配)。
			db = db.Where("ext IS NULL OR LOWER(ext) NOT IN ?", entity.AllKnownExts())
		} else if exts, ok := entity.FileCategoryExts[q.Category]; ok && len(exts) > 0 {
			db = db.Where("LOWER(ext) IN ?", exts)
		} else {
			// 未知分类:回退到空结果而不是忽略筛选,避免静默绕过。
			db = db.Where("1 = 0")
		}
	}
	return db
}

// FindPage 分页查询文件列表,返回数据与总数。
func (r *FileRepo) FindPage(ctx context.Context, q *request.FileQuery) ([]entity.SysFile, int64, error) {
	countDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysFile{}), q)
	dataDB := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysFile{}), q).
		Order(fileSortClause(q.SortBy, q.SortOrder))
	return paginate[entity.SysFile](countDB, dataDB, q)
}

// FindByID 按主键查询单个文件;不存在返回 (nil, nil)。
func (r *FileRepo) FindByID(ctx context.Context, id uint64) (*entity.SysFile, error) {
	var file entity.SysFile
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&file).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &file, nil
}

// FindByIDs 批量查询文件(用于删除前确认可见性并定位物理文件)。
func (r *FileRepo) FindByIDs(ctx context.Context, ids []uint64) ([]entity.SysFile, error) {
	var files []entity.SysFile
	err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&files).Error
	return files, err
}

// CreateBatch 批量插入文件元数据(ID/审计字段由 GORM 回调自动填充)。
func (r *FileRepo) CreateBatch(ctx context.Context, files []*entity.SysFile) error {
	if len(files) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&files).Error
}

// Rename 更新文件显示名(限定 ID,防御性不动 Path)。
func (r *FileRepo) Rename(ctx context.Context, id uint64, name string) error {
	return r.db.WithContext(ctx).Model(&entity.SysFile{}).Where("id = ?", id).
		Updates(map[string]any{"name": name, "original_name": name}).Error
}

// DeleteByIDs 软删除指定 ID 的文件元数据(ID 参数 + scope 条件共同约束)。
func (r *FileRepo) DeleteByIDs(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Delete(&entity.SysFile{}, ids).Error
}

// Stats 对可见(数据权限内)文件做单行聚合:总数/总大小/近7天新增/分类计数。
// 分类列别名约定 "<category>_count"(见 entity.FileStatsRow),分类清单变化需同步。
// 聚合走 Model+Select 而非 Raw:soft-delete 与 datascope 子句正常生效。
func (r *FileRepo) Stats(ctx context.Context, weekStart time.Time) (*entity.FileStatsRow, error) {
	selectSQL := `COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN size IS NULL THEN 0 ELSE size END), 0) AS total_size,
		SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) AS week_uploads`
	args := make([]any, 0, len(entity.FileCategories)+1)
	args = append(args, weekStart)
	for _, category := range entity.FileCategories {
		if category == entity.FileCategoryOther {
			selectSQL += ", SUM(CASE WHEN ext IS NULL OR LOWER(ext) NOT IN (?) THEN 1 ELSE 0 END) AS other_count"
			args = append(args, entity.AllKnownExts())
			continue
		}
		selectSQL += fmt.Sprintf(", SUM(CASE WHEN LOWER(ext) IN (?) THEN 1 ELSE 0 END) AS %s_count", category)
		args = append(args, entity.FileCategoryExts[category])
	}

	row := &entity.FileStatsRow{}
	err := r.db.WithContext(ctx).Model(&entity.SysFile{}).Select(selectSQL, args...).Scan(row).Error
	return row, err
}
