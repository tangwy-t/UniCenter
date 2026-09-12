package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/snowflake"
	"gorm.io/gorm"
)

// newRoleTestDB 建 sqlite 内存库并注册 SetupJoinTable(与生产 migration.MigrateAll
// 一致), 迁移 role/menu/dept 及其 join 表。
func newRoleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	db.SetupJoinTable(&entity.SysRole{}, "Menus", &entity.SysRoleMenu{})
	db.SetupJoinTable(&entity.SysRole{}, "Depts", &entity.SysRoleDept{})
	if err := db.AutoMigrate(&entity.SysRole{}, &entity.SysMenu{}, &entity.SysDept{},
		&entity.SysRoleMenu{}, &entity.SysRoleDept{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestRoleRepo_ReplaceMenusDeptsViaAssociation 验证菜单/部门关联经
// Association.Replace 写入:多 ID 全部落库且 join 行拿到雪花 ID, 替换与清空语义正确。
func TestRoleRepo_ReplaceMenusDeptsViaAssociation(t *testing.T) {
	db := newRoleTestDB(t)
	mustCreate(t, db, &entity.SysRole{BaseEntity: entity.BaseEntity{ID: 1}, Name: "dev", Code: "dev"})
	mustCreate(t, db, &entity.SysMenu{BaseEntity: entity.BaseEntity{ID: 5}, Name: "m5"})
	mustCreate(t, db, &entity.SysMenu{BaseEntity: entity.BaseEntity{ID: 6}, Name: "m6"})
	mustCreate(t, db, &entity.SysDept{BaseEntity: entity.BaseEntity{ID: 7}, Name: "d7"})
	mustCreate(t, db, &entity.SysDept{BaseEntity: entity.BaseEntity{ID: 8}, Name: "d8"})

	repo := NewRoleRepository(db)
	ctx := context.Background()

	if err := repo.CreateWithAssociations(ctx,
		&entity.SysRole{BaseEntity: entity.BaseEntity{ID: 9}, Name: "x", Code: "x"},
		[]uint64{5, 6}, []uint64{7, 8}); err != nil {
		t.Fatalf("CreateWithAssociations: %v", err)
	}

	assertRoleMenus(t, db, 9, 5, 6)
	assertRoleDepts(t, db, 9, 7, 8)
	assertJoinSnowflakeIDs(t, db, "sys_role_menu", 9)
	assertJoinSnowflakeIDs(t, db, "sys_role_dept", 9)

	// 替换: 菜单换成只有 6。
	if _, err := repo.UpdateWithAssociationsAndUserIDs(ctx,
		&entity.SysRole{BaseEntity: entity.BaseEntity{ID: 9}, Name: "x", Code: "x"},
		[]uint64{6}, []uint64{7}); err != nil {
		t.Fatalf("UpdateWithAssociationsAndUserIDs: %v", err)
	}
	assertRoleMenus(t, db, 9, 6)
	assertRoleDepts(t, db, 9, 7)
}

// TestRoleRepo_FindExistingIDs 验证存在性查询的过滤语义。
func TestRoleRepo_FindExistingIDs(t *testing.T) {
	db := newRoleTestDB(t)
	mustCreate(t, db, &entity.SysMenu{BaseEntity: entity.BaseEntity{ID: 5}, Name: "m5"})
	mustCreate(t, db, &entity.SysDept{BaseEntity: entity.BaseEntity{ID: 7}, Name: "d7"})

	repo := NewRoleRepository(db)
	ctx := context.Background()

	menus, err := repo.FindExistingMenuIDs(ctx, []uint64{5, 999})
	if err != nil {
		t.Fatalf("FindExistingMenuIDs: %v", err)
	}
	if len(menus) != 1 || menus[0] != 5 {
		t.Fatalf("existing menus = %v, want [5]", menus)
	}

	depts, err := repo.FindExistingDeptIDs(ctx, []uint64{7, 999})
	if err != nil {
		t.Fatalf("FindExistingDeptIDs: %v", err)
	}
	if len(depts) != 1 || depts[0] != 7 {
		t.Fatalf("existing depts = %v, want [7]", depts)
	}
}

func assertRoleMenus(t *testing.T, db *gorm.DB, roleID uint64, want ...uint64) {
	t.Helper()
	var got []uint64
	if err := db.Table("sys_role_menu").Where("role_id = ?", roleID).
		Order("menu_id").Pluck("menu_id", &got).Error; err != nil {
		t.Fatalf("pluck role menus: %v", err)
	}
	assertUint64Slice(t, got, want)
}

func assertRoleDepts(t *testing.T, db *gorm.DB, roleID uint64, want ...uint64) {
	t.Helper()
	var got []uint64
	if err := db.Table("sys_role_dept").Where("role_id = ?", roleID).
		Order("dept_id").Pluck("dept_id", &got).Error; err != nil {
		t.Fatalf("pluck role depts: %v", err)
	}
	assertUint64Slice(t, got, want)
}

func assertJoinSnowflakeIDs(t *testing.T, db *gorm.DB, table string, roleID uint64) {
	t.Helper()
	const snowflakeIDMin uint64 = 1 << 52
	var low int64
	if err := db.Table(table).
		Where("role_id = ? AND id < ?", roleID, snowflakeIDMin).
		Count(&low).Error; err != nil {
		t.Fatalf("count non-snowflake %s rows: %v", table, err)
	}
	if low != 0 {
		t.Fatalf("%s has %d row(s) without snowflake ID", table, low)
	}
}

func assertUint64Slice(t *testing.T, got, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func mustCreate(t *testing.T, db *gorm.DB, v interface{}) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("create %T: %v", v, err)
	}
}
