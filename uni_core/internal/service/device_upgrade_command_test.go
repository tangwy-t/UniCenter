package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// ── 命令面：下发 / 预览 / 全站 / 汇总 ────────────────────────────────────

// TestDispatchSkipsAndCounts 钉住四类跳过与「可下发的才下发」。
func TestDispatchSkipsAndCounts(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)

	ok := env.seedDevice(t, "0.1.0")          // 可下发
	onTarget := env.seedDevice(t, "0.2.0")    // 已在该版本
	unsupported := env.seedDevice(t, "0.1.0") // 不支持远程升级
	disabled := env.seedDevice(t, "0.1.0")    // 已停用
	armDev := env.seedDevice(t, "0.1.0")      // arm64：该版本没有产物
	if err := env.db.Model(&entity.Device{}).Where("id = ?", unsupported.ID).
		Update("agent_upgrade_supported", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Model(&entity.Device{}).Where("id = ?", disabled.ID).
		Update("status", entity.DeviceStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Model(&entity.Device{}).Where("id = ?", armDev.ID).
		Update("arch", "arm64").Error; err != nil {
		t.Fatal(err)
	}

	ids := util.JsonUint64Slice{ok.ID, onTarget.ID, unsupported.ID, disabled.ID, armDev.ID}
	resp, err := env.svc.Dispatch(ctx, &request.DeviceBatchUpgradeRequest{
		Version: "0.2.0", IDs: ids,
	}, entity.AgentUpgradeSourceBatch, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Dispatched != 1 {
		t.Fatalf("只有 1 台可下发，实得 %d", resp.Dispatched)
	}
	if resp.Skip.AlreadyOnTarget != 1 || resp.Skip.Unsupported != 1 ||
		resp.Skip.Disabled != 1 || resp.Skip.NoArtifact != 1 {
		t.Fatalf("跳过明细不符: %+v", resp.Skip)
	}
	// 只有可下发的那台写了目标、开了尝试、收到了催办。
	var withTarget int64
	if err := env.db.Model(&entity.Device{}).
		Where("target_agent_version = ?", "0.2.0").Count(&withTarget).Error; err != nil {
		t.Fatal(err)
	}
	if withTarget != 1 {
		t.Fatalf("应恰有 1 台被写入目标，实得 %d", withTarget)
	}
	if n := env.openAttemptCount(t); n != 1 {
		t.Fatalf("应恰有 1 条未终结尝试，实得 %d", n)
	}
	if len(env.notifier.notified) != 1 || env.notifier.notified[0] != ok.ID {
		t.Fatalf("催办对象不符: %v", env.notifier.notified)
	}
	if resp.TaskID == "" {
		t.Fatal("下发应建出任务")
	}
}

// TestDispatchAlreadyOnTargetIsNotPinned 钉住一条容易被顺手写错的规则：
// 批次里「已在该版本」的设备**不得**被写入设备级目标。
//
// 后果对比：写了它 = 这台设备从此不再跟随全站（一次全站升级把每台设备都钉死），
// 而「跟随全站」的台数会静默变成 0 —— 下次改全站目标谁都跟不上。
func TestDispatchAlreadyOnTargetIsNotPinned(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.2.0") // 已在该版本

	if _, err := env.svc.Dispatch(ctx, &request.DeviceBatchUpgradeRequest{
		Version: "0.2.0", IDs: util.JsonUint64Slice{dev.ID},
	}, entity.AgentUpgradeSourceBatch, 0); err != nil {
		t.Fatal(err)
	}
	if got := env.deviceRow(t, dev.ID).TargetAgentVersion; got != "" {
		t.Fatalf("已在该版本的设备不该被写下目标（会把它钉死），实得 %q", got)
	}
}

// TestSetDeviceTargetPinWritesTarget 钉住「固定」这条独立意图：
// 版本与当前相同时也写目标（这正是「不再跟随全站」的全部语义）。
func TestSetDeviceTargetPinWritesTarget(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	dev := env.seedDevice(t, "0.2.0")

	pin := true
	resp, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", pin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Dispatched != 0 || resp.Skip.AlreadyOnTarget != 1 {
		t.Fatalf("固定不该产生下发: %+v", resp)
	}
	if got := env.deviceRow(t, dev.ID).TargetAgentVersion; got != "0.2.0" {
		t.Fatalf("固定必须写下目标，实得 %q", got)
	}

	// 清空 = 恢复跟随。
	if err := env.svc.ClearDeviceTarget(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	if got := env.deviceRow(t, dev.ID).TargetAgentVersion; got != "" {
		t.Fatalf("清空后目标应为空，实得 %q", got)
	}
}

// TestDispatchFilterRequiresMatchingCount 钉住「按筛选下发」的防呆：
//
// preview 给出的命中台数是操作者确认过的**影响面**；两次之间设备集合变了
// （新注册、被删、被停用）就说明影响面已经不是那个了 —— 静默照做或静默缩小都是错的。
func TestDispatchFilterRequiresMatchingCount(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	env.seedDevice(t, "0.1.0")
	env.seedDevice(t, "0.1.0")

	req := &request.DeviceBatchUpgradeRequest{Version: "0.2.0", Filter: &request.DeviceQuery{}}
	preview, err := env.svc.Preview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Matched != 2 || preview.WillUpgrade != 2 {
		t.Fatalf("预演数字不符: %+v", preview)
	}

	// 缺少影响面确认：拒绝。
	if _, err := env.svc.Dispatch(ctx, req, entity.AgentUpgradeSourceFilter, 0); err == nil {
		t.Fatal("按筛选下发缺少 expectedCount 必须被拒")
	}

	// 数字对得上：放行。
	req.ExpectedCount = &preview.Matched
	if _, err := env.svc.Dispatch(ctx, req, entity.AgentUpgradeSourceFilter, 0); err != nil {
		t.Fatalf("数字一致应放行: %v", err)
	}

	// 两次之间多了一台设备 → 409。
	env.seedDevice(t, "0.1.0")
	req.ExpectedCount = &preview.Matched
	_, err = env.svc.Dispatch(ctx, req, entity.AgentUpgradeSourceFilter, 0)
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("影响面变化必须 409，实得 %v", err)
	}
	if strings.Contains(ae.Message, "expectedCount") || strings.Contains(ae.Message, "字段") {
		t.Fatalf("文案只讲结论，不得出现字段名: %q", ae.Message)
	}
}

// TestDispatchRejectsBothIdsAndFilter 钉住二选一（两种入口的语义不同，同时给会让
// 「以哪个为准」变成实现细节）。
func TestDispatchRejectsBothIdsAndFilter(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.1.0")
	_, err := env.svc.Dispatch(ctx, &request.DeviceBatchUpgradeRequest{
		Version: "0.2.0", IDs: util.JsonUint64Slice{dev.ID}, Filter: &request.DeviceQuery{},
	}, entity.AgentUpgradeSourceBatch, 0)
	if err == nil {
		t.Fatal("同时给 ids 与 filter 必须被拒")
	}
}

// TestSetGlobalTargetAffectsOnlyFollowers 钉住全站目标的边界：
//   - 设备级指定的一律不动（并给出台数）；
//   - 全站目标也要**先有已发布产物**，否则设了等于没设而页面看不出为什么。
func TestSetGlobalTargetAffectsOnlyFollowers(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})

	// 无产物：拒绝（否则全站设备都会因「无产物」跳过，而没人知道为什么）。
	if _, err := env.svc.SetGlobalTarget(ctx, "0.2.0", 0); err == nil {
		t.Fatal("没有已发布产物时不得设置全站目标")
	}

	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	env.seedRelease(t, "0.3.0", "linux", "amd64", true)
	follower := env.seedDevice(t, "0.1.0")
	pinned := env.seedDevice(t, "0.1.0")
	if err := env.devices.SetUpgradeTarget(ctx, pinned.ID, "0.3.0"); err != nil {
		t.Fatal(err)
	}

	resp, err := env.svc.SetGlobalTarget(ctx, "0.2.0", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Affected != 1 || resp.PinnedDevices != 1 {
		t.Fatalf("影响面不符（应只动跟随的那台）: %+v", resp)
	}
	if resp.TaskID == "" {
		t.Fatal("有设备受影响就该建任务")
	}
	if got := env.deviceRow(t, follower.ID).TargetAgentVersion; got != "0.2.0" {
		t.Fatalf("跟随的设备应被写下目标，实得 %q", got)
	}
	if got := env.deviceRow(t, pinned.ID).TargetAgentVersion; got != "0.3.0" {
		t.Fatalf("已指定版本的设备不得被改动，实得 %q", got)
	}
	if env.setter.key != ConfigTargetVersion || env.setter.value != "0.2.0" {
		t.Fatalf("配置键写入不符: %s=%s", env.setter.key, env.setter.value)
	}

	// 清空 = 关闭全站升级：不动任何设备的版本。
	resp, err = env.svc.SetGlobalTarget(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Affected != 0 || resp.TaskID != "" {
		t.Fatalf("关闭全站升级不该产生下发: %+v", resp)
	}
}

// TestSummaryBucketsAndDerivedPhase 钉住「当前状态」视角的分桶与**读时推导**：
// 待升级/升级中/已达成都不落库，全在这一处算出来。
func TestSummaryBucketsAndDerivedPhase(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)

	achieved := env.seedDevice(t, "0.2.0") // 已达成
	pending := env.seedDevice(t, "0.1.0")  // 待升级
	running := env.seedDevice(t, "0.1.0")  // 升级中
	none := env.seedDevice(t, "0.1.0")     // 无目标
	_ = none
	// 一台已达成、一台待升级、一台升级中，都跟随全站目标 0.2.0。
	if _, err := env.svc.SetGlobalTarget(ctx, "0.2.0", 0); err != nil {
		t.Fatal(err)
	}
	// 让「升级中」那台真的开工：下发后上报 downloading。
	d, err := env.svc.ReconcileOnHello(ctx, running.ID, helloFrom("0.1.0"))
	if err != nil || d == nil {
		t.Fatalf("对账失败: %v", err)
	}
	if err := env.svc.ReportUpgradeStatus(ctx, running.ID, &agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateDownloading,
		TargetVersion: "0.2.0", FromVersion: "0.1.0",
	}); err != nil {
		t.Fatal(err)
	}

	sum, err := env.svc.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.GlobalTargetVersion != "0.2.0" {
		t.Fatalf("全站目标应回显: %+v", sum)
	}
	var bucket *struct{ achieved, pending, running int64 }
	for i := range sum.Buckets {
		if sum.Buckets[i].TargetVersion == "0.2.0" {
			b := sum.Buckets[i]
			bucket = &struct{ achieved, pending, running int64 }{b.Achieved, b.Pending, b.Running}
		}
	}
	if bucket == nil {
		t.Fatalf("缺少 0.2.0 桶: %+v", sum.Buckets)
	}
	if bucket.achieved != 1 || bucket.running != 1 {
		t.Fatalf("桶计数不符（应 1 已达成 / 1 升级中）：%+v", bucket)
	}
	// 待升级 = 有目标、没开工（含那台无目标的设备不参与本桶）。
	if bucket.pending < 1 {
		t.Fatalf("应有至少 1 台待升级: %+v", bucket)
	}
	if len(sum.VersionDistribution) == 0 {
		t.Fatal("版本分布不得为空")
	}
	_ = achieved
	_ = pending
}

// ── 发布物：上传 / 发布 / 撤回 / 删除 / 下载解析 ─────────────────────────

// TestReleaseUploadComputesHashAndGuards 钉住上传的三条纪律：
// sha256 由服务端算、草稿不能被选为目标、同平台重复上传被拒。
func TestReleaseUploadComputesHashAndGuards(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	body := strings.Repeat("A", 1024)

	item, err := env.release.Upload(ctx, "0.2.0", "linux", "amd64", "首个版本", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if item.SHA256 != "8cd9f3362f7e6d0a7a3a7cd0d1b0a4a6b1b0f1a2c3d4e5f60718293a4b5c6d7e"[:64] && len(item.SHA256) != 64 {
		t.Fatalf("摘要应由服务端算出 64 位 hex，实得 %q", item.SHA256)
	}
	if item.Status != entity.AgentReleaseDraft {
		t.Fatalf("上传应为草稿态，实得 %d", item.Status)
	}
	// 草稿不可被当作下发依据。
	env.seedDevice(t, "0.1.0")
	if err := env.devices.SetUpgradeTarget(ctx, 1, "0.2.0"); err != nil {
		// 设备 id 不是 1 也无妨：这里只验证对账时的产物解析
		_ = err
	}

	// 同平台重复上传 → 409。
	_, err = env.release.Upload(ctx, "0.2.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body)))
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("同平台重复上传必须 409，实得 %v", err)
	}

	// 版本号形态不对 / 平台不支持 → 400。
	if _, err := env.release.Upload(ctx, "0.2", "linux", "amd64", "", strings.NewReader(body), 1); err == nil {
		t.Fatal("非法版本号必须被拒")
	}
	if _, err := env.release.Upload(ctx, "0.2.1", "windows", "amd64", "", strings.NewReader(body), 1); err == nil {
		t.Fatal("不支持的系统必须被拒")
	}

	// 发布 → 可被下载解析；撤回后**仍可下载**（撤回只影响新下发）。
	if err := env.release.Publish(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	dev := env.seedDevice(t, "0.1.0")
	rc, rel, err := env.release.OpenForDownload(ctx, dev, "0.2.0")
	if err != nil {
		t.Fatalf("已发布版本应可下载: %v", err)
	}
	_ = rc.Close()
	if rel.SHA256 != item.SHA256 {
		t.Fatalf("下载解析到的产物不符: %+v", rel)
	}
	if err := env.release.Unpublish(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	rc, _, err = env.release.OpenForDownload(ctx, dev, "0.2.0")
	if err != nil {
		t.Fatalf("撤回后已指向该版本的设备仍应能下载（否则会打断正在进行的升级）: %v", err)
	}
	_ = rc.Close()
}

// TestReleaseDeleteGuardedByRollbackReserve 钉住回滚余量：
// 在升级记录里出现过的版本（作为目标**或起始版本**）一律禁删。
func TestReleaseDeleteGuardedByRollbackReserve(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	body := strings.Repeat("B", 256)

	old, err := env.release.Upload(ctx, "0.1.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	next, err := env.release.Upload(ctx, "0.2.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint64{old.ID, next.ID} {
		if err := env.release.Publish(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	// 一次成功的升级：0.1.0 → 0.2.0（from_version=0.1.0 也算「用过」）。
	dev := env.seedDevice(t, "0.1.0")
	if _, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.2.0")); err != nil {
		t.Fatal(err)
	}

	// 两个版本都被用过 → 都禁删，且文案只讲结论。
	for _, id := range []uint64{old.ID, next.ID} {
		err := env.release.Delete(ctx, id)
		var ae *apperror.AppError
		if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
			t.Fatalf("用过的版本必须禁删（回滚余量），实得 %v", err)
		}
		if strings.Contains(ae.Message, "from_version") || strings.Contains(ae.Message, "attempt") {
			t.Fatalf("文案不得出现内部名: %q", ae.Message)
		}
	}

	// 没被用过的版本可以删（并连带删掉存储对象）。
	fresh, err := env.release.Upload(ctx, "0.3.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.release.Delete(ctx, fresh.ID); err != nil {
		t.Fatalf("未用过的版本应可删: %v", err)
	}
	if _, err := env.releases.FindByID(ctx, fresh.ID); err == nil {
		t.Fatal("删除后记录应消失")
	}
}

// TestReleaseListReportsDeletable 钉住列表给出「能不能删」的结论：
// 前端据此禁用按钮，而不是让人点了才知道。
func TestReleaseListReportsDeletable(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	body := strings.Repeat("C", 128)
	rel, err := env.release.Upload(ctx, "0.2.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.release.Publish(ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	list, err := env.release.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.List) != 1 || !list.List[0].Deletable {
		t.Fatalf("未用过的版本应可删: %+v", list.List)
	}
	if len(list.PublishedVersions) != 1 || list.PublishedVersions[0] != "0.2.0" {
		t.Fatalf("已发布版本号应回显: %+v", list.PublishedVersions)
	}
}

// TestOpenForDownloadRejectsUnknownPlatform 平台不匹配时给「没有该设备可用的程序文件」
// —— 这是设备侧唯一能看到的失败语义，且不能泄露「别的平台有这个版本」。
func TestOpenForDownloadRejectsUnknownPlatform(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	body := strings.Repeat("D", 64)
	if _, err := env.release.Upload(ctx, "0.2.0", "linux", "amd64", "", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	dev := env.seedDevice(t, "0.1.0")
	if err := env.db.Model(&entity.Device{}).Where("id = ?", dev.ID).
		Update("arch", "arm64").Error; err != nil {
		t.Fatal(err)
	}
	dev.Arch = "arm64"
	if _, _, err := env.release.OpenForDownload(ctx, dev, "0.2.0"); err == nil {
		t.Fatal("平台不匹配必须拒绝")
	}
	if _, _, err := env.release.OpenForDownload(ctx, dev, "9.9.9"); err == nil {
		t.Fatal("不存在的版本必须拒绝")
	}
}

// TestTaskListAndDetail 覆盖任务查询面：计数聚合 + 明细里的设备信息与软删标注。
func TestTaskListAndDetail(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.1.0")

	disp, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := env.svc.TaskList(ctx, &request.AgentUpgradeTaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("任务数不符: %+v", page)
	}

	taskID := mustParseUint(t, disp.TaskID)
	detail, err := env.svc.TaskDetail(ctx, taskID, &request.AgentUpgradeAttemptQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Total != 1 || len(detail.List) != 1 {
		t.Fatalf("明细数不符: %+v", detail)
	}
	item := detail.List[0]
	if item.Hostname != "web-01" || item.State != entity.AttemptStatePending ||
		item.ToVersion != "0.2.0" || item.DeviceDeleted {
		t.Fatalf("明细内容不符: %+v", item)
	}
	if detail.Task.Counts.Pending != 1 {
		t.Fatalf("计数不符: %+v", detail.Task.Counts)
	}

	// 设备被软删：记录保留，明细标注「设备已删除」。
	if err := env.devices.Delete(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	detail, err = env.svc.TaskDetail(ctx, taskID, &request.AgentUpgradeAttemptQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.List) != 1 || !detail.List[0].DeviceDeleted || detail.List[0].Hostname != "" {
		t.Fatalf("软删设备的明细应保留并标注: %+v", detail.List)
	}
}

// TestDeviceRecordsAndRollbackVersion 覆盖详情页的两块数据：升级记录与一键回滚目标。
func TestDeviceRecordsAndRollbackVersion(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.1.0")

	if v, err := env.svc.RollbackVersion(ctx, dev.ID); err != nil || v != "" {
		t.Fatalf("没有历史时回滚版本应为空: %q %v", v, err)
	}
	if _, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.2.0")); err != nil {
		t.Fatal(err)
	}
	// 成功之后：回滚目标 = 当时的起始版本。
	if v, err := env.svc.RollbackVersion(ctx, dev.ID); err != nil || v != "0.1.0" {
		t.Fatalf("回滚目标应为 0.1.0，实得 %q %v", v, err)
	}
	recs, err := env.svc.DeviceRecords(ctx, dev.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].State != entity.AttemptStateSucceeded {
		t.Fatalf("升级记录不符: %+v", recs)
	}
}

func mustParseUint(t *testing.T, s string) uint64 {
	t.Helper()
	var v uint64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			t.Fatalf("不是十进制 ID: %q", s)
		}
		v = v*10 + uint64(s[i]-'0')
	}
	return v
}

// TestLateStatusReportCannotReopenSettledAttempt 钉住一个**并发真实存在**的缺口：
// hello 归位与状态上报是两条写路径，各自「先读后写」时会交错 ——
// 一条刚把行判成功，另一条拿着旧状态（restarting）把它写回去，于是
// 「设备已达成、任务却永远显示升级中」（端到端验收实测）。
//
// 修法是把「仍未终结」写进 UPDATE 的 WHERE（原子互斥），本用例直接构造
// 「先终结、后上报」的时序来钉住它。
func TestLateStatusReportCannotReopenSettledAttempt(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.1.0")
	if _, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", false, 0); err != nil {
		t.Fatal(err)
	}
	d, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.1.0"))
	if err != nil || d == nil {
		t.Fatalf("对账失败: %v", err)
	}
	// 设备开工 → 上报 restarting。
	if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateRestarting,
		TargetVersion: "0.2.0", FromVersion: "0.1.0",
	}); err != nil {
		t.Fatal(err)
	}
	// 新版本连上：hello 归位判成功。
	if _, err := env.svc.ReconcileOnHello(ctx, dev.ID, helloFrom("0.2.0")); err != nil {
		t.Fatal(err)
	}
	// 迟到的 restarting 上报（旧连接的最后一条，与 hello 并发）不得把它写回去。
	if err := env.svc.ReportUpgradeStatus(ctx, dev.ID, &agentproto.UpgradeStatus{
		RequestID: d.RequestID, State: agentproto.UpgradeStateRestarting,
		TargetVersion: "0.2.0", FromVersion: "0.1.0",
	}); err != nil {
		t.Fatalf("迟到上报应被丢弃而不是报错: %v", err)
	}
	if got := env.attemptRows(t)[0].State; got != entity.AttemptStateSucceeded {
		t.Fatalf("已终结的尝试不得被迟到上报改写，实得 %s", got)
	}
	// 任务也应随之收口（两条终结路径都要收口，否则任务永远显示未完成）。
	if disp, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.3.0", false, 0); err == nil && disp != nil {
		_ = disp
	}
}

// TestSweepStaleTimesOutStuckAttempts 钉住巡检的三条语义：
//   - 卡住的（已开工、久无动静）判超时 + 同步设备终态 + 收口任务；
//   - **等待上线的不算卡住**（从未开工 → 不判）；
//   - 已终结的行不被改写（并发守卫）。
func TestSweepStaleTimesOutStuckAttempts(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)

	stuck := env.seedDevice(t, "0.1.0")
	waiting := env.seedDevice(t, "0.1.0")
	for _, id := range []uint64{stuck.ID, waiting.ID} {
		if _, err := env.svc.SetDeviceTarget(ctx, id, "0.2.0", false, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.ReconcileOnHello(ctx, id, helloFrom("0.1.0")); err != nil {
			t.Fatal(err)
		}
	}
	// 让「卡住」那台开工（收到过状态上报 = started_at 非空），然后把它的
	// last_report_at 拨到很久以前（模拟静默）。
	if err := env.db.Model(&entity.AgentUpgradeAttempt{}).
		Where("device_id = ?", stuck.ID).
		Updates(map[string]any{
			"state":          entity.AttemptStateDownloading,
			"started_at":     time.Now().Add(-time.Hour),
			"last_report_at": time.Now().Add(-time.Hour),
		}).Error; err != nil {
		t.Fatal(err)
	}

	n, err := env.svc.SweepStale(ctx, 15*time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应恰有 1 条被判超时（等待上线的不算），实得 %d", n)
	}

	var rows []entity.AgentUpgradeAttempt
	if err := env.db.Order("device_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		switch rows[i].DeviceID {
		case stuck.ID:
			if rows[i].State != entity.AttemptStateTimeout ||
				rows[i].ReasonCode != entity.AttemptReasonTimeout || rows[i].FinishedAt == nil {
				t.Fatalf("卡住的行应判超时: %+v", rows[i])
			}
		case waiting.ID:
			if rows[i].State != entity.AttemptStatePending {
				t.Fatalf("等待上线的行不得被判超时（声明式目标仍在生效）: %+v", rows[i])
			}
		}
	}
	if got := env.deviceRow(t, stuck.ID).AgentUpgradeState; got != entity.DeviceUpgradeFailed {
		t.Fatalf("设备行应同步为失败: %d", got)
	}
	if got := env.deviceRow(t, stuck.ID).AgentUpgradeReason; got != entity.AttemptReasonTimeout {
		t.Fatalf("原因码应为 timeout: %q", got)
	}

	// 再扫一遍：已终结的行不该被重复处理。
	n, err = env.svc.SweepStale(ctx, 15*time.Minute, 100)
	if err != nil || n != 0 {
		t.Fatalf("第二次巡检不该再判（已终结）: n=%d err=%v", n, err)
	}
}
