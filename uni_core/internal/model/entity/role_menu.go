package entity

type SysRoleMenu struct {
	ID     uint64 `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	RoleID uint64 `gorm:"column:role_id;not null;uniqueIndex:uk_role_menu" json:"roleId,string"`
	MenuID uint64 `gorm:"column:menu_id;not null;uniqueIndex:uk_role_menu" json:"menuId,string"`
}

func (SysRoleMenu) TableName() string { return "sys_role_menu" }
