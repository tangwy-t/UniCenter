package metricshistory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// Options 配置一个 Redis 滚动窗口。
type Options struct {
	Key       string        // Redis list key
	Step      time.Duration // 采样器节奏:取点启发式按 window/Step 估算
	MaxPoints int64         // 窗口容量(条数)
	QueryTTL  time.Duration // 查询结果 TTL;<=0 使用默认 1s
	CacheMax  int           // 查询缓存键族上限;<=0 使用默认 4
}

// AggregateFunc 把取到的原始点聚合为领域快照 S。
type AggregateFunc[P, S any] func(points []P, b Buckets) (*S, error)

// Window 是 Redis 滚动窗口:Append 写入采样点(LPUSH+LTRIM),
// Query 校验参数、读头取点、调用 agg 聚合并带短 TTL 缓存。
// 返回的快照是共享只读对象,调用方不得修改。
type Window[P, S any] struct {
	rdb  goredis.UniversalClient
	opts Options
	agg  AggregateFunc[P, S]

	mu    sync.Mutex
	cache map[queryKey]queryEntry[S]
	// sf 合并同一 queryKey 的并发缓存 miss，把 Redis 往返移到互斥锁之外，
	// 同时避免"释放锁后各自回源"的惊群（评审 #20）。
	sf singleflight.Group
}

// queryKey 按归一化后的 window/step 秒数分键。与旧实现的原始
// duration 分键相比,1m 与 60s 现在共享同一缓存项(聚合结果本就相同),
// 其余行为一致。
type queryKey struct{ windowSec, stepSec int64 }

// String 返回 queryKey 的稳定字符串形式，供 singleflight 去重键使用。
func (k queryKey) String() string {
	return fmt.Sprintf("%d:%d", k.windowSec, k.stepSec)
}

type queryEntry[S any] struct {
	snap *S
	at   time.Time
}

// NewWindow 创建滚动窗口。P 必须可 json.Marshal(采样点契约)。
// QueryTTL<=0 取 1s,CacheMax<=0 取 4,与旧默认一致。
func NewWindow[P, S any](rdb goredis.UniversalClient, opts Options, agg AggregateFunc[P, S]) *Window[P, S] {
	if opts.QueryTTL <= 0 {
		opts.QueryTTL = time.Second
	}
	if opts.CacheMax <= 0 {
		opts.CacheMax = 4
	}
	return &Window[P, S]{rdb: rdb, opts: opts, agg: agg, cache: make(map[queryKey]queryEntry[S])}
}

// Append 把一个采样点压入滚动窗口头部并裁剪到 MaxPoints 条。
// LPUSH+LTRIM 走 TxPipeline,一次往返。
func (w *Window[P, S]) Append(ctx context.Context, p P) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	pipe := w.rdb.TxPipeline()
	pipe.LPush(ctx, w.opts.Key, payload)
	pipe.LTrim(ctx, w.opts.Key, 0, w.opts.MaxPoints-1)
	_, err = pipe.Exec(ctx)
	return err
}

// Query 返回 [now-window, now] 内按 step 聚合的快照,TTL 内共享同一份结果。
//
// 安全修复（评审 #20）：此前在 w.mu.Lock() 内调用 w.rdb.LRange，整个 Redis
// 网络往返都在互斥锁内完成，任何一次 Redis 抖动都会串行阻塞同一 Window 上
// 的所有并发 Query（队头阻塞）。现在互斥锁只保护内存 cache 的快读写，Redis
// 往返移到锁外，并用 singleflight 合并同一 key 的并发 miss 以避免惊群。
func (w *Window[P, S]) Query(ctx context.Context, window, step time.Duration) (*S, error) {
	b, err := AlignBuckets(window, step)
	if err != nil {
		return nil, err
	}
	key := queryKey{windowSec: b.WindowSec, stepSec: b.StepSec}

	// 快路径：TTL 内命中内存缓存，仅在锁内做一次 map 读。
	w.mu.Lock()
	if e, ok := w.cache[key]; ok && b.Now.Sub(e.at) < w.opts.QueryTTL {
		snap := e.snap
		w.mu.Unlock()
		return snap, nil
	}
	w.mu.Unlock()

	// 慢路径：Redis 往返在锁外完成，同一 key 的并发 miss 由 singleflight
	// 合并为一次回源，避免惊群与锁跨 IO 持有。
	v, err, _ := w.sf.Do(key.String(), func() (any, error) {
		snap, err := w.queryUncached(ctx, b)
		if err != nil {
			return nil, err
		}
		w.mu.Lock()
		if len(w.cache) >= w.opts.CacheMax {
			w.cache = make(map[queryKey]queryEntry[S])
		}
		w.cache[key] = queryEntry[S]{snap: snap, at: b.Now}
		w.mu.Unlock()
		return snap, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*S), nil
}

// queryUncached 从 Redis 读头部采样点并聚合;单条坏数据跳过,不影响整体。
// 取点数 = window/采样节奏 × 2 安全系数 + 2:采样点时间戳取自墙钟,
// 虚拟机环境墙钟步进(回跳/前跳)会压缩点的时间戳密度,只按
// window/节奏精确取点会覆盖不满窗口;多取的点会被聚合端按
// cutoff 丢弃,无副作用。
func (w *Window[P, S]) queryUncached(ctx context.Context, b Buckets) (*S, error) {
	stepSec := int64(w.opts.Step.Seconds())
	if stepSec < 1 {
		stepSec = 1
	}
	fetch := int(b.WindowSec)/int(stepSec)*2 + 2
	if fetch < 1 {
		fetch = 1
	}
	if int64(fetch) > w.opts.MaxPoints {
		fetch = int(w.opts.MaxPoints)
	}

	// LPUSH 后最新点在列表头部(index 0),必须读头而不是尾;
	// 读尾会把窗口外最旧的点取回来,导致 window 较小时整窗被截断为空。
	raw, err := w.rdb.LRange(ctx, w.opts.Key, 0, int64(fetch-1)).Result()
	if err != nil {
		return nil, err
	}

	points := make([]P, 0, len(raw))
	for _, item := range raw {
		var p P
		if json.Unmarshal([]byte(item), &p) != nil {
			continue
		}
		points = append(points, p)
	}
	return w.agg(points, b)
}
