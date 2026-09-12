package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"
)

// ── 测试替身 ────────────────────────────────────────────

type stubCacheStore struct {
	page *cache.ValuePage
	err  error
	got  cache.ValuePageOption
}

func (s *stubCacheStore) ScanKeys(context.Context, string, uint64, int64) ([]cache.KeyInfo, uint64, error) {
	return nil, 0, nil
}
func (s *stubCacheStore) DeleteByPattern(context.Context, string, int64) (int64, error) {
	return 0, nil
}
func (s *stubCacheStore) GetValuePage(_ context.Context, _ string, opt cache.ValuePageOption) (*cache.ValuePage, error) {
	s.got = opt
	return s.page, s.err
}
func (s *stubCacheStore) GetStats(context.Context) (*cache.Stats, error) { return nil, nil }

func wantErrCode(t *testing.T, err error, code int) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, 不是 AppError", err)
	}
	if ae.Code != code {
		t.Fatalf("err.Code = %d, want %d", ae.Code, code)
	}
}

func TestCacheServiceGetKeyValueMapsPage(t *testing.T) {
	store := &stubCacheStore{page: &cache.ValuePage{
		Key: "job:logs", Type: "list", TTL: -1, Total: 30000, Start: 200,
		HasMore: true, NextCursor: 0, Truncated: false,
		Value: []string{"a", "b"},
	}}
	svc := NewCacheService(store, logger.NewNop())

	got, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{
		Key: "job:logs", Offset: 200, Limit: 100,
	})
	if err != nil {
		t.Fatalf("GetKeyValue err: %v", err)
	}
	if store.got.Offset != 200 || store.got.Limit != 100 || store.got.Cursor != 0 {
		t.Fatalf("store got opt = %+v, want Offset 200/Limit 100/Cursor 0", store.got)
	}
	if got.Key != "job:logs" || got.Type != "list" || got.TTL != -1 ||
		got.Total != 30000 || got.Start != 200 || !got.HasMore {
		t.Fatalf("dto = %+v", got)
	}
	if v, ok := got.Value.([]string); !ok || len(v) != 2 || v[0] != "a" {
		t.Fatalf("dto.Value = %#v", got.Value)
	}
}

func TestCacheServiceGetKeyValueHashEntryJSONContract(t *testing.T) {
	// 回归(验收现场发现):hash 条目经 HTTP 序列化必须是小写 {field,value},
	// 前端 HashEntry 契约按小写键过滤,否则弹窗空白。
	store := &stubCacheStore{page: &cache.ValuePage{
		Key: "job:cfg", Type: "hash", TTL: -1, Total: 2,
		Value: []cache.HashEntry{{Field: "f-1", Value: "v-1"}},
	}}
	svc := NewCacheService(store, logger.NewNop())

	got, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{Key: "job:cfg", Limit: 100})
	if err != nil {
		t.Fatalf("GetKeyValue err: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"field":"f-1"`) || !strings.Contains(s, `"value":"v-1"`) {
		t.Fatalf("hash 条目 JSON 未按小写契约输出: %s", s)
	}
	if strings.Contains(s, `"Field"`) || strings.Contains(s, `"Value"`) {
		t.Fatalf("hash 条目泄漏 Go 字段名: %s", s)
	}
}

func TestCacheServiceGetKeyValueNotFound(t *testing.T) {
	store := &stubCacheStore{page: &cache.ValuePage{Key: "gone", Type: "none", TTL: -2}}
	svc := NewCacheService(store, logger.NewNop())

	_, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{Key: "gone", Limit: 100})
	wantErrCode(t, err, apperror.CodeNotFound)
}

func TestCacheServiceGetKeyValueBadLimit(t *testing.T) {
	store := &stubCacheStore{err: cache.ErrBadLimit}
	svc := NewCacheService(store, logger.NewNop())

	_, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{Key: "k", Limit: 201})
	wantErrCode(t, err, apperror.CodeBadRequest)
}

func TestCacheServiceGetKeyValueInternal(t *testing.T) {
	store := &stubCacheStore{err: errors.New("redis boom")}
	svc := NewCacheService(store, logger.NewNop())

	_, err := svc.GetKeyValue(context.Background(), &request.GetKeyValueRequest{Key: "k", Limit: 100})
	wantErrCode(t, err, apperror.CodeInternal)
}
