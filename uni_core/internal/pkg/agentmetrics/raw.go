package agentmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/metricshistory"
)

// Redis key 约定（与 spec §7.1 一致）：
//
//	agent:device:{id}:history  —— 原始 10s 点（滚动窗，24h）
//	agent:device:{id}:latest   —— 最新水位（latest.go）
//	agent:device:index         —— 活跃设备 SET（枚举源）
const (
	historyKeyPrefix = "agent:device:"
	historyKeySuffix = ":history"
	// DeviceIndexKey 是活跃设备集合；写路径 SADD、删除设备时 SREM。
	DeviceIndexKey = "agent:device:index"
)

func historyKey(deviceID uint64) string {
	return fmt.Sprintf("%s%d%s", historyKeyPrefix, deviceID, historyKeySuffix)
}

// latestKey 是水位键（内容由 Task 2 的 latest.go 定义与读写）。
// 定义放在这里而不是 latest.go：RawStore.Purge 必须**同时**删掉原始窗与水位，
// 两者是同一份 key 约定的两半，放一起才不会漂移。
//
// 键名是 **Redis key 契约**，必须与 spec §7 表格逐字一致：
//
//	agent:device:{id}:latest
//
// 曾经写成 `agent:device:latest:{id}`（latest 在前、设备号在后）。这类偏差
// 不会让任何测试变红（读写共用本函数，自洽即通过），却会让**别的**消费方
// —— 2C 的 flush/rollup 任务、每日孤儿键兜底扫描、运维手工 DEL、外部脚本 ——
// 按 spec 的键名找不到数据，且症状是「静默读不到」而不是报错。
// 契约守卫见 TestLatestKeyMatchesSpecContract（用字符串字面量钉住键名，
// 不复用本函数，否则改错了也自洽）。
func latestKey(deviceID uint64) string {
	return fmt.Sprintf("%s%d:latest", historyKeyPrefix, deviceID)
}

// RawStore 是**每设备一个** Redis 原始滚动窗。
//
// 为什么每设备一个实例：metricshistory.Options.Key 是单个 key，而设备维度必须隔离。
// 改造 metricshistory 支持多 key 会波及既有的 serverstats / sqlhistory 两个消费者，
// 风险大于收益（见设计文档 §8 R4）。
type RawStore struct {
	rdb  goredis.UniversalClient
	opts RawOptions

	mu   sync.Mutex
	wins map[uint64]*metricshistory.Window[agentproto.MetricsSample, RawSnapshot]
}

func NewRawStore(rdb goredis.UniversalClient, opts RawOptions) *RawStore {
	return &RawStore{rdb: rdb, opts: opts, wins: map[uint64]*metricshistory.Window[agentproto.MetricsSample, RawSnapshot]{}}
}

// windowFor 懒建某设备的滚动窗（带锁）。
func (s *RawStore) windowFor(deviceID uint64) *metricshistory.Window[agentproto.MetricsSample, RawSnapshot] {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.wins[deviceID]
	if !ok {
		w = metricshistory.NewWindow(s.rdb, metricshistory.Options{
			Key:       historyKey(deviceID),
			Step:      s.opts.Step,
			MaxPoints: s.opts.MaxPoints,
			QueryTTL:  s.opts.QueryTTL,
		}, aggregateRaw)
		s.wins[deviceID] = w
	}
	return w
}

// Append 把一个原始样本压入该设备的滚动窗，并登记活跃设备。
// 写 Redis 失败不阻断上报通道（由调用方决定是否记日志/告警，见 spec §11）。
func (s *RawStore) Append(ctx context.Context, deviceID uint64, sample *agentproto.MetricsSample) error {
	if sample == nil {
		return nil
	}
	if err := s.windowFor(deviceID).Append(ctx, *sample); err != nil {
		return err
	}
	return s.rdb.SAdd(ctx, DeviceIndexKey, deviceID).Err()
}

// Bucket 读取某设备在 [fromMs, toMs) 内的原始样本，按 T 升序返回。
//
// 这里**不用** Window.Query：Query 是「拖尾窗口」语义（相对 now 的窗口 + 查询时聚合），
// 而 flush 需要的是「固定的已闭桶」，故直接 LRange 读取并按 T 过滤。
func (s *RawStore) Bucket(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error) {
	// 取点启发式与 metricshistory.queryUncached 保持一致：按 (toMs-fromMs)/Step 估算，
	// 再乘 2 留余量，避免采样密度略高于 Step 时漏点。
	fetch := int64(1)
	if s.opts.Step > 0 {
		fetch = ((toMs - fromMs) / s.opts.Step.Milliseconds()) * 2
	}
	if fetch < 1 {
		fetch = 1
	}
	if s.opts.MaxPoints > 0 && fetch > s.opts.MaxPoints {
		fetch = s.opts.MaxPoints
	}

	raws, err := s.rdb.LRange(ctx, historyKey(deviceID), 0, fetch-1).Result()
	if err != nil {
		return nil, err
	}

	out := make([]agentproto.MetricsSample, 0, len(raws))
	for _, raw := range raws {
		var p agentproto.MetricsSample
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			// 单条脏数据不应让整桶失败：跳过并继续（脏数据只可能来自旧版本协议）。
			continue
		}
		if p.T >= fromMs && p.T < toMs {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// Query 返回最近 window 内按 step 聚合的趋势（热层路径，供 range ≤ 24h 用）。
// 结果在 QueryTTL 内共享，调用方**不得修改**返回的切片。
func (s *RawStore) Query(ctx context.Context, deviceID uint64, window, step time.Duration) (*RawSnapshot, error) {
	return s.windowFor(deviceID).Query(ctx, window, step)
}

// Index 返回当前活跃设备（原始窗里可能有数据的设备）。
func (s *RawStore) Index(ctx context.Context) ([]uint64, error) {
	strs, err := s.rdb.SMembers(ctx, DeviceIndexKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(strs))
	for _, s := range strs {
		var id uint64
		if _, err := fmt.Sscanf(s, "%d", &id); err != nil {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Purge 删除某设备的全部 Redis 侧数据（原始窗 + 水位 + 活跃标记）。
// 设备被删除时必须调用，否则这些 key 永久泄漏（spec §7.3）。
func (s *RawStore) Purge(ctx context.Context, deviceID uint64) error {
	s.Forget(deviceID)
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, historyKey(deviceID))
	pipe.Del(ctx, latestKey(deviceID))
	pipe.SRem(ctx, DeviceIndexKey, deviceID)
	_, err := pipe.Exec(ctx)
	return err
}

// Forget 丢弃内存里的窗口实例（不删 Redis 数据）。幂等。
func (s *RawStore) Forget(deviceID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.wins, deviceID)
}

// aggregateRaw 把原始样本按绝对时间对齐归并为 step 桶（纯函数，供 metricshistory 调用）。
//
// 分桶方案（对齐 / step 放大 / 边界折叠 / 空桶剔除）由 metricshistory.Buckets 统一提供，
// 本函数只负责热层的聚合语义：桶内**均值**（nil 跳过）、温度取 max、单调量取最新。
func aggregateRaw(points []agentproto.MetricsSample, b metricshistory.Buckets) (*RawSnapshot, error) {
	acc := make([]rawBucketAcc, b.N)
	for _, p := range points {
		ts := time.UnixMilli(p.T)
		if ts.Before(b.Cutoff()) {
			continue
		}
		pos, ok := b.Pos(ts.Unix())
		if !ok {
			continue
		}
		acc[pos].add(p)
	}

	snap := &RawSnapshot{
		WindowSeconds: b.WindowSec,
		StepSeconds:   b.StepSec,
		Buckets:       make([]TrendPoint, 0, b.N),
	}
	for i := range acc {
		if acc[i].samples == 0 {
			continue // 空桶剔除，不产生误导性零值（与 sqlhistory 一致）
		}
		snap.Buckets = append(snap.Buckets, acc[i].finish(b.Timestamp(i).Unix()))
	}
	return snap, nil
}
