package entity

import "github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"

type SysDept struct {
	BaseEntity
	ParentID  *uint64    `gorm:"column:parent_id;default:0"     json:"parentId,string"`
	Ancestors string     `gorm:"column:ancestors;size:512;default:''" json:"ancestors"`
	Name      string     `gorm:"column:name;size:64;not null"   json:"name"`
	Sort      *int       `gorm:"column:sort;default:0"          json:"sort"`
	Leader    *string    `gorm:"column:leader;size:64"          json:"leader"`
	Phone     *string    `gorm:"column:phone;size:20"           json:"phone"`
	Email     *string    `gorm:"column:email;size:128"          json:"email"`
	Status    *int8      `gorm:"column:status;default:1"        json:"status"`
	Children  []*SysDept `gorm:"-"                              json:"children,omitempty"`
}

// Kept for backward compatibility.
const (
	DeptStatusEnabled  int8 = 1
	DeptStatusDisabled int8 = 0
)

func (SysDept) TableName() string { return "sys_dept" }

// DataScopeRules implements rule.DataScopeable.
func (SysDept) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "id", DimensionType: "dept"},
		// self 维度:仅本人用户也要能看见自己所属的部门。部门表主键是部门 ID,
		// 不能像 sys_user 那样直接 `id IN (用户ID)`,需经 sys_user 把用户 ID
		// 翻译成用户的 dept_id:sys_dept.id IN (SELECT dept_id FROM sys_user
		// WHERE id IN ?)。与 sys_file/login_log 等实体的 dept 维度 Via 子查询同构。
		{Column: "id", DimensionType: "self", Via: &rule.ScopeVia{Table: "sys_user", Select: "dept_id", Where: "id"}},
	}
}
