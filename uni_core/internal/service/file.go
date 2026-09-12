package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/storage"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
)

// ── 上传参数默认值:与 v006 的 sys_config 种子值一致,配置读取失败时的兜底 ──
const (
	defaultFileUploadPath    = "./uploads"
	defaultFileUploadMaxSize = 10 << 20 // 10MB
)

// FileRepositoryInterface 文件服务所需的数据访问方法集(消费方接口)。
type FileRepositoryInterface interface {
	FindPage(ctx context.Context, q *request.FileQuery) ([]entity.SysFile, int64, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysFile, error)
	FindByIDs(ctx context.Context, ids []uint64) ([]entity.SysFile, error)
	CreateBatch(ctx context.Context, files []*entity.SysFile) error
	Rename(ctx context.Context, id uint64, name string) error
	DeleteByIDs(ctx context.Context, ids []uint64) error
	Stats(ctx context.Context, weekStart time.Time) (*entity.FileStatsRow, error)
}

// FileConfigProvider 上传参数配置读取接口(消费方定义,由 ConfigService 实现)。
type FileConfigProvider interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
}

// FileService 文件管理服务:元数据 CRUD + 存储后端的读写路由。
type FileService struct {
	repo        FileRepositoryInterface
	cfg         FileConfigProvider
	logger      logger.LoggerInterface
	thumbCache  *thumbLRU
	local       storage.Backend // 本地盘后端,恒存在
	remote      storage.Backend // S3 兼容对象存储后端,未配置时为 nil
	defaultType string          // 新上传默认存储类型: local | s3
}

// NewFileService constructs a FileService with the given dependencies.
// 默认使用本地盘后端(向后兼容);对象存储见 NewFileServiceWithRemoteS3。
func NewFileService(repo FileRepositoryInterface, cfg FileConfigProvider, logger logger.LoggerInterface) *FileService {
	return newFileService(repo, cfg, logger, nil, "local")
}

// NewFileServiceWithRemoteS3 构造以 S3 兼容对象存储为默认后端的文件服务。
// 本地盘后端仍保留,用于读取历史 StorageType=local 的文件(混合存储切换期间)。
func NewFileServiceWithRemoteS3(repo FileRepositoryInterface, cfg FileConfigProvider, logger logger.LoggerInterface, remote storage.Backend) *FileService {
	return newFileService(repo, cfg, logger, remote, "s3")
}

func newFileService(repo FileRepositoryInterface, cfg FileConfigProvider, logger logger.LoggerInterface, remote storage.Backend, defaultType string) *FileService {
	return &FileService{
		repo:        repo,
		cfg:         cfg,
		logger:      logger,
		thumbCache:  newThumbLRU(),
		local:       storage.NewLocal(),
		remote:      remote,
		defaultType: defaultType,
	}
}

// List 分页查询文件列表。
func (s *FileService) List(ctx context.Context, q *request.FileQuery) (*app.PageResponse, error) {
	files, total, err := s.repo.FindPage(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]response.FileResp, 0, len(files))
	for _, f := range files {
		items = append(items, toFileResp(f))
	}
	return app.NewPageResponse(items, total, q.GetPage(), q.GetPageSize()), nil
}

// Stats 返回文件概览统计(总数/总大小/近7天新增/分类计数)。
func (s *FileService) Stats(ctx context.Context) (*response.FileStatsResp, error) {
	row, err := s.repo.Stats(ctx, time.Now().AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	return &response.FileStatsResp{
		Total:          row.Total,
		TotalSize:      row.TotalSize,
		WeekUploads:    row.WeekUploads,
		CategoryCounts: row.CategoryCounts(),
	}, nil
}

// Upload 校验并落盘上传文件,随后批量写入元数据。
// 所有文件先整体校验(大小/扩展名),再逐个落盘;任一步失败回滚已写入的物理文件,
// 保证磁盘与数据库一致。物理文件名随机生成,杜绝路径注入与同名覆盖。
func (s *FileService) Upload(ctx context.Context, headers []*multipart.FileHeader) ([]response.FileResp, error) {
	if len(headers) == 0 {
		return nil, apperror.BadRequest("未接收到文件")
	}

	maxSize := s.cfg.GetInt(ctx, "sys.file.upload.maxSize", defaultFileUploadMaxSize)
	allowedExts := util.ParseExtSet(s.cfg.GetString(ctx, "sys.file.upload.allowedExts", ""))
	basePath := s.cfg.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath)

	type pendingFile struct {
		header *multipart.FileHeader
		ext    string
		mime   string
	}
	pending := make([]pendingFile, 0, len(headers))
	for _, h := range headers {
		if h.Size > int64(maxSize) {
			return nil, apperror.BadRequest(fmt.Sprintf("%s 超过上传大小限制(%s)", h.Filename, util.FormatBytes(int64(maxSize))))
		}
		ext := strings.ToLower(filepath.Ext(h.Filename))
		if len(allowedExts) > 0 && !allowedExts[ext] {
			return nil, apperror.BadRequest(fmt.Sprintf("%s 的文件类型 %s 不在允许上传范围内", h.Filename, ext))
		}
		pending = append(pending, pendingFile{header: h, ext: ext, mime: detectFileMime(h)})
	}

	// 选择本次上传使用的后端与存储类型:默认本地盘,配置为 s3 时切对象存储。
	backend := s.local
	storageType := "local"
	if s.defaultType == "s3" && s.remote != nil {
		backend = s.remote
		storageType = "s3"
	}

	dateDir := time.Now().Format("2006/01")
	savedKeys := make([]string, 0, len(pending))
	rollback := func() {
		for _, key := range savedKeys {
			_ = backend.Delete(ctx, key)
		}
	}

	files := make([]*entity.SysFile, 0, len(pending))
	for _, p := range pending {
		relKey := filepath.Join(dateDir, randomFileKey()+p.ext)
		// 后端相关定位键:local 用绝对路径,s3 用对象键。
		key := relKey
		if storageType == "local" {
			key = filepath.Join(basePath, relKey)
		}

		src, err := p.header.Open()
		if err != nil {
			rollback()
			return nil, apperror.BadRequest("读取上传文件失败")
		}
		n, err := backend.Put(ctx, key, src, int64(maxSize))
		_ = src.Close()
		if err != nil {
			rollback()
			if errors.Is(err, storage.ErrTooLarge) {
				return nil, apperror.BadRequest(fmt.Sprintf("%s 超过上传大小限制(%s)", p.header.Filename, util.FormatBytes(int64(maxSize))))
			}
			s.logger.Warn("failed to store upload", zap.String("filename", p.header.Filename), zap.Error(err))
			return nil, apperror.Internal("写入上传文件失败", err)
		}

		savedKeys = append(savedKeys, key)
		files = append(files, &entity.SysFile{
			Name:         p.header.Filename,
			OriginalName: p.header.Filename,
			Path:         relKey,
			Size:         util.Ptr(n),
			MimeType:     util.Ptr(p.mime),
			Ext:          util.Ptr(p.ext),
			StorageType:  storageType,
		})
	}

	if err := s.repo.CreateBatch(ctx, files); err != nil {
		rollback()
		return nil, err
	}

	items := make([]response.FileResp, 0, len(files))
	for _, f := range files {
		items = append(items, toFileResp(*f))
	}
	return items, nil
}

// Rename 更新文件显示名(Trim 后校验,不改变物理路径与扩展名)。
func (s *FileService) Rename(ctx context.Context, id uint64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return apperror.BadRequest("文件名不能为空")
	}
	if strings.ContainsAny(name, `/\`) {
		return apperror.BadRequest("文件名不能包含路径分隔符")
	}
	file, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if file == nil {
		return apperror.NotFound("文件不存在")
	}
	return s.repo.Rename(ctx, id, name)
}

// DeleteMany 批量删除文件元数据并清理本地磁盘文件。
//
// 安全修复（评审 #16）：先删元数据、后删物理文件。此前先 os.Remove 物理文件
// 再 DeleteByIDs 删元数据 —— 若 DB 删除失败，物理文件已丢失而元数据仍指向
// 不存在的磁盘文件（坏元数据残留）。改为先删元数据：DB 删除失败时物理文件
// 原样保留（至多产生无元数据引用的孤儿文件，可后续清理，方向更安全）；元数据
// 删除成功后再清理物理文件，失败仅告警不阻断（元数据已删，物理残留无害）。
func (s *FileService) DeleteMany(ctx context.Context, ids []uint64) error {
	files, err := s.repo.FindByIDs(ctx, ids)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return apperror.NotFound("文件不存在")
	}

	// 先删元数据（真相源），成功后再清理物理对象（本地盘/对象存储）。
	if err := s.repo.DeleteByIDs(ctx, ids); err != nil {
		s.logger.Warn("delete file metadata failed", zap.Int("count", len(files)), zap.Error(err))
		return err
	}

	for i := range files {
		f := &files[i]
		backend, err := s.backendFor(f.StorageType)
		if err != nil {
			s.logger.Warn("skip removing file (unsupported backend)",
				zap.Uint64("fileId", f.ID), zap.Error(err))
			continue
		}
		if err := backend.Delete(ctx, s.resolveKey(ctx, f)); err != nil {
			s.logger.Warn("remove file failed", zap.String("path", f.Path), zap.Error(err))
		}
	}
	return nil
}

// OpenFile 打开文件用于下载/预览,返回实体与可随机读取的内容句柄。
// 依文件 StorageType 路由到本地盘或对象存储后端;对象缺失映射为 404。
func (s *FileService) OpenFile(ctx context.Context, id uint64) (*entity.SysFile, io.ReadSeekCloser, error) {
	file, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if file == nil {
		return nil, nil, apperror.NotFound("文件不存在")
	}
	backend, err := s.backendFor(file.StorageType)
	if err != nil {
		return nil, nil, err
	}
	reader, err := backend.Open(ctx, s.resolveKey(ctx, file))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, apperror.NotFound("文件已丢失")
		}
		return nil, nil, err
	}
	return file, reader, nil
}

// backendFor 依据文件存储类型返回对应后端。
func (s *FileService) backendFor(storageType string) (storage.Backend, error) {
	switch storageType {
	case "", "local":
		return s.local, nil
	case "s3":
		if s.remote == nil {
			return nil, apperror.Internal("对象存储后端未配置")
		}
		return s.remote, nil
	default:
		return nil, apperror.Internal(fmt.Sprintf("存储类型 %s 暂不支持在线访问", storageType))
	}
}

// resolveKey 返回给定文件在其所属后端的定位键:
// local → 上传根目录下的绝对路径;s3 → bucket 内对象键。
func (s *FileService) resolveKey(ctx context.Context, file *entity.SysFile) string {
	if file.StorageType == "s3" {
		return file.Path
	}
	return filepath.Join(s.cfg.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath), file.Path)
}

// ── 工具函数 ──────────────────────────────────────────────────────────

func toFileResp(f entity.SysFile) response.FileResp {
	resp := response.FileResp{
		ID:           f.ID,
		Name:         f.Name,
		OriginalName: f.OriginalName,
		StorageType:  f.StorageType,
		CreatedAt:    f.CreatedAt,
		UpdatedAt:    f.UpdatedAt,
	}
	if f.Size != nil {
		resp.Size = *f.Size
	}
	if f.MimeType != nil {
		resp.MimeType = *f.MimeType
	}
	if f.Ext != nil {
		resp.Ext = *f.Ext
		resp.Category = entity.CategoryOfExt(*f.Ext)
	} else {
		resp.Category = entity.FileCategoryOther
	}
	if f.CreatedBy != nil {
		resp.CreatedBy = *f.CreatedBy
	}
	return resp
}

// randomFileKey 生成 16 字节随机 hex 存储键(crypto/rand,零额外依赖)。
func randomFileKey() string {
	key, err := util.RandomHex(16)
	if err != nil {
		// crypto/rand 失败属极端情形:时间戳兜底,保证可用性优先。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return key
}

// saveMultipart 将 multipart 文件写入目标路径,双重校验大小上限。
func saveMultipart(header *multipart.FileHeader, dstPath string, maxSize int64) (int64, error) {
	src, err := header.Open()
	if err != nil {
		return 0, apperror.BadRequest("读取上传文件失败")
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return 0, apperror.Internal("写入上传文件失败", err)
	}
	// 一旦文件创建成功,后续任何失败都必须删除已写文件:O_EXCL 建出的
	// 半成品/超限文件若残留,调用方的 rollback 只会清理「已登记 savedKeys」,
	// 未登记的本文件会永久残留为孤儿(磁盘泄漏)。故在此统一兜底清理。
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(dstPath)
		}
	}()

	n, copyErr := io.Copy(dst, io.LimitReader(src, maxSize+1))
	closeErr := dst.Close()
	if copyErr != nil {
		return 0, apperror.Internal("写入上传文件失败", copyErr)
	}
	if closeErr != nil {
		return 0, apperror.Internal("写入上传文件失败", closeErr)
	}
	if n > maxSize {
		return 0, apperror.BadRequest(fmt.Sprintf("%s 超过上传大小限制(%s)", header.Filename, util.FormatBytes(maxSize)))
	}
	removeOnError = false
	return n, nil
}

// detectFileMime 根据文件真实内容（魔数）探测 MIME 类型，而不是取信客户端
// multipart 声明的 Content-Type。
//
// 安全背景：此前实现直接返回客户端可任意伪造的 Content-Type（或按扩展名
// 回退为 text/html），攻击者可上传名为 *.png 的 HTML 文件并以 text/html
// 内联渲染执行同源脚本（存储型 XSS）。改为读文件头魔数后，Content-Type 由
// 服务端依据真实字节决定，客户端不可控。
//
// 实现：打开文件读前 512 字节调用 http.DetectContentType 嗅探。嗅探失败
// （空文件/未知类型）时按扩展名回退，但绝不回退到 text/html 等活动内容。
func detectFileMime(header *multipart.FileHeader) string {
	// 优先按真实内容嗅探；读不到则回退扩展名（保守降级为 octet-stream）。
	if mimeType := sniffFileMime(header); mimeType != "" {
		return mimeType
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if mimeType := mime.TypeByExtension(ext); mimeType != "" && util.IsInlineSafeMime(mimeType) {
		return mimeType
	}
	// 无法确定为安全类型时，一律按二进制流处理，阻止内联脚本执行。
	return "application/octet-stream"
}

// sniffFileMime 打开 multipart 文件读前 512 字节做内容嗅探；失败返回空串。
func sniffFileMime(header *multipart.FileHeader) string {
	src, err := header.Open()
	if err != nil {
		return ""
	}
	defer src.Close()
	buf := make([]byte, 512)
	n, err := io.ReadFull(src, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return ""
	}
	if n == 0 {
		return ""
	}
	return http.DetectContentType(buf[:n])
}
