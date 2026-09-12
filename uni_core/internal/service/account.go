package service

import (
	"context"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
)

func (s *AuthService) GetUserInfo(ctx context.Context) (*response.UserInfoResp, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return nil, apperror.Unauthorized("未登录")
	}
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, translateNotFound(err, "用户不存在")
	}
	roleCodes, err := s.repo.GetRoleCodes(ctx, userID)
	if err != nil {
		s.logger.Warn("failed to load role codes", zap.Uint64("userId", userID), zap.Error(err))
		roleCodes = []string{}
	}
	perms, err := s.GetUserPermissions(ctx, userID)
	if err != nil {
		s.logger.Warn("failed to load user permissions", zap.Uint64("userId", userID), zap.Error(err))
		perms = []string{}
	}

	resp := &response.UserInfoResp{}
	util.CopyEntity(resp, user, s.logger)
	resp.Permissions = perms
	resp.Roles = roleCodes
	if user.MustChangePassword != nil {
		resp.MustChangePassword = *user.MustChangePassword
	}

	// 授权上下文收敛:范围口径唯一来源是 ScopeContext(ScopeResolverHandler
	// 注入,与查询过滤同源)。旧实现在此无条件用 contextkeys.DataScope/DeptID
	// 覆写实体值,ScopeSelf 用户(无 dept 维度)会被覆写为 0/0 丢掉真实部门。
	// 新语义:dept 维度取解析 Level/SelfID;self 维度只定 DataScope=5,
	// DeptID 保留实体值;其余情形保持实体值。
	if sc, has := datascope.ScopeContextFromCtx(ctx); has && sc != nil {
		if d, ok := sc.Dimensions["dept"]; ok && d != nil {
			resp.DataScope = d.Level
			resp.DeptID = d.SelfID
		} else if d, ok := sc.Dimensions["self"]; ok && d != nil {
			resp.DataScope = d.Level
		}
	}
	return resp, nil
}

// ── 个人中心:资料自维护 / 头像上传 ─────────────────────────────────

// 头像上传约束:大小上限 2MB,扩展名白名单。与文件管理共用 sys.file.upload.path
// 上传根目录,头像落在其下 avatars/ 子目录(每用户一个文件,重传覆盖)。
const (
	defaultAvatarMaxSize = 2 << 20 // 2MB
	defaultAvatarDir     = "avatars"
)

// avatarAllowedExts 头像允许的图片扩展名(下载/展示均无需特殊解码)。
var avatarAllowedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
}

// UpdateProfile updates the current user's own basic profile (realName, email,
// phone). Other columns (roles/dept/status/username) are never touched here —
// those flows remain in user management.
func (s *AuthService) UpdateProfile(ctx context.Context, req *request.UpdateProfileReq) error {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return apperror.Unauthorized("未登录")
	}
	if _, err := s.repo.FindByID(ctx, userID); err != nil {
		return translateNotFound(err, "用户不存在")
	}
	// UpdateProfileReq 与 SysUser 个人资料字段同名同构:DTO → CopyEntity →
	// entity,与 UserService.UpdateUserInfo 一致。nil 指针表示"清空该字段",
	// 由 repo 的显式列更新落为 SQL NULL(不再逐个透传标量)。
	user := &entity.SysUser{}
	util.CopyEntity(user, req, s.logger)
	if err := s.repo.UpdateProfile(ctx, userID, user); err != nil {
		s.logger.Warn("failed to update profile", zap.Uint64("userId", userID), zap.Error(err))
		return err
	}
	s.logger.Info("profile updated", zap.Uint64("userId", userID))
	return nil
}

// UploadAvatar validates and stores an avatar image for the current user, then
// persists its access path. Returns e.g. "/api/v1/user/avatar/8?v=1757…".
// Files live under <upload.path>/avatars/<userID><ext> (one per user, overwrite
// on re-upload); stale files with other extensions are removed so the
// directory never accumulates orphan images.
func (s *AuthService) UploadAvatar(ctx context.Context, header *multipart.FileHeader) (string, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return "", apperror.Unauthorized("未登录")
	}
	if _, err := s.repo.FindByID(ctx, userID); err != nil {
		return "", translateNotFound(err, "用户不存在")
	}

	if header.Size > defaultAvatarMaxSize {
		return "", apperror.BadRequest("头像大小不能超过 2MB")
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !avatarAllowedExts[ext] {
		return "", apperror.BadRequest("头像仅支持 JPG/PNG/WebP/GIF 格式")
	}

	basePath := s.cfgProv.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath)
	dir := filepath.Join(basePath, defaultAvatarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.logger.Error("create avatar dir failed", zap.String("path", dir), zap.Error(err))
		return "", apperror.Internal("创建头像目录失败", err)
	}

	fileName := fmt.Sprintf("%d%s", userID, ext)
	fullPath := filepath.Join(dir, fileName)

	// 清理该用户旧扩展名残留(上次为 png、这次为 webp 时),避免孤儿文件。
	// 同时必须先删除与本次相同扩展名的旧文件:saveMultipart 以 O_EXCL 排他
	// 创建,若同名文件已存在会直接 EEXIST 失败,导致「同扩展名重传头像」必错,
	// 与「overwrite on re-upload」契约矛盾。
	if stale, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%d.*", userID))); len(stale) > 0 {
		for _, old := range stale {
			_ = os.Remove(old)
		}
	}

	if _, err := saveMultipart(header, fullPath, defaultAvatarMaxSize); err != nil {
		return "", err
	}

	// ?v=<unix> 仅作缓存破坏:同一用户 URL 前缀稳定,重传后 ?v 变化触发回源。
	// URL 含 API 前缀,同源/反代部署下 <img> 可直接引用。
	avatarURL := fmt.Sprintf("%s/user/avatar/%d?v=%d", s.apiPrefix, userID, time.Now().Unix())
	if err := s.repo.UpdateAvatar(ctx, userID, avatarURL); err != nil {
		s.logger.Warn("failed to persist avatar", zap.Uint64("userId", userID), zap.Error(err))
		return "", err
	}
	s.logger.Info("avatar uploaded", zap.Uint64("userId", userID))
	return avatarURL, nil
}

// AvatarFilePath resolves the on-disk path of the given user's avatar image.
// The user must exist; a missing image maps to a 404 so clients can fall back
// to their default asset.
func (s *AuthService) AvatarFilePath(ctx context.Context, userID uint64) (string, error) {
	if _, err := s.repo.FindByID(ctx, userID); err != nil {
		return "", translateNotFound(err, "用户不存在")
	}
	basePath := s.cfgProv.GetString(ctx, "sys.file.upload.path", defaultFileUploadPath)
	dir := filepath.Join(basePath, defaultAvatarDir)
	matches, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%d.*", userID)))
	if len(matches) == 0 {
		return "", apperror.NotFound("头像不存在")
	}
	return matches[0], nil
}

// GetUserOverview 聚合个人中心页所需的全部展示数据:身份(含部门/角色显示名)、
// 登录统计、近 30 天逐日活动序列(零填充)与最近登录尝试。活动数据尽力而为:
// 日志查询失败时降级为零值,身份区块必须始终可渲染。
func (s *AuthService) GetUserOverview(ctx context.Context) (*response.UserOverviewResp, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return nil, apperror.Unauthorized("未登录")
	}
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, translateNotFound(err, "用户不存在")
	}

	resp := &response.UserOverviewResp{
		ID:        user.ID,
		Username:  user.Username,
		CreatedAt: util.JSONTime(user.CreatedAt),
		Roles:     make([]response.RoleBriefResp, 0, len(user.Roles)),
	}
	if user.RealName != nil {
		resp.RealName = *user.RealName
	}
	if user.Email != nil {
		resp.Email = *user.Email
	}
	if user.Phone != nil {
		resp.Phone = *user.Phone
	}
	if user.Avatar != nil {
		resp.Avatar = *user.Avatar
	}
	if user.LastLoginIP != nil {
		resp.LastLoginIP = *user.LastLoginIP
	}
	if user.LastLoginTime != nil {
		t := util.JSONTime(*user.LastLoginTime)
		resp.LastLoginTime = &t
	}
	if user.DeptID != nil && *user.DeptID > 0 {
		name, err := s.repo.GetDeptName(ctx, *user.DeptID)
		if err != nil {
			s.logger.Warn("failed to load dept name for overview", zap.Uint64("userId", userID), zap.Error(err))
		}
		resp.DeptName = name
	}
	for _, r := range user.Roles {
		resp.Roles = append(resp.Roles, response.RoleBriefResp{Code: r.Code, Name: r.Name})
	}

	// 30 天窗口:含今天在内的 30 个自然日(服务器本地时区,与 login_time 一致)。
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -29)

	if total, err := s.repo.CountUserLogins(ctx, userID); err != nil {
		s.logger.Warn("failed to count user logins", zap.Uint64("userId", userID), zap.Error(err))
	} else {
		resp.Stats.TotalLogins = total
	}

	daily := make([]response.DailyActivityResp, 30)
	index := make(map[string]*response.DailyActivityResp, 30)
	for i := range daily {
		daily[i].Date = since.AddDate(0, 0, i).Format("2006-01-02")
		index[daily[i].Date] = &daily[i]
	}
	resp.DailyActivity = daily

	logs, err := s.repo.FindUserLoginLogsSince(ctx, userID, since)
	if err != nil {
		s.logger.Warn("failed to load login activity", zap.Uint64("userId", userID), zap.Error(err))
	} else {
		const recentLimit = 10 // 与前端文案「最近 10 条登录记录」保持一致
		recent := make([]response.RecentLoginResp, 0, recentLimit)
		for i := range logs {
			entry := &logs[i]
			day := index[entry.LoginTime.Format("2006-01-02")]
			if entry.Code == 0 {
				resp.Stats.Logins30d++
				if day != nil {
					day.Success++
				}
			} else {
				resp.Stats.Failed30d++
				if day != nil {
					day.Failed++
				}
			}
			if len(recent) < recentLimit {
				item := response.RecentLoginResp{
					Time: util.JSONTime(entry.LoginTime),
					Code: entry.Code,
				}
				if entry.IP != nil {
					item.IP = *entry.IP
				}
				if entry.Browser != nil {
					item.Browser = *entry.Browser
				}
				if entry.OS != nil {
					item.OS = *entry.OS
				}
				if entry.Msg != nil {
					item.Msg = *entry.Msg
				}
				recent = append(recent, item)
			}
		}
		resp.RecentLogins = recent
	}
	return resp, nil
}
