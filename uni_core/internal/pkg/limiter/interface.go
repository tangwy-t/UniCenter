package limiter

import "context"

type ConfigGetterInterface interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
	GetBool(ctx context.Context, key string, defaultVal bool) bool
}

type CacheStoreInterface interface {
	SlidingWindowIncr(ctx context.Context, keys []string, ttl int) (any, error)
}
