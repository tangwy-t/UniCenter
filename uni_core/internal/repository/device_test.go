package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

// errOtherRepoFailure 是「非未命中」错误样本：用于证明哨兵不会与任意错误混淆。
var errOtherRepoFailure = errors.New("repository: db unavailable")

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

// seedDeviceFull 在 seedDevice 之上补 primary_ip / agent_version：这两列的
// 值是本轮「列表带 IP + 按版本筛选 + 服务端排序」契约的主角，seedDevice 的
// 固定值（0.1.0 / 空 IP）无法构造出可比的行序与命中差。
func seedDeviceFull(t *testing.T, db *gorm.DB, id uint64, instanceID, tokenHash, hostname,
	primaryIP, agentVersion string, seen *time.Time) {
	t.Helper()
	d := entity.Device{
		BaseEntity: entity.BaseEntity{ID: id},
		InstanceID: instanceID, TokenHash: tokenHash, Hostname: hostname,
		OS: "linux", Arch: "amd64", AgentVersion: agentVersion, PrimaryIP: primaryIP,
		Status: entity.DeviceStatusEnabled, LastSeenAt: seen,
	}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// TestDeviceFindPagePrimaryIpAndAgentVersionFilters 钉住两个新筛选的语义：
//   - primaryIp 模糊（LIKE %v%，与 hostname 同语义 —— 定位场景按片段搜）；
//   - agentVersion **精确**（0.6 不得命中 0.6.0：版本是精确串）。
func TestDeviceFindPagePrimaryIpAndAgentVersionFilters(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedDeviceFull(t, db, 1001, "i1", "h1", "web-01", "10.1.1.10", "0.6.0", &seen)
	seedDeviceFull(t, db, 1002, "i2", "h2", "web-02", "10.1.1.100", "0.6.1", &seen)
	seedDeviceFull(t, db, 1003, "i3", "h3", "db-01", "192.168.7.5", "0.5.9", &seen)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	// primaryIp 模糊：片段 ".1.1." 命中两台
	_, total, err := repo.FindPage(ctx, &request.DeviceQuery{PrimaryIp: "10.1.1."}, seen)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("primaryIp=10.1.1. 应命中 2 台, got %d", total)
	}

	// agentVersion 精确：0.6 只命中 0.6（不得把 0.6.0/0.6.1 混进来）
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{AgentVersion: "0.6"}, seen)
	if total != 0 {
		t.Fatalf("agentVersion=0.6 精确匹配应命中 0 台, got %d", total)
	}
	list, total, _ := repo.FindPage(ctx, &request.DeviceQuery{AgentVersion: "0.6.0"}, seen)
	if total != 1 || len(list) != 1 || list[0].ID != 1001 {
		t.Fatalf("agentVersion=0.6.0 精确匹配应只命中 1001, got total=%d len=%d", total, len(list))
	}
}

// TestDeviceSortWhitelistAndFallback 钉住排序契约的**守卫**：
//  1. 白名单列映射正确：lastSeenAt→last_seen_at、hostname→hostname、
//     agentVersion→agent_version（各列按 asc 排出的顺序必须真的变）；
//  2. 非法 sortBy / sortOrder **静默回落默认**（last_seen_at DESC, id DESC），
//     与「不传排序」逐字同行为 —— 不抛错、不 400（旧客户端不能断）；
//  3. 任意方向都带 id DESC 锚（并列值不抖动）。
func TestDeviceSortWhitelistAndFallback(t *testing.T) {
	db := newDeviceTestDB(t)
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 9, 14, 9, 59, 45, 0, time.UTC)
	seedDeviceFull(t, db, 1001, "i1", "h1", "web-02", "10.0.0.1", "0.6.0", &fresh)
	seedDeviceFull(t, db, 1002, "i2", "h2", "web-01", "10.0.0.2", "0.5.9", &old)
	seedDeviceFull(t, db, 1003, "i3", "h3", "alpha-01", "10.0.0.3", "0.7.0", &old)
	repo := NewDeviceRepository(db)
	ctx := context.Background()
	idsOf := func(list []entity.Device) []uint64 {
		ids := make([]uint64, len(list))
		for i, d := range list {
			ids[i] = d.ID
		}
		return ids
	}

	// 白名单映射：hostname asc → web-01(1002) → web-02(1001) → alpha-01(1003)？否：
	// 字典序 alpha-01 < web-01 < web-02 → [1003, 1002, 1001]
	list, _, err := repo.FindPage(ctx, &request.DeviceQuery{SortBy: "hostname", SortOrder: "asc"}, old)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(list); !equalIDs(got, []uint64{1003, 1002, 1001}) {
		t.Fatalf("sortBy=hostname asc 的顺序 = %v, want [1003 1002 1001]", got)
	}

	// agentVersion asc → 0.5.9(1002) → 0.6.0(1001) → 0.7.0(1003)
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{SortBy: "agentVersion", SortOrder: "asc"}, old)
	if got := idsOf(list); !equalIDs(got, []uint64{1002, 1001, 1003}) {
		t.Fatalf("sortBy=agentVersion asc 的顺序 = %v, want [1002 1001 1003]", got)
	}

	// lastSeenAt 显式 desc = 默认：fresh(1001) 最前，old 两台按 id desc 锚 [1003, 1002]
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{SortBy: "lastSeenAt", SortOrder: "desc"}, old)
	if got := idsOf(list); !equalIDs(got, []uint64{1001, 1003, 1002}) {
		t.Fatalf("sortBy=lastSeenAt desc 的顺序 = %v, want [1001 1003 1002]", got)
	}

	// 不传排序：与显式默认逐字一致（评审回归点：默认排序不变）
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{}, old)
	if got := idsOf(list); !equalIDs(got, []uint64{1001, 1003, 1002}) {
		t.Fatalf("不传排序的顺序 = %v, want [1001 1003 1002]（last_seen_at DESC, id DESC）", got)
	}

	// 非法 sortBy（注入尝试也不得过白名单）→ 回落 last_seen_at desc
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{SortBy: "primary_ip; DROP TABLE device", SortOrder: "desc"}, old)
	if got := idsOf(list); !equalIDs(got, []uint64{1001, 1003, 1002}) {
		t.Fatalf("非法 sortBy 必须回落默认排序, got %v", got)
	}

	// 非法 sortOrder → 回落 desc（不是 asc：默认方向是 desc）
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{SortBy: "hostname", SortOrder: "DESC "}, old)
	if got := idsOf(list); !equalIDs(got, []uint64{1001, 1002, 1003}) {
		// hostname desc：web-02(1001) > web-01(1002) > alpha-01(1003)
		t.Fatalf("非法 sortOrder 必须回落 desc, got %v", got)
	}
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

	// 分页（既有的 Page=2/PageSize=2 语义：OFFSET = (2-1)*2 = 2 → 取到第 2 页）
	q := &request.DeviceQuery{}
	q.Page, q.PageSize = 2, 2
	list, total, _ = repo.FindPage(ctx, q, since)
	if total != 3 {
		t.Fatalf("分页时 total 必须仍是全量 3, got %d", total)
	}
	if len(list) != 1 {
		t.Fatalf("page2(offset=2, size=2) 必须返回剩余 1 条, got %d", len(list))
	}
	if list[0].ID == 1001 || list[0].ID == 1002 {
		t.Fatalf("page2 必须是不在第 1 页的那台设备, got %d", list[0].ID)
	}

	// 第 1 页：paginate 的 Offset/Limit 必须生效，且排序仍为 last_seen_at DESC（评审 F4 回归）
	q1 := &request.DeviceQuery{}
	q1.Page, q1.PageSize = 1, 2
	page1, total1, _ := repo.FindPage(ctx, q1, since)
	if total1 != 3 || len(page1) != 2 {
		t.Fatalf("page1 必须返回 2 条且 total 仍是 3, got len=%d total=%d", len(page1), total1)
	}
	pos := map[uint64]int{}
	for i, d := range page1 {
		pos[d.ID] = i
	}
	if pos[1001] > pos[1002] {
		t.Fatalf("last_seen_at 较新的 1001 必须排在 1002 之前, got %v", pos)
	}

	// status 精确匹配（启停态，与在线状态正交）
	enabled := entity.DeviceStatusEnabled
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Status: &enabled}, since)
	if total != 3 {
		t.Fatalf("status=启用 应命中 3 台, got %d", total)
	}
	disabled := entity.DeviceStatusDisabled
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Status: &disabled}, since)
	if total != 0 {
		t.Fatalf("status=停用 应命中 0 台, got %d", total)
	}
}

func TestDeviceFindByIDHitAndMiss(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-a", "web-01", nil)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	got, err := repo.FindByID(ctx, 1001)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ID != 1001 || got.InstanceID != "inst-a" {
		t.Fatalf("FindByID 返回不符: id=%d instance=%q", got.ID, got.InstanceID)
	}
	// 未命中必须是 NotFound 级别错误（IsAccepting 依赖它判定「设备不存在」）
	if _, err := repo.FindByID(ctx, 9999); err == nil {
		t.Fatal("未命中必须返回错误")
	}
}

func TestDeviceUpdateEnrollOnlyTouchesInventoryAndTokenHash(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	seedDevice(t, db, 1001, "inst-a", "hash-old", "web-01", &seen)
	// 停用态：重新 enroll **不得**把它自动启用
	if err := db.Model(&entity.Device{}).Where("id = ?", 1001).
		Update("status", entity.DeviceStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	before, err := repo.FindByID(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.UpdateEnroll(ctx, &entity.Device{
		BaseEntity: entity.BaseEntity{ID: 1001},
		Hostname:   "web-01-new", OS: "linux", Arch: "arm64", Kernel: "6.1.0",
		AgentVersion: "0.2.0", Platform: "ubuntu", PlatformVer: "22.04",
		CPUModel: "EPYC 7B13", CPUCores: 32, MemTotalMB: 65536, BootTime: 1_700_000_000,
		TokenHash: "hash-new",
		Status:    entity.DeviceStatusEnabled, // 故意传入启用态，断言它被忽略
	}); err != nil {
		t.Fatalf("UpdateEnroll: %v", err)
	}

	after, err := repo.FindByID(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	// 库存字段 + token_hash 必须更新
	if after.TokenHash != "hash-new" {
		t.Fatalf("token_hash = %q, want hash-new（轮换必须生效）", after.TokenHash)
	}
	if after.Hostname != "web-01-new" || after.Arch != "arm64" || after.Kernel != "6.1.0" {
		t.Fatalf("库存字段未更新: hostname=%q arch=%q kernel=%q", after.Hostname, after.Arch, after.Kernel)
	}
	if after.AgentVersion != "0.2.0" || after.Platform != "ubuntu" || after.PlatformVer != "22.04" {
		t.Fatalf("库存字段未更新: agentVersion=%q platform=%q platformVer=%q",
			after.AgentVersion, after.Platform, after.PlatformVer)
	}
	if after.CPUModel != "EPYC 7B13" || after.CPUCores != 32 || after.MemTotalMB != 65536 || after.BootTime != 1_700_000_000 {
		t.Fatalf("库存字段未更新: cpuModel=%q cores=%d mem=%v boot=%d",
			after.CPUModel, after.CPUCores, after.MemTotalMB, after.BootTime)
	}
	// status 不得被改动（停用机器重新 enroll 不自动启用）
	if after.Status != entity.DeviceStatusDisabled {
		t.Fatalf("status = %d, want %d（停用态不被 enroll 自动启用）",
			after.Status, entity.DeviceStatusDisabled)
	}
	// last_seen_at 不得被改动（那是 Touch 的职责）
	if after.LastSeenAt == nil || !after.LastSeenAt.UTC().Equal(seen) {
		t.Fatalf("last_seen_at = %v, want 保持 %v", after.LastSeenAt, seen)
	}
	if before.LastSeenAt == nil || !before.LastSeenAt.UTC().Equal(after.LastSeenAt.UTC()) {
		t.Fatal("last_seen_at 在 UpdateEnroll 前后必须一致")
	}
}

func TestDeviceUpdateEnrollWritesZeroValuesAndMissingRowErrors(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-old", "web-01", nil)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	// map 形式的 Updates 必须让零值也落库（hostname 变空是合法上报）
	if err := repo.UpdateEnroll(ctx, &entity.Device{
		BaseEntity: entity.BaseEntity{ID: 1001},
		TokenHash:  "hash-empty",
	}); err != nil {
		t.Fatalf("UpdateEnroll: %v", err)
	}
	got, err := repo.FindByID(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "" {
		t.Fatalf("零值必须被写入（struct 形式 Updates 会跳过零值）: hostname = %q", got.Hostname)
	}
	if got.CPUCores != 0 || got.MemTotalMB != 0 {
		t.Fatalf("零值必须被写入: cores=%d mem=%v", got.CPUCores, got.MemTotalMB)
	}
	if got.TokenHash != "hash-empty" {
		t.Fatalf("token_hash = %q", got.TokenHash)
	}

	// 不存在的行必须报 NotFound，而不是静默成功
	if err := repo.UpdateEnroll(ctx, &entity.Device{
		BaseEntity: entity.BaseEntity{ID: 9999}, TokenHash: "hash-x",
	}); err == nil {
		t.Fatal("更新不存在的设备必须返回错误")
	}
}

// TestDeviceSetStatusAndDeleteReturnNotFoundSentinel 锁定「未命中」的契约：
// SetStatus / Delete 在 RowsAffected==0 时必须返回 ErrNotFound 哨兵，
// 且该哨兵**同时**兼容既有的 gorm.ErrRecordNotFound 判定（错误链包装）。
//
// 这是 service 层「未命中 → 404、其它错误 → 500」判别的基础：
// 一旦有人把哨兵换成普通错误或直接改成 nil，这条测试必须红。
func TestDeviceSetStatusAndDeleteReturnNotFoundSentinel(t *testing.T) {
	db := newDeviceTestDB(t)
	seedDevice(t, db, 1001, "inst-a", "hash-a", "web-01", nil)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	// 命中：不得报错
	if err := repo.SetStatus(ctx, 1001, entity.DeviceStatusDisabled); err != nil {
		t.Fatalf("命中时 SetStatus 不应报错: %v", err)
	}

	// 未命中：ErrNotFound 哨兵（errors.Is 判定）
	err := repo.SetStatus(ctx, 9999, entity.DeviceStatusDisabled)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus 未命中必须返回 ErrNotFound 哨兵, got %v", err)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("哨兵必须兼容既有 gorm.ErrRecordNotFound 判定（否则是对既有契约的破坏）, got %v", err)
	}
	if errors.Is(err, errOtherRepoFailure) {
		t.Fatal("哨兵不得与无关错误混淆")
	}

	// 未命中：Delete 同契约
	if err := repo.Delete(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete 未命中必须返回 ErrNotFound 哨兵, got %v", err)
	}
	// 命中：Delete 走软删，不报错
	if err := repo.Delete(ctx, 1001); err != nil {
		t.Fatalf("命中时 Delete 不应报错: %v", err)
	}
}

// ── 主机分组 / 标签（平台级）──────────────────────────────────────────

// seedDeviceGrouped 在 seedDeviceFull 之上补 group_name / tags —— 本组用例的主角。
func seedDeviceGrouped(t *testing.T, db *gorm.DB, id uint64, instanceID, tokenHash, hostname,
	primaryIP, agentVersion, group, tags string, seen *time.Time) {
	t.Helper()
	d := entity.Device{
		BaseEntity: entity.BaseEntity{ID: id},
		InstanceID: instanceID, TokenHash: tokenHash, Hostname: hostname,
		OS: "linux", Arch: "amd64", AgentVersion: agentVersion, PrimaryIP: primaryIP,
		GroupName: group, Tags: tags,
		Status: entity.DeviceStatusEnabled, LastSeenAt: seen,
	}
	if err := db.Create(&d).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// TestDeviceHostGroupColumnsSchema 是主机分组/标签两列的 **schema 守卫**（本仓结构
// 变更由 AutoMigrate 按 entity 建、无版本化 DDL，故守卫落在测试而非迁移）：
//  1. 两列存在；
//  2. 两列 NOT NULL 且 DEFAULT ”（一次消除「NULL vs 空串」的二义）；
//  3. 未设置分组/标签的设备，用原始 INSERT（绕过 GORM 字段填充）落库后读出为 ""，
//     既不是 NULL 报错、也不是零值异常。
//
// 任一条失守都意味着实体的 gorm tag 被改坏（如有人把 not null/default 去掉），
// 而这类破坏平时完全静默（读出来碰巧是 ""，页面上看不出来）。
func TestDeviceHostGroupColumnsSchema(t *testing.T) {
	db := newDeviceTestDB(t)
	cols, err := db.Migrator().ColumnTypes(&entity.Device{})
	if err != nil {
		t.Fatalf("ColumnTypes: %v", err)
	}
	byName := make(map[string]gorm.ColumnType, len(cols))
	for _, c := range cols {
		byName[c.Name()] = c
	}
	for _, name := range []string{"group_name", "tags"} {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("device 表缺列 %s（entity.Device 字段或 autoMigrateEntities 清单可能被破坏）", name)
		}
		if nullable, ok := c.Nullable(); !ok || nullable {
			t.Fatalf("%s 必须 NOT NULL（消除 NULL vs 空串的二义）, got nullable=%v ok=%v", name, nullable, ok)
		}
		def, ok := c.DefaultValue()
		if !ok {
			t.Fatalf("%s 必须有 DEFAULT 值（滚动发布对加列的硬要求）", name)
		}
		// 不同方言对空串默认的取词不同（SQLite 回 "''"、MySQL 回 ""），故按「去引号后为空」
		// 判定，而不是死抠字面量。
		if strings.Trim(def, `'"`) != "" {
			t.Fatalf("%s 的 DEFAULT 应为空串, got %q", name, def)
		}
	}

	// 缺省插入（不写这两列）落库为 '' 而非 NULL：用原始 INSERT 绕过 GORM 的字段填充。
	if err := db.Exec("INSERT INTO device (id, instance_id, hostname, status) VALUES (?, ?, ?, ?)",
		7001, "i-grp", "web-grp", entity.DeviceStatusEnabled).Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	repo := NewDeviceRepository(db)
	got, err := repo.FindByID(context.Background(), 7001)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.GroupName != "" || got.Tags != "" {
		t.Fatalf("未设置分组/标签的设备应读出空串, got group=%q tags=%q", got.GroupName, got.Tags)
	}
}

// TestDeviceFindPageGroupFilter 钉住主机分组筛选的语义：**单值精确匹配**
// （不是 LIKE、不是逗号分隔多值）—— 选 "prod" 不得带出 "prod-archive"。
func TestDeviceFindPageGroupFilter(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedDeviceGrouped(t, db, 2001, "i1", "h1", "web-01", "10.0.0.1", "0.6.0", "prod", "web,cn", &seen)
	seedDeviceGrouped(t, db, 2002, "i2", "h2", "web-02", "10.0.0.2", "0.6.0", "prod", "web", &seen)
	seedDeviceGrouped(t, db, 2003, "i3", "h3", "db-01", "10.0.0.3", "0.6.0", "prod-archive", "db", &seen)
	seedDeviceGrouped(t, db, 2004, "i4", "h4", "edge-01", "10.0.0.4", "0.6.0", "", "", &seen)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	// 精确：prod 命中 2 台，绝不含 prod-archive（子串误命中）
	list, total, err := repo.FindPage(ctx, &request.DeviceQuery{Group: "prod"}, seen)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("group=prod 应精确命中 2 台（不含 prod-archive）, got total=%d len=%d", total, len(list))
	}
	for _, d := range list {
		if d.GroupName != "prod" {
			t.Fatalf("group=prod 结果混入了 %q（子串误命中）", d.GroupName)
		}
	}
	// 组名更长的不被误命中
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Group: "prod-archive"}, seen)
	if total != 1 {
		t.Fatalf("group=prod-archive 应命中 1 台, got %d", total)
	}
	// 空 = 不过滤（含未分组那台共 4 台）
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Group: ""}, seen)
	if total != 4 {
		t.Fatalf("group 空 = 不过滤，应 4 台, got %d", total)
	}
	// 不存在的组：0 台
	_, total, _ = repo.FindPage(ctx, &request.DeviceQuery{Group: "nope"}, seen)
	if total != 0 {
		t.Fatalf("不存在的分组应 0 台, got %d", total)
	}
}

// TestDeviceSortByGroup 钉住 group 列进了排序白名单（group→group_name），
// 且它的加入**不放宽回落纪律**：非法 sortBy 仍静默回落默认、不得 400。
func TestDeviceSortByGroup(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedDeviceGrouped(t, db, 3001, "i1", "h1", "a", "10.0.0.1", "0.6.0", "beta", "", &seen)
	seedDeviceGrouped(t, db, 3002, "i2", "h2", "b", "10.0.0.2", "0.6.0", "alpha", "", &seen)
	seedDeviceGrouped(t, db, 3003, "i3", "h3", "c", "10.0.0.3", "0.6.0", "gamma", "", &seen)
	repo := NewDeviceRepository(db)
	ctx := context.Background()

	list, _, err := repo.FindPage(ctx, &request.DeviceQuery{SortBy: "group", SortOrder: "asc"}, seen)
	if err != nil {
		t.Fatal(err)
	}
	idsOf := func(l []entity.Device) []uint64 {
		ids := make([]uint64, len(l))
		for i, d := range l {
			ids[i] = d.ID
		}
		return ids
	}
	if got := idsOf(list); !equalIDs(got, []uint64{3002, 3001, 3003}) {
		t.Fatalf("sortBy=group asc 顺序 = %v, want [3002 3001 3003]（alpha<beta<gamma）", got)
	}

	// 非法 sortBy（注入尝试）仍回落默认排序（last_seen_at DESC, id DESC = [3003 3002 3001]）
	list, _, _ = repo.FindPage(ctx, &request.DeviceQuery{SortBy: "group_name; DROP TABLE device"}, seen)
	if got := idsOf(list); !equalIDs(got, []uint64{3003, 3002, 3001}) {
		t.Fatalf("非法 sortBy 必须回落默认排序, got %v", got)
	}
}

// TestDeviceUpdateGroupTagsPartialSemantics 钉住 UpdateGroupTags 的**部分更新**语义（③）：
//   - group 与 tags 都是 *string：nil = 不动该列、空串 = 清空；
//   - 只传 group 时 tags 不动（反之亦然）；
//   - 空串用 map 形式的 Updates 让零值也落库（struct 形式会跳过）；
//   - 双 nil 命中行是 no-op、不改动；不存在的 id 无论单列还是双 nil 都必须报 ErrNotFound。
func TestDeviceUpdateGroupTagsPartialSemantics(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedDeviceGrouped(t, db, 4001, "i1", "h1", "web-01", "10.0.0.1", "0.6.0", "prod", "web,cn", &seen)
	repo := NewDeviceRepository(db)
	ctx := context.Background()
	strp := func(s string) *string { return &s }

	read := func() (string, string) {
		d, err := repo.FindByID(ctx, 4001)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		return d.GroupName, d.Tags
	}

	// 只传 group：tags 必须保持
	if err := repo.UpdateGroupTags(ctx, 4001, strp("staging"), nil); err != nil {
		t.Fatalf("UpdateGroupTags: %v", err)
	}
	if g, tg := read(); g != "staging" || tg != "web,cn" {
		t.Fatalf("只传 group 时 tags 不得变动: group=%q tags=%q", g, tg)
	}

	// 只传 tags：group 必须保持
	if err := repo.UpdateGroupTags(ctx, 4001, nil, strp("db,cn")); err != nil {
		t.Fatalf("UpdateGroupTags: %v", err)
	}
	if g, tg := read(); g != "staging" || tg != "db,cn" {
		t.Fatalf("只传 tags 时 group 不得变动: group=%q tags=%q", g, tg)
	}

	// 空串 = 清空（map 形式 Updates 让零值也落库）
	if err := repo.UpdateGroupTags(ctx, 4001, strp(""), strp("")); err != nil {
		t.Fatalf("UpdateGroupTags: %v", err)
	}
	if g, tg := read(); g != "" || tg != "" {
		t.Fatalf("空串必须清空: group=%q tags=%q", g, tg)
	}

	// 双 nil：命中行 → no-op 不报错，且不改动
	if err := repo.UpdateGroupTags(ctx, 4001, nil, nil); err != nil {
		t.Fatalf("双 nil 命中行不得报错: %v", err)
	}
	if g, tg := read(); g != "" || tg != "" {
		t.Fatalf("双 nil 不得改动: group=%q tags=%q", g, tg)
	}
}

// TestDeviceUpdateGroupTagsMissingRowErrors 钉住「不存在的 id」契约：单列与双 nil
// 两条路径都必须返回 ErrNotFound 哨兵，且兼容既有的 gorm.ErrRecordNotFound 判定。
func TestDeviceUpdateGroupTagsMissingRowErrors(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewDeviceRepository(db)
	ctx := context.Background()
	strp := func(s string) *string { return &s }

	cases := []struct {
		name         string
		group, tags  *string
	}{
		{"只传 group", strp("prod"), nil},
		{"只传 tags", nil, strp("web")},
		{"双 nil", nil, nil},
	}
	for _, c := range cases {
		err := repo.UpdateGroupTags(ctx, 9999, c.group, c.tags)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s：不存在的 id 必须报 ErrNotFound 哨兵, got %v", c.name, err)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("%s：哨兵必须兼容 gorm.ErrRecordNotFound, got %v", c.name, err)
		}
		if errors.Is(err, errOtherRepoFailure) {
			t.Fatalf("%s：哨兵不得与无关错误混淆, got %v", c.name, err)
		}
	}
}

// TestDeviceUpdateGroupTagsIdempotentReplayIsNotNotFound 钉住「重放同一值不报 404」：
// 把分组/标签设为它们已经是的值（值未改变）是合法 no-op，绝不是「设备不存在」。
//
// 这条守的是 UpdateGroupTags 里 RowsAffected==0 的分支：MySQL 默认把「值未改变的
// UPDATE」计为 0 行受影响，若直接据此报 ErrNotFound，用户重放同一次 PATCH 就会看到
// 「设备不存在」。（SQLite 夹具把同值更新也计入受影响行，故此测试在夹具上无法触发
// 那条分支，但仍钉住「重放必须无错」这一契约。）
func TestDeviceUpdateGroupTagsIdempotentReplayIsNotNotFound(t *testing.T) {
	db := newDeviceTestDB(t)
	seen := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedDeviceGrouped(t, db, 4001, "i1", "h1", "web-01", "10.0.0.1", "0.6.0", "prod", "web,cn", &seen)
	repo := NewDeviceRepository(db)
	ctx := context.Background()
	strp := func(s string) *string { return &s }

	if err := repo.UpdateGroupTags(ctx, 4001, strp("prod"), strp("web,cn")); err != nil {
		t.Fatalf("重放同一分组值必须 no-op（不得报未命中）: %v", err)
	}
	d, err := repo.FindByID(ctx, 4001)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroupName != "prod" || d.Tags != "web,cn" {
		t.Fatalf("重放后值不得变动: group=%q tags=%q", d.GroupName, d.Tags)
	}
}
