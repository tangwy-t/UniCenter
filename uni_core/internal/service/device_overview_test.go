package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// 本文件的测试全部围绕**总览页最容易错、且错了最难发现**的几条口径：
//
//   - 缺值必须是 nil（图上空洞），绝不能变成 0（图上「真的空闲」）；
//   - 列式转置后每条 series 与共享 axis 严格对齐（错位 = 时间轴说谎）；
//   - 单台设备取数失败不影响其余设备（总览的价值在「大多数设备是好的」）；
//   - 0 台设备是合法空态而非错误；
//   - 列白名单复用详情页的同一套解析（避免两页口径漂移）。

// overviewStubDeviceRepo 是可控的设备仓储桩。
type overviewStubDeviceRepo struct {
	devices   []entity.Device
	truncated bool
	err       error

	gotLimit int
	gotIDs   []uint64
}

func (r *overviewStubDeviceRepo) FindForOverview(_ context.Context, _ *request.DeviceOverviewQuery,
	ids []uint64, _ time.Time, limit int) ([]entity.Device, bool, error) {
	r.gotLimit = limit
	r.gotIDs = ids
	if r.err != nil {
		return nil, false, r.err
	}
	return r.devices, r.truncated, nil
}

// overviewStubTrend 是可控的趋势桩：按设备 ID 决定返回或报错。
type overviewStubTrend struct {
	// rows 按设备 ID 给出行；设备不在 map 里 → 返回空（该设备无数据）。
	rows map[uint64][]agentmetrics.TrendPoint
	// failIDs 里的设备读数报错（模拟单台故障）。
	failIDs map[uint64]bool
	calls   int
}

func (s *overviewStubTrend) ReadTrendPoints(_ context.Context, _ string, deviceID uint64,
	_, _ int64, _ []string) ([]agentmetrics.TrendPoint, error) {
	s.calls++
	if s.failIDs[deviceID] {
		return nil, fmt.Errorf("boom-%d", deviceID)
	}
	return s.rows[deviceID], nil
}

// overviewStubLatest 返回预置水位。
type overviewStubLatest struct {
	marks map[uint64]*agentmetrics.LatestSummary
	err   error
}

func (s *overviewStubLatest) GetMany(context.Context, []uint64) (map[uint64]*agentmetrics.LatestSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.marks, nil
}

// overviewStubCfg 提供离线阈值。
type overviewStubCfg struct{ threshold int }

func (c overviewStubCfg) GetInt(context.Context, string, int) int { return c.threshold }

// newOverviewSvc 组装一个全桩的总览服务（冷层档：range > 24h 才会走 trend）。
func newOverviewSvc(t *testing.T, repo DeviceOverviewRepository, trend DeviceOverviewTrendReader,
	latest DeviceOverviewLatestReader, cfgThreshold int) *DeviceOverviewService {
	t.Helper()
	return NewDeviceOverviewService(repo, latest, trend, nil,
		nil, overviewStubCfg{threshold: cfgThreshold}, logger.NewNop())
}

func f64(v float64) *float64 { return &v }

// nowUnix / i64ToTime 是本文件局部的时间辅助（不与其它测试文件共享，
// 避免在 service 包内引入全局同名符号而与其他 _test.go 冲突）。
func nowUnix() int64 { return time.Now().Unix() }

func i64ToTime(v int64) *time.Time {
	tm := time.Unix(v, 0)
	return &tm
}

// TestOverviewMissingValueStaysNil 是总览**最硬**的一条口径：
// 未采集的桶必须是 nil（JSON null → 前端画空洞并显示「—」），
// 绝不能变成 0 —— 0 是合法观测值（CPU 真为 0%），两者在图上必须可区分。
func TestOverviewMissingValueStaysNil(t *testing.T) {
	const devID = uint64(1001)
	base := int64(1_700_000_000)
	// 桶 0：有 cpu；桶 1：cpu 缺失（其余列也缺失）；桶 2：cpu = 0（**真实观测到的 0**）
	rows := []agentmetrics.TrendPoint{
		{T: base, CPUUsedPercent: f64(12.5), MemUsedPercent: f64(30)},
		{T: base + 300},
		{T: base + 600, CPUUsedPercent: f64(0), MemUsedPercent: f64(0)},
	}
	repo := &overviewStubDeviceRepo{devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1, LastSeenAt: i64ToTime(nowUnix())}}}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{rows: map[uint64][]agentmetrics.TrendPoint{devID: rows}}, &overviewStubLatest{}, 30)

	// range=24h 走 Redis 档，桩里 raw=nil 会失败；故用 48h 走冷层（DB 档）。
	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent,mem_used_percent",
	})
	if err != nil {
		t.Fatalf("Overview 失败: %v", err)
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("设备数 = %d, want 1", len(resp.Devices))
	}
	if len(resp.Axis) != 3 {
		t.Fatalf("axis 长度 = %d, want 3（三台桶）", len(resp.Axis))
	}
	s := seriesOf(t, resp, 0, "cpu_used_percent")
	if len(s.Values) != 3 {
		t.Fatalf("values 长度 = %d, want 3（必须与 axis 等长）", len(s.Values))
	}
	if s.Values[0] == nil || *s.Values[0] != 12.5 {
		t.Fatalf("桶0 应为 12.5，实得 %v", s.Values[0])
	}
	// 关键断言：缺失桶是 nil
	if s.Values[1] != nil {
		t.Fatalf("桶1 无数据，应为 nil，实得 %v（若为 0 则前端会把「没数据」画成「真的空闲」）", *s.Values[1])
	}
	// 关键断言：真实的 0 必须保留为 0，不能被当成缺失
	if s.Values[2] == nil || *s.Values[2] != 0 {
		t.Fatalf("桶2 的 cpu 真值 0 必须保留为 0，实得 %v", s.Values[2])
	}
	// 统计量只按非空值算：min=0（桶2）、max=12.5，avg=(12.5+0)/2=6.25，last=0
	if s.Min == nil || *s.Min != 0 {
		t.Fatalf("min 应为 0，实得 %v", s.Min)
	}
	if s.Max == nil || *s.Max != 12.5 {
		t.Fatalf("max 应为 12.5，实得 %v", s.Max)
	}
	if s.Avg == nil || *s.Avg != 6.25 {
		t.Fatalf("avg 应为 6.25，实得 %v", s.Avg)
	}
	// Present/Missing 是覆盖率，必须能直接读出「3 桶中 1 桶无数据」
	if s.Present != 2 || s.Missing != 1 {
		t.Fatalf("present/missing = %d/%d, want 2/1", s.Present, s.Missing)
	}
}

// TestOverviewWholeColumnMissingYieldsNoStats 一列**整列为空**时，
// last/min/max/avg 必须全部缺席（omitempty），让前端显示「无数据」
// 而不是显示 0 —— 这与「有数据且值为 0」是两种截然不同的结论。
func TestOverviewWholeColumnMissingYieldsNoStats(t *testing.T) {
	const devID = uint64(1002)
	base := int64(1_700_000_000)
	rows := []agentmetrics.TrendPoint{
		{T: base, CPUUsedPercent: f64(5)},
		{T: base + 300, CPUUsedPercent: f64(6)},
	}
	repo := &overviewStubDeviceRepo{devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1}}}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{rows: map[uint64][]agentmetrics.TrendPoint{devID: rows}}, &overviewStubLatest{}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent,tcp_total",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := seriesOf(t, resp, 0, "tcp_total")
	if s.Present != 0 || s.Missing != 2 {
		t.Fatalf("tcp_total 应整列为空 (0/2)，实得 %d/%d", s.Present, s.Missing)
	}
	if s.Last != nil || s.Min != nil || s.Max != nil || s.Avg != nil {
		t.Fatalf("整列为空时统计量必须全部缺席，实得 last=%v min=%v max=%v avg=%v",
			s.Last, s.Min, s.Max, s.Avg)
	}
}

// TestOverviewPerDeviceErrorIsolated 单台设备取数失败时：
//   - 该设备仍出现在列表里（用户要看到「它有问题」），并带 error；
//   - **其余设备正常返回数据**（不能因为一台坏设备把整页打空）。
func TestOverviewPerDeviceErrorIsolated(t *testing.T) {
	const okID, badID = uint64(2001), uint64(2002)
	base := int64(1_700_000_000)
	rows := []agentmetrics.TrendPoint{{T: base, CPUUsedPercent: f64(42)}}
	repo := &overviewStubDeviceRepo{devices: []entity.Device{
		{BaseEntity: entity.BaseEntity{ID: okID}, Hostname: "good", Status: 1},
		{BaseEntity: entity.BaseEntity{ID: badID}, Hostname: "bad", Status: 1},
	}}
	trend := &overviewStubTrend{
		rows:    map[uint64][]agentmetrics.TrendPoint{okID: rows},
		failIDs: map[uint64]bool{badID: true},
	}
	svc := newOverviewSvc(t, repo, trend, &overviewStubLatest{}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent",
	})
	if err != nil {
		t.Fatalf("单台失败不应让整个请求失败: %v", err)
	}
	if len(resp.Devices) != 2 {
		t.Fatalf("设备数 = %d, want 2（坏设备也必须出现）", len(resp.Devices))
	}
	byID := map[string]int{}
	for i, d := range resp.Devices {
		byID[d.ID] = i
	}
	okIdx, badIdx := byID[fmt.Sprint(okID)], byID[fmt.Sprint(badID)]
	if resp.Devices[badIdx].Error == "" {
		t.Fatal("失败设备必须带非空 error，让 UI 能就地提示而不是静默空白")
	}
	if resp.Devices[badIdx].Series != nil {
		t.Fatal("失败设备不应带 series（避免前端画出空线掩盖故障）")
	}
	if resp.Devices[okIdx].Error != "" {
		t.Fatalf("成功设备不应带 error，实得 %q", resp.Devices[okIdx].Error)
	}
	s := seriesOf(t, resp, okIdx, "cpu_used_percent")
	if s.Values[0] == nil || *s.Values[0] != 42 {
		t.Fatalf("成功设备的数据必须完好，实得 %v", s.Values[0])
	}
}

// TestOverviewEmptyDeviceListIsNotError 0 台设备必须返回**合法空态**
// （不是 404、不是 500），否则前端只能显示错误页而不是「暂无设备」。
func TestOverviewEmptyDeviceListIsNotError(t *testing.T) {
	repo := &overviewStubDeviceRepo{devices: nil}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatalf("空设备列表应是合法空态，实得错误: %v", err)
	}
	if resp.Summary.Total != 0 || resp.Summary.Online != 0 || resp.Summary.Offline != 0 {
		t.Fatalf("空态计数必须全 0，实得 %+v", resp.Summary)
	}
	if len(resp.Devices) != 0 || len(resp.Axis) != 0 {
		t.Fatalf("空态不应有设备或时间轴，实得 %d/%d", len(resp.Devices), len(resp.Axis))
	}
	// 阈值仍须回显：前端要靠它解释「为什么这台算离线」，空态也不例外。
	if resp.Summary.OfflineThresholdSec != 30 {
		t.Fatalf("空态也必须回显离线阈值，实得 %d", resp.Summary.OfflineThresholdSec)
	}
}

// TestOverviewEmptyCollectionsAreArrays 空结果时集合字段必须是**空数组**
// （JSON `[]`），不能是 Go 的 nil slice（序列化成 `null`）。
//
// 为什么必须守：生成的 TS 类型把它们声明成数组（`axis: number[]`、
// `devices: DeviceOverviewItem[]`）。后端一旦发 `null`，前端 `resp.axis.length`
// 在空结果上直接 TypeError —— 而空结果（筛选无命中、新装系统）恰恰是最需要
// 稳妥渲染的状态：用户点一次「查询」把筛选条件写严了，页面就整页崩白。
//
// 实测发现过程：对真实环境发 `?hostname=zzz-nonexistent` 时返回了
// `"axis": null, "devices": null`（见本次修复）。
func TestOverviewEmptyCollectionsAreArrays(t *testing.T) {
	svc := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Hostname: "nonexistent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Axis == nil {
		t.Fatal("axis 为 nil → 会序列化成 JSON null，前端 resp.axis.length 会抛 TypeError")
	}
	if resp.Devices == nil {
		t.Fatal("devices 为 nil → 会序列化成 JSON null，前端 resp.devices.map 会抛 TypeError")
	}
	if len(resp.Axis) != 0 || len(resp.Devices) != 0 {
		t.Fatalf("空结果应无元素，实得 axis=%d devices=%d", len(resp.Axis), len(resp.Devices))
	}

	// 序列化后再断言一次：Go 的 nil 检查不足以覆盖 json 编码行为，
	// 直接比对编码结果才是对前端**实际收到什么**的断言。
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"axis", "devices", "available_metrics"} {
		if decoded[k] == nil {
			t.Fatalf("字段 %q 编码为 null，want []（前端类型声明为数组）", k)
		}
		if _, ok := decoded[k].([]any); !ok {
			t.Fatalf("字段 %q = %#v，want 数组", k, decoded[k])
		}
	}
}

// TestOverviewDeviceIDHasNoExtraQuotes 设备 ID 必须是**裸字符串**，不能带
// 字面引号。
//
// 这个缺陷的成因值得记下来：DTO 里把 ID 声明成 Go `string` 的同时保留了
// `json:"id,string"` 标签。`,string` 选项对数值类型字段是「编码成 JSON 字符串」
// （既有 DeviceListItem 用 uint64 + `,string` 正是为了避开 JS 大整数精度问题），
// 但对**已经是 string 的字段**，它会再加一层引号 —— 于是 `2100772…`
// 变成了 `"\"2100772…\""`。
//
// 症状与危害：
//   - 页面上设备 ID 显示为 `"2100772873982447616"`（用户看得见的字面引号）；
//   - 前端把该值回传给 `ids=` 参数时，后端 parseOverviewIDs 直接 400
//     —— 「勾选设备对比」整个功能不可用。
//
// 这类「编码层面多一层转义」的缺陷不会报错、不会 panic，只能靠断言实际
// JSON 字节来拦截，故这里直接 Marshal 后比对。
func TestOverviewDeviceIDHasNoExtraQuotes(t *testing.T) {
	const devID = uint64(2100772873982447616)
	repo := &overviewStubDeviceRepo{devices: []entity.Device{
		{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1},
	}}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("设备数 = %d, want 1", len(resp.Devices))
	}
	want := strconv.FormatUint(devID, 10)
	if resp.Devices[0].ID != want {
		t.Fatalf("服务层返回的 ID = %q, want %q", resp.Devices[0].ID, want)
	}

	// 关键断言：看**编码后的 JSON 字节**。Go 层的 string 相等看不出多余引号，
	// 因为问题出在 json 标签的 `,string` 选项上（它对 string 字段二次编码）。
	//
	// 判定方式刻意不用字符串转义比较（反斜杠与引号的组合极易写错，
	// 实测第一次就写错成「匹配裸引号」而误报失败）。改为两条不依赖转义的判据：
	//   ① 期望形态：JSON 里出现 "id":"<裸ID>"
	//   ② 解码形态：把 id 解回来必须**逐字等于**裸 ID（双重编码会解出带引号的值）
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	wantJSON := "\"id\":\"" + want + "\""
	if !strings.Contains(raw, wantJSON) {
		t.Fatalf("JSON 里应有 %s（裸字符串），实得:\n%s", wantJSON, excerptAround(raw, "id"))
	}

	var decoded struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Devices) != 1 {
		t.Fatalf("解码后设备数 = %d, want 1", len(decoded.Devices))
	}
	if decoded.Devices[0].ID != want {
		t.Fatalf("解码出的 id = %q, want %q —— 不等说明 JSON 里被加了多余引号"+
			"（json 标签的 `,string` 对已经是 string 的字段会二次编码）",
			decoded.Devices[0].ID, want)
	}
}

// excerptAround 截取子串附近的片段，便于失败时定位（避免打印整段响应）。
func excerptAround(s, needle string) string {
	i := strings.Index(s, needle)
	if i < 0 {
		return s
	}
	end := i + 160
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

// TestOverviewSummaryCounts 页面级概览计数的口径：
// online/offline 按最后上报时间，stale 按**水位采样时刻**，disabled 按停用态。
// stale 与 offline 刻意是两条独立的判据（心跳正常但指标停更 = 最危险的一种故障）。
func TestOverviewSummaryCounts(t *testing.T) {
	now := nowUnix()
	fresh := now - 5
	old := now - 3600
	repo := &overviewStubDeviceRepo{devices: []entity.Device{
		// 在线 + 水位新鲜
		{BaseEntity: entity.BaseEntity{ID: 1}, Hostname: "a", Status: 1, LastSeenAt: i64ToTime(fresh)},
		// 在线但**水位陈旧**（心跳还在、指标停更）→ online 且 stale
		{BaseEntity: entity.BaseEntity{ID: 2}, Hostname: "b", Status: 1, LastSeenAt: i64ToTime(fresh)},
		// 离线（从未上报）
		{BaseEntity: entity.BaseEntity{ID: 3}, Hostname: "c", Status: 1, LastSeenAt: nil},
		// 停用 + 离线
		{BaseEntity: entity.BaseEntity{ID: 4}, Hostname: "d", Status: 0, LastSeenAt: i64ToTime(old)},
	}}
	marks := map[uint64]*agentmetrics.LatestSummary{
		1: {T: fresh * 1000, CPUUsedPercent: f64(10)},
		2: {T: old * 1000, CPUUsedPercent: f64(20)}, // 一秒都不新
		4: {T: old * 1000, CPUUsedPercent: f64(30)},
	}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{marks: marks}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Summary
	if got.Total != 4 {
		t.Fatalf("total = %d, want 4", got.Total)
	}
	if got.Online != 2 || got.Offline != 2 {
		t.Fatalf("online/offline = %d/%d, want 2/2（按 last_seen_at）", got.Online, got.Offline)
	}
	if got.Disabled != 1 {
		t.Fatalf("disabled = %d, want 1（status=0）", got.Disabled)
	}
	// 设备 2（在线但水位旧）与设备 4（水位旧）→ stale=2。
	if got.Stale != 2 {
		t.Fatalf("stale = %d, want 2（stale 看水位采样时刻，与 online 无关）", got.Stale)
	}
	if got.OfflineThresholdSec != 30 {
		t.Fatalf("threshold = %d, want 30", got.OfflineThresholdSec)
	}
}

// TestOverviewRejectsUnknownColumn 未知列必须 **400**（复用详情页同一套白名单），
// 而不是静默忽略 —— 静默会画出恒空的线，让用户以为「这台机器没这个指标」。
func TestOverviewRejectsUnknownColumn(t *testing.T) {
	svc := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	_, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent,not_a_real_column",
	})
	if err == nil {
		t.Fatal("未知列必须报错")
	}
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeBadRequest {
		t.Fatalf("未知列应映射为 400(badRequest)，实得 %v", err)
	}
}

// TestOverviewRejectsOutOfRange 越界 range 必须 400（与详情页同一套区间）。
func TestOverviewRejectsOutOfRange(t *testing.T) {
	svc := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	for _, r := range []int64{1, 60, request.DeviceRangeMax + 1} {
		if _, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: r}); err == nil {
			t.Fatalf("range=%d 应被拒绝", r)
		}
	}
}

// TestOverviewRejectsBadIDs 非法 ID 必须 400 而非静默丢弃：
// 「筛了 5 台只剩 3 台」的静默差异会让用户照着错误的线得出错误结论。
func TestOverviewRejectsBadIDs(t *testing.T) {
	svc := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	for _, raw := range []string{"abc", "1,,x", "0", "-3"} {
		if _, err := svc.Overview(context.Background(),
			&request.DeviceOverviewQuery{Range: 48 * 3600, Ids: raw}); err == nil {
			t.Fatalf("ids=%q 应被拒绝", raw)
		}
	}
}

// TestOverviewIdsParsedAndDeduped 合法 IDs 应去重并排序后传给仓储，
// 且空字符串表示「不限制」（传 nil 而不是空切片里的零值）。
func TestOverviewIdsParsedAndDeduped(t *testing.T) {
	repo := &overviewStubDeviceRepo{}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{}, 30)

	if _, err := svc.Overview(context.Background(),
		&request.DeviceOverviewQuery{Range: 48 * 3600, Ids: " 30,10,30,20 "}); err != nil {
		t.Fatal(err)
	}
	want := []uint64{10, 20, 30}
	if len(repo.gotIDs) != len(want) {
		t.Fatalf("ids = %v, want %v", repo.gotIDs, want)
	}
	for i := range want {
		if repo.gotIDs[i] != want[i] {
			t.Fatalf("ids = %v, want %v（须去重并升序）", repo.gotIDs, want)
		}
	}

	repo2 := &overviewStubDeviceRepo{}
	svc2 := newOverviewSvc(t, repo2, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	if _, err := svc2.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600}); err != nil {
		t.Fatal(err)
	}
	if len(repo2.gotIDs) != 0 {
		t.Fatalf("未指定 ids 时不应下发白名单，实得 %v", repo2.gotIDs)
	}
}

// TestOverviewWatermarkDegradesGracefully 水位整体读取失败时：
// 设备列表与趋势仍须返回（降级为「无快照」），而不是整页 500 ——
// 否则一次 Redis 抖动会让用户连「有哪几台设备」都看不到。
func TestOverviewWatermarkDegradesGracefully(t *testing.T) {
	const devID = uint64(3001)
	base := int64(1_700_000_000)
	repo := &overviewStubDeviceRepo{devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1}}}
	trend := &overviewStubTrend{rows: map[uint64][]agentmetrics.TrendPoint{
		devID: {{T: base, CPUUsedPercent: f64(7)}},
	}}
	latest := &overviewStubLatest{err: fmt.Errorf("redis down")}
	svc := newOverviewSvc(t, repo, trend, latest, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent",
	})
	if err != nil {
		t.Fatalf("水位失败应降级而非整页失败: %v", err)
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("设备列表仍须返回，实得 %d 台", len(resp.Devices))
	}
	if resp.Devices[0].Watermark != nil {
		t.Fatalf("水位不可用时 watermark 应为空，实得 %v", resp.Devices[0].Watermark)
	}
	if resp.Devices[0].WatermarkAt != nil {
		t.Fatal("水位不可用时 watermarkAt 应缺席")
	}
	if len(seriesOf(t, resp, 0, "cpu_used_percent").Values) == 0 {
		t.Fatal("趋势不应因水位失败而丢失")
	}
}

// TestOverviewWatermarkExposesAllFields 水位全字段都要透出（不只是列表页的 3 个）：
// 总览页要靠 load1/mem_used_mb/disk_total_gb/nic_rx_bytes_sec 等画出更多图表，
// 这正是「新增批量端点」而非复用列表接口的核心理由。
func TestOverviewWatermarkExposesAllFields(t *testing.T) {
	const devID = uint64(3002)
	marks := map[uint64]*agentmetrics.LatestSummary{
		devID: {
			T: nowUnix() * 1000, CPUUsedPercent: f64(11), Load1: f64(0.5),
			MemUsedPercent: f64(40), MemUsedMB: f64(6400),
			DiskTotalGB: f64(100), DiskUsedGB: f64(25), DiskUsedPercent: f64(25),
			NICRXBytesSec: f64(1024), NICTXBytesSec: f64(2048), MaxTemperatureC: f64(55),
		},
	}
	repo := &overviewStubDeviceRepo{devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1}}}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{marks: marks}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	wm := resp.Devices[0].Watermark
	for _, col := range []string{"cpu_used_percent", "load1", "mem_used_percent", "mem_used_mb",
		"disk_total_gb", "disk_used_gb", "disk_used_percent",
		"nic_rx_bytes_sec", "nic_tx_bytes_sec", "max_temperature_c"} {
		if _, ok := wm[col]; !ok {
			t.Fatalf("水位缺列 %q（总览的多个图表依赖它），实得键集 %v", col, wm)
		}
	}
}

// TestOverviewAxisSharedAcrossDevices 所有设备共享同一条时间轴，
// 且每台设备的 series 长度与 axis 严格一致 —— 错位会让时间轴说谎。
func TestOverviewAxisSharedAcrossDevices(t *testing.T) {
	base := int64(1_700_000_000)
	repo := &overviewStubDeviceRepo{devices: []entity.Device{
		{BaseEntity: entity.BaseEntity{ID: 1}, Hostname: "a", Status: 1}, {BaseEntity: entity.BaseEntity{ID: 2}, Hostname: "b", Status: 1},
	}}
	trend := &overviewStubTrend{rows: map[uint64][]agentmetrics.TrendPoint{
		// 设备 a 三个桶，设备 b 只有中间那个桶有值（其余缺失）
		1: {{T: base, CPUUsedPercent: f64(1)}, {T: base + 300, CPUUsedPercent: f64(2)}, {T: base + 600, CPUUsedPercent: f64(3)}},
		2: {{T: base + 300, CPUUsedPercent: f64(99)}},
	}}
	svc := newOverviewSvc(t, repo, trend, &overviewStubLatest{}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Axis) != 3 {
		t.Fatalf("axis = %v, want 3 个桶", resp.Axis)
	}
	for i := range resp.Devices {
		s := seriesOf(t, resp, i, "cpu_used_percent")
		if len(s.Values) != len(resp.Axis) {
			t.Fatalf("设备 %d 的 values 长度 %d != axis 长度 %d",
				i, len(s.Values), len(resp.Axis))
		}
	}
	// 设备 b 的值必须落在**正确的桶**上（下标 1），而两端是 nil。
	sb := seriesOf(t, resp, 1, "cpu_used_percent")
	if sb.Values[0] != nil || sb.Values[2] != nil {
		t.Fatalf("设备 b 只在中间桶有值，两端应为 nil，实得 %v / %v", sb.Values[0], sb.Values[2])
	}
	if sb.Values[1] == nil || *sb.Values[1] != 99 {
		t.Fatalf("设备 b 的值必须落在下标 1，实得 %v", sb.Values[1])
	}
}

// TestOverviewAvailableMetricsExcludesBucketTS available_metrics 必须是**纯值列**：
// bucket_ts 由 axis 承载，混进去会让前端多画一条「时间戳折线」。
func TestOverviewAvailableMetricsExcludesBucketTS(t *testing.T) {
	svc := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 30)
	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 48 * 3600, Metrics: "cpu_used_percent,mem_used_percent",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.AvailableMetrics {
		if c == "bucket_ts" {
			t.Fatalf("available_metrics 不应含键列 bucket_ts，实得 %v", resp.AvailableMetrics)
		}
	}
	if len(resp.AvailableMetrics) != 2 {
		t.Fatalf("available_metrics = %v, want 2 列", resp.AvailableMetrics)
	}
}

// TestOverviewTruncationFlagged 超过上限时截断并置 truncated（不报错）。
func TestOverviewTruncationFlagged(t *testing.T) {
	repo := &overviewStubDeviceRepo{truncated: true, devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: 1}, Hostname: "a", Status: 1}}}
	svc := newOverviewSvc(t, repo, &overviewStubTrend{}, &overviewStubLatest{}, 30)

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated {
		t.Fatal("截断必须置 truncated，让 UI 说明「还有更多设备未展示」")
	}
	if resp.MaxDevices != request.OverviewMaxDevicesDefault {
		t.Fatalf("max_devices = %d, want %d（须随响应下发，供 UI 解释）",
			resp.MaxDevices, request.OverviewMaxDevicesDefault)
	}
}

// TestOverviewThresholdFallbackOnBadConfig 配置阈值 <=0 时必须回落 30，
// 否则阈值 0 会让 last_seen_at >= now 恒不成立，把所有设备判成离线。
func TestOverviewThresholdFallbackOnBadConfig(t *testing.T) {
	resp, err := newOverviewSvc(t, &overviewStubDeviceRepo{}, &overviewStubTrend{}, &overviewStubLatest{}, 0).
		Overview(context.Background(), &request.DeviceOverviewQuery{Range: 48 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary.OfflineThresholdSec != 30 {
		t.Fatalf("阈值 0 应回落 30，实得 %d", resp.Summary.OfflineThresholdSec)
	}
}

// TestOverviewRedisTierUsesRaw 热层档（range ≤ 24h）必须走 Redis 聚合查询，
// 且**不**触碰冷层趋势读取。
func TestOverviewRedisTierUsesRaw(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{Step: 10 * time.Second, MaxPoints: 1000})

	const devID = uint64(4001)
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 5; i++ {
		sample := agentproto.MetricsSample{
			T:              base.Add(time.Duration(i) * 10 * time.Second).UnixMilli(),
			CPUUsedPercent: 25,
		}
		if err := raw.Append(context.Background(), devID, &sample); err != nil {
			t.Fatal(err)
		}
	}
	repo := &overviewStubDeviceRepo{devices: []entity.Device{{BaseEntity: entity.BaseEntity{ID: devID}, Hostname: "h", Status: 1}}}
	trend := &overviewStubTrend{}
	svc := NewDeviceOverviewService(repo, &overviewStubLatest{}, trend, raw, nil,
		overviewStubCfg{threshold: 30}, logger.NewNop())

	resp, err := svc.Overview(context.Background(), &request.DeviceOverviewQuery{
		Range: 3600, Metrics: "cpu_used_percent",
	})
	if err != nil {
		t.Fatalf("热层档查询失败: %v", err)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("range=3600 应走 Redis 档，实得 %q", resp.Source)
	}
	if trend.calls != 0 {
		t.Fatalf("热层档不应触碰冷层趋势读取，实得 %d 次调用", trend.calls)
	}
}

// ── 辅助 ──────────────────────────────────────────────────────────

// seriesOf 取第 idx 台设备里指定列的 series（找不到即 Fatal）。
func seriesOf(t *testing.T, resp *response.DeviceOverviewResp, idx int, metric string) response.DeviceOverviewSeries {
	t.Helper()
	if idx >= len(resp.Devices) {
		t.Fatalf("设备下标 %d 越界（共 %d 台）", idx, len(resp.Devices))
	}
	for _, s := range resp.Devices[idx].Series {
		if s.Metric == metric {
			return s
		}
	}
	t.Fatalf("设备 %d 缺列 %q（实得 %d 条 series）", idx, metric, len(resp.Devices[idx].Series))
	return response.DeviceOverviewSeries{}
}
