package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		raw, latest, stubCfg{}, nil, nil, logger.NewNop())
	return svc, db, raw, latest
}

// TestDeviceListItemCarriesPrimaryIP 钉住「列表项带 IP」的投影：
// entity.PrimaryIP 必须经 normalizedIP 进 DeviceListItem（IPv4-mapped 归一）。
// 这是「按 IP 定位设备」在列表页能用的前提 —— 只在详情里有 IP 时，
// 排查得一台台点进详情。
func TestDeviceListItemCarriesPrimaryIP(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()

	d := &entity.Device{BaseEntity: entity.BaseEntity{ID: 1003}, InstanceID: "i-pip",
		Hostname: "web-pip", OS: "linux", Arch: "amd64", AgentVersion: "0.6.0",
		Status: entity.DeviceStatusEnabled, PrimaryIP: "::ffff:10.1.2.3"}
	if err := db.Create(d).Error; err != nil {
		t.Fatal(err)
	}

	pr, err := svc.List(ctx, &request.DeviceQuery{AgentVersion: "0.6.0"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	items, ok := pr.List.([]response.DeviceListItem)
	if !ok {
		t.Fatalf("List 元素类型 = %T, want []response.DeviceListItem", pr.List)
	}
	if len(items) != 1 {
		t.Fatalf("列表长度 = %d, want 1（agentVersion 精确筛选）", len(items))
	}
	if items[0].PrimaryIP != "10.1.2.3" {
		t.Fatalf("列表项 primaryIp = %q, want 10.1.2.3（IPv4-mapped 必须归一）", items[0].PrimaryIP)
	}
}

// TestDeviceListItemCarriesHostGroupAndTags 钉住「列表项带分组/标签」的投影：
// entity 的 group_name / tags 必须原样进 DeviceListItem（Tags 保持逗号分隔串，
// 服务端不拆分），且 group 筛选经 service 端到端生效。
func TestDeviceListItemCarriesHostGroupAndTags(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()

	rows := []*entity.Device{
		{BaseEntity: entity.BaseEntity{ID: 2001}, InstanceID: "i-g1", TokenHash: "h-g1", Hostname: "web-01",
			OS: "linux", Arch: "amd64", AgentVersion: "0.6.0",
			Status: entity.DeviceStatusEnabled, GroupName: "prod", Tags: "web,cn"},
		{BaseEntity: entity.BaseEntity{ID: 2002}, InstanceID: "i-g2", TokenHash: "h-g2", Hostname: "db-01",
			OS: "linux", Arch: "amd64", AgentVersion: "0.6.0",
			Status: entity.DeviceStatusEnabled, GroupName: "staging", Tags: "db"},
	}
	for _, d := range rows {
		if err := db.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 全量：两台都带出各自的分组/标签
	pr, err := svc.List(ctx, &request.DeviceQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	items, ok := pr.List.([]response.DeviceListItem)
	if !ok {
		t.Fatalf("List 元素类型 = %T, want []response.DeviceListItem", pr.List)
	}
	byID := map[uint64]response.DeviceListItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got := byID[2001]; got.Group != "prod" || got.Tags != "web,cn" {
		t.Fatalf("2001 分组/标签投影丢值: group=%q tags=%q", got.Group, got.Tags)
	}
	if got := byID[2002]; got.Group != "staging" || got.Tags != "db" {
		t.Fatalf("2002 分组/标签投影丢值: group=%q tags=%q", got.Group, got.Tags)
	}

	// 按分组筛选：只回 prod 那台，且其标签照常带出
	pr, err = svc.List(ctx, &request.DeviceQuery{Group: "prod"})
	if err != nil {
		t.Fatalf("List(group=prod): %v", err)
	}
	items, _ = pr.List.([]response.DeviceListItem)
	if len(items) != 1 || items[0].ID != 2001 {
		t.Fatalf("group=prod 应只回 2001, got %+v", items)
	}
	if items[0].Group != "prod" || items[0].Tags != "web,cn" {
		t.Fatalf("筛选后的项仍须带分组/标签: %+v", items[0])
	}
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

// TestDeviceDeletePurgesDockerKeys：删除设备必须连带清 docker 键（docker:state:<id>
// 与 docker:hosts 成员），否则已删设备会一直出现在主机切换器里（清单按集合枚举）。
//
// 两条断言各对应一个真实后果：
//   - 清理**被调用**（只断言「Delete 没报错」证明不了它发生过）；
//   - 清理**失败不阻断**删除（docker 键是可自愈的读面缓存，不该拦住设备删除）。
func TestDeviceDeletePurgesDockerKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("清理被调用", func(t *testing.T) {
		_, db, raw, latest := newDeviceTestEnv(t)
		if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
			InstanceID: "i1", Status: entity.DeviceStatusEnabled}).Error; err != nil {
			t.Fatal(err)
		}
		purger := &stubDockerPurger{}
		svc := NewDeviceService(repository.NewDeviceRepository(db),
			repository.NewDeviceResourceRepository(db), raw, latest, stubCfg{}, nil, purger, logger.NewNop())

		if err := svc.Delete(ctx, 1001); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(purger.purged) != 1 || purger.purged[0] != 1001 {
			t.Fatalf("删除设备必须连带清理 docker 键, purged=%v", purger.purged)
		}
	})

	t.Run("清理失败不阻断删除", func(t *testing.T) {
		_, db, raw, latest := newDeviceTestEnv(t)
		if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
			InstanceID: "i1", Status: entity.DeviceStatusEnabled}).Error; err != nil {
			t.Fatal(err)
		}
		purger := &stubDockerPurger{err: errors.New("redis down")}
		svc := NewDeviceService(repository.NewDeviceRepository(db),
			repository.NewDeviceResourceRepository(db), raw, latest, stubCfg{}, nil, purger, logger.NewNop())

		if err := svc.Delete(ctx, 1001); err != nil {
			t.Fatalf("docker 键清理失败不得阻断设备删除: %v", err)
		}
		if _, err := svc.GetByID(ctx, 1001); err == nil {
			t.Fatal("清理失败后设备仍必须被删掉")
		}
	})

	t.Run("未装配 docker 域时照常可删", func(t *testing.T) {
		_, db, raw, latest := newDeviceTestEnv(t)
		if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
			InstanceID: "i1", Status: entity.DeviceStatusEnabled}).Error; err != nil {
			t.Fatal(err)
		}
		svc := NewDeviceService(repository.NewDeviceRepository(db),
			repository.NewDeviceResourceRepository(db), raw, latest, stubCfg{}, nil, nil, logger.NewNop())

		if err := svc.Delete(ctx, 1001); err != nil {
			t.Fatalf("docker purger 为 nil（未装配）时必须照常删除: %v", err)
		}
	})
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
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())

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
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())

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
	findErr        error
	findDevice     *entity.Device
	setStatusErr   error
	updateGroupErr error
	deleteErr      error
}

func (s *stubDeviceRepo) FindByID(context.Context, uint64) (*entity.Device, error) {
	return s.findDevice, s.findErr
}

func (s *stubDeviceRepo) FindPage(context.Context, *request.DeviceQuery, time.Time) ([]entity.Device, int64, error) {
	return nil, 0, s.findErr
}

func (s *stubDeviceRepo) SetStatus(context.Context, uint64, int8) error { return s.setStatusErr }

func (s *stubDeviceRepo) UpdateGroupTags(context.Context, uint64, *string, *string) error {
	return s.updateGroupErr
}

func (s *stubDeviceRepo) Delete(context.Context, uint64) error { return s.deleteErr }

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

// stubDockerPurger 是 DeviceDockerPurger 的最小桩：记录被清理过的设备 id，
// 好让「删除设备确实连带清了 docker 键」这件事可断言（只断言没报错证明不了它被调用）。
type stubDockerPurger struct {
	purged []uint64
	err    error
}

func (s *stubDockerPurger) PurgeDevice(_ context.Context, deviceID uint64) error {
	s.purged = append(s.purged, deviceID)
	return s.err
}

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
	svcMiss := NewDeviceService(miss, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())
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
			svc := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())

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

// ── 主机分组 / 标签写路径（PATCH /devices/:id 的 service 半边）──────────────

func deviceStrPtr(s string) *string { return &s }

// TestDeviceServiceUpdateGroupTrimsAndValidates 钉住校验（④）：
//   - 两端空白必须被 trim（存进去的是归一后的值）；
//   - group ≤64、tags ≤255 按**字符数**计（CJK 按字符不按字节）；
//   - 超长 → 400（用户输入校验显式报错，不静默截断）。
func TestDeviceServiceUpdateGroupTrimsAndValidates(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
		InstanceID: "i1", Status: entity.DeviceStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}

	// trim 生效
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr("  prod  "), deviceStrPtr("  web,cn ")); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	got, _ := svc.GetByID(ctx, 1001)
	if got.Group != "prod" || got.Tags != "web,cn" {
		t.Fatalf("trim 未生效: group=%q tags=%q", got.Group, got.Tags)
	}

	// 边界：恰好 64 / 255 字符（CJK 计数按字符）通过
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr(strings.Repeat("测", 64)), deviceStrPtr(strings.Repeat("标", 255))); err != nil {
		t.Fatalf("恰好等于上限应通过: %v", err)
	}
	// 超长：65 / 256 字符 → 400
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr(strings.Repeat("测", 65)), nil); appErrStatus(err) != 400 {
		t.Fatalf("group 65 字符必须 400, got %d err=%v", appErrStatus(err), err)
	}
	if err := svc.UpdateGroup(ctx, 1001, nil, deviceStrPtr(strings.Repeat("标", 256))); appErrStatus(err) != 400 {
		t.Fatalf("tags 256 字符必须 400, got %d err=%v", appErrStatus(err), err)
	}
	// trim 后再判长：64 字符 + 两端空白 = 合法（证明判定发生在 trim 之后）
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr("  "+strings.Repeat("测", 64)+"  "), nil); err != nil {
		t.Fatalf("trim 后等于上限应通过: %v", err)
	}
}

// TestDeviceServiceUpdateGroupPartialSemantics 钉住部分更新语义：
// 只传 group 时 tags 不动；传空串时清空；双 nil 是无副作用 no-op。
func TestDeviceServiceUpdateGroupPartialSemantics(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
		InstanceID: "i1", GroupName: "prod", Tags: "web,cn",
		Status: entity.DeviceStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	read := func() (string, string) {
		d, err := repository.NewDeviceRepository(db).FindByID(ctx, 1001)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		return d.GroupName, d.Tags
	}

	// 只传 group：tags 不动
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr("staging"), nil); err != nil {
		t.Fatal(err)
	}
	if g, tg := read(); g != "staging" || tg != "web,cn" {
		t.Fatalf("只更新 group 时 tags 不得变动: group=%q tags=%q", g, tg)
	}

	// 只传 tags：group 不动
	if err := svc.UpdateGroup(ctx, 1001, nil, deviceStrPtr("db")); err != nil {
		t.Fatal(err)
	}
	if g, tg := read(); g != "staging" || tg != "db" {
		t.Fatalf("只更新 tags 时 group 不得变动: group=%q tags=%q", g, tg)
	}

	// 空串清空
	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr(""), deviceStrPtr("")); err != nil {
		t.Fatal(err)
	}
	if g, tg := read(); g != "" || tg != "" {
		t.Fatalf("空串必须清空: group=%q tags=%q", g, tg)
	}

	// 双 nil：无副作用 no-op
	if err := svc.UpdateGroup(ctx, 1001, nil, nil); err != nil {
		t.Fatalf("双 nil 应是无副作用 no-op: %v", err)
	}
	if g, tg := read(); g != "" || tg != "" {
		t.Fatalf("双 nil 不得改动: group=%q tags=%q", g, tg)
	}
}

// TestDeviceServiceUpdateGroupNotFoundAndErrors 钉住错误映射：
// 不存在的 id → 404（真仓储走哨兵）；DB 故障 → 500（不得伪装成 404）。
func TestDeviceServiceUpdateGroupNotFoundAndErrors(t *testing.T) {
	ctx := context.Background()

	// 真仓储：不存在的设备 → 404
	svc, _, _, _ := newDeviceTestEnv(t)
	if err := svc.UpdateGroup(ctx, 999999, deviceStrPtr("prod"), nil); appErrStatus(err) != 404 {
		t.Fatalf("不存在的设备必须 404, got %d err=%v", appErrStatus(err), err)
	}
	// 双 nil + 不存在的设备 → 同样 404（空对象也不能绕过存在性检查）
	if err := svc.UpdateGroup(ctx, 999999, nil, nil); appErrStatus(err) != 404 {
		t.Fatalf("双 nil + 不存在必须 404, got %d err=%v", appErrStatus(err), err)
	}

	// 桩：仓储故障 → 500 且带 cause
	dbDown := fmt.Errorf("db: connection refused")
	repo := &stubDeviceRepo{updateGroupErr: dbDown}
	svcErr := NewDeviceService(repo, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())
	err := svcErr.UpdateGroup(ctx, 1001, deviceStrPtr("prod"), nil)
	if appErrStatus(err) != 500 {
		t.Fatalf("DB 故障必须 500, got %d err=%v", appErrStatus(err), err)
	}
	if !errors.Is(err, dbDown) {
		t.Fatalf("Internal 必须携带 cause: %v", err)
	}

	// 桩：未命中哨兵 → 404
	repoMiss := &stubDeviceRepo{updateGroupErr: repository.ErrNotFound}
	svcMiss := NewDeviceService(repoMiss, &stubResourceRepo{}, stubPurger{}, stubLatestReader{}, stubCfg{}, nil, nil, logger.NewNop())
	if err := svcMiss.UpdateGroup(ctx, 999999, deviceStrPtr("prod"), nil); appErrStatus(err) != 404 {
		t.Fatalf("未命中哨兵必须 404, got %d err=%v", appErrStatus(err), err)
	}
}

// TestDeviceServiceUpdateGroupRoundTrip 端到端（⑥）：写完再 GET /devices 能读回新分组，
// 且 group 筛选命中该设备 —— 证明写路径与读路径在同一份数据上闭环。
func TestDeviceServiceUpdateGroupRoundTrip(t *testing.T) {
	svc, db, _, _ := newDeviceTestEnv(t)
	ctx := context.Background()
	if err := db.Create(&entity.Device{BaseEntity: entity.BaseEntity{ID: 1001},
		InstanceID: "i1", Hostname: "web-01", Status: entity.DeviceStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.UpdateGroup(ctx, 1001, deviceStrPtr("prod"), deviceStrPtr("web,cn")); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}

	// 详情读回
	detail, err := svc.GetByID(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Group != "prod" || detail.Tags != "web,cn" {
		t.Fatalf("详情未读回新分组: group=%q tags=%q", detail.Group, detail.Tags)
	}

	// 列表 + 分组筛选读回
	pr, err := svc.List(ctx, &request.DeviceQuery{Group: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := pr.List.([]response.DeviceListItem)
	if !ok {
		t.Fatalf("列表元素类型 = %T", pr.List)
	}
	if len(items) != 1 || items[0].ID != 1001 || items[0].Group != "prod" || items[0].Tags != "web,cn" {
		t.Fatalf("列表未按新分组读回: %+v", items)
	}
}
