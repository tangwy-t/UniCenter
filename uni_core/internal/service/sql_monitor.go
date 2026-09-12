package service

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/sqlhistory"
)

// defaultSnapshotTTL 快照响应缓存的默认有效期。
// SQL 指标本身已由 SQLStats 常驻内存实时聚合,真正的开销在冷路径:
// 每次请求都全量遍历 ring buffer 重算百分位。TTL 让窗口期内的并发
// 请求共享同一份结果(单飞),前端 5s 轮询几乎感知不到 1s 的延迟。
const defaultSnapshotTTL = time.Second

// snapshotCacheMaxEntries 每个缓存键族的条目上限(按 window/step 区分)。
// 监控页的参数组合固定有限,几乎不会触发;超限时整体清空重建。
const snapshotCacheMaxEntries = 4

// defaultSampleInterval 历史采样协程的默认采集节奏。
// 3s:与 15m 视图桶粒度一致(step=3s),且是 12s/60s 步长的整数倍;
// 24h 固定保留 = sqlhistory.maxPoints(28800) 条。
const defaultSampleInterval = 3 * time.Second

// lastAppendErrGap Append 失败告警的最小间隔(节流,避免每 3s 刷屏)。
const lastAppendErrGap = 30 * time.Second

type statCacheEntry struct {
	snap *database.StatsSnapshot
	at   time.Time
}

type historyCacheKey struct {
	window, step time.Duration
}

type historyCacheEntry struct {
	snap *response.SQLHistorySnapshot
	at   time.Time
}

// SQLMonitorService 封装 SQL 统计读取逻辑,带短 TTL 快照缓存;
// 内置历史采样协程(store 非空时)。返回的快照是共享只读对象,
// 调用方必须视为不可变、不得修改其内容。
type SQLMonitorService struct {
	stats *database.SQLStats
	store sqlhistory.Store
	log   logger.LoggerInterface

	snapshotTTL time.Duration

	mu         sync.Mutex
	statsCache map[time.Duration]statCacheEntry
	histCache  map[historyCacheKey]historyCacheEntry

	// 采样协程状态(store == nil 时不启动)
	interval        time.Duration
	stopCh          chan struct{}
	closeOnce       sync.Once
	lastTick        time.Time
	lastAppendErrAt time.Time
}

// NewSQLMonitorService 创建 SQLMonitorService 实例。
// store 可为 nil:历史查询回退 ring buffer 现场计算(测试/无 Redis
// 的开发环境),采样协程不启动;log 可为 nil:采样失败仅静默。
// 可选 ttl 参数仅供测试注入;生产使用默认 1 秒。
func NewSQLMonitorService(stats *database.SQLStats, store sqlhistory.Store, log logger.LoggerInterface, ttl ...time.Duration) *SQLMonitorService {
	t := defaultSnapshotTTL
	if len(ttl) > 0 && ttl[0] > 0 {
		t = ttl[0]
	}
	return &SQLMonitorService{
		stats:       stats,
		store:       store,
		log:         log,
		snapshotTTL: t,
		statsCache:  make(map[time.Duration]statCacheEntry),
		histCache:   make(map[historyCacheKey]historyCacheEntry),
		interval:    defaultSampleInterval,
		stopCh:      make(chan struct{}),
	}
}

// GetStats 返回当前所有统计数据的快照(累计模式),TTL 内缓存。
func (s *SQLMonitorService) GetStats() *database.StatsSnapshot {
	return s.getStatsCached(0)
}

// GetStatsWindow 返回指定时间窗口内的统计快照,TTL 内缓存。
// 与累计模式使用相互独立的缓存项,互不覆盖。
func (s *SQLMonitorService) GetStatsWindow(window time.Duration) *database.StatsSnapshot {
	return s.getStatsCached(window)
}

// getStatsCached 内部实现:window=0 表示累计模式,>0 表示窗口模式。
// 计算本身放在锁内(单飞):并发请求在 TTL 内只会触发一次全量重算,
// 其余请求排队后命中缓存;监控页请求频次低,锁内成本可接受。
func (s *SQLMonitorService) getStatsCached(window time.Duration) *database.StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if e, ok := s.statsCache[window]; ok && now.Sub(e.at) < s.snapshotTTL {
		return e.snap
	}

	var snap *database.StatsSnapshot
	if window > 0 {
		snap = s.stats.SnapshotWindow(window)
	} else {
		snap = s.stats.Snapshot()
	}

	if len(s.statsCache) >= snapshotCacheMaxEntries {
		s.statsCache = make(map[time.Duration]statCacheEntry)
	}
	s.statsCache[window] = statCacheEntry{snap: snap, at: now}
	return snap
}

// GetHistory 返回指定窗口内按 step 分桶的时序快照(HTTP DTO)。
// store 非空:读 Redis 滚动窗口(3s 采样、保留 24h、跨重启)。
// store 为空:回退 ring buffer 现场计算(行为与改造前一致)。
// Redis 读失败返回 error,由 handler 统一 500。
func (s *SQLMonitorService) GetHistory(ctx context.Context, window, step time.Duration) (*response.SQLHistorySnapshot, error) {
	if s.store != nil {
		snap, err := s.store.Query(ctx, window, step)
		if err != nil {
			return nil, err
		}
		return snapshotFromSQL(snap, s.stats.SlowThreshold()), nil
	}
	return s.getHistoryFromBuffer(window, step), nil
}

// getHistoryFromBuffer 旧路径:ring buffer 现场分桶 + 1s TTL 单飞缓存。
func (s *SQLMonitorService) getHistoryFromBuffer(window, step time.Duration) *response.SQLHistorySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	key := historyCacheKey{window: window, step: step}
	if e, ok := s.histCache[key]; ok && now.Sub(e.at) < s.snapshotTTL {
		return e.snap
	}

	snap := historyFromStats(s.stats.SnapshotHistory(window, step))

	if len(s.histCache) >= snapshotCacheMaxEntries {
		s.histCache = make(map[historyCacheKey]historyCacheEntry)
	}
	s.histCache[key] = historyCacheEntry{snap: snap, at: now}
	return snap
}

// snapshotFromSQL 把 sqlhistory.Snapshot 适配为 response.SQLHistorySnapshot,
// 并回填慢查询阈值(store 不感知 SQLStats 配置)。
func snapshotFromSQL(src *sqlhistory.Snapshot, thrMs int64) *response.SQLHistorySnapshot {
	dst := &response.SQLHistorySnapshot{
		WindowSeconds:   src.WindowSeconds,
		StepSeconds:     src.StepSeconds,
		SlowThresholdMs: thrMs,
		RecentQPS:       src.RecentQPS,
		Buckets:         make([]response.SQLHistoryPoint, 0, len(src.Buckets)),
	}
	for _, b := range src.Buckets {
		dst.Buckets = append(dst.Buckets, response.SQLHistoryPoint{
			Timestamp:  b.Timestamp,
			Count:      b.Count,
			QPS:        b.QPS,
			AvgMs:      b.AvgMs,
			P50Ms:      b.P50Ms,
			P95Ms:      b.P95Ms,
			P99Ms:      b.P99Ms,
			MaxMs:      b.MaxMs,
			ErrorCount: b.ErrorCount,
			SlowCount:  b.SlowCount,
		})
	}
	return dst
}

// historyFromStats 把 ring buffer 快照适配为 HTTP DTO(慢查询阈值原样透传)。
func historyFromStats(src *database.HistorySnapshot) *response.SQLHistorySnapshot {
	dst := &response.SQLHistorySnapshot{
		WindowSeconds:   src.WindowSeconds,
		StepSeconds:     src.StepSeconds,
		SlowThresholdMs: src.SlowThresholdMs,
		RecentQPS:       src.RecentQPS,
		Buckets:         make([]response.SQLHistoryPoint, 0, len(src.Buckets)),
	}
	for _, b := range src.Buckets {
		dst.Buckets = append(dst.Buckets, response.SQLHistoryPoint{
			Timestamp:  b.Timestamp,
			Count:      b.Count,
			QPS:        b.QPS,
			AvgMs:      b.AvgMs,
			P50Ms:      b.P50Ms,
			P95Ms:      b.P95Ms,
			P99Ms:      b.P99Ms,
			MaxMs:      b.MaxMs,
			ErrorCount: b.ErrorCount,
			SlowCount:  b.SlowCount,
		})
	}
	return dst
}

// Start 启动历史采样协程;store == nil 时 no-op。
// 可选 sampleInterval 仅供测试注入短节奏,生产用默认 3s。
func (s *SQLMonitorService) Start(sampleInterval ...time.Duration) {
	if s.store == nil {
		return
	}
	if len(sampleInterval) > 0 && sampleInterval[0] > 0 {
		s.interval = sampleInterval[0]
	}
	s.lastTick = time.Now().Add(-s.interval)
	go s.run()
}

func (s *SQLMonitorService) run() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sampleOnce()
		case <-s.stopCh:
			return
		}
	}
}

// Close 停止采样协程(幂等,注册到 lifecycle drain 钩子)。
func (s *SQLMonitorService) Close() {
	s.closeOnce.Do(func() { close(s.stopCh) })
}

// sampleOnce 计算 (lastTick, now] 区间摘要并 Append 到 Redis。
// 无论 Append 成败都推进 lastTick(失败不重试,避免区间重复计数、
// 下轮越积越长);空区间不写入(保留「剔空桶」语义)。
func (s *SQLMonitorService) sampleOnce() {
	now := time.Now()
	since := s.lastTick
	s.lastTick = now
	if s.store == nil {
		return
	}

	sum := s.stats.IntervalSummary(since)
	if sum.Count == 0 {
		return
	}
	p := sqlhistory.Point{
		T:          now.UnixMilli(),
		Count:      sum.Count,
		AvgMs:      sum.AvgMs,
		P50Ms:      sum.P50Ms,
		P95Ms:      sum.P95Ms,
		P99Ms:      sum.P99Ms,
		MaxMs:      sum.MaxMs,
		ErrorCount: sum.ErrorCount,
		SlowCount:  sum.SlowCount,
	}
	if err := s.store.Append(context.Background(), p); err != nil && s.log != nil {
		if now.Sub(s.lastAppendErrAt) >= lastAppendErrGap {
			s.log.Warn("failed to append sql history point", zap.Error(err))
			s.lastAppendErrAt = now
		}
	}
}
