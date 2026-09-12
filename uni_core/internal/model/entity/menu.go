package entity

import "github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"

type SysMenu struct {
	BaseEntity
	ParentID  *uint64    `gorm:"column:parent_id;default:0;index:idx_menu_parent_id" json:"parentId,string"`
	Name      string     `gorm:"column:name;size:64;not null"   json:"name"`
	Type      string     `gorm:"column:type;size:8;default:dir" json:"type"`
	Perms     *string    `gorm:"column:perms;size:128"          json:"perms"`
	Path      *string    `gorm:"column:path;size:256"           json:"path"`
	Component *string    `gorm:"column:component;size:256"      json:"component"`
	Icon      *string    `gorm:"column:icon;size:64"            json:"icon"`
	Sort      *int       `gorm:"column:sort;default:0"          json:"sort"`
	Visible   *int8      `gorm:"column:visible;default:1"       json:"visible"`
	Status    *int8      `gorm:"column:status;default:1"        json:"status"`
	Children  []*SysMenu `gorm:"-"                              json:"children,omitempty"`
}

// Kept for backward compatibility.
const (
	MenuStatusEnabled  int8 = 1
	MenuStatusDisabled int8 = 0
	MenuVisible        int8 = 1
	MenuHidden         int8 = 0
)

func (SysMenu) TableName() string { return "sys_menu" }

// DataScopeRules implements rule.DataScopeable.
func (SysMenu) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "id", DimensionType: "role"},
	}
}
