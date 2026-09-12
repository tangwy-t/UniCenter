package entity

import (
	"time"

	"gorm.io/gorm"
)

type BaseEntity struct {
	ID        uint64         `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"       json:"createdAt"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"       json:"updatedAt"`
	CreatedBy *uint64        `gorm:"column:created_by"                      json:"createdBy,string"`
	UpdatedBy *uint64        `gorm:"column:updated_by"                      json:"updatedBy,string"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"                json:"-"`
}
