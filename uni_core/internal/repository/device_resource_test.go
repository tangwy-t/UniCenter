package repository

import (
	"context"
	"errors"
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

	// 枚举窗口下界之后：全部新鲜，两行都在
	fresh, err := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 2 {
		t.Fatalf("新鲜过滤应保留 2 个, got %d", len(fresh))
	}

	// 枚举窗口下界晚于 last_seen_at（即「超过窗口未再被观测到」）→ **不再枚举**。
	// 这是 S1 的核心断言：旧行为忽略 staleBefore、返回全部行，于是 200 天前的
	// 挂载点永久堆在 drill 下拉里（spec §8 明文要求「设过期…即不再枚举」）。
	stale, err := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("超过枚举窗口的资源必须被 SQL 过滤掉（不得再枚举）, got %d 行: %+v", len(stale), stale)
	}
	// 反向：边界本身必须**包含**（last_seen_at >= staleBefore 是闭区间下界）
	atBoundary, err := repo.ListByDevice(ctx, 1001, entity.ResourceKindDisk, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(atBoundary) != 2 {
		t.Fatalf("last_seen_at == staleBefore 必须仍枚举（闭区间）, got %d", len(atBoundary))
	}
}

// TestResolveIDMissReturnsNotFoundSentinel 守卫 S5 的**判别基础**：
// service 层用 errors.Is(err, repository.ErrNotFound) 把「未命中」与「DB 故障」
// 分开（前者空结果/404，后者 500）。若仓储在查询路径上抛裸的
// gorm.ErrRecordNotFound，这个判别**永远为假**（哨兵包装 gorm 错误，反向不成立），
// 于是真实的「资源不存在」被当成 DB 故障映射成 500 —— 与「DB 故障伪装 404」
// 是同一个坑的两面。
func TestResolveIDMissReturnsNotFoundSentinel(t *testing.T) {
	db := newResourceTestDB(t)
	repo := NewDeviceResourceRepository(db)
	ctx := context.Background()

	_, err := repo.ResolveID(ctx, 1001, entity.ResourceKindDisk, "/nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("未命中必须返回 ErrNotFound 哨兵, got %v", err)
	}
	// 向后兼容：哨兵包装了 gorm.ErrRecordNotFound，既有判别继续成立
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("哨兵必须仍匹配 gorm.ErrRecordNotFound（既有调用方的契约）, got %v", err)
	}

	// 命中时不得误报未命中
	if err := repo.UpsertSeen(ctx, []entity.DeviceResource{
		{ID: 3001, DeviceID: 1001, Kind: entity.ResourceKindDisk, Name: "/"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	id, err := repo.ResolveID(ctx, 1001, entity.ResourceKindDisk, "/")
	if err != nil || id != 3001 {
		t.Fatalf("命中时 ResolveID = (%d, %v), want (3001, nil)", id, err)
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
