package session

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCache 是进程内 CacheStoreInterface 替身:Get 对缺失键返回 ("", nil),
// 与 RedisStore 的 miss 翻译语义一致。
type fakeCache struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[string]string{}} }

func (f *fakeCache) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[key], nil
}

func (f *fakeCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
	return nil
}

func (f *fakeCache) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.data, k)
	}
	return nil
}

func (f *fakeCache) DeleteByPattern(_ context.Context, pattern string, _ int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := strings.TrimSuffix(pattern, "*")
	var n int64
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}

// CompareAndSwap 模拟 Redis Lua 脚本的原子语义:比对与写入在同一把锁内
// 完成,并发调用不会交错 —— 这是 fake 必须与真实实现语义一致的关键点,
// 否则会在假实现上"通过"而线上仍然重放。
func (f *fakeCache) CompareAndSwap(_ context.Context, key, old, new string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || cur != old {
		return false, nil
	}
	f.data[key] = new
	return true, nil
}

// SetAdd 用 JSON 数组模拟 Redis 集合:追加成员(去重)。
// 与真实 RedisStore 的 SADD 语义对齐(成员去重、原子追加)。
func (f *fakeCache) SetAdd(_ context.Context, key, member string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var members []string
	if raw, ok := f.data[key]; ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &members)
	}
	for _, m := range members {
		if m == member {
			return nil // 已存在,幂等
		}
	}
	members = append(members, member)
	raw, _ := json.Marshal(members)
	f.data[key] = string(raw)
	return nil
}

// SetMembers 返回 JSON 数组形式的集合成员;key 不存在返回空切片。
func (f *fakeCache) SetMembers(_ context.Context, key string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.data[key]
	if !ok || raw == "" {
		return nil, nil
	}
	var members []string
	_ = json.Unmarshal([]byte(raw), &members)
	return members, nil
}

// SetRemove 模拟 SREM:从 JSON 数组集合移除成员。
func (f *fakeCache) SetRemove(_ context.Context, key, member string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var members []string
	if raw, ok := f.data[key]; ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &members)
	}
	out := members[:0]
	for _, m := range members {
		if m != member {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		delete(f.data, key)
		return nil
	}
	raw, _ := json.Marshal(out)
	f.data[key] = string(raw)
	return nil
}

// MGet 模拟批量读取:缺失键返回空串。
func (f *fakeCache) MGet(_ context.Context, keys ...string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = f.data[k]
	}
	return out, nil
}

// ScanKeyNames 模拟 SCAN:按前缀匹配返回 key 名,受 maxCount 上限。
func (f *fakeCache) ScanKeyNames(_ context.Context, pattern string, maxCount int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := strings.TrimSuffix(pattern, "*")
	var out []string
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
			if int64(len(out)) >= maxCount {
				break
			}
		}
	}
	return out, nil
}

// TestAccessWhitelistRoundTrip 会话白名单:存→有效→吊销→无效。
func TestAccessWhitelistRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	if err := s.StoreAccess(ctx, "tok-1", 7, time.Minute); err != nil {
		t.Fatalf("StoreAccess: %v", err)
	}
	ok, err := s.IsAccessValid(ctx, "tok-1")
	if err != nil || !ok {
		t.Fatalf("IsAccessValid = %v/%v, want true/nil", ok, err)
	}
	// miss = 已登出/吊销/过期 → false
	ok, err = s.IsAccessValid(ctx, "tok-missing")
	if err != nil || ok {
		t.Fatalf("miss IsAccessValid = %v/%v, want false/nil(白名单缺失必须判无效)", ok, err)
	}
	if err := s.RevokeAll(ctx, 7, "tok-1"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	ok, _ = s.IsAccessValid(ctx, "tok-1")
	if ok {
		t.Fatal("RevokeAll 后 token 仍有效")
	}
}

// TestPermsCacheRoundTrip 权限缓存:JSON 数组存取闭环 + 回源 miss 语义。
func TestPermsCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewSession(newFakeCache())

	perms, err := s.LoadPerms(ctx, 7, "")
	if err != nil || perms != nil {
		t.Fatalf("miss LoadPerms = %v/%v, want nil/nil(消费方回源 DB)", perms, err)
	}
	if err := s.StorePerms(ctx, 7, "", []string{"a", "b"}, time.Minute); err != nil {
		t.Fatalf("StorePerms: %v", err)
	}
	perms, err = s.LoadPerms(ctx, 7, "")
	if err != nil || len(perms) != 2 || perms[1] != "b" {
		t.Fatalf("LoadPerms = %v/%v", perms, err)
	}
	if err := s.RevokePerms(ctx, 7); err != nil {
		t.Fatalf("RevokePerms: %v", err)
	}
	perms, _ = s.LoadPerms(ctx, 7, "")
	if perms != nil {
		t.Fatal("RevokePerms 后应回源 miss")
	}
}

// TestRefreshKeyRoundTrip refresh 键以 userID 为 key,覆盖存取/删除。
func TestRefreshKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewSession(newFakeCache())

	if err := s.StoreRefresh(ctx, 7, "rt-1", time.Hour); err != nil {
		t.Fatalf("StoreRefresh: %v", err)
	}
	got, err := s.GetRefresh(ctx, 7)
	if err != nil || got != "rt-1" {
		t.Fatalf("GetRefresh = %q/%v", got, err)
	}
	if err := s.DeleteRefresh(ctx, 7); err != nil {
		t.Fatalf("DeleteRefresh: %v", err)
	}
	got, _ = s.GetRefresh(ctx, 7)
	if got != "" {
		t.Fatalf("删除后 GetRefresh = %q, want \"\"", got)
	}
}

// TestRevokeAllPermsPattern 全量失效按 perms:* 前缀清理。
func TestRevokeAllPermsPattern(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	for _, uid := range []uint64{1, 2, 3} {
		if err := s.StorePerms(ctx, uid, "", []string{"x"}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.StoreAccess(ctx, "tok", 1, time.Minute) // 非 perms 键不可被误删
	if err := s.RevokeAllPerms(ctx); err != nil {
		t.Fatalf("RevokeAllPerms: %v", err)
	}
	if ok, _ := s.IsAccessValid(ctx, "tok"); !ok {
		t.Fatal("RevokeAllPerms 误删了 access 白名单键")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if strings.HasPrefix(k, PermsPrefix) {
			t.Fatalf("RevokeAllPerms 后仍有 perms 键残留: %q", k)
		}
	}
}

// TestRevokeAll_EmptyTokenRevokesEveryAccessToken 是本包最关键的回归测试。
//
// 历史缺陷:禁用/删除/重置密码三条路径调用 RevokeAll(ctx, uid, ""),
// 而旧实现只做 Del(AccessPrefix+token) = Del("access:") —— 删一个永远
// 不存在的空键。用户全部已签发的 access token 因此留在白名单中,在剩余
// TTL 内依然通过 middleware.Auth 校验:禁用/删除/改密后旧 token 仍可用。
// 本测试锁定"空 token 也必须吊销全部 access token"这一契约。
func TestRevokeAll_EmptyTokenRevokesEveryAccessToken(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	// 同一用户多次登录 → 多个并发有效的 access token(多设备/多标签页)。
	for _, tok := range []string{"tok-a", "tok-b", "tok-c"} {
		if err := s.StoreAccess(ctx, tok, 42, time.Hour); err != nil {
			t.Fatalf("StoreAccess(%s): %v", tok, err)
		}
	}
	// 另一个用户的 token 不得被误伤。
	if err := s.StoreAccess(ctx, "tok-other", 99, time.Hour); err != nil {
		t.Fatal(err)
	}

	if err := s.RevokeAll(ctx, 42, ""); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}

	for _, tok := range []string{"tok-a", "tok-b", "tok-c"} {
		ok, err := s.IsAccessValid(ctx, tok)
		if err != nil {
			t.Fatalf("IsAccessValid(%s): %v", tok, err)
		}
		if ok {
			t.Errorf("RevokeAll(uid, \"\") 后 %s 仍有效 —— 禁用/删除/改密未真正吊销令牌", tok)
		}
	}
	if ok, _ := s.IsAccessValid(ctx, "tok-other"); !ok {
		t.Error("RevokeAll 误伤了其他用户的 token")
	}
}

// TestRevokeAll_ExplicitTokenAlsoRevoked 显式 token(登出/改密)同样被吊销,
// 且不依赖用户索引 —— 索引缺失(历史 token、索引写失败)时仍要保证
// "当前正在使用的 token"立刻失效。
func TestRevokeAll_ExplicitTokenAlsoRevoked(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	// 直接写入白名单键,不经过 StoreAccess → 模拟无索引的历史 token。
	c.data[AccessPrefix+"legacy"] = "42"
	if err := s.StoreRefresh(ctx, 42, "rt", time.Hour); err != nil {
		t.Fatal(err)
	}

	if err := s.RevokeAll(ctx, 42, "legacy"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if ok, _ := s.IsAccessValid(ctx, "legacy"); ok {
		t.Error("无索引的显式 token 未被吊销")
	}
	if rt, _ := s.GetRefresh(ctx, 42); rt != "" {
		t.Error("refresh token 未被吊销")
	}
}

// TestRevokeAll_ClearsIndexAndPerms 吊销后索引与权限缓存一并清理,
// 否则索引残留会让后续登录的 token 被前一次吊销操作顺带删除。
func TestRevokeAll_ClearsIndexAndPerms(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	if err := s.StoreAccess(ctx, "tok-1", 7, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePerms(ctx, 7, "", []string{"a"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAll(ctx, 7, ""); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}

	// 吊销后重新登录签发新 token —— 必须有效。
	if err := s.StoreAccess(ctx, "tok-2", 7, time.Hour); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsAccessValid(ctx, "tok-2"); !ok {
		t.Fatal("吊销后重新登录的 token 应有效(索引未清理会导致误删)")
	}
	if perms, _ := s.LoadPerms(ctx, 7, ""); perms != nil {
		t.Error("perms 缓存未被吊销")
	}
}

// TestAccessIndex_SurvivesCorruptValue 索引内容损坏时降级为空,不阻断登录。
func TestAccessIndex_SurvivesCorruptValue(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	c.data[UserAccessPrefix+"7"] = "{not json"
	if err := s.StoreAccess(ctx, "tok-1", 7, time.Hour); err != nil {
		t.Fatalf("索引损坏时 StoreAccess 应仍成功: %v", err)
	}
	if ok, _ := s.IsAccessValid(ctx, "tok-1"); !ok {
		t.Fatal("token 应有效")
	}
}

// TestStoreAccess_ConcurrentNoIndexLoss 同一用户并发登录时,反向索引不得
// 丢失任何 token —— 这是 Get→append→Set 旧实现会互相覆盖的竞态。改用
// SetAdd(SADD 原子追加)后,并发下每个 token 都必须进入索引,否则改密/禁用
// 后该 token 无法被批量吊销。
func TestStoreAccess_ConcurrentNoIndexLoss(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := NewSession(c)

	const n = 32
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = "tok-" + strconv.Itoa(i)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			if err := s.StoreAccess(ctx, tok, 7, time.Hour); err != nil {
				t.Errorf("StoreAccess(%s): %v", tok, err)
			}
		}(tokens[i])
	}
	wg.Wait()

	// 吊销全部后,每个 token 都应失效(索引无丢失)。
	if err := s.RevokeAll(ctx, 7, ""); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	for _, tok := range tokens {
		if ok, _ := s.IsAccessValid(ctx, tok); ok {
			t.Fatalf("token %s 吊销后仍有效,索引丢失", tok)
		}
	}
}
