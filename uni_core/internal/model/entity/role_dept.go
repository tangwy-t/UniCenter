package entity

type SysRoleDept struct {
	ID     uint64 `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	RoleID uint64 `gorm:"column:role_id;not null;uniqueIndex:uk_role_dept" json:"roleId,string"`
	DeptID uint64 `gorm:"column:dept_id;not null;uniqueIndex:uk_role_dept" json:"deptId,string"`
}

func (SysRoleDept) TableName() string { return "sys_role_dept" }
