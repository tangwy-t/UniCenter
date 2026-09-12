package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"gorm.io/gorm"
)

// TestFindReadUsers_JoinPaginationSearch 验证已读用户查询:联结用户/部门、
// 按阅读时间倒序、分页与搜索过滤。
func TestFindReadUsers_JoinPaginationSearch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(
		&entity.SysNotice{}, &entity.SysNoticeUser{}, &entity.SysUser{}, &entity.SysDept{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 种子:部门 + 用户 + 已读记录
	dept := entity.SysDept{BaseEntity: entity.BaseEntity{ID: 7}, Name: "技术部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	alice := entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: 1},
		Username:   "alice", RealName: strPtr("Alice"), Phone: strPtr("13800000001"), DeptID: u64Ptr(7),
	}
	bob := entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: 2},
		Username:   "bob", RealName: strPtr("Bob"),
	}
	carol := entity.SysUser{
		BaseEntity: entity.BaseEntity{ID: 3},
		Username:   "carol", RealName: strPtr("Carol"),
	}
	for _, u := range []entity.SysUser{alice, bob, carol} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("seed user %s: %v", u.Username, err)
		}
	}
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	rows := []entity.SysNoticeUser{
		{NoticeID: 10, UserID: 2, ReadStatus: entity.NoticeReadStatusRead, ReadTime: &t2}, // bob 最新
		{NoticeID: 10, UserID: 1, ReadStatus: entity.NoticeReadStatusRead, ReadTime: &t1}, // alice 次之
		{NoticeID: 11, UserID: 3, ReadStatus: entity.NoticeReadStatusRead, ReadTime: &t1}, // 其他公告
	}
	for i := range rows {
		rows[i].BaseEntity.ID = uint64(i + 1)
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed read rows: %v", err)
	}

	repo := NewNoticeRepository(db)
	ctx := context.Background()

	// 1) 基础查询:公告 10 → 2 行,按阅读时间倒序
	q := &request.NoticeReadUsersQuery{PageRequest: pageReq(1, 10)}
	got, total, err := repo.FindReadUsers(ctx, 10, q)
	if err != nil {
		t.Fatalf("FindReadUsers: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(got) != 2 || got[0].UserID != 2 || got[1].UserID != 1 {
		t.Fatalf("rows = %+v, want [bob(2), alice(1)]", got)
	}
	if got[0].Username != "bob" || got[0].RealName != "Bob" {
		t.Fatalf("row0 = %+v", got[0])
	}
	if got[1].DeptName != "技术部" || got[1].Phone != "13800000001" {
		t.Fatalf("row1 = %+v, want dept/phone joined", got[1])
	}
	if got[0].ReadTime == nil {
		t.Fatalf("row0.readTime nil")
	}

	// 2) 搜索:按姓名/账号过滤
	qSearch := &request.NoticeReadUsersQuery{PageRequest: pageReq(1, 10), SearchValue: "ali"}
	gotS, totalS, err := repo.FindReadUsers(ctx, 10, qSearch)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if totalS != 1 || len(gotS) != 1 || gotS[0].UserID != 1 {
		t.Fatalf("search rows = %+v total = %d", gotS, totalS)
	}

	// 3) 分页
	qPage := &request.NoticeReadUsersQuery{PageRequest: pageReq(1, 1)}
	gotP, totalP, err := repo.FindReadUsers(ctx, 10, qPage)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if totalP != 2 || len(gotP) != 1 || gotP[0].UserID != 2 {
		t.Fatalf("page rows = %+v total = %d", gotP, totalP)
	}
}

func pageReq(page, size int) app.PageRequest {
	return app.PageRequest{Page: page, PageSize: size}
}

func strPtr(s string) *string { return &s }
func u64Ptr(v uint64) *uint64 { return &v }
