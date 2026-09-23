package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/storage"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 发布物管理（上传 / 发布 / 撤回 / 删除 / 设备下载解析）─────────────────
//
// 与升级编排分开成两个服务：编排关心「谁该升到哪」，发布物关心「有哪些二进制可用」。
// 合成一个会逼着资源上传路径也持有任务/尝试仓储。

// AgentReleaseRepository 是发布物的写面与查面。
type AgentReleaseRepository interface {
	Create(ctx context.Context, rel *entity.AgentRelease) error
	FindByID(ctx context.Context, id uint64) (*entity.AgentRelease, error)
	FindByPlatform(ctx context.Context, version, os, arch string, onlyPublished bool) (*entity.AgentRelease, error)
	List(ctx context.Context) ([]entity.AgentRelease, error)
	ListByVersion(ctx context.Context, version string) ([]entity.AgentRelease, error)
	PublishedVersions(ctx context.Context) ([]string, error)
	SetStatus(ctx context.Context, id uint64, status int8, publishedAt *time.Time) error
	Delete(ctx context.Context, id uint64) error
}

// AgentReleaseUsageChecker 回答「这个版本有没有被升级尝试用过」（回滚余量守卫）。
type AgentReleaseUsageChecker interface {
	VersionWasUsed(ctx context.Context, version string) (bool, error)
}

// 允许上传的平台。**收紧而不是放任**：写错一个字母的 arch（`amd46`）不会报错，
// 只会让「这台设备永远升不上去」——而现象与「没有产物」一模一样，极难排查。
// 新增平台 = 在这里加一行（agent 侧的自替换支持另算：Windows 覆盖不了运行中的 exe）。
var (
	releaseOSAllowed   = map[string]bool{"linux": true}
	releaseArchAllowed = map[string]bool{"amd64": true, "arm64": true}
)

// AgentReleaseService 管理发布物。
type AgentReleaseService struct {
	repo   AgentReleaseRepository
	usage  AgentReleaseUsageChecker
	cfg    AgentConfigGetter
	log    logger.LoggerInterface
	local  storage.Backend
	remote storage.Backend
	// defaultType 是**新上传**走的后端（local / s3）；读取仍按行上的 StorageType 路由。
	defaultType string
}

func NewAgentReleaseService(repo AgentReleaseRepository, usage AgentReleaseUsageChecker,
	cfg AgentConfigGetter, log logger.LoggerInterface) *AgentReleaseService {
	return newAgentReleaseService(repo, usage, cfg, log, nil, "local")
}

// NewAgentReleaseServiceWithRemoteS3 构造以对象存储为默认后端的发布物服务。
// 本地盘后端仍保留：历史（StorageType=local）的发布物必须还能被设备下载。
func NewAgentReleaseServiceWithRemoteS3(repo AgentReleaseRepository, usage AgentReleaseUsageChecker,
	cfg AgentConfigGetter, log logger.LoggerInterface, remote storage.Backend) *AgentReleaseService {
	return newAgentReleaseService(repo, usage, cfg, log, remote, "s3")
}

func newAgentReleaseService(repo AgentReleaseRepository, usage AgentReleaseUsageChecker,
	cfg AgentConfigGetter, log logger.LoggerInterface, remote storage.Backend,
	defaultType string) *AgentReleaseService {
	return &AgentReleaseService{repo: repo, usage: usage, cfg: cfg, log: log,
		local: storage.NewLocal(), remote: remote, defaultType: defaultType}
}

// maxSize 返回上传上限（sys.agent.release.maxSize，默认 200MB）。
//
// 与文件模块的 sys.file.upload.maxSize **分开**：后者的默认 10MB 拦不住
// 15–25MB 的静态 Go 二进制，而报错会指向文件模块的限制，与 agent 升级毫无字面关系。
func (s *AgentReleaseService) maxSize(ctx context.Context) int64 {
	v := s.cfg.GetInt(ctx, ConfigReleaseMaxSize, DefaultReleaseMaxSize)
	if v <= 0 {
		return DefaultReleaseMaxSize
	}
	return int64(v)
}

// List 返回发布物列表（含「能不能删」的结论与已发布版本号）。
func (s *AgentReleaseService) List(ctx context.Context) (*response.AgentReleaseListResp, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	versions, err := s.repo.PublishedVersions(ctx)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	// 「用过即禁删」逐版本问一次（发布物数量是「每版本×平台几行」，可接受）。
	usedCache := map[string]bool{}
	items := make([]response.AgentReleaseItem, 0, len(rows))
	for i := range rows {
		rel := rows[i]
		used, ok := usedCache[rel.Version]
		if !ok {
			used, err = s.usage.VersionWasUsed(ctx, rel.Version)
			if err != nil {
				return nil, apperror.Internal("内部错误", err)
			}
			usedCache[rel.Version] = used
		}
		items = append(items, response.AgentReleaseItem{
			ID: rel.ID, Version: rel.Version, OS: rel.OS, Arch: rel.Arch,
			FileName: rel.FileName, SizeBytes: rel.SizeBytes, SHA256: rel.SHA256,
			Status: rel.Status, Notes: rel.Notes,
			CreatedAt:   rel.CreatedAt.Unix(),
			PublishedAt: unixPtr(rel.PubAt),
			Deletable:   !used,
		})
	}
	return &response.AgentReleaseListResp{List: items, PublishedVersions: versions}, nil
}

// Upload 接收一个发布物（草稿态）。
//
// src 是文件流、declaredSize 是声明大小（来自 multipart 头）：
//   - declaredSize 先于读取做一次拒绝（省掉把 200MB 读进限流器的开销）；
//   - 真正的上限由 storage 后端在写入时强制（声明大小可以被伪造）；
//   - **sha256 边写边算**（TeeReader）：摘要只信服务端算出来的，
//     不接受客户端自报 —— 它是设备替换二进制前唯一的完整性依据。
func (s *AgentReleaseService) Upload(ctx context.Context, version, osName, arch, notes string,
	src io.Reader, declaredSize int64) (*response.AgentReleaseItem, error) {
	if !agentproto.IsSemver(version) {
		return nil, apperror.BadRequest("版本号格式不正确")
	}
	if !releaseOSAllowed[osName] {
		return nil, apperror.BadRequest("不支持该系统")
	}
	if !releaseArchAllowed[arch] {
		return nil, apperror.BadRequest("不支持该架构")
	}
	limit := s.maxSize(ctx)
	if declaredSize > limit {
		return nil, apperror.BadRequest(fmt.Sprintf("程序包超过 %d MB 的上限", limit/1024/1024))
	}
	// 同 (版本, 平台) 只允许一份：重复上传多半是「以为传失败了又传一次」，
	// 静默留下两份会让「设备到底下载哪一份」变得不确定。
	if _, err := s.repo.FindByPlatform(ctx, version, osName, arch, false); err == nil {
		return nil, apperror.Conflict("该版本在该平台上已存在，请先删除原文件")
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, apperror.Internal("内部错误", err)
	}

	key, err := s.newStorageKey(version, osName, arch)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	backend, err := s.backendFor(s.defaultType)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	// Put 用的是**后端要的最终 key**：本地盘的 key 是绝对路径（要把上传根目录拼上），
	// 而库里存的是相对 key（与文件模块同一约定，换存储后端时行不用改）。
	// 这两者混用过的症状是「上传成功、下载 500 程序文件不可用」——文件写到了
	// 进程工作目录下，而读取时去上传根目录找。
	hasher := sha256.New()
	n, err := backend.Put(ctx, s.putKey(ctx, key), io.TeeReader(src, hasher), limit)
	if err != nil {
		if errors.Is(err, storage.ErrTooLarge) {
			return nil, apperror.BadRequest(fmt.Sprintf("程序包超过 %d MB 的上限", limit/1024/1024))
		}
		return nil, apperror.Internal("保存程序包失败", err)
	}
	if n == 0 {
		return nil, apperror.BadRequest("程序包为空")
	}

	rel := &entity.AgentRelease{
		Version: version, OS: osName, Arch: arch,
		FileName:  fmt.Sprintf("uni_agent_%s_%s_%s", version, osName, arch),
		SizeBytes: n, SHA256: hex.EncodeToString(hasher.Sum(nil)),
		StorageType: s.defaultType, StorageKey: key,
		Status: entity.AgentReleaseDraft, Notes: notes,
	}
	if err := s.repo.Create(ctx, rel); err != nil {
		// 落库失败要把已写入的对象删掉：否则存储里留下一份没有任何记录指向的
		// 孤儿文件（它永远不会被下载，也永远不会被清理）。
		if delErr := backend.Delete(ctx, key); delErr != nil {
			s.log.Warn("orphan release object cleanup failed", zap.String("key", key), zap.Error(delErr))
		}
		if database.IsDuplicateKey(err) {
			return nil, apperror.Conflict("该版本在该平台上已存在，请先删除原文件")
		}
		return nil, apperror.Internal("登记程序包失败", err)
	}
	s.log.Info("agent release uploaded", zap.String("version", version),
		zap.String("os", osName), zap.String("arch", arch), zap.Int64("size", n))
	return &response.AgentReleaseItem{
		ID: rel.ID, Version: rel.Version, OS: rel.OS, Arch: rel.Arch,
		FileName: rel.FileName, SizeBytes: rel.SizeBytes, SHA256: rel.SHA256,
		Status: rel.Status, Notes: rel.Notes, CreatedAt: rel.CreatedAt.Unix(),
		Deletable: true,
	}, nil
}

// Publish 把草稿置为已发布（此后才能被选为目标版本）。
func (s *AgentReleaseService) Publish(ctx context.Context, id uint64) error {
	rel, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if rel.Status == entity.AgentReleasePublished {
		return nil // 幂等：重复点发布不是错误
	}
	now := time.Now()
	if err := s.repo.SetStatus(ctx, id, entity.AgentReleasePublished, &now); err != nil {
		return apperror.Internal("发布失败", err)
	}
	s.log.Info("agent release published", zap.String("version", rel.Version),
		zap.String("os", rel.OS), zap.String("arch", rel.Arch))
	return nil
}

// Unpublish 撤回发布。
//
// **只影响新下发**：已指向该版本的设备仍可下载（下载路径不校验 status）——
// 撤回若同时切断下载，就会打断正在进行的升级，把一次「别再选了」变成一次故障。
func (s *AgentReleaseService) Unpublish(ctx context.Context, id uint64) error {
	rel, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if rel.Status != entity.AgentReleasePublished {
		return nil
	}
	if err := s.repo.SetStatus(ctx, id, entity.AgentReleaseDraft, nil); err != nil {
		return apperror.Internal("撤回失败", err)
	}
	return nil
}

// Delete 删除一份发布物（行 + 存储对象）。
//
// **回滚余量是硬约束**：该版本在升级尝试里出现过（作为目标或起始版本）就拒绝删除
// —— 用过即禁删。理由：产物一旦被删，「回滚」就是一句空话，而且往往是在最需要它
// 的时候才发现（设备升坏了、要把目标设回旧版本，而旧版本的程序包已经没了）。
func (s *AgentReleaseService) Delete(ctx context.Context, id uint64) error {
	rel, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	used, err := s.usage.VersionWasUsed(ctx, rel.Version)
	if err != nil {
		return apperror.Internal("内部错误", err)
	}
	if used {
		// 文案只讲结论：不出现「attempt」「from_version」等内部名。
		return apperror.Conflict("该版本已被使用，需保留以便回滚")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.NotFound("程序包不存在")
		}
		return apperror.Internal("删除失败", err)
	}
	if backend, err := s.backendFor(rel.StorageType); err == nil {
		key := s.resolveKey(ctx, rel)
		if err := backend.Delete(ctx, key); err != nil {
			// 行已删、对象没删成功：留日志，不再回滚数据库（回滚会让「已删」变成
			// 不确定状态，而孤儿对象的代价只是磁盘空间）。
			s.log.Warn("release object delete failed", zap.String("key", key), zap.Error(err))
		}
	}
	s.log.Info("agent release deleted", zap.String("version", rel.Version))
	return nil
}

// OpenForDownload 按**设备平台**解析该版本的产物并打开读句柄（agent 下载端点用）。
//
// 刻意**不校验 status**：撤回只影响新下发，已经指向该版本的设备要继续能拉到 ——
// 否则一次撤回会打断正在进行的升级（见 Unpublish 的说明）。
func (s *AgentReleaseService) OpenForDownload(ctx context.Context, dev *entity.Device,
	version string) (io.ReadSeekCloser, *entity.AgentRelease, error) {
	if !agentproto.IsSemver(version) {
		return nil, nil, apperror.NotFound("没有该设备可用的程序文件")
	}
	rel, err := s.repo.FindByPlatform(ctx, version, dev.OS, dev.Arch, false)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil, apperror.NotFound("没有该设备可用的程序文件")
		}
		return nil, nil, apperror.Internal("内部错误", err)
	}
	backend, err := s.backendFor(rel.StorageType)
	if err != nil {
		return nil, nil, apperror.Internal("内部错误", err)
	}
	rc, err := backend.Open(ctx, s.resolveKey(ctx, rel))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// 行在、对象没了：这是数据不一致，按 500 处理并记日志（不是「没这个版本」，
			// 那会让运维去查版本号，而真相是存储里少了文件）。
			s.log.Error("release object missing", zap.String("key", rel.StorageKey),
				zap.String("version", rel.Version))
			return nil, nil, apperror.Internal("程序文件不可用")
		}
		return nil, nil, apperror.Internal("内部错误", err)
	}
	return rc, rel, nil
}

func (s *AgentReleaseService) find(ctx context.Context, id uint64) (*entity.AgentRelease, error) {
	rel, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.NotFound("程序包不存在")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	return rel, nil
}

// backendFor 依据存储类型返回对应后端。
func (s *AgentReleaseService) backendFor(storageType string) (storage.Backend, error) {
	if storageType == "s3" {
		if s.remote == nil {
			return nil, fmt.Errorf("storage: s3 后端未配置")
		}
		return s.remote, nil
	}
	return s.local, nil
}

// putKey 是写入时用的最终 key（规则与 resolveKey 完全一致）。
//
// 抽成独立函数而不是复用 resolveKey：后者的入参是**实体**，写入时还没有实体
// （摘要与大小要等写完才知道）。但两条路径必须同规则 —— 不一致就会「存进去、取不出来」。
func (s *AgentReleaseService) putKey(ctx context.Context, relKey string) string {
	if s.defaultType == "s3" {
		return relKey
	}
	return filepath.Join(s.cfg.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath), relKey)
}

// resolveKey 把行上的相对 key 解析成后端要的最终 key（与文件模块同款：
// local 需要拼上传根目录，s3 直接用）。
func (s *AgentReleaseService) resolveKey(ctx context.Context, rel *entity.AgentRelease) string {
	if rel.StorageType == "s3" {
		return rel.StorageKey
	}
	return filepath.Join(s.cfg.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath), rel.StorageKey)
}

// newStorageKey 生成存储键：`releases/<版本>/<os>-<arch>-<随机后缀>`。
//
// 带随机后缀而不是定值：本地后端的 Put 用 O_EXCL（拒绝覆盖），若上一次删除时
// 对象没删干净，定值 key 会让重新上传直接失败，而错误信息只会说「文件已存在」。
func (s *AgentReleaseService) newStorageKey(version, osName, arch string) (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return filepath.Join("releases", version, fmt.Sprintf("%s-%s-%s", osName, arch, hex.EncodeToString(buf))), nil
}
