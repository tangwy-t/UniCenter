package entity

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"
	"time"
)

type SysLoginLog struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	UserID    *uint64   `gorm:"column:user_id"                            json:"userId,string"`
	Username  string    `gorm:"column:username;size:64;not null"          json:"username"`
	IP        *string   `gorm:"column:ip;size:64"                         json:"ip"`
	Location  *string   `gorm:"column:location;size:128"                  json:"location"`
	Browser   *string   `gorm:"column:browser;size:64"                    json:"browser"`
	OS        *string   `gorm:"column:os;size:64"                         json:"os"`
	Code      int       `gorm:"column:code;not null;default:0"          json:"code"`
	Msg       *string   `gorm:"column:msg;size:256"                       json:"msg"`
	LoginTime time.Time `gorm:"column:login_time;not null;index:idx_login_log_login_time" json:"loginTime"`
}

func (SysLoginLog) TableName() string { return "sys_login_log" }

// DataScopeRules implements rule.DataScopeable.
func (SysLoginLog) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "user_id", DimensionType: "dept", Via: &rule.ScopeVia{Table: "sys_user", Select: "id", Where: "dept_id"}},
		{Column: "user_id", DimensionType: "self"},
	}
}
