package repository

import (
	"context"
	"strconv"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type UserRepo struct {
	db *gorm.DB
}

// NewUserRepository returns a UserRepository backed by the given *gorm.DB.
func NewUserRepository(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) applyUserFilters(db *gorm.DB, query *request.UserQuery) *gorm.DB {
	if query.Username != "" {
		db = db.Where("username LIKE ?", "%"+query.Username+"%")
	}
	if query.RealName != "" {
		db = db.Where("real_name LIKE ?", "%"+query.RealName+"%")
	}
	if query.Phone != "" {
		db = db.Where("phone LIKE ?", "%"+query.Phone+"%")
	}
	if query.Email != "" {
		db = db.Where("email LIKE ?", "%"+query.Email+"%")
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	if query.DeptID != nil {
		// 部门筛选「含子部门」:选中节点 + 其全部后代部门。ancestors 为「根,…,自身」
		// 链路,(',' || ancestors || ',') LIKE '%,{id},%' 命中自身及所有含该节点的后代。
		// 与数据范围(scope 插件)相互独立:插件再以 AND 叠加 dept 维度,不会放大越权。
		deptIDStr := strconv.FormatUint(*query.DeptID, 10)
		pattern := "%," + deptIDStr + ",%"
		db = db.Where(
			"dept_id IN (SELECT id FROM sys_dept WHERE id = ? OR (',' || ancestors || ',') LIKE ?)",
			*query.DeptID, pattern,
		)
	}
	if query.RoleID != nil {
		// 过滤已分配该角色的用户:不用 JOIN,交给子查询,避免与 Roles Preload 的
		// many2many JOIN 产生笛卡尔积。
		db = db.Where("id IN (SELECT user_id FROM sys_user_role WHERE role_id = ?)", *query.RoleID)
	}
	if query.CreatedAtStart != "" {
		db = db.Where("created_at >= ?", query.CreatedAtStart+" 00:00:00")
	}
	if query.CreatedAtEnd != "" {
		db = db.Where("created_at <= ?", query.CreatedAtEnd+" 23:59:59")
	}
	return db
}

func (r *UserRepo) FindPage(ctx context.Context, query *request.UserQuery) ([]entity.SysUser, int64, error) {
	// Count without Preload to avoid unnecessary JOIN overhead.
	countDB := r.applyUserFilters(r.db.WithContext(ctx).Model(&entity.SysUser{}), query)
	dataDB := r.applyUserFilters(r.db.WithContext(ctx).Model(&entity.SysUser{}), query).
		Preload("Dept").Preload("Roles").Order("id DESC")
	return paginate[entity.SysUser](countDB, dataDB, query)
}

func (r *UserRepo) FindByID(ctx context.Context, id uint64) (*entity.SysUser, error) {
	var user entity.SysUser
	err := r.db.WithContext(ctx).Preload("Dept").Preload("Roles").First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByIDs returns users by a batch of primary keys, with Dept preloaded.
// The query inherits the request context, so the DataScope GORM plugin applies
// row-level dept filtering to the result automatically (non-admin callers only
// see users within their data scope). Empty ids returns an empty slice.
func (r *UserRepo) FindByIDs(ctx context.Context, ids []uint64) ([]entity.SysUser, error) {
	if len(ids) == 0 {
		return []entity.SysUser{}, nil
	}
	var users []entity.SysUser
	err := r.db.WithContext(ctx).
		Preload("Dept").
		Where("id IN ?", ids).
		Find(&users).Error
	if err != nil {
		return nil, err
	}
	return users, nil
}

// FindExistingIDs returns the subset of ids that exist in sys_user.
// Used by the service layer to validate role-member userIDs before an
// Association write (which would otherwise upsert a phantom user for an
// unknown ID).
func (r *UserRepo) FindExistingIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var existing []uint64
	err := r.db.WithContext(ctx).Model(&entity.SysUser{}).
		Where("id IN ?", ids).Pluck("id", &existing).Error
	return existing, err
}

func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*entity.SysUser, error) {
	var user entity.SysUser
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// CreateWithRoles inserts a user together with their role assignments.
//
// Roles are attached through Association("Roles").Append; SetupJoinTable
// (pkg/migration) points the association at the real SysUserRole schema so the
// id:generate callback assigns snowflake IDs to every join row.
func (r *UserRepo) CreateWithRoles(ctx context.Context, user *entity.SysUser, roleIDs []uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Create(user).Error; err != nil {
			return err
		}
		return r.appendRoles(ctx, tx, user.ID, roleIDs)
	})
}

func (r *UserRepo) Update(ctx context.Context, user *entity.SysUser) error {
	// status is omitted so the info-update endpoint never touches the status
	// column — status changes must go through Enable/Disable (self-guarded).
	// FindByID preloads a non-nil status; without this Omit, Updates would
	// rewrite it on every UpdateUserInfo, racing concurrent Disable/Enable.
	return r.db.WithContext(ctx).Model(user).
		Omit("password", "last_login_time", "last_login_ip", "status").
		Updates(user).Error
}

// ReplaceRoles atomically swaps a user's role set for exactly roleIDs.
//
// Association("Roles").Replace deletes join rows not in the new set, then
// appends the new ones — each join row receiving a snowflake ID because
// SetupJoinTable (pkg/migration) binds the association to the real SysUserRole
// schema. The caller (service layer) must have validated that every roleID
// exists; an unknown ID would otherwise be upserted as a phantom role by the
// association's own save step.
func (r *UserRepo) ReplaceRoles(ctx context.Context, id uint64, roleIDs []uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.replaceRolesTx(ctx, tx, id, roleIDs)
	})
}

// replaceRolesTx performs the ReplaceRoles write inside tx.
func (r *UserRepo) replaceRolesTx(ctx context.Context, tx *gorm.DB, id uint64, roleIDs []uint64) error {
	roles := make([]entity.SysRole, 0, len(roleIDs))
	for _, rid := range roleIDs {
		roles = append(roles, entity.SysRole{BaseEntity: entity.BaseEntity{ID: rid}})
	}
	return tx.WithContext(ctx).Model(&entity.SysUser{BaseEntity: entity.BaseEntity{ID: id}}).
		Association("Roles").Replace(roles)
}

// appendRoles attaches roleIDs to the user identified by id via Association.
func (r *UserRepo) appendRoles(ctx context.Context, tx *gorm.DB, id uint64, roleIDs []uint64) error {
	if len(roleIDs) == 0 {
		return nil
	}
	roles := make([]entity.SysRole, 0, len(roleIDs))
	for _, rid := range roleIDs {
		roles = append(roles, entity.SysRole{BaseEntity: entity.BaseEntity{ID: rid}})
	}
	return tx.WithContext(ctx).Model(&entity.SysUser{BaseEntity: entity.BaseEntity{ID: id}}).
		Association("Roles").Append(roles)
}

func (r *UserRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysUser{}, id).Error
}

func (r *UserRepo) UpdateStatus(ctx context.Context, id uint64, status int8) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", id).
		Update("status", status).Error
}

func (r *UserRepo) UpdatePassword(ctx context.Context, id uint64, password string, salt *string) error {
	updates := map[string]interface{}{"password": password}
	if salt != nil {
		updates["password_salt"] = *salt
	}
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", id).
		Updates(updates).Error
}

// SetMustChangePassword 写入“建议改密”标记。重置密码后置 true,改密后清 false。
func (r *UserRepo) SetMustChangePassword(ctx context.Context, id uint64, mustChange bool) error {
	return r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("id = ?", id).
		Update("must_change_password", mustChange).Error
}

// FindRoleCodeByID returns the role's code (used by role-member guards:
// the built-in admin role's membership is not modifiable via the role page).
func (r *UserRepo) FindRoleCodeByID(ctx context.Context, roleID uint64) (string, error) {
	var code string
	err := r.db.WithContext(ctx).Model(&entity.SysRole{}).
		Where("id = ?", roleID).Pluck("code", &code).Error
	return code, err
}

// FindExistingRoleIDs returns the subset of roleIDs that exist in sys_role.
// Used by the service layer to validate roleIDs before an Association write
// (which would otherwise upsert a phantom role for an unknown ID).
func (r *UserRepo) FindExistingRoleIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var existing []uint64
	err := r.db.WithContext(ctx).Model(&entity.SysRole{}).
		Where("id IN ?", ids).Pluck("id", &existing).Error
	return existing, err
}

// AddUsersToRole appends users to a role via the reverse Association
// ("Users" on SysRole). Idempotent per user: GORM skips already-assigned rows
// (OnConflict DoNothing). The caller must have validated that every userID
// exists — an unknown ID would be upserted as a phantom user by the
// association's save step.
func (r *UserRepo) AddUsersToRole(ctx context.Context, roleID uint64, userIDs []uint64) error {
	if len(userIDs) == 0 {
		return nil
	}
	users := make([]entity.SysUser, 0, len(userIDs))
	for _, uid := range userIDs {
		users = append(users, entity.SysUser{BaseEntity: entity.BaseEntity{ID: uid}})
	}
	return r.db.WithContext(ctx).Model(&entity.SysRole{BaseEntity: entity.BaseEntity{ID: roleID}}).
		Association("Users").Append(users)
}

// RemoveUsersFromRole removes the given users from a role via the reverse
// Association ("Users" on SysRole).
func (r *UserRepo) RemoveUsersFromRole(ctx context.Context, roleID uint64, userIDs []uint64) error {
	if len(userIDs) == 0 {
		return nil
	}
	users := make([]entity.SysUser, 0, len(userIDs))
	for _, uid := range userIDs {
		users = append(users, entity.SysUser{BaseEntity: entity.BaseEntity{ID: uid}})
	}
	return r.db.WithContext(ctx).Model(&entity.SysRole{BaseEntity: entity.BaseEntity{ID: roleID}}).
		Association("Users").Delete(users)
}
