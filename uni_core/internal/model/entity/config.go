package entity

type SysConfig struct {
	BaseEntity
	Name        string  `gorm:"column:name;size:64;not null;default:''" json:"name"`
	ConfigKey   string  `gorm:"column:config_key;size:128;not null"  json:"configKey"`
	ConfigValue string  `gorm:"column:config_value;size:512;not null" json:"configValue"`
	ConfigType  string  `gorm:"column:config_type;size:1;default:S"  json:"configType"`
	Remark      *string `gorm:"column:remark;size:512"               json:"remark"`
	Status      *int8   `gorm:"column:status;default:1"              json:"status"`
}

// Kept for backward compatibility.
const (
	ConfigStatusEnabled  int8 = 1
	ConfigStatusDisabled int8 = 0
)

func (SysConfig) TableName() string { return "sys_config" }
