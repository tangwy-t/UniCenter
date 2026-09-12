package repository

import (
	"context"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"gorm.io/gorm"
)

func TestFindReadStatusMap(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	seed := func(db *gorm.DB) {
		published := util.Ptr[int8](entity.NoticeStatusPublished)
		// 公告 11/12 + 用户 7 的两条阅读行(11 已读,12 未读 但软删)
		if err := db.Create(&entity.SysNotice{BaseEntity: entity.BaseEntity{ID: 11}, Title: "A", Status: published}).Error; err != nil {
			t.Fatalf("seed notice: %v", err)
		}
		if err := db.Create(&entity.SysNotice{BaseEntity: entity.BaseEntity{ID: 12}, Title: "B", Status: published}).Error; err != nil {
			t.Fatalf("seed notice: %v", err)
		}
		rows := []entity.SysNoticeUser{
			{BaseEntity: entity.BaseEntity{ID: 21}, NoticeID: 11, UserID: 7, ReadStatus: entity.NoticeReadStatusRead, ReadTime: &now},
			{BaseEntity: entity.BaseEntity{ID: 22}, NoticeID: 12, UserID: 7, ReadStatus: entity.NoticeReadStatusUnread},
		}
		if err := db.Create(&rows).Error; err != nil {
			t.Fatalf("seed read rows: %v", err)
		}
		// 软删公告 12 的行:模拟"行已删除不参与"
		if err := db.Where("id = ?", 22).Delete(&entity.SysNoticeUser{}).Error; err != nil {
			t.Fatalf("soft delete: %v", err)
		}
	}

	t.Run("取到已读行", func(t *testing.T) {
		db := newNoticeReadAllDB(t)
		seed(db)
		repo := NewNoticeRepository(db)
		m, err := repo.FindReadStatusMap(ctx, 7, []uint64{11})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if v, ok := m[11]; !ok || v != entity.NoticeReadStatusRead {
			t.Fatalf("map[11] = %d, ok=%v", v, ok)
		}
	})

	t.Run("无行缺键", func(t *testing.T) {
		db := newNoticeReadAllDB(t)
		if err := db.Create(&entity.SysNotice{BaseEntity: entity.BaseEntity{ID: 13}, Title: "C", Status: util.Ptr[int8](entity.NoticeStatusPublished)}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		repo := NewNoticeRepository(db)
		m, err := repo.FindReadStatusMap(ctx, 7, []uint64{13})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if _, ok := m[13]; ok {
			t.Fatalf("map[13] should be absent, got %v", m)
		}
	})

	t.Run("软删行不参与", func(t *testing.T) {
		db := newNoticeReadAllDB(t)
		seed(db)
		repo := NewNoticeRepository(db)
		m, err := repo.FindReadStatusMap(ctx, 7, []uint64{11, 12})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if _, ok := m[12]; ok {
			t.Fatalf("map[12] should be absent after soft delete")
		}
		if v, ok := m[11]; !ok || v != entity.NoticeReadStatusRead {
			t.Fatalf("map[11] = %d, ok=%v", v, ok)
		}
	})
}
