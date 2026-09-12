package entity

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"
	"time"
)

type SysNoticeUser struct {
	BaseEntity
	NoticeID   uint64     `gorm:"column:notice_id;not null;uniqueIndex:uk_notice_user" json:"noticeId,string"`
	UserID     uint64     `gorm:"column:user_id;not null;uniqueIndex:uk_notice_user" json:"userId,string"`
	ReadStatus int8       `gorm:"column:read_status;default:0" json:"readStatus"`
	ReadTime   *time.Time `gorm:"column:read_time" json:"readTime"`
}

// NoticeWithRead 用户视角公告行:公告实体 + 当前用户的阅读状态投影
// (LEFT JOIN sys_notice_user)。仅查询投影使用,不做持久化。
type NoticeWithRead struct {
	SysNotice
	ReadStatus *int8      `gorm:"column:read_status"`
	ReadTime   *time.Time `gorm:"column:read_time"`
}

// Notice 读取状态常量。此前标注 Deprecated 指向字典服务,但 repository/
// service 实际仍直接使用本常量(且字典查询在热路径上多一次往返)——
// 标注与现实不符,移除。如未来切换字典驱动,请同步迁移全部使用点。
const (
	NoticeReadStatusUnread int8 = 0
	NoticeReadStatusRead   int8 = 1
)

func (SysNoticeUser) TableName() string { return "sys_notice_user" }

// DataScopeRules implements rule.DataScopeable.
func (SysNoticeUser) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "user_id", DimensionType: "dept", Via: &rule.ScopeVia{Table: "sys_user", Select: "id", Where: "dept_id"}},
		{Column: "user_id", DimensionType: "self"},
	}
}
