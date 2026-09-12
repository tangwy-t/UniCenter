package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

type NoticeResp struct {
	ID          uint64 `json:"id,string"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	NoticeType  int8   `json:"noticeType"`
	Status      int8   `json:"status"`
	Priority    int8   `json:"priority"`
	PublishType int8   `json:"publishType"`
	// 接收范围:0=全体成员 1=指定角色 2=指定部门 3=指定个人
	TargetType int8   `json:"targetType"`
	TargetIDs  string `json:"targetIds"`
	TargetDesc string `json:"targetDesc"` // 展示文案,如 全体成员 / 指定角色（2个）
	// 当前用户阅读状态:仅"已发布"行有值(0 未读 / 1 已读;无关联行=0 未读),草稿/已撤回为 nil。
	ReadStatus  *int8          `json:"readStatus,omitempty"`
	CreateBy    string         `json:"createBy"` // 创建者登录名
	PublishTime *util.JSONTime `json:"publishTime"`
	CreatedAt   util.JSONTime  `json:"createdAt"`
	UpdatedAt   util.JSONTime  `json:"updatedAt"`
}

// NoticeReadUserResp 已读用户行(阅读用户弹窗)。
type NoticeReadUserResp struct {
	UserID   uint64         `json:"userId,string"`
	Username string         `json:"username"`
	RealName string         `json:"realName"`
	DeptName string         `json:"deptName"`
	Phone    string         `json:"phone"`
	ReadTime *util.JSONTime `json:"readTime"`
}

// NoticeTargetUserResp 指定个人反查行(接收范围回显)。
type NoticeTargetUserResp struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username"`
	RealName string `json:"realName"`
}

// NoticeMyItemResp 用户公告收件箱条目(白名单接口 /notices/my):
// 公告字段 + 当前用户阅读状态。
type NoticeMyItemResp struct {
	ID          uint64         `json:"id,string"`
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	NoticeType  int8           `json:"noticeType"`
	Priority    int8           `json:"priority"`
	IsRead      bool           `json:"isRead"`
	PublishTime *util.JSONTime `json:"publishTime"`
}

// NoticeMyListResp 用户公告收件箱(/notices/my 响应):可见公告列表 + 未读数。
type NoticeMyListResp struct {
	List        []NoticeMyItemResp `json:"list"`
	UnreadCount int                `json:"unreadCount"`
}
