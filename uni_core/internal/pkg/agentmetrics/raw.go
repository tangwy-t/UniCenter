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

// MaxPoints 返回本实例**冻结的窗口容量**（条数上限）—— 只读访问器，供「运行期窗口比对」用。
//
// 为什么要这个访问器（而不是让消费方自己持有装配时的那个数）：容量是 `RawStore` 的内部
// 决策，装配侧算完就交出去了；消费方（flush 的每轮比对）若自己去读配置再推导，就会变成
// 「按**当前**配置推导容量」—— 而缺陷恰恰是容量**不**跟着配置走，那样比对永远自洽、永远不响。
// 唯一正确的事实来源是真正被用来构造滚动窗的那个数。
//
// 为什么只要 `MaxPoints()` 一个数、**不**导出 `Options() RawOptions`：比对需要的步长必须是
// **当前**配置的 `sys.agent.reportInterval`（可热更），而 `RawOptions.Step` 是**启动时冻结**
// 的那一个。把整个 `Options` 暴露出去，等于给「拿冻结的 Step 当现在的步长」留一条缝 ——
// 两个冻结值互相自洽，正是本访问器要暴露的那种错配会被它掩盖掉。返回一个数既够用、
// 又把那条错路从签名上关掉了（同 `FlushRawReader` 只给 Index/BucketRange 的取向）。
func (s *RawStore) MaxPoints() int64 { return s.opts.MaxPoints }

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
// 它是 BucketRange 的**一行薄封装**（签名与语义都不变）：查询服务的资源下钻
// （AgentRawBucketReader，见 agent_metrics_query.go 的 ≤24h 下钻）按**单个窗口**取点，
// 与 flush 的「读一次覆盖整段」是两种消费面，故两条入口各自保留 —— 下钻不需要整段读，
// flush 不该按桶切开来读（理由见 BucketRange 的注释）。
func (s *RawStore) Bucket(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error) {
	return s.BucketRange(ctx, deviceID, fromMs, toMs)
}

// BucketRange 读取某设备在 [fromMs, toMs) 内的原始样本，按 T 升序返回：一次 LRange
// 覆盖**整段**（而不是每个桶各读一次），调用方再在内存里按桶切分。
//
// 这里**不用** Window.Query：Query 是「拖尾窗口」语义（相对 now 的窗口 + 查询时聚合），
// 而 flush 需要的是「固定的已闭桶」，故直接 LRange 读取并按 T 过滤。
//
// 取点数的**唯一正确依据是「要读的最老点离最新点有多远」**，而不是请求窗口的宽度。
// 原因在窗的写侧：metricshistory.Window.Append 用 LPUSH + LTRIM 写入，**index 0 是最新点**，
// 于是「读多少条」等价于「从最新点往回覆盖多远」。一次 LRange 只能从头部往后取，
// 想读到 fromMs 附近的点，就必须至少取到 (最新点T − fromMs) 这段时间的点数。
//
// 为什么入口是「整段」而不是「单桶」（Plan 2E Task 1）：fetch 由 fromMs 推出，于是
// **请求起点越老、读得越多**。flush 逐桶回填积压时（首轮 288 个桶）每个桶都要为
// 「自己离最新点多远」付一次满额读取，最坏各读 MaxPoints 条 ≈ 2.5M 条 JSON，
// 而其中绝大多数点被重复读 288 遍。整段读一次时 fetch 只由**最老**的那个桶推出，
// 它天然覆盖后面每个桶（fetch 随起点变老单调不减），把「读多少」与「有几个桶」解耦：
// 逐桶调用 Bucket 的语义仍等价（同一批点、同样的过滤），只是把重复读变成一次读。
//
// 曾经写作 ((toMs-fromMs)/Step)*2 —— 只按**请求窗口宽度**估算。它对「贴着 now 的窗口」
// （热层 Query 的拖尾窗口、下钻的相对区间查询）恰好成立，却与「这个桶离现在有多远」
// 完全无关：flush 逐桶回填历史桶时，无论桶多老都只取最新 fetch 个点，**所有老桶一律被
// 读成空桶**，可见范围只有最新的 fetch×Step（5min 桶、10s 节奏下约 10 分钟）。
// 而空桶在 flush 里是「照常推进水位」的合法结果（见 agent_metrics_flush.go 的 flushDevice），
// 水位会直接越过那个桶 —— device_metric_5m 出现**永久空洞**（5m 是唯一真值来源，
// 1h 从它回滚）。Bootstrap 注释宣称的「从 now−24h 补齐」也因此在同一处失效：
// 它只能补到最新约 10 分钟，更老的桶全被当空桶跳过。
//
// 保留 ×2 余量：真实上报节奏可能快于 Step（墙钟步进、reportInterval 被改小），
// 少读会真的漏点，多读只是多取几条、由下面的 T 过滤与 MaxPoints 上限兜住。
func (s *RawStore) BucketRange(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error) {
	key := historyKey(deviceID)
	stepMs := s.opts.Step.Milliseconds()

	// 兜底：拿不到「最新点的 T」时的按窗口宽度估算（就是上面被否定的那条启发式，
	// 此处只在信息缺失时当保守退路用）。
	widthFetch := int64(1)
	if stepMs > 0 {
		widthFetch = ((toMs - fromMs) / stepMs) * 2
	}
	if widthFetch < 1 {
		widthFetch = 1
	}

	// 第一步：读头部一条拿最新点的 T。fetch 必须由它推出来，而不能由窗口宽度推。
	// 列表为空（该设备从无原始点 / 已 Purge）→ 直接返回空，**不再发第二次请求**。
	head, err := s.rdb.LIndex(ctx, key, 0).Result()
	if err == goredis.Nil {
		return []agentproto.MetricsSample{}, nil
	}
	if err != nil {
		return nil, err
	}

	fetch := int64(1)
	var newest agentproto.MetricsSample
	switch {
	case json.Unmarshal([]byte(head), &newest) != nil || stepMs <= 0:
		// 头部这条解析不了（只可能来自旧版本协议），或 Step 未配置、无法把时间
		// 距离换算成点数：此时**不能**按「1 条」读（那会让整桶读空），也**不能**
		// 让整桶失败 —— 单条脏数据只该伤到它自己。故取到容量上限；连上限都没配
		// （MaxPoints<=0）时退回窗口宽度估算。
		fetch = s.opts.MaxPoints
		if fetch <= 0 {
			fetch = widthFetch
		}
	case newest.T < fromMs:
		// 请求的是未来窗口（最新点还没走到 fromMs）：区间内不可能有数据，读 1 条即可判空。
		fetch = 1
	default:
		// 距离 = 最新点回到 fromMs 的跨度；×2 余量 + 2 条头部兜底。
		fetch = (newest.T-fromMs)/stepMs*2 + 2
	}
	if fetch < 1 {
		fetch = 1
	}
	// 上界夹到窗口容量：极老窗口的 fetch 会远大于窗里实际存在的点数，
	// 不夹就等于「读全窗」（24h 窗 8640 点 × 288 个桶不可接受）。
	if s.opts.MaxPoints > 0 && fetch > s.opts.MaxPoints {
		fetch = s.opts.MaxPoints
	}

	raws, err := s.rdb.LRange(ctx, key, 0, fetch-1).Result()
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
