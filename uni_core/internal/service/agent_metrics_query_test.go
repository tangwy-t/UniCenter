package service

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

func f64p(f float64) *float64 { return &f }

// maxBucketsForTest 与实现里的上限**同源**（MaxBuckets），避免测试里出现第二个 4000。
const maxBucketsForTest = MaxBuckets

// assertBadRequest 断言错误是 400（apperror.BadRequest）。
func assertBadRequest(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：必须报 400，但返回 nil", what)
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("%s：必须是 apperror.AppError，got %T (%v)", what, err, err)
	}
	if ae.HTTPStatus != 400 {
		t.Fatalf("%s：HTTPStatus = %d, want 400（%v）", what, ae.HTTPStatus, err)
	}
}

func TestSelectTierBoundaries(t *testing.T) {
	cases := []struct {
		rangeSec int64
		source   string
		table    string
		res      int64
		wantErr  bool
	}{
		{3600, "redis", "", 10, false},                    // 1h → Redis 原始
		{86400, "redis", "", 10, false},                   // 24h 边界仍在 Redis
		{86401, "db", "device_metric_5m", 300, false},     // 刚过 24h → 5min 档
		{2592000, "db", "device_metric_5m", 300, false},   // 30d 边界
		{2592001, "db", "device_metric_1h", 3600, false},  // 刚过 30d → 1h 档
		{15552000, "db", "device_metric_1h", 3600, false}, // 半年
		{3599, "", "", 0, true},                           // 小于 1h → 400
		{15552001, "", "", 0, true},                       // 超过半年 → 400
		{0, "", "", 0, true},                              // 缺省 → 由 service 填默认值，此处视为越界
	}
	for _, c := range cases {
		got, err := SelectTier(c.rangeSec)
		if c.wantErr {
			assertBadRequest(t, "range="+itoa(c.rangeSec), err)
			continue
		}
		if err != nil {
			t.Errorf("range=%d 不应报错: %v", c.rangeSec, err)
			continue
		}
		if got.Source != c.source || got.Table != c.table || got.Resolution != c.res {
			t.Errorf("range=%d → %+v, want source=%s table=%s res=%d", c.rangeSec, got, c.source, c.table, c.res)
		}
		// 越界之外的每一条都必须带 RangeSeconds 且 Scale >= 1（升档倍数语义）
		if got.RangeSeconds != c.rangeSec {
			t.Errorf("range=%d → RangeSeconds=%d 未回填", c.rangeSec, got.RangeSeconds)
		}
		if got.Scale < 1 {
			t.Errorf("range=%d → Scale=%d，必须 >= 1", c.rangeSec, got.Scale)
		}
		// Redis 档不得出现 DB 表名，反之亦然（选档即选表，不留混用缝）
		if c.source == "redis" && got.Table != "" {
			t.Errorf("range=%d → Redis 档不得带表名, got %q", c.rangeSec, got.Table)
		}
		if c.source == "db" && got.Table == "" {
			t.Errorf("range=%d → DB 档必须带表名", c.rangeSec)
		}
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestSelectTierRejectsOutOfRange(t *testing.T) {
	for _, r := range []int64{-1, 0, 1, 3599, request.DeviceRangeMin - 1, request.DeviceRangeMax + 1} {
		_, err := SelectTier(r)
		assertBadRequest(t, "越界 range="+itoa(r), err)
	}
}

func TestSelectTierNeverMixesTiers(t *testing.T) {
	// 40 天必须整体走 1h 档（不允许前 30 天 5min + 后 10 天 1h 拼接）
	got, err := SelectTier(40 * 24 * 3600)
	if err != nil {
		t.Fatal(err)
	}
	if got.Table != "device_metric_1h" {
		t.Fatalf("40d 必须整体走 1h 档, got %q", got.Table)
	}
	if got.Source != "db" {
		t.Fatalf("source = %q, want db", got.Source)
	}
	if got.Resolution != 3600 {
		t.Fatalf("40d 原生分辨率 = %d, want 3600（不得回落 300s 与 1h 混拼）", got.Resolution)
	}
}

func TestStepLiftForBucketCap(t *testing.T) {
	// 180d @1h = 4320 桶 > 4000 → 升 step 到 2h（Scale=2），桶数 2160
	got, err := SelectTier(15552000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scale != 2 {
		t.Fatalf("180d@1h = 4320 桶，必须升到 Scale=2（4320<=4000<8640 时为 2）, got Scale=%d", got.Scale)
	}
	if step := got.Resolution * got.Scale; step != 7200 {
		t.Fatalf("180d 有效桶宽 = %d, want 7200（spec §7.2「半年→2h」）", step)
	}
	if n := got.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("升档后桶数 %d 仍超过 %d 上限", n, maxBucketsForTest)
	}
	// 30d @300s = 8640 桶 → Scale=3 → 2880
	got30, err := SelectTier(2592000)
	if err != nil {
		t.Fatal(err)
	}
	if got30.Scale != 3 {
		t.Fatalf("30d@300s = 8640 桶，必须升到 Scale=3, got Scale=%d", got30.Scale)
	}
	if step := got30.Resolution * got30.Scale; step != 900 {
		t.Fatalf("30d 有效桶宽 = %d, want 900（spec §7.2「30d→15min」）", step)
	}
	if n := got30.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("30d 升档后桶数 %d 超过上限", n)
	}
}

func TestStepLiftKeepsNativeMultiplesWithinCap(t *testing.T) {
	// 升 step 仍是**原生桶宽的整数倍**（桶边界仍对齐）；且每个合法 range 都不越上限。
	ranges := []int64{3600, 7200, 86400, 86401, 604800, 2592000, 2592001, 7776000, 15552000}
	for _, r := range ranges {
		got, err := SelectTier(r)
		if err != nil {
			t.Fatalf("range=%d: %v", r, err)
		}
		step := got.Resolution * got.Scale
		if step <= 0 || step%got.Resolution != 0 {
			t.Fatalf("range=%d: 有效桶宽 %d 不是原生 %d 的整数倍", r, step, got.Resolution)
		}
		if n := got.BucketCount(); n > maxBucketsForTest {
			t.Fatalf("range=%d: 升档后桶数 %d 超过 %d", r, n, maxBucketsForTest)
		}
	}
}

func TestStepLiftAppliesToRedisTier(t *testing.T) {
	// 24h @10s = 8640 桶 > 4000 → Scale=3 → 2880 桶（有效桶宽 30s）；
	// 但 Resolution 报的是**原生** 10s，前端只信 response.resolution_seconds。
	got, err := SelectTier(86400)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolution != 10 {
		t.Fatalf("Redis 档原生分辨率 = %d, want 10", got.Resolution)
	}
	if step := got.Resolution * got.Scale; step != 30 {
		t.Fatalf("24h Redis 档有效桶宽 = %d, want 30（8640 桶需升 k=3）", step)
	}
	if n := got.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("24h 升档后桶数 %d 超过上限", n)
	}
}

func TestMetricsRejectsKindNameMismatch(t *testing.T) {
	svc := &AgentMetricsQueryService{}
	// 只给 kind 不给 name → 必须 400
	_, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk"})
	assertBadRequest(t, "只给 kind 不给 name", err)

	// 只给 name 不给 kind → 必须 400（成对出现，反向同样成立）
	_, err = svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Name: "/"})
	assertBadRequest(t, "只给 name 不给 kind", err)

	// kind+name 同时给 → 走资源下钻，整机趋势接口必须拒绝
	_, err = svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk", Name: "/"})
	assertBadRequest(t, "整机趋势收到 kind/name", err)
}

func TestMetricsRejectsNilQuery(t *testing.T) {
	svc := &AgentMetricsQueryService{}
	_, err := svc.Metrics(context.Background(), 1001, nil)
	assertBadRequest(t, "nil 查询参数", err)
}

func TestMetricsDefaultsRangeTo24h(t *testing.T) {
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RangeSeconds != 24*3600 {
		t.Fatalf("缺省 range → RangeSeconds = %d, want 86400", resp.RangeSeconds)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("缺省 range 走 Redis, got %q", resp.Source)
	}
	if raw.gotWindow != 24*time.Hour {
		t.Fatalf("热层窗口 = %v, want 24h", raw.gotWindow)
	}
}

func TestMetricsUsesRawStoreWithin24h(t *testing.T) {
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{
		WindowSeconds: 86400, StepSeconds: 30,
		Buckets: []agentmetrics.TrendPoint{{T: 1234, CPUUsedPercent: f64p(11.5)}},
	}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 86400, Metrics: "cpu_used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("source = %q, want redis", resp.Source)
	}
	if resp.ResolutionSeconds != 30 {
		t.Fatalf("resolution_seconds = %d, want 30（8640 桶 → k=3）", resp.ResolutionSeconds)
	}
	if raw.gotDevice != 1001 || raw.gotWindow != 24*time.Hour || raw.gotStep != 30*time.Second {
		t.Fatalf("热层入参 = device=%d window=%v step=%v", raw.gotDevice, raw.gotWindow, raw.gotStep)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].T != 1234 {
		t.Fatalf("buckets 未归一: %+v", resp.Buckets)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 11.5 {
		t.Fatalf("cpu_used_percent = %v, want 11.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("热层未给的列必须保持 nil（缺 ≠ 0）, got %v", *resp.Buckets[0].Load1)
	}
}

func TestMetricsUsesFiveMinuteTableForSevenDays(t *testing.T) {
	reader := &stubMetricReader{trend: []agentmetrics.TrendPoint{{T: 300, CPUUsedPercent: f64p(3)}}}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 7 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if reader.gotTable != "device_metric_5m" {
		t.Fatalf("7d 必须查 _5m, got %q", reader.gotTable)
	}
	if resp.Source != SourceDB || resp.ResolutionSeconds != 300 {
		t.Fatalf("source=%q resolution=%d, want db/300", resp.Source, resp.ResolutionSeconds)
	}
	if reader.gotDevice != 1001 {
		t.Fatalf("仓储入参 deviceID = %d, want 1001", reader.gotDevice)
	}
	if reader.gotFrom != reader.gotTo-7*24*3600 {
		t.Fatalf("查询窗口 = [%d, %d]，跨度不是 7d", reader.gotFrom, reader.gotTo)
	}
	if !slices.Equal(reader.gotCols, defaultMetricColumns) {
		t.Fatalf("缺省投影列 = %v, want %v", reader.gotCols, defaultMetricColumns)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].T != 300 {
		t.Fatalf("buckets 未归一: %+v", resp.Buckets)
	}
}

func TestMetricsUsesHourlyTableBeyondThirtyDays(t *testing.T) {
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 40 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if reader.gotTable != "device_metric_1h" {
		t.Fatalf("40d 必须整体查 _1h, got %q", reader.gotTable)
	}
	if resp.ResolutionSeconds != 3600 {
		t.Fatalf("40d resolution_seconds = %d, want 3600", resp.ResolutionSeconds)
	}
}

func TestMetricsPropagatesStoreError(t *testing.T) {
	reader := &stubMetricReader{err: errors.New("db down")}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	_, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 7 * 24 * 3600})
	if err == nil {
		t.Fatal("仓储报错必须向上抛")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 apperror.AppError, got %T", err)
	}
}

// ---------- 下钻 ----------

func TestResourceMetricsRejectsIncompleteOrUnknownKind(t *testing.T) {
	svc := NewAgentMetricsQueryService(nil, nil, &stubResourceResolver{}, logger.NewNop())
	ctx := context.Background()
	assertBadRequest(t, "下钻只给 kind",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk"})))
	assertBadRequest(t, "下钻只给 name",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Name: "/"})))
	assertBadRequest(t, "下钻 nil 参数",
		mustErr(svc.ResourceMetrics(ctx, 1001, nil)))
	assertBadRequest(t, "不支持的 kind",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "gpu", Name: "0"})))
}

// mustErr 把 (resp, err) 折成 err，便于表驱动断言 400。
func mustErr[T any](_ T, err error) error { return err }

func TestResourceMetricsRejectsRangeBeyondThirtyDays(t *testing.T) {
	ctx := context.Background()
	svc := NewAgentMetricsQueryService(nil, nil, &stubResourceResolver{id: 2001}, logger.NewNop())
	// 子表只有 5min 档：>30d 落到 1h 档 → 400（不得静默返回空曲线）
	assertBadRequest(t, "40d 资源下钻",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
			Range: 40 * 24 * 3600, Kind: "disk", Name: "/"})))
	assertBadRequest(t, "半年资源下钻",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
			Range: request.DeviceRangeMax, Kind: "nic", Name: "eth0"})))

	// 30d 边界本身合法（仍是 5min 档），且必须解析出 resource_id 后按子表查
	reader := &stubMetricReader{resourceRows: []map[string]any{{"t": int64(600)}}}
	resolver := &stubResourceResolver{id: 2001}
	svc2 := NewAgentMetricsQueryService(nil, reader, resolver, logger.NewNop())
	resp, err := svc2.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 30 * 24 * 3600, Kind: "disk", Name: "/"})
	if err != nil {
		t.Fatalf("30d 资源下钻必须合法: %v", err)
	}
	if reader.gotTable != "device_metric_disk" || reader.gotResourceID != 2001 {
		t.Fatalf("下钻入参 table=%q resourceID=%d, want device_metric_disk/2001", reader.gotTable, reader.gotResourceID)
	}
	if resolver.gotKind != "disk" || resolver.gotName != "/" {
		t.Fatalf("ResolveID 入参 kind=%q name=%q", resolver.gotKind, resolver.gotName)
	}
	if resp.ResourceKind != "disk" || resp.Name != "/" || len(resp.Buckets) != 1 {
		t.Fatalf("下钻响应不完整: %+v", resp)
	}
}

func TestResourceMetricsMapsEveryKindToItsSubTable(t *testing.T) {
	// 注：range 用 7d（DB 档）—— ≤24h 的下钻按 D7 走 Redis 原始，不再碰子表，
	// 本测试要锁的是「kind → 子表」的映射，故必须落在 DB 档（Task 8b 的夹具修正）。
	want := map[string]string{
		"disk": "device_metric_disk", "disk_io": "device_metric_diskio",
		"nic": "device_metric_nic", "sensor": "device_metric_sensor",
	}
	for kind, table := range want {
		reader := &stubMetricReader{}
		svc := NewAgentMetricsQueryService(nil, reader, &stubResourceResolver{id: 7}, logger.NewNop())
		if _, err := svc.ResourceMetrics(context.Background(), 1001,
			&request.DeviceMetricsQuery{Range: 7 * 24 * 3600, Kind: kind, Name: "x"}); err != nil {
			t.Fatalf("kind=%s 下钻报错: %v", kind, err)
		}
		if reader.gotTable != table {
			t.Fatalf("kind=%s → 表 %q, want %q", kind, reader.gotTable, table)
		}
	}
}

func TestResourceMetricsUnknownResourceReturnsEmptyNot404(t *testing.T) {
	// 设备可能刚卸载该资源：ResolveID 未命中必须返回**空结果**，不得 404、不得报错。
	//
	// 注：range=6h 属 ≤24h（D7 → Redis 原始），热层下钻**不查 device_resource**
	// （刚挂载的资源可能还没落库），故「资源不存在」在这里表现为「热层没有该资源的样本」。
	// DB 档的同一语义由 TestResourceDrillDBPathUnknownResourceReturnsEmpty 覆盖。
	resolver := &stubResourceResolver{err: errors.New("record not found")}
	reader := &stubMetricReader{err: errors.New("未命中资源时不得查子表")}
	raw := &stubRawQuerier{} // 热层里没有该资源的样本
	svc := NewAgentMetricsQueryService(raw, reader, resolver, logger.NewNop())
	resp, err := svc.ResourceMetrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 6 * 3600, Kind: "disk", Name: "/data"})
	if err != nil {
		t.Fatalf("资源不存在必须返回空结果而不是错误: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("未命中资源时不得查子表（calls=%d）", reader.calls)
	}
	if resp == nil {
		t.Fatal("必须返回响应体（空 buckets），不能 nil")
	}
	if resp.Buckets == nil || len(resp.Buckets) != 0 {
		t.Fatalf("buckets 必须是空切片而不是 nil/有值: %#v", resp.Buckets)
	}
	if resp.ResourceKind != "disk" || resp.Name != "/data" {
		t.Fatalf("回显 kind/name 缺失: %+v", resp)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("6h 下钻 source = %q, want redis 档位语义", resp.Source)
	}
}

// ---------- 白名单 / 归一 ----------

// newQueryTestRepo 起内存 sqlite + 生产同款雪花回调，并 AutoMigrate 指标表。
func newQueryTestRepo(t *testing.T) *repository.DeviceMetricRepo {
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
	if err := db.AutoMigrate(
		&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{},
		&entity.DeviceMetricDiskIO{}, &entity.DeviceMetricNIC{}, &entity.DeviceMetricSensor{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(entity.TableNameMetric1h).AutoMigrate(&entity.DeviceMetricWide{}); err != nil {
		t.Fatal(err)
	}
	return repository.NewDeviceMetricRepository(db)
}

func TestMetricsProjectionGoesThroughRepositoryWhitelist(t *testing.T) {
	repo := newQueryTestRepo(t)
	svc := NewAgentMetricsQueryService(nil, repo, nil, logger.NewNop())
	ctx := context.Background()

	// 非白名单列（内含注入载荷）必须被仓储白名单拦下，不得静默通过
	_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent,load1); DROP TABLE device_metric_5m;--"})
	if err == nil {
		t.Fatal("非白名单列必须报错（拒绝任意列名拼接）")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 apperror.AppError, got %T", err)
	}

	// 注入未生效：正常投影查询仍然可用
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, CPUUsedPercent: f64p(7.5), Load1: f64p(0.5), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}
	// 同一个桶上另一个设备的行不得串进来
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1002, BucketTS: now - 300, CPUUsedPercent: f64p(99), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "bucket_ts,cpu_used_percent"})
	if err != nil {
		t.Fatalf("注入尝试后正常查询必须仍然可用: %v", err)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1（只回本设备）", len(resp.Buckets))
	}
	if resp.Buckets[0].T != now-300 {
		t.Fatalf("t = %d, want %d（bucket_ts 必须映射到 TrendPoint.T）", resp.Buckets[0].T, now-300)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 7.5 {
		t.Fatalf("cpu_used_percent = %v, want 7.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("未请求的列必须保持 nil（白名单投影）, got %v", *resp.Buckets[0].Load1)
	}
}

func TestToMetricPointsPreservesNil(t *testing.T) {
	got := toMetricPoints([]agentmetrics.TrendPoint{
		{T: 60, CPUUsedPercent: f64p(12.5), Samples: 3},
		{T: 120}, // 全空桶：t 必须保留，其余列省略
	})
	if len(got) != 2 {
		t.Fatalf("点数 = %d, want 2", len(got))
	}
	if got[0].T != 60 || got[0].Samples != 3 || got[0].CPUUsedPercent == nil || *got[0].CPUUsedPercent != 12.5 {
		t.Fatalf("有值桶映射错误: %+v", got[0])
	}
	if got[0].MemUsedPercent != nil || got[0].NICRXBytesSec != nil {
		t.Fatal("缺 ≠ 0：未采集的列必须保持 nil")
	}
	if got[1].T != 120 {
		t.Fatalf("空桶必须保留 t（稀疏桶靠 t 定位）, got %d", got[1].T)
	}
	if got[1].CPUUsedPercent != nil {
		t.Fatal("空桶不得被填 0")
	}
}

// ---------- 契约缺口闭合（Task 8b，逐条对齐 spec §8）----------

// TestMetricsAllExpandsToWholeTableColumns：D4 —— `metrics=*` 回该表**全量值列**。
//
// 缺陷背景（实测）：`sanitizeColumns(table, nil)` 的语义是「只要 bucket_ts」，
// 而 service 把 `*` 直接交给仓储（返回 nil）→ `metrics=*` 只回 bucket_ts、
// **没有任何值列**，与 spec §8 明文「metrics=* 回全量」相反。
func TestMetricsAllExpandsToWholeTableColumns(t *testing.T) {
	ctx := context.Background()
	want, err := repository.MetricQueryColumns("device_metric_5m")
	if err != nil {
		t.Fatal(err)
	}

	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: request.MetricsAll})
	if err != nil {
		t.Fatalf("metrics=* 必须可用: %v", err)
	}
	if len(resp.AvailableMetrics) <= 8 {
		t.Fatalf("metrics=* 的可用列只有 %d 个（%v）—— 修前实测只回 bucket_ts，即 1 个",
			len(resp.AvailableMetrics), resp.AvailableMetrics)
	}
	if !slices.Contains(resp.AvailableMetrics, "nic_rx_bytes_sec") {
		t.Fatalf("metrics=* 必须含速率列 nic_rx_bytes_sec, got %v", resp.AvailableMetrics)
	}
	// 与「该表全部值列 + bucket_ts」逐列相等（不多不少）
	if len(resp.AvailableMetrics) != len(want)+1 {
		t.Fatalf("可用列数 = %d, want %d（全量值列 %d + bucket_ts）: %v",
			len(resp.AvailableMetrics), len(want)+1, len(want), resp.AvailableMetrics)
	}
	for _, c := range want {
		if !slices.Contains(resp.AvailableMetrics, c) {
			t.Fatalf("metrics=* 漏列 %q（%v）", c, resp.AvailableMetrics)
		}
	}
	// 展开必须**真下推**到仓储投影（只把 available_metrics 写大是假修）
	if !slices.Equal(reader.gotCols, resp.AvailableMetrics) {
		t.Fatalf("投影列 = %v, 响应可用列 = %v —— 必须一致", reader.gotCols, resp.AvailableMetrics)
	}

	// 1h 档：* 只回**该档位产出**的列。
	// 为什么：spec §8 要求 available_metrics 让前端区分「该档位无此指标」；
	// 若把 1h 档恒为 nil 的 5min-only 列也报成可用，就退回了「半年视图上 tcp_time_wait
	// 与 agent_* 恒显示 —，被误读成 agent 挂了」的老问题。同时也不得直接把 * 拒成 400
	// （那会让文档化的 metrics=* 在半年档完全不可用）。
	reader1h := &stubMetricReader{}
	svc1h := NewAgentMetricsQueryService(nil, reader1h, nil, logger.NewNop())
	resp1h, err := svc1h.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 40 * 24 * 3600, Metrics: request.MetricsAll})
	if err != nil {
		t.Fatalf("1h 档的 metrics=* 必须可用（不得 400）: %v", err)
	}
	for _, dead := range []string{"tcp_time_wait", "tcp_close_wait", "agent_ws_reconnect_count", "agent_last_report_error"} {
		if slices.Contains(resp1h.AvailableMetrics, dead) {
			t.Fatalf("1h 档不产出 %q，不得报成可用列: %v", dead, resp1h.AvailableMetrics)
		}
	}
	if !slices.Contains(resp1h.AvailableMetrics, "nic_rx_bytes_sec") || len(resp1h.AvailableMetrics) <= 8 {
		t.Fatalf("1h 档的 * 仍必须回该档的值列: %v", resp1h.AvailableMetrics)
	}
}

// TestBucketTSAlwaysProjectedEvenWhenMetricsNamed：D5 —— `t` 是契约的一部分。
//
// 缺陷背景（实测）：显式 `metrics=cpu_used_percent`（不含 bucket_ts）时投影里没有
// bucket_ts，扫出来的**每个点 t 都是 0**，前端按 t 定位时间全落在 1970。
func TestBucketTSAlwaysProjectedEvenWhenMetricsNamed(t *testing.T) {
	ctx := context.Background()

	// 投影列侧：bucket_ts 必须被恒补后下推到仓储
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(reader.gotCols, "bucket_ts") {
		t.Fatalf("显式 metrics 未含 bucket_ts 时必须恒补（D5）, got 投影列 %v", reader.gotCols)
	}

	// 端到端侧：真库投影 → 桶里的 t 必须非 0
	repo := newQueryTestRepo(t)
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, CPUUsedPercent: f64p(7.5), Load1: f64p(0.5), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}
	svcDB := NewAgentMetricsQueryService(nil, repo, nil, logger.NewNop())
	resp, err := svcDB.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(resp.Buckets))
	}
	if resp.Buckets[0].T != now-300 {
		t.Fatalf("t = %d, want %d —— 显式 metrics 时投影缺了 bucket_ts（t 恒 0）",
			resp.Buckets[0].T, now-300)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 7.5 {
		t.Fatalf("cpu_used_percent = %v, want 7.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("未请求的列必须保持 nil（白名单投影语义不变）, got %v", *resp.Buckets[0].Load1)
	}
}

// TestTierRejectsFiveMinOnlyColumnsOnOneHourTier：D6 —— 1h 档不存在的列被**显式请求** → 400。
//
// spec §8 明文：「range > 2592000 且 metrics 含 5min-only 列（tcp_time_wait/
// tcp_close_wait/agent_*）时返回 BadRequest，**不静默剔除**（与 kind+name 的校验同构）」。
func TestTierRejectsFiveMinOnlyColumnsOnOneHourTier(t *testing.T) {
	ctx := context.Background()
	for _, col := range []string{"tcp_time_wait", "tcp_close_wait", "agent_ws_reconnect_count", "agent_last_report_error"} {
		reader := &stubMetricReader{}
		svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
		_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 40 * 24 * 3600, Metrics: col})
		assertBadRequest(t, "1h 档显式请求 "+col, err)
		if reader.calls != 0 {
			t.Fatalf("必须在查库之前拒绝（不静默剔除、也不白跑一次查询）: col=%s calls=%d", col, reader.calls)
		}
	}

	// 同档位、不含 5min-only 列 → 正常
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, logger.NewNop())
	if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 40 * 24 * 3600, Metrics: "cpu_used_percent,tcp_total"}); err != nil {
		t.Fatalf("1h 档请求该档存在的列不得报错: %v", err)
	}
	if reader.gotTable != "device_metric_1h" {
		t.Fatalf("40d 必须整体走 _1h, got %q", reader.gotTable)
	}

	// 反向对照：这些列在 5min 档与 Redis 档**真实存在**，不得被误拒
	reader5m := &stubMetricReader{}
	svc5m := NewAgentMetricsQueryService(nil, reader5m, nil, logger.NewNop())
	if _, err := svc5m.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 30 * 24 * 3600, Metrics: "tcp_time_wait"}); err != nil {
		t.Fatalf("5min 档的 tcp_time_wait 必须可用: %v", err)
	}
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svcRaw := NewAgentMetricsQueryService(raw, nil, nil, logger.NewNop())
	if _, err := svcRaw.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 24 * 3600, Metrics: "tcp_time_wait"}); err != nil {
		t.Fatalf("Redis 档的 tcp_time_wait 必须可用: %v", err)
	}
}

// TestResourceDrillUsesSubTableColumns：D2+D3 —— 默认下钻（只传 kind+name）必须成功，
// 且可用列/值列都是**子表自己的列**。
//
// 缺陷背景（实测）：不传 metrics 时用的是**宽表**默认列 → 被子表白名单拒绝 → 500
// （console 的默认下钻必然失败）；即使换了列名，子表列名与 TrendPoint 字段也不同名，
// 类型化扫描会让桶的**值列全为 nil**（GORM 映射不上不报错）。
func TestResourceDrillUsesSubTableColumns(t *testing.T) {
	ctx := context.Background()
	want, ok := repository.ResourceTableColumns("device_metric_disk")
	if !ok {
		t.Fatal("device_metric_disk 必须登记下钻列集")
	}

	repo := newQueryTestRepo(t)
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, Samples: 30,
	}, repository.MetricSubRows{Disks: []entity.DeviceMetricDisk{{
		ResourceID: 2001, BucketTS: now - 300,
		UsedPercent: f64p(62), UsedGB: f64p(620), TotalGB: f64p(1000), InodesUsedPercent: f64p(12),
	}}}); err != nil {
		t.Fatal(err)
	}

	svc := NewAgentMetricsQueryService(nil, repo, &stubResourceResolver{id: 2001}, logger.NewNop())
	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Kind: "disk", Name: "/"})
	if err != nil {
		t.Fatalf("只传 kind+name 的默认下钻必须成功（console 默认路径）: %v", err)
	}
	if len(resp.AvailableMetrics) != len(want) {
		t.Fatalf("available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, want)
	}
	for _, c := range want {
		if !slices.Contains(resp.AvailableMetrics, c) {
			t.Fatalf("available_metrics 漏列 %q: %v", c, resp.AvailableMetrics)
		}
	}
	if slices.Contains(resp.AvailableMetrics, "cpu_used_percent") {
		t.Fatalf("下钻的可用列不得是宽表列: %v", resp.AvailableMetrics)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1: %+v", len(resp.Buckets), resp.Buckets)
	}
	b := resp.Buckets[0]
	if b.T != now-300 {
		t.Fatalf("t = %d, want %d", b.T, now-300)
	}
	for col, wantVal := range map[string]float64{
		"used_percent": 62, "used_gb": 620, "total_gb": 1000, "inodes_used_percent": 12,
	} {
		v, ok := b.Values[col]
		if !ok || v == nil {
			t.Fatalf("values 缺列 %q（修前实测：下钻桶的值列全为 nil）: %v", col, b.Values)
		}
		if *v != wantVal {
			t.Fatalf("values[%q] = %v, want %v", col, *v, wantVal)
		}
	}
}

// TestResourceDrillWithin24hUsesHotLayer：D7 —— ≤24h 走 Redis 原始 + AggregateResource，
// DB 子表**不得被调用**（spec §7.2 选档表）。
func TestResourceDrillWithin24hUsesHotLayer(t *testing.T) {
	ctx := context.Background()
	const t0 = int64(1_700_000_000_000) // 对齐到 10s 桶（1700000000s 与 1700000004s 同桶）
	raw := &stubRawQuerier{bucketPts: []agentproto.MetricsSample{
		{T: t0, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/data", UsedPercent: 10, UsedGB: 10, TotalGB: 100, InodesUsedPercent: 8},
		}},
		{T: t0 + 4000, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/", UsedPercent: 99, UsedGB: 99, TotalGB: 100}, // 不得串进 "/data"
			{Mountpoint: "/data", UsedPercent: 20, UsedGB: 20, TotalGB: 100, InodesUsedPercent: 12},
		}},
	}}
	// DB 侧一律报错：一旦下钻 ≤24h 还去打子表，本测试必红（同时证明「未被调用」）
	reader := &stubMetricReader{err: errors.New("下钻 ≤24h 不得落 DB 子表")}
	resolver := &stubResourceResolver{id: 2001}
	svc := NewAgentMetricsQueryService(raw, reader, resolver, logger.NewNop())

	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data"})
	if err != nil {
		t.Fatalf("1h 下钻必须走热层: %v", err)
	}
	if raw.bucketCalls != 1 {
		t.Fatalf("热层 Bucket 调用次数 = %d, want 1", raw.bucketCalls)
	}
	if reader.calls != 0 {
		t.Fatalf("下钻 ≤24h 不得查 DB 子表（calls=%d）", reader.calls)
	}
	if resolver.calls != 0 {
		t.Fatalf("热层下钻不需要 device_resource 解析（刚挂载的资源可能还没落库）, calls=%d", resolver.calls)
	}
	if resp.Source != SourceRedis || resp.ResolutionSeconds != 10 {
		t.Fatalf("source=%q resolution=%d, want redis/10", resp.Source, resp.ResolutionSeconds)
	}
	if raw.gotDevice != 1001 || raw.gotToMs-raw.gotFromMs != 3600*1000 {
		t.Fatalf("热层入参 device=%d 窗口=[%d, %d]，跨度不是 1h", raw.gotDevice, raw.gotFromMs, raw.gotToMs)
	}
	// 两档的可用列集必须逐字一致（前端切档不丢列）
	wantCols, _ := repository.ResourceTableColumns("device_metric_disk")
	if len(resp.AvailableMetrics) != len(wantCols) {
		t.Fatalf("热层 available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, wantCols)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1（两个样本同 10s 桶）: %+v", len(resp.Buckets), resp.Buckets)
	}
	b := resp.Buckets[0]
	if b.T != t0/1000 {
		t.Fatalf("桶时间 = %d, want %d（按 bucketSec 对齐）", b.T, t0/1000)
	}
	if b.Samples != 2 {
		t.Fatalf("samples = %d, want 2（命中该资源的样本数）", b.Samples)
	}
	for col, wantVal := range map[string]float64{
		"used_percent": 15, "used_gb": 15, "total_gb": 100, "inodes_used_percent": 10,
	} {
		v, ok := b.Values[col]
		if !ok || v == nil {
			t.Fatalf("热层 values 缺列 %q: %v", col, b.Values)
		}
		if *v != wantVal {
			t.Fatalf("热层 values[%q] = %v, want %v（只聚合 name 命中的资源）", col, *v, wantVal)
		}
	}
}

// TestResourceDrillFiltersValuesByExplicitMetrics：下钻的显式 metrics 只回该子集；
// 子表明细列集之外的列 → 400（不得静默回空曲线）。
func TestResourceDrillFiltersValuesByExplicitMetrics(t *testing.T) {
	ctx := context.Background()
	want, _ := repository.ResourceTableColumns("device_metric_disk")

	raw := &stubRawQuerier{bucketPts: []agentproto.MetricsSample{{T: 1_700_000_000_000,
		Disks: []agentproto.DiskMetric{
			{Mountpoint: "/data", UsedPercent: 10, UsedGB: 10, TotalGB: 100, InodesUsedPercent: 8},
		}}}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, logger.NewNop())

	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data", Metrics: "used_percent,used_gb"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.AvailableMetrics, []string{"used_percent", "used_gb"}) {
		t.Fatalf("available_metrics = %v, want 请求的子集", resp.AvailableMetrics)
	}
	if len(resp.Buckets) != 1 || len(resp.Buckets[0].Values) != 2 {
		t.Fatalf("values 必须过滤到请求的列: %+v", resp.Buckets)
	}
	if _, ok := resp.Buckets[0].Values["inodes_used_percent"]; ok {
		t.Fatal("未请求的列不得出现在 values 里")
	}

	// "*" → 子表全量列（与趋势路径的语义一致）
	respAll, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data", Metrics: request.MetricsAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(respAll.AvailableMetrics) != len(want) {
		t.Fatalf("下钻的 metrics=* 必须回子表全量列: %v", respAll.AvailableMetrics)
	}

	// 子表列集之外的列 → 400，且必须在读热层之前拒绝
	before := raw.bucketCalls
	assertBadRequest(t, "下钻请求宽表列", mustErr(svc.ResourceMetrics(ctx, 1001,
		&request.DeviceMetricsQuery{Range: 3600, Kind: "disk", Name: "/data", Metrics: "cpu_used_percent"})))
	if raw.bucketCalls != before {
		t.Fatal("非法列必须在读热层之前拒绝")
	}
}

// TestResourceDrillDBPathUnknownResourceReturnsEmpty：>24h 的 DB 档保留
// 「资源解析不到 → 空结果而非 404」的语义（≤24h 热层档没有这一步，见 D7）。
func TestResourceDrillDBPathUnknownResourceReturnsEmpty(t *testing.T) {
	resolver := &stubResourceResolver{err: errors.New("record not found")}
	reader := &stubMetricReader{err: errors.New("未命中资源时不得查子表")}
	svc := NewAgentMetricsQueryService(nil, reader, resolver, logger.NewNop())
	resp, err := svc.ResourceMetrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 7 * 24 * 3600, Kind: "disk", Name: "/gone"})
	if err != nil {
		t.Fatalf("资源不存在必须返回空结果而不是错误: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("未命中资源时不得查子表（calls=%d）", reader.calls)
	}
	if resp.Buckets == nil || len(resp.Buckets) != 0 {
		t.Fatalf("buckets 必须是空切片而不是 nil/有值: %#v", resp.Buckets)
	}
	if resp.Source != SourceDB {
		t.Fatalf("7d 下钻 source = %q, want db", resp.Source)
	}
	want, _ := repository.ResourceTableColumns("device_metric_disk")
	if len(resp.AvailableMetrics) != len(want) {
		t.Fatalf("available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, want)
	}
}

// TestResourceDrillColumnsMatchHotLayerAggregation：漂移守卫（D2/D7）。
//
// 同一次下钻在 1h（热层聚合）与 7d（子表读取）必须给出**同一套键**：否则前端
// 在切档的瞬间会丢列（available_metrics 与实际 keys 不一致）。三层必须同源：
// service 的 kind→表、repository 的子表列集、agentmetrics 的聚合产出。
func TestResourceDrillColumnsMatchHotLayerAggregation(t *testing.T) {
	const ts = int64(1_700_000_000_000)
	names := map[string]string{"disk": "/", "disk_io": "sda", "nic": "eth0", "sensor": "coretemp"}
	samples := map[string]agentproto.MetricsSample{
		"disk": {T: ts, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/", UsedPercent: 1, UsedGB: 1, TotalGB: 1, InodesUsedPercent: 1}}},
		"disk_io": {T: ts, DiskIO: []agentproto.DiskIOMetric{
			{Name: "sda", ReadBytesPerSec: 1, WriteBytesPerSec: 1, ReadOpsPerSec: 1, WriteOpsPerSec: 1, IOTimePercent: 1}}},
		"nic": {T: ts, NICs: []agentproto.NICMetric{
			{Name: "eth0", RXBytesPerSec: 1, TXBytesPerSec: 1, RXPacketsPerSec: 1, TXPacketsPerSec: 1,
				RXErrorsPerSec: 1, TXErrorsPerSec: 1, RXDroppedPerSec: 1}}},
		"sensor": {T: ts, Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: f64p(1)}}},
	}

	for kind, s := range samples {
		table, ok := resourceTable(kind)
		if !ok {
			t.Fatalf("kind %q 未映射到子表", kind)
		}
		want, ok := repository.ResourceTableColumns(table)
		if !ok {
			t.Fatalf("%s 未登记下钻列集", table)
		}
		got := agentmetrics.AggregateResource(kind, names[kind], []agentproto.MetricsSample{s}, 300)
		if len(got) != 1 {
			t.Fatalf("%s: 聚合桶数 = %d, want 1", kind, len(got))
		}
		keys := make([]string, 0, len(got[0].Values))
		for k, v := range got[0].Values {
			if v == nil {
				t.Fatalf("%s: 键 %q 的值为 nil（缺 ≠ 0 应当不出现）", kind, k)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		wantSorted := append([]string(nil), want...)
		sort.Strings(wantSorted)
		if !slices.Equal(keys, wantSorted) {
			t.Fatalf("%s: 热层聚合键集 = %v, 子表登记列集 = %v", kind, keys, wantSorted)
		}
		if !slices.Contains(agentmetrics.ResourceKinds(), kind) {
			t.Fatalf("热层不认识 kind %q（三层枚举必须同源）", kind)
		}
	}
	if len(agentmetrics.ResourceKinds()) != 4 {
		t.Fatalf("资源种类数 = %d, want 4", len(agentmetrics.ResourceKinds()))
	}
	if got := agentmetrics.AggregateResource("gpu", "0", nil, 300); len(got) != 0 {
		t.Fatalf("未知 kind 必须返回空而不是 panic, got %+v", got)
	}
}

// TestToFloat64PtrNormalisesDriverShapes：下钻的列值只有**一个**归一入口。
//
// 子表列名没有对应的 Go 结构体（D2），逐字段类型断言是脆弱的主要来源 ——
// 驱动对同一张表的不同列可能给出 int64 / float64 / []byte / string / nil。
// 归一必须全部覆盖，且无法解析时返回 nil（**绝不能**当成 0）。
func TestToFloat64PtrNormalisesDriverShapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want *float64
	}{
		{"nil（NULL）", nil, nil},
		{"float64", 62.5, f64p(62.5)},
		{"int64", int64(62), f64p(62)},
		{"int", 62, f64p(62)},
		{"int32", int32(62), f64p(62)},
		{"uint64", uint64(62), f64p(62)},
		{"float32", float32(62.5), f64p(62.5)},
		{"[]byte（驱动文本形态）", []byte("62.5"), f64p(62.5)},
		{"string", "62.5", f64p(62.5)},
		{"*float64", f64p(62.5), f64p(62.5)},
		{"nil *float64", (*float64)(nil), nil},
		{"空 []byte（MySQL 空串形态）", []byte(""), nil},
		{"不可解析的串", []byte("abc"), nil},
		{"NaN", math.NaN(), nil},
		{"+Inf", math.Inf(1), nil},
		{"不支持的类型", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toFloat64Ptr(c.in)
			if (got == nil) != (c.want == nil) {
				t.Fatalf("toFloat64Ptr(%#v) = %v, want %v", c.in, got, c.want)
			}
			if got != nil && *got != *c.want {
				t.Fatalf("toFloat64Ptr(%#v) = %v, want %v", c.in, *got, *c.want)
			}
		})
	}
}

// ---------- 测试替身 ----------

type stubRawQuerier struct {
	snap *agentmetrics.RawSnapshot
	err  error

	// 下钻热层（RawStore.Bucket）的夹具与调用记录（D7）
	bucketPts []agentproto.MetricsSample
	bucketErr error

	calls       int
	gotDevice   uint64
	gotWindow   time.Duration
	gotStep     time.Duration
	bucketCalls int
	gotFromMs   int64
	gotToMs     int64
}

func (s *stubRawQuerier) Query(_ context.Context, deviceID uint64, window, step time.Duration) (*agentmetrics.RawSnapshot, error) {
	s.calls++
	s.gotDevice, s.gotWindow, s.gotStep = deviceID, window, step
	if s.err != nil {
		return nil, s.err
	}
	return s.snap, nil
}

// Bucket 实现 AgentRawBucketReader：记录调用次数，供「热层被调用 / DB 未被调用」断言。
func (s *stubRawQuerier) Bucket(_ context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error) {
	s.bucketCalls++
	s.gotDevice, s.gotFromMs, s.gotToMs = deviceID, fromMs, toMs
	if s.bucketErr != nil {
		return nil, s.bucketErr
	}
	return s.bucketPts, nil
}

type stubMetricReader struct {
	trend []agentmetrics.TrendPoint
	// resourceRows 是下钻的开放形状夹具（SELECT * + t 别名）。
	resourceRows []map[string]any
	err          error

	calls         int
	gotTable      string
	gotDevice     uint64
	gotResourceID uint64
	gotFrom       int64
	gotTo         int64
	gotCols       []string
}

func (s *stubMetricReader) ReadTrendPoints(_ context.Context, table string, deviceID uint64,
	from, to int64, columns []string) ([]agentmetrics.TrendPoint, error) {
	s.calls++
	s.gotTable, s.gotDevice, s.gotFrom, s.gotTo, s.gotCols = table, deviceID, from, to, columns
	if s.err != nil {
		return nil, s.err
	}
	return s.trend, nil
}

func (s *stubMetricReader) ReadResourceRows(_ context.Context, table string, resourceID uint64,
	from, to int64) ([]map[string]any, error) {
	s.calls++
	s.gotTable, s.gotResourceID, s.gotFrom, s.gotTo = table, resourceID, from, to
	if s.err != nil {
		return nil, s.err
	}
	return s.resourceRows, nil
}

type stubResourceResolver struct {
	id  uint64
	err error

	calls     int
	gotDevice uint64
	gotKind   string
	gotName   string
}

func (s *stubResourceResolver) ResolveID(_ context.Context, deviceID uint64, kind, name string) (uint64, error) {
	s.calls++
	s.gotDevice, s.gotKind, s.gotName = deviceID, kind, name
	if s.err != nil {
		return 0, s.err
	}
	return s.id, nil
}
