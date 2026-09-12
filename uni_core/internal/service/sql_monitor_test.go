package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/sqlhistory"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// newTestSQLStats 构造最小 SQLStats,写入两条记录供快照读取。
func newTestSQLStats() *database.SQLStats {
	stats := database.NewSQLStats(64, 200*time.Millisecond)
	stats.Record("sys_user", "select", time.Millisecond, "SELECT * FROM sys_user WHERE deleted_at IS NULL", false, false)
	stats.Record("sys_menu", "update", 5*time.Millisecond, "UPDATE sys_menu SET parent_id = 0", false, false)
	return stats
}

// TTL 内重复读取应共享同一份缓存快照(指针相同)。
func TestSQLMonitorService_GetStatsTTLHit(t *testing.T) {
	svc := NewSQLMonitorService(newTestSQLStats(), nil, nil, time.Hour)
	a := svc.GetStats()
	b := svc.GetStats()
	if a != b {
		t.Fatal("TTL 内应返回同一份缓存快照")
	}
}

// TTL 过期后应重新计算(指针不同)。
func TestSQLMonitorService_GetStatsTTLExpired(t *testing.T) {
	svc := NewSQLMonitorService(newTestSQLStats(), nil, nil, time.Nanosecond)
	a := svc.GetStats()
	time.Sleep(time.Millisecond)
	b := svc.GetStats()
	if a == b {
		t.Fatal("TTL 过期后应重新计算快照")
	}
}

// 累计模式与窗口模式必须使用相互独立的缓存项。
func TestSQLMonitorService_GetStatsWindowIsolated(t *testing.T) {
	svc := NewSQLMonitorService(newTestSQLStats(), nil, nil, time.Hour)
	a := svc.GetStats()
	b := svc.GetStatsWindow(time.Minute)
	if a == nil || b == nil {
		t.Fatal("快照不应为 nil")
	}
	if a == b {
		t.Fatal("累计与窗口模式禁止共用缓存")
	}
}

// history 缓存(store == nil 的回退路径):同参数 TTL 内命中,
// 不同 window/step 互不串扰。
func TestSQLMonitorService_GetHistoryTTL(t *testing.T) {
	svc := NewSQLMonitorService(newTestSQLStats(), nil, nil, time.Hour)
	h1, err := svc.GetHistory(context.Background(), time.Minute, time.Second)
	if err != nil {
		t.Fatalf("回退路径不应报错: %v", err)
	}
	h2, err := svc.GetHistory(context.Background(), time.Minute, time.Second)
	if err != nil {
		t.Fatalf("回退路径不应报错: %v", err)
	}
	if h1 != h2 {
		t.Fatal("同参数 TTL 内应命中缓存")
	}
	h3, err := svc.GetHistory(context.Background(), 5*time.Minute, time.Second)
	if err != nil {
		t.Fatalf("回退路径不应报错: %v", err)
	}
	if h1 == h3 {
		t.Fatal("不同 window 的缓存项应相互独立")
	}
	h4, err := svc.GetHistory(context.Background(), time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("回退路径不应报错: %v", err)
	}
	if h2 == h4 {
		t.Fatal("不同 step 的缓存项应相互独立")
	}
}

// ---- Redis 双路径与采样器 ----

// testSQLHistoryKey 与 sqlhistory 内部 key 一致(包内常量未导出)。
const testSQLHistoryKey = "monitor:sql:history"

// newTestSvcWithStore 起 miniredis + store,构造带 Redis 的服务。
func newTestSvcWithStore(t *testing.T) (*SQLMonitorService, *goredis.Client, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	svc := NewSQLMonitorService(newTestSQLStats(), sqlhistory.NewHistoryStore(rdb), nil)
	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return svc, rdb, cleanup
}

// sampleOnce 应把 (lastTick, now] 区间的查询压入 Redis,
// GetHistory 走 Redis 路径并回填慢查询阈值。
// newTestSQLStats 的两条 Record 在构造前写入,lastTick 零值下界
// 会把它们全部计入 —— 不依赖时间睡眠的确定性测试。
func TestSQLMonitorService_SampleOnceAppends(t *testing.T) {
	svc, rdb, cleanup := newTestSvcWithStore(t)
	defer cleanup()

	svc.sampleOnce()

	ll, err := rdb.LLen(context.Background(), testSQLHistoryKey).Result()
	if err != nil {
		t.Fatalf("llen: %v", err)
	}
	if ll != 1 {
		t.Fatalf("Redis 列表长度 = %d, want 1", ll)
	}

	snap, err := svc.GetHistory(context.Background(), time.Minute, 3*time.Second)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if snap.SlowThresholdMs != 200 {
		t.Fatalf("slow_threshold_ms = %d, want 200(回填)", snap.SlowThresholdMs)
	}
	var total int64
	for _, b := range snap.Buckets {
		total += b.Count
	}
	if total != 2 {
		t.Fatalf("sum(count) = %d, want 2", total)
	}
	if snap.RecentQPS <= 0 {
		t.Fatalf("recent_qps = %v, want > 0", snap.RecentQPS)
	}
}

// 空区间不写 Redis:没有任何记录时 sampleOnce 不产生列表元素。
func TestSQLMonitorService_SampleOnceSkipsEmpty(t *testing.T) {
	stats := database.NewSQLStats(64, 200*time.Millisecond) // 空 stats
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close(); mr.Close() }()

	svc := NewSQLMonitorService(stats, sqlhistory.NewHistoryStore(rdb), nil)
	svc.sampleOnce()

	ll, err := rdb.LLen(context.Background(), testSQLHistoryKey).Result()
	if err != nil {
		t.Fatalf("llen: %v", err)
	}
	if ll != 0 {
		t.Fatalf("空区间不应写入,列表长度 = %d, want 0", ll)
	}
}

// fakeLogger 统计 Warn 调用次数的测试替身。
type fakeLogger struct{ warns int }

func (f *fakeLogger) Debug(string, ...zap.Field) {}
func (f *fakeLogger) Info(string, ...zap.Field)  {}
func (f *fakeLogger) Warn(string, ...zap.Field)  { f.warns++ }
func (f *fakeLogger) Error(string, ...zap.Field) {}
func (f *fakeLogger) IsDebug() bool              { return false }

// errStore Append 恒失败的存储替身。
type errStore struct{}

func (errStore) Append(context.Context, sqlhistory.Point) error { return errors.New("boom") }
func (errStore) Query(context.Context, time.Duration, time.Duration) (*sqlhistory.Snapshot, error) {
	return &sqlhistory.Snapshot{}, nil
}

// Append 失败告警 30s 节流:两次失败只 Warn 一次。
func TestSQLMonitorService_SampleOnceThrottlesWarn(t *testing.T) {
	stats := database.NewSQLStats(64, 200*time.Millisecond)
	log := &fakeLogger{}
	svc := NewSQLMonitorService(stats, errStore{}, log)

	stats.Record("t", "select", time.Millisecond, "S1", false, false)
	svc.sampleOnce() // 第一次:lastAppendErrAt 零值 → 超过 30s → Warn
	stats.Record("t", "select", time.Millisecond, "S2", false, false)
	svc.sampleOnce() // 第二次:30s 内 → 节流不告警

	if log.warns != 1 {
		t.Fatalf("warns = %d, want 1(30s 内节流)", log.warns)
	}
}

// TestGetHistoryReturnsResponseDTO 两个历史路径(Redis 与 ring buffer)
// 都必须返回 response.SQLHistorySnapshot(HTTP 契约),而非底层存储类型。
func TestGetHistoryReturnsResponseDTO(t *testing.T) {
	// ring buffer 回退路径
	svc := NewSQLMonitorService(newTestSQLStats(), nil, nil)
	snap, err := svc.GetHistory(context.Background(), time.Minute, time.Second)
	if err != nil {
		t.Fatalf("回退路径: %v", err)
	}
	if snap == nil || snap.WindowSeconds != 60 {
		t.Fatalf("回退路径快照 = %+v, want window=60", snap)
	}
	if _, ok := any(snap).(*database.HistorySnapshot); ok {
		t.Fatal("GetHistory 不应再返回底层 database.HistorySnapshot")
	}
	if _, ok := any(snap).(*response.SQLHistorySnapshot); !ok {
		t.Fatal("GetHistory 应返回 response.SQLHistorySnapshot")
	}
}

// TestHistoryAdaptersPreserveJSONShape 适配器必须保形:新契约(HTTP DTO)与
// 旧契约(database.HistorySnapshot 同值对象)的 JSON 序列化逐字节一致,
// 即前端契约零变化。覆盖 sqlhistory→DTO 与 database→DTO 双路径。
func TestHistoryAdaptersPreserveJSONShape(t *testing.T) {
	ts := util.JSONTime(time.Unix(1800000, 0))
	dbSnap := &database.HistorySnapshot{
		WindowSeconds:   60,
		StepSeconds:     3,
		SlowThresholdMs: 200,
		RecentQPS:       1.23,
		Buckets: []database.HistoryPoint{{
			Timestamp:  ts,
			Count:      2,
			QPS:        0.67,
			AvgMs:      25.5,
			P50Ms:      20,
			P95Ms:      40.1,
			P99Ms:      50,
			MaxMs:      50,
			ErrorCount: 1,
			SlowCount:  1,
		}},
	}
	sqlSnap := &sqlhistory.Snapshot{
		WindowSeconds: 60,
		StepSeconds:   3,
		RecentQPS:     1.23,
		Buckets: []sqlhistory.Bucket{{
			Timestamp:  ts,
			Count:      2,
			QPS:        0.67,
			AvgMs:      25.5,
			P50Ms:      20,
			P95Ms:      40.1,
			P99Ms:      50,
			MaxMs:      50,
			ErrorCount: 1,
			SlowCount:  1,
		}},
	}

	// 旧契约基线:改造前接口直接序列化 database.HistorySnapshot。
	oldBytes, err := json.Marshal(dbSnap)
	if err != nil {
		t.Fatalf("marshal old: %v", err)
	}

	// 新契约 ×2 路径。
	newFromSQL, err := json.Marshal(snapshotFromSQL(sqlSnap, 200))
	if err != nil {
		t.Fatalf("marshal dto(sql): %v", err)
	}
	if !bytes.Equal(oldBytes, newFromSQL) {
		t.Fatalf("sql 路径 JSON 漂移:\nold=%s\nnew=%s", oldBytes, newFromSQL)
	}
	newFromBuf, err := json.Marshal(historyFromStats(dbSnap))
	if err != nil {
		t.Fatalf("marshal dto(buffer): %v", err)
	}
	if !bytes.Equal(oldBytes, newFromBuf) {
		t.Fatalf("buffer 路径 JSON 漂移:\nold=%s\nnew=%s", oldBytes, newFromBuf)
	}
}
