package repository

import (
	"context"
	"errors"
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
