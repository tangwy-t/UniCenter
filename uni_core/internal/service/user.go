package service

import (
	"context"
	"strconv"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/crypto"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// SessionStoreInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type SessionStoreInterface interface {
	StoreAccess(ctx context.Context, token string, userID uint64, ttl time.Duration) error
	StoreRefresh(ctx context.Context, userID uint64, token string, ttl time.Duration) error
	StorePerms(ctx context.Context, userID uint64, scopeKey string, perms []string, ttl time.Duration) error
	RevokeAll(ctx context.Context, userID uint64, token string) error
	GetRefresh(ctx context.Context, userID uint64) (string, error)
	DeleteRefresh(ctx context.Context, userID uint64) error
	// RotateRefresh 原子地把 refresh token 从 old 换成 new;consumed=false
	// 表示 old 已不是当前值(已被兑换或已失效),调用方须拒绝本次刷新。
	RotateRefresh(ctx context.Context, userID uint64, old, new string, ttl time.Duration) (bool, error)
	RevokePerms(ctx context.Context, userID uint64) error
	RevokeAllPerms(ctx context.Context) error
	// StoreSessionMeta 写入 access token 的会话元数据(在线用户列表展示用)。
	StoreSessionMeta(ctx context.Context, token string, meta *session.SessionMeta, ttl time.Duration) error
}

// UserRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// UserRepositoryInterface defines the data-access contract for user management operations.
type UserRepositoryInterface interface {
	// FindPage returns a paginated list of users matching the given query, along with the total count.
	FindPage(ctx context.Context, query *request.UserQuery) ([]entity.SysUser, int64, error)
	// FindByID returns a single user (with Dept and Roles preloaded) by primary key.
	FindByID(ctx context.Context, id uint64) (*entity.SysUser, error)
	// FindByUsername returns a user by username, used for uniqueness checks.
	FindByUsername(ctx context.Context, username string) (*entity.SysUser, error)
	// CreateWithRoles creates a user and associates the given role IDs in a single transaction.
	CreateWithRoles(ctx context.Context, user *entity.SysUser, roleIDs []uint64) error
	// Delete performs a soft-delete on the user with the given ID.
	Delete(ctx context.Context, id uint64) error
	// UpdateStatus sets the status field for the given user ID.
	UpdateStatus(ctx context.Context, id uint64, status int8) error
	// UpdatePassword sets the password field for the given user ID.
	UpdatePassword(ctx context.Context, id uint64, password string, salt *string) error
	// SetMustChangePassword sets the "must change password" flag for the given user.
	SetMustChangePassword(ctx context.Context, id uint64, mustChange bool) error
	// Update updates basic user fields without touching role associations.
	Update(ctx context.Context, user *entity.SysUser) error
	// ReplaceRoles replaces all role associations for the given user ID in a single transaction.
	ReplaceRoles(ctx context.Context, id uint64, roleIDs []uint64) error
	// FindRoleCodeByID returns a role's code, used by role-membership guards.
	FindRoleCodeByID(ctx context.Context, roleID uint64) (string, error)
	// AddUsersToRole appends the given users to a role (idempotent per user).
	AddUsersToRole(ctx context.Context, roleID uint64, userIDs []uint64) error
	// RemoveUsersFromRole removes the given users from a role.
	RemoveUsersFromRole(ctx context.Context, roleID uint64, userIDs []uint64) error
	// FindExistingIDs returns the subset of userIDs that exist (association
	// phantom-row guard for role-member writes).
	FindExistingIDs(ctx context.Context, ids []uint64) ([]uint64, error)
	// FindExistingRoleIDs returns the subset of roleIDs that exist (association
	// phantom-row guard for role-assignment writes).
	FindExistingRoleIDs(ctx context.Context, ids []uint64) ([]uint64, error)
}

type UserService struct {
	repo         UserRepositoryInterface
	logger       logger.LoggerInterface
	sessionStore SessionStoreInterface
	cfgProv      ConfigGetterInterface
}

// NewUserService constructs a UserService with the given dependencies.
// Snowflake ID generation is handled by GORM callbacks (database.RegisterIDCallback).
func NewUserService(repo UserRepositoryInterface, logger logger.LoggerInterface, tokenStore SessionStoreInterface, cfgProv ConfigGetterInterface) *UserService {
	return &UserService{repo: repo, logger: logger, sessionStore: tokenStore, cfgProv: cfgProv}
}

// passwordCost 返回 bcrypt cost:优先 sys.auth.bcryptCost(热更),否则默认。
func (s *UserService) passwordCost(ctx context.Context) int {
	if s.cfgProv == nil {
		return bcrypt.DefaultCost
	}
	return s.cfgProv.GetInt(ctx, "sys.auth.bcryptCost", bcrypt.DefaultCost)
}

func (s *UserService) FindPage(ctx context.Context, query *request.UserQuery) (*app.PageResponse, error) {
	users, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		return nil, err
	}
	list := make([]response.UserResp, 0, len(users))
	for i := range users {
		list = append(list, s.toUserResp(&users[i]))
	}
	return app.NewPageResponse(list, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *UserService) FindByID(ctx context.Context, id uint64) (*response.UserResp, error) {
	user, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "用户不存在")
	}
	resp := s.toUserResp(user)
	return &resp, nil
}

// toUserResp 统一 UserResp 的组装:copier 拷贝同名字段后,补上跨表
// 关联字段(DeptName/RoleNames)。此前 FindPage 与 FindByID 各写一份,
// 新增关联字段时极易只改一处。
func (s *UserService) toUserResp(user *entity.SysUser) response.UserResp {
	resp := util.MapEntity[response.UserResp](user, s.logger)
	if user.Dept != nil {
		resp.DeptName = user.Dept.Name
	}
	roleNames := make([]string, 0, len(user.Roles))
	roleIDs := make([]string, 0, len(user.Roles))
	for _, role := range user.Roles {
		roleNames = append(roleNames, role.Name)
		roleIDs = append(roleIDs, strconv.FormatUint(role.ID, 10))
	}
	resp.RoleNames = roleNames
	resp.RoleIDs = roleIDs
	return resp
}

func (s *UserService) Create(ctx context.Context, req *request.CreateUserReq) (uint64, error) {
	hashed, salt, err := crypto.HashPassword(req.Password, s.passwordCost(ctx))
	if err != nil {
		return 0, apperror.Internal("密码加密失败", err)
	}

	// ID is auto-generated by the GORM BeforeCreate callback (database.RegisterIDCallback).
	user := &entity.SysUser{}
	// DeptID is *util.JsonUint64 in the request to preserve snowflake-ID
	// precision; copier converts it to the entity's *uint64 automatically
	// (behavior locked by util.TestCopyEntityJsonUint64Pointer).
	util.CopyEntity(user, req, s.logger)
	user.Password = hashed
	user.PasswordSalt = &salt
	mustChange := true
	user.MustChangePassword = &mustChange

	// Default status to enabled if not provided.
	if user.Status == nil {
		enabled := entity.UserStatusEnabled
		user.Status = &enabled
	}

	if err := validateIDsExist(ctx, "角色", []uint64(req.RoleIDs), s.repo.FindExistingRoleIDs); err != nil {
		return 0, err
	}
	// 安全修复（评审 #5）：新建账号时同样拦截 admin 角色授予。
	for _, roleID := range []uint64(req.RoleIDs) {
		code, err := s.repo.FindRoleCodeByID(ctx, roleID)
		if err != nil {
			return 0, translateNotFound(err, "角色不存在")
		}
		if code == "admin" {
			return 0, apperror.BadRequest("内置管理员角色不允许通过接口授予")
		}
	}

	if err := s.repo.CreateWithRoles(ctx, user, []uint64(req.RoleIDs)); err != nil {
		if database.IsDuplicateKey(err) {
			return 0, apperror.Conflict("用户名已存在")
		}
		s.logger.Warn("failed to create user", zap.String("username", req.Username), zap.Error(err))
		return 0, err
	}
	s.logger.Info("user created", zap.Uint64("userId", user.ID), zap.String("username", user.Username))
	return user.ID, nil
}

// UpdateUserInfo updates a user's basic fields only. It does not modify roles
// or status. Status changes must go through Enable/Disable (self-guarded).
func (s *UserService) UpdateUserInfo(ctx context.Context, req *request.UpdateUserReq) error {
	user, err := s.repo.FindByID(ctx, req.ID)
	if err != nil {
		return translateNotFound(err, "用户不存在")
	}

	// DeptID is *util.JsonUint64 in the request to preserve snowflake-ID
	// precision; copier converts it to the entity's *uint64 automatically
	// (behavior locked by util.TestCopyEntityJsonUint64Pointer).
	util.CopyEntity(user, req, s.logger)

	if err := s.repo.Update(ctx, user); err != nil {
		s.logger.Warn("failed to update user info", zap.Uint64("userId", req.ID), zap.Error(err))
		return err
	}
	s.logger.Info("user info updated", zap.Uint64("userId", req.ID))
	return nil
}

// isSelf reports whether the operator in ctx targets themselves. Absent
// operator context (internal/system calls) returns false — self-guards only
// apply to human operators.
func isSelf(ctx context.Context, id uint64) bool {
	operatorID, ok := contextkeys.UserIDFromCtx(ctx)
	return ok && id == operatorID
}

// AssignRoles replaces the target user's roles. The operator cannot modify
// their own roles (admin is not exempt), matching the Disable self-guard.
func (s *UserService) AssignRoles(ctx context.Context, id uint64, roleIDs []uint64) error {
	if isSelf(ctx, id) {
		return apperror.BadRequest("不能修改自己的角色")
	}
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return translateNotFound(err, "用户不存在")
	}
	if roleIDs == nil {
		roleIDs = []uint64{} // 空=清空全部角色
	}
	if err := validateIDsExist(ctx, "角色", roleIDs, s.repo.FindExistingRoleIDs); err != nil {
		return err
	}
	// 安全修复（评审 #5）：授予角色时须拦截内置 admin 角色。与
	// AddRoleUsers/RemoveRoleUsers 的 guardRoleMembershipEditable 对齐 ——
	// 内置 admin 角色的成员集合固定，不允许通过 API 授予/收回，否则持
	// user:assign 的非 admin 操作者可给他人直接授 admin → 竖直提权。
	for _, roleID := range roleIDs {
		code, err := s.repo.FindRoleCodeByID(ctx, roleID)
		if err != nil {
			return translateNotFound(err, "角色不存在")
		}
		if code == "admin" {
			return apperror.BadRequest("内置管理员角色不允许通过接口授予")
		}
	}
	if err := s.repo.ReplaceRoles(ctx, id, roleIDs); err != nil {
		s.logger.Warn("failed to assign roles", zap.Uint64("userId", id), zap.Error(err))
		return err
	}
	// 角色变更立即失效该用户的权限缓存:否则被收回的权限最长延迟
	// accessExpire(默认 2h)才生效(PermissionGuard 缓存 TTL 与之同长)。
	//
	// 安全修复（评审 #3）：数据范围与 "admin" 判定的权威来源是登录时签发
	// 的 JWT claims.Scopes，而非权限缓存。仅 RevokePerms 清缓存无法让旧
	// token 内嵌的旧 scope 失效 —— 被降职/降权者的旧 token 会继续以
	// ScopeAll 行权直到过期。因此角色变更必须 RevokeAll 吊销其全部已签发
	// access token（与禁用/删除/改密一致）。
	if s.sessionStore != nil {
		if err := s.sessionStore.RevokeAll(ctx, id, ""); err != nil {
			s.logger.Error("failed to revoke sessions after role change",
				zap.Uint64("userId", id), zap.Error(err))
		}
	}
	s.logger.Info("user roles assigned", zap.Uint64("userId", id), zap.Int("count", len(roleIDs)))
	return nil
}

// guardRoleMembershipEditable blocks membership edits on the built-in admin
// role: its member set is fixed (removing admin would lock the system out).
func (s *UserService) guardRoleMembershipEditable(ctx context.Context, roleID uint64) error {
	code, err := s.repo.FindRoleCodeByID(ctx, roleID)
	if err != nil {
		return translateNotFound(err, "角色不存在")
	}
	if code == "admin" {
		return apperror.BadRequest("内置管理员角色不允许调整成员")
	}
	return nil
}

// revokePermsForUsers invalidates the permission cache of affected users.
// Failures are logged but not returned: the membership change has already
// been committed, so failing the request would only produce misleading
// "failed" feedback for a change that actually succeeded (same as AssignRoles).
func (s *UserService) revokePermsForUsers(ctx context.Context, userIDs []uint64) {
	if s.sessionStore == nil {
		return
	}
	for _, uid := range userIDs {
		if err := s.sessionStore.RevokePerms(ctx, uid); err != nil {
			// Error:已变更的权限在 Redis 缓存残留至 TTL(最长 2h),
			// 该窗口内旧权限仍生效,运维必须可见。
			s.logger.Error("failed to revoke perms cache after role member change",
				zap.Uint64("userId", uid), zap.Error(err))
		}
	}
}

// AddRoleUsers appends users to a role (角色管理-分配用户). Idempotent per
// user: users already assigned are skipped by the repository.
func (s *UserService) AddRoleUsers(ctx context.Context, roleID uint64, userIDs []uint64) error {
	if err := s.guardRoleMembershipEditable(ctx, roleID); err != nil {
		return err
	}
	if err := validateIDsExist(ctx, "用户", userIDs, s.repo.FindExistingIDs); err != nil {
		return err
	}
	if err := s.repo.AddUsersToRole(ctx, roleID, userIDs); err != nil {
		s.logger.Warn("failed to add users to role", zap.Uint64("roleId", roleID), zap.Error(err))
		return err
	}
	s.revokePermsForUsers(ctx, userIDs)
	s.logger.Info("users added to role", zap.Uint64("roleId", roleID), zap.Int("count", len(userIDs)))
	return nil
}

// RemoveRoleUsers removes users from a role (角色管理-分配用户).
func (s *UserService) RemoveRoleUsers(ctx context.Context, roleID uint64, userIDs []uint64) error {
	if err := s.guardRoleMembershipEditable(ctx, roleID); err != nil {
		return err
	}
	if err := s.repo.RemoveUsersFromRole(ctx, roleID, userIDs); err != nil {
		s.logger.Warn("failed to remove users from role", zap.Uint64("roleId", roleID), zap.Error(err))
		return err
	}
	s.revokePermsForUsers(ctx, userIDs)
	s.logger.Info("users removed from role", zap.Uint64("roleId", roleID), zap.Int("count", len(userIDs)))
	return nil
}

func (s *UserService) Delete(ctx context.Context, id uint64) error {
	if isSelf(ctx, id) {
		return apperror.BadRequest("不能删除自己")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		s.logger.Warn("failed to delete user", zap.Uint64("userId", id), zap.Error(err))
		return err
	}
	// Revoke all sessions for the deleted user so their tokens are invalidated.
	// token 传空:吊销对象是目标用户而非调用者,调用方不持有其 token。
	// 全部 access token 经用户索引(user_access:<uid>)清除。
	if err := s.sessionStore.RevokeAll(ctx, id, ""); err != nil {
		// Error:已删用户的 token 在白名单残留至 TTL,该窗口内仍可访问。
		s.logger.Error("failed to revoke sessions after user deletion", zap.Uint64("userId", id), zap.Error(err))
	}
	s.logger.Info("user deleted", zap.Uint64("userId", id))
	return nil
}

func (s *UserService) Enable(ctx context.Context, id uint64) error {
	if isSelf(ctx, id) {
		return apperror.BadRequest("不能修改自己的状态")
	}
	if err := s.repo.UpdateStatus(ctx, id, entity.UserStatusEnabled); err != nil {
		s.logger.Warn("failed to enable user", zap.Uint64("userId", id), zap.Error(err))
		return err
	}
	s.logger.Info("user enabled", zap.Uint64("userId", id))
	return nil
}

func (s *UserService) Disable(ctx context.Context, id uint64) error {
	if isSelf(ctx, id) {
		return apperror.BadRequest("不能修改自己的状态")
	}
	if err := s.repo.UpdateStatus(ctx, id, entity.UserStatusDisabled); err != nil {
		s.logger.Warn("failed to disable user", zap.Uint64("userId", id), zap.Error(err))
		return err
	}
	// Revoke all sessions when user is disabled.
	// 禁用是安全动作:必须在同一请求内让目标用户**全部**已签发 access
	// token 失效,否则禁用只影响后续登录,已登录会话最长可继续用满 TTL。
	if err := s.sessionStore.RevokeAll(ctx, id, ""); err != nil {
		// Error:禁用用户旧 token 在白名单残留至 TTL,禁用效果延迟生效。
		s.logger.Error("failed to revoke sessions for disabled user", zap.Uint64("userId", id), zap.Error(err))
	}
	s.logger.Info("user disabled", zap.Uint64("userId", id))
	return nil
}

func (s *UserService) ResetPassword(ctx context.Context, id uint64, req *request.ResetPasswordReq) error {
	hashed, salt, err := crypto.HashPassword(req.NewPassword, s.passwordCost(ctx))
	if err != nil {
		return apperror.Internal("密码加密失败", err)
	}
	if err := s.repo.UpdatePassword(ctx, id, hashed, &salt); err != nil {
		s.logger.Warn("failed to reset password", zap.Uint64("userId", id), zap.Error(err))
		return err
	}
	if err := s.repo.SetMustChangePassword(ctx, id, true); err != nil {
		s.logger.Warn("failed to set must-change flag", zap.Uint64("userId", id), zap.Error(err))
	}
	// Revoke all sessions after password reset.
	// 重置密码同样按用户吊销全部 access token:旧密码会话不得继续存活。
	if err := s.sessionStore.RevokeAll(ctx, id, ""); err != nil {
		// Error:重置密码后旧 token 残留,疑似泄露的凭证窗口内仍有效。
		s.logger.Error("failed to revoke sessions after password reset", zap.Uint64("userId", id), zap.Error(err))
	}
	return nil
}
