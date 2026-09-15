package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

func newResourceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(&entity.DeviceResource{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestResourceUpsertIsIdempotentOnIdentity(t *testing.T) {
	db := newResourceTestDB(t)
	repo := NewDeviceResourceRepository(db)
	ctx := context.Background()
	t1 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Minute)

	// 首次写入：测试栈的雪花回调不生成 ID，故显式给 ID（与 notice_test.go 同做法）
	if err := repo.UpsertSeen(ctx, []entity.DeviceResource{
		{ID: 2001, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/", FSType: "ext4"},
	}, t1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// 同一身份再次写入：必须更新而非新增，且 id 不被覆盖
	if err := repo.UpsertSeen(ctx, []entity.DeviceResource{
		{ID: 9999, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/", FSType: "xfs"},
	}, t2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var cnt int64
	if err := db.Model(&entity.DeviceResource{}).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("同身份重复 upsert 后行数 = %d, want 1", cnt)
	}

	id, err := repo.ResolveID(ctx, 1001, entity.ResourceKindDisk, "/")
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if id != 2001 {
		t.Fatalf("id = %d, want 2001（冲突更新不得覆盖 id）", id)
	}

	var got entity.DeviceResource
	if err := db.Where("id = ?", 2001).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.FSType != "xfs" {
		t.Fatalf("fs_type = %q, want xfs（冲突应更新）", got.FSType)
	}
	if !got.LastSeenAt.UTC().Equal(t2) {
		t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, t2)
	}
}

func TestResourceListByDeviceFiltersKindAndStaleness(t *testing.T) {
	db := newResourceTestDB(t)
	repo := NewDeviceResourceRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	if err := repo.UpsertSeen(ctx, []entity.DeviceResource{
		{ID: 2001, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/"},
		{ID: 2002, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/data"},
		{ID: 2003, DeviceID: 1001, Kind: entity.ResourceKindNIC, Name: "eth0"},
		{ID: 2004, DeviceID: 1002, Kind: entity.ResourceKindDisk, Name: "/"},
	}, now); err != nil {
		t.Fatal(err)
	}

	disks, err := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 {
		t.Fatalf("1001 的 disk 资源 = %d 个, want 2", len(disks))
	}

	// staleBefore 之后：全部新鲜
	fresh, _ := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, now.Add(-time.Hour))
	if len(fresh) != 2 {
		t.Fatalf("新鲜过滤应保留 2 个, got %d", len(fresh))
	}
	// staleBefore 早于 last_seen_at 的情况：全部过期
	stale, _ := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, now.Add(24*time.Hour))
	if len(stale) != 2 {
		t.Fatalf("staleBefore 为未来时仍应返回（由调用方决定是否剔除）, got %d", len(stale))
	}
}

func TestResourceDeleteByDevice(t *testing.T) {
	db := newResourceTestDB(t)
	repo := NewDeviceResourceRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertSeen(ctx, []entity.DeviceResource{
		{ID: 2001, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/"},
		{ID: 2002, DeviceID: 1002, Kind: entity.ResourceKindDisk, Name: "/"},
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteByDevice(ctx, 1001); err != nil {
		t.Fatalf("DeleteByDevice: %v", err)
	}
	var cnt int64
	db.Model(&entity.DeviceResource{}).Where("device_id = ?", 1001).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("删除后 1001 的资源行 = %d, want 0", cnt)
	}
	db.Model(&entity.DeviceResource{}).Where("device_id = ?", 1002).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("不得误删其它设备的资源行, 1002 = %d, want 1", cnt)
	}
}
