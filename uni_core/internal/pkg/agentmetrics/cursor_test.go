package agentmetrics

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// newCursorStoreFixture 起一个 miniredis + CursorStore。
//
// 为什么用真 Redis 替身而不是 mock Cmdable：本文件要验证的正是 **Lua 脚本本身**
// （原子比较-写），mock 掉 Eval 等于把被测对象换成替身的行为。
func newCursorStoreFixture(t *testing.T) (*CursorStore, goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewCursorStore(rdb), rdb, mr
}

// cursorKeyLiteral 是契约字面量（不复用 CursorKey，否则键名写错也自洽）。
func cursorKeyLiteral(deviceID uint64, suffix string) string {
	return "agent:device:" + itoa(deviceID) + ":" + suffix
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestCursorStoreReadMissingIsNotAnError 钉住「键不存在」与「Redis 故障」是两回事：
// 前者是 Bootstrap 语义的入口（exists=false），后者必须上抛。
func TestCursorStoreReadMissingIsNotAnError(t *testing.T) {
	store, _, _ := newCursorStoreFixture(t)
	ctx := context.Background()

	v, ok, err := store.Read(ctx, 1001, Resolution5m)
	if err != nil {
		t.Fatalf("键缺失时 Read 报错: %v", err)
	}
	if ok || v != 0 {
		t.Fatalf("键缺失时 Read = (%d, %v), want (0, false)", v, ok)
	}

	if _, err := store.Init(ctx, 1001, Resolution5m, 1800000000); err != nil {
		t.Fatalf("Init: %v", err)
	}
	v, ok, err = store.Read(ctx, 1001, Resolution5m)
	if err != nil || !ok || v != 1800000000 {
		t.Fatalf("Init 后 Read = (%d, %v, %v), want (1800000000, true, nil)", v, ok, err)
	}
}

// TestCursorStoreInitNeverOverwrites 钉住 Bootstrap 语义：只在键缺失时写。
//
// 覆写已有的水位会让最近一个窗口的桶被无谓重放，也会破坏「水位只前进」的不变量。
func TestCursorStoreInitNeverOverwrites(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	key := cursorKeyLiteral(1001, "cursor_5m")

	if err := rdb.Set(ctx, key, 1800000000, 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Init(ctx, 1001, Resolution5m, 1700000000)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got != 1800000000 {
		t.Fatalf("Init 返回 %d, want 1800000000（已有水位时必须返回**键上生效的值**，不回写自己的候选值）", got)
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1800000000" {
		t.Fatalf("Init 覆写了已有水位：%s", cur)
	}
}

// TestCursorStoreAdvanceOnlyForward 钉住「只前进」：新值必须严格大于当前值。
func TestCursorStoreAdvanceOnlyForward(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	key := cursorKeyLiteral(1001, "cursor_5m")

	if err := rdb.Set(ctx, key, 1800000300, 0).Err(); err != nil {
		t.Fatal(err)
	}

	// ① 更小的值：拒绝（这正是「水位被推回」的形态）。
	applied, err := store.Advance(ctx, 1001, Resolution5m, 1800000300, 1800000000)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if applied {
		t.Fatal("Advance 接受了比当前值更小的水位（水位被推回）")
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1800000300" {
		t.Fatalf("水位 = %s, want 1800000300（拒绝写入后必须一动不动）", cur)
	}

	// ② 相等的值：也拒绝（相等不是前进；写它只会制造无谓的写放大）。
	if applied, err := store.Advance(ctx, 1001, Resolution5m, 1800000300, 1800000300); err != nil || applied {
		t.Fatalf("Advance(相等) = %v/%v, want false/nil", applied, err)
	}

	// ③ 更大的值：写入。
	applied, err = store.Advance(ctx, 1001, Resolution5m, 1800000300, 1800000600)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !applied {
		t.Fatal("Advance 拒绝了严格更大的水位")
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1800000600" {
		t.Fatalf("水位 = %s, want 1800000600", cur)
	}
}

// TestCursorStoreAdvanceRefusesWhenLevelWasRewound 是本仓库 S1 缺陷的**最小复现**。
//
// 时序（flush 与 backfill 在 :15:00 同时被 cron 唤醒）：
//
//	① flush 轮读到水位 expected = H；
//	② backfill 把水位回退到 T（T ≪ H），准备重放 [T+300, H]；
//	③ flush 轮跑完自己那段后想写下 next（> H，即 now−300）。
//
// 「只判断 next > 当前值」的实现会**通过** ③（next > T），于是水位被推回高位，
// 而 [T+300, H] 这段桶 flush 本轮根本没处理 → 回退丢失、待重放的桶永远不会被处理。
// 本实现要求「当前值仍是 expected」，故 ③ 被拒绝。
func TestCursorStoreAdvanceRefusesWhenLevelWasRewound(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	key := cursorKeyLiteral(1001, "cursor_5m")

	const (
		rewound  = int64(1799992800) // backfill 回退到的目标（N 小时前）
		stale    = int64(1800000000) // flush 轮初读到的水位 H
		advanced = int64(1800000300) // flush 本轮实际处理到的最后一个桶 next
	)
	if err := rdb.Set(ctx, key, rewound, 0).Err(); err != nil {
		t.Fatal(err)
	}

	applied, err := store.Advance(ctx, 1001, Resolution5m, stale, advanced)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if applied {
		t.Fatalf("水位被回退到 %d 后，flush 用轮初旧游标 %d 就读把水位推回到 %d —— "+
			"回退出来的那段桶本轮没被处理，水位却越过了它们", rewound, stale, advanced)
	}
	if cur := rdb.Get(ctx, key).Val(); cur != itoa(uint64(rewound)) {
		t.Fatalf("水位 = %s, want %d（并发回退后的水位不得被推回）", cur, rewound)
	}
}

// TestCursorStoreAdvanceRefusesWhenKeyVanished 钉住「键被并发删除」时让位：
// 下轮按 Bootstrap 语义重建起点，比用一个已失效的 expected 硬写安全。
func TestCursorStoreAdvanceRefusesWhenKeyVanished(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()

	applied, err := store.Advance(ctx, 1001, Resolution5m, 1800000000, 1800000300)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if applied {
		t.Fatal("键不存在时 Advance 不该写（应为让位，等下一轮按 Bootstrap 语义重建）")
	}
	if n := rdb.Exists(ctx, cursorKeyLiteral(1001, "cursor_5m")).Val(); n != 0 {
		t.Fatal("让位路径不得顺手创建水位键")
	}
}

// TestCursorStoreRewindOnlyBackward 钉住 backfill 的回退语义：只后退，绝不上移。
//
// 「绝不上移」这条既有安全性质（目标起点比当前游标更新时什么都不做）必须在
// **Redis 侧原子成立**：Go 侧「读 → 比 → 写」会被并发者插进来（另一个 backfill
// 回退得更深时，我们的 SET 相对新的当前值反而是一次上移）。
func TestCursorStoreRewindOnlyBackward(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	key := cursorKeyLiteral(1001, "cursor_5m")

	// ① 目标比当前值新：拒绝（回退会上移水位 → 永久跳过中间那段桶）。
	if err := rdb.Set(ctx, key, 1700000000, 0).Err(); err != nil {
		t.Fatal(err)
	}
	applied, err := store.Rewind(ctx, 1001, Resolution5m, 1800000000)
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if applied {
		t.Fatal("Rewind 把水位**上移**了（目标起点比当前游标更新）")
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1700000000" {
		t.Fatalf("水位 = %s, want 1700000000（拒绝写入后必须一动不动）", cur)
	}

	// ② 目标比当前值旧：写入。
	applied, err = store.Rewind(ctx, 1001, Resolution5m, 1600000000)
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if !applied {
		t.Fatal("Rewind 拒绝了比当前值更旧的目标")
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1600000000" {
		t.Fatalf("水位 = %s, want 1600000000", cur)
	}

	// ③ 相等：不写（不需要回退）。
	if applied, err := store.Rewind(ctx, 1001, Resolution5m, 1600000000); err != nil || applied {
		t.Fatalf("Rewind(相等) = %v/%v, want false/nil", applied, err)
	}

	// ④ 键缺失：写入（Bootstrap 缺失语义下的初始化）。
	if err := rdb.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	if applied, err := store.Rewind(ctx, 1001, Resolution5m, 1500000000); err != nil || !applied {
		t.Fatalf("Rewind(键缺失) = %v/%v, want true/nil", applied, err)
	}
	if cur := rdb.Get(ctx, key).Val(); cur != "1500000000" {
		t.Fatalf("水位 = %s, want 1500000000", cur)
	}
}

// TestCursorStoreTwoResolutionsAreIsolated 钉住「两个档位各自独立」：
// 推进 5m 不得动 1h，反之亦然（同键会让 flush 与 rollup 互相覆盖水位）。
func TestCursorStoreTwoResolutionsAreIsolated(t *testing.T) {
	store, _, _ := newCursorStoreFixture(t)
	ctx := context.Background()

	if _, err := store.Init(ctx, 1001, Resolution5m, 1800000000); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Init(ctx, 1001, Resolution1h, 1799996400); err != nil {
		t.Fatal(err)
	}
	if applied, err := store.Advance(ctx, 1001, Resolution1h, 1799996400, 1800000000); err != nil || !applied {
		t.Fatalf("推进 cursor_1h = %v/%v, want true/nil", applied, err)
	}
	v5, _, err := store.Read(ctx, 1001, Resolution5m)
	if err != nil {
		t.Fatal(err)
	}
	if v5 != 1800000000 {
		t.Fatalf("cursor_5m = %d, want 1800000000（推进 1h 不得影响 5m）", v5)
	}
	v1, _, err := store.Read(ctx, 1001, Resolution1h)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 1800000000 {
		t.Fatalf("cursor_1h = %d, want 1800000000", v1)
	}
}

// TestCursorStoreRejectsUnknownResolution 钉住「未登记档位不得被当成 5m 用」：
// 三个方法都必须在构造键之前就报错，且不得触碰任何键。
func TestCursorStoreRejectsUnknownResolution(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	unknown := Resolution(uint8(len(cursorKeySuffixes)))

	if _, _, err := store.Read(ctx, 1001, unknown); err == nil {
		t.Fatal("Read(未登记档位) 未报错")
	}
	if _, err := store.Init(ctx, 1001, unknown, 1); err == nil {
		t.Fatal("Init(未登记档位) 未报错")
	}
	if _, err := store.Advance(ctx, 1001, unknown, 0, 1); err == nil {
		t.Fatal("Advance(未登记档位) 未报错")
	}
	if _, err := store.Rewind(ctx, 1001, unknown, 1); err == nil {
		t.Fatal("Rewind(未登记档位) 未报错")
	}
	if n := len(rdb.Keys(ctx, "agent:device:*").Val()); n != 0 {
		t.Fatalf("未登记档位不得触碰任何键，实际写了 %d 个", n)
	}
}

// TestCursorStoreAdvanceScriptIsAtomicCompareAndWrite 是脚本形态的守卫：
// 断言推进脚本**同时**包含期望值比对与只前进比对。
//
// 为什么直接断言脚本文本：这两个条件都在 Lua 里，Go 侧没有任何可观测的替身能证明
// 「比较与写之间没有缝」；而删掉任一条都会让 S1 的缺陷以「测试偶发变红」的形态回来
// （时序相关）。文本守卫让「误删一行 Lua」变成一条确定性的失败。
func TestCursorStoreAdvanceScriptIsAtomicCompareAndWrite(t *testing.T) {
	for _, want := range []string{`redis.call("GET", KEYS[1])`, `cur ~= ARGV[1]`, `tonumber(ARGV[2]) <= tonumber(cur)`, `redis.call("SET", KEYS[1], ARGV[2])`} {
		if !strings.Contains(advanceCursorScript, want) {
			t.Fatalf("推进脚本缺少 %q —— 缺了它就不再是「原子比较-写」", want)
		}
	}
	if strings.Contains(advanceCursorScript, "EXPIRE") || strings.Contains(advanceCursorScript, "ARGV[3]") {
		t.Fatal("水位不得带 TTL：水位是「已落库到哪」的记账，过期会让窗口被整段重放/漏放")
	}
}

// ── 回退「待追平」标记（Task 1 的标记写入侧与清除侧）──────────────

// TestCursorStoreRewindMarkerLifecycle 钉住标记三件套的语义：
//   - 键缺失时 PendingRewind 返回 (0,false,nil)（**不是错误**，= 没有待追平的回退）；
//   - MarkRewind 写值；重复写只向**更早**移动（浅回退不得把目标推后）；
//   - ClearRewind 只在值仍是期望值时才删（并发回退改得更早时让位）；
//   - 键名走契约字面量（形态与游标键同族：agent:device:{id}:rewind_1h）。
func TestCursorStoreRewindMarkerLifecycle(t *testing.T) {
	store, rdb, _ := newCursorStoreFixture(t)
	ctx := context.Background()
	key := cursorKeyLiteral(1001, "rewind_1h")

	// 缺键：没有待追平的回退，不是错误。
	if v, ok, err := store.PendingRewind(ctx, 1001, Resolution1h); err != nil || ok || v != 0 {
		t.Fatalf("缺键时 PendingRewind = (%d, %v, %v), want (0, false, nil)", v, ok, err)
	}

	// 写入后读回同一个值（键名必须是契约字面量）。
	if err := store.MarkRewind(ctx, 1001, Resolution1h, 1800000000); err != nil {
		t.Fatalf("MarkRewind: %v", err)
	}
	if got := rdb.Get(ctx, key).Val(); got != "1800000000" {
		t.Fatalf("标记键 %s = %q, want %q（键名契约：agent:device:{id}:rewind_1h）", key, got, "1800000000")
	}
	if v, ok, err := store.PendingRewind(ctx, 1001, Resolution1h); err != nil || !ok || v != 1800000000 {
		t.Fatalf("PendingRewind = (%d, %v, %v), want (1800000000, true, nil)", v, ok, err)
	}

	// 更早的目标 → 覆盖；更晚的目标 → **不动**（只向更早移动）。
	if err := store.MarkRewind(ctx, 1001, Resolution1h, 1799990000); err != nil {
		t.Fatalf("MarkRewind(更早): %v", err)
	}
	if v, _, _ := store.PendingRewind(ctx, 1001, Resolution1h); v != 1799990000 {
		t.Fatalf("标记 = %d, want 1799990000（更早的目标必须覆盖）", v)
	}
	if err := store.MarkRewind(ctx, 1001, Resolution1h, 1800000000); err != nil {
		t.Fatalf("MarkRewind(更晚): %v", err)
	}
	if v, _, _ := store.PendingRewind(ctx, 1001, Resolution1h); v != 1799990000 {
		t.Fatalf("标记 = %d, want 1799990000（更晚的目标不得推后它：待追平区间只许变长）", v)
	}

	// 期望值不匹配 → 不删（并发回退写下的更早目标必须留下）。
	if cleared, err := store.ClearRewind(ctx, 1001, Resolution1h, 1800000000); err != nil || cleared {
		t.Fatalf("ClearRewind(期望值不匹配) = (%v, %v), want (false, nil)", cleared, err)
	}
	if v, ok, _ := store.PendingRewind(ctx, 1001, Resolution1h); !ok || v != 1799990000 {
		t.Fatalf("期望值不匹配时标记被删掉了（待追平状态丢失）：(%d, %v)", v, ok)
	}

	// 期望值匹配 → 删掉，且再读是「没有标记」。
	if cleared, err := store.ClearRewind(ctx, 1001, Resolution1h, 1799990000); err != nil || !cleared {
		t.Fatalf("ClearRewind(期望值匹配) = (%v, %v), want (true, nil)", cleared, err)
	}
	if v, ok, err := store.PendingRewind(ctx, 1001, Resolution1h); err != nil || ok || v != 0 {
		t.Fatalf("清除后 PendingRewind = (%d, %v, %v), want (0, false, nil)", v, ok, err)
	}

	// 两个档位的标记是**两个键**：各自的值互不影响（同键会让 5m 的回退掩盖 1h 的待追平状态）。
	if err := store.MarkRewind(ctx, 1001, Resolution5m, 1799990000); err != nil {
		t.Fatalf("MarkRewind(5m): %v", err)
	}
	if err := store.MarkRewind(ctx, 1001, Resolution1h, 1799980000); err != nil {
		t.Fatalf("MarkRewind(1h): %v", err)
	}
	if v, ok, _ := store.PendingRewind(ctx, 1001, Resolution1h); !ok || v != 1799980000 {
		t.Fatalf("1h 标记 = %d(ok=%v), want 1799980000（不得被 5m 的那次写入影响）", v, ok)
	}
	if v, ok, _ := store.PendingRewind(ctx, 1001, Resolution5m); !ok || v != 1799990000 {
		t.Fatalf("5m 标记 = %d(ok=%v), want 1799990000", v, ok)
	}
	key5m := cursorKeyLiteral(1001, "rewind_5m")
	if got := rdb.Get(ctx, key5m).Val(); got != "1799990000" {
		t.Fatalf("5m 标记键 %s = %q, want 1799990000（两个档位的标记必须落在各自的键上）", key5m, got)
	}

	// 未登记的档位：三个方法都必须在构造键之前报错，且不触碰任何键。
	unknown := Resolution(uint8(len(rewindMarkerSuffixes)))
	before := len(rdb.Keys(ctx, "agent:device:*").Val())
	if _, _, err := store.PendingRewind(ctx, 1001, unknown); err == nil {
		t.Fatal("PendingRewind(未登记档位) 未报错")
	}
	if err := store.MarkRewind(ctx, 1001, unknown, 1); err == nil {
		t.Fatal("MarkRewind(未登记档位) 未报错")
	}
	if _, err := store.ClearRewind(ctx, 1001, unknown, 1); err == nil {
		t.Fatal("ClearRewind(未登记档位) 未报错")
	}
	if after := len(rdb.Keys(ctx, "agent:device:*").Val()); after != before {
		t.Fatalf("未登记档位不得触碰任何键：键数 %d → %d", before, after)
	}
}
