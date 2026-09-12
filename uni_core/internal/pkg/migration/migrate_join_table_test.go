package migration

import (
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/snowflake"
	"gorm.io/gorm"
)

// snowflakeIDMin 是雪花 ID 的下界(约 2020-01-01 的毫秒时间戳 << 22)。
//
// 断言必须用它, 而不是 `id != 0`: id 省略时 MySQL(STRICT_TRANS_TABLES)会直接
// 报错, 但 SQLite 会退回 rowid(1、2、3…)。若只判 != 0, 在 SQLite 上测试会因
// rowid 兜底而恒通过, 完全检测不到 SetupJoinTable 缺失 —— 这一点已实测确认。
const snowflakeIDMin uint64 = 1 << 52

// newJoinTableTestDB 建 sqlite 内存库并注册生产同款回调(id:generate/audit),
// 再执行 MigrateAll(含 setupJoinTables)。
func newJoinTableTestDB(t *testing.T) *gorm.DB {
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
	if err := MigrateAll(db); err != nil {
		t.Fatalf("MigrateAll: %v", err)
	}
	return db
}

// TestJoinTableSetups_CoverAllMany2Many 是防漏守卫: 实体上每声明一个
// `many2many:` 关联, 就必须在 joinTableSetups 里注册对应 join 实体。
//
// 漏注册不会编译失败、不会报错, 只会让该关联的 join 行拿不到雪花 ID,
// 再叠加 GORM 的 OnConflict{DoNothing} 变成静默丢数据 ——
// 即"给用户分配多个角色只生效第一个且无任何报错"的成因。
func TestJoinTableSetups_CoverAllMany2Many(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range joinTableSetups {
		modelType := reflect.TypeOf(s.model).Elem()
		registered[modelType.Name()+"."+s.field] = true
	}

	found := 0
	for _, model := range []interface{}{
		&entity.SysUser{}, &entity.SysRole{}, &entity.SysMenu{}, &entity.SysDept{},
	} {
		rt := reflect.TypeOf(model).Elem()
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("gorm")
			if !containsMany2Many(tag) {
				continue
			}
			found++
			key := rt.Name() + "." + rt.Field(i).Name
			if !registered[key] {
				t.Errorf("many2many relation %s is missing from joinTableSetups; "+
					"its join rows would silently keep ID 0", key)
			}
		}
	}
	if found == 0 {
		t.Fatal("no many2many relations found — guard is not actually checking anything")
	}
}

func containsMany2Many(tag string) bool {
	return len(tag) >= 10 && indexOf(tag, "many2many:") >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestMigrateAll_JoinRowsGetSnowflakeID 端到端验证: 经 SetupJoinTable 注册后,
// GORM 的关联写入(Roles)也会经过 id:generate 回调, join 行拿到真正的雪花 ID。
//
// 未注册时 GORM 合成的 schema 无 ID 字段, LookUpField("ID") 返回 nil,
// 回调安静跳过 —— 这正是该缺陷的根因。
func TestMigrateAll_JoinRowsGetSnowflakeID(t *testing.T) {
	db := newJoinTableTestDB(t)

	mustCreate(t, db, &entity.SysRole{BaseEntity: entity.BaseEntity{ID: 1}, Name: "admin", Code: "admin"})
	mustCreate(t, db, &entity.SysRole{BaseEntity: entity.BaseEntity{ID: 2}, Name: "dev", Code: "dev"})
	mustCreate(t, db, &entity.SysUser{BaseEntity: entity.BaseEntity{ID: 10}, Username: "alice", Password: "x"})

	// 走 GORM 关联写入(即先前出问题的那条路径)。
	if err := db.Model(&entity.SysUser{BaseEntity: entity.BaseEntity{ID: 10}}).
		Association("Roles").Replace([]entity.SysRole{
		{BaseEntity: entity.BaseEntity{ID: 1}},
		{BaseEntity: entity.BaseEntity{ID: 2}},
	}); err != nil {
		t.Fatalf("Association.Replace: %v", err)
	}

	var rows []entity.SysUserRole
	if err := db.Table("sys_user_role").Where("user_id = ?", 10).Find(&rows).Error; err != nil {
		t.Fatalf("load join rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("join rows = %d, want 2 (second role was silently dropped)", len(rows))
	}
	for _, r := range rows {
		if r.ID < snowflakeIDMin {
			t.Fatalf("join row (user=%d, role=%d) has ID %d, not a snowflake ID: "+
				"SetupJoinTable is not in effect, id:generate callback never ran", r.UserID, r.RoleID, r.ID)
		}
	}
}

// TestMigrateAll_RoleJoinTablesRegistered 覆盖另外两张 join 表:
// sys_role_menu / sys_role_dept 同样依赖 setupJoinTables 才能拿到雪花 ID。
func TestMigrateAll_RoleJoinTablesRegistered(t *testing.T) {
	db := newJoinTableTestDB(t)

	mustCreate(t, db, &entity.SysRole{BaseEntity: entity.BaseEntity{ID: 1}, Name: "dev", Code: "dev"})
	mustCreate(t, db, &entity.SysMenu{BaseEntity: entity.BaseEntity{ID: 5}, Name: "menu"})
	mustCreate(t, db, &entity.SysDept{BaseEntity: entity.BaseEntity{ID: 7}, Name: "dept"})

	role := &entity.SysRole{BaseEntity: entity.BaseEntity{ID: 1}}
	if err := db.Model(role).Association("Menus").Replace(
		[]entity.SysMenu{{BaseEntity: entity.BaseEntity{ID: 5}}}); err != nil {
		t.Fatalf("Association(Menus).Replace: %v", err)
	}
	if err := db.Model(role).Association("Depts").Replace(
		[]entity.SysDept{{BaseEntity: entity.BaseEntity{ID: 7}}}); err != nil {
		t.Fatalf("Association(Depts).Replace: %v", err)
	}

	for _, table := range []string{"sys_role_menu", "sys_role_dept"} {
		var id uint64
		if err := db.Table(table).Select("id").Limit(1).Scan(&id).Error; err != nil {
			t.Fatalf("load %s: %v", table, err)
		}
		if id < snowflakeIDMin {
			t.Fatalf("%s row has ID %d, not a snowflake ID: "+
				"join table not registered via SetupJoinTable", table, id)
		}
	}
}

func mustCreate(t *testing.T, db *gorm.DB, v interface{}) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("create %T: %v", v, err)
	}
}
