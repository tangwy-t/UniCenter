package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/snowflake"
	"gorm.io/gorm"
)

// newUserDeptFilterTestDB 建 sqlite 内存库,除 user/role 外额外迁移 sys_dept,
// 用于验证部门筛选「含子部门」语义(经 ancestors 链路命中全部后代)。
func newUserDeptFilterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:userdeptfilter?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	db.SetupJoinTable(&entity.SysUser{}, "Roles", &entity.SysUserRole{})
	db.SetupJoinTable(&entity.SysRole{}, "Users", &entity.SysUserRole{})
	if err := db.AutoMigrate(&entity.SysUser{}, &entity.SysRole{}, &entity.SysUserRole{},
		&entity.SysDept{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedDept(t *testing.T, db *gorm.DB, id uint64, name, ancestors string) {
	t.Helper()
	if err := db.Create(&entity.SysDept{
		BaseEntity: entity.BaseEntity{ID: id},
		Name:       name,
		Ancestors:  ancestors,
	}).Error; err != nil {
		t.Fatalf("seed dept %s: %v", name, err)
	}
}

func seedUserInDept(t *testing.T, db *gorm.DB, id uint64, username string, deptID uint64) {
	t.Helper()
	d := deptID
	if err := db.Create(&entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: id},
		Username:   username,
		Password:   "hashed",
		DeptID:     &d,
	}).Error; err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
}

// TestUserRepoFindPage_DeptIDFilterIncludesDescendants 部门筛选应「含子部门」:
// 选根/父节点时,连同其后代部门的用户一并返回;与筛选部门无关的用户不返回。
func TestUserRepoFindPage_DeptIDFilterIncludesDescendants(t *testing.T) {
	db := newUserDeptFilterTestDB(t)

	// 部门树:100 为根,110 为其子,111 为 110 之子;200 为独立部门。
	seedDept(t, db, 100, "root", "100")
	seedDept(t, db, 110, "child", "100,110")
	seedDept(t, db, 111, "grandchild", "100,110,111")
	seedDept(t, db, 200, "other", "200")

	seedUserInDept(t, db, 1, "root-user", 100)
	seedUserInDept(t, db, 2, "child-user", 110)
	seedUserInDept(t, db, 3, "grandchild-user", 111)
	seedUserInDept(t, db, 4, "other-user", 200)

	repo := NewUserRepository(db)
	ctx := context.Background()

	// 选根部门 → 根 + 所有后代部门的用户（3 个），不含独立部门 200。
	rootID := uint64(100)
	users, total, err := repo.FindPage(ctx, &request.UserQuery{
		PageRequest: app.PageRequest{Page: 1, PageSize: 10},
		DeptID:      &rootID,
	})
	if err != nil {
		t.Fatalf("FindPage(root): %v", err)
	}
	if total != 3 {
		t.Fatalf("root dept total = %d, want 3 (self+child+grandchild)", total)
	}
	got := map[uint64]bool{}
	for _, u := range users {
		got[u.ID] = true
	}
	if !got[1] || !got[2] || !got[3] || got[4] {
		t.Fatalf("root dept filter returned unexpected users: %v", got)
	}

	// 选中间部门 → 该部门 + 其后代（2 个），不含上级 100 与无关 200。
	childID := uint64(110)
	users2, total2, err := repo.FindPage(ctx, &request.UserQuery{
		PageRequest: app.PageRequest{Page: 1, PageSize: 10},
		DeptID:      &childID,
	})
	if err != nil {
		t.Fatalf("FindPage(child): %v", err)
	}
	if total2 != 2 {
		t.Fatalf("child dept total = %d, want 2 (self+grandchild)", total2)
	}
	got2 := map[uint64]bool{}
	for _, u := range users2 {
		got2[u.ID] = true
	}
	if got2[1] || !got2[2] || !got2[3] || got2[4] {
		t.Fatalf("child dept filter returned unexpected users: %v", got2)
	}
}
