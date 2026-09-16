package agentmetrics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 本文件是 Plan 2G Task 1 的产物：把「热层窗口覆盖多久」这件事收敛成**单一公式与
// 单一常量**，并钉住「服务侧回填/回退假设的窗口」与它的关系。
//
// 缺陷形态：`MaxPoints` 是**条数**上限，在启动时按**当轮** `sys.agent.reportInterval`
// 冻结；`reportInterval` 却可热更。把间隔从 10s 调到 5s 后，热层实际只保留
// `MaxPoints × 5s ≈ 14.4h` 的点，而服务侧仍按 24h 回填/回退 —— 那一段**必然**读不到点，
// 表现为空桶 / `HoursSkipped++`，既不报错、也没有任何读数指向它。
// 时间跨度 = `maxPoints × step` 是本包对外唯一的算法（`RawWindowSpan`）。

// TestRawWindowSpanFormula 是热层窗口**时间跨度**的单一公式守卫：maxPoints × step。
//
// 为什么需要一个专门的函数而不是各处手算：跨度由两个**互相独立**的来源决定 ——
// 容量 MaxPoints（启动时冻结的条数上限）与 Step（可热更的上报间隔）。两侧各算各的，
// 就会出现本任务要收掉的形态：wireup 的容量推导与 service 的补齐窗口各写一份
// 「24h」，而热层实际覆盖多久谁都没算过。
//
// 用例里的 8640 = 24h/10s（不含 wireup 那 1.2 倍余量的理论条数）：
//   - 8640 × 10s == 24h —— 容量与间隔匹配时的名义跨度；
//   - 8640 × 5s  == 12h —— **这就是那个坑**：间隔调小一半，跨度跟着减半，
//     而 MaxPoints 不会跟着变（它是条数上限，不是时间上限）。
func TestRawWindowSpanFormula(t *testing.T) {
	cases := []struct {
		name      string
		maxPoints int64
		step      time.Duration
		want      time.Duration
	}{
		{"容量与 10s 间隔匹配（理论条数）", 8640, 10 * time.Second, 24 * time.Hour},
		{"间隔热更为 5s、容量不动（那个坑）", 8640, 5 * time.Second, 12 * time.Hour},
		{"生产容量（含 1.2 余量）配 10s", 10368, 10 * time.Second, 28*time.Hour + 48*time.Minute},
		{"生产容量配热更后的 5s（实际只剩 14.4h）", 10368, 5 * time.Second, 14*time.Hour + 24*time.Minute},
		{"容量为 0（未配置）", 0, 10 * time.Second, 0},
		{"step 为 0（未配置，不做乘法）", 8640, 0, 0},
		// 非法输入（负数容量）不得放大成一个负跨度：调用方拿它去和窗口比较会得出
		// 「跨度无限大」的相反结论。归 0，与「未配置」同语义。
		{"容量为负（非法输入归 0）", -1, 10 * time.Second, 0},
		{"step 为负（非法输入归 0）", 8640, -time.Second, 0},
	}
	for _, c := range cases {
		if got := RawWindowSpan(c.maxPoints, c.step); got != c.want {
			t.Errorf("%s: RawWindowSpan(%d, %s) = %s, want %s",
				c.name, c.maxPoints, c.step, got, c.want)
		}
	}
}

// TestRawBootstrapWindowIsSingleSource 把「服务侧回填/回退所假设的时间跨度」钉成
// **字面量契约**：24h（= raw 点在 Redis 里的保留期，spec §7.1）。
//
// 为什么期望值写字面量而不复用常量自身：那样改错了也自洽（先例：
// TestLatestKeyMatchesSpecContract 用字符串字面量钉住 Redis 键名）。
// 这个常量必须是**单一来源**：service 的 bootstrapWindow（补齐/回填/回退的窗口）与
// wireup 的容量推导都引用它。service 侧的「逐字引用」由 service 包的
// TestBootstrapWindowIsSingleSource 断言（依赖方向决定了本包看不到它）。
func TestRawBootstrapWindowIsSingleSource(t *testing.T) {
	if RawBootstrapWindow != 24*time.Hour {
		t.Fatalf("RawBootstrapWindow = %s, want 24h（回填/回退窗口与容量推导的共同假设，spec §7.1）",
			RawBootstrapWindow)
	}
}

// TestRawMaxPointsKeepsWindowAtLeastBootstrap 是「默认配置下自洽」的守卫。
//
// 装配按 `RawMaxPoints(Step)` 推导容量，必须保证**任意**合法 Step 下
// `RawWindowSpan(MaxPoints, Step) ≥ RawBootstrapWindow` —— 这正是「默认 reportInterval
// 不产生噪声」的结构性依据：余量（1.2）一旦被改小到 ≤ 1.0，跨度就会短于回填假设，
// 而那时的症状（空桶 / HoursSkipped）不会指向容量推导。
//
// 期望值用**字面量公式** `ceil(24h/Step × 1.2)` 独立算出（不复用实现），
// 所以把余量改小、把 24h 换成别的窗口、或漏掉向上取整都会让这条断言变红。
func TestRawMaxPointsKeepsWindowAtLeastBootstrap(t *testing.T) {
	steps := []time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second,
		30 * time.Second, 45 * time.Second, time.Minute, 90 * time.Second, 2 * time.Minute,
	}
	for _, step := range steps {
		got := RawMaxPoints(step)
		want := int64(math.Ceil((24 * time.Hour).Seconds() / step.Seconds() * 1.2))
		if got != want {
			t.Fatalf("RawMaxPoints(%s) = %d, want %d（公式：ceil(24h/Step × 1.2)）", step, got, want)
		}
		span := RawWindowSpan(got, step)
		if span < RawBootstrapWindow {
			t.Fatalf("Step=%s 时跨度 %s < %s：默认装配会短于回填假设（1.2 倍余量被改坏了？）",
				step, span, RawBootstrapWindow)
		}
		// 上界同样要有界：余量是「够用就好」，不是越大越好（容量直接决定每台设备
		// 在 Redis 里的点数，放大会把热层成本乘上去）。ceil 至多多留一个 step。
		if limit := time.Duration(float64(RawBootstrapWindow)*rawWindowHeadroom) + step; span > limit {
			t.Fatalf("Step=%s 时跨度 %s > 上界 %s：余量 %v 被放大了？",
				step, span, limit, rawWindowHeadroom)
		}
	}

	// 非正 Step：不做除法（NaN/Inf 换算成 int64 会得到一个荒唐的巨大容量）。
	for _, step := range []time.Duration{0, -time.Second} {
		if got := RawMaxPoints(step); got != 0 {
			t.Fatalf("RawMaxPoints(%s) = %d, want 0（非正 Step 不做除法）", step, got)
		}
	}
}

// TestRawStoreMaxPointsReturnsFrozenCapacity 钉住「冻结容量的唯一只读出口」的语义
// （Plan 2G Task 2：flush 的每轮运行期比对靠它取证）。
//
// 为什么这条断言不可省：运行期比对的两个数里，容量必须是**真正被用来构造滚动窗的那一个**
// （启动时按当轮的 reportInterval 冻结）。访问器一旦变成「按当前配置重算」「读当前点位数」
// 或「返回一个兜底默认值」，比对就会在真实错配下自洽 —— 而那正是它唯一的失效模式，
// 且症状与「本来就一致」完全一样（都不出声）。
func TestRawStoreMaxPointsReturnsFrozenCapacity(t *testing.T) {
	const frozen = int64(10368) // = RawMaxPoints(10s)，生产默认间隔下的容量
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// 容量按 10s 冻结（生产装配的形态）。
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: frozen})
	if got := store.MaxPoints(); got != frozen {
		t.Fatalf("MaxPoints() = %d, want %d（必须原样返回装配时冻结的容量）", got, frozen)
	}

	// 访问器是**只读**的：写入（会触发 LTRIM 裁剪）之后读数不得改变。
	for i := 0; i < 3; i++ {
		if err := store.Append(context.Background(), 1, &agentproto.MetricsSample{T: int64(i) * 10000}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := store.MaxPoints(); got != frozen {
		t.Fatalf("写入后 MaxPoints() = %d, want %d（访问器不得变成「当前点位数」或别的动态量）",
			got, frozen)
	}

	// 未配置（0）原样返回 0：调用方据此得到 RawWindowSpan == 0 →「跨度 < 回填假设」——
	// 那是**正确**的结论（容量为 0 的窗什么都留不住），不得在这里被悄悄兜成默认容量。
	if got := NewRawStore(rdb, RawOptions{}).MaxPoints(); got != 0 {
		t.Fatalf("未配置容量的 MaxPoints() = %d, want 0（不得兜底成默认容量）", got)
	}
}
