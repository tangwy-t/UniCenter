package service

import (
	"context"
	"errors"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
)

// CacheStoreInterface 定义在消费方:service 只用到缓存管理的四个读删操作。
type CacheStoreInterface interface {
	ScanKeys(ctx context.Context, pattern string, cursor uint64, count int64) ([]cache.KeyInfo, uint64, error)
	DeleteByPattern(ctx context.Context, pattern string, maxCount int64) (int64, error)
	GetValuePage(ctx context.Context, key string, opt cache.ValuePageOption) (*cache.ValuePage, error)
	GetStats(ctx context.Context) (*cache.Stats, error)
}

// CacheService 承担缓存管理的 HTTP 语义翻译与日志:此前 handler 直连
// infra store 自行翻译,与其余"翻译在 service 层"的契约不一致。
type CacheService struct {
	store  CacheStoreInterface
	logger logger.LoggerInterface
}

func NewCacheService(store CacheStoreInterface, logger logger.LoggerInterface) *CacheService {
	return &CacheService{store: store, logger: logger}
}

func (s *CacheService) ListKeys(ctx context.Context, req *request.ListKeysRequest) (*response.ListKeysResponse, error) {
	keys, nextCursor, err := s.store.ScanKeys(ctx, req.Prefix, req.Cursor, req.Count)
	if err != nil {
		s.logger.Error("CacheService.ListKeys failed", zap.Error(err))
		return nil, apperror.Internal("查询缓存 key 失败", err)
	}
	out := make([]response.CacheKeyInfo, len(keys))
	for i, k := range keys {
		out[i] = response.CacheKeyInfo{Key: k.Key, Type: k.Type}
	}
	return &response.ListKeysResponse{Keys: out, Cursor: nextCursor}, nil
}

func (s *CacheService) GetKeyValue(ctx context.Context, req *request.GetKeyValueRequest) (*response.CacheValuePage, error) {
	page, err := s.store.GetValuePage(ctx, req.Key, cache.ValuePageOption{
		Offset: req.Offset,
		Limit:  req.Limit,
		Cursor: req.Cursor,
	})
	if err != nil {
		// limit 超过该类型上限(集合 200 条):参数错误而非内部错误
		if errors.Is(err, cache.ErrBadLimit) {
			return nil, apperror.BadRequest("参数错误")
		}
		s.logger.Error("CacheService.GetKeyValue failed", zap.Error(err))
		return nil, apperror.Internal("查询缓存值失败", err)
	}
	// TTL=-2 means key does not exist
	if page.TTL == -2 {
		return nil, apperror.NotFound("key 不存在: " + req.Key)
	}
	// 安全修复（评审 #2）：缓存接口读 config:values 共享 hash 时，对敏感字段
	// （sys.jwt.[FUNC] 等）做与 config 管理接口一致的掩码，堵住"掩码被第二条
	// 读路径绕过"的密钥泄露。此前 GetValuePage 裸读 hash 字段，持
	// system:cache:query 的账号可读出当前 HMAC 签名密钥并伪造任意 token。
	value := maskCacheValue(page.Key, page.Type, page.Value)
	return &response.CacheValuePage{
		Key:        page.Key,
		Type:       page.Type,
		TTL:        page.TTL,
		Total:      page.Total,
		Start:      page.Start,
		HasMore:    page.HasMore,
		NextCursor: page.NextCursor,
		Truncated:  page.Truncated,
		Value:      value,
	}, nil
}

// maskCacheValue 对缓存页内容中的敏感字段值做掩码。
// hash 类型的 Value 为 []cache.HashEntry（field=配置键），按 field 匹配
// 敏感键正则；string 类型仅当 key 本身为敏感键（如直接读 "access:<token>"
// 之外的敏感 string 键）时掩码。其余类型（list/set/zset）不掩码，保持语义。
func maskCacheValue(key, typ string, value any) any {
	switch typ {
	case "hash":
		entries, ok := value.([]cache.HashEntry)
		if !ok {
			return value
		}
		out := make([]cache.HashEntry, len(entries))
		copy(out, entries)
		for i := range out {
			if util.IsSensitiveConfigKey(out[i].Field) {
				out[i].Value = util.MaskedValue
			}
		}
		return out
	case "string":
		s, ok := value.(string)
		if !ok {
			return value
		}
		if util.IsSensitiveConfigKey(key) {
			return util.MaskedValue
		}
		return s
	default:
		return value
	}
}

func (s *CacheService) DeleteKeys(ctx context.Context, req *request.DeleteKeysRequest) (*response.DeleteKeysResponse, error) {
	deleted, err := s.store.DeleteByPattern(ctx, req.Prefix, req.MaxCount)
	if err != nil {
		s.logger.Error("CacheService.DeleteKeys failed", zap.Error(err))
		return nil, apperror.Internal("删除缓存 key 失败", err)
	}
	return &response.DeleteKeysResponse{Deleted: deleted}, nil
}

func (s *CacheService) GetStats(ctx context.Context) (*cache.Stats, error) {
	stats, err := s.store.GetStats(ctx)
	if err != nil {
		s.logger.Error("CacheService.GetStats failed", zap.Error(err))
		return nil, apperror.Internal("查询缓存统计失败", err)
	}
	return stats, nil
}
