package captcha

import (
	"context"
	"time"
)

type ConfigGetterInterface interface {
	GetInt(ctx context.Context, key string, defaultVal int) int
}

type CacheStoreInterface interface {
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
	TTL(ctx context.Context, key string) (time.Duration, error)
	IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
	FixedWindowIncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
}
