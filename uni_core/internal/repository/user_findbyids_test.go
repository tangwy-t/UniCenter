package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"gorm.io/gorm"
)

func TestFindByIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(&entity.SysUser{}, &entity.SysDept{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dept := entity.SysDept{BaseEntity: entity.BaseEntity{ID: 1}, Name: "研发部"}
	_ = db.Create(&dept).Error
	_ = db.Create(&entity.SysUser{BaseEntity: entity.BaseEntity{ID: 10}, Username: "a", DeptID: &dept.ID}).Error
	_ = db.Create(&entity.SysUser{BaseEntity: entity.BaseEntity{ID: 20}, Username: "b", DeptID: &dept.ID}).Error

	repo := NewUserRepository(db)

	users, err := repo.FindByIDs(context.Background(), []uint64{10, 999})
	if err != nil {
		t.Fatalf("FindByIDs: %v", err)
	}
	if len(users) != 1 || users[0].Username != "a" {
		t.Fatalf("users = %+v", users)
	}
	if users[0].Dept == nil || users[0].Dept.Name != "研发部" {
		t.Fatalf("Dept 未预加载: %+v", users[0].Dept)
	}

	empty, err := repo.FindByIDs(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 ids 应返回空切片: %v/%v", empty, err)
	}
}
