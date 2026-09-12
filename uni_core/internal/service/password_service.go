package service

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/crypto"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/passwordpolicy"

	"go.uber.org/zap"
)

// PasswordRepositoryInterface 密码域所需的数据访问面(消费方定义)。
type PasswordRepositoryInterface interface {
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
	UpdatePassword(ctx context.Context, userID uint64, newPassword string, newSalt *string) error
	SetMustChangePassword(ctx context.Context, userID uint64, mustChange bool) error
}

// PasswordService 密码域服务:改密/锁屏校验/密码策略。独立于认证登录流程。
type PasswordService struct {
	cfgProv      ConfigGetterInterface
	repo         PasswordRepositoryInterface
	sessionStore SessionStoreInterface
	logger       logger.LoggerInterface
}

// NewPasswordService constructs a PasswordService.
func NewPasswordService(cfgProv ConfigGetterInterface, repo PasswordRepositoryInterface, sessionStore SessionStoreInterface, logger logger.LoggerInterface) *PasswordService {
	return &PasswordService{cfgProv: cfgProv, repo: repo, sessionStore: sessionStore, logger: logger}
}

func (s *PasswordService) cfgInt(ctx context.Context, key string, def int) int {
	if s.cfgProv == nil {
		return def
	}
	return s.cfgProv.GetInt(ctx, key, def)
}

// policy 组装当前生效策略(来自 sys.auth.password.*,热更;cfgProv 为空回退默认)。
// 读取配置沿用请求 ctx,保持 tracing/cancellation 链路不断。
func (s *PasswordService) policy(ctx context.Context) passwordpolicy.Policy {
	return passwordpolicy.Policy{
		MinLength:                s.cfgInt(ctx, "sys.auth.password.minLength", 8),
		MinCategories:            s.cfgInt(ctx, "sys.auth.password.minCategories", 3),
		ForbidContainingUsername: s.cfgProv != nil && s.cfgProv.GetBool(ctx, "sys.auth.password.forbidContainingUsername", true),
	}
}

// ChangePassword 修改当前用户密码:校验强度、更新密码、吊销会话、清除改密提醒。
func (s *PasswordService) ChangePassword(ctx context.Context, req *request.ChangePasswordReq, accessToken string) error {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return apperror.Unauthorized("未登录")
	}
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return translateNotFound(err, "用户不存在")
	}
	salt := ""
	if user.PasswordSalt != nil {
		salt = *user.PasswordSalt
	}
	if !crypto.VerifyPassword(req.OldPassword, user.Password, salt) {
		return apperror.BadRequest("旧密码不正确")
	}
	if err := s.policy(ctx).Validate(req.NewPassword, user.Username); err != nil {
		return apperror.BadRequest(err.Error())
	}
	hashed, newSalt, err := crypto.HashPassword(req.NewPassword, s.cfgInt(ctx, "sys.auth.bcryptCost", 10))
	if err != nil {
		return apperror.Internal("密码加密失败", err)
	}
	if err := s.repo.UpdatePassword(ctx, userID, string(hashed), &newSalt); err != nil {
		return err
	}
	if err := s.repo.SetMustChangePassword(ctx, userID, false); err != nil {
		s.logger.Warn("failed to clear must-change flag", zap.Uint64("userId", userID), zap.Error(err))
	}
	if s.sessionStore != nil {
		if err := s.sessionStore.RevokeAll(ctx, userID, accessToken); err != nil {
			s.logger.Error("failed to revoke sessions after password change", zap.Uint64("userId", userID), zap.Error(err))
		}
	}
	return nil
}

// VerifyPassword 校验当前用户登录密码(锁屏解锁),不触碰会话与令牌。
func (s *PasswordService) VerifyPassword(ctx context.Context, req *request.VerifyPasswordReq) (*response.VerifyPasswordResp, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return nil, apperror.Unauthorized("未登录")
	}
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, translateNotFound(err, "用户不存在")
	}
	salt := ""
	if user.PasswordSalt != nil {
		salt = *user.PasswordSalt
	}
	if !crypto.VerifyPassword(req.Password, user.Password, salt) {
		return nil, apperror.BadRequest("密码不正确")
	}
	return &response.VerifyPasswordResp{Valid: true}, nil
}
