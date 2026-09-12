package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// 本文件是「JWT 密钥经缓存管理接口泄露」（评审报告 #2）修复后的回归验证。
//
// 修复内容：CacheService.GetKeyValue 读 config:values hash 时，对敏感字段
// （匹配 sensitiveConfigKeyRe 的 field）做与 config 管理接口一致的掩码，
// 堵住"掩码被第二条读路径绕过"的密钥泄露。

// pocHashStore 记录 HSet 调用，用于验证 configWarm 写入共享 hash。
type pocHashStore struct {
	hset [][3]string // [key, field, value]
}

func (s *pocHashStore) HSet(_ context.Context, key, field, value string) error {
	s.hset = append(s.hset, [3]string{key, field, value})
	return nil
}
func (s *pocHashStore) HGet(_ context.Context, key, field string) (string, error)  { return "", nil }
func (s *pocHashStore) HDel(_ context.Context, key string, fields ...string) error { return nil }

// pocCacheStore 返回一个含明文密钥的 ValuePage，用于验证 GetKeyValue 掩码。
type pocCacheStore struct{ val *cache.ValuePage }

func (s *pocCacheStore) ScanKeys(context.Context, string, uint64, int64) ([]cache.KeyInfo, uint64, error) {
	return nil, 0, nil
}
func (s *pocCacheStore) DeleteByPattern(context.Context, string, int64) (int64, error) { return 0, nil }
func (s *pocCacheStore) GetValuePage(context.Context, string, cache.ValuePageOption) (*cache.ValuePage, error) {
	return s.val, nil
}
func (s *pocCacheStore) GetStats(context.Context) (*cache.Stats, error) { return nil, nil }

// 证据①：config 管理接口判定敏感键并掩码（与缓存接口一致）。
func TestPoc_ConfigApiMasksSensitiveKey(t *testing.T) {
	key := "sys.jwt.secret"
	if !util.IsSensitiveConfigKey(key) {
		t.Fatalf("%s 应被判定为敏感键", key)
	}
	if got := util.MaskIfSensitive(key, "actual-secret"); got != util.MaskedValue {
		t.Fatalf("config API 应掩码 %s，实际返回 %q", key, got)
	}
}

// 证据②：configWarm 把明文密钥写进共享 hash（内部缓存，读取时再掩码）。
func TestPoc_ConfigWarmStoresRawSecretInSharedHash(t *testing.T) {
	rec := &pocHashStore{}
	svc := NewConfigService(nil, rec, nil, logger.NewNop())
	svc.configWarm(context.Background(), "sys.jwt.secret", "actual-secret-value")

	if len(rec.hset) != 1 {
		t.Fatalf("configWarm 应 HSet 一次，实际 %d 次", len(rec.hset))
	}
	c := rec.hset[0]
	if c[0] != ConfigHashKey || c[1] != "sys.jwt.secret" || c[2] != "actual-secret-value" {
		t.Fatalf("configWarm 应把密钥写入 %q，实际 %+v", ConfigHashKey, c)
	}
}

// 证据③（修复后）：缓存管理接口读 config:values 时对敏感字段掩码，
// 不再明文透传密钥；非敏感字段保持原值。
func TestPoc_CacheEndpointMasksSensitiveField(t *testing.T) {
	rec := &pocCacheStore{val: &cache.ValuePage{
		Key: "config:values", Type: "hash", TTL: 1000,
		Value: []cache.HashEntry{
			{Field: "sys.jwt.secret", Value: "actual-secret-value"},
			{Field: "sys.app.name", Value: "webmanager"},
		},
	}}
	svc := NewCacheService(rec, logger.NewNop())

	resp, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{Key: "config:values", Limit: 100})
	if err != nil {
		t.Fatalf("GetKeyValue 失败: %v", err)
	}
	entries, ok := resp.Value.([]cache.HashEntry)
	if !ok {
		t.Fatalf("hash 类型 Value 应为 []cache.HashEntry，实际 %T", resp.Value)
	}
	for _, e := range entries {
		switch e.Field {
		case "sys.jwt.secret":
			if e.Value != util.MaskedValue {
				t.Fatalf("缓存接口应掩码 sys.jwt.secret，实际 %q", e.Value)
			}
		case "sys.app.name":
			if e.Value != "webmanager" {
				t.Fatalf("非敏感字段不应被掩码，实际 %q", e.Value)
			}
		}
	}
}
