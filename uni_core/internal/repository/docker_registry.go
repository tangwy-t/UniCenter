package repository

import (
	"context"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"

	"gorm.io/gorm"
)

// DockerRegistryRepo 是仓库凭据的存储面（4c）。
//
// 纪律：**删除即失效**是全库硬删（Unscoped）—— 凭据不是审计资产，墓碑
// 只会让同 registry 重新创建时撞唯一索引（「重输密码新建」本该夺回键）。
// 读只按 registry 唯一键（列表除外），没有按 id 的读路径 —— id 在 HTTP
// 契约上不出现，凭据的寻址键始终是仓库地址本身。
type DockerRegistryRepo struct {
	db *gorm.DB
}

// NewDockerRegistryRepo 构造存储面。
func NewDockerRegistryRepo(db *gorm.DB) *DockerRegistryRepo {
	return &DockerRegistryRepo{db: db}
}

// FindByRegistry 按仓库地址取一条凭据；不存在返回 gorm.ErrRecordNotFound。
func (r *DockerRegistryRepo) FindByRegistry(ctx context.Context, registry string) (*entity.DockerRegistryCredential, error) {
	var row entity.DockerRegistryCredential
	err := r.db.WithContext(ctx).Where("registry = ?", registry).First(&row).Error
	return &row, err
}

// Create 新建一条凭据（registry 已规范化后写入）。
func (r *DockerRegistryRepo) Create(ctx context.Context, row *entity.DockerRegistryCredential) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// Save 更新既有凭据（限定 Registry 列 —— 唯一键不可改，见 service 的说明）。
func (r *DockerRegistryRepo) Save(ctx context.Context, row *entity.DockerRegistryCredential) error {
	return r.db.WithContext(ctx).Model(&entity.DockerRegistryCredential{}).
		Where("registry = ?", row.Registry).
		Updates(map[string]any{
			"username":     row.Username,
			"password_enc": row.PasswordEnc,
			"remark":       row.Remark,
		}).Error
}

// Delete 按仓库地址**硬删**一条凭据（删除即失效：下一单带这个 registry 的
// 拉取在受理处就得到「没有这个仓库的凭据」，不需要等任何对账周期）。
func (r *DockerRegistryRepo) Delete(ctx context.Context, registry string) error {
	res := r.db.WithContext(ctx).Unscoped().
		Where("registry = ?", registry).
		Delete(&entity.DockerRegistryCredential{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// List 返回全部凭据（按 created_at 升序 —— 与「列表页的添加顺序」一致）。
// 只取外发需要的列，密文盒也一起读出（调用方并不拿到明文）。
func (r *DockerRegistryRepo) List(ctx context.Context) ([]entity.DockerRegistryCredential, error) {
	var rows []entity.DockerRegistryCredential
	err := r.db.WithContext(ctx).Order("created_at ASC").Find(&rows).Error
	return rows, err
}