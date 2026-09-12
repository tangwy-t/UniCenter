package metricshistory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// testPoint 测试用采样点(JSON 形状与领域契约无关,仅验证存储机制)。
type testPoint struct {
	T int64 `json:"t"`
	V int   `json:"v"`
}

// testSnap 测试用快照。
type testSnap struct {
	Points []testPoint `json:"points"`
	Window int64       `json:"window"`
}

// countAgg 聚合函数:直接透传点列表 + 窗口秒数。
func countAgg(points []testPoint, b Buckets) (*testSnap, error) {
	return &testSnap{Points: points, Window: b.WindowSec}, nil
}

// newTestWindow 起内存 miniredis,返回窗口与原始客户端(直接发命令用)。
func newTestWindow(t *testing.T, ttl time.Duration, maxPoints int64) (*Window[testPoint, testSnap], *goredis.Client, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	win := NewWindow(rdb, Options{Key: "test:metrics", Step: 2 * time.Second, MaxPoints: maxPoints, QueryTTL: ttl}, countAgg)
	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return win, rdb, cleanup
}

func appendPt(t *testing.T, win *Window[testPoint, testSnap], p testPoint) {
	t.Helper()
	if err := win.Append(context.Background(), p); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestWindowAppendQueryRoundtrip 写入后可查询往返;聚合函数收到取回的点。
func TestWindowAppendQueryRoundtrip(t *testing.T) {
	win, _, cleanup := newTestWindow(t, time.Hour, 4096)
	defer cleanup()

	appendPt(t, win, testPoint{T: time.Now().Add(-5 * time.Second).UnixMilli(), V: 1})
	appendPt(t, win, testPoint{T: time.Now().Add(-1 * time.Second).UnixMilli(), V: 2})

	snap, err := win.Query(context.Background(), 2*time.Minute, 2*time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(snap.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(snap.Points))
	}
	if snap.Window != 120 {
		t.Fatalf("window = %d, want 120", snap.Window)
	}
}

// TestWindowAppendTrim LTRIM 裁剪:写入超过 MaxPoints 后列表长度封顶。
// (容量用小值验证机制;生产 43200/28800 由各领域 Options 注入。)
func TestWindowAppendTrim(t *testing.T) {
	win, rdb, cleanup := newTestWindow(t, time.Hour, 64)
	defer cleanup()

	for i := 0; i < 64+20; i++ {
		appendPt(t, win, testPoint{T: time.Now().UnixMilli(), V: i})
	}
	ll, err := rdb.LLen(context.Background(), "test:metrics").Result()
	if err != nil {
		t.Fatalf("llen: %v", err)
	}
	if ll != 64 {
		t.Fatalf("llen = %d, want 64", ll)
	}
}

// TestWindowQueryTTL TTL 内共享同一指针;不同 window/step 分键隔离。
func TestWindowQueryTTL(t *testing.T) {
	win, _, cleanup := newTestWindow(t, time.Hour, 4096)
	defer cleanup()
	appendPt(t, win, testPoint{T: time.Now().Add(-time.Second).UnixMilli(), V: 1})

	a, err := win.Query(context.Background(), time.Minute, time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	b, err := win.Query(context.Background(), time.Minute, time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if a != b {
		t.Fatal("TTL 内应返回同一份缓存快照")
	}
	c, err := win.Query(context.Background(), 5*time.Minute, time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if a == c {
		t.Fatal("不同 window 的缓存项应相互独立")
	}
}

// TestWindowQuerySkipsMalformed 单条坏 JSON 跳过,不影响有效点。
func TestWindowQuerySkipsMalformed(t *testing.T) {
	win, rdb, cleanup := newTestWindow(t, time.Hour, 4096)
	defer cleanup()

	ctx := context.Background()
	if err := rdb.LPush(ctx, "test:metrics", "not-valid-json").Err(); err != nil {
		t.Fatalf("lpush: %v", err)
	}
	appendPt(t, win, testPoint{T: time.Now().Add(-time.Second).UnixMilli(), V: 7})

	snap, err := win.Query(ctx, time.Minute, time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(snap.Points) != 1 || snap.Points[0].V != 7 {
		t.Fatalf("points = %+v, want 仅 [v=7](坏点跳过)", snap.Points)
	}
}

// TestWindowQueryReadsNewest 读头语义:LPUSH 后最新点在头部;
// 只取最近 fetch 条(读尾会把窗口外最旧点取回,导致整窗截断)。
func TestWindowQueryReadsNewest(t *testing.T) {
	win, _, cleanup := newTestWindow(t, time.Hour, 32)
	defer cleanup()

	// 30 分钟前的 50 个旧点(超出 window),容量 32 会先被挤出;
	// 最新点必须在结果里。
	for i := 0; i < 50; i++ {
		appendPt(t, win, testPoint{T: time.Now().Add(-30 * time.Minute).UnixMilli(), V: 100 + i})
	}
	newPt := testPoint{T: time.Now().Add(-time.Second).UnixMilli(), V: 10}
	appendPt(t, win, newPt)

	snap, err := win.Query(context.Background(), time.Minute, 2*time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	found := false
	for _, p := range snap.Points {
		if p.V == 10 {
			found = true
		}
	}
	if !found {
		t.Fatal("应包含最新采样点 v=10(读头语义)")
	}
}

// TestWindowQueryInvalidParams 非法参数返回错误(校验在取点之前)。
func TestWindowQueryInvalidParams(t *testing.T) {
	win, _, cleanup := newTestWindow(t, time.Hour, 4096)
	defer cleanup()

	ctx := context.Background()
	if _, err := win.Query(ctx, 0, time.Second); err == nil {
		t.Fatal("window<=0 应报错")
	}
	if _, err := win.Query(ctx, time.Minute, 500*time.Millisecond); err == nil {
		t.Fatal("step<1s 应报错")
	}
	if _, err := win.Query(ctx, 30*time.Second, time.Minute); err == nil {
		t.Fatal("step>window 应报错")
	}
}
