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

// TestNormalizedIP 钉住展示层清洗：IPv4-mapped IPv6 必须归一成点分十进制。
//
// 为什么值得测：Go 在双栈监听下常给出 ::ffff:192.168.1.5，原样显示会让运维
// 以为设备走 IPv6（进而去查错误的方向）。同时确认「无法解析」时**原样返回**
// 而不是清空 —— IP 只用于展示，宁可显示怪字符串也不要凭空变成「—」。
func TestNormalizedIP(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"空串", "", ""},
		{"裸 IPv4", "192.168.1.5", "192.168.1.5"},
		{"IPv4-mapped IPv6", "::ffff:192.168.1.5", "192.168.1.5"},
		{"带空白", "  192.168.1.5  ", "192.168.1.5"},
		{"带端口", "192.168.1.5:51422", "192.168.1.5"},
		{"真 IPv6", "2001:db8::1", "2001:db8::1"},
		{"无法解析时原样保留", "not-an-ip", "not-an-ip"},
	}
	for _, c := range cases {
		if got := normalizedIP(c.in); got != c.want {
			t.Errorf("%s: normalizedIP(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestDeviceDetailExposesIPAndThreshold 钉住 I-1/I-5 两个字段真的进了详情响应。
func TestDeviceDetailExposesIPAndThreshold(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()

	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1002}, InstanceID: "i-ip",
		Hostname: "web-ip", OS: "linux", Arch: "amd64", AgentVersion: "0.1.0",
		Status: entity.DeviceStatusEnabled, PrimaryIP: "::ffff:10.1.2.3"}
	if err := db.Create(d).Error; err != nil {
		t.Fatal(err)
	}

	resp, err := svc.GetByID(ctx, 1002)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if resp.PrimaryIP != "10.1.2.3" {
		t.Fatalf("primaryIp = %q, want 10.1.2.3（IPv4-mapped 必须归一）", resp.PrimaryIP)
	}
	// stubCfg 未配置阈值 → 必须与 onlineSince 的缺省一致（30），
	// 且**不能是 0**（0 会让 UI 文案变成「0 秒内未上报即离线」）。
	if resp.OfflineThresholdSec != 30 {
		t.Fatalf("offlineThresholdSec = %d, want 30（未配置时的缺省）", resp.OfflineThresholdSec)
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

	// 三个资源：刚出现、久未出现但仍在枚举窗口内、超过枚举窗口
	db.Create(&entity.DeviceResource{ID: 2001, DeviceID: 1001, Kind: "disk", Name: "/", LastSeenAt: time.Now()})
	db.Create(&entity.DeviceResource{ID: 2002, DeviceID: 1001, Kind: "disk", Name: "/old",
		LastSeenAt: time.Now().AddDate(0, 0, -30)})
	db.Create(&entity.DeviceResource{ID: 2003, DeviceID: 1001, Kind: "disk", Name: "/gone",
		LastSeenAt: time.Now().AddDate(0, 0, -120)})

	resp, err := svc.Resources(ctx, 1001, "disk")
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}
	// S1 修正：**超过枚举窗口（90d）的资源不再枚举**。
	// 旧断言要求 120 天的资源必须返回（且被标 stale）——那是错的：spec §8 明文
	// 「设过期（如 90d 未出现即不再枚举）」，否则已卸载的挂载点永久堆在 drill 下拉里。
	// 现在 90d 是过滤下界，Stale 标记改用**另一个更短的阈值**（1 天，见
	// service/device.go 的 resourceStaleMarkDays），用于标出「窗口内但久未出现」。
	if len(resp.List) != 2 {
		t.Fatalf("资源数 = %d, want 2（120 天未出现的必须不再枚举）: %+v", len(resp.List), resp.List)
	}
	byName := map[string]bool{}
	for _, r := range resp.List {
		byName[r.Name] = r.Stale
	}
	if _, enumerated := byName["/gone"]; enumerated {
		t.Fatal("超过 90 天未出现的资源不得再出现在枚举结果里")
	}
	if byName["/"] {
		t.Fatal("刚出现的资源不应标 stale")
	}
	if !byName["/old"] {
		t.Fatal("窗口内但久未出现（30 天）的资源必须 Stale=true 且**仍枚举**（spec §8：消失资源要看得到）")
	}
	// last_seen_at 必须照常返回（窗口内的行一个字段都不少）
	for _, r := range resp.List {
		if r.LastSeenAt <= 0 {
			t.Fatalf("行必须带 last_seen_at（unix 秒）: %+v", r)
		}
	}
}

// TestDeviceReadPathsMapRepositoryFailureToInternal：S5 —— 除 SetStatus 外，
// **读取路径**的存在性检查同样必须分辨「未命中」与「故障」。
//
// 回归价值：GetByID / Resources / Delete 曾写 `if err != nil → NotFound`，
// 于是 DB 故障（连接断开、超时、约束冲突）全部伪装成 404「设备不存在」。
func TestDeviceReadPathsMapRepositoryFailureToInternal(t *testing.T) {
	ctx := context.Background()
	dbDown := fmt.Errorf("db: connection refused")

	calls := []struct {
		name string
		call func(svc *DeviceService) error
	}{
		{"GetByID", func(svc *DeviceService) error { _, err := svc.GetByID(ctx, 1001); return err }},
		{"Resources", func(svc *DeviceService) error { _, err := svc.Resources(ctx, 1001, "disk"); return err }},
		{"Delete", func(svc *DeviceService) error { return svc.Delete(ctx, 1001) }},
	}

	for _, tc := range calls {
		t.Run(tc.name+"_故障→500", func(t *testing.T) {
			repo := &stubDeviceRepo{findErr: dbDown}
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, logger.NewNop())

			err := tc.call(svc)
			if err == nil {
				t.Fatal("仓储报错时必须返回错误，不得静默成功")
			}
			if status := appErrStatus(err); status != 500 {
				t.Fatalf("非未命中的仓储错误必须映射为 500 Internal, got status=%d err=%v", status, err)
			}
			if !errors.Is(err, dbDown) {
				t.Fatalf("Internal 必须携带原始 cause: %v", err)
			}
		})

		t.Run(tc.name+"_未命中→404", func(t *testing.T) {
			repo := &stubDeviceRepo{findErr: repository.ErrNotFound}
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, logger.NewNop())

			if status := appErrStatus(tc.call(svc)); status != 404 {
				t.Fatalf("仓储未命中必须映射为 404 NotFound, got status=%d", status)
			}
		})
	}

	// 真仓储路径：不存在的设备必须是 404（证明仓储确实返回哨兵，而不是靠桩伪造）。
	// 这条同时守卫 FindByID 的错误映射：查询路径若抛裸的 gorm.ErrRecordNotFound，
	// errors.Is(err, repository.ErrNotFound) 为假 → 会退化成 500。
	svc, db, _, _ := newDeviceTestEnv(t)
	if status := appErrStatus(mustErr2(svc.GetByID(ctx, 999999))); status != 404 {
		t.Fatalf("真仓储未命中必须 404, got status=%d", status)
	}
	if status := appErrStatus(mustErr2(svc.Resources(ctx, 999999, "disk"))); status != 404 {
		t.Fatalf("Resources 真仓储未命中必须 404, got status=%d", status)
	}
	if status := appErrStatus(svc.Delete(ctx, 999999)); status != 404 {
		t.Fatalf("Delete 真仓储未命中必须 404, got status=%d", status)
	}
	// 存在的设备走通（保证上面的 404 不是因为整条路径都报错）
	if err := db.Create(&entity.Device{
		BaseEntity: entity.BaseEntity{ID: 2001}, InstanceID: "i2", Status: entity.DeviceStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetByID(ctx, 2001); err != nil {
		t.Fatalf("存在的设备必须能取到详情: %v", err)
	}
}

// mustErr2 把 (resp, err) 折成 err（局部工具，避免与查询测试的 mustErr 重名）。
func mustErr2[T any](_ T, err error) error { return err }

// ─ 错误遮蔽回归（Task 9 上报的观察点）────────────────────────────────
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
