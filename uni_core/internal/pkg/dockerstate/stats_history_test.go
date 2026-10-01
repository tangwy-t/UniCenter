package dockerstate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 容器 stats 历史留存（P2）的测试：写入投影、环形裁剪、容器消失后序列冻结、
// TTL 兜底、Purge 精确清理、紧凑形态与体积守卫。

// statsFrame 造一帧带 N 个容器的快照：cpu 全部取 cpuBase（帧标识），mem 按容器
// 序号错开（容器标识）—— 两个维度分开，断言就能同时钉住「帧没串」和「容器没串」。
func statsFrame(cids []string, cpuBase float64, t int64) *agentproto.DockerState {
	st := &agentproto.DockerState{T: t, DockerOK: true}
	st.Containers = make([]agentproto.DockerContainer, 0, len(cids))
	for i, cid := range cids {
		st.Containers = append(st.Containers, agentproto.DockerContainer{
			ID: cid, Name: "c-" + cid, State: "running",
			CPUPercent: cpuBase, MemUsageMB: 100 + float64(i), MemLimitMB: 512,
		})
	}
	return st
}

// newStatsStore 构造 miniredis 上的留存存储：mr 供 FastForward 确定性驱动 TTL，
// rdb 供直接检查键形态（LLen/LRange/Exists）。
func newStatsStore(t *testing.T) (*StatsHistoryStore, *miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStatsHistoryStore(rdb), mr, rdb
}

// TestStatsHistoryRecordMultiContainer 钉住 ingest 投影的基本语义：
// 多容器 × 多帧各自成环，读回升序、逐字段对得上（与快照同口径）。
func TestStatsHistoryRecordMultiContainer(t *testing.T) {
	sh, _, _ := newStatsStore(t)
	ctx := context.Background()
	base := time.UnixMilli(1730000000000)

	// 两台容器、三帧快照（每帧递增的 cpu 便于断言）。
	for f := 0; f < 3; f++ {
		at := base.Add(time.Duration(f) * 30 * time.Second)
		st := statsFrame([]string{"aaaa", "bbbb"}, float64(f)*10, at.UnixMilli())
		if err := sh.Record(ctx, 7, st, at); err != nil {
			t.Fatal(err)
		}
	}
	for off, cid := range []string{"aaaa", "bbbb"} {
		got, err := sh.History(ctx, 7, cid)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("容器 %s 应有 3 个样本, got %d", cid, len(got))
		}
		// 升序 + 时刻/读数逐帧对上（f=0 的帧 cpu=0、f=2 的帧 cpu=20；
		// mem 按容器序号错开，钉住两个容器的序列没有串）。
		for f, sm := range got {
			wantT := base.Add(time.Duration(f) * 30 * time.Second).UnixMilli()
			if sm.T != wantT {
				t.Fatalf("容器 %s 样本 %d 时刻不符: got %d want %d（升序 + 收帧时刻）", cid, f, sm.T, wantT)
			}
			if sm.CPUPercent != float64(f)*10 {
				t.Fatalf("容器 %s 样本 %d cpu 不符: got %v", cid, f, sm.CPUPercent)
			}
			if sm.MemUsageMB != 100+float64(off) {
				t.Fatalf("容器 %s 样本 %d mem 不符: got %v", cid, f, sm.MemUsageMB)
			}
			if sm.MemLimitMB != 512 {
				t.Fatalf("样本 %d memLimit 不符: got %v", f, sm.MemLimitMB)
			}
		}
	}
}

// TestStatsHistoryRingTrimsToKeep 钉住环形容量纪律：写 70 帧后恰留最新 60 条
// （裁剪与写入同事务 —— 容量账不靠调用方自觉，与 docker:cmd:recent 同一句话）。
func TestStatsHistoryRingTrimsToKeep(t *testing.T) {
	sh, _, rdb := newStatsStore(t)
	ctx := context.Background()
	base := time.UnixMilli(1730000000000)

	for f := 0; f < StatsHistoryKeep+10; f++ {
		at := base.Add(time.Duration(f) * 30 * time.Second)
		if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa"}, float64(f), at.UnixMilli()), at); err != nil {
			t.Fatal(err)
		}
	}
	n, err := rdb.LLen(ctx, StatsRingKey(7, "aaaa")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != StatsHistoryKeep {
		t.Fatalf("环形容量必须恒 ≤ %d, got %d", StatsHistoryKeep, n)
	}
	// 保留的是**最新**的 60 条：最早的 10 帧（cpu=0..9）被裁掉，序列从 cpu=10 起。
	got, _ := sh.History(ctx, 7, "aaaa")
	if len(got) != StatsHistoryKeep || got[0].CPUPercent != 10 {
		t.Fatalf("裁剪必须保留最新样本, first=%+v len=%d", got[0], len(got))
	}
}

// TestStatsHistoryFrozenWhenContainerGone 钉住「容器消失后序列冻结」：
//   - 从快照里消失（被 docker rm）：序列不再增长、内容原样冻结；
//   - 停止但还在清单里（exited）：同样不写 —— 冻结在最后一次真实读数上，
//     这正是「死前发生了什么」的排障窗口。
func TestStatsHistoryFrozenWhenContainerGone(t *testing.T) {
	sh, _, _ := newStatsStore(t)
	ctx := context.Background()
	base := time.UnixMilli(1730000000000)

	for f := 0; f < 3; f++ {
		at := base.Add(time.Duration(f) * 30 * time.Second)
		if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa", "bbbb"}, float64(f), at.UnixMilli()), at); err != nil {
			t.Fatal(err)
		}
	}
	frozen, _ := sh.History(ctx, 7, "aaaa")
	if len(frozen) != 3 {
		t.Fatalf("前置失败: 应有 3 个样本, got %d", len(frozen))
	}

	// bbbb 停止（仍在清单、state=exited）；aaaa 整个消失（被删除）。
	stopped := statsFrame([]string{"bbbb"}, 99, base.Add(3*30*time.Second).UnixMilli())
	stopped.Containers[0].State = "exited"
	at := base.Add(3 * 30 * time.Second)
	if err := sh.Record(ctx, 7, stopped, at); err != nil {
		t.Fatal(err)
	}
	// 再来一帧只有 cccc 的，确认旁路容器也不受牵连。
	if err := sh.Record(ctx, 7, statsFrame([]string{"cccc"}, 1, base.Add(4*30*time.Second).UnixMilli()),
		base.Add(4*30*time.Second)); err != nil {
		t.Fatal(err)
	}

	// 冻结 = 长度不变 + 最后一帧的读数原样（不被后来的帧覆盖成零值）。
	got, _ := sh.History(ctx, 7, "aaaa")
	if len(got) != len(frozen) {
		t.Fatalf("消失容器的序列必须冻结在 %d 条, got %d", len(frozen), len(got))
	}
	for i := range frozen {
		if got[i] != frozen[i] {
			t.Fatalf("冻结序列的内容不得被改写: [%d] got %+v want %+v", i, got[i], frozen[i])
		}
	}
	if got, _ := sh.History(ctx, 7, "bbbb"); len(got) != 3 {
		t.Fatalf("停止（exited）容器的序列同样冻结, got %d", len(got))
	}
	if got, _ := sh.History(ctx, 7, "cccc"); len(got) != 1 {
		t.Fatalf("新容器照常开记, got %d", len(got))
	}
}

// TestStatsHistoryCompactElements 钉住紧凑形态 + 体积守卫：
// 元素是 JSON 数组（无键名、无对象壳），一条样本 ≤ 44B —— 60×N 的体积敏感
// 路径上，谁把形态改回逐样本对象（体积 ~2.2×）这条测试立刻红。
func TestStatsHistoryCompactElements(t *testing.T) {
	sh, _, rdb := newStatsStore(t)
	ctx := context.Background()
	at := time.UnixMilli(1730000000123)

	if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa"}, 3.14, at.UnixMilli()), at); err != nil {
		t.Fatal(err)
	}
	raw, err := rdb.LRange(ctx, StatsRingKey(7, "aaaa"), 0, -1).Result()
	if err != nil || len(raw) != 1 {
		t.Fatalf("读取序列元素失败: %v %v", raw, err)
	}
	el := raw[0]
	if !strings.HasPrefix(el, "[") || !strings.HasSuffix(el, "]") {
		t.Fatalf("元素必须是 JSON 数组形态, got %q", el)
	}
	if strings.Contains(el, ":") || strings.Contains(el, "{") {
		t.Fatalf("元素不得带键名/对象壳（逐样本对象形态体积 ~2.2×）, got %q", el)
	}
	if len(el) > 44 {
		t.Fatalf("一条样本应 ≤ 44B（体积守卫, 见键设计处的体积账）, got %dB: %q", len(el), el)
	}
	// 往返一致（含 round2 浮点）。
	sm, ok := decodeStatsSample(el)
	if !ok || sm.T != 1730000000123 || sm.CPUPercent != 3.14 || sm.MemLimitMB != 512 {
		t.Fatalf("编解码往返不符: %+v ok=%v", sm, ok)
	}
}

// TestStatsHistoryTTL 钉住 TTL 兜底：容器消失/主机停报后键在 StatsHistoryTTL
// 内自然消失 —— 留存键不设 TTL 就会在容器增删中永久泄漏。
func TestStatsHistoryTTL(t *testing.T) {
	sh, mr, rdb := newStatsStore(t)
	ctx := context.Background()
	at := time.UnixMilli(1730000000000)
	if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa"}, 1, at.UnixMilli()), at); err != nil {
		t.Fatal(err)
	}
	// 活跃写入会续命：多写一帧后 TTL 仍是整窗（不是从第一帧起算）。
	if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa"}, 2, at.Add(30*time.Second).UnixMilli()),
		at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	// miniredis 的 FastForward 直接拨快时钟，确定性驱动过期（与票据测试同款）。
	mr.FastForward(StatsHistoryTTL - time.Minute)
	if n, _ := rdb.LLen(ctx, StatsRingKey(7, "aaaa")).Result(); n != 2 {
		t.Fatalf("TTL 未到时序列必须还在, got %d", n)
	}
	mr.FastForward(time.Minute)
	if got, _ := sh.History(ctx, 7, "aaaa"); len(got) != 0 {
		t.Fatalf("TTL 到期后序列必须消失（空数组而非报错）, got %d", len(got))
	}
}

// TestStatsHistoryPurge 钉住设备删除的连带清理：桶成员的序列 + 桶一并删掉。
func TestStatsHistoryPurge(t *testing.T) {
	sh, _, rdb := newStatsStore(t)
	ctx := context.Background()
	at := time.UnixMilli(1730000000000)
	if err := sh.Record(ctx, 7, statsFrame([]string{"aaaa", "bbbb"}, 1, at.UnixMilli()), at); err != nil {
		t.Fatal(err)
	}
	if err := sh.Purge(ctx, 7); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{statsBucketKey(7), StatsRingKey(7, "aaaa"), StatsRingKey(7, "bbbb")} {
		if n, _ := rdb.Exists(ctx, key).Result(); n != 0 {
			t.Fatalf("Purge 后键 %s 必须删除", key)
		}
	}
	// 幂等：对没有留存的设备再清一次不报错（删除路径的重复调用是常态）。
	if err := sh.Purge(ctx, 9); err != nil {
		t.Fatalf("Purge 对无留存设备必须幂等: %v", err)
	}
}

// TestStatsHistoryEmptyAndCorrupt 钉住读面两条边界：
//   - 无历史（从未记录/已过期）→ 空切片非 nil、非错误；
//   - 单条坏元素跳过，不炸整条序列（与 metricshistory 同款纪律）。
func TestStatsHistoryEmptyAndCorrupt(t *testing.T) {
	sh, _, rdb := newStatsStore(t)
	ctx := context.Background()

	got, err := sh.History(ctx, 7, "nope")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("无历史必须返回空切片非 nil/非错误: %v %v", got, err)
	}
	if got, err := sh.History(ctx, 7, ""); err != nil || len(got) != 0 {
		t.Fatalf("空容器 id 必须直接空返回: %v %v", got, err)
	}

	// 手工塞 3 条（中间一条是损坏的旧格式），读回应跳过坏条、其余照常。
	key := StatsRingKey(7, "aaaa")
	for _, el := range []string{
		encodeStatsSample(StatsSample{T: 2, CPUPercent: 2}),
		`{"t":1,"cpu_percent":1}`, // 旧对象形态（或任何损坏）：解码失败
		encodeStatsSample(StatsSample{T: 1, CPUPercent: 1}),
	} {
		if err := rdb.RPush(ctx, key, el).Err(); err != nil { // RPush 保证次序可控（头新尾旧）
			t.Fatal(err)
		}
	}
	got, err = sh.History(ctx, 7, "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].T != 1 || got[1].T != 2 {
		t.Fatalf("坏元素必须被跳过且其余升序: %+v", got)
	}
}
