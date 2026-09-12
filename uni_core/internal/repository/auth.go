package repository

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"time"

	"gorm.io/gorm"
)

type AuthRepo struct {
	db *gorm.DB
}

// NewAuthRepository returns an AuthRepository backed by the given *gorm.DB.
func NewAuthRepository(db *gorm.DB) *AuthRepo {
	return &AuthRepo{db: db}
}

func (r *AuthRepo) FindByUsername(ctx context.Context, username string) (*entity.SysUser, error) {
	var user entity.SysUser
	err := r.db.WithContext(ctx).Where("username = ?", username).Preload("Roles").First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindMenuPerms 返回当前 ctx 数据范围下所有非空菜单权限标识。
// 本方法不手写任何角色 join:ctx 携带 ScopeContext 时,scope 插件自动
// 注入 sys_menu.id IN (角色授权菜单 ID) 过滤,与菜单树查询共用同一来源。
func (r *AuthRepo) FindMenuPerms(ctx context.Context) ([]string, error) {
	var perms []string
	err := r.db.WithContext(ctx).Model(&entity.SysMenu{}).
		Where("perms IS NOT NULL AND perms != ''").
		Pluck("perms", &perms).Error
	return perms, err
}

func (r *AuthRepo) FindByID(ctx context.Context, id uint64) (*entity.SysUser, error) {
	var user entity.SysUser
	err := r.db.WithContext(ctx).Preload("Roles").First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *AuthRepo) GetRoleCodes(ctx context.Context, userID uint64) ([]string, error) {
	var codes []string
	err := r.db.WithContext(ctx).Table("sys_role").
		Joins("JOIN sys_user_role ON sys_role.id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ?", userID).
		Pluck("sys_role.code", &codes).Error
	return codes, err
}

func (r *AuthRepo) UpdatePassword(ctx context.Context, userID uint64, newPassword string, newSalt *string) error {
	updates := map[string]interface{}{"password": newPassword}
	if newSalt != nil {
		updates["password_salt"] = *newSalt
	}
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", userID).
		Updates(updates).Error
}

// SetMustChangePassword 写入“建议改密”标记。false 由改密成功后调用清除。
func (r *AuthRepo) SetMustChangePassword(ctx context.Context, userID uint64, mustChange bool) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", userID).
		Update("must_change_password", mustChange).Error
}

// UpdateProfile updates only the self-editable profile columns (real_name,
// nickname, email, phone, gender) — explicit column list, no mass assignment.
// A nil pointer writes SQL NULL ("clear the field"), mirroring the semantics
// of request.UpdateProfileReq where an empty string means "clear".
func (r *AuthRepo) UpdateProfile(ctx context.Context, userID uint64, user *entity.SysUser) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", userID).
		Updates(map[string]interface{}{
			"real_name": user.RealName,
			"nickname":  user.Nickname,
			"email":     user.Email,
			"phone":     user.Phone,
			"gender":    user.Gender,
		}).Error
}

// UpdateAvatar stores the avatar path produced by the avatar upload flow.
func (r *AuthRepo) UpdateAvatar(ctx context.Context, userID uint64, avatar string) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", userID).
		Update("avatar", avatar).Error
}

func (r *AuthRepo) UpdateLoginInfo(ctx context.Context, userID uint64, ip string) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", userID).
		Updates(map[string]interface{}{
			"last_login_time": time.Now(),
			"last_login_ip":   ip,
		}).Error
}

func (r *AuthRepo) GetUserDataScope(ctx context.Context, userID uint64) (int8, uint64, error) {
	var user entity.SysUser
	if err := r.db.WithContext(ctx).Select("dept_id").First(&user, userID).Error; err != nil {
		return 0, 0, err
	}
	deptID := uint64(0)
	if user.DeptID != nil {
		deptID = *user.DeptID
	}

	// Check if user has admin role
	var adminCount int64
	if err := r.db.WithContext(ctx).Table("sys_user_role").
		Joins("JOIN sys_role ON sys_role.id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ? AND sys_role.code = ?", userID, "admin").
		Count(&adminCount).Error; err != nil {
		return 0, 0, err
	}
	if adminCount > 0 {
		return 1, deptID, nil
	}

	// Get min data_scope from user's roles
	var minScope *int8
	if err := r.db.WithContext(ctx).Table("sys_role").
		Joins("JOIN sys_user_role ON sys_role.id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ?", userID).
		Select("MIN(sys_role.data_scope)").
		Scan(&minScope).Error; err != nil {
		return 0, 0, err
	}

	if minScope == nil || *minScope == 0 {
		return 5, deptID, nil
	}
	return *minScope, deptID, nil
}

func (r *AuthRepo) FindRoleDeptIDs(ctx context.Context, userID uint64) ([]uint64, error) {
	var deptIDs []uint64
	err := r.db.WithContext(ctx).Table("sys_role_dept").
		Joins("JOIN sys_user_role ON sys_role_dept.role_id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ?", userID).
		Pluck("sys_role_dept.dept_id", &deptIDs).Error
	return deptIDs, err
}

// FindRoleMenuIDs finds all menu IDs accessible to the user through role assignments,
// including ancestor chain completion for tree structure integrity.
func (r *AuthRepo) FindRoleMenuIDs(ctx context.Context, userID uint64) ([]uint64, error) {
	// Step 1: Get menu IDs from user's role assignments
	var menuIDs []uint64
	err := r.db.WithContext(ctx).Table("sys_role_menu").
		Joins("JOIN sys_user_role ON sys_role_menu.role_id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ?", userID).
		Pluck("sys_role_menu.menu_id", &menuIDs).Error
	if err != nil {
		return nil, err
	}
	if len(menuIDs) == 0 {
		return []uint64{}, nil
	}

	// Step 2: Complete ancestor chain. Load id/parent_id once and walk the
	// tree in memory — the previous per-level query loop issued one SQL
	// statement per menu per tree level (hundreds of queries when loading
	// permissions for a user with many menus).
	var rows []struct {
		ID       uint64
		ParentID *uint64
	}
	if err := r.db.WithContext(ctx).Model(&entity.SysMenu{}).
		Select("id, parent_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	parentOf := make(map[uint64]*uint64, len(rows))
	for i := range rows {
		parentOf[rows[i].ID] = rows[i].ParentID
	}

	ancestorMap := make(map[uint64]bool, len(menuIDs))
	for _, id := range menuIDs {
		for cur := id; cur != 0; {
			if ancestorMap[cur] {
				break
			}
			ancestorMap[cur] = true
			parent := parentOf[cur]
			if parent == nil {
				break
			}
			cur = *parent
		}
	}

	result := make([]uint64, 0, len(ancestorMap))
	for id := range ancestorMap {
		result = append(result, id)
	}
	return result, nil
}

// GetUserRoleScope determines the role dimension scope level for a user.
// Admin users get ScopeAll; all other users get ScopeCustom.
func (r *AuthRepo) GetUserRoleScope(ctx context.Context, userID uint64) int8 {
	var adminCount int64
	if err := r.db.WithContext(ctx).Table("sys_user_role").
		Joins("JOIN sys_role ON sys_role.id = sys_user_role.role_id").
		Where("sys_user_role.user_id = ? AND sys_role.code = ?", userID, "admin").
		Count(&adminCount).Error; err != nil {
		// Fail closed: on DB failure treat the user as non-admin rather than
		// silently widening their menu scope.
		return datascope.ScopeCustom
	}
	if adminCount > 0 {
		return datascope.ScopeAll
	}
	return datascope.ScopeCustom
}

// CountUserLogins returns the lifetime count of successful login attempts.
// 登录/登出记录的区分依据:RecordLogin 总会写入 browser(最差为 "Unknown"),
// 而 RecordLogout 不写 —— browser IS NOT NULL 即登录尝试。
func (r *AuthRepo) CountUserLogins(ctx context.Context, userID uint64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Table("sys_login_log").
		Where("user_id = ? AND code = 0 AND browser IS NOT NULL", userID).
		Count(&count).Error
	return count, err
}

// FindUserLoginLogsSince returns the user's login attempts since the given
// time, newest first, excluding logout records (see CountUserLogins).
func (r *AuthRepo) FindUserLoginLogsSince(ctx context.Context, userID uint64, since time.Time) ([]entity.SysLoginLog, error) {
	var logs []entity.SysLoginLog
	err := r.db.WithContext(ctx).
		Table("sys_login_log").
		Where("user_id = ? AND browser IS NOT NULL AND login_time >= ?", userID, since).
		Order("login_time DESC").
		Find(&logs).Error
	return logs, err
}

// GetDeptName returns a department's display name ("" when missing).
func (r *AuthRepo) GetDeptName(ctx context.Context, deptID uint64) (string, error) {
	var name string
	err := r.db.WithContext(ctx).
		Table("sys_dept").
		Select("name").
		Where("id = ?", deptID).
		Scan(&name).Error
	return name, err
}
