package entity

import "time"

type SysNotice struct {
	BaseEntity
	Title       string     `gorm:"column:title;size:128;not null" json:"title"`
	Content     *string    `gorm:"column:content;type:text"             json:"content"`
	NoticeType  *int8      `gorm:"column:notice_type;default:1"   json:"noticeType"`
	Status      *int8      `gorm:"column:status;default:0"        json:"status"`
	Priority    *int8      `gorm:"column:priority;default:0"      json:"priority"`
	PublishType *int8      `gorm:"column:publish_type;default:1"   json:"publishType"`
	PublishTime *time.Time `gorm:"column:publish_time;index:idx_notice_publish_time" json:"publishTime"`
	// 接收范围:创建/编辑时选择,发布时据此解析收件人。
	// TargetType: 0=全体成员 1=指定角色 2=指定部门 3=指定个人
	// TargetIDs:  逗号分隔的收件对象 ID 列表(与 TargetType 对应)
	TargetType *int8  `gorm:"column:target_type" json:"targetType"`
	TargetIDs  string `gorm:"column:target_ids;size:2048;not null;default:''" json:"targetIds"`
}

// Kept for backward compatibility.
const (
	NoticeStatusDraft       int8 = 0
	NoticeStatusPublished   int8 = 1
	NoticeStatusRevoked     int8 = 2
	NoticeTypeNotice        int8 = 1
	NoticeTypeAnnounce      int8 = 2
	NoticePriorityNormal    int8 = 0
	NoticePriorityImportant int8 = 1
	NoticePriorityUrgent    int8 = 2
	NoticePublishTypeAll    int8 = 1
	NoticePublishTypeCustom int8 = 2

	NoticeTargetTypeAll  int8 = 0 // 全体成员
	NoticeTargetTypeRole int8 = 1 // 指定角色
	NoticeTargetTypeDept int8 = 2 // 指定部门
	NoticeTargetTypeUser int8 = 3 // 指定个人
)

func (SysNotice) TableName() string { return "sys_notice" }
