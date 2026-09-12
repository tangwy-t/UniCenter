package entity

type SysRole struct {
	BaseEntity
	Name      string    `gorm:"column:name;size:64;not null"   json:"name"`
	Code      string    `gorm:"column:code;size:64;not null;uniqueIndex:uk_role_code" json:"code"`
	DataScope *int8     `gorm:"column:data_scope;default:1"    json:"dataScope"`
	Sort      *int      `gorm:"column:sort;default:0"          json:"sort"`
	Status    *int8     `gorm:"column:status;default:1"        json:"status"`
	Remark    *string   `gorm:"column:remark;size:512"         json:"remark"`
	Menus     []SysMenu `gorm:"many2many:sys_role_menu;joinForeignKey:role_id;joinReferences:menu_id"                             json:"menus,omitempty"`
	Depts     []SysDept `gorm:"many2many:sys_role_dept;joinForeignKey:role_id;joinReferences:dept_id" json:"depts,omitempty"`
	// Users 是 SysUser.Roles 的反向关联, 指向同一张 sys_user_role join 表。
	// 用于从角色侧用 Association("Users").Append/Delete 调整成员, 与用户侧
	// 的 Association("Roles") 对称。两者都需在 migration.joinTableSetups 注册。
	Users []SysUser `gorm:"many2many:sys_user_role;joinForeignKey:role_id;joinReferences:user_id" json:"users,omitempty"`
}

// Kept for backward compatibility.
const (
	RoleStatusEnabled  int8 = 1
	RoleStatusDisabled int8 = 0
)

func (SysRole) TableName() string { return "sys_role" }
