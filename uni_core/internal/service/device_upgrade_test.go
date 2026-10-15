package service

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// 升级服务的测试环境：sqlite 内存库 + 真仓储（与 agent_ingest_test 同款）。
//
// 用真仓储而不是桩：本服务的不变式有一半落在 SQL 上（未终结行查询、
// 唯一索引、节流后的列取值），桩会把它们全部测不到。
type upgradeEnv struct {
	svc      *DeviceUpgradeService
	release  *AgentReleaseService
	db       *gorm.DB
	devices  *repository.DeviceRepo
	attempts *repository.AgentUpgradeAttemptRepo
	tasks    *repository.AgentUpgradeTaskRepo
	releases *repository.AgentReleaseRepo
	setter   *stubConfigSetter
	notifier *stubNotifier
	// uploadDir 是发布物落盘根目录（sys.file.upload.path）。
	uploadDir string
	// ingest 是设备入站服务：有了它，测试才能走**真实时序**
	// （Authenticate 刷新静态信息 → ReconcileOnHello 对账），而不是只调对账那一半。
	ingest *AgentIngestService
	// tokens 是各设备的明文 agent token（seedDevice 生成，供 simulateHello 使用）。
	tokens map[uint64]string
}

func newUpgradeTestEnv(t *testing.T, cfg stubCfg) *upgradeEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.Device{}, &entity.AgentRelease{},
		&entity.AgentUpgradeTask{}, &entity.AgentUpgradeAttempt{}); err != nil {
		t.Fatal(err)
	}
	devices := repository.NewDeviceRepository(db)
	attempts := repository.NewAgentUpgradeAttemptRepository(db)
	tasks := repository.NewAgentUpgradeTaskRepository(db)
	releases := repository.NewAgentReleaseRepository(db)

	// 发布物落盘到临时目录：上传/下载路径必须真的碰文件系统（摘要、O_EXCL、
	// ServeContent 的 Range 都只在真实文件上才有意义）。
	uploadDir := t.TempDir()
	if cfg == nil {
		cfg = stubCfg{}
	}
	if _, ok := cfg["sys.file.upload.path"]; !ok {
		cfg["sys.file.upload.path"] = uploadDir
	}
	setter := &stubConfigSetter{cfg: cfg}
	notifier := &stubNotifier{}
	return &upgradeEnv{
		svc: NewDeviceUpgradeService(devices, attempts, tasks, releases, cfg,
			setter, notifier, nil, logger.NewNop()),
		release:   NewAgentReleaseService(releases, attempts, cfg, logger.NewNop()),
		ingest:    NewAgentIngestService(devices, stubAgentRaw{}, stubAgentLatest{}, cfg, logger.NewNop()),
		db:        db,
		devices:   devices,
		attempts:  attempts,
		tasks:     tasks,
		releases:  releases,
		setter:    setter,
		notifier:  notifier,
		uploadDir: uploadDir,
		tokens:    map[uint64]string{},
	}
}

// simulateHello 走**真实的握手时序**：先 Authenticate（刷新设备自述的静态信息，
// 包括 agent_version），再 ReconcileOnHello（对账 + 解析指令）。
//
// 为什么测试必须走这两步而不是只调对账：agent_version 是刷新写进去的，而「已达成」
// 的推导（版本 == 目标）与成功判定都要用**库里的**版本号。只调对账等于假设了一个
// 「重连不刷新版本」的世界 —— 那正是这个特性修掉的缺口（升级成功观测不到）。
func (e *upgradeEnv) simulateHello(t *testing.T, deviceID uint64,
	version string) *agentproto.UpgradeDirective {
	t.Helper()
	ctx := context.Background()
	h := helloFrom(version)
	h.EnrollToken = ""
	h.AgentToken = e.tokens[deviceID]
	if h.AgentToken == "" {
		t.Fatalf("设备 %d 没有登记明文 token（必须由 seedDevice 创建）", deviceID)
	}
	if _, err := e.ingest.Authenticate(ctx, h); err != nil {
		t.Fatalf("鉴权失败: %v", err)
	}
	d, err := e.svc.ReconcileOnHello(ctx, deviceID, h)
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	return d
}

// seedDevice 造一台 linux/amd64 的设备（支持远程升级）。
//
// instance_id 与 token_hash 都由自增序号派生：同一个用例里要造多台设备，
// 而两者在库里都有唯一约束（早期版本用常量 token_hash，第二个设备就插不进去）。
func (e *upgradeEnv) seedDevice(t *testing.T, version string) *entity.Device {
	t.Helper()
	inst := fmt.Sprintf("inst-%d", deviceSeq.Add(1))
	token := fmt.Sprintf("tok-%s-%d", t.Name(), deviceSeq.Load())
	d := &entity.Device{
		InstanceID: inst,
		Hostname:   "web-01", OS: "linux", Arch: "amd64",
		AgentVersion: version, Status: entity.DeviceStatusEnabled,
		TokenHash: hashToken(token), AgentUpgradeSupported: 1,
	}
	if err := e.devices.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if e.tokens == nil {
		e.tokens = map[uint64]string{}
	}
	e.tokens[d.ID] = token
	return d
}

var deviceSeq atomic.Uint64

// seedRelease 造一份产物（published=false 时是草稿）。
func (e *upgradeEnv) seedRelease(t *testing.T, version, osName, arch string, published bool) {
	t.Helper()
	status := entity.AgentReleaseDraft
	if published {
		status = entity.AgentReleasePublished
	}
	rel := &entity.AgentRelease{
		Version: version, OS: osName, Arch: arch, FileName: "uni_agent_" + version,
		SizeBytes: 24117248, SHA256: strings.Repeat("b", 64),
		StorageType: "local", StorageKey: "releases/" + version, Status: status,
	}
	if err := e.releases.Create(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
}

func helloFrom(version string) *agentproto.Hello {
	return &agentproto.Hello{InstanceID: "x", Hostname: "web-01", OS: "linux",
		Arch: "amd64", AgentVersion: version, UpgradeSupported: true, AgentToken: "t"}
}

// openAttemptCount 数一数未终结的尝试行（D1 的核心断言）。
func (e *upgradeEnv) openAttemptCount(t *testing.T) int {
	t.Helper()
	var n int64
	if err := e.db.Model(&entity.AgentUpgradeAttempt{}).
		Where("state NOT IN ?", entity.AttemptTerminalStates()).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func (e *upgradeEnv) attemptRows(t *testing.T) []entity.AgentUpgradeAttempt {
	t.Helper()
	var rows []entity.AgentUpgradeAttempt
	if err := e.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e *upgradeEnv) deviceRow(t *testing.T, id uint64) entity.Device {
	t.Helper()
	var d entity.Device
	if err := e.db.First(&d, id).Error; err != nil {
		t.Fatal(err)
	}
	return d
}

// stubConfigSetter 是可观测的配置写入桩（全站目标走它）。
//
// **写回同一个 map**：真实的 ConfigService 写库后会回写 Redis 缓存，因此
// 「写完之后再读」必须立刻看到新值。若桩只记下调用而不改 cfg，测试就会在
// 「已设置的全站目标读出来还是空」这种假环境下跑 —— 那正好会掩盖真实缺陷。
type stubConfigSetter struct {
	cfg        stubCfg
	key, value string
}

func (s *stubConfigSetter) SetString(_ context.Context, key, value string) error {
	s.key, s.value = key, value
	if s.cfg != nil {
		s.cfg[key] = value
	}
	return nil
}

// stubNotifier 记录催办过的设备（不碰 socket）。
type stubNotifier struct {
	notified []uint64
}

func (n *stubNotifier) NotifyUpgrade(_ context.Context, deviceID uint64,
	_ *agentproto.UpgradeDirective) error {
	n.notified = append(n.notified, deviceID)
	return nil
}

// ── 对账（ReconcileOnHello）────────────────────────────────────────────

// TestReconcileCreatesAttemptOnceAndReusesRequestID 钉住 D1 的核心：
// **重连对账不算新尝试**。
//
// 这是最容易写错、后果又最难看的一条：若每次 hello 都开一行，一台设备在弱网下
// 一天能刷出几十行「升级中」，任务明细彻底失去可读性；而指令里的 request_id 若
// 每次换新，设备的状态上报又会因为归属不到行而被丢弃 —— 页面表现为「设备在升级、
// 界面上什么都不动」。
func TestReconcileCreatesAttemptOnceAndReusesRequestID(t *testing.T) {
	env := newUpgradeTestEnv(t, stubCfg{})
	ctx := context.Background()
	dev := env.seedDevice(t, "0.1.0")
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}

	first, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if first == nil {
		t.Fatal("有目标且有产物时必须下发指令")
	}
	if first.TargetVersion != "0.2.0" || first.RequestID == "" || first.SizeBytes <= 0 {
		t.Fatalf("指令字段不完整: %+v", first)
	}

	// 模拟断线重连：同一个设备再握一次手。
	second, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.RequestID != first.RequestID {
		t.Fatalf("重连必须复用同一次尝试的 request_id: %+v vs %+v", first, second)
	}
	if n := env.openAttemptCount(t); n != 1 {
		t.Fatalf("重连不得产生新尝试行，未终结行数 = %d", n)
	}
	rows := env.attemptRows(t)
	if rows[0].FromVersion != "0.1.0" || rows[0].ToVersion != "0.2.0" ||
		rows[0].State != entity.AttemptStatePending {
		t.Fatalf("尝试行内容不符: %+v", rows[0])
	}
}

// TestReconcileWithoutArtifactDoesNotOpenAttempt：没有产物时**不开行**。
//
// 若先开行再查产物，「有目标但该平台没产物」会留下一条永不开工的 pending 行，
// 页面把它显示成「升级中」—— 而真相是「缺程序文件」，两者对运维完全不同。
func TestReconcileWithoutArtifactDoesNotOpenAttempt(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		published bool
	}{
		{"没有产物", false}, // 连草稿都没有
		{"只有草稿", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newUpgradeTestEnv(t, stubCfg{})
			dev := env.seedDevice(t, "0.1.0")
			if tc.name == "只有草稿" {
				env.seedRelease(t, "0.2.0", "linux", "amd64", false)
			}
			if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
				t.Fatal(err)
			}
			d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
			if err != nil {
				t.Fatal(err)
			}
			if d != nil {
				t.Fatalf("没有已发布产物时不得下发指令: %+v", d)
			}
			if n := env.openAttemptCount(t); n != 0 {
				t.Fatalf("没有产物时不得开尝试行，实得 %d 行", n)
			}
		})
	}
}

// TestReconcileNoTargetAndPinned 覆盖两种「不下发」的合法情形：
// 无目标（谁都没设）与固定（目标 = 当前版本）。
func TestReconcileNoTargetAndPinned(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	dev := env.seedDevice(t, "0.1.0")
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)

	if d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0")); err != nil || d != nil {
		t.Fatalf("无目标时不得下发指令: %+v %v", d, err)
	}
	// 固定：设备级目标 == 当前版本。
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0")); err != nil || d != nil {
		t.Fatalf("固定在当前版本时不得下发指令: %+v %v", d, err)
	}
	if n := env.openAttemptCount(t); n != 0 {
		t.Fatalf("这两种情形都不该开尝试行，实得 %d", n)
	}
}

// TestReconcileGlobalTargetAndOverride 钉住生效目标的三态优先级：
// 设备级 > 全站；全站对**新注册设备**同样生效（这是全站目标的产品语义）。
func TestReconcileGlobalTargetAndOverride(t *testing.T) {
	ctx := context.Background()
	cfg := stubCfg{ConfigTargetVersion: "0.2.0"}
	env := newUpgradeTestEnv(t, cfg)
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	env.seedRelease(t, "0.3.0", "linux", "amd64", true)

	// 新设备（注册时全站目标已生效）→ 首次握手就该拿到指令。
	dev := env.seedDevice(t, "0.1.0")
	d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	if err != nil || d == nil || d.TargetVersion != "0.2.0" {
		t.Fatalf("跟随全站的设备应拿到 0.2.0 指令: %+v %v", d, err)
	}

	// 设备级指定覆盖全站。
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	// 覆盖发生在下一次对账：先把当前未终结的行终结掉（模拟已在 0.2.0 上）。
	if _, err := env.attempts.FinishOpen(ctx, dev.ID, entity.AttemptStateSucceeded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	d, err = env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.2.0"))
	if err != nil || d == nil || d.TargetVersion != "0.3.0" {
		t.Fatalf("设备级目标应覆盖全站: %+v %v", d, err)
	}

	// 清空设备级目标 = 恢复跟随全站。
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	row := env.deviceRow(t, dev.ID)
	if got := env.svc.ResolveTargetVersion(ctx, &row); got != "0.2.0" {
		t.Fatalf("清空设备级目标后应恢复跟随全站，got %q", got)
	}
}

// TestReconcileSettlesAttemptByReportedVersion 钉住「成功只在 hello 裁决」，
// 以及回滚/意外版本三种归位判定。
func TestReconcileSettlesAttemptByReportedVersion(t *testing.T) {
	ctx := context.Background()

	t.Run("版本等于目标=成功", func(t *testing.T) {
		env := newUpgradeTestEnv(t, stubCfg{})
		dev := env.seedDevice(t, "0.1.0")
		env.seedRelease(t, "0.2.0", "linux", "amd64", true)
		if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0")); err != nil {
			t.Fatal(err)
		}
		d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.2.0"))
		if err != nil {
			t.Fatal(err)
		}
		if d != nil {
			t.Fatalf("已到达目标不应再下发指令: %+v", d)
		}
		rows := env.attemptRows(t)
		if rows[0].State != entity.AttemptStateSucceeded || rows[0].FinishedAt == nil {
			t.Fatalf("应判成功并写终态: %+v", rows[0])
		}
		if got := env.deviceRow(t, dev.ID).AgentUpgradeState; got != entity.DeviceUpgradeAchieved {
			t.Fatalf("设备行应写「已达成」，got %d", got)
		}
	})

	t.Run("已开工后回到原版本=本地自动回滚", func(t *testing.T) {
		env := newUpgradeTestEnv(t, stubCfg{})
		dev := env.seedDevice(t, "0.1.0")
		env.seedRelease(t, "0.2.0", "linux", "amd64", true)
		if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		// 设备开工（走到 downloading）。
		if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
			RequestID: d.RequestID, State: agentproto.UpgradeStateDownloading,
			TargetVersion: "0.2.0", FromVersion: "0.1.0",
		}); err != nil {
			t.Fatal(err)
		}
		// 它以原版本重新握手 → 说明新版本起来后没连上、设备自行回滚了。
		if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0")); err != nil {
			t.Fatal(err)
		}
		rows := env.attemptRows(t)
		if rows[0].State != entity.AttemptStateRolledBack ||
			rows[0].ReasonCode != agentproto.ReasonNotConnectedAfterUpgrade {
			t.Fatalf("应判回滚并带原因码: %+v", rows[0])
		}
		dev2 := env.deviceRow(t, dev.ID)
		if dev2.AgentUpgradeState != entity.DeviceUpgradeRolledBack ||
			dev2.AgentUpgradeReason != agentproto.ReasonNotConnectedAfterUpgrade {
			t.Fatalf("设备行应写「已回滚 + 原因」: %+v", dev2)
		}
	})

	t.Run("未开工时回到原版本=不回滚", func(t *testing.T) {
		env := newUpgradeTestEnv(t, stubCfg{})
		dev := env.seedDevice(t, "0.1.0")
		env.seedRelease(t, "0.2.0", "linux", "amd64", true)
		if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		// 还没开工（pending）就以原版本重连：这是正常过程，不能判成回滚。
		d2, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		if err != nil {
			t.Fatal(err)
		}
		if d2 == nil || d2.RequestID != d.RequestID {
			t.Fatalf("未开工的重连应继续复用该尝试: %+v", d2)
		}
		if rows := env.attemptRows(t); entity.IsAttemptTerminal(rows[0].State) {
			t.Fatalf("未开工的重连不得终结尝试，实得 %s", rows[0].State)
		}
	})

	t.Run("版本既非目标也非原版本=失败", func(t *testing.T) {
		env := newUpgradeTestEnv(t, stubCfg{})
		dev := env.seedDevice(t, "0.1.0")
		env.seedRelease(t, "0.2.0", "linux", "amd64", true)
		if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		_ = env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
			RequestID: d.RequestID, State: agentproto.UpgradeStateDownloading,
			TargetVersion: "0.2.0", FromVersion: "0.1.0",
		})
		if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.9.9")); err != nil {
			t.Fatal(err)
		}
		rows := env.attemptRows(t)
		if rows[0].State != entity.AttemptStateFailed ||
			rows[0].ReasonCode != entity.AttemptReasonUnexpectedVersion {
			t.Fatalf("应判失败(unexpected_version): %+v", rows[0])
		}
	})
}

// TestReconcileDoesNotAutoRetryFailedTarget 防「升级失败 → 自动重试」死循环。
//
// 失败之后要不要再试是**人的决定**（控制台的重试按钮走「终结旧行 + 开新行」），
// 不是系统默认行为 —— 否则一台怎么都升不上去的机器会每几分钟一轮地重装。
func TestReconcileDoesNotAutoRetryFailedTarget(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	dev := env.seedDevice(t, "0.1.0")
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	// 设备上报失败（例如校验不通过）。
	if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateFailed,
		TargetVersion: "0.2.0", FromVersion: "0.1.0",
		ReasonCode: agentproto.ReasonChecksumMismatch,
	}); err != nil {
		t.Fatal(err)
	}
	// 之后每次重连都不得自动重开。
	for i := 0; i < 3; i++ {
		got, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("失败后不得自动重下发（会形成无限重试）: %+v", got)
		}
	}
	if n := env.openAttemptCount(t); n != 0 {
		t.Fatalf("不该有未终结行，实得 %d", n)
	}
	if got := env.deviceRow(t, dev.ID).AgentUpgradeState; got != entity.DeviceUpgradeFailed {
		t.Fatalf("设备行应是失败态，got %d", got)
	}
}

// TestReconcileDoesNotTouchOtherDevices：对账是**逐设备**的，
// 一台设备的目标不得影响另一台（全站目标除外，它本来就该影响所有设备）。
func TestReconcileDoesNotTouchOtherDevices(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	a := env.seedDevice(t, "0.1.0")
	b := env.seedDevice(t, "0.1.0")
	if err := env.devices.SetUpgradeTarget(ctx, a.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if d, err := env.svc.ReconcileOnHello(ctx, b.ID, helloFrom("0.1.0")); err != nil || d != nil {
		t.Fatalf("B 未设目标，不应拿到指令: %+v %v", d, err)
	}
	if n := env.openAttemptCount(t); n != 0 {
		t.Fatalf("B 不该开尝试行，实得 %d", n)
	}
}

// ── 状态上报（ReportUpgradeStatus）────────────────────────────────────

// TestReportStatusThrottleAndTerminal 覆盖节流与终态同步。
func TestReportStatusThrottleAndTerminal(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	dev := env.seedDevice(t, "0.1.0")
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	progress := func(p int) *int { return &p }

	report := func(state string, p *int) {
		t.Helper()
		if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
			RequestID: d.RequestID, State: state, TargetVersion: "0.2.0",
			FromVersion: "0.1.0", Progress: p,
		}); err != nil {
			t.Fatal(err)
		}
	}
	row := func() entity.AgentUpgradeAttempt { return env.attemptRows(t)[0] }

	report(agentproto.UpgradeStateDownloading, progress(10))
	if got := row(); got.State != agentproto.UpgradeStateDownloading || got.Progress == nil ||
		*got.Progress != 10 || got.StartedAt == nil {
		t.Fatalf("首次下载上报应落库并记开工时间: %+v", got)
	}

	// 同一阶段、几分钟内的小幅进度：不写库（节流）。
	report(agentproto.UpgradeStateDownloading, progress(12))
	if got := row(); got.Progress == nil || *got.Progress != 10 {
		t.Fatalf("小幅进度不该写库（节流），got %+v", got.Progress)
	}

	// 跨过 10 个百分点：写。
	report(agentproto.UpgradeStateDownloading, progress(25))
	if got := row(); got.Progress == nil || *got.Progress != 25 {
		t.Fatalf("跨 10 个百分点应写库，got %+v", got.Progress)
	}

	// 阶段跃迁：立即写，**且进度必须被清空**（否则页面会显示「校验中 25%」）。
	report(agentproto.UpgradeStateVerifying, nil)
	if got := row(); got.State != agentproto.UpgradeStateVerifying || got.Progress != nil {
		t.Fatalf("阶段跃迁应写库并清空进度: %+v", got)
	}

	// 终态：写终态 + 设备行同步。
	report(agentproto.UpgradeStateFailed, nil)
	got := row()
	if got.State != agentproto.UpgradeStateFailed || got.FinishedAt == nil {
		t.Fatalf("终态上报应写 finished_at: %+v", got)
	}
}

// TestReportStatusDropsUnattributable 覆盖两种必须**丢弃**的上报：
// 归属不到的 request_id（陈旧/伪造），以及目标对不上的（设备还在按旧指令跑）。
func TestReportStatusDropsUnattributable(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	dev := env.seedDevice(t, "0.1.0")
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	d, _ := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))

	// 归属不到：不报错（这是「丢弃」而不是「故障」），也不改动任何行。
	if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
		RequestID: "99999999", State: agentproto.UpgradeStateFailed,
		TargetVersion: "0.2.0", ReasonCode: agentproto.ReasonDownloadFailed,
	}); err != nil {
		t.Fatalf("归属不到的上报应被静默丢弃而不是报错: %v", err)
	}
	// 目标对不上：同样丢弃（落库会让页面出现「目标 A、进度来自 B」的错位）。
	if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateDownloading,
		TargetVersion: "0.3.0",
	}); err != nil {
		t.Fatal(err)
	}
	if got := env.attemptRows(t)[0]; got.State != entity.AttemptStatePending {
		t.Fatalf("被丢弃的上报不得改动尝试行: %+v", got)
	}
}

// TestReportTerminalSettlesTaskOnceAllSettled：任务收口只在「无进行中且无等待」时发生。
func TestReportTerminalSettlesTaskOnceAllSettled(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)

	task := &entity.AgentUpgradeTask{TargetVersion: "0.2.0",
		Source: entity.AgentUpgradeSourceBatch, Actor: "tester", Total: 2}
	if err := env.tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	mk := func(dev *entity.Device) *agentproto.UpgradeDirective {
		if err := env.devices.SetUpgradeTarget(ctx, dev.ID, "0.2.0"); err != nil {
			t.Fatal(err)
		}
		d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
		if err != nil || d == nil {
			t.Fatalf("对账失败: %v", err)
		}
		return d
	}
	devA := env.seedDevice(t, "0.1.0")
	devB := env.seedDevice(t, "0.1.0")
	dA, dB := mk(devA), mk(devB)

	// 把两行都挂到任务上（真实路径由下发的命令面完成，这里直接改库模拟）。
	if err := env.db.Model(&entity.AgentUpgradeAttempt{}).
		Where("request_id IN ?", []string{dA.RequestID, dB.RequestID}).
		Update("task_id", task.ID).Error; err != nil {
		t.Fatal(err)
	}

	// 第一台失败：任务还在等第二台，不应收口。
	if err := env.svc.ReportUpgradeStatus(ctx, devA.ID, &agentproto.UpgradeStatus{
		RequestID: dA.RequestID, State: agentproto.UpgradeStateFailed,
		TargetVersion: "0.2.0", ReasonCode: agentproto.ReasonDownloadFailed,
	}); err != nil {
		t.Fatal(err)
	}
	var got entity.AgentUpgradeTask
	if err := env.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FinishedAt != nil {
		t.Fatalf("还有一台未终结，任务不该收口: %+v", got.FinishedAt)
	}

	// 第二台也终结 → 收口。
	if _, err := env.attempts.FinishOpen(ctx, devB.ID, entity.AttemptStateSucceeded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	done, err := env.tasks.MarkFinishedIfSettled(ctx, task.ID, time.Now())
	if err != nil || !done {
		t.Fatalf("全部终结后应收口: done=%v err=%v", done, err)
	}
}
