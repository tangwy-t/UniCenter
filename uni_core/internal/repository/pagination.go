package repository

import "gorm.io/gorm"

// pageQuery 是各查询 DTO 共同满足的分页契约:*ConfigQuery / *UserQuery 等
// 均内嵌 app.PageRequest,Offset() 与 GetPageSize() 经方法提升对外可用。
type pageQuery interface {
	Offset() int
	GetPageSize() int
}

// paginate 执行"先 Count 后取页"的两段查询,统一各 Repo FindPage 的样板:
//
//	countDB  —— 仅用于 COUNT 的会话(不携带 Order/Offset/Limit);
//	dataDB  —— 用于取当前页的会话,由调用方按需链入 Order / Preload;
//
// 两个会话相互独立,COUNT 与最终 SELECT 不会共享 GORM 语句状态。此前各
// Repo 各自手写 countDB/dataDB 两段 + Offset/Limit/Find,只有排序方向不同,
// 本函数把这些机械步骤收口到一处,越界/默认页大小等边界继续由
// app.PageRequest 统一处理。
func paginate[T any](countDB, dataDB *gorm.DB, page pageQuery) ([]T, int64, error) {
	var total int64
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []T
	if err := dataDB.Offset(page.Offset()).Limit(page.GetPageSize()).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
