package datascope

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// ScopeResolver 是 scope 解析器，持有各维度 resolver 的映射。
type ScopeResolver struct {
	Resolvers map[string]DimensionResolver
	Logger    logger.LoggerInterface
}

// NewScopeResolver 创建一个 ScopeResolver。
func NewScopeResolver(resolvers []DimensionResolver, log logger.LoggerInterface) *ScopeResolver {
	m := make(map[string]DimensionResolver)
	for _, r := range resolvers {
		m[r.DimensionType()] = r
	}
	return &ScopeResolver{Resolvers: m, Logger: log}
}
