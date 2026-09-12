package migrations

import (
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     3,
		Description: "初始化内置部门（总部）",
		Up:          seedDept,
	})
}

// builtinDeptName 是内置根部门的固定名称，供后续迁移（v004 绑定 admin）
// 与运行时按名查询。种子间不共享包级变量，按 name 自行查库。
const builtinDeptName = "总部"

// seedDept 创建内置根部门「总部」。
//
// 根部门的 ParentID 为 nil、ancestors 只含自身 ID（与 service.DeptService
// 的约定一致：根部门 ancestors = strconv.FormatUint(dept.ID, 10)）。
// ID 由 snowflake 回调在 Create 时生成并回写到结构体，因此 ancestors
// 只能在 Create 之后再补写。
func seedDept(tx *gorm.DB) error {
	status := entity.DeptStatusEnabled
	dept := entity.SysDept{
		Name:   builtinDeptName,
		Sort:   util.Ptr(0),
		Status: &status,
	}
	if err := tx.Create(&dept).Error; err != nil {
		return err
	}

	dept.Ancestors = strconv.FormatUint(dept.ID, 10)
	if err := tx.Model(&entity.SysDept{}).
		Where("id = ?", dept.ID).
		Update("ancestors", dept.Ancestors).Error; err != nil {
		return fmt.Errorf("seedDept: 回写 ancestors 失败: %w", err)
	}
	return nil
}
