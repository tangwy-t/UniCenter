package session

import (
	"context"
	"time"
)

type CacheStoreInterface interface {
	// Get 返回 key 对应的值;key 不存在时返回 ("", nil),不返回错误。
	// miss 语义由实现方保证(RedisStore 将 goredis.Nil 翻译为空串),
	// 消费方以空串判定 miss,不得假设会收到 goredis.Nil。
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	// DeleteByPattern 按 glob 模式批量删除 key(受 maxCount 上限保护),
	// 由 RedisStore 实现;用于低频管理操作的全量缓存失效。
	DeleteByPattern(ctx context.Context, pattern string, maxCount int64) (int64, error)
	// CompareAndSwap 仅当 key 的当前值等于 old 时才写入 new 并返回 true;
	// key 不存在或其值不等于 old 时不做任何修改并返回 false。
	//
	// 用于需要"读-判断-写"原子性的场景:refresh token 轮换必须保证
	// 同一 refresh token 只能被兑换一次。先 Get 再 Delete 的两步写法在
	// 并发下会双放行(两个请求都读到同一个旧 token),CAS 是唯一正确做法。
	CompareAndSwap(ctx context.Context, key, old, new string, ttl time.Duration) (bool, error)
	// SetAdd 向 key 对应的集合原子追加一个成员,并刷新 TTL。用于用户
	// access token 反向索引:并发登录时 SADD 天然原子,不会像 Get+append+Set
	// 那样互相覆盖丢失索引。key 首次写入时自动创建为集合。
	SetAdd(ctx context.Context, key, member string, ttl time.Duration) error
	// SetMembers 读取 key 对应集合的全部成员;key 不存在返回空切片。
	SetMembers(ctx context.Context, key string) ([]string, error)
	// SetRemove 从 key 对应的集合原子移除一个成员;key/成员不存在为无操作。
	SetRemove(ctx context.Context, key, member string) error
	// MGet 批量读取多个字符串 key;缺失的 key 返回空串(与 Get 的 miss 语义一致)。
	MGet(ctx context.Context, keys ...string) ([]string, error)
	// ScanKeyNames 按 glob 模式扫描并返回命中的 key 名,受 maxCount 上限保护;
	// 用于低频管理操作(如枚举在线会话索引键),不存在则返回空切片。
	ScanKeyNames(ctx context.Context, pattern string, maxCount int64) ([]string, error)
}
