package handler

import (
	"context"
	"mime/multipart"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/captcha"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"go.uber.org/zap"

	"github.com/gin-gonic/gin"
)

// CaptchaInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type CaptchaInterface interface {
	Generate(ctx context.Context, clientIP string) (*captcha.Result, error)
	Unlock(ctx context.Context, userID uint64) error
}

// AuthServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
// AuthServiceInterface defines the business-logic contract for authentication operations.
type AuthServiceInterface interface {
	Login(ctx context.Context, req *request.LoginReq, ip, userAgent string) (*response.LoginResp, error)
	Logout(ctx context.Context, token, ip string) error
	GetUserInfo(ctx context.Context) (*response.UserInfoResp, error)
	GetUserOverview(ctx context.Context) (*response.UserOverviewResp, error)
	ChangePassword(ctx context.Context, req *request.ChangePasswordReq, accessToken string) error
	VerifyPassword(ctx context.Context, req *request.VerifyPasswordReq) (*response.VerifyPasswordResp, error)
	UpdateProfile(ctx context.Context, req *request.UpdateProfileReq) error
	UploadAvatar(ctx context.Context, header *multipart.FileHeader) (string, error)
	AvatarFilePath(ctx context.Context, userID uint64) (string, error)
	// GetUserPermissions 的消费方是 middleware.PermissionGuard(自有窄接口),
	// AuthHandler 不调用,按需包含原则不在此声明。
	RefreshToken(ctx context.Context, req *request.RefreshTokenReq, ip, userAgent string) (*response.RefreshTokenResp, error)
}

// AuthHandler exposes HTTP handlers for authentication endpoints.
type AuthHandler struct {
	svc        AuthServiceInterface
	captchaSvc CaptchaInterface
	logger     logger.LoggerInterface
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(svc AuthServiceInterface, captchaSvc CaptchaInterface, logger logger.LoggerInterface) *AuthHandler {
	return &AuthHandler{svc: svc, captchaSvc: captchaSvc, logger: logger}
}

// Login handles POST /api/v1/login.
// @Summary      用户登录
// @Description  使用用户名和密码登录系统，返回 JWT access token 和 refresh token
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        req  body      request.LoginReq  true  "登录请求参数"
// @Success      200  {object}  app.Response{data=response.LoginResp}  "登录成功"
// @Failure      400  {object}  app.Response  "验证码错误(10004-10007)"
// @Failure      401  {object}  app.Response  "认证失败"
// @Router       /login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req request.LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.Login(c.Request.Context(), &req, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Logout handles POST /api/v1/logout.
// @Summary      用户登出
// @Description  清除当前用户的 token，退出登录
// @Tags         认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "登出成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if err := h.svc.Logout(c.Request.Context(), token, c.ClientIP()); err != nil {
		// 会话存储故障时不可谎报成功:客户端会丢弃本地 token,
		// 但服务端白名单仍在,该 token 在其 JWT 过期前依然可用。
		h.logger.Warn("logout failed, session may remain valid",
			zap.Error(err))
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// GetUserInfo handles GET /api/v1/user/info.
// @Summary      获取当前用户信息
// @Description  获取当前登录用户的详细信息，包括角色和权限列表
// @Tags         认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.UserInfoResp}  "获取成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/info [get]
func (h *AuthHandler) GetUserInfo(c *gin.Context) {
	resp, err := h.svc.GetUserInfo(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ChangePassword handles POST /api/v1/user/password.
// @Summary      修改密码
// @Description  当前登录用户修改自己的密码，需要提供旧密码
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        req  body      request.ChangePasswordReq  true  "修改密码请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "修改成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/password [post]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req request.ChangePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	// 传入当前 token：改密成功后立即失效（含本次会话），防止被盗 token
	// 在密码已修改后仍然有效至自然过期。
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if err := h.svc.ChangePassword(c.Request.Context(), &req, token); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// VerifyPassword handles POST /api/v1/user/password/verify.
// @Summary      验证当前登录密码
// @Description  锁屏解锁校验当前用户登录密码;错误不计入登录日志
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        req  body      request.VerifyPasswordReq  true  "验证密码请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "验证成功"
// @Failure      400  {object}  app.Response  "参数错误或密码不正确"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/password/verify [post]
func (h *AuthHandler) VerifyPassword(c *gin.Context) {
	var req request.VerifyPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.VerifyPassword(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// UpdateProfile handles PUT /api/v1/user/info — update one's own basic profile.
// @Summary      更新个人资料
// @Description  当前登录用户更新自己的姓名/邮箱/手机号(角色、部门、状态等不在个人中心维护)
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        req  body      request.UpdateProfileReq  true  "更新资料请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/info [put]
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	var req request.UpdateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.UpdateProfile(c.Request.Context(), &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// UploadAvatar handles POST /api/v1/user/info/avatar (multipart 表单域 "file")。
// 头像属认证域(个人中心),不走 /files 的权限门(system:file:upload)。
// @Summary      上传头像
// @Description  上传并替换当前用户的头像图片(JPG/PNG/WebP/GIF,≤2MB)
// @Tags         认证
// @Accept       multipart/form-data
// @Produce      json
// @Param        file  formData  file  true  "头像图片文件"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.AvatarUploadResp}  "上传成功"
// @Failure      400  {object}  app.Response  "文件不合法/超限"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/info/avatar [post]
func (h *AuthHandler) UploadAvatar(c *gin.Context) {
	// 4MB 内存阈值:头像上限 2MB,留出 multipart 表单开销余量。
	if err := c.Request.ParseMultipartForm(4 << 20); err != nil {
		app.Error(c, apperror.BadRequest("解析上传数据失败"))
		return
	}
	headers := c.Request.MultipartForm.File["file"]
	if len(headers) == 0 {
		app.Error(c, apperror.BadRequest("未接收到文件"))
		return
	}
	avatar, err := h.svc.UploadAvatar(c.Request.Context(), headers[0])
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.AvatarUploadResp{Avatar: avatar})
}

// GetAvatar handles GET /api/v1/user/avatar/:id — 输出指定用户的头像图片。
// 挂在公开分组:<img> 标签无法携带 Authorization 头,用户列表等场景以
// 普通图片源引用头像 URL,故不要求登录(文件内容仅头像类图片,不涉敏)。
// @Summary      获取用户头像
// @Description  按用户 ID 返回头像图片文件
// @Tags         认证
// @Produce      image/*
// @Param        id   path  uint64  true  "用户ID"
// @Success      200  {file}  file  "头像图片"
// @Failure      404  {object}  app.Response  "头像不存在"
// @Router       /user/avatar/{id} [get]
func (h *AuthHandler) GetAvatar(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	path, err := h.svc.AvatarFilePath(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	// 头像 URL 携带 ?v=<unix>,重传会换新 URL;未变化的请求长缓存即可。
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(path)
}

// RefreshToken handles POST /api/v1/refresh.
// @Summary      刷新令牌
// @Description  使用 refresh token 刷新 access token，返回新的 token 对
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        req  body      request.RefreshTokenReq  true  "刷新令牌请求"
// @Success      200  {object}  app.Response{data=response.RefreshTokenResp}  "刷新成功"
// @Failure      401  {object}  app.Response  "refresh token 无效或已过期"
// @Router       /refresh [post]
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req request.RefreshTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.RefreshToken(c.Request.Context(), &req, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// CaptchaGenerate handles POST /api/v1/captcha/generate.生成并存储验证码
// 属创建/触发型动作,按动词约定用 POST(无请求体,幂等性不成立)。
// @Summary      获取验证码
// @Description  生成数学题图片验证码，返回 captchaKey 和 base64 图片
// @Tags         认证
// @Accept       json
// @Produce      json
// @Success      200  {object}  app.Response{data=captcha.Result}  "获取成功"
// @Failure      429  {object}  app.Response  "请求频率过高"
// @Failure      500  {object}  app.Response  "服务内部错误"
// @Router       /captcha/generate [post]
func (h *AuthHandler) CaptchaGenerate(c *gin.Context) {
	result, err := h.captchaSvc.Generate(c.Request.Context(), c.ClientIP())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, result)
}

// GetUserOverview handles GET /api/v1/user/overview.
// @Summary      个人中心总览
// @Description  聚合当前用户的身份信息、登录统计、近 30 天登录活动与最近登录记录(个人中心页专用,仅本人数据)
// @Tags         认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.UserOverviewResp}  "获取成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /user/overview [get]
func (h *AuthHandler) GetUserOverview(c *gin.Context) {
	resp, err := h.svc.GetUserOverview(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}
