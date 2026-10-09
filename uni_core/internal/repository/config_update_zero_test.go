package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

// 零值必须落库守卫：GORM 的 Updates(struct) 会静默跳过零值字段，而
// config_value 的合法值包含**空串**（sys.agent.targetVersion 空 = 关闭全站
// 升级）。2026-10-09 生产实测：关闭全站升级返回 200 但 DB 旧值原样保留
// （UPDATE 语句里根本没有 config_value 列）。Update 必须 Select 点名列，
// 否则本测试直接红 —— 它钉死的是「关闭操作静默失效」这类高危缺陷。
func TestConfigUpdateWritesZeroValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(&entity.SysConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	status := int8(1)
	seed := entity.SysConfig{
		ConfigKey:   "sys.agent.targetVersion",
		ConfigValue: "0.6.1",
		ConfigType:  "S",
		Status:      &status,
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 关闭全站升级 = 把 config_value 写成空串（零值）。
	seed.ConfigValue = ""
	repo := &ConfigRepo{db: db}
	if err := repo.Update(context.Background(), &seed); err != nil {
		t.Fatalf("update: %v", err)
	}

	var got entity.SysConfig
	if err := db.Where("config_key = ?", seed.ConfigKey).First(&got).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.ConfigValue != "" {
		t.Fatalf("config_value 应落库为空串（关闭全站升级），实际为 %q —— "+
			"零值被 Updates 跳过，关闭操作静默失效", got.ConfigValue)
	}
}
