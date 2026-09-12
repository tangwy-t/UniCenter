package cache

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// newPageTestStore 启动内存 Redis（miniredis）并返回由它支撑的 RedisStore。
func newPageTestStore(t *testing.T) (*RedisStore, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewStore(client), client
}

// seedList 生成 "item-0".."item-<n-1>" 的链表。
func seedList(t *testing.T, client goredis.UniversalClient, key string, n int) {
	t.Helper()
	vals := make([]interface{}, 0, n)
	for i := 0; i < n; i++ {
		vals = append(vals, fmt.Sprintf("item-%d", i))
	}
	if err := client.RPush(context.Background(), key, vals...).Err(); err != nil {
		t.Fatalf("seed list: %v", err)
	}
}

// TestGetValuePageListPages 验证 list 按下标分页的边界与元信息。
func TestGetValuePageListPages(t *testing.T) {
	store, client := newPageTestStore(t)
	const key = "job:logs"
	seedList(t, client, key, 250)

	ctx := context.Background()

	// 第 1 页：offset 0，limit 100
	p1, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 0, Limit: 100})
	if err != nil {
		t.Fatalf("page1 err: %v", err)
	}
	if p1.Total != 250 || p1.Start != 0 || !p1.HasMore || p1.Type != "list" {
		t.Fatalf("page1 meta = %+v, want Total 250/Start 0/HasMore true/Type list", p1)
	}
	items := p1.Value.([]string)
	if len(items) != 100 || items[0] != "item-0" || items[99] != "item-99" {
		t.Fatalf("page1 len=%d first=%q last=%q", len(items), items[0], items[len(items)-1])
	}

	// 第 3 页（末页）：offset 200，只剩 50 条
	p3, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 200, Limit: 100})
	if err != nil {
		t.Fatalf("page3 err: %v", err)
	}
	if p3.HasMore {
		t.Fatalf("page3 HasMore = true, want false")
	}
	items = p3.Value.([]string)
	if len(items) != 50 || items[0] != "item-200" {
		t.Fatalf("page3 len=%d first=%q", len(items), items[0])
	}

	// offset 越过 total：空页，不报错
	p4, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 300, Limit: 100})
	if err != nil {
		t.Fatalf("overshoot err: %v", err)
	}
	if items := p4.Value.([]string); len(items) != 0 || p4.HasMore {
		t.Fatalf("overshoot page len=%d HasMore=%v, want empty/false", len(items), p4.HasMore)
	}
}

// TestGetValuePageZSetPages 验证 zset 按排名分页与分值透传。
func TestGetValuePageZSetPages(t *testing.T) {
	store, client := newPageTestStore(t)
	const key = "rank:board"
	ctx := context.Background()

	members := make([]goredis.Z, 0, 150)
	for i := 0; i < 150; i++ {
		members = append(members, goredis.Z{Score: float64(i + 1), Member: fmt.Sprintf("u-%d", i)})
	}
	if err := client.ZAdd(ctx, key, members...).Err(); err != nil {
		t.Fatalf("seed zset: %v", err)
	}

	p1, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 0, Limit: 100})
	if err != nil {
		t.Fatalf("page1 err: %v", err)
	}
	if p1.Total != 150 || !p1.HasMore {
		t.Fatalf("page1 meta = %+v", p1)
	}
	zs := p1.Value.([]ZSetMember)
	if len(zs) != 100 || zs[0].Member != "u-0" || zs[0].Score != 1 || zs[99].Member != "u-99" {
		t.Fatalf("page1 len=%d first=%+v", len(zs), zs[0])
	}

	p2, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 100, Limit: 100})
	if err != nil {
		t.Fatalf("page2 err: %v", err)
	}
	zs = p2.Value.([]ZSetMember)
	if len(zs) != 50 || zs[0].Member != "u-100" || zs[0].Score != 101 || p2.HasMore {
		t.Fatalf("page2 len=%d first=%+v HasMore=%v", len(zs), zs[0], p2.HasMore)
	}
}

// TestGetValuePageSetPages 验证 set 的 SSCAN 游标分页。
// 注意：miniredis 的 SSCAN 对成员排序（sort.Strings），游标=已扫描偏移，行为确定。
func TestGetValuePageSetPages(t *testing.T) {
	store, client := newPageTestStore(t)
	const key = "tags:all"
	ctx := context.Background()

	members := make([]interface{}, 0, 250)
	for i := 0; i < 250; i++ {
		members = append(members, fmt.Sprintf("t-%03d", i))
	}
	if err := client.SAdd(ctx, key, members...).Err(); err != nil {
		t.Fatalf("seed set: %v", err)
	}

	p1, err := store.GetValuePage(ctx, key, ValuePageOption{Cursor: 0, Limit: 100})
	if err != nil {
		t.Fatalf("page1 err: %v", err)
	}
	if p1.Total != 250 || !p1.HasMore || p1.NextCursor == 0 {
		t.Fatalf("page1 meta = %+v", p1)
	}
	ms := p1.Value.([]string)
	if len(ms) != 100 || ms[0] != "t-000" || ms[99] != "t-099" {
		t.Fatalf("page1 len=%d first=%q last=%q", len(ms), ms[0], ms[len(ms)-1])
	}

	p2, err := store.GetValuePage(ctx, key, ValuePageOption{Cursor: p1.NextCursor, Limit: 100})
	if err != nil {
		t.Fatalf("page2 err: %v", err)
	}
	ms = p2.Value.([]string)
	if len(ms) != 100 || ms[0] != "t-100" {
		t.Fatalf("page2 len=%d first=%q", len(ms), ms[0])
	}

	p3, err := store.GetValuePage(ctx, key, ValuePageOption{Cursor: p2.NextCursor, Limit: 100})
	if err != nil {
		t.Fatalf("page3 err: %v", err)
	}
	ms = p3.Value.([]string)
	if len(ms) != 50 || p3.HasMore || p3.NextCursor != 0 {
		t.Fatalf("page3 len=%d HasMore=%v NextCursor=%d", len(ms), p3.HasMore, p3.NextCursor)
	}
}

// TestGetValuePageHashPages 验证 hash 的 HSCAN 给到分页方法后的收敛行为与条目顺序。
// 注意（执行期修订,miniredis v2.39.0 源码实证）：miniredis 的 HSCAN 忽略 COUNT（cmdHscan
// 注释 "we do nothing with count"），游标非 0 一律按无效处理返回空，游标 0 时一次返回全量
// 字段且 next cursor 恒为 0。因此 miniredis 下无法断言 hash 的游标翻页（SSCAN 尊重 count
// 有翻页，set 用例不受影响）；本用例改断言：单次收敛全量、Total/HasMore/NextCursor 正确。
// 真实 Redis 的 HSCAN 游标分页由 Task 6 对宿主 redis 的人工验收覆盖；分页累积循环与
// getSetPage 同一形状，已由 SetPages 用例覆盖。
func TestGetValuePageHashPages(t *testing.T) {
	store, client := newPageTestStore(t)
	const key = "cfg:map"
	ctx := context.Background()

	fields := make(map[string]interface{}, 150)
	for i := 0; i < 150; i++ {
		fields[fmt.Sprintf("f-%03d", i)] = fmt.Sprintf("v-%03d", i)
	}
	if err := client.HSet(ctx, key, fields).Err(); err != nil {
		t.Fatalf("seed hash: %v", err)
	}

	p, err := store.GetValuePage(ctx, key, ValuePageOption{Cursor: 0, Limit: 100})
	if err != nil {
		t.Fatalf("page err: %v", err)
	}
	if p.Total != 150 || p.HasMore || p.NextCursor != 0 {
		t.Fatalf("meta = %+v, want Total 150/HasMore false/NextCursor 0(miniredis HSCAN 全量返回)", p)
	}
	es := p.Value.([]HashEntry)
	if len(es) != 150 || es[0].Field != "f-000" || es[0].Value != "v-000" || es[149].Field != "f-149" {
		t.Fatalf("value len=%d first=%+v last=%+v", len(es), es[0], es[149])
	}
}

// TestGetValuePageStringWindows 验证 string 字节窗与截断标记。
func TestGetValuePageStringWindows(t *testing.T) {
	store, client := newPageTestStore(t)
	const key = "raw:big"
	ctx := context.Background()

	val := ""
	for i := 0; i < 3; i++ {
		val += strings.Repeat("a", 25) + strings.Repeat("b", 25) + strings.Repeat("c", 25) + strings.Repeat("d", 25) // 100B
	}
	if err := client.Set(ctx, key, val, 0).Err(); err != nil {
		t.Fatalf("seed string: %v", err)
	}

	// 截断路径：limit 100 → 前 100 字节，truncated=true
	p1, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 0, Limit: 100})
	if err != nil {
		t.Fatalf("window1 err: %v", err)
	}
	if p1.Total != 300 || !p1.Truncated || !p1.HasMore {
		t.Fatalf("window1 meta = %+v", p1)
	}
	if s := p1.Value.(string); len(s) != 100 || s != val[:100] {
		t.Fatalf("window1 value len=%d", len(s))
	}

	// 完整路径：窗口 ≥ 总长 → 不截断
	p2, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 0, Limit: 300})
	if err != nil {
		t.Fatalf("window2 err: %v", err)
	}
	if p2.Truncated || p2.HasMore || p2.Value.(string) != val {
		t.Fatalf("window2 meta = %+v", p2)
	}

	// offset 续读：后 200 字节
	p3, err := store.GetValuePage(ctx, key, ValuePageOption{Offset: 100, Limit: 300})
	if err != nil {
		t.Fatalf("window3 err: %v", err)
	}
	if p3.Truncated || p3.Value.(string) != val[100:] {
		t.Fatalf("window3 meta = %+v", p3)
	}
}

// TestGetValuePageLimitGuard 验证按类型上限校验。
func TestGetValuePageLimitGuard(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	seedList(t, client, "k:list", 3)
	if err := client.Set(ctx, "k:str", "abc", 0).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := store.GetValuePage(ctx, "k:list", ValuePageOption{Limit: 201}); !errors.Is(err, ErrBadLimit) {
		t.Fatalf("list limit 201 err = %v, want ErrBadLimit", err)
	}
	if _, err := store.GetValuePage(ctx, "k:str", ValuePageOption{Limit: MaxStringFetchBytes + 1}); !errors.Is(err, ErrBadLimit) {
		t.Fatalf("string limit 4MB+1 err = %v, want ErrBadLimit", err)
	}
	// 合理上限不报错
	if _, err := store.GetValuePage(ctx, "k:list", ValuePageOption{Limit: 200}); err != nil {
		t.Fatalf("list limit 200 err = %v", err)
	}
}

// TestGetValuePageOverflowGuard 验证 offset 极大(近 MaxInt64)时不回绕为负(执行期修订 R7)。
func TestGetValuePageOverflowGuard(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	seedList(t, client, "k:big-offset", 3)
	if err := client.Set(ctx, "k:str", "abc", 0).Err(); err != nil {
		t.Fatalf("seed string: %v", err)
	}
	big := int64(math.MaxInt64) - 10
	// list:offset 极大 → 空页、不 panic、Start/Total 语义不变
	p, err := store.GetValuePage(ctx, "k:big-offset", ValuePageOption{Offset: big, Limit: 100})
	if err != nil {
		t.Fatalf("list overflow err: %v", err)
	}
	if p.Total != 3 || p.Start != big || p.HasMore {
		t.Fatalf("list overflow meta = %+v, want Total 3/Start %d/HasMore false", p, big)
	}
	if vals := p.Value.([]string); len(vals) != 0 {
		t.Fatalf("list overflow value len=%d, want 0", len(vals))
	}
	// string 同理(GetRange 不回绕到负下标)
	p2, err := store.GetValuePage(ctx, "k:str", ValuePageOption{Offset: big, Limit: 100})
	if err != nil {
		t.Fatalf("string overflow err: %v", err)
	}
	if p2.Truncated || p2.Value.(string) != "" {
		t.Fatalf("string overflow meta = %+v, want 空串/Truncated false", p2)
	}
}

// TestGetValuePageMissingKey 验证 key 不存在 → TTL=-2（service 层据此 404）。
func TestGetValuePageMissingKey(t *testing.T) {
	store, _ := newPageTestStore(t)
	p, err := store.GetValuePage(context.Background(), "no:such:key", ValuePageOption{Limit: 100})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.TTL != -2 || p.Type != "none" {
		t.Fatalf("meta = %+v, want TTL -2/Type none", p)
	}
}

// TestGetValuePageTTL 验证 TTL 透传（含负值语义）与 miniredis 上的秒级一致性。
// 执行期修订：go-redis v9.21.0 的 DurationCmd 对 -1/-2 回包直接存为 time.Duration 纳秒值
// （不乘 time.Second），因此实现必须区分负值后映射为 -1/-2；此处显式断言两条语义。
func TestGetValuePageTTL(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	seedList(t, client, "k:ttl", 3)
	// 通过 go-redis Expire 设置过期;GetValuePage 返回剩余 TTL 秒数
	if err := client.Expire(ctx, "k:ttl", 30*time.Second).Err(); err != nil {
		t.Fatalf("expire: %v", err)
	}
	p, err := store.GetValuePage(ctx, "k:ttl", ValuePageOption{Limit: 100})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.TTL <= 0 || p.TTL > 30 {
		t.Fatalf("TTL = %d, want (0, 30]", p.TTL)
	}
	// 不设置 TTL 的 key → -1（永不过期）
	seedList(t, client, "k:forever", 3)
	p, err = store.GetValuePage(ctx, "k:forever", ValuePageOption{Limit: 100})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.TTL != -1 {
		t.Fatalf("TTL = %d, want -1", p.TTL)
	}
}

// TestGetValuePageStreamUnsupported 验证 stream 返回空 value 不报错。
func TestGetValuePageStreamUnsupported(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	if err := client.XAdd(ctx, &goredis.XAddArgs{Stream: "ev:stream", Values: map[string]interface{}{"f": "v"}}).Err(); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	p, err := store.GetValuePage(ctx, "ev:stream", ValuePageOption{Limit: 100})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.Type != "stream" || p.Value != nil || p.HasMore {
		t.Fatalf("meta = %+v, want Type stream/Value nil/HasMore false", p)
	}
}

// ── CompareAndSwap ────────────────────────────────────────────────────
//
// 这些用 miniredis(真实 Lua 执行)验证 CAS 语义,而非只测 fake:
// refresh token 轮换依赖它保证"同一 token 只能兑换一次",语义写错
// 会直接变成令牌重放漏洞。

func TestCompareAndSwap_SwapsWhenValueMatches(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	key := "refresh:1"

	if err := client.Set(ctx, key, "old-token", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}

	ok, err := store.CompareAndSwap(ctx, key, "old-token", "new-token", time.Hour)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !ok {
		t.Fatal("值匹配却未替换")
	}

	got, _ := client.Get(ctx, key).Result()
	if got != "new-token" {
		t.Fatalf("value = %q, want new-token", got)
	}
}

func TestCompareAndSwap_RejectsWhenValueDiffers(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	key := "refresh:1"
	_ = client.Set(ctx, key, "current", time.Hour).Err()

	ok, err := store.CompareAndSwap(ctx, key, "stale", "new-token", time.Hour)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if ok {
		t.Fatal("值不匹配却替换成功 —— refresh token 可被重放")
	}
	// 原值必须保持不变(不能被误写)。
	if got, _ := client.Get(ctx, key).Result(); got != "current" {
		t.Fatalf("值被意外修改为 %q", got)
	}
}

func TestCompareAndSwap_RejectsWhenKeyMissing(t *testing.T) {
	store, _ := newPageTestStore(t)

	ok, err := store.CompareAndSwap(context.Background(), "refresh:absent", "any", "new", time.Hour)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if ok {
		t.Fatal("键不存在却报告替换成功")
	}
}

// TestCompareAndSwap_ConcurrentOnlyOneWins 是轮换安全性的核心:
// 并发用同一个 old 值做 CAS,必须恰好有一个成功。
// 若实现退化为 GET+SET,多个调用都会成功 —— 一个 refresh token
// 就能换出多组有效令牌。
func TestCompareAndSwap_ConcurrentOnlyOneWins(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	key := "refresh:1"
	_ = client.Set(ctx, key, "old-token", time.Hour).Err()

	const n = 16
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // 尽量让所有 goroutine 同时冲进来
			results[idx], errs[idx] = store.CompareAndSwap(ctx, key, "old-token", fmt.Sprintf("new-%d", idx), time.Hour)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	wins := 0
	for _, ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("成功次数 = %d, want 恰好 1(多个成功 = refresh token 可重放)", wins)
	}
}

// TestCompareAndSwap_TTLApplied 新值需带上传入的 TTL,
// 否则轮换后的 refresh token 永不过期。
func TestCompareAndSwap_TTLApplied(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	key := "refresh:1"
	_ = client.Set(ctx, key, "old", time.Hour).Err()

	if _, err := store.CompareAndSwap(ctx, key, "old", "new", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > 30*time.Second {
		t.Fatalf("TTL = %v, want (0, 30s]", ttl)
	}
}

// TestSetRemove 验证 SREM 从集合移除成员。
func TestSetRemove(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	_ = client.SAdd(ctx, "sess:set", "a", "b", "c").Err()
	if err := store.SetRemove(ctx, "sess:set", "b"); err != nil {
		t.Fatalf("SetRemove: %v", err)
	}
	members, _ := client.SMembers(ctx, "sess:set").Result()
	if len(members) != 2 {
		t.Fatalf("members = %v, want 2", members)
	}
}

// TestMGet 验证批量读取 + miss 空串语义。
func TestMGet(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	_ = client.Set(ctx, "a", "1", 0).Err()
	_ = client.Set(ctx, "c", "3", 0).Err()
	vals, err := store.MGet(ctx, "a", "b", "c")
	if err != nil {
		t.Fatalf("MGet: %v", err)
	}
	if vals[0] != "1" || vals[1] != "" || vals[2] != "3" {
		t.Fatalf("vals = %q, want [1 \"\" 3]", vals)
	}
}

// TestScanKeyNames 验证 cursor 翻页收集纯 key 名 + 上限截断。
func TestScanKeyNames(t *testing.T) {
	store, client := newPageTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = client.Set(ctx, fmt.Sprintf("ua:%d", i), "x", 0).Err()
	}
	keys, err := store.ScanKeyNames(ctx, "ua:*", 100)
	if err != nil {
		t.Fatalf("ScanKeyNames: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("len(keys) = %d, want 5", len(keys))
	}
	capped, err := store.ScanKeyNames(ctx, "ua:*", 2)
	if err != nil {
		t.Fatalf("ScanKeyNames capped: %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("capped len = %d, want 2", len(capped))
	}
}
