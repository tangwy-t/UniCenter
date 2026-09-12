package entity

type SysDictData struct {
	BaseEntity
	TypeID    uint64  `gorm:"column:type_id;not null"               json:"typeId,string"`
	ListClass *string `gorm:"column:list_class;size:16"             json:"listClass"`
	Label     string  `gorm:"column:label;size:64;not null"         json:"label"`
	Value     string  `gorm:"column:value;size:64;not null"         json:"value"`
	IsDefault *int8   `gorm:"column:is_default;default:0"           json:"isDefault"`
	Sort      *int    `gorm:"column:sort;default:0"                 json:"sort"`
	Status    *int8   `gorm:"column:status;default:1"               json:"status"`
	Remark    *string `gorm:"column:remark;size:512"                json:"remark"`
}

// Kept for backward compatibility.
const (
	DictDataStatusEnabled  int8 = 1
	DictDataStatusDisabled int8 = 0
	DictDataDefaultYes     int8 = 1
	DictDataDefaultNo      int8 = 0
)

func (SysDictData) TableName() string { return "sys_dict_data" }
