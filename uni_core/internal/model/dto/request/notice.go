package request

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

type NoticeQuery struct {
	app.PageRequest
	Title      string `form:"title"`
	NoticeType *int8  `form:"noticeType"`
	Status     *int8  `form:"status"`
}

type CreateNoticeReq struct {
	Title       string  `json:"title" binding:"required,max=128"`
	Content     *string `json:"content" binding:"required"`
	NoticeType  *int8   `json:"noticeType"`
	Priority    *int8   `json:"priority"`
	PublishType *int8   `json:"publishType"`
	// 接收范围(0=全体成员 1=指定角色 2=指定部门 3=指定个人)与对象 ID CSV
	TargetType *int8  `json:"targetType"`
	TargetIDs  string `json:"targetIds"`
}

type UpdateNoticeReq struct {
	Title       string  `json:"title" binding:"required,max=128"`
	Content     *string `json:"content" binding:"required"`
	NoticeType  *int8   `json:"noticeType"`
	Priority    *int8   `json:"priority"`
	PublishType *int8   `json:"publishType"`
	// 接收范围(0=全体成员 1=指定角色 2=指定部门 3=指定个人)与对象 ID CSV
	TargetType *int8  `json:"targetType"`
	TargetIDs  string `json:"targetIds"`
}

type PublishNoticeReq struct {
	UserIDs util.JsonUint64Slice `json:"userIds"`
	RoleIDs util.JsonUint64Slice `json:"roleIds"`
	DeptIDs util.JsonUint64Slice `json:"deptIds"`
}

// NoticeReadUsersQuery 已读用户列表查询。
type NoticeReadUsersQuery struct {
	app.PageRequest
	SearchValue string `form:"searchValue"` // 登录名称 / 用户姓名模糊匹配
}

// NoticeTargetUsersQuery 反查已选接收人(按 ID 还原标签)。
type NoticeTargetUsersQuery struct {
	IDs string `form:"ids"` // 逗号分隔的用户 ID
}
