package serverstats

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
)

// HistoryStore 服务器监控历史的 Redis 滚动窗口实现。
// Append/Query 的存储机制(逐点 JSON、LPUSH+LTRIM、取点启发式与
// 短 TTL 查询缓存)沉淀在 metricshistory.Window,本类型只装配
// 服务器监控自身的 key / 采样节奏 / 容量与聚合函数。
type HistoryStore struct {
	win *metricshistory.Window[Point, Snapshot]
}

// NewHistoryStore 创建 HistoryStore。
// 可选 ttl 参数仅供测试注入;生产使用默认 1 秒。
func NewHistoryStore(rdb goredis.UniversalClient, ttl ...time.Duration) *HistoryStore {
	opts := metricshistory.Options{
		Key:       historyKey,
		Step:      sampleStepSeconds * time.Second,
		MaxPoints: maxPoints,
	}
	if len(ttl) > 0 && ttl[0] > 0 {
		opts.QueryTTL = ttl[0]
	}
	return &HistoryStore{win: metricshistory.NewWindow(rdb, opts, aggregate)}
}

// Append 把一个采样点压入滚动窗口头部并裁剪到 maxPoints 条。
func (s *HistoryStore) Append(ctx context.Context, p Point) error {
	return s.win.Append(ctx, p)
}

// Query 返回 [now-window, now] 内按 step 聚合的历史快照
// (结果在短 TTL 内共享,调用方不得修改)。
func (s *HistoryStore) Query(ctx context.Context, window, step time.Duration) (*Snapshot, error) {
	return s.win.Query(ctx, window, step)
}
