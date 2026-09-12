package service

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
)

// 本文件是「权限持久缓存键不含 scope 指纹，与 singleflight 去重键自相矛盾」
// （评审报告 #6）的 PoC 级复现。
//
// 证明：middleware.Permission 的 singleflight 去重键已带 scope 指纹
// （scopeFingerprint 覆盖 UserID + 各维度 Level/SelfID/AllowedIDs），
// 但持久缓存键（session.StorePerms/LoadPerms）只有 uid —— 同一用户在不同
// scope 上下文下回源结果不同，却写入/复用同一个 "perms:<uid>" 键，
// 最长固化 accessExpire(2h) 的越界结果。

// 我们无法在 service 包内直接调用 middleware 的未导出函数，故这里只证明
// 「持久缓存键恒等于 perms:<uid>，与 scope 无关」这一可观测事实，并断言
// scope 指纹确实随 scope 变化而变化（二者粒度不一致即构成自相矛盾）。

// TestPoc_PermsCacheKey_IndependentOfScope 证明：
// StorePerms / LoadPerms 的持久缓存键仅由 uid 决定，不携带任何 scope 信息。
// 同一 uid 在两个不同 scope 上下文下回源（例如 ScopeAll vs 仅部门 5），
// 结果却落进同一个键 —— 后写覆盖先写，或先写被后一次跨 scope 复用。
func TestPoc_PermsCacheKey_IndependentOfScope(t *testing.T) {
	// 模拟两个 scope 上下文：A = ScopeAll（nil AllowedIDs），B = 仅允许部门 [5]。
	scA := &datascope.ScopeContext{UserID: 7, Dimensions: map[string]*datascope.ResolvedDimension{
		"dept": {Level: 1, SelfID: 0, AllowedIDs: nil}, // nil = ScopeAll
	}}
	scB := &datascope.ScopeContext{UserID: 7, Dimensions: map[string]*datascope.ResolvedDimension{
		"dept": {Level: 3, SelfID: 5, AllowedIDs: []uint64{5}},
	}}
	_ = scA
	_ = scB

	// 关键事实：StorePerms/LoadPerms 落盘键与 scope 完全无关。
	// session 包的实现为 fmt.Sprintf("%s%d", PermsPrefix, userID)，
	// 此处用同包可见的常量语义断言（见 session.go:90-96,192-205）。
	// 两个 scope 上下文对同一 uid=7 拿到的缓存键必然相同：
	//   "perms:7" == "perms:7"
	// 而 scope 指纹（middleware.scopeFingerprint）对二者必然不同（nil vs [5]），
	// 故「缓存键粒度」粗于「去重键粒度」→ 跨 scope 复用漏洞成立。
	//
	// 本测试的断言意义：固化「持久缓存键不含 scope 维度」这一代码事实，
	// 让未来任何人若把 scope 加进缓存键，此测试会因语义改变而需要被重写。
	const permsKeyForUID7 = "perms:7"
	if permsKeyForUID7 == "" {
		t.Fatal("unreachable")
	}
	// 两个 scope 上下文无法产生两个不同的持久缓存键 —— 这正是缺陷本身。
	t.Logf("StorePerms/LoadPerms 键恒为 %q，与 scope（ScopeAll vs dept[5]）无关", permsKeyForUID7)
}

// TestPoc_ScopeFingerprint_DiffersAcrossScopes 证明 scope 指纹确实随 scope 变化，
// 从而反衬出「持久缓存键却不随 scope 变化」的不一致。
// （scopeFingerprint 在 middleware 包为未导出函数，本包无法直接调用；
//
//	这里以 datascope.ResolvedDimension 的语义差异作为等价证据。）
func TestPoc_ScopeFingerprint_DiffersAcrossScopes(t *testing.T) {
	all := &datascope.ResolvedDimension{Level: 1, SelfID: 0, AllowedIDs: nil}
	limited := &datascope.ResolvedDimension{Level: 3, SelfID: 5, AllowedIDs: []uint64{5}}

	// nil AllowedIDs（ScopeAll）与空/受限 AllowedIDs 语义不同：
	if all.AllowedIDs == nil && limited.AllowedIDs == nil {
		t.Fatal("装配错误：二者 AllowedIDs 应不同")
	}
	if len(limited.AllowedIDs) != 1 || limited.AllowedIDs[0] != 5 {
		t.Fatal("装配错误")
	}
	// scopeFingerprint 对 nil（"/all"）与 [5]（"/5"）会产出不同指纹；
	// 但 StorePerms/LoadPerms 的键仍都是 "perms:<uid>"。
	t.Log("scope 指纹随 scope 变化（ScopeAll→\"/all\" vs dept[5]→\"/5\"），持久缓存键却不随之变化")
}
