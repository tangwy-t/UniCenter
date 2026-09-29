package migrations

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/crypto"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     4,
		Description: "创建 admin 用户并绑定超级管理员角色",
		Up:          seedAdminUser,
	})
}

// adminPasswordEnvKey 是 admin 初始密码的环境变量键。部署方应通过它注入强密码；
// 未注入时不再退回到内置固定口令，而是生成一次性随机口令（见 generateInitialPassword）。
const adminPasswordEnvKey = "ADMIN_INITIAL_PASSWORD"

// generateInitialPassword 生成一次性初始口令：crypto/rand 16 字节随机数取 base64url。
//
// 刻意不保留内置默认口令：固定默认口令意味着"所有忘记注入 ADMIN_INITIAL_PASSWORD 的
// 部署共用同一凭据"，属于已知弱凭据（CWE-798）。随机口令只在首次迁移时打印一次，
// 管理员登录后必须在「修改密码」中更换为自选强口令。
func generateInitialPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// seedAdminUser 创建 admin 用户（密码哈希需 bcrypt，唯一非纯 INSERT 步骤），
// 并将其绑定到超级管理员角色。
func seedAdminUser(tx *gorm.DB) error {
	password := os.Getenv(adminPasswordEnvKey)
	generated := false
	if password == "" {
		// 未注入环境变量:生成一次性随机口令,迁移结束时打印一次供首次登录。
		// 这样既不需要"内置弱口令",也不会让不同部署共用同一凭据。
		var err error
		password, err = generateInitialPassword()
		if err != nil {
			return fmt.Errorf("seedAdminUser: 生成初始口令失败: %w", err)
		}
		generated = true
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

	// 一次性初始口令只在真正种成功之后输出,且仅本次迁移（已入库的部署不会重复打印）。
	if generated {
		log.Printf(
			"警告: 未设置 %s,已为 admin 生成一次性初始口令: %s —— 请立即登录并在「修改密码」中更换;该口令仅输出这一次。",
			adminPasswordEnvKey, password,
		)
	}
	return nil
}
