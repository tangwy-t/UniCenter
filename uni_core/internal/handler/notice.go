package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"

	"github.com/gin-gonic/gin"
)

// NoticeServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type NoticeServiceInterface interface {
	FindPage(ctx context.Context, query *request.NoticeQuery) (*app.PageResponse, error)
	FindByID(ctx context.Context, id uint64) (*response.NoticeResp, error)
	Create(ctx context.Context, req *request.CreateNoticeReq) (uint64, error)
	Update(ctx context.Context, id uint64, req *request.UpdateNoticeReq) error
	Delete(ctx context.Context, id uint64) error
	Publish(ctx context.Context, id uint64, req *request.PublishNoticeReq) error
	Revoke(ctx context.Context, id uint64) error
	MarkRead(ctx context.Context, noticeID, userID uint64) error
	MarkAllRead(ctx context.Context, userID uint64) error
	MyNotices(ctx context.Context) (*response.NoticeMyListResp, error)
	FindReadUsers(ctx context.Context, noticeID uint64, query *request.NoticeReadUsersQuery) (*app.PageResponse, error)
	FindTargetUsers(ctx context.Context, idsCSV string) ([]response.NoticeTargetUserResp, error)
}

type NoticeHandler struct {
	svc NoticeServiceInterface
}

func NewNoticeHandler(svc NoticeServiceInterface) *NoticeHandler {
	return &NoticeHandler{svc: svc}
}

// List handles GET /api/v1/notices — paginated notice listing.
// @Summary      通知公告列表
// @Description  分页查询通知公告列表，支持按标题、类型、状态筛选
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        page        query  int     false  "页码"           default(1)
// @Param        pageSize    query  int     false  "每页条数"       default(10)
// @Param        title       query  string  false  "标题(模糊查询)"
// @Param        noticeType  query  int     false  "类型(1=通知 2=公告)"
// @Param        status      query  int     false  "状态(0=草稿 1=已发布 2=已撤回)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /notices [get]
func (h *NoticeHandler) List(c *gin.Context) {
	var query request.NoticeQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.FindPage(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// GetByID handles GET /api/v1/notices/:id — get a single notice.
// @Summary      通知公告详情
// @Description  根据通知公告 ID 查询详细信息
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "通知公告ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.NoticeResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id} [get]
func (h *NoticeHandler) GetByID(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.FindByID(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Create handles POST /api/v1/notices — create a notice.
// @Summary      创建通知公告
// @Description  创建新的通知公告（默认状态为草稿）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateNoticeReq  true  "创建通知公告请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /notices [post]
func (h *NoticeHandler) Create(c *gin.Context) {
	var req request.CreateNoticeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.IDResp{ID: id})
}

// Update handles PUT /api/v1/notices/:id — update a notice.
// @Summary      更新通知公告
// @Description  更新指定通知公告的信息（已发布的通知不能修改）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                  true  "通知公告ID"
// @Param        req  body      request.UpdateNoticeReq  true  "更新通知公告请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id} [put]
func (h *NoticeHandler) Update(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.UpdateNoticeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/notices/:id — delete a notice.
// @Summary      删除通知公告
// @Description  软删除指定通知公告
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "通知公告ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id} [delete]
func (h *NoticeHandler) Delete(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Publish handles POST /api/v1/notices/:id/publish — publish a notice.
// @Summary      发布通知公告
// @Description  发布指定通知公告，支持全员发布或自定义发布范围（按用户/角色/部门）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                   true  "通知公告ID"
// @Param        req  body      request.PublishNoticeReq  true  "发布通知公告请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "发布成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id}/publish [post]
func (h *NoticeHandler) Publish(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.PublishNoticeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.Publish(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Revoke handles POST /api/v1/notices/:id/revoke — revoke a published notice.
// @Summary      撤回通知公告
// @Description  撤回已发布的通知公告
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "通知公告ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "撤回成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id}/revoke [post]
func (h *NoticeHandler) Revoke(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Revoke(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// MarkRead handles POST /api/v1/notices/:id/read — mark a notice as read.
// @Summary      标记已读
// @Description  将指定通知公告标记为当前用户已读
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "通知公告ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "标记成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id}/read [post]
func (h *NoticeHandler) MarkRead(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	userID, ok := contextkeys.UserIDFromCtx(c.Request.Context())
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录"))
		return
	}
	if err := h.svc.MarkRead(c.Request.Context(), id, userID); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// MarkAllRead handles POST /api/v1/notices/read-all — mark all visible notices read.
// 白名单(仅登录,与 /notices/my、/notices/:id/read 配套):普通用户也能一键已读。
// @Summary      全部标为已读
// @Description  将当前用户可见的全部已发布公告标记为已读（幂等）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "标记成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      500  {object}  app.Response  "标记全部已读失败"
// @Router       /notices/read-all [post]
func (h *NoticeHandler) MarkAllRead(c *gin.Context) {
	userID, ok := contextkeys.UserIDFromCtx(c.Request.Context())
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录"))
		return
	}
	if err := h.svc.MarkAllRead(c.Request.Context(), userID); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// MyNotices handles GET /api/v1/notices/my — the current user's notice inbox.
// 白名单接口(登录即可,对齐若依 listTop):返回当前用户可见的
// 已发布公告(含已读标记)与未读数,服务端按接收范围过滤。
// @Summary      我的公告
// @Description  获取当前用户可见的已发布公告列表（含已读标记与未读数）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.NoticeMyListResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /notices/my [get]
func (h *NoticeHandler) MyNotices(c *gin.Context) {
	resp, err := h.svc.MyNotices(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ReadUsers handles GET /api/v1/notices/:id/read-users — read users list.
// @Summary      通知公告已读用户
// @Description  分页查询指定通知公告的已读用户列表，支持按登录名/姓名模糊搜索
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        id          path      uint64  true  "通知公告ID"
// @Param        page        query  int     false  "页码"           default(1)
// @Param        pageSize    query  int     false  "每页条数"       default(10)
// @Param        searchValue query  string  false  "登录名称/用户姓名"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      404  {object}  app.Response  "通知公告不存在"
// @Router       /notices/{id}/read-users [get]
func (h *NoticeHandler) ReadUsers(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var query request.NoticeReadUsersQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.FindReadUsers(c.Request.Context(), id, &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// TargetUsers handles GET /api/v1/notices/target-users — resolve 指定个人 labels.
// @Summary      反查通知接收人
// @Description  按逗号分隔的用户 ID 反查用户信息（接收范围回显用）
// @Tags         通知公告
// @Accept       json
// @Produce      json
// @Param        ids  query  string  true  "逗号分隔的用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.NoticeTargetUserResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      400  {object}  app.Response  "参数错误"
// @Router       /notices/target-users [get]
func (h *NoticeHandler) TargetUsers(c *gin.Context) {
	var query request.NoticeTargetUsersQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	rows, err := h.svc.FindTargetUsers(c.Request.Context(), query.IDs)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, rows)
}
