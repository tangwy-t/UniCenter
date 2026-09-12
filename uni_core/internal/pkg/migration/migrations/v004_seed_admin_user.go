package migrations

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/crypto"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     4,
		Description: "创建 admin 用户并绑定超级管理员角色",
		Up:          seedAdminUser,
	})
}

// defaultAdminPassword 是 admin 用户的初始密码兜底值。仅当未通过环境
// 变量 ADMIN_INITIAL_PASSWORD 注入强密码时使用。
// 上线后务必立即登录并在"修改密码"中更换为强密码。
const defaultAdminPassword = "admin123"

// adminPasswordEnvKey 是 admin 初始密码的环境变量键。部署方应通过它注入
// 强密码,避免使用内置兜底值 admin123。
const adminPasswordEnvKey = "ADMIN_INITIAL_PASSWORD"

// seedAdminUser 创建 admin 用户（密码哈希需 bcrypt，唯一非纯 INSERT 步骤），
// 并将其绑定到超级管理员角色。
func seedAdminUser(tx *gorm.DB) error {
	password := os.Getenv(adminPasswordEnvKey)
	if password == "" {
		// 未注入环境变量:退回内置兜底密码。这是已知弱凭据,仅保证
		// 首次能登录;生产环境必须通过 ADMIN_INITIAL_PASSWORD 注入强密码。
		password = defaultAdminPassword
		log.Printf("警告: 未设置 %s,使用内置默认密码创建 admin 用户", adminPasswordEnvKey)
	}
	hashed, salt, err := crypto.HashPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	// admin 归属 v003 创建的内置根部门「总部」，使 admin 在部门维度数据
	// 权限中拥有明确归属（否则 dept_id 为 NULL）。
	var deptID uint64
	if err := tx.Model(&entity.SysDept{}).
		Select("id").
		Where("name = ?", builtinDeptName).
		First(&deptID).Error; err != nil {
		return fmt.Errorf("seedAdminUser: 查询内置部门失败: %w", err)
	}

	status := entity.UserStatusEnabled
	user := entity.SysUser{
		Username:     "admin",
		Password:     hashed,
		PasswordSalt: util.Ptr(salt),
		DeptID:       &deptID,
		Status:       &status,
	}
	if err := tx.Create(&user).Error; err != nil {
		return err
	}

	// 种子间不共享包级变量：按 code 自行查询 v001 创建的超管角色 ID。
	var adminRoleID uint64
	if err := tx.Model(&entity.SysRole{}).
		Select("id").
		Where("code = ?", "admin").
		First(&adminRoleID).Error; err != nil {
		return fmt.Errorf("seedAdminUser: 查询超级管理员角色失败: %w", err)
	}

	if err := tx.Create(&entity.SysUserRole{
		UserID: user.ID,
		RoleID: adminRoleID,
	}).Error; err != nil {
		return err
	}
	return nil
}
