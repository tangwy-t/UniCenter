package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
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

// ── FindRoleIDsByPerm（权限码 → 持有角色，docker 通知受众收敛）───────────
//
// 形态刻意铺开：多菜单同码（docker:list 在种子里就挂 4 个页面）、perms 的
// CSV 多值形态、子串陷阱（docker:listing 不算命中）、软删菜单排除 —— 这些
// 是反查 SQL 与 Go 侧 CSV 匹配的全部行为面。

// seedPermMenus 铺一张「菜单 × 角色」的权限面。
//
//	菜单  perms                     挂到角色
//	m10   docker:list               r1
//	m11   docker:list               r2（另挂 m12 按钮码）
//	m12   docker:manage             r2
//	m13   docker:list,docker:inspect   r3（CSV 多值形态）
//	m14   (空，目录)               r4（另挂 m16 子串陷阱）
//	m15   docker:list               r6（稍后软删）
//	m16   docker:listing            r4（子串陷阱：整词比对不得命中）
func seedPermMenus(t *testing.T, db *gorm.DB) {
	t.Helper()
	perm := func(s string) *string { return &s }
	menus := []entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 10}, Name: "m10", Perms: perm("docker:list")},
		{BaseEntity: entity.BaseEntity{ID: 11}, Name: "m11", Perms: perm("docker:list")},
		{BaseEntity: entity.BaseEntity{ID: 12}, Name: "m12", Perms: perm("docker:manage")},
		{BaseEntity: entity.BaseEntity{ID: 13}, Name: "m13", Perms: perm("docker:list,docker:inspect")},
		{BaseEntity: entity.BaseEntity{ID: 14}, Name: "m14"}, // 目录：无 perms
		{BaseEntity: entity.BaseEntity{ID: 15}, Name: "m15", Perms: perm("docker:list")},
		{BaseEntity: entity.BaseEntity{ID: 16}, Name: "m16", Perms: perm("docker:listing")},
	}
	for i := range menus {
		mustCreate(t, db, &menus[i])
	}
	roles := []entity.SysRole{
		{BaseEntity: entity.BaseEntity{ID: 1}, Name: "ops", Code: "ops"},
		{BaseEntity: entity.BaseEntity{ID: 2}, Name: "dev", Code: "dev"},
		{BaseEntity: entity.BaseEntity{ID: 3}, Name: "qa", Code: "qa"},
		{BaseEntity: entity.BaseEntity{ID: 4}, Name: "guest", Code: "guest"},
		{BaseEntity: entity.BaseEntity{ID: 6}, Name: "legacy", Code: "legacy"},
	}
	for i := range roles {
		mustCreate(t, db, &roles[i])
	}
	joins := []entity.SysRoleMenu{
		{RoleID: 1, MenuID: 10},
		{RoleID: 2, MenuID: 11},
		{RoleID: 2, MenuID: 12},
		{RoleID: 3, MenuID: 13},
		{RoleID: 4, MenuID: 14},
		{RoleID: 4, MenuID: 16},
		{RoleID: 6, MenuID: 15},
	}
	for i := range joins {
		mustCreate(t, db, &joins[i]) // 零值 ID 由雪花回调补齐（newRoleTestDB 已注册）
	}
}

func TestRoleRepo_FindRoleIDsByPerm(t *testing.T) {
	db := newRoleTestDB(t)
	seedPermMenus(t, db)
	// m15 软删：挂它的角色（r6）必须随之出局 —— 与 FindMenuPerms 同款口径。
	if err := db.Delete(&entity.SysMenu{}, 15).Error; err != nil {
		t.Fatalf("soft-delete m15: %v", err)
	}

	repo := NewRoleRepository(db)
	ctx := context.Background()

	// docker:list：r1（m10）、r2（m11，多菜单同码并集）、r3（CSV 第二形态）；
	// r4 只有子串陷阱与空码目录、r6 的菜单已软删 —— 都不得命中。
	roles, err := repo.FindRoleIDsByPerm(ctx, "docker:list")
	if err != nil {
		t.Fatalf("FindRoleIDsByPerm: %v", err)
	}
	assertUint64Slice(t, roles, []uint64{1, 2, 3})

	// 另一个码各查各的：r2 持有 manage、r3 经 CSV 持有 inspect。
	manage, err := repo.FindRoleIDsByPerm(ctx, "docker:manage")
	if err != nil {
		t.Fatalf("FindRoleIDsByPerm docker:manage: %v", err)
	}
	assertUint64Slice(t, manage, []uint64{2})
	inspect, err := repo.FindRoleIDsByPerm(ctx, "docker:inspect")
	if err != nil {
		t.Fatalf("FindRoleIDsByPerm docker:inspect: %v", err)
	}
	assertUint64Slice(t, inspect, []uint64{3})

	// 无人持有：空集合、非错误（受众跳过分支的输入形态）。
	none, err := repo.FindRoleIDsByPerm(ctx, "docker:nonexist")
	if err != nil {
		t.Fatalf("FindRoleIDsByPerm docker:nonexist: %v", err)
	}
	assertUint64Slice(t, none, []uint64{})

	// 空码防御：不查库直接空手而归（perm 是调用方字面量，空串是编程错误
	// 的信号 —— 拿它跑 join 只会得到全量行）。
	empty, err := repo.FindRoleIDsByPerm(ctx, "")
	if err != nil {
		t.Fatalf("FindRoleIDsByPerm empty perm: %v", err)
	}
	assertUint64Slice(t, empty, []uint64{})
}

// permCSVContains 的整词比对与空白容错（反查的 CSV 匹配面，独立钉住：
// SQL 测试走不到「带空格的人工录入形态」）。
func TestPermCSVContains(t *testing.T) {
	cases := []struct {
		perms, code string
		want        bool
	}{
		{"docker:list", "docker:list", true},
		{"docker:list,docker:inspect", "docker:inspect", true}, // CSV 多值
		{"docker:inspect,docker:list", "docker:list", true},    // 位置无关
		{" docker:list , docker:manage ", "docker:list", true}, // 空白容错
		{"docker:listing", "docker:list", false},               // 子串不算命中
		{"docker:manage", "docker:list", false},
		{"", "docker:list", false},
	}
	for _, c := range cases {
		if got := permCSVContains(c.perms, c.code); got != c.want {
			t.Fatalf("permCSVContains(%q, %q) = %v, want %v", c.perms, c.code, got, c.want)
		}
	}
}
