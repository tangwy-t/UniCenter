package database

import (
	"container/heap"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// 操作类型索引
const (
	opSelect = iota
	opInsert
	opUpdate
	opDelete
	opOther
)

var opNames = [5]string{"SELECT", "INSERT", "UPDATE", "DELETE", "OTHER"}

// maxSQLLen 是 ring buffer 中存储的 SQL 最大长度（字符数），超出部分截断。
const maxSQLLen = 512

// QueryEntry 记录单次查询的元数据。
type QueryEntry struct {
	Timestamp  util.JSONTime `json:"timestamp"`
	Duration   time.Duration `json:"-"`
	DurationMs float64       `json:"duration_ms"`
	SQL        string        `json:"sql"`
	Table      string        `json:"table"`
	Operation  string        `json:"operation"`
	IsError    bool          `json:"is_error"`
	IsSlow     bool          `json:"is_slow"`
}

// dimStats 按维度（表）的统计计数器。
type dimStats struct {
	count      atomic.Int64
	duration   atomic.Int64 // 总耗时 (ns)
	maxDur     atomic.Int64 // 最大耗时 (ns)
	minDur     atomic.Int64 // 最小耗时 (ns)
	lastAccess atomic.Int64 // 单调递增的时间戳，用于 LRU 淘汰
}

// opDimStats 按操作类型的统计计数器。
type opDimStats struct {
	count    atomic.Int64
	duration atomic.Int64
	maxDur   atomic.Int64
	minDur   atomic.Int64
}

// lruItem 是 LRU 堆中的条目，按 access 时间排序。
type lruItem struct {
	access int64
	key    string
}

type lruHeap []lruItem

func (h lruHeap) Len() int           { return len(h) }
func (h lruHeap) Less(i, j int) bool { return h[i].access < h[j].access }
func (h lruHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *lruHeap) Push(x any) { *h = append(*h, x.(lruItem)) }
func (h *lruHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// DimSnapshot 单维度的统计快照。
type DimSnapshot struct {
	Count int64   `json:"count"`
	AvgMs float64 `json:"avg_ms"`
	MaxMs float64 `json:"max_ms"`
	MinMs float64 `json:"min_ms"`
	P50Ms float64 `json:"p50_ms,omitempty"`
	P95Ms float64 `json:"p95_ms,omitempty"`
	P99Ms float64 `json:"p99_ms,omitempty"`
}

// GlobalSnapshot 全局统计快照。
type GlobalSnapshot struct {
	Count           int64   `json:"count"`
	AvgMs           float64 `json:"avg_ms"`
	MaxMs           float64 `json:"max_ms"`
	MinMs           float64 `json:"min_ms"`
	P50Ms           float64 `json:"p50_ms"`
	P95Ms           float64 `json:"p95_ms"`
	P99Ms           float64 `json:"p99_ms"`
	ErrorCount      int64   `json:"error_count"`
	SlowCount       int64   `json:"slow_count"`
	SlowThresholdMs int64   `json:"slow_threshold_ms"`
}

// StatsSnapshot API 响应的完整统计快照。
type StatsSnapshot struct {
	Global      GlobalSnapshot         `json:"global"`
	ByTable     map[string]DimSnapshot `json:"by_table"`
	ByOperation map[string]DimSnapshot `json:"by_operation"`
	SlowQueries []QueryEntry           `json:"slow_queries"`
}

// HistoryPoint 单个时间桶的聚合指标（由 ring buffer 按时间分桶计算）。
type HistoryPoint struct {
	Timestamp  util.JSONTime `json:"timestamp"`
	Count      int64         `json:"count"`
	QPS        float64       `json:"qps"`
	AvgMs      float64       `json:"avg_ms"`
	P50Ms      float64       `json:"p50_ms"`
	P95Ms      float64       `json:"p95_ms"`
	P99Ms      float64       `json:"p99_ms"`
	MaxMs      float64       `json:"max_ms"`
	ErrorCount int64         `json:"error_count"`
	SlowCount  int64         `json:"slow_count"`
}

// HistorySnapshot 时间序列快照,支撑前端实时趋势图。
type HistorySnapshot struct {
	WindowSeconds   int64          `json:"window_seconds"`
	StepSeconds     int64          `json:"step_seconds"`
	SlowThresholdMs int64          `json:"slow_threshold_ms"`
	RecentQPS       float64        `json:"recent_qps"`
	Buckets         []HistoryPoint `json:"buckets"`
}

// SQLStats 无锁热路径 SQL 统计器。
type SQLStats struct {
	totalCount    atomic.Int64
	totalDuration atomic.Int64
	maxDuration   atomic.Int64
	minDuration   atomic.Int64
	errorCount    atomic.Int64
	slowCount     atomic.Int64
	accessCounter atomic.Int64 // 单调递增计数器，用于 LRU 淘汰

	// slowThresholdMs 慢查询阈值(毫秒),构造后只读,无并发写入。
	slowThresholdMs int64

	mu         sync.RWMutex
	tableStats map[string]*dimStats
	lruHeap    lruHeap

	opStats [5]opDimStats

	buf     []atomic.Pointer[QueryEntry]
	bufIdx  atomic.Int64
	bufSize int
}

// NewSQLStats 创建 SQLStats 实例，bufSize 为 ring buffer 容量，
// slowThreshold 为慢查询阈值(用于快照与 API 响应透出)。
func NewSQLStats(bufSize int, slowThreshold time.Duration) *SQLStats {
	if bufSize <= 0 {
		bufSize = 10000
	}
	thrMs := slowThreshold.Milliseconds()
	if thrMs <= 0 {
		thrMs = 200
	}
	return &SQLStats{
		tableStats:      make(map[string]*dimStats),
		lruHeap:         make(lruHeap, 0),
		buf:             make([]atomic.Pointer[QueryEntry], bufSize),
		bufSize:         bufSize,
		minDuration:     atomic.Int64{},
		slowThresholdMs: thrMs,
	}
}

// Record 记录一次 SQL 执行（热路径，无锁）。
// isSlow 由调用方依据慢查询阈值提前判定。
func (s *SQLStats) Record(table, op string, duration time.Duration, sql string, isError, isSlow bool) {
	ns := duration.Nanoseconds()

	// 全局统计
	s.totalCount.Add(1)
	s.totalDuration.Add(ns)

	// 更新最大耗时
	for {
		old := s.maxDuration.Load()
		if ns <= old {
			break
		}
		if s.maxDuration.CompareAndSwap(old, ns) {
			break
		}
	}

	// 更新最小耗时（跳过初始零值）
	for {
		old := s.minDuration.Load()
		if old > 0 && ns >= old {
			break
		}
		if s.minDuration.CompareAndSwap(old, ns) {
			break
		}
	}

	if isError {
		s.errorCount.Add(1)
	}
	if isSlow {
		s.slowCount.Add(1)
	}

	// 按表统计
	// 安全修复（评审 #12）：在持锁之前分配本次访问的单调时间戳 ts，新键与
	// 已存在键都统一用它写入 lastAccess，且新键入堆也用它。此前新键 heap.Push
	// 的 access 恒为 0、锁外才 Store(ts)，导致：① 0 与 lastAccess>=1 恒不等，
	// evictLRU 的 currentAccess==item.access 判定永不命中（淘汰退化）；② push
	// 后、锁外 Store(ts) 前，另一 goroutine 触发 evictLRU 读到 lastAccess==0
	// 会 0==0 命中删除分支、误删刚创建的键。统一用同一 ts 后两者恒一致。
	ts := s.accessCounter.Add(1)

	s.mu.RLock()
	ds, ok := s.tableStats[table]
	s.mu.RUnlock()
	if !ok {
		s.mu.Lock()
		// double-check
		ds, ok = s.tableStats[table]
		if !ok {
			// LRU 淘汰：超过 1000 key 时删除最久未访问的
			if len(s.tableStats) >= 1000 {
				s.evictLRU()
			}
			ds = &dimStats{}
			ds.lastAccess.Store(ts)
			ds.minDur.Store(ns)
			s.tableStats[table] = ds
			heap.Push(&s.lruHeap, lruItem{access: ts, key: table})
		}
		s.mu.Unlock()
	}
	// 更新 lastAccess 用于 LRU 淘汰：已存在键在此刷新热度；新键已在持锁
	// 分支内写入同一 ts，此处重复 Store 同一值幂等无害。
	ds.lastAccess.Store(ts)
	ds.count.Add(1)
	ds.duration.Add(ns)
	for {
		old := ds.maxDur.Load()
		if ns <= old {
			break
		}
		if ds.maxDur.CompareAndSwap(old, ns) {
			break
		}
	}
	for {
		old := ds.minDur.Load()
		if old > 0 && ns >= old {
			break
		}
		if ds.minDur.CompareAndSwap(old, ns) {
			break
		}
	}

	// 按操作类型统计
	opIdx := classifyOp(op)
	s.opStats[opIdx].count.Add(1)
	s.opStats[opIdx].duration.Add(ns)
	for {
		old := s.opStats[opIdx].maxDur.Load()
		if ns <= old {
			break
		}
		if s.opStats[opIdx].maxDur.CompareAndSwap(old, ns) {
			break
		}
	}
	for {
		old := s.opStats[opIdx].minDur.Load()
		if old > 0 && ns >= old {
			break
		}
		if s.opStats[opIdx].minDur.CompareAndSwap(old, ns) {
			break
		}
	}

	// Ring buffer 写入（无锁）
	// DurationMs 在源头量化到 2 位小数(10µs 分辨率,远高于监控展示所需):
	// 慢查询列表、分位数插值、区间摘要、历史分桶全部以它为输入,
	// 源头干净可以避免长尾小数(µs 原值如 2.345678)扩散到每个派生指标。
	idx := s.bufIdx.Add(1) - 1
	pos := int(idx % int64(s.bufSize))
	s.buf[pos].Store(&QueryEntry{
		Timestamp:  util.JSONTime(time.Now()),
		Duration:   duration,
		DurationMs: util.Round2(float64(duration.Microseconds()) / 1000.0),
		SQL:        truncateSQL(sql),
		Table:      table,
		Operation:  op,
		IsError:    isError,
		IsSlow:     isSlow,
	})
}

// evictLRU 通过最小堆删除最久未访问的 key。
// 调用方必须持有 s.mu.Lock()。
func (s *SQLStats) evictLRU() {
	for s.lruHeap.Len() > 0 {
		item := heap.Pop(&s.lruHeap).(lruItem)
		ds, ok := s.tableStats[item.key]
		if !ok {
			continue // key 已不存在，跳过
		}
		currentAccess := ds.lastAccess.Load()
		if currentAccess == item.access {
			// 堆中 access 与当前值一致，确认为真正 LRU
			delete(s.tableStats, item.key)
			return
		}
		// 堆条目过时：用当前 access 重新入堆
		heap.Push(&s.lruHeap, lruItem{access: currentAccess, key: item.key})
	}
}

// Snapshot 返回当前所有统计数据的快照（冷路径，加读锁）。
func (s *SQLStats) Snapshot() *StatsSnapshot {
	return s.buildSnapshot(nil)
}

// SnapshotWindow 返回指定时间窗口内的统计快照。
func (s *SQLStats) SnapshotWindow(window time.Duration) *StatsSnapshot {
	return s.buildSnapshot(&window)
}

func (s *SQLStats) buildSnapshot(window *time.Duration) *StatsSnapshot {
	snap := &StatsSnapshot{}

	// 全局统计
	total := s.totalCount.Load()
	td := s.totalDuration.Load()
	snap.Global = GlobalSnapshot{
		Count:           total,
		AvgMs:           calcAvgMs(td, total),
		MaxMs:           util.Round2(float64(s.maxDuration.Load()) / 1e6),
		MinMs:           util.Round2(float64(s.minDuration.Load()) / 1e6),
		ErrorCount:      s.errorCount.Load(),
		SlowCount:       s.slowCount.Load(),
		SlowThresholdMs: s.slowThresholdMs,
	}

	// 按表统计
	s.mu.RLock()
	snap.ByTable = make(map[string]DimSnapshot, len(s.tableStats))
	for table, ds := range s.tableStats {
		c := ds.count.Load()
		d := ds.duration.Load()
		snap.ByTable[table] = DimSnapshot{
			Count: c,
			AvgMs: calcAvgMs(d, c),
			MaxMs: util.Round2(float64(ds.maxDur.Load()) / 1e6),
			MinMs: util.Round2(float64(ds.minDur.Load()) / 1e6),
		}
	}
	s.mu.RUnlock()

	// 按操作类型统计
	snap.ByOperation = make(map[string]DimSnapshot, 5)
	for i := 0; i < 5; i++ {
		c := s.opStats[i].count.Load()
		d := s.opStats[i].duration.Load()
		snap.ByOperation[opNames[i]] = DimSnapshot{
			Count: c,
			AvgMs: calcAvgMs(d, c),
			MaxMs: util.Round2(float64(s.opStats[i].maxDur.Load()) / 1e6),
			MinMs: util.Round2(float64(s.opStats[i].minDur.Load()) / 1e6),
		}
	}

	// 从 ring buffer 提取慢查询（最近 100 条）
	now := time.Now()
	entries := s.snapshotEntries(window, now)
	snap.Global.P50Ms, snap.Global.P95Ms, snap.Global.P99Ms = calcPercentiles(entries)

	// 窗口模式：用过滤后的条目覆盖全局统计
	if window != nil {
		snap.Global.Count = int64(len(entries))
		if snap.Global.Count > 0 {
			var totalMs float64
			for _, e := range entries {
				totalMs += e.DurationMs
			}
			snap.Global.AvgMs = util.Round2(totalMs / float64(snap.Global.Count))
		} else {
			snap.Global.AvgMs = 0
		}
	}

	// 按表百分位
	for table := range snap.ByTable {
		var tableEntries []float64
		for _, e := range entries {
			if e.Table == table {
				tableEntries = append(tableEntries, e.DurationMs)
			}
		}
		if len(tableEntries) > 0 {
			sort.Float64s(tableEntries)
			ds := snap.ByTable[table]
			ds.P50Ms = percentile(tableEntries, 0.5)
			ds.P95Ms = percentile(tableEntries, 0.95)
			ds.P99Ms = percentile(tableEntries, 0.99)
			snap.ByTable[table] = ds
		}
	}

	// 慢查询列表（最近 100 条，按耗时降序）
	snap.SlowQueries = s.buildSlowQueryList(entries)

	return snap
}

// walkEntries 按写入顺序遍历 ring buffer 中的有效条目(旧→新)。
func (s *SQLStats) walkEntries(fn func(QueryEntry)) {
	idx := s.bufIdx.Load()
	size := int64(s.bufSize)
	start := int64(0)
	if idx > size {
		start = idx - size
	}
	for i := start; i < idx; i++ {
		pos := int(i % size)
		e := s.buf[pos].Load()
		// 跳过未初始化的条目
		if e == nil || time.Time(e.Timestamp).IsZero() {
			continue
		}
		fn(*e)
	}
}

func (s *SQLStats) snapshotEntries(window *time.Duration, now time.Time) []QueryEntry {
	entries := make([]QueryEntry, 0, s.bufSize)
	s.walkEntries(func(e QueryEntry) {
		if window != nil {
			if now.Sub(time.Time(e.Timestamp)) > *window {
				return
			}
		}
		entries = append(entries, e)
	})
	return entries
}

func (s *SQLStats) buildSlowQueryList(entries []QueryEntry) []QueryEntry {
	// 按耗时降序排序
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].DurationMs > entries[j].DurationMs
	})
	limit := 100
	if len(entries) < limit {
		limit = len(entries)
	}
	return entries[:limit]
}

// SnapshotHistory 返回最近 window 内按 step 分桶的时序快照。
// 桶边界按绝对时间对齐(Unix 秒整除 step),两次轮询得到的桶划分保持稳定,
// 空桶会被剔除:前端图表直接按返回的 timestamp 排布,不产生误导性零值。
// 参数夹取(step<1s 取 1s、window<step 取 step)后交给 metricshistory
// 共享分桶方案,与 Redis 路径的 serverstats / sqlhistory 同源。
func (s *SQLStats) SnapshotHistory(window, step time.Duration) *HistorySnapshot {
	now := time.Now()

	stepSec := int64(step.Seconds())
	if stepSec < 1 {
		stepSec = 1
	}
	windowSec := int64(window.Seconds())
	if windowSec < stepSec {
		windowSec = stepSec
	}

	// 夹取后参数必合法(stepSec>=1、windowSec>=stepSec);错误分支仅防御。
	b, err := metricshistory.AlignBucketsAt(now, time.Duration(windowSec)*time.Second, time.Duration(stepSec)*time.Second)
	if err != nil {
		return &HistorySnapshot{
			WindowSeconds:   windowSec,
			StepSeconds:     stepSec,
			SlowThresholdMs: s.slowThresholdMs,
		}
	}

	points := make([]HistoryPoint, b.N)
	durs := make([][]float64, b.N)
	for i := range points {
		points[i].Timestamp = util.JSONTime(b.Timestamp(i))
	}

	// 当前最近一个 step 内的实时 QPS
	recentCut := b.RecentCut()
	var recentCount int64

	s.walkEntries(func(e QueryEntry) {
		ts := time.Time(e.Timestamp)
		if ts.Before(b.Cutoff()) {
			return
		}
		if !ts.Before(recentCut) {
			recentCount++
		}
		// 边界桶:当前秒内、与快照 now 同秒的记录恰好落在 [now, now+step)
		// 半开区间外,由 b.Pos 折叠进最后一桶,避免最新秒的数据被丢弃。
		pos, ok := b.Pos(ts.Unix())
		if !ok {
			return
		}
		p := &points[pos]
		p.Count++
		if e.DurationMs > p.MaxMs {
			p.MaxMs = e.DurationMs
		}
		if e.IsError {
			p.ErrorCount++
		}
		if e.IsSlow {
			p.SlowCount++
		}
		durs[pos] = append(durs[pos], e.DurationMs)
	})

	buckets := make([]HistoryPoint, 0, b.N)
	for i := range points {
		p := &points[i]
		if p.Count == 0 {
			continue
		}
		p.QPS = util.Round2(float64(p.Count) / float64(b.StepSec))
		var totalMs float64
		for _, d := range durs[i] {
			totalMs += d
		}
		p.AvgMs = util.Round2(totalMs / float64(p.Count))
		if len(durs[i]) > 0 {
			sort.Float64s(durs[i])
			p.P50Ms = percentile(durs[i], 0.5)
			p.P95Ms = percentile(durs[i], 0.95)
			p.P99Ms = percentile(durs[i], 0.99)
		}
		buckets = append(buckets, *p)
	}

	return &HistorySnapshot{
		WindowSeconds:   b.WindowSec,
		StepSeconds:     b.StepSec,
		SlowThresholdMs: s.slowThresholdMs,
		RecentQPS:       util.Round2(float64(recentCount) / float64(b.StepSec)),
		Buckets:         buckets,
	}
}

// IntervalSummary 区间摘要:遍历 ring buffer 中 ts > since 的条目,
// 产出区间内 count / avg / max / error / slow 与精确分位数。
// count == 0 时其余字段均为 0。仅供 3s 历史采样器使用,
// 不参与 buildSnapshot 冷路径。
type IntervalSummary struct {
	Count      int64
	AvgMs      float64
	MaxMs      float64
	P50Ms      float64
	P95Ms      float64
	P99Ms      float64
	ErrorCount int64
	SlowCount  int64
}

func (s *SQLStats) IntervalSummary(since time.Time) *IntervalSummary {
	sum := &IntervalSummary{}
	var totalMs float64
	durs := make([]float64, 0, 128)
	s.walkEntries(func(e QueryEntry) {
		if !time.Time(e.Timestamp).After(since) {
			return
		}
		sum.Count++
		totalMs += e.DurationMs
		if e.DurationMs > sum.MaxMs {
			sum.MaxMs = e.DurationMs
		}
		if e.IsError {
			sum.ErrorCount++
		}
		if e.IsSlow {
			sum.SlowCount++
		}
		durs = append(durs, e.DurationMs)
	})
	if sum.Count == 0 {
		return sum
	}
	sum.AvgMs = util.Round2(totalMs / float64(sum.Count))
	sort.Float64s(durs)
	sum.P50Ms = percentile(durs, 0.5)
	sum.P95Ms = percentile(durs, 0.95)
	sum.P99Ms = percentile(durs, 0.99)
	return sum
}

// SlowThreshold 返回慢查询阈值(毫秒),供 service 回填历史快照。
func (s *SQLStats) SlowThreshold() int64 {
	return s.slowThresholdMs
}

func classifyOp(op string) int {
	op = strings.ToUpper(strings.TrimSpace(op))
	switch op {
	case "SELECT":
		return opSelect
	case "INSERT":
		return opInsert
	case "UPDATE":
		return opUpdate
	case "DELETE":
		return opDelete
	default:
		return opOther
	}
}

func truncateSQL(s string) string {
	if len(s) <= maxSQLLen {
		return s
	}
	return s[:maxSQLLen] + "..."
}

func calcAvgMs(totalNs, count int64) float64 {
	if count == 0 {
		return 0
	}
	return util.Round2(float64(totalNs) / float64(count) / 1e6)
}

func calcPercentiles(entries []QueryEntry) (p50, p95, p99 float64) {
	if len(entries) == 0 {
		return 0, 0, 0
	}
	durs := make([]float64, len(entries))
	for i, e := range entries {
		durs[i] = e.DurationMs
	}
	sort.Float64s(durs)
	return percentile(durs, 0.5), percentile(durs, 0.95), percentile(durs, 0.99)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return util.Round2(sorted[lo])
	}
	frac := idx - float64(lo)
	// 线性插值必然产生长尾小数(如 3*0.03+4*0.97=3.9699999...),量化到 2 位。
	// Round2 单调,不会破坏 p50<=p95<=p99 的顺序。
	return util.Round2(sorted[lo]*(1-frac) + sorted[hi]*frac)
}
