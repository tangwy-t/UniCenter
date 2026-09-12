package entity

import "time"

// SysMigration 记录已执行的数据库迁移版本。
// 不嵌入 BaseEntity —— 版本号自身即主键，snowflake ID / 软删除 / audit 字段
// 对元数据表无意义。
type SysMigration struct {
	Version     int       `gorm:"column:version;primaryKey"          json:"version"`
	Description string    `gorm:"column:description;size:255;not null" json:"description"`
	AppliedAt   time.Time `gorm:"column:applied_at;not null"         json:"appliedAt"`
}

func (SysMigration) TableName() string { return "sys_migration" }
