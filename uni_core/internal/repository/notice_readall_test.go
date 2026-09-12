package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/snowflake"
	"gorm.io/gorm"
)

// TestMarkAllRead_UpsertsVisiblePublishedOnly 覆盖:全员公告补行、指定公告翻行、
// 已读幂等、草稿/撤回不动、他人行不受影响。
func TestMarkAllRead_UpsertsVisiblePublishedOnly(t *testing.T) {
	db := newNoticeReadAllDB(t)
	repo := NewNoticeRepository(db)
	ctx := context.Background()

	seedNoticeReadAll(t, db)

	if err := repo.MarkAllRead(ctx, 1); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}

	assertReadRow := func(noticeID, userID uint64, wantExists bool, wantRead bool) {
		t.Helper()
		var row entity.SysNoticeUser
		err := db.Where("notice_id = ? AND user_id = ?", noticeID, userID).First(&row).Error
		if !wantExists {
			if err == nil {
				t.Fatalf("notice %d user %d: row exists, want absent", noticeID, userID)
			}
			return
		}
		if err != nil {
			t.Fatalf("notice %d user %d: %v", noticeID, userID, err)
		}
		if (wantRead && row.ReadStatus != entity.NoticeReadStatusRead) ||
			(wantRead && row.ReadTime == nil) {
			t.Fatalf("notice %d user %d: read_status=%d(read_time nil=%v), want read",
				noticeID, userID, row.ReadStatus, row.ReadTime == nil)
		}
		if !wantRead && row.ReadStatus != entity.NoticeReadStatusUnread {
			t.Fatalf("notice %d user %d: read_status=%d, want unread", noticeID, userID, row.ReadStatus)
		}
	}

	// 全员已发布·无行 → 补行已读
	assertReadRow(10, 1, true, true)
	// 补插行必须拿到独立非零 ID(sqlite 自动递增、生产雪花回调):
	// 防回归「补行共用 id=0 撞主键 Duplicate entry '0'」。
	var fresh entity.SysNoticeUser
	if err := db.Where("notice_id = ? AND user_id = ?", 10, 1).First(&fresh).Error; err != nil {
		t.Fatalf("fresh row for notice 10: %v", err)
	}
	if fresh.ID == 0 || fresh.ID <= 4 {
		t.Fatalf("fresh row id = %d, want generated unique non-zero(>seed 4)", fresh.ID)
	}
	// 指定发布·未读行 → 翻已读
	assertReadRow(11, 1, true, true)
	// 已读行 → 幂等保持
	assertReadRow(12, 1, true, true)
	// 草稿 · 全员 → 不产生行
	assertReadRow(13, 1, false, false)
	// 全员已发布·已有已读行 → 幂等保持
	assertReadRow(14, 1, true, true)
	// 他人(用户 2)对全员公告 10 的行不受本次影响
	assertReadRow(10, 2, true, false)
}

// TestMarkAllRead_NoVisibleNotices 无可见公告也不报错。
func TestMarkAllRead_NoVisibleNotices(t *testing.T) {
	db := newNoticeReadAllDB(t)
	repo := NewNoticeRepository(db)
	if err := repo.MarkAllRead(context.Background(), 99); err != nil {
		t.Fatalf("MarkAllRead empty: %v", err)
	}
}

func newNoticeReadAllDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 注册真实雪花节点:与生产一致,由 id:generate 回调补插行填非零 ID
	// (sys_notice_user.id 无自增,直接插入 0 会在 MySQL 撞主键)。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.SysNotice{}, &entity.SysNoticeUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedNoticeReadAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	pub := entity.NoticeStatusPublished
	draft := entity.NoticeStatusDraft
	all := entity.NoticePublishTypeAll
	custom := entity.NoticePublishTypeCustom
	pt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	notices := []entity.SysNotice{
		{BaseEntity: entity.BaseEntity{ID: 10}, Title: "A 全员已发布无行", Status: &pub, PublishType: &all, PublishTime: &pt},
		{BaseEntity: entity.BaseEntity{ID: 11}, Title: "B 指定未读行", Status: &pub, PublishType: &custom, PublishTime: &pt},
		{BaseEntity: entity.BaseEntity{ID: 12}, Title: "C 指定已读行", Status: &pub, PublishType: &custom, PublishTime: &pt},
		{BaseEntity: entity.BaseEntity{ID: 13}, Title: "D 草稿全员", Status: &draft, PublishType: &all, PublishTime: &pt},
		{BaseEntity: entity.BaseEntity{ID: 14}, Title: "E 全员已有已读行", Status: &pub, PublishType: &all, PublishTime: &pt},
	}
	for i := range notices {
		if err := db.Create(&notices[i]).Error; err != nil {
			t.Fatalf("seed notice: %v", err)
		}
	}
	unread := entity.NoticeReadStatusUnread
	read := entity.NoticeReadStatusRead
	rt := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	rows := []entity.SysNoticeUser{
		{BaseEntity: entity.BaseEntity{ID: 1}, NoticeID: 11, UserID: 1, ReadStatus: unread},              // → 翻已读
		{BaseEntity: entity.BaseEntity{ID: 2}, NoticeID: 12, UserID: 1, ReadStatus: read, ReadTime: &rt}, // 幂等
		{BaseEntity: entity.BaseEntity{ID: 3}, NoticeID: 14, UserID: 1, ReadStatus: read, ReadTime: &rt}, // 幂等
		{BaseEntity: entity.BaseEntity{ID: 4}, NoticeID: 10, UserID: 2, ReadStatus: unread},              // 他人,不动
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed notice_user: %v", err)
		}
	}
}
