package middleware

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"time"
)

type ConfigGetterInterface interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
	GetBool(ctx context.Context, key string, defaultVal bool) bool
}

type TokenStoreInterface interface {
	IsAccessValid(ctx context.Context, token string) (bool, error)
}

type OperationLogServiceInterface interface {
	Create(ctx context.Context, log *entity.SysOperationLog) error
}

type AuthServiceInterface interface {
	GetUserPermissions(ctx context.Context, userID uint64) ([]string, error)
}

type SessionStoreInterface interface {
	LoadPerms(ctx context.Context, userID uint64, scopeKey string) ([]string, error)
	StorePerms(ctx context.Context, userID uint64, scopeKey string, perms []string, ttl time.Duration) error
}
