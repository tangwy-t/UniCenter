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

// newUserRoleTestDB 建 sqlite 内存库并迁移 user/role(含 many2many join 表)。
// 使用独立 DSN(带缓存共享关闭)避免与其他 :memory: 测试串库。
func newUserRoleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:userrole?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 用真实 snowflake node 注册回调,与生产一致:批量 INSERT join 行时自动生成 ID。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	// 与生产(migration.MigrateAll)一致: 关联写入走 Association, 依赖
	// SetupJoinTable 把 join 表绑定到带 id 的实体 schema, 否则 id:generate
	// 回调在合成 schema 上拿不到 ID 字段、join 行 ID 恒为 0。
	db.SetupJoinTable(&entity.SysUser{}, "Roles", &entity.SysUserRole{})
	db.SetupJoinTable(&entity.SysRole{}, "Users", &entity.SysUserRole{})
	db.SetupJoinTable(&entity.SysRole{}, "Menus", &entity.SysRoleMenu{})
	db.SetupJoinTable(&entity.SysRole{}, "Depts", &entity.SysRoleDept{})
	if err := db.AutoMigrate(&entity.SysUser{}, &entity.SysRole{}, &entity.SysUserRole{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedRole(t *testing.T, db *gorm.DB, id uint64, code string) {
	t.Helper()
	if err := db.Create(&entity.SysRole{
		BaseEntity: entity.BaseEntity{ID: id},
		Name:       code, Code: code,
	}).Error; err != nil {
		t.Fatalf("seed role %s: %v", code, err)
	}
}

func seedUser(t *testing.T, db *gorm.DB, id uint64, username string, roleIDs ...uint64) {
	t.Helper()
	user := &entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: id},
		Username:   username,
		Password:   "hashed",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	for _, rid := range roleIDs {
		if err := db.Model(user).Association("Roles").Append(
			&entity.SysRole{BaseEntity: entity.BaseEntity{ID: rid}}); err != nil {
			t.Fatalf("seed user-role join: %v", err)
		}
	}
}

func TestUserRepoFindPage_RoleIDFilter(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice", 1)
	seedUser(t, db, 11, "bob", 1, 2)
	seedUser(t, db, 12, "carol") // 无角色
	repo := NewUserRepository(db)
	ctx := context.Background()

	roleID := uint64(1)
	users, total, err := repo.FindPage(ctx, &request.UserQuery{
		PageRequest: app.PageRequest{Page: 1, PageSize: 10},
		RoleID:      &roleID,
	})
	if err != nil {
		t.Fatalf("FindPage: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	ids := map[uint64]bool{}
	for _, u := range users {
		ids[u.ID] = true
	}
	if !ids[10] || !ids[11] || ids[12] {
		t.Fatalf("unexpected role-filtered users: %v", ids)
	}

	// 无 RoleID 过滤时返回全部
	users2, total2, err := repo.FindPage(ctx, &request.UserQuery{
		PageRequest: app.PageRequest{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("FindPage(all): %v", err)
	}
	if total2 != 3 || len(users2) != 3 {
		t.Fatalf("total(all) = %d, want 3", total2)
	}
}

func TestUserRepoAddUsersToRole_Idempotent(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice", 1)
	seedUser(t, db, 11, "bob")
	seedUser(t, db, 12, "carol")
	repo := NewUserRepository(db)
	ctx := context.Background()

	// 第一次:alice 已在该角色,只应新增 bob
	if err := repo.AddUsersToRole(ctx, 1, []uint64{10, 11}); err != nil {
		t.Fatalf("AddUsersToRole#1: %v", err)
	}
	// 第二次:重复提交 alice+bob,另加 carol,不应产生重复行
	if err := repo.AddUsersToRole(ctx, 1, []uint64{10, 11, 12}); err != nil {
		t.Fatalf("AddUsersToRole#2: %v", err)
	}

	var count int64
	if err := db.Table("sys_user_role").Where("role_id = ?", 1).Count(&count).Error; err != nil {
		t.Fatalf("count join rows: %v", err)
	}
	if count != 3 { // alice、bob、carol 各一行
		t.Fatalf("join rows = %d, want 3", count)
	}
}

// TestUserRepoReplaceRoles_MultipleRolesPersist 是"分配角色静默失效"的回归测试。
//
// 历史缺陷:ReplaceRoles 走 GORM 的 Association("Roles").Replace,join 行的 ID
// 由内部 join schema 生成,不经过全局 id:generate 回调,因此每行 ID 恒为 0。
// id 是主键,故只有第一行能落库 —— 给用户分配两个及以上角色时,第二个角色
// 撞主键 (Duplicate entry '0' for key 'PRIMARY') 被 Association 吞掉,
// 接口仍返回成功,表现为"角色只能赋予一次,且没有任何报错"。
func TestUserRepoReplaceRoles_MultipleRolesPersist(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice") // 无角色
	repo := NewUserRepository(db)
	ctx := context.Background()

	if err := repo.ReplaceRoles(ctx, 10, []uint64{1, 2}); err != nil {
		t.Fatalf("ReplaceRoles: %v", err)
	}

	assertUserRoles(t, db, 10, 1, 2)
	// join 行必须有非零雪花 ID:全零 ID 会再次触发主键冲突。
	assertJoinIDsNonZero(t, db, 10)
}

// TestUserRepoReplaceRoles_SecondRoleAfterFirst 复刻线上时序:先只赋予一个角色,
// 再追加第二个角色。旧实现下第二次调用静默丢失新增角色。
func TestUserRepoReplaceRoles_SecondRoleAfterFirst(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice")
	repo := NewUserRepository(db)
	ctx := context.Background()

	if err := repo.ReplaceRoles(ctx, 10, []uint64{1}); err != nil {
		t.Fatalf("ReplaceRoles#1: %v", err)
	}
	assertUserRoles(t, db, 10, 1)

	if err := repo.ReplaceRoles(ctx, 10, []uint64{1, 2}); err != nil {
		t.Fatalf("ReplaceRoles#2: %v", err)
	}
	assertUserRoles(t, db, 10, 1, 2)
	assertJoinIDsNonZero(t, db, 10)
}

// TestUserRepoReplaceRoles_ReplacesAndClears 锁定替换语义:重复提交同一角色不会
// 产生重复行;传空集合会清空全部角色。
func TestUserRepoReplaceRoles_ReplacesAndClears(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice", 1)
	repo := NewUserRepository(db)
	ctx := context.Background()

	// 重复提交同一角色:不应产生重复行(uk_user_role 唯一索引兜底)。
	if err := repo.ReplaceRoles(ctx, 10, []uint64{1, 1}); err != nil {
		t.Fatalf("ReplaceRoles(dup input): %v", err)
	}
	assertUserRoles(t, db, 10, 1)

	// 换成另一个角色:旧关联被删除。
	if err := repo.ReplaceRoles(ctx, 10, []uint64{2}); err != nil {
		t.Fatalf("ReplaceRoles(swap): %v", err)
	}
	assertUserRoles(t, db, 10, 2)

	// 空集合:清空全部角色。
	if err := repo.ReplaceRoles(ctx, 10, nil); err != nil {
		t.Fatalf("ReplaceRoles(clear): %v", err)
	}
	assertUserRoles(t, db, 10)
}

// TestUserRepoCreateWithRoles_MultipleRolesPersist 覆盖创建用户时的同一缺陷:
// 旧实现用 Association("Roles").Append,多角色只保留第一个。
func TestUserRepoCreateWithRoles_MultipleRolesPersist(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	repo := NewUserRepository(db)
	ctx := context.Background()

	user := &entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: 20},
		Username:   "newbie",
		Password:   "hashed",
	}
	if err := repo.CreateWithRoles(ctx, user, []uint64{1, 2}); err != nil {
		t.Fatalf("CreateWithRoles: %v", err)
	}

	assertUserRoles(t, db, 20, 1, 2)
	assertJoinIDsNonZero(t, db, 20)
}

// assertUserRoles 断言用户当前的 role_id 集合与 want 完全一致(与顺序无关)。
func assertUserRoles(t *testing.T, db *gorm.DB, userID uint64, want ...uint64) {
	t.Helper()
	var got []uint64
	if err := db.Table("sys_user_role").
		Where("user_id = ?", userID).
		Order("role_id").
		Pluck("role_id", &got).Error; err != nil {
		t.Fatalf("pluck user roles: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("user %d roles = %v, want %v", userID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("user %d roles = %v, want %v", userID, got, want)
		}
	}
}

// assertJoinIDsNonZero 断言 join 行都带有雪花 ID。ID 恒为 0 时,第二行必然
// 违反主键约束而被静默丢弃 —— 这正是本项目角色分配失效的根因。
//
// 判定用下界 1<<52 而非 != 0:SQLite 在 id 省略时会退回 rowid(1、2、3…),
// 若只判 != 0 会因 rowid 兜底而恒通过, 检测不到 SetupJoinTable 缺失。
func assertJoinIDsNonZero(t *testing.T, db *gorm.DB, userID uint64) {
	t.Helper()
	const snowflakeIDMin uint64 = 1 << 52
	var lowIDCount int64
	if err := db.Table("sys_user_role").
		Where("user_id = ? AND id < ?", userID, snowflakeIDMin).
		Count(&lowIDCount).Error; err != nil {
		t.Fatalf("count non-snowflake join rows: %v", err)
	}
	if lowIDCount != 0 {
		t.Fatalf("user %d has %d join row(s) without a snowflake ID: "+
			"SetupJoinTable missing, id:generate callback did not run", userID, lowIDCount)
	}
}

func TestUserRepoRemoveUsersFromRole_ScopedToRole(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	seedRole(t, db, 2, "dev")
	seedUser(t, db, 10, "alice", 1, 2) // 同属两个角色
	seedUser(t, db, 11, "bob", 1)
	repo := NewUserRepository(db)
	ctx := context.Background()

	if err := repo.RemoveUsersFromRole(ctx, 1, []uint64{10}); err != nil {
		t.Fatalf("RemoveUsersFromRole: %v", err)
	}

	// alice 与角色1的关联被删除、与角色2的关联保留;bob 不受影响
	for _, tc := range []struct {
		userID, roleID uint64
		want           int64
	}{
		{10, 1, 0},
		{10, 2, 1},
		{11, 1, 1},
	} {
		var count int64
		if err := db.Table("sys_user_role").
			Where("user_id = ? AND role_id = ?", tc.userID, tc.roleID).
			Count(&count).Error; err != nil {
			t.Fatalf("count join rows: %v", err)
		}
		if count != tc.want {
			t.Fatalf("join(user=%d, role=%d) = %d, want %d", tc.userID, tc.roleID, count, tc.want)
		}
	}
}

func TestUserRepoFindRoleCodeByID(t *testing.T) {
	db := newUserRoleTestDB(t)
	seedRole(t, db, 1, "admin")
	repo := NewUserRepository(db)

	code, err := repo.FindRoleCodeByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("FindRoleCodeByID: %v", err)
	}
	if code != "admin" {
		t.Fatalf("code = %q, want admin", code)
	}
}
