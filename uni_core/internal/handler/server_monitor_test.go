package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger/loggertest"
	"github.com/tangwy-t/webmanager-server/internal/pkg/serverstats"
)

// 采样协程应发布缓存快照,HTTP 请求读到完整指标。
func TestServerMonitorHandler_ServesCachedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewServerMonitorHandler(loggertest.New(), nil, 10*time.Millisecond)
	t.Cleanup(h.Close)

	// 首个采样周期前请求:走同步兜底采集,同样应返回 200 + 完整字段。
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.ServeHTTP(c)
	if w.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", w.Code, http.StatusOK)
	}
	if body := w.Body.String(); !strings.Contains(body, `"version"`) {
		t.Fatalf("first response missing server.version: %s", body)
	}

	// 等待采样协程发布缓存快照。
	deadline := time.Now().Add(2 * time.Second)
	for h.CachedSnapshot() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.CachedSnapshot() == nil {
		t.Fatal("采样协程未在时限内发布缓存快照")
	}

	// 缓存命中路径。
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	h.ServeHTTP(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("cached request status = %d, want %d", w2.Code, http.StatusOK)
	}
	if body := w2.Body.String(); !strings.Contains(body, `"version"`) {
		t.Fatalf("cached response missing server.version: %s", body)
	}
}

// Close 幂等:两次关闭不 panic(注册到 lifecycle 后可能被重复触发)。
func TestServerMonitorHandler_CloseIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewServerMonitorHandler(loggertest.New(), nil, time.Millisecond)
	h.Close()
	h.Close()
}

// stubHistoryStore 内存实现:捕获 Append 的采样点与 Query 的参数。
type stubHistoryStore struct {
	mu      sync.Mutex
	points  []serverstats.Point
	queries [][2]time.Duration
	appends int
}

func (s *stubHistoryStore) Append(_ context.Context, p serverstats.Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points = append(s.points, p)
	s.appends++
	return nil
}

func (s *stubHistoryStore) Query(_ context.Context, window, step time.Duration) (*serverstats.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, [2]time.Duration{window, step})
	return &serverstats.Snapshot{WindowSeconds: int64(window.Seconds()), StepSeconds: int64(step.Seconds()), Buckets: []serverstats.Bucket{}}, nil
}

// 采样协程应把每轮采样追加到历史存储。
func TestServerMonitorHandlerAppendsHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &stubHistoryStore{}
	h := NewServerMonitorHandler(loggertest.New(), store, 10*time.Millisecond)
	t.Cleanup(h.Close)

	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		n := store.appends
		store.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if store.appends == 0 {
		t.Fatal("采样协程未追加任何历史点")
	}
	store.mu.Lock()
	ts := int64(0)
	if len(store.points) > 0 {
		ts = store.points[0].T
	}
	store.mu.Unlock()
	if ts == 0 {
		t.Fatal("历史点缺少时间戳")
	}
}

// GetHistory 默认参数:window 15m、step 5s(900/180)。
func TestServerMonitorHandlerGetHistoryDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &stubHistoryStore{}
	h := NewServerMonitorHandler(loggertest.New(), store, time.Hour)
	t.Cleanup(h.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/monitor/server/history", nil)
	h.GetHistory(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `"window_seconds":900`) || !strings.Contains(body, `"step_seconds":5`) {
		t.Fatalf("默认参数错误: %s", body)
	}
	if len(store.queries) != 1 || store.queries[0] != [2]time.Duration{15 * time.Minute, 5 * time.Second} {
		t.Fatalf("query 参数 = %#v, want 15m/5s", store.queries)
	}
}

// 非法 window/step 返回 400;未配置存储返回 500。
func TestServerMonitorHandlerGetHistoryValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name     string
		target   string
		nilStore bool
		want     int
	}{
		{"window 格式错误", "/api/v1/monitor/server/history?window=abc", false, http.StatusBadRequest},
		{"window 小于 1m", "/api/v1/monitor/server/history?window=30s", false, http.StatusBadRequest},
		{"step 大于 window", "/api/v1/monitor/server/history?window=15m&step=1h", false, http.StatusBadRequest},
		{"step 格式错误", "/api/v1/monitor/server/history?window=15m&step=xyz", false, http.StatusBadRequest},
		{"存储未配置", "/api/v1/monitor/server/history", true, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var store serverstats.Store
			if !tc.nilStore {
				store = &stubHistoryStore{}
			}
			h := NewServerMonitorHandler(loggertest.New(), store, time.Hour)
			t.Cleanup(h.Close)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
			h.GetHistory(c)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d, body: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
