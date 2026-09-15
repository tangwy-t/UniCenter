package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

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
	reader := &stubMetricReader{resTrend: []agentmetrics.TrendPoint{{T: 600}}}
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
	want := map[string]string{
		"disk": "device_metric_disk", "disk_io": "device_metric_diskio",
		"nic": "device_metric_nic", "sensor": "device_metric_sensor",
	}
	for kind, table := range want {
		reader := &stubMetricReader{}
		svc := NewAgentMetricsQueryService(nil, reader, &stubResourceResolver{id: 7}, logger.NewNop())
		if _, err := svc.ResourceMetrics(context.Background(), 1001,
			&request.DeviceMetricsQuery{Range: 6 * 3600, Kind: kind, Name: "x"}); err != nil {
			t.Fatalf("kind=%s 下钻报错: %v", kind, err)
		}
		if reader.gotTable != table {
			t.Fatalf("kind=%s → 表 %q, want %q", kind, reader.gotTable, table)
		}
	}
}

func TestResourceMetricsUnknownResourceReturnsEmptyNot404(t *testing.T) {
	// 设备可能刚卸载该资源：ResolveID 未命中必须返回**空结果**，不得 404、不得报错。
	resolver := &stubResourceResolver{err: errors.New("record not found")}
	reader := &stubMetricReader{err: errors.New("未命中资源时不得查子表")}
	svc := NewAgentMetricsQueryService(nil, reader, resolver, logger.NewNop())
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

// ---------- 测试替身 ----------

type stubRawQuerier struct {
	snap *agentmetrics.RawSnapshot
	err  error

	calls     int
	gotDevice uint64
	gotWindow time.Duration
	gotStep   time.Duration
}

func (s *stubRawQuerier) Query(_ context.Context, deviceID uint64, window, step time.Duration) (*agentmetrics.RawSnapshot, error) {
	s.calls++
	s.gotDevice, s.gotWindow, s.gotStep = deviceID, window, step
	if s.err != nil {
		return nil, s.err
	}
	return s.snap, nil
}

type stubMetricReader struct {
	trend    []agentmetrics.TrendPoint
	resTrend []agentmetrics.TrendPoint
	err      error

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

func (s *stubMetricReader) ReadResourceTrendPoints(_ context.Context, table string, resourceID uint64,
	from, to int64, columns []string) ([]agentmetrics.TrendPoint, error) {
	s.calls++
	s.gotTable, s.gotResourceID, s.gotFrom, s.gotTo, s.gotCols = table, resourceID, from, to, columns
	if s.err != nil {
		return nil, s.err
	}
	return s.resTrend, nil
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
