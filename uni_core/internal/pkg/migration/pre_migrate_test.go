package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
)

// newMigrationTestDB 建 sqlite 内存库并注册**生产同款回调**（id:generate / audit）。
//
// 必须注册回调：本文件的 TestRunCallsPreMigrateBeforeAutoMigrate 会跑真实的
// migration.Run，其中 v008 的菜单种子依赖雪花回调生成主键；回调缺失时 6 条菜单
// 的 ID 全为 0，第二条即主键冲突，Run 直接失败。
// 写法与 internal/pkg/migration/migrate_join_table_test.go 的 newJoinTableTestDB
// 保持一致（含按测试名隔离的命名内存库）。
func newMigrationTestDB(t *testing.T) *gorm.DB {
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
	return db
}

func TestRunPreMigrateExecutesInRegistrationOrder(t *testing.T) {
	// 该测试独占一个注册表快照：先清空，跑完恢复。
	saved := snapshotPreMigrate()
	t.Cleanup(func() { restorePreMigrate(saved) })
	resetPreMigrate()

	var order []string
	RegisterPreMigrate("second", func(context.Context, *gorm.DB, string) error {
		order = append(order, "second")
		return nil
	})
	RegisterPreMigrate("first", func(context.Context, *gorm.DB, string) error {
		order = append(order, "first")
		return nil
	})

	if err := RunPreMigrate(context.Background(), newMigrationTestDB(t)); err != nil {
		t.Fatalf("RunPreMigrate: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("钩子执行次数 = %d, want 2", len(order))
	}
	if order[0] != "second" {
		t.Fatalf("钩子必须按注册顺序执行, got %v", order)
	}
}

func TestRunPreMigratePropagatesError(t *testing.T) {
	saved := snapshotPreMigrate()
	t.Cleanup(func() { restorePreMigrate(saved) })
	resetPreMigrate()

	boom := errors.New("boom")
	RegisterPreMigrate("ok", func(context.Context, *gorm.DB, string) error { return nil })
	RegisterPreMigrate("bad", func(context.Context, *gorm.DB, string) error { return boom })

	err := RunPreMigrate(context.Background(), newMigrationTestDB(t))
	if !errors.Is(err, boom) {
		t.Fatalf("必须把钩子错误透传出来, got %v", err)
	}
}

func TestRunPreMigrateIsRepeatable(t *testing.T) {
	saved := snapshotPreMigrate()
	t.Cleanup(func() { restorePreMigrate(saved) })
	resetPreMigrate()

	calls := 0
	RegisterPreMigrate("counter", func(context.Context, *gorm.DB, string) error {
		calls++
		return nil
	})
	db := newMigrationTestDB(t)
	for i := 0; i < 3; i++ {
		if err := RunPreMigrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("钩子不参与版本号跳过机制，每次启动都必须执行: calls = %d, want 3", calls)
	}
}

func TestRunPreMigratePassesDialect(t *testing.T) {
	saved := snapshotPreMigrate()
	t.Cleanup(func() { restorePreMigrate(saved) })
	resetPreMigrate()

	var got string
	RegisterPreMigrate("dialect", func(_ context.Context, _ *gorm.DB, dialect string) error {
		got = dialect
		return nil
	})
	if err := RunPreMigrate(context.Background(), newMigrationTestDB(t)); err != nil {
		t.Fatal(err)
	}
	if got != "sqlite" {
		t.Fatalf("dialect = %q, want sqlite（来自 db.Dialector.Name()）", got)
	}
}

func TestRunCallsPreMigrateBeforeAutoMigrate(t *testing.T) {
	saved := snapshotPreMigrate()
	t.Cleanup(func() { restorePreMigrate(saved) })
	resetPreMigrate()

	// 钩子内建一张 AutoMigrate 清单里没有的表；
	// 若钩子在 MigrateAll 之后才跑，本断言仍然成立 —— 所以真正要断言的是
	// 「钩子看到了一个尚无迁移表的结构」，即它先于 MigrateAll。
	var tablesAtHookTime int64
	RegisterPreMigrate("probe", func(_ context.Context, db *gorm.DB, _ string) error {
		return db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tablesAtHookTime).Error
	})

	db := newMigrationTestDB(t)
	if err := Run(db, logger.NewNop()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// MigrateAll 会建出 sys_user 等表；钩子执行时它们还不存在。
	var userTableAtHook int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sys_user'").Scan(&userTableAtHook).Error; err != nil {
		t.Fatal(err)
	}
	if userTableAtHook != 1 {
		t.Fatal("前置条件不成立：AutoMigrate 应当已建出 sys_user")
	}
	if tablesAtHookTime >= 10 {
		t.Fatalf("钩子执行时已有 %d 张表，说明它跑在 AutoMigrate 之后了", tablesAtHookTime)
	}
}

// 若 package migration 未导出内部状态访问器，本测试需要它。
// 直接在 pre_migrate_test.go 中以内联方式访问包级变量（同包测试）。
func snapshotPreMigrate() []namedPreMigrate {
	preMigrateMu.Lock()
	defer preMigrateMu.Unlock()
	out := make([]namedPreMigrate, len(preMigrates))
	copy(out, preMigrates)
	return out
}

func restorePreMigrate(saved []namedPreMigrate) {
	preMigrateMu.Lock()
	defer preMigrateMu.Unlock()
	preMigrates = saved
}

func resetPreMigrate() {
	preMigrateMu.Lock()
	defer preMigrateMu.Unlock()
	preMigrates = nil
}
