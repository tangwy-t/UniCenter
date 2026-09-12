package lock

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// unlockScript is a Lua script that atomically checks the lock owner
// before releasing the lock. It only deletes the key if the value
// matches the expected owner, preventing accidental lock release.
const unlockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`

type RedisLocker struct {
	client goredis.UniversalClient
}

// NewLocker creates a Locker backed by the given Redis client.
func NewLocker(client goredis.UniversalClient) *RedisLocker {
	return &RedisLocker{client: client}
}

// TryLock attempts to acquire a distributed lock using Redis SETNX.
// Returns true if the lock was acquired, false if it was already held.
func (l *RedisLocker) TryLock(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error) {
	return l.client.SetNX(ctx, key, owner, ttl).Result()
}

// Unlock releases the lock only if the current owner matches.
// Uses a Lua script for atomic check-and-delete.
func (l *RedisLocker) Unlock(ctx context.Context, key string, owner string) error {
	_, err := l.client.Eval(ctx, unlockScript, []string{key}, owner).Result()
	return err
}
