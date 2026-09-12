package cache

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisStore implements both StringStore and HashStore backed by Redis.
type RedisStore struct {
	client goredis.UniversalClient
}

// NewStore creates a RedisStore implementing both StringStore and HashStore.
func NewStore(client goredis.UniversalClient) *RedisStore {
	return &RedisStore{client: client}
}

// Stats holds Redis keyspace hit/miss statistics.
type Stats struct {
	KeyspaceHits   int64   `json:"keyspace_hits"`
	KeyspaceMisses int64   `json:"keyspace_misses"`
	HitRate        float64 `json:"hit_rate"`
}

// ZSetMember represents a member in a sorted set with its score.
type ZSetMember struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

// ── ValuePage 分页取值 ────────────────────────────────────────────────

// 分页上限:集合类型(list/zset/set/hash)每页条数上限,与 string 单次字节窗上限。
// limit 的全局 binding 上限(4194304 = 4MB)在 handler 层;集合类 200 条的上限
// 因依赖 type 无法用 tag 表达,由本轮 ErrBadLimit 哨兵错误承接。
const (
	MaxCollectionPageSize = 200
	MaxStringFetchBytes   = 4 << 20 // 4MB
)

// ErrBadLimit 表示 limit 超出该 value 类型的上限;service 层翻译为 400。
var ErrBadLimit = errors.New("cache: limit 超出该类型上限")

// ValuePageOption 分页取值选项:list/zset 用 Offset+Limit,set/hash 用 Cursor+Limit,
// string 用 Offset(起始字节)+Limit(字节窗)。按类型路由在 GetValuePage 内部完成。
type ValuePageOption struct {
	Offset int64
	Limit  int64
	Cursor uint64
}

// HashEntry 哈希字段条目:分页扫描保留扫描顺序。JSON 契约 [{field,value}] 小写字段名(与 ZSetMember 一致)。
type HashEntry struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// ValuePage 单页值 + 分页元信息。
type ValuePage struct {
	Key  string
	Type string
	TTL  int64
	// Total 集合总条数（LLen/ZCard/SCard/HLen）或 string 总字节数（StrLen）
	Total int64
	// Start 本页首元素偏移：list/zset 为下标，string 为字节；set/hash 恒 0（前端自算累计）
	Start int64
	// HasMore 是否还有后续数据（string 语义等同 Truncated）
	HasMore bool
	// NextCursor 仅 set/hash 有效；游标归零即翻完
	NextCursor uint64
	// Truncated 仅 string：本窗口之后仍有内容
	Truncated bool
	// Value 当前页内容：list/set → []string，zset → []ZSetMember，hash → []HashEntry，string → string
	Value any
}

// checkCollectionLimit 集合类型页大小校验。
func checkCollectionLimit(limit int64) error {
	if limit > MaxCollectionPageSize {
		return ErrBadLimit
	}
	return nil
}

// ttlSeconds 把 TTL 回包换算为秒级语义(-1 永不过期,-2 不存在)。
// 执行期修订 R8:go-redis v9.21.0 对 -1/-2 存裸纳秒值,直接 ttl.Seconds() 会把 -1 截成 0;
// 此处显式映射(兼容秒级负值的防御分支)。
func ttlSeconds(ttl time.Duration) int64 {
	switch {
	case ttl == -1 || ttl == -1*time.Second:
		return -1
	case ttl == -2 || ttl == -2*time.Second:
		return -2
	default:
		return int64(ttl / time.Second)
	}
}

// toZSetMembers 把 go-redis 的 Z 转成分页条目,异常类型成员跳过而非 panic。
func toZSetMembers(zs []goredis.Z) []ZSetMember {
	members := make([]ZSetMember, 0, len(zs))
	for _, z := range zs {
		member, ok := z.Member.(string)
		if !ok {
			continue
		}
		members = append(members, ZSetMember{Member: member, Score: z.Score})
	}
	return members
}

// safeEnd 计算区间右端(含)。执行期修订 R7:offset 极大时 offset+limit-1 可能回绕为负,
// 被 Redis 当作负下标从尾部取值;此处饱和钳制为 MaxInt64,保证最多取到尾部。
func safeEnd(offset, limit int64) int64 {
	end := offset + limit - 1
	if end < offset {
		return math.MaxInt64
	}
	return end
}

// KeyInfo holds a key's name and its Redis value type.
type KeyInfo struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

// -- StringStore methods --

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *RedisStore) Del(ctx context.Context, keys ...string) error {
	return s.client.Del(ctx, keys...).Err()
}

func (s *RedisStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	return s.client.TTL(ctx, key).Result()
}

// -- HashStore methods --

func (s *RedisStore) HGet(ctx context.Context, key, field string) (string, error) {
	val, err := s.client.HGet(ctx, key, field).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (s *RedisStore) HSet(ctx context.Context, key, field, value string) error {
	return s.client.HSet(ctx, key, field, value).Err()
}

func (s *RedisStore) HDel(ctx context.Context, key string, fields ...string) error {
	return s.client.HDel(ctx, key, fields...).Err()
}

func (s *RedisStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return s.client.HGetAll(ctx, key).Result()
}

func (s *RedisStore) HMSet(ctx context.Context, key string, fields map[string]string) error {
	return s.client.HMSet(ctx, key, fields).Err()
}

func (s *RedisStore) ScanKeys(ctx context.Context, pattern string, cursor uint64, count int64) ([]KeyInfo, uint64, error) {
	keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, count).Result()
	if err != nil {
		return nil, 0, err
	}

	// 用一次 pipeline 批量获取每个 key 的类型，避免逐条往返
	pipe := s.client.Pipeline()
	typeCmds := make([]*goredis.StatusCmd, len(keys))
	for i, k := range keys {
		typeCmds[i] = pipe.Type(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, 0, err
	}

	result := make([]KeyInfo, len(keys))
	for i, k := range keys {
		t := ""
		if typeCmds[i] != nil {
			t = typeCmds[i].Val()
		}
		result[i] = KeyInfo{Key: k, Type: t}
	}
	return result, nextCursor, nil
}

// ── GetValuePage:分页取值 ─────────────────────────────────────────────

// GetValuePage 按 value 类型分页获取单页内容与元信息。
// list/zset 用 Offset+Limit 区间,set/hash 用 Cursor+Limit 游标,
// string 用 Offset+Limit 字节窗。key 不存在返回 TTL=-2。
func (s *RedisStore) GetValuePage(ctx context.Context, key string, opt ValuePageOption) (*ValuePage, error) {
	// Determine type first
	keyType, err := s.client.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	page := &ValuePage{Key: key, Type: keyType}

	// Get TTL —— go-redis v9.21.0 对 -1/-2 存裸纳秒值,负值语义统一由 ttlSeconds 映射
	ttl, err := s.client.TTL(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	page.TTL = ttlSeconds(ttl)

	// key does not exist
	if keyType == "none" {
		page.TTL = -2
		return page, nil
	}

	limit := opt.Limit
	if limit <= 0 {
		limit = 100
	}

	switch keyType {
	case "string":
		return s.getStringPage(ctx, key, page, opt.Offset, limit)
	case "list":
		return s.getListPage(ctx, key, page, opt.Offset, limit)
	case "zset":
		return s.getZSetPage(ctx, key, page, opt.Offset, limit)
	case "set":
		return s.getSetPage(ctx, key, page, opt.Cursor, limit)
	case "hash":
		return s.getHashPage(ctx, key, page, opt.Cursor, limit)
	default: // stream 等暂不支持在线查看
		return page, nil
	}
}

func (s *RedisStore) getStringPage(ctx context.Context, key string, page *ValuePage, offset, limit int64) (*ValuePage, error) {
	if limit > MaxStringFetchBytes {
		return nil, ErrBadLimit
	}
	total, err := s.client.StrLen(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	val, err := s.client.GetRange(ctx, key, offset, safeEnd(offset, limit)).Result()
	if err != nil {
		return nil, err
	}
	page.Total = total
	page.Start = offset
	page.Value = val
	page.Truncated = offset+int64(len(val)) < total
	page.HasMore = page.Truncated
	return page, nil
}

func (s *RedisStore) getListPage(ctx context.Context, key string, page *ValuePage, offset, limit int64) (*ValuePage, error) {
	if err := checkCollectionLimit(limit); err != nil {
		return nil, err
	}
	total, err := s.client.LLen(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	vals, err := s.client.LRange(ctx, key, offset, safeEnd(offset, limit)).Result()
	if err != nil {
		return nil, err
	}
	page.Total = total
	page.Start = offset
	page.Value = vals
	page.HasMore = offset+int64(len(vals)) < total
	return page, nil
}

func (s *RedisStore) getZSetPage(ctx context.Context, key string, page *ValuePage, offset, limit int64) (*ValuePage, error) {
	if err := checkCollectionLimit(limit); err != nil {
		return nil, err
	}
	total, err := s.client.ZCard(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	zs, err := s.client.ZRangeWithScores(ctx, key, offset, safeEnd(offset, limit)).Result()
	if err != nil {
		return nil, err
	}
	members := toZSetMembers(zs)
	page.Total = total
	page.Start = offset
	page.Value = members
	page.HasMore = offset+int64(len(members)) < total
	return page, nil
}

// getSetPage SSCAN 游标分页:内部循环 SCAN 直到集满一页(或游标归零)。
// 说明:SCAN 的 COUNT 只是提示,单次调用可能返回不足一页;循环集齐保证
// 页内条数稳定(偶发场景下最后一次调用可能让本页略超 limit,属可接受)。
func (s *RedisStore) getSetPage(ctx context.Context, key string, page *ValuePage, cursor uint64, limit int64) (*ValuePage, error) {
	if err := checkCollectionLimit(limit); err != nil {
		return nil, err
	}
	total, err := s.client.SCard(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	acc := make([]string, 0, limit)
	for {
		batch, next, err := s.client.SScan(ctx, key, cursor, "", limit).Result()
		if err != nil {
			return nil, err
		}
		acc = append(acc, batch...)
		cursor = next
		if cursor == 0 || int64(len(acc)) >= limit {
			break
		}
	}
	page.Total = total
	page.HasMore = cursor != 0
	page.NextCursor = cursor
	page.Value = acc
	return page, nil
}

// getHashPage HSCAN 游标分页:与 getSetPage 同构,返回有序 HashEntry 条目。
func (s *RedisStore) getHashPage(ctx context.Context, key string, page *ValuePage, cursor uint64, limit int64) (*ValuePage, error) {
	if err := checkCollectionLimit(limit); err != nil {
		return nil, err
	}
	total, err := s.client.HLen(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	acc := make([]HashEntry, 0, limit)
	for {
		batch, next, err := s.client.HScan(ctx, key, cursor, "", limit).Result()
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(batch); i += 2 {
			acc = append(acc, HashEntry{Field: batch[i], Value: batch[i+1]})
		}
		cursor = next
		if cursor == 0 || int64(len(acc)) >= limit {
			break
		}
	}
	page.Total = total
	page.HasMore = cursor != 0
	page.NextCursor = cursor
	page.Value = acc
	return page, nil
}

// ── DeleteByPattern ───────────────────────────────────────────────────

func (s *RedisStore) DeleteByPattern(ctx context.Context, pattern string, maxCount int64) (int64, error) {
	var deleted int64
	var cursor uint64

	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return deleted, err
		}

		for _, key := range keys {
			if deleted >= maxCount {
				return deleted, nil
			}
			// UNLINK one key at a time to avoid CROSSSLOT in cluster mode
			if err := s.client.Unlink(ctx, key).Err(); err != nil {
				return deleted, err
			}
			deleted++
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return deleted, nil
}

func (s *RedisStore) GetStats(ctx context.Context) (*Stats, error) {
	info, err := s.client.Info(ctx, "stats").Result()
	if err != nil {
		return nil, err
	}

	stats := &Stats{}
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "keyspace_hits":
			stats.KeyspaceHits, _ = strconv.ParseInt(parts[1], 10, 64)
		case "keyspace_misses":
			stats.KeyspaceMisses, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}

	total := stats.KeyspaceHits + stats.KeyspaceMisses
	if total > 0 {
		stats.HitRate = float64(stats.KeyspaceHits) / float64(total) * 100
	}

	return stats, nil
}

// IncrWithTTL atomically increments a key and REFRESHES its expiration on
// every call (sliding TTL). If the key does not exist, it starts at 1.
// Use for counters where sustained activity should extend the window — e.g.
// login-fail counting: a slow brute-forcer who keeps failing must not slip
// out of the lock window by spacing attempts.
// For fixed-window rate limiting use FixedWindowIncrWithTTL instead.
func (s *RedisStore) IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	script := `local c = redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], ARGV[1])
return c`
	return s.client.Eval(ctx, script, []string{key}, int(ttl.Seconds())).Int64()
}

// FixedWindowIncrWithTTL atomically increments a counter and sets its
// expiration ONLY on the first increment (fixed window). Unlike IncrWithTTL,
// subsequent increments do not extend the TTL, so "N per window" means a
// true fixed window instead of "N since the last request".
// Used by captcha rate limiting ("10 per minute").
func (s *RedisStore) FixedWindowIncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	script := `local c = redis.call('INCR', KEYS[1])
if c == 1 then
	redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return c`
	return s.client.Eval(ctx, script, []string{key}, int(ttl.Seconds())).Int64()
}

// SlidingWindowIncr implements sliding-window rate limiting via a Lua script.
// keys[0] = previous window key, keys[1] = current window key.
// ttl is the expiration in seconds for the current window key.
// Returns []any{prevCount, currCount}.
func (s *RedisStore) SlidingWindowIncr(ctx context.Context, keys []string, ttl int) (any, error) {
	script := `local prev = redis.call('GET', KEYS[1])
if prev == false then
	prev = 0
else
	prev = tonumber(prev)
end
local curr = redis.call('INCR', KEYS[2])
if curr == 1 then
	redis.call('EXPIRE', KEYS[2], ARGV[1])
end
return {prev, curr}`
	return s.client.Eval(ctx, script, keys, ttl).Result()
}

// ── CompareAndSwap ────────────────────────────────────────────────────

// casScript 在一个 Lua 脚本内完成"比对 + 写入",保证原子性。
//
// 不能用 GET 后 SET 的两步写法:两步之间其他客户端可以插入写入,
// 两个并发的 refresh 请求会都读到同一个旧 token 从而双双放行
// (refresh token 重放)。Redis 单线程执行 Lua,天然是原子的。
//
// KEYS[1] = key, ARGV[1] = old, ARGV[2] = new, ARGV[3] = ttl 秒
// 返回 1 表示已替换,0 表示值不匹配(未修改)。
var casScript = goredis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur == false or cur ~= ARGV[1] then
  return 0
end
local ttl = tonumber(ARGV[3])
if ttl > 0 then
  redis.call('SET', KEYS[1], ARGV[2], 'EX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[2])
end
return 1
`)

// CompareAndSwap 仅当 key 当前值等于 old 时写入 new,返回是否发生替换。
func (s *RedisStore) CompareAndSwap(ctx context.Context, key, old, new string, ttl time.Duration) (bool, error) {
	res, err := casScript.Run(ctx, s.client, []string{key}, old, new, int64(ttl.Seconds())).Int()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, err
	}
	return res == 1, nil
}

// SetAdd 向 key 对应的集合原子追加成员并刷新 TTL。
// SADD 在 Redis 单线程内原子完成,并发追加不会丢失;TTL 通过 Expire
// 在 SADD 后单独设置(集合首次创建时无 TTL,每次追加后统一刷新)。
func (s *RedisStore) SetAdd(ctx context.Context, key, member string, ttl time.Duration) error {
	if err := s.client.SAdd(ctx, key, member).Err(); err != nil {
		return err
	}
	if ttl > 0 {
		return s.client.Expire(ctx, key, ttl).Err()
	}
	return nil
}

// SetMembers 读取 key 对应集合的全部成员;key 不存在返回空切片。
func (s *RedisStore) SetMembers(ctx context.Context, key string) ([]string, error) {
	return s.client.SMembers(ctx, key).Result()
}

// SetRemove 从集合原子移除一个成员（SREM）。
func (s *RedisStore) SetRemove(ctx context.Context, key, member string) error {
	return s.client.SRem(ctx, key, member).Err()
}

// MGet 批量读取多个字符串 key；缺失的 key 返回空串（对齐 Get 的 miss 语义：
// goredis.Nil 翻译为空串，消费方不得假设收到 goredis.Nil）。
func (s *RedisStore) MGet(ctx context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return []string{}, nil
	}
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		if str, ok := v.(string); ok {
			out[i] = str
		} // 其余（nil）保持空串
	}
	return out, nil
}

// ScanKeyNames 按 pattern 扫描并返回纯 key 名。cursor 循环收集，受 maxCount
// 上限保护；数据量大时截断（用于低频管理操作，如枚举在线会话索引键）。
func (s *RedisStore) ScanKeyNames(ctx context.Context, pattern string, maxCount int64) ([]string, error) {
	var result []string
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			result = append(result, k)
			if int64(len(result)) >= maxCount {
				return result, nil
			}
		}
		if next == 0 {
			return result, nil
		}
		cursor = next
	}
}
