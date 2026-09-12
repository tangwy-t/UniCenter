package util

import "testing"

// TestRound2 验证 2 位小数量化:常规值、长尾小数、进位边界与负数。
func TestRound2(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{12.266666666666667, 12.27}, // 均值长尾 → 进位
		{1.2333333333333334, 1.23},  // 均值长尾 → 舍去
		{2.345678, 2.35},            // ns→ms 换算长尾
		{3.9699999999999998, 3.97},  // 分位数插值长尾
		{80.01666666666667, 80.02},  // 磁盘均值
		{0.6666666666666666, 0.67},  // 负载均值
		{-1.235, -1.24},             // 负数:half-away-from-zero(实际指标不会出现)
		{1234567.891, 1234567.89},   // 大数不溢出
	}
	for _, c := range cases {
		if got := Round2(c.in); got != c.want {
			t.Errorf("Round2(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestRound2Monotonic 验证单调性:量化不得破坏已排序序列的顺序
// (分位数 p50<=p95<=p99<=max 依赖该性质)。
func TestRound2Monotonic(t *testing.T) {
	prev := Round2(0)
	for i := 1; i <= 10000; i++ {
		v := float64(i) / 997.0 // 制造密集长尾小数
		cur := Round2(v)
		if cur < prev {
			t.Fatalf("单调性破坏: Round2(%v)=%v < 前值 %v", v, cur, prev)
		}
		prev = cur
	}
}
