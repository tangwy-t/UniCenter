package database

import (
	"testing"
	"time"
)

// 本文件是「LRU 淘汰：access:0 永不匹配 + 窄窗口误删新键」（评审报告 #12）
// 修复后的回归验证。
//
// 修复内容：新键在持锁期间分配单调时间戳 ts 并同时写入 lastAccess 与入堆
// （二者恒一致），消除了 ① access:0 与 lastAccess>=1 永不匹配的淘汰退化，
// 以及 ② push 后锁外 Store(ts) 前的窄窗口误删。

// 新键的堆条目 access 与表 lastAccess 必须一致（不再恒等 mismatch）。
func TestPoc_LRUEvict_NewKeyAccessMatchesLastAccess(t *testing.T) {
	stats := NewSQLStats(8, 200*time.Millisecond)
	stats.Record("sys_user", "SELECT", time.Millisecond, "SELECT 1", false, false)

	stats.mu.RLock()
	heapAccess := stats.lruHeap[0].access
	lastAccess := stats.tableStats["sys_user"].lastAccess.Load()
	stats.mu.RUnlock()

	if heapAccess == 0 {
		t.Fatal("新键入堆 access 不应为 0（修复后应为真实时间戳）")
	}
	if heapAccess != lastAccess {
		t.Fatalf("新键堆 access=%d 应与 lastAccess=%d 一致（否则淘汰判定仍退化）", heapAccess, lastAccess)
	}
}

// 新键创建后应立即可被 evictLRU 正确判定为真正 LRU 并删除（不再误删/退化）。
func TestPoc_LRUEvict_EvictsTrulyColdestKey(t *testing.T) {
	stats := NewSQLStats(8, 200*time.Millisecond)

	// 记录两张表：t1 先访问（更旧），t2 后访问（更新）。
	stats.Record("t1", "SELECT", time.Millisecond, "SELECT 1", false, false)
	stats.Record("t2", "SELECT", time.Millisecond, "SELECT 2", false, false)

	// 直接触发一次淘汰：应删除最旧键 t1（access 更小），保留 t2。
	stats.mu.Lock()
	stats.evictLRU()
	stats.mu.Unlock()

	stats.mu.RLock()
	_, t1Exists := stats.tableStats["t1"]
	_, t2Exists := stats.tableStats["t2"]
	stats.mu.RUnlock()

	if t1Exists {
		t.Fatal("evictLRU 应删除最旧键 t1，实际仍存在 —— 淘汰判定退化")
	}
	if !t2Exists {
		t.Fatal("evictLRU 不应删除较新键 t2")
	}
}
