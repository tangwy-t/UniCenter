package entity

type SysDictType struct {
	BaseEntity
	Code   string  `gorm:"column:code;size:64;not null" json:"code"`
	Name   string  `gorm:"column:name;size:64;not null" json:"name"`
	Status *int8   `gorm:"column:status;default:1"      json:"status"`
	Remark *string `gorm:"column:remark;size:512"       json:"remark"`
}

// Kept for backward compatibility.
const (
	DictTypeStatusEnabled  int8 = 1
	DictTypeStatusDisabled int8 = 0
)

func (SysDictType) TableName() string { return "sys_dict_type" }
