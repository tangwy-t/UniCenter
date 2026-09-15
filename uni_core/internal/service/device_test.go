package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

func newDeviceTestEnv(t *testing.T) (*DeviceService, *gorm.DB, *agentmetrics.RawStore, *agentmetrics.LatestStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	node, _ := snowflake.New(1, logger.NewNop())
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.Device{}, &entity.DeviceResource{}); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{Step: 10 * time.Second, MaxPoints: 100})
	latest := agentmetrics.NewLatestStore(rdb)
	svc := NewDeviceService(repository.NewDeviceRepository(db), repository.NewDeviceResourceRepository(db),
		raw, latest, stubCfg{}, logger.NewNop())
	return svc, db, raw, latest
}

func TestDeviceListJoinsLatestWatermark(t *testing.T) {
	svc, db, raw, latest := newDeviceTestEnv(t)
	ctx := context.Background()

	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1001}, InstanceID: "i1", Hostname: "web-01",
		OS: "linux", Arch: "amd64", AgentVersion: "0.1.0", Status: entity.DeviceStatusEnabled}
	if err := db.Create(d).Error; err != nil {
		t.Fatal(err)
	}
	// 写入水位
	s := &agentproto.MetricsSample{T: time.Now().UnixMilli(), CPUUsedPercent: 42}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := latest.Set(ctx, 1001, s); err != nil {
		t.Fatal(err)
	}
	_ = raw

	pr, err := svc.List(ctx, &request.DeviceQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	items, ok := pr.List.([]response.DeviceListItem)
	if !ok {
		t.Fatalf("List 元素类型 = %T, want []response.DeviceListItem", pr.List)
	}
	if len(items) != 1 {
		t.Fatalf("列表长度 = %d, want 1", len(items))
	}
	if items[0].CPUUsedPercent == nil || *items[0].CPUUsedPercent != 42 {
		t.Fatalf("必须拼上 latest 水位: %+v", items[0])
	}
}

func TestDeviceDeletePurgesRedisAndResources(t *testing.T) {
	svc, db, raw, latest := newDeviceTestEnv(t)
	ctx := context.Background()

	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1001}, InstanceID: "i1", Hostname: "web-01",
		Status: entity.DeviceStatusEnabled}
	if err := db.Create(d).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.DeviceResource{ID: 2001, DeviceID: 1001, Kind: "disk", Name: "/"}).Error; err != nil {
		t.Fatal(err)
	}
	s := &agentproto.MetricsSample{T: time.Now().UnixMilli(), CPUUsedPercent: 1}
	_ = s.Validate()
	_ = raw.Append(ctx, 1001, s)
	_ = latest.Set(ctx, 1001, s)

	if err := svc.Delete(ctx, 1001); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 软删：查不到
	if _, err := svc.GetByID(ctx, 1001); err == nil {
		t.Fatal("软删后不应能查到")
	}
	// Redis 连带清理
	if got, _ := latest.Get(ctx, 1001); got != nil {
		t.Fatal("删除设备必须清理 latest")
	}
	if pts, _ := raw.Bucket(ctx, 1001, 0, 9_999_999_999_999); len(pts) != 0 {
		t.Fatal("删除设备必须清理原始窗")
	}
	// 资源行清理
	var n int64
	db.Model(&entity.DeviceResource{}).Where("device_id = ?", 1001).Count(&n)
	if n != 0 {
		t.Fatalf("删除设备必须清理资源维度行, got %d", n)
	}
	// 不存在 → NotFound
	if err := svc.Delete(ctx, 999999); err == nil {
		t.Fatal("删除不存在的设备必须报错")
	}
}

func TestDeviceEnableDisable(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1001}, InstanceID: "i1", Status: entity.DeviceStatusEnabled}
	db.Create(d)

	if err := svc.Disable(ctx, 1001); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err := svc.GetByID(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != entity.DeviceStatusDisabled {
		t.Fatalf("停用后 status = %d, want %d", got.Status, entity.DeviceStatusDisabled)
	}
	if err := svc.Enable(ctx, 1001); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got, _ = svc.GetByID(ctx, 1001)
	if got.Status != entity.DeviceStatusEnabled {
		t.Fatalf("启用后 status = %d", got.Status)
	}
	if err := svc.Enable(ctx, 999999); err == nil {
		t.Fatal("对不存在的设备启停必须报错")
	}
}

func TestDeviceResourcesStaleMarking(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1001}, InstanceID: "i1"}
	db.Create(d)

	// 一个新鲜、一个久未出现
	db.Create(&entity.DeviceResource{ID: 2001, DeviceID: 1001, Kind: "disk", Name: "/", LastSeenAt: time.Now()})
	db.Create(&entity.DeviceResource{ID: 2002, DeviceID: 1001, Kind: "disk", Name: "/old", LastSeenAt: time.Now().AddDate(0, 0, -120)})

	resp, err := svc.Resources(ctx, 1001, "disk")
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}
	if len(resp.List) != 2 {
		t.Fatalf("资源数 = %d, want 2", len(resp.List))
	}
	byName := map[string]bool{}
	for _, r := range resp.List {
		byName[r.Name] = r.Stale
	}
	if byName["/"] {
		t.Fatal("刚出现的资源不应标 stale")
	}
	if !byName["/old"] {
		t.Fatal("120 天未出现的资源必须标 stale（spec §8：消失资源要看得到）")
	}
}
