package repository

import (
	"context"
	"sort"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"gorm.io/gorm"
)

// TestFindMenuPerms_ScopeInjected 验证权限点查询不手写 role join:
// ctx 携带 ScopeContext 时,scope 插件自动注入 sys_menu.id IN (role 维度允许集合)。
func TestFindMenuPerms_ScopeInjected(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	plugin := datascope.NewScopePlugin()
	plugin.RegisterEntities(entity.ScopeEntities...)
	plugin.RegisterPlugin(db)
	if err := db.AutoMigrate(&entity.SysMenu{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seed := []entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, Name: "菜单1", Perms: strPtr("sys:a:list")},
		{BaseEntity: entity.BaseEntity{ID: 2}, Name: "菜单2", Perms: strPtr("sys:b:list")},
		{BaseEntity: entity.BaseEntity{ID: 3}, Name: "菜单3", Perms: strPtr("sys:c:list")},
		{BaseEntity: entity.BaseEntity{ID: 4}, Name: "菜单4", Perms: nil},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed menu %d: %v", seed[i].ID, err)
		}
	}

	// role 维度只允许 {1, 3}:id=2 的 perms 应被 plugin 过滤,id=4 perms 为空被条件过滤。
	sc := &datascope.ScopeContext{UserID: 7, Dimensions: map[string]*datascope.ResolvedDimension{
		datascope.DimRole: {Level: datascope.ScopeCustom, AllowedIDs: []uint64{1, 3}},
	}}
	ctx := datascope.WithScopeContext(context.Background(), sc)

	perms, err := NewAuthRepository(db).FindMenuPerms(ctx)
	if err != nil {
		t.Fatalf("FindMenuPerms: %v", err)
	}

	want := []string{"sys:a:list", "sys:c:list"}
	sort.Strings(perms)
	sort.Strings(want)
	if len(perms) != len(want) {
		t.Fatalf("perms = %v, want %v", perms, want)
	}
	for i := range want {
		if perms[i] != want[i] {
			t.Fatalf("perms = %v, want %v", perms, want)
		}
	}
}
