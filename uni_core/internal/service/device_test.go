package service

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
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

// ── 错误遮蔽回归（Task 9 上报的观察点）────────────────────────────────
//
// 背景：Enable/Disable 曾把 SetStatus 的**任何**错误都映射成 NotFound，
// 于是「DB 故障 / 连接断开 / 约束冲突」也会回给前端 404 —— 运营看到
// 「设备不存在」去排查设备，而真正的问题在数据库。下面两条测试把
// 「未命中 → 404」与「其它错误 → 500」钉死成两个不同的结果。

// stubDeviceRepo 是可注入错误的 DeviceRepository 桩（只为触发错误分支）。
type stubDeviceRepo struct {
	findErr      error
	findDevice   *entity.Device
	setStatusErr error
	deleteErr    error
}

func (s *stubDeviceRepo) FindByID(context.Context, uint64) (*entity.Device, error) {
	return s.findDevice, s.findErr
}

func (s *stubDeviceRepo) FindPage(context.Context, *request.DeviceQuery, time.Time) ([]entity.Device, int64, error) {
	return nil, 0, s.findErr
}

func (s *stubDeviceRepo) SetStatus(context.Context, uint64, int8) error { return s.setStatusErr }
func (s *stubDeviceRepo) Delete(context.Context, uint64) error          { return s.deleteErr }

// stubResourceRepo 是 DeviceResourceRepository 的最小桩。
type stubResourceRepo struct {
	deleteErr error
}

func (s *stubResourceRepo) ListByDevice(context.Context, uint64, string, time.Time) ([]entity.DeviceResource, error) {
	return nil, nil
}
func (s *stubResourceRepo) DeleteByDevice(context.Context, uint64) error { return s.deleteErr }

// stubPurger 是 DeviceRedisPurger 的最小桩。
type stubPurger struct{ err error }

func (s stubPurger) Purge(context.Context, uint64) error { return s.err }

// stubLatestReader 是 DeviceLatestReader 的最小桩（未命中一律 nil）。
type stubLatestReader struct{ err error }

func (s stubLatestReader) GetMany(context.Context, []uint64) (map[uint64]*agentmetrics.LatestSummary, error) {
	return nil, s.err
}

// appErrStatus 把 service 返回的错误归一成 HTTP 状态；非 AppError 记为 0。
func appErrStatus(err error) int {
	var ae *apperror.AppError
	if errors.As(err, &ae) {
		return ae.HTTPStatus
	}
	return 0
}

func TestDeviceEnableDisableMapsRepositoryMissToNotFound(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	if err := db.Create(&entity.Device{
		BaseEntity: entity.BaseEntity{ID: 1001}, InstanceID: "i1", Status: entity.DeviceStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 未命中（设备不存在）：仓储返回 repository.ErrNotFound 哨兵 → 必须是 404
	miss := &stubDeviceRepo{setStatusErr: repository.ErrNotFound}
	svcMiss := NewDeviceService(miss, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, logger.NewNop())
	if err := svcMiss.Enable(ctx, 999999); appErrStatus(err) != 404 {
		t.Fatalf("未命中必须映射为 404 NotFound, got status=%d err=%v", appErrStatus(err), err)
	}
	if err := svcMiss.Disable(ctx, 999999); appErrStatus(err) != 404 {
		t.Fatalf("未命中必须映射为 404 NotFound, got status=%d err=%v", appErrStatus(err), err)
	}

	// 真仓储路径同样必须是 404（证明仓储确实返回哨兵，而不是靠桩伪造）
	if err := svc.Enable(ctx, 999999); appErrStatus(err) != 404 {
		t.Fatalf("真仓储未命中必须 404, got status=%d err=%v", appErrStatus(err), err)
	}
}

func TestDeviceEnableDisableMapsRepositoryFailureToInternal(t *testing.T) {
	ctx := context.Background()
	dbDown := fmt.Errorf("db: connection refused")

	for _, tc := range []struct {
		name string
		call func(svc *DeviceService) error
	}{
		{"Enable", func(svc *DeviceService) error { return svc.Enable(ctx, 1001) }},
		{"Disable", func(svc *DeviceService) error { return svc.Disable(ctx, 1001) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubDeviceRepo{setStatusErr: dbDown}
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, logger.NewNop())

			err := tc.call(svc)
			if err == nil {
				t.Fatal("仓储报错时 service 必须返回错误，不得静默成功")
			}
			status := appErrStatus(err)
			if status == 404 {
				t.Fatalf("DB 故障被伪装成 404（错误遮蔽回归）: %v", err)
			}
			if status != 500 {
				t.Fatalf("非未命中的仓储错误必须映射为 500 Internal, got status=%d err=%v", status, err)
			}
			if !errors.Is(err, dbDown) {
				t.Fatalf("Internal 必须携带原始 cause（便于日志定位）: %v", err)
			}
		})
	}
}
