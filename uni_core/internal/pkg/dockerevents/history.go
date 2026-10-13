package dockerevents

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 事件留存（历史窗口跨 core 重启存活）──────────────────────────────────
//
// 起因：保留窗口（memory 里的每主机环形缓冲）是**进程态** —— core 一重启，活动流
// 的历史页就空了，而这恰好发生在最需要它的时候（发布即重启，重启后想回答的正是
// 「重启前那波容器怎么了」）。
//
// 键设计（与 dockerstate 的 `docker:stats:history:` / `docker:cmd:` 同族命名，
// 运维按 `docker:events:history:*` 定位全部留存）：
//
//	docker:events:history:<deviceID>  ZSET —— 每主机一个键：
//	                                         score = core 接收时刻（unix 毫秒），
//	                                         member = historyRecord（JSON）
//
// 为什么是 ZSET 而不是 LIST（`docker:stats:history:` 的每容器序列用的是 LIST）：
// 这里的裁剪是**双限**且带**时窗** —— ZREMRANGEBYSCORE 一次摘掉整段过期的、
// ZREMRANGEBYRANK 一次摘掉超容量的，都是 O(log n + k)；LIST 的 LTRIM 只管容量，
// 时窗要靠「从头逐条读出来判断」才能裁，等于把内存窗口的 pruneLocked 搬到 Redis
// 上再跑一遍（多一次往返 + 全量反序列化）。ZSET 的 score 排序还顺带给出了水合的
// 遍历序（按 core 接收时刻升序 = 到达序）。
//
// 为什么键里只有 deviceID（不像 stats 留存那样「桶 + 每容器一键」两层）：事件
// 的归属就是主机，没有第二层维度；水合的枚举源也不是键扫描，而是 docker:hosts
// 集合（见 Manager.Hydrate），因此不需要桶来反查「有哪些主机」。
const (
	// eventsHistoryKeyPrefix 是事件留存键族前缀。
	eventsHistoryKeyPrefix = "docker:events:history:"
	// defaultPersistQueueDepth 是落盘通道的容量（条）。
	//
	// 1024 ≈ 两台 500 容量的窗口同时灌满：通道只在「Redis 变慢/不可用」的窗口里
	// 堆起来，正常节奏下工作协程几乎总是空转 —— 它不是缓冲，是一道**必须有界**的
	// 落差（满了就丢持久化副本，绝不反压帧路径）。
	defaultPersistQueueDepth = 1024
	// defaultPersistBatch 是单次落盘的最大批量（条）。
	//
	// 256：每事件一次 Redis 往返会把一次容器风暴放大成同规模的小包风暴，批量化
	// 让「50 容器重启的 200 条」落在一次事务里；再大只是让单次写入的延迟账变难看，
	// 没有增量收益（ZADD 的成员数不改变网络往返次数）。
	defaultPersistBatch = 256
	// persistDrainTimeout 是停机收尾（把队列残余尽力写完）的时长上限。
	//
	// 停机**不能**被留存拖住（通知/流/HTTP 的收手都排在这后面），2 秒是「本地
	// Redis 一次事务」与「Redis 已经不可用」两种情形的分界：可达即瞬间写完，
	// 不可达就在 2 秒后放弃 —— 拖住停机去等一个坏 Redis，只会让发布变慢。
	persistDrainTimeout = 2 * time.Second
	// persistTTLMargin 是留存键 TTL 相对保留窗口的余量：TTL = RetainWindow + 它。
	//
	// 为什么 TTL 不能恰等于窗口：TTL 由 **Redis 侧**的挂钟判定，而窗口由 **core**
	// 的挂钟判定（容器/宿主各看各的钟，NTP 也常有毫秒级到秒级偏差）。TTL 取等值时，
	// 钟偏快的一侧会把仍在窗内的尾巴提前抹掉 —— 表现是「重启后历史少了一截」，
	// 且只在部署时偶发，极难归因。10 分钟余量覆盖正常偏差；多出的残留期里那些
	// 条目本就超窗，读侧照旧过滤（与「静默主机不主动清扫、读侧同一把尺」同款纪律）。
	persistTTLMargin = 10 * time.Minute
)

// eventsHistoryKey 返回一台主机的事件留存键（ZSET）。
func eventsHistoryKey(deviceID uint64) string {
	return eventsHistoryKeyPrefix + strconv.FormatUint(deviceID, 10)
}

// historyRecord 是一条事件在 Redis 里的留存形态（ZSET 的 member）。
//
// 为什么**不存接收时刻**：那个事实就是 ZSET 的 score —— 时间存两份必然漂移（改了
// score 忘了改字段），而裁剪（ZREMRANGEBYSCORE）只可能依赖 score 那一份。读回时
// at 由 score 还原（见 historyEntry）。
//
// 为什么存 seq：内存窗口里「同 t 多条」靠到达序决胜（Query 全序的第二键），水合
// 要还原这个顺序。它**只是还原用的中间量**：水合时会被重编号（到达序计数器重启
// 归零，旧进程的值不能当身份）—— 与 retained.seq「刻意不外送」同一条纪律。
type historyRecord struct {
	Seq      uint64                     `json:"seq"`
	Hostname string                     `json:"hostname,omitempty"`
	Item     agentproto.DockerEventItem `json:"item"`
}

// historyEntry 是读回的一条：记录 + 由 score 还原的 core 接收时刻。
type historyEntry struct {
	rec      historyRecord
	atMillis int64
}

// historyStore 是事件留存的内存外落点（Redis ZSET，每主机一个键）。
type historyStore struct {
	rdb goredis.UniversalClient
}

func newHistoryStore(rdb goredis.UniversalClient) *historyStore { return &historyStore{rdb: rdb} }

// persistPruneScriptSrc 原子地「裁剪双限 + 续期」一台主机的留存键（ZADD 在同一
// 事务里先做）。
//
// 为什么裁剪必须用脚本而不是裸命令：容量裁剪是 ZREMRANGEBYRANK，而过负的 stop 在
// 两套 Redis 实现下语义不一致 —— 真 Redis 会把「小于集合长度的负 rank」按「从
// start 一直删到底」钳制（集合不足 keep+1 条时整锅清空，刚 ZADD 进去的那条也活
// 不了），miniredis 则按区间取交（不动）。同一份代码在两套实现下给两个答案，坑的
// 是测试永远绿、生产悄悄空（dockerstate 的 recentAddScriptSrc 先例，同一句话）。
// 脚本里用 ZCARD 的当场读数算正索引，两边语义一致且原子。
//
// 顺序：先裁容量再裁时窗（两者互不依赖，但先容量让「时窗裁剪」面对的是一个已经被
// 压回上限的集合，ZREMRANGEBYSCORE 的扫描量更小 —— 风暴场景下这是主要路径）。
const persistPruneScriptSrc = `
local n = redis.call('ZCARD', KEYS[1])
local keep = tonumber(ARGV[1])
if n > keep then
    redis.call('ZREMRANGEBYRANK', KEYS[1], 0, n - keep - 1)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[2])
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3]))
return redis.call('ZCARD', KEYS[1])
`

// append 把一批同主机记录写进留存键（批量 ZADD + 裁剪续期脚本，一个事务）。
//
// cut 是时窗下界（unix 毫秒，**含**边界：score ≤ cut 的一律摘掉 —— 与内存侧
// pruneLocked 的 `now.Sub(at) > window` 逐毫秒对齐，调用方已把差一毫秒处理掉）
// 与 depth / ttl 都由调用方折算：本层不认识挂钟，时间口径只有一个来源（core 的
// 接收时刻），把「现在几点」的判断留在 manager 侧意味着两边不可能有两把尺。
func (s *historyStore) append(ctx context.Context, deviceID uint64, entries []goredis.Z,
	cut int64, ttl time.Duration, depth int) error {
	if len(entries) == 0 {
		return nil
	}
	key := eventsHistoryKey(deviceID)
	pipe := s.rdb.TxPipeline()
	pipe.ZAdd(ctx, key, entries...)
	pipe.Eval(ctx, persistPruneScriptSrc, []string{key}, depth, cut, int(ttl/time.Second))
	_, err := pipe.Exec(ctx)
	return err
}

// load 读回一台主机留存键里的全部记录（按 score 升序 = core 接收时刻升序）。
//
// 单条 member 损坏（解不开）**跳过该条**：损坏只让历史缺一条，把整窗判成错误则是
// 杀了全部数据（dockerstate 紧凑序列的同款纪律）。键不存在是正常答案（从未留存 /
// TTL 已到），返回空切片而非错误 ——「没有历史」与「读了但失败」在调用侧是两种
// 处置（前者无事可做，后者要留日志），不能混成一个答案。
func (s *historyStore) load(ctx context.Context, deviceID uint64) ([]historyEntry, error) {
	zs, err := s.rdb.ZRangeWithScores(ctx, eventsHistoryKey(deviceID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]historyEntry, 0, len(zs))
	for _, z := range zs {
		member, ok := z.Member.(string)
		if !ok {
			continue
		}
		var rec historyRecord
		if err := json.Unmarshal([]byte(member), &rec); err != nil {
			continue
		}
		out = append(out, historyEntry{rec: rec, atMillis: int64(z.Score)})
	}
	return out, nil
}

// encodeHistoryRecord 把一条内存账目编码成留存 member。
func encodeHistoryRecord(r retained) (string, error) {
	b, err := json.Marshal(historyRecord{Seq: r.seq, Hostname: r.ev.Hostname, Item: r.ev.Item})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
