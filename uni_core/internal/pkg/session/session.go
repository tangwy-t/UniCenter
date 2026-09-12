package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cast"
	"strconv"
	"strings"
	"time"
)

const (
	// AccessPrefix is the Redis key prefix for access token → userID mappings.
	AccessPrefix = "access:"
	// UserAccessPrefix is the Redis key prefix for the user → access-token index
	// (a Redis SET of token strings, keyed by userID). It exists solely so that
	// every access token ever issued to a user can be revoked in one operation:
	// the access keys themselves are token-keyed, so without this reverse index
	// "revoke everything for user N" is impossible to express with a single DEL.
	UserAccessPrefix = "user_access:"
	// RefreshPrefix is the Redis key prefix for refresh token → userID mappings (keyed by userID).
	RefreshPrefix = "refresh:"
	// PermsPrefix is the Redis key prefix for cached user permissions (keyed by userID).
	PermsPrefix = "perms:"
	// SessionPrefix is the Redis key prefix for online-session metadata
	// (JSON value), keyed by access token, TTL aligned with the access token.
	SessionPrefix = "session:"
)

// maxAccessRevocation 一次按用户吊销最多清理的 access token 数(防御性上限)。
const maxAccessRevocation = 1000

// SessionStore is the shared Redis-backed implementation of the token and
// permission store contracts. NewSession returns it; consumers accept it
// through their own focused consumer-side interfaces (TokenStoreInterface,
// PermissionStoreInterface, SessionStoreInterface).
type SessionStore struct {
	cacheStore CacheStoreInterface
}

func NewSession(cacheStore CacheStoreInterface) *SessionStore {
	return &SessionStore{cacheStore: cacheStore}
}

func (s *SessionStore) StoreAccess(ctx context.Context, token string, userID uint64, ttl time.Duration) error {
	if err := s.cacheStore.Set(ctx, AccessPrefix+token, cast.ToString(userID), ttl); err != nil {
		return err
	}
	return s.indexUserAccess(ctx, userID, token, ttl)
}

// indexUserAccess 把 token 追加进该用户的 access 索引。
//
// 索引是 redis 中的集合(SetAdd 原子追加),而非普通 string 键。
// 此前用「JSON 数组 + Get→append→Set」:并发登录(多设备同时登录同一
// 用户)会互相覆盖,导致部分 token 索引丢失(改密/禁用后旧 token 无法
// 批量吊销)。SetAdd 在 Redis 单线程内原子完成追加,从根上消除竞态。
//
// 索引写失败不阻断登录:索引只影响"能否一次性吊销全部 token",
// 不影响 token 本身可用性;失败降级为"该次登录的 token 不参与批量吊销"
// (逐 token 登出仍然有效),由调用方按运维告警跟进。
func (s *SessionStore) indexUserAccess(ctx context.Context, userID uint64, token string, ttl time.Duration) error {
	key := fmt.Sprintf("%s%d", UserAccessPrefix, userID)
	return s.cacheStore.SetAdd(ctx, key, token, ttl)
}

// loadAccessIndex 读取用户的 access 索引;键不存在时返回空切片。
// 索引已改为 Redis 集合(SetAdd/SetMembers),读取走集合语义。
func (s *SessionStore) loadAccessIndex(ctx context.Context, key string) ([]string, error) {
	return s.cacheStore.SetMembers(ctx, key)
}

func (s *SessionStore) StoreRefresh(ctx context.Context, userID uint64, token string, ttl time.Duration) error {
	return s.cacheStore.Set(ctx, fmt.Sprintf("%s%d", RefreshPrefix, userID), token, ttl)
}

// RotateRefresh 原子地把用户的 refresh token 从 old 换成 new。
//
// 返回 consumed=false 表示 old 已不是当前有效的 refresh token
// (已被兑换过、已过期,或值不匹配),调用方必须拒绝本次刷新。
//
// 为什么必须是 CAS 而不是 Get+Delete:
//   - 竞态重放:两个并发刷新请求都能 Get 到同一个 old,两步写法会双双放行,
//     同一个 refresh token 被兑换出两组有效令牌(等于轮换形同虚设)。
//   - 失败即登出:此前实现在校验通过后立刻 Delete,再签发并存储新令牌;
//     若签发/存储阶段失败(DB 抖动、账号被禁用),用户既没有旧 token 也
//     没有新 token,被强制登出。CAS 把"作废旧"与"写入新"合并为一步,
//     只有新令牌已就绪才提交,失败时用户仍持有旧令牌可重试。
func (s *SessionStore) RotateRefresh(ctx context.Context, userID uint64, old, new string, ttl time.Duration) (consumed bool, err error) {
	key := fmt.Sprintf("%s%d", RefreshPrefix, userID)
	return s.cacheStore.CompareAndSwap(ctx, key, old, new, ttl)
}

// permsKey 计算权限缓存的持久化键。安全修复（评审 #6）：键必须携带 scope
// 指纹，与 middleware.Permission 的 singleflight 去重键粒度一致。此前仅按
// uid 分键，同一用户在不同 scope 上下文（登录构造的 ctx 与请求经
// ScopeResolverHandler 注入的 ctx）回源结果不同，却写入/复用同一个键，
// 最长固化 accessExpire(2h) 的越界权限。
func permsKey(userID uint64, scopeKey string) string {
	if scopeKey == "" {
		return fmt.Sprintf("%s%d", PermsPrefix, userID)
	}
	return fmt.Sprintf("%s%d:%s", PermsPrefix, userID, scopeKey)
}

func (s *SessionStore) StorePerms(ctx context.Context, userID uint64, scopeKey string, perms []string, ttl time.Duration) error {
	data, err := json.Marshal(perms)
	if err != nil {
		return err
	}
	return s.cacheStore.Set(ctx, permsKey(userID, scopeKey), string(data), ttl)
}

// RevokeAll 吊销用户的全部会话:access 白名单(全部已签发 token)、
// refresh token 与权限缓存。
//
// accessToken 非空时额外立即吊销该 token —— 用于改密/登出场景:调用方
// 手上正好持有当前 token,即使索引缺失(历史 token 或索引写失败)也能
// 确保"正在使用的这个"立刻失效。
//
// 修复历史缺陷:此前实现为 Del(AccessPrefix+token),而三个调用点
// (禁用/删除/重置密码)传的是空串 → 实际执行 Del("access:") ——
// 删一个不存在的键。结果是 refresh 与 perms 被清,但该用户**全部
// 已签发的 access token 仍在白名单中**,禁用/删除/改密后旧 token
// 在剩余 TTL(默认 2h)内完全可用。现在按用户索引逐 token 删除。
func (s *SessionStore) RevokeAll(ctx context.Context, userID uint64, token string) error {
	// Split into individual Del calls to avoid CROSSSLOT error in Redis cluster mode
	// (keys with different prefixes hash to different slots).
	if err := s.revokeUserAccessTokens(ctx, userID); err != nil {
		return err
	}
	if token != "" {
		if err := s.cacheStore.Del(ctx, AccessPrefix+token); err != nil {
			return err
		}
	}
	if err := s.cacheStore.Del(ctx, fmt.Sprintf("%s%d", RefreshPrefix, userID)); err != nil {
		return err
	}
	// 权限缓存键现在带 scope 指纹后缀，按用户吊销须前缀删除全部形态。
	_, err := s.cacheStore.DeleteByPattern(ctx, fmt.Sprintf("%s%d", PermsPrefix, userID)+"*", maxPermsInvalidation)
	return err
}

// revokeUserAccessTokens 删除该用户索引中的全部 access 白名单键,并清除索引本身。
func (s *SessionStore) revokeUserAccessTokens(ctx context.Context, userID uint64) error {
	key := fmt.Sprintf("%s%d", UserAccessPrefix, userID)
	tokens, err := s.loadAccessIndex(ctx, key)
	if err != nil {
		return err
	}
	if len(tokens) > 0 {
		if len(tokens) > maxAccessRevocation {
			tokens = tokens[:maxAccessRevocation]
		}
		keys := make([]string, 0, len(tokens)*2)
		for _, t := range tokens {
			if t != "" {
				keys = append(keys, AccessPrefix+t, SessionPrefix+t)
			}
		}
		if len(keys) > 0 {
			if err := s.cacheStore.Del(ctx, keys...); err != nil {
				return err
			}
		}
	}
	return s.cacheStore.Del(ctx, key)
}

func (s *SessionStore) RevokePerms(ctx context.Context, userID uint64) error {
	// 安全修复（评审 #6）：权限缓存键现在带 scope 指纹后缀，按用户吊销须
	// 匹配 perms:<uid> 与 perms:<uid>:* 两种形态，故改用前缀删除。
	_, err := s.cacheStore.DeleteByPattern(ctx, fmt.Sprintf("%s%d", PermsPrefix, userID)+"*", maxPermsInvalidation)
	return err
}

// maxPermsInvalidation 一次全量失效最多删除的 perms 缓存键数(防御性上限)。
const maxPermsInvalidation = 100000

// RevokeAllPerms 失效所有用户的权限缓存。用于菜单权限点变更:影响面是
// "引用该菜单的全部角色 → 全部用户",逐角色追踪成本高且易漏;菜单管理为
// 低频管理操作,全清一次让所有用户下次请求时回源重建,最简且安全。
func (s *SessionStore) RevokeAllPerms(ctx context.Context) error {
	_, err := s.cacheStore.DeleteByPattern(ctx, PermsPrefix+"*", maxPermsInvalidation)
	return err
}

// IsAccessValid 校验 access token 是否在会话白名单中。
// 空串 = key 不存在 = 已登出/吊销/过期 → 无效。曾经依赖 goredis.Nil
// 判 miss,但 CacheStoreInterface.Get 的实现已把 Nil 翻译成空串,
// 死分支导致 miss 返回 (true, nil)——吊销校验整体 fail-open。
func (s *SessionStore) IsAccessValid(ctx context.Context, token string) (bool, error) {
	val, err := s.cacheStore.Get(ctx, AccessPrefix+token)
	if err != nil {
		return false, err
	}
	return val != "", nil
}

func (s *SessionStore) GetRefresh(ctx context.Context, userID uint64) (string, error) {
	val, err := s.cacheStore.Get(ctx, fmt.Sprintf("%s%d", RefreshPrefix, userID))
	if err != nil {
		return "", err
	}
	return val, nil // 空串 = 无有效 refresh token
}

func (s *SessionStore) DeleteRefresh(ctx context.Context, userID uint64) error {
	return s.cacheStore.Del(ctx, fmt.Sprintf("%s%d", RefreshPrefix, userID))
}

func (s *SessionStore) LoadPerms(ctx context.Context, userID uint64, scopeKey string) ([]string, error) {
	cached, err := s.cacheStore.Get(ctx, permsKey(userID, scopeKey))
	if err != nil {
		return nil, err
	}
	if cached == "" {
		return nil, nil // 缓存 miss,消费方回源 DB
	}
	var perms []string
	if err := json.Unmarshal([]byte(cached), &perms); err != nil {
		return nil, err
	}
	return perms, nil
}

// ListOnlineUserIDs 扫描 user_access:* 索引键,解析出全部"曾登录"的用户 ID。
// maxCount 为防御性上限:超出截断,正常管理后台不会触达。
func (s *SessionStore) ListOnlineUserIDs(ctx context.Context, maxCount int64) ([]uint64, error) {
	keys, err := s.cacheStore.ScanKeyNames(ctx, UserAccessPrefix+"*", maxCount)
	if err != nil {
		return nil, err
	}
	seen := make(map[uint64]struct{}, len(keys))
	uids := make([]uint64, 0, len(keys))
	for _, k := range keys {
		idStr := strings.TrimPrefix(k, UserAccessPrefix)
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uids = append(uids, id)
	}
	return uids, nil
}

// ListUserTokens 返回该用户全部 access token(反向索引成员)。
func (s *SessionStore) ListUserTokens(ctx context.Context, userID uint64) ([]string, error) {
	return s.loadAccessIndex(ctx, fmt.Sprintf("%s%d", UserAccessPrefix, userID))
}

// StoreSessionMeta 写入一个 access token 的会话元数据,TTL 与 access 一致。
func (s *SessionStore) StoreSessionMeta(ctx context.Context, token string, meta *SessionMeta, ttl time.Duration) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.cacheStore.Set(ctx, SessionPrefix+token, string(raw), ttl)
}

// LoadSessionMeta 读取单个会话元数据;key 不存在返回 (nil, nil)。
func (s *SessionStore) LoadSessionMeta(ctx context.Context, token string) (*SessionMeta, error) {
	raw, err := s.cacheStore.Get(ctx, SessionPrefix+token)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var m SessionMeta
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// BulkLoadSessionMeta 批量读取元数据,返回 token→meta 映射;缺失的 token 不出现在结果中。
func (s *SessionStore) BulkLoadSessionMeta(ctx context.Context, tokens []string) (map[string]*SessionMeta, error) {
	keys := make([]string, len(tokens))
	for i, t := range tokens {
		keys[i] = SessionPrefix + t
	}
	vals, err := s.cacheStore.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*SessionMeta, len(tokens))
	for i, v := range vals {
		if v == "" {
			continue
		}
		var m SessionMeta
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, err
		}
		out[tokens[i]] = &m
	}
	return out, nil
}
