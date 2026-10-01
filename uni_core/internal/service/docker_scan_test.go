package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── P3·安全面：image:scan 的缓存快路径（受理秒回）与缓存写入挂钩（ingest）──
//
// 一条贯穿三段的主叙事（TestDockerScanCacheLifecycle）：受理（无缓存 → 下发
// agent）→ result 回写（ingest 挂钩把报告落缓存）→ 再受理（命中 → 秒回、不下发）。
// 拆开的短用例各钉一条纪律（在飞优先于缓存 / 离线可秒回 / 隐式 tag / 坏报告不写缓存）。

// scanImageKey / scanTargetRef 是快照与缓存共用的测试事实。
const (
	scanImageKey  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	scanTargetRef = "nginx:1.27"
)

// scanTestEnv 折一套三件（miniredis + 三个 store），快照里预置一台 docker 可用的
// 主机与一个镜像条目（target 的 tag 与内容键的对应关系在这里建立）。
func scanTestEnv(t *testing.T, tags ...string) (*miniredis.Miniredis, *dockerstate.CmdStore,
	*dockerstate.Store, *dockerstate.ScanCacheStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cmds := dockerstate.NewCmdStore(rdb)
	store := dockerstate.NewStore(rdb)
	cache := dockerstate.NewScanCacheStore(rdb)
	ctx := context.Background()
	st := &agentproto.DockerState{
		T: time.Now().UnixMilli(), DockerOK: true,
		Images: []agentproto.DockerImage{{ID: scanImageKey, RepoTags: tags, SizeMB: 42}},
	}
	if err := store.Save(ctx, 7, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	return mr, cmds, store, cache
}

// scanReportFor 是一份合法报告（image_id 与内容键对齐 —— agent 直发时由它填）。
func scanReportFor(at time.Time) *agentproto.DockerScanReport {
	return &agentproto.DockerScanReport{
		ImageID: scanImageKey, ScannedAt: at.Unix(),
		Counts: agentproto.DockerScanCounts{Critical: 1},
		Vulns: []agentproto.DockerScanVuln{
			{ID: "CVE-2026-0001", Pkg: "openssl", Severity: agentproto.DockerScanSeverityCritical, FixedVersion: "3.0.12"},
		},
	}
}

// scanReq 折一条 image:scan 请求。
func scanReq(target string) *request.DockerCmdReq {
	return &request.DockerCmdReq{Action: agentproto.DockerActionImageScan, Target: target}
}

// TestDockerScanCacheLifecycle：无缓存下发 → 回写落缓存 → 再受理秒回。
func TestDockerScanCacheLifecycle(t *testing.T) {
	_, cmds, store, cache := scanTestEnv(t, scanTargetRef)
	sender := &fakeCmdSender{}
	ingest := NewAgentIngestService(nil, nil, nil, nil, cmds, nil, logger.NewNop()).
		WithDockerScanCache(cache)
	svc := NewDockerCmdService(cmds, sender, store, nil).WithScanCache(cache)
	ctx := context.Background()

	// ① 首次受理：无缓存 → 真下发（pending 期任务中心可见 —— 长任务的可见性
	// 就是 cmd 通道天然给的）。
	ref, err := svc.Send(ctx, 42, 7, scanReq(scanTargetRef))
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Action != agentproto.DockerActionImageScan {
		t.Fatalf("无缓存时必须下发 agent 真扫描: %+v", sender.sent)
	}
	if rec, _ := cmds.Get(ctx, ref); rec == nil || rec.Status != dockerstate.StatusPending {
		t.Fatalf("首扫受理后必须是 pending: %+v", rec)
	}

	// ② agent 回结果：报告进指令记录（轮询可见），ingest 挂钩把它落进缓存
	//（scanned_at 重盖成 core 收帧时刻 —— agent 挂钟不进缓存）。
	agentAt := time.Unix(1790000000, 0) // agent 自称的扫描时刻
	payload, err := json.Marshal(scanReportFor(agentAt))
	if err != nil {
		t.Fatal(err)
	}
	coreAt := time.Unix(1790000500, 0) // 8 分钟后 core 才收到（慢链路/重试）
	ingest.now = func() time.Time { return coreAt }
	if err := ingest.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{
		Ref: ref, OK: true, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	cached, err := cache.Get(ctx, scanImageKey)
	if err != nil || cached == nil {
		t.Fatalf("成功的扫描结果必须落缓存: %v", err)
	}
	if cached.ScannedAt != coreAt.Unix() {
		t.Fatalf("缓存里的 scanned_at 必须是 core 收帧时刻 %d，实际 %d", coreAt.Unix(), cached.ScannedAt)
	}

	// ③ 再次受理同镜像：命中 → 秒回（不下发），照常有 ref 与终态记录。
	ref2, err := svc.Send(ctx, 42, 7, scanReq(scanTargetRef))
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("缓存命中后不得再下发 agent（共 %d 条）", len(sender.sent))
	}
	if ref2 == "" || ref2 == ref {
		t.Fatalf("快路径必须回一个新的 ref: %q", ref2)
	}
	rec, err := svc.Lookup(ctx, 7, ref2)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != dockerstate.StatusSucceeded || rec.FinishedAt == 0 {
		t.Fatalf("快路径的记录必须立刻是终态: %+v", rec)
	}
	var report agentproto.DockerScanReport
	if err := json.Unmarshal(rec.Payload, &report); err != nil || report.ImageID != scanImageKey {
		t.Fatalf("快路径的载荷必须是缓存报告本身: err=%v report=%+v", err, report)
	}
	if report.ScannedAt != coreAt.Unix() {
		t.Fatalf("秒回的报告必须带着缓存时刻（「扫描于 N 小时前」的陈述来源）: %d", report.ScannedAt)
	}
	// 任务中心留痕：最近索引里有这条（枚举 + 逐条 Get 的读面契约）。
	refs, err := cmds.RecentRefs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range refs {
		if r == ref2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("快路径的记录必须进任务中心（「扫了没反应」要靠这条记录缓解）: %v", refs)
	}
	// 快路径的记录是终态：不得挡住同目标的后续指令（在飞索引已清）。
	if inFlight, _ := cmds.Inflight(ctx, 7, agentproto.DockerActionImageScan, scanTargetRef); inFlight != "" {
		t.Fatal("秒回的终态记录不得占用在飞索引")
	}
}

// TestDockerScanFastPathPrecedence：同目标在飞 → 409 优先于缓存秒回
// （在飞的那次扫完会把缓存换新，秒回旧报告反而误导）。
func TestDockerScanFastPathPrecedence(t *testing.T) {
	_, cmds, store, cache := scanTestEnv(t, scanTargetRef)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(cmds, sender, store, nil).WithScanCache(cache)
	ctx := context.Background()

	// 先让一场真扫描在飞（无缓存时下发）。
	if _, err := svc.Send(ctx, 42, 7, scanReq(scanTargetRef)); err != nil {
		t.Fatal(err)
	}
	// 期间缓存出现了（另一台主机扫了同一内容 —— 内容寻址跨主机共享）。
	if err := cache.Save(ctx, scanImageKey, scanReportFor(time.Now()), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, 42, 7, scanReq(scanTargetRef)); err == nil ||
		!strings.Contains(err.Error(), "已有同一条指令在执行") {
		t.Fatalf("在飞必须 409 而不是秒回: %v", err)
	}
}

// TestDockerScanFastPathOffline：缓存命中不依赖主机在线 —— 报告是镜像内容的
// 事实，主机掉线期间用户仍能看到「N 小时前扫的报告」。
func TestDockerScanFastPathOffline(t *testing.T) {
	_, cmds, store, cache := scanTestEnv(t, scanTargetRef)
	sender := &fakeCmdSender{offline: true}
	svc := NewDockerCmdService(cmds, sender, store, nil).WithScanCache(cache)
	ctx := context.Background()
	if err := cache.Save(ctx, scanImageKey, scanReportFor(time.Now()), time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, err := svc.Send(ctx, 42, 7, scanReq(scanTargetRef))
	if err != nil {
		t.Fatalf("离线 + 缓存命中必须秒回（不下发）: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("秒回不得触碰发送面")
	}
	if rec, _ := cmds.Get(ctx, ref); rec == nil || rec.Status != dockerstate.StatusSucceeded {
		t.Fatalf("秒回的记录必须立刻终态: %+v", rec)
	}
}

// TestDockerScanFastPathImplicitLatest：target 不带 tag 时按 daemon 口径补
// :latest 再匹配快照的 RepoTags（「nginx」与「nginx:latest」是同一个镜像）。
func TestDockerScanFastPathImplicitLatest(t *testing.T) {
	_, cmds, store, cache := scanTestEnv(t, "nginx:latest")
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(cmds, sender, store, nil).WithScanCache(cache)
	ctx := context.Background()
	if err := cache.Save(ctx, scanImageKey, scanReportFor(time.Now()), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, 42, 7, scanReq("nginx")); err != nil {
		t.Fatalf("隐式 :latest 必须命中同内容键: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("隐式 tag 命中后必须秒回")
	}
}

// TestDockerScanFastPathUnknownTarget：快照里没有这个 target（刚 pull 快照未
// 更新）→ 回落真扫描 —— 内容键解析不出来不构成拒绝，agent 的 ImageInspect 才是
// 「镜像存不存在」的权威判定。
func TestDockerScanFastPathUnknownTarget(t *testing.T) {
	_, cmds, store, cache := scanTestEnv(t, scanTargetRef)
	sender := &fakeCmdSender{}
	svc := NewDockerCmdService(cmds, sender, store, nil).WithScanCache(cache)
	ctx := context.Background()
	if err := cache.Save(ctx, scanImageKey, scanReportFor(time.Now()), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, 42, 7, scanReq("redis:7.4")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatal("快照里找不到 target 时必须下发真扫描（agent 才是权威判定）")
	}
}

// TestDockerScanIngestBadReport：报告解码失败 / image_id 形态不合法 → 只 warn
// 不写缓存（结果回写主链不受影响 —— 缓存是加速器，坏数据进不了键空间）。
func TestDockerScanIngestBadReport(t *testing.T) {
	_, cmds, _, cache := scanTestEnv(t)
	svc := NewAgentIngestService(nil, nil, nil, nil, cmds, nil, logger.NewNop()).
		WithDockerScanCache(cache)
	ctx := context.Background()

	cases := []struct {
		name    string
		payload []byte
	}{
		// 注意「非 JSON 的 payload」在这里测不到：CmdStore.Complete 会把
		// Payload(json.RawMessage) 编回记录，RawMessage 的 MarshalJSON 会先验证
		// JSON 合法性 —— 非 JSON 字节在任何 action 上都会在那一层失败（既有
		// 行为，不是扫描特有）。这里测的是「合法 JSON 但不是合法报告」的形态。
		{"payload 是 JSON 但不是报告", []byte(`42`)},
		{"image_id 形态不合法", mustScanJSON(t, &agentproto.DockerScanReport{ImageID: "nginx:1.27", ScannedAt: 1})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &dockerstate.CmdRecord{Ref: "1790000000009", DeviceID: 7,
				Action: agentproto.DockerActionImageScan, Target: scanTargetRef, UserID: 42, Perm: "docker:manage"}
			if err := cmds.Create(ctx, rec, time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: true, Payload: c.payload}); err != nil {
				t.Fatal(err)
			}
			if cached, err := cache.Get(ctx, scanImageKey); err != nil || cached != nil {
				t.Fatalf("坏报告不得写缓存: cached=%v err=%v", cached, err)
			}
			// 指令主链不受影响：记录已成功（报告原样在 payload 里）。
			got, err := cmds.Get(ctx, rec.Ref)
			if err != nil || got == nil || got.Status != dockerstate.StatusSucceeded {
				t.Fatalf("结果回写必须照常成功: err=%v rec=%+v", err, got)
			}
		})
	}
}

func mustScanJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
