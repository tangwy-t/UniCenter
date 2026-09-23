package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
)

// AgentReleaseServiceInterface 是发布物域的能力面（由 service.AgentReleaseService 实现）。
type AgentReleaseServiceInterface interface {
	List(ctx context.Context) (*response.AgentReleaseListResp, error)
	Upload(ctx context.Context, version, osName, arch, notes string, src io.Reader, declaredSize int64) (*response.AgentReleaseItem, error)
	Publish(ctx context.Context, id uint64) error
	Unpublish(ctx context.Context, id uint64) error
	Delete(ctx context.Context, id uint64) error
	OpenForDownload(ctx context.Context, dev *entity.Device, version string) (io.ReadSeekCloser, *entity.AgentRelease, error)
}

// AgentDownloadAuthenticator 用 agent token 鉴权下载请求
// （由 service.AgentIngestService 实现：与 WS 首帧同一套凭据、同一条不可区分纪律）。
type AgentDownloadAuthenticator interface {
	AuthenticateDownload(ctx context.Context, token string) (*entity.Device, error)
}

// AgentReleaseHandler 暴露发布物管理端点与**设备侧下载端点**。
type AgentReleaseHandler struct {
	svc  AgentReleaseServiceInterface
	auth AgentDownloadAuthenticator
}

func NewAgentReleaseHandler(svc AgentReleaseServiceInterface, auth AgentDownloadAuthenticator) *AgentReleaseHandler {
	return &AgentReleaseHandler{svc: svc, auth: auth}
}

// List handles GET /api/v1/devices/releases — 发布物列表。
// @Summary      发布物列表
// @Description  agent 程序包列表（含可删除性结论）与已发布版本号
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.AgentReleaseListResp}  "查询成功"
// @Router       /devices/releases [get]
func (h *AgentReleaseHandler) List(c *gin.Context) {
	resp, err := h.svc.List(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Upload handles POST /api/v1/devices/releases — 上传程序包（草稿态）。
// @Summary      上传 Agent 程序包
// @Description  上传 agent 二进制（草稿态，需发布后才能被选为目标版本）；sha256 由服务端计算
// @Tags         设备监控
// @Accept       multipart/form-data
// @Produce      json
// @Param        file     formData  file    true  "agent 二进制"
// @Param        version  formData  string  true  "版本号(如 0.2.0)"
// @Param        os       formData  string  true  "目标系统(linux)"
// @Param        arch     formData  string  true  "目标架构(amd64/arm64)"
// @Param        notes    formData  string  false "备注"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.AgentReleaseItem}  "上传成功"
// @Failure      400  {object}  app.Response  "参数错误或超过大小上限"
// @Failure      409  {object}  app.Response  "该版本在该平台上已存在"
// @Router       /devices/releases [post]
func (h *AgentReleaseHandler) Upload(c *gin.Context) {
	var form request.AgentReleaseUploadRequest
	if err := c.ShouldBind(&form); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		app.Error(c, apperror.BadRequest("请选择要上传的程序包"))
		return
	}
	f, err := fh.Open()
	if err != nil {
		app.Error(c, apperror.Internal("读取上传文件失败", err))
		return
	}
	defer func() { _ = f.Close() }()

	item, err := h.svc.Upload(c.Request.Context(), form.Version, form.OS, form.Arch,
		form.Notes, f, fh.Size)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, item)
}

// Publish handles POST /api/v1/devices/releases/:id/publish — 发布。
// @Summary      发布程序包
// @Description  把草稿置为已发布（此后可被选为目标版本）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id  path  uint64  true  "程序包ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "已发布"
// @Failure      404  {object}  app.Response  "程序包不存在"
// @Router       /devices/releases/{id}/publish [post]
func (h *AgentReleaseHandler) Publish(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Publish(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Unpublish handles POST /api/v1/devices/releases/:id/unpublish — 撤回。
// @Summary      撤回程序包
// @Description  撤回发布（只影响新下发；已指向该版本的设备仍可下载）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id  path  uint64  true  "程序包ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "已撤回"
// @Failure      404  {object}  app.Response  "程序包不存在"
// @Router       /devices/releases/{id}/unpublish [post]
func (h *AgentReleaseHandler) Unpublish(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Unpublish(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/devices/releases/:id — 删除。
// @Summary      删除程序包
// @Description  删除程序包；被升级记录使用过的版本会被拒绝（回滚余量保护）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id  path  uint64  true  "程序包ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "已删除"
// @Failure      404  {object}  app.Response  "程序包不存在"
// @Failure      409  {object}  app.Response  "该版本已被使用，需保留以便回滚"
// @Router       /devices/releases/{id} [delete]
func (h *AgentReleaseHandler) Delete(c *gin.Context) {
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

// Download handles GET /api/v1/agent/releases/:version/download — **设备侧**下载。
//
// 鉴权是 agent token（请求头 X-Agent-Token），与 WS 首帧同一套凭据：
//   - 挂在 JWT 之外（agent 没有控制台凭据）；
//   - 三种失败（无此 token / 设备已删 / 设备停用）返回**同一个** 401 ——
//     「设备是否存在」不能在状态码上泄露；
//   - **token 不进日志**：本路由是 GET，operation-log 中间件只记写操作；
//     处理器本身也绝不把 token 写进任何 message。
//
// @Summary      Agent 下载程序包
// @Description  agent 用自身令牌下载指定版本的程序包（按设备平台解析）
// @Tags         Agent 通道
// @Accept       json
// @Produce      application/octet-stream
// @Param        version       path   string  true   "版本号"
// @Param        X-Agent-Token header string  true   "agent 令牌"
// @Success      200  {file}    binary  "程序包字节"
// @Failure      401  {object}  app.Response  "凭据无效"
// @Failure      404  {object}  app.Response  "没有该设备可用的程序文件"
// @Router       /agent/releases/{version}/download [get]
func (h *AgentReleaseHandler) Download(c *gin.Context) {
	token := c.GetHeader("X-Agent-Token")
	dev, err := h.auth.AuthenticateDownload(c.Request.Context(), token)
	if err != nil {
		app.Error(c, err)
		return
	}
	rc, rel, err := h.svc.OpenForDownload(c.Request.Context(), dev, c.Param("version"))
	if err != nil {
		app.Error(c, err)
		return
	}
	defer func() { _ = rc.Close() }()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(rel.SizeBytes, 10))
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", rel.FileName))
	// ServeContent 而不是 io.Copy：它按 Range 请求给分段（内网大包也有用），
	// 并在 Content-Length 已知时自动处理 HEAD。
	http.ServeContent(c.Writer, c.Request, rel.FileName, rel.CreatedAt, rc)
}
