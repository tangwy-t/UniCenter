package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"gorm.io/gorm"
)

// TestCreateWithRolesAssociation is a regression test for the production
// panic "reflect: call of reflect.Value.Field on slice Value" raised when
// POST /users created a user WITH roles: GORM saves the many2many association
// by internally issuing Create/Updates with a slice Dest, and the audit
// callbacks (audit:set_create) called schema.Field.Set on that slice.
func TestCreateWithRolesAssociation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Same callback set as production (id:generate + audit hooks).
	database.NewCallbacks(nil).Register(db)
	// Association("Roles") needs SetupJoinTable so the join table uses the real
	// SysUserRole schema (with id) — production registers it in migration.MigrateAll.
	db.SetupJoinTable(&entity.SysUser{}, "Roles", &entity.SysUserRole{})
	db.SetupJoinTable(&entity.SysRole{}, "Users", &entity.SysUserRole{})
	// SysUserRole must be migrated explicitly: production registers it in
	// MigrateAll, which is what gives sys_user_role its snowflake `id` column.
	// Letting GORM auto-create the join table from the many2many tag produces a
	// table without `id`, so every join-row insert would fail.
	if err := db.AutoMigrate(&entity.SysUser{}, &entity.SysRole{}, &entity.SysUserRole{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed one role (no user context → audit fields untouched).
	if err := db.Create(&entity.SysRole{
		BaseEntity: entity.BaseEntity{ID: 1},
		Name:       "admin", Code: "admin",
	}).Error; err != nil {
		t.Fatalf("seed role: %v", err)
	}

	repo := NewUserRepository(db)
	ctx := contextkeys.WithUserID(context.Background(), 42)

	user := &entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: 10},
		Username:   "18349930104",
		Password:   "hashed",
	}
	if err := repo.CreateWithRoles(ctx, user, []uint64{1}); err != nil {
		t.Fatalf("CreateWithRoles: %v", err)
	}

	// Join row must exist.
	var count int64
	if err := db.Table("sys_user_role").
		Where("user_id = ? AND role_id = ?", user.ID, 1).Count(&count).Error; err != nil {
		t.Fatalf("count join rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("join rows = %d, want 1", count)
	}

	// Audit fields on the created user must reflect the ctx user.
	if user.CreatedBy == nil || *user.CreatedBy != 42 {
		t.Fatalf("CreatedBy = %v, want 42", user.CreatedBy)
	}
	if user.UpdatedBy == nil || *user.UpdatedBy != 42 {
		t.Fatalf("UpdatedBy = %v, want 42", user.UpdatedBy)
	}
}
