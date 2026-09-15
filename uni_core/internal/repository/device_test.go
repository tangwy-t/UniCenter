package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

func newDeviceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(&entity.Device{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedDevice(t *testing.T, db *gorm.DB, id uint64, instanceID, tokenHash, hostname string, seen *time.Time) {
	t.Helper()
	d := entity.Device{
		BaseEntity: entity.BaseEntity{ID: id},
		InstanceID: instanceID, TokenHash: tokenHash, Hostname: hostname,
		OS: "linux", Arch: "amd64", AgentVersion: "0.1.0",
		Status: entity.DeviceStatusEnabled, LastSeenAt: seen,
	}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestDeviceFindByInstanceIDAndTokenHash(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-a", "web-01", nil)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	got, err := repo.FindByInstanceID(ctx, "inst-a")
	if err != nil {
		t.Fatalf("FindByInstanceID: %v", err)
	}
	if got.ID != 1001 {
		t.Fatalf("id = %d, want 1001", got.ID)
	}
	if _, err := repo.FindByTokenHash(ctx, "hash-a"); err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	// 未命中：软删语义下必须是 NotFound 级别的错误（GORM ErrRecordNotFound 亦接受）
	if _, err := repo.FindByInstanceID(ctx, "nope"); err == nil {
		t.Fatal("未命中必须返回错误")
	}
}

func TestDeviceInstanceIDIsUnique(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-a", "web-01", nil)
	dup := entity.Device{BaseEntity: entity.BaseEntity{ID: 1002}, InstanceID: "inst-a", TokenHash: "hash-b"}
	err := db.Create(&dup).Error
	if err == nil {
		t.Fatal("instance_id 重复必须被唯一约束拒绝")
	}
	if !database.IsDuplicateKey(err) {
		t.Fatalf("重复键判定失败（enroll 幂等依赖它）: %v", err)
	}
}

func TestDeviceTouchUpdatesOnlyLastSeen(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-a", "web-01", nil)
	repo := NewDeviceRepository(db)
	at := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	if err := repo.Touch(context.Background(), 1001, at); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _ := repo.FindByInstanceID(context.Background(), "inst-a")
	if got.LastSeenAt == nil || !got.LastSeenAt.UTC().Equal(at) {
		t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, at)
	}
	if got.Hostname != "web-01" {
		t.Fatalf("Touch 不应改动其它列: hostname = %q", got.Hostname)
	}
}

func TestDeviceFindPageFiltersAndPagination(t *testing.T) {
	db := newDeviceTestDB(t)
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 9, 14, 9, 59, 45, 0, time.UTC)
	seedDevice(t, db, 1001, "i1", "h1", "web-01", &fresh)
	seedDevice(t, db, 1002, "i2", "h2", "web-02", &old)
	seedDevice(t, db, 1003, "i3", "h3", "db-01", nil)
	repo := NewDeviceRepository(db)
	ctx := context.Background()
	since := time.Date(2026, 9, 14, 9, 59, 30, 0, time.UTC)

	// 不带过滤：全部 3 台
	list, total, err := repo.FindPage(ctx, &request.DeviceQuery{}, since)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(list) != 3 {
		t.Fatalf("total=%d len=%d, want 3/3", total, len(list))
	}

	// hostname 模糊
	list, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Hostname: "web"}, since)
	if total != 2 {
		t.Fatalf("hostname=web 应命中 2 台, got %d", total)
	}

	// online=true：只算 last_seen_at >= since
	online := true
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Online: &online}, since)
	if total != 1 {
		t.Fatalf("online=true 应命中 1 台, got %d", total)
	}

	// online=false：含 NULL 与过期
	offline := false
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Online: &offline}, since)
	if total != 2 {
		t.Fatalf("online=false 应命中 2 台（含 NULL）, got %d", total)
	}

	// 分页
	q := &request.DeviceQuery{}
	q.Page, q.PageSize = 2, 2
	_, total, _ = repo.FindPage(ctx, q, since)
	if total != 3 {
		t.Fatalf("分页时 total 必须仍是全量 3, got %d", total)
	}
}
