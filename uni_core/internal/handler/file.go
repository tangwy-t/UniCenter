package handler

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// FileServiceInterface 文件管理服务方法集(消费方接口,按需定义)。
type FileServiceInterface interface {
	List(ctx context.Context, q *request.FileQuery) (*app.PageResponse, error)
	Stats(ctx context.Context) (*response.FileStatsResp, error)
	Upload(ctx context.Context, headers []*multipart.FileHeader) ([]response.FileResp, error)
	Rename(ctx context.Context, id uint64, name string) error
	DeleteMany(ctx context.Context, ids []uint64) error
	OpenFile(ctx context.Context, id uint64) (*entity.SysFile, io.ReadSeekCloser, error)
	Thumb(ctx context.Context, id uint64, width int) ([]byte, string, error)
}

// FileHandler exposes HTTP handlers for the file management endpoints.
type FileHandler struct {
	svc FileServiceInterface
}

// NewFileHandler creates a new FileHandler.
func NewFileHandler(svc FileServiceInterface) *FileHandler {
	return &FileHandler{svc: svc}
}

// List handles GET /api/v1/files — paginated file listing.
// @Summary      文件列表
// @Description  分页查询文件列表,支持关键字/分类筛选与排序
// @Tags         文件管理
// @Accept       json
// @Produce      json
// @Param        page       query     int     false  "页码"           default(1)
// @Param        pageSize   query     int     false  "每页条数"       default(10)
// @Param        keyword    query     string  false  "文件名关键字(模糊)"
// @Param        category   query     string  false  "分类(image/video/audio/document/archive/code/other)"
// @Param        sortBy     query     string  false  "排序字段(createdAt/name/size)"
// @Param        sortOrder  query     string  false  "排序方向(asc/desc)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Router       /files [get]
func (h *FileHandler) List(c *gin.Context) {
	var q request.FileQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.List(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Stats handles GET /api/v1/files/stats — overview statistics for the UI header.
// @Summary      文件概览统计
// @Description  返回文件总数/总大小/近7天新增/分类计数(用于文件管理首页卡片与分布条)
// @Tags         文件管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.FileStatsResp}  "查询成功"
// @Router       /files/stats [get]
func (h *FileHandler) Stats(c *gin.Context) {
	resp, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Upload handles POST /api/v1/files — multipart upload (field: files, 可多文件).
// @Summary      上传文件
// @Description  接收 multipart/form-data 的 files 字段(支持多文件),落盘并登记元数据
// @Tags         文件管理
// @Accept       multipart/form-data
// @Produce      json
// @Param        files  formData  file  true  "上传文件(可多个,字段名 files)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.UploadFilesResp}  "上传成功"
// @Failure      400  {object}  app.Response  "文件为空/超出大小限制/类型不允许"
// @Router       /files [post]
func (h *FileHandler) Upload(c *gin.Context) {
	// 32MB 内存阈值:超出部分由 multipart 写入临时文件,避免大文件全量驻留内存。
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		app.Error(c, apperror.BadRequest("解析上传数据失败"))
		return
	}
	headers := c.Request.MultipartForm.File["files"]
	items, err := h.svc.Upload(c.Request.Context(), headers)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.UploadFilesResp{Items: items})
}

// Rename handles PUT /api/v1/files/:id — rename a file.
// @Summary      重命名文件
// @Description  更新文件显示名(不影响物理存储)
// @Tags         文件管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                      true  "文件ID"
// @Param        req  body      request.RenameFileReq       true  "重命名请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Router       /files/{id} [put]
func (h *FileHandler) Rename(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.RenameFileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.Rename(c.Request.Context(), id, req.Name); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// DeleteMany handles DELETE /api/v1/files — batch delete.
// @Summary      批量删除文件
// @Description  删除指定 ID 的文件元数据并清理本地磁盘文件
// @Tags         文件管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.DeleteFilesReq  true  "批量删除请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Router       /files [delete]
func (h *FileHandler) DeleteMany(c *gin.Context) {
	var req request.DeleteFilesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.DeleteMany(c.Request.Context(), []uint64(req.IDs)); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Thumbnail handles GET /api/v1/files/:id/thumbnail — 按需生成的图片缩略图。
// @Summary      图片缩略图
// @Description  服务端将图片缩放为最长边 width(默认 256,上限 512)的 JPEG 并做内存 LRU 缓存;响应带长缓存头
// @Tags         文件管理
// @Produce      image/jpeg
// @Param        id     path      uint64  true  "文件ID"
// @Param        width  query     int     false "最长边像素(默认256,上限512)"
// @Security     BearerAuth
// @Success      200  {file}  file  "JPEG 缩略图"
// @Failure      400  {object}  app.Response  "非图片文件/无法解码"
// @Router       /files/{id}/thumbnail [get]
func (h *FileHandler) Thumbnail(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	width, _ := strconv.Atoi(c.DefaultQuery("width", "256"))
	data, contentType, err := h.svc.Thumb(c.Request.Context(), id, width)
	if err != nil {
		app.Error(c, err)
		return
	}
	// 文件内容不可变(物理文件随机键、重命名不动内容):长缓存安全。
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, contentType, data)
}

// Download handles GET /api/v1/files/:id/download — attachment download.
// @Summary      下载文件
// @Description  以附件形式下载文件(Content-Disposition: attachment)
// @Tags         文件管理
// @Accept       json
// @Produce      application/octet-stream
// @Param        id   path      uint64  true  "文件ID"
// @Security     BearerAuth
// @Success      200  {file}  file  "文件内容"
// @Router       /files/{id}/download [get]
func (h *FileHandler) Download(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	file, reader, err := h.svc.OpenFile(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	defer reader.Close()

	mimeType := ""
	if file.MimeType != nil {
		mimeType = *file.MimeType
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Header("Content-Type", mimeType)
	// 内容不可变(随机键、重命名不动内容):长缓存安全,与缩略图一致。
	c.Header("Cache-Control", "public, max-age=86400")
	// RFC 5987 UTF-8 文件名转义(与 FileAttachment 等效)。
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	http.ServeContent(c.Writer, c.Request, file.Name, file.CreatedAt, reader)
}

// Preview handles GET /api/v1/files/:id/preview — inline content streaming.
// @Summary      预览文件
// @Description  以内联方式输出文件内容,支持 Range 分段请求(视频拖动/续传)
// @Tags         文件管理
// @Accept       json
// @Produce      application/octet-stream
// @Param        id   path      uint64  true  "文件ID"
// @Security     BearerAuth
// @Success      200  {file}  file  "文件内容"
// @Router       /files/{id}/preview [get]
func (h *FileHandler) Preview(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	file, reader, err := h.svc.OpenFile(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	defer reader.Close()

	// 安全修复（存储型 XSS 纵深防御）：
	//  1. 恒加 X-Content-Type-Options: nosniff —— 阻止浏览器按内容二次嗅探
	//     （否则即使 Content-Type 为 text/plain，含 HTML 的内容仍可能被当作
	//     HTML 渲染执行脚本）。
	//  2. 恒加 CSP default-src 'none' —— 即便内联渲染，也禁止加载/执行任何
	//     子资源与脚本。
	//  3. 活动内容（text/html / image/svg+xml / text/javascript 等）或服务端
	//     无法确认安全的类型，一律 Content-Disposition: attachment 强制下载，
	//     绝不内联渲染。
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	// 内容不可变(随机键、重命名不动内容):长缓存安全,与缩略图一致。
	c.Header("Cache-Control", "public, max-age=86400")

	mimeType := ""
	if file.MimeType != nil {
		mimeType = *file.MimeType
	}
	if mimeType == "" || !util.IsInlineSafeMime(mimeType) {
		// 活动内容/未知类型：强制下载，禁止内联执行。
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, util.SanitizeFilename(file.Name)))
		http.ServeContent(c.Writer, c.Request, file.Name, file.CreatedAt, reader)
		return
	}

	// 安全类型（图片/视频/文档等）内联渲染，但仍带 nosniff + CSP。
	c.Header("Content-Type", mimeType)
	http.ServeContent(c.Writer, c.Request, file.Name, file.CreatedAt, reader)
}
