package entity

type SysUserRole struct {
	ID     uint64 `gorm:"column:id;primaryKey;autoIncrement:false"      json:"id,string"`
	UserID uint64 `gorm:"column:user_id;not null;uniqueIndex:uk_user_role" json:"userId,string"`
	RoleID uint64 `gorm:"column:role_id;not null;uniqueIndex:uk_user_role" json:"roleId,string"`
}

func (SysUserRole) TableName() string { return "sys_user_role" }
