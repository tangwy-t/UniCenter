package dockerevents

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// ── 事件留存守卫（本波：历史窗口跨 core 重启存活）─────────────────────────
//
// 四条口径逐条钉住：**重启存活**（核心断言：写入 → 落盘 → 新 Manager 水合 →
// 仍查得到）、**水合再裁**（超窗/超容量数据不得借水合回流）、**写侧双限与内存
// 同源**（Redis 里的 ZSET 自己就是裁剪过的）、**Redis 不可用时降级**（帧路径
// 绝不阻塞、内存历史照常）。全部走 miniredis（与 dockerstate 同源）。

// newRedis 起一台 miniredis 与它的客户端（留存测试的 Redis 侧替身）。
func newRedis(t *testing.T) (*miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

// newPersistEnv 在窗口夹具上装配留存（替身与 windowEnv 同源，只多这一件事）。
func newPersistEnv(t *testing.T, rdb goredis.UniversalClient, opts Options, hosts ...uint64) *mgrEnv {
	t.Helper()
	env := windowEnv(t, opts, hosts...)
	env.m.WithPersistence(rdb)
	return env
}

// 核心断言：历史窗口跨 core 重启存活 —— 写入若干事件 → 落盘 → 模拟重启（新
// Manager + 启动水合）→ Query 仍能查到**同样的一页**（形状/顺序/归属都不变，
// 前端零适配）。
func TestHistorySurvivesRestart(t *testing.T) {
	mr, rdb := newRedis(t)
	ctx := context.Background()
	opts := Options{RetainWindow: time.Hour, RetainDepth: 50}

	before := newPersistEnv(t, rdb, opts, 7, 9)
	before.emit(7, sessionsOf(7), 1, evAt(1790000001000, "container", "die", "web", "abcd1234"))
	before.emit(7, sessionsOf(7), 2, evAt(1790000002000, "image", "pull", "redis:7", "sha256:beef"))
	before.emit(9, sessionsOf(9), 1, evAt(1790000003000, "container", "start", "api", "9999"))
	// 确定性落盘（生产走 Run 起的后台协程；这里同步收干，断言才有对象）。
	before.m.drainPersist(ctx, defaultPersistQueueDepth)
	if n := before.m.PersistDropped(); n != 0 {
		t.Fatalf("通道没满就丢了 %d 条副本", n)
	}
	want, err := before.m.Query(QueryFilter{}, 0, "")
	if err != nil || want.Total != 3 {
		t.Fatalf("重启前的窗口应 3 条: %+v %v", want, err)
	}

	// 留存确实按主机分键落在 Redis 上（每主机一个 ZSET），且带 TTL 兜底。
	if n, _ := rdb.ZCard(ctx, eventsHistoryKey(7)).Result(); n != 2 {
		t.Fatalf("主机 7 的留存键应有 2 条，实际 %d", n)
	}
	if n, _ := rdb.ZCard(ctx, eventsHistoryKey(9)).Result(); n != 1 {
		t.Fatalf("主机 9 的留存键应有 1 条，实际 %d", n)
	}
	if ttl := mr.TTL(eventsHistoryKey(7)); ttl <= 0 {
		t.Fatalf("留存键必须带 TTL 兜底（不留永久键），实际 %v", ttl)
	}
	// 留存的时间口径与内存**同一份**：score 是 core 的**接收时刻**（这里是同一个
	// 假时钟瞬间），不是 agent 事件自带的 t —— 后者只有 1 秒差，前者一眼可辨。
	zs, _ := rdb.ZRangeWithScores(ctx, eventsHistoryKey(7), 0, -1).Result()
	if len(zs) != 2 {
		t.Fatalf("主机 7 的留存应 2 条，实际 %d", len(zs))
	}
	coreAt := float64(before.clk.Now().UnixMilli())
	for _, z := range zs {
		if z.Score != coreAt {
			t.Fatalf("score 必须是 core 接收时刻 %v（而非事件的 t），实际 %v", coreAt, z.Score)
		}
	}

	// ── 重启：全新 Manager（同样的替身与同一挂钟瞬间），启动期水合 ──────────
	after := newPersistEnv(t, rdb, opts, 7, 9)
	after.m.Hydrate(ctx)

	got, err := after.m.Query(QueryFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != want.Total || len(got.Items) != len(want.Items) {
		t.Fatalf("重启后历史条数必须不变: before=%d after=%d", want.Total, got.Total)
	}
	for i := range want.Items {
		w, g := want.Items[i], got.Items[i]
		if w.DeviceID != g.DeviceID || w.Hostname != g.Hostname || !reflect.DeepEqual(w.Item, g.Item) {
			t.Fatalf("第 %d 条重启后必须逐字段相同: before=%+v after=%+v", i, w, g)
		}
	}

	// 水合后的新事件照常入账并与水合历史共窗（重启不是断点）。
	after.emit(7, sessionsOf(7), 9, evAt(1790000004000, "container", "start", "after-restart", "x"))
	after.m.drainPersist(ctx, defaultPersistQueueDepth)
	page, err := after.m.Query(QueryFilter{}, 0, "")
	if err != nil || page.Total != 4 || page.Items[0].Item.ActorName != "after-restart" {
		t.Fatalf("水合后新事件必须照常入账并排在最新: %+v %v", page, err)
	}
	// 再水合回写后键里是 3 条（主机 7 的 2 条 + 新 1 条）。
	if n, _ := rdb.ZCard(ctx, eventsHistoryKey(7)).Result(); n != 3 {
		t.Fatalf("重启后的写入必须续上同一个键，实际 %d 条", n)
	}
}

// 水合**再裁一遍**：Redis 里躺着的陈旧数据（超窗/超容量）不得借水合回流到内存
// 窗口 —— 水合是「读副本」，不是「信副本」。
func TestHydratePrunesStaleAndOverCapacity(t *testing.T) {
	_, rdb := newRedis(t)
	ctx := context.Background()
	env := newPersistEnv(t, rdb, Options{RetainWindow: time.Minute, RetainDepth: 5}, 7)
	now := env.clk.Now().UnixMilli()

	// 绕过写侧裁剪直接塞进留存键：模拟「上一个进程写入时仍在窗内、此刻已过期」
	// 那类陈旧数据。
	mk := func(at int64, name string, seq uint64) goredis.Z {
		b, err := json.Marshal(historyRecord{Seq: seq, Hostname: "alpha",
			Item: evAt(at, "container", "start", name, "id")})
		if err != nil {
			t.Fatal(err)
		}
		return goredis.Z{Score: float64(at), Member: string(b)}
	}
	zs := []goredis.Z{}
	for i := range 3 { // 超窗：窗口 1 分钟，这些在 10 分钟前
		zs = append(zs, mk(now-10*60*1000+int64(i), "stale"+u64s(uint64(i)), uint64(i+1)))
	}
	for i := range 7 { // 窗内但超容量（限 5）
		zs = append(zs, mk(now-6000+int64(i*1000), "c"+u64s(uint64(i)), uint64(i+1)))
	}
	if err := rdb.ZAdd(ctx, eventsHistoryKey(7), zs...).Err(); err != nil {
		t.Fatal(err)
	}

	env.m.Hydrate(ctx)

	env.m.mu.Lock()
	n := len(env.m.rings[7])
	env.m.mu.Unlock()
	if n != 5 {
		t.Fatalf("水合必须**就地**过双限（而不是靠读侧过滤），ring=%d", n)
	}
	page, err := env.m.Query(QueryFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 {
		t.Fatalf("水合后应只剩 5 条，实际 %d", page.Total)
	}
	for _, e := range page.Items {
		if strings.HasPrefix(e.Item.ActorName, "stale") {
			t.Fatalf("超窗条目不得出现在水合后的窗口里: %+v", e.Item)
		}
	}
	if page.Items[0].Item.ActorName != "c6" || page.Items[4].Item.ActorName != "c2" {
		t.Fatalf("容量裁剪必须丢最旧（留最近 5 条）: %s … %s",
			page.Items[0].Item.ActorName, page.Items[4].Item.ActorName)
	}
}

// 写侧双限与内存窗口**同源**：留存键自己就是裁剪过的（时窗靠 ZREMRANGEBYSCORE、
// 容量靠 ZREMRANGEBYRANK），不是「写全量、读时才筛」。
func TestPersistPrunesWindowAndCapacityInRedis(t *testing.T) {
	_, rdb := newRedis(t)
	ctx := context.Background()
	env := newPersistEnv(t, rdb, Options{RetainWindow: time.Minute, RetainDepth: 5}, 7)
	for i := 1; i <= 12; i++ {
		env.emit(7, sessionsOf(7), uint64(i),
			evAt(int64(1790000000000+i*1000), "container", "start", "c"+u64s(uint64(i)), "id"))
	}
	env.m.drainPersist(ctx, defaultPersistQueueDepth)
	if n, _ := rdb.ZCard(ctx, eventsHistoryKey(7)).Result(); n != 5 {
		t.Fatalf("留存键必须按容量裁到 5，实际 %d", n)
	}
	// 推进时钟越过窗口后再写一条：超窗的老条目必须被时窗裁剪摘掉。
	env.clk.Advance(2 * time.Minute)
	env.emit(7, sessionsOf(7), 99, evAt(1790000000099000, "container", "start", "fresh", "id"))
	env.m.drainPersist(ctx, defaultPersistQueueDepth)
	if n, _ := rdb.ZCard(ctx, eventsHistoryKey(7)).Result(); n != 1 {
		t.Fatalf("超窗条目必须被裁掉（只剩新写的一条），实际 %d", n)
	}
}

// 降级：Redis 不可用时投递**绝不阻塞**（帧路径是 agent 读循环，一秒都不能停）、
// 内存历史与实时流照常、落盘失败只是日志、水合退化为「无事发生」。
func TestPersistDegradesWhenRedisDown(t *testing.T) {
	mr, rdb := newRedis(t)
	mr.SetError("ERR 模拟 Redis 故障") // 连接还在、每条命令都报错：与「Redis 抖动」同形

	ctx := context.Background()

	// 通道深度 4：灌 20 条也不阻塞 —— 溢出的只是**持久化副本**。
	env := newPersistEnv(t, rdb, Options{RetainWindow: time.Hour, RetainDepth: 10, PersistQueueDepth: 4}, 7)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 20; i++ {
			env.emit(7, sessionsOf(7), uint64(i),
				evAt(int64(1790000000000+i*1000), "container", "start", "c"+u64s(uint64(i)), "id"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Redis 不可用时帧路径被阻塞（留存不得反压主链路）")
	}
	if n := env.m.PersistDropped(); n == 0 {
		t.Fatal("通道溢出必须计数（丢弃是显式事实，不是静默）")
	}
	// 内存历史照常：容量限 10 → 最近 10 条，且最新那条在首位。
	page, err := env.m.Query(QueryFilter{}, 0, "")
	if err != nil || page.Total != 10 {
		t.Fatalf("内存历史必须照常（total=%d err=%v）", page.Total, err)
	}
	if page.Items[0].Item.ActorName != "c20" {
		t.Fatalf("实时入账必须照常: %+v", page.Items[0].Item)
	}
	// 落盘（失败）与水合（读失败）都必须安全返回，不 panic、不阻塞。
	env.m.drainPersist(ctx, defaultPersistQueueDepth)
	env.m.Hydrate(ctx)
	if page, err = env.m.Query(QueryFilter{}, 0, ""); err != nil || page.Total != 10 {
		t.Fatalf("降级路径不得动到内存窗口: total=%d err=%v", page.Total, err)
	}
}

// 未装配留存（rdb 为 nil / 未调用 WithPersistence）：整条链消失，行为与引入它
// 之前一致 —— 这是「把 Redis 那一侧整个删掉」的等价性守卫。
func TestPersistenceNotWiredIsNoop(t *testing.T) {
	env := windowEnv(t, Options{RetainWindow: time.Hour}, 7)
	env.m.WithPersistence(nil)
	env.emit(7, sessionsOf(7), 1, evAt(1790000001000, "container", "die", "web", "ab"))
	if n := env.m.PersistDropped(); n != 0 {
		t.Fatalf("未装配留存不该有「丢弃」这种账，实际 %d", n)
	}
	env.m.drainPersist(context.Background(), defaultPersistQueueDepth) // 空桶：立即返回
	env.m.Hydrate(context.Background())                                // 未装配：无事发生
	page, err := env.m.Query(QueryFilter{}, 0, "")
	if err != nil || page.Total != 1 {
		t.Fatalf("未装配留存时内存历史照常: total=%d err=%v", page.Total, err)
	}
}
