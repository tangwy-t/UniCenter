package collect

import (
	"math"
	"os"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// TestSampleValidate 是最关键的一条：采集结果必须能通过协议校验。
//
// 意义在于 Validate 失败 = core **丢弃整帧**。采集侧任何一处漏了 clamp
// （越界百分比、used > total、负速率）都会让整帧数据消失，
// 而现象只是「页面上指标断档」，极难定位。
func TestSampleValidate(t *testing.T) {
	c := New(Options{Interval: time.Second})
	// 采两帧：第二帧才带速率（首帧只播基线）
	if _, err := c.Sample(time.Now()); err != nil {
		t.Fatalf("第 1 帧: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	s, err := c.Sample(time.Now())
	if err != nil {
		t.Fatalf("第 2 帧: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("采集结果未通过协议校验（core 会丢整帧）: %v", err)
	}
	if s.T <= 0 {
		t.Fatal("T 未设置")
	}
}

// TestSampleHasCoreMetrics 断言「必然可得」的指标确实被采到了。
//
// 只断言**与环境无关**的字段：CPU/内存/负载/进程数/运行时长在 Linux/macOS
// 上恒有值。磁盘、网卡、温度依赖具体机器，不能硬断言（容器里可能没有）。
func TestSampleHasCoreMetrics(t *testing.T) {
	c := New(Options{Interval: time.Second})
	s, err := c.Sample(time.Now())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}

	if s.CPUUsedPercent < 0 || s.CPUUsedPercent > 100 {
		t.Errorf("CPU 使用率越界: %v", s.CPUUsedPercent)
	}
	if s.MemUsedPercent < 0 || s.MemUsedPercent > 100 {
		t.Errorf("内存使用率越界: %v", s.MemUsedPercent)
	}
	if s.UptimeSec <= 0 {
		t.Errorf("运行时长应大于 0，实际 %d", s.UptimeSec)
	}
	if s.ProcCount <= 0 {
		t.Errorf("进程数应大于 0，实际 %d", s.ProcCount)
	}
}

// TestRateDelta 验证速率是**差量**而不是累计值。
//
// 这是最容易写错的地方：直接把 gopsutil 的累计计数器上报，
// 会得到「开机至今的总字节数」，在页面上表现为一个持续增长但不反映
// 当前负载的巨大数字。
func TestRateDelta(t *testing.T) {
	base := uint64(1_000_000)
	// 1 秒内增加 500 字节 → 500 B/s
	if got := rate(base+500, base, 1); math.Abs(got-500) > 0.01 {
		t.Errorf("rate = %v, 期望 500", got)
	}
	// 2 秒内增加 500 字节 → 250 B/s（必须除以 dt，不能只算差值）
	if got := rate(base+500, base, 2); math.Abs(got-250) > 0.01 {
		t.Errorf("rate(dt=2) = %v, 期望 250", got)
	}
}

// TestRateCounterWrap 验证计数器回绕/重置不产生负值或天文数字。
//
// 场景真实存在：机器重启、网卡重载、容器重建都会让计数器归零。
// 若不处理，Δ 为负会算出负速率（被 Validate 拒 → 丢整帧），
// 或 Δ 极大算出荒诞速率（污染图表量程）。
func TestRateCounterWrap(t *testing.T) {
	if got := rate(100, 1_000_000, 1); got != 0 {
		t.Errorf("回绕时应返回 0，实际 %v", got)
	}
	// 注意：cur > prev 且增量很大**不是**回绕，而是真实的突发流量
	// （1 秒内 1MB）。回绕的定义是「读数变小」，只有那种才是基线失效。
	if got := rate(1_000_000, 100, 1); got <= 0 {
		t.Errorf("读数变大的大幅增量是合法速率，不应归零，实际 %v", got)
	}
	// dt <= 0 无意义，必须避开除零
	if got := rate(1000, 100, 0); got != 0 {
		t.Errorf("dt=0 时应返回 0，实际 %v", got)
	}
	if got := rate(1000, 100, -1); got != 0 {
		t.Errorf("dt<0 时应返回 0，实际 %v", got)
	}
}

// TestRateRejectsAbsurd 验证明显不合理的速率被归零。
//
// 阈值存在的意义：计数器被重置后若从 0 跳到极大值，Δ 会是一个
// 天文数字。画到图上会把纵轴量程彻底带偏，让真实流量压成一条平线。
func TestRateRejectsAbsurd(t *testing.T) {
	if got := rate(1e18, 0, 1); got != 0 {
		t.Errorf("荒谬速率应归零，实际 %v", got)
	}
	if got := rate(2e16, 0, 1); got != 0 {
		t.Errorf("超过 1e15 的速率应归零，实际 %v", got)
	}
	// 真实世界的高速率（如 100 Gbps ≈ 1.25e10 B/s）必须保留
	if got := rate(1.25e10, 0, 1); got == 0 {
		t.Error("100Gbps 量级的真实速率不应被误杀")
	}
}

// TestClampPercent 验证百分比夹取。
//
// 必须夹而不是靠 Validate 拒：一个末位浮点误差丢掉整帧数据不划算。
func TestClampPercent(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-0.0001, 0},
		{0, 0},
		{50.555, 50.56},
		{100, 100},
		{100.0001, 100},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
	}
	for _, tc := range cases {
		if got := clampPercent(tc.in); got != tc.want {
			t.Errorf("clampPercent(%v) = %v, 期望 %v", tc.in, got, tc.want)
		}
	}
}

// TestSanitizeFixesInvalidSample 验证 sanitize 能救回「本来会被拒」的样本。
func TestSanitizeFixesInvalidSample(t *testing.T) {
	bad := &agentproto.MetricsSample{
		T:              time.Now().UnixMilli(),
		CPUUsedPercent: 150, // 越界
		MemUsedPercent: -5,  // 负值
		MemUsedMB:      -1,  // 负值
		Disks: []agentproto.DiskMetric{
			// used > total：协议硬约束，不夹住会被拒
			{Mountpoint: "/", TotalGB: 10, UsedGB: 20, UsedPercent: 200},
		},
		DiskIO: []agentproto.DiskIOMetric{{Name: "sda", ReadBytesPerSec: -100}},
		NICs:   []agentproto.NICMetric{{Name: "eth0", RXBytesPerSec: -1}},
	}
	// 先确认它确实是「非法」的（否则本测试没有意义）
	if err := bad.Validate(); err == nil {
		t.Fatal("构造的样本本应非法，但 Validate 通过了 —— 测试前提失效")
	}

	sanitize(bad)

	if err := bad.Validate(); err != nil {
		t.Fatalf("sanitize 后仍非法: %v", err)
	}
	if bad.CPUUsedPercent != 100 {
		t.Errorf("CPU 应夹到 100，实际 %v", bad.CPUUsedPercent)
	}
	if bad.Disks[0].UsedGB != bad.Disks[0].TotalGB {
		t.Errorf("used 应夹到 total，实际 used=%v total=%v",
			bad.Disks[0].UsedGB, bad.Disks[0].TotalGB)
	}
}

// TestFirstFrameOmitsRates 验证首帧不产生速率。
//
// 首帧没有基线，此时给出 0 是被允许的（速率字段非指针），
// 但**绝不能**把累计值当速率上报 —— 那条假线会从 0 跳到几十 GB/s。
func TestFirstFrameOmitsRates(t *testing.T) {
	c := New(Options{Interval: time.Second})
	s, err := c.Sample(time.Now())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	for _, d := range s.DiskIO {
		if d.ReadBytesPerSec != 0 || d.WriteBytesPerSec != 0 {
			t.Errorf("首帧 %s 不应有速率: read=%v write=%v",
				d.Name, d.ReadBytesPerSec, d.WriteBytesPerSec)
		}
	}
	for _, n := range s.NICs {
		if n.RXBytesPerSec != 0 || n.TXBytesPerSec != 0 {
			t.Errorf("首帧 %s 不应有速率: rx=%v tx=%v",
				n.Name, n.RXBytesPerSec, n.TXBytesPerSec)
		}
	}
}

// TestIOTimePercent 验证 IO 繁忙度（%util）的差量与夹取。
//
// 语义（同 iostat -x 的 %util）：设备忙碌毫秒差量 ÷ 经过毫秒 × 100。
// 必须夹到 [0,100]：多队列设备会把并发请求的忙碌时间累加成超过实际
// 经过时间的值，不夹会出现 >100% 的百分比，被协议层拒掉整帧。
func TestIOTimePercent(t *testing.T) {
	c := New(Options{Interval: 2 * time.Second})

	// 采两帧建立基线并产出 %util
	if _, err := c.Sample(time.Now()); err != nil {
		t.Fatalf("第 1 帧: %v", err)
	}
	// 制造真实磁盘写（工作区在真实盘上）
	f, err := os.CreateTemp(".", "iotime-*.bin")
	if err != nil {
		t.Fatalf("创建临时文件: %v", err)
	}
	name := f.Name()
	defer os.Remove(name)
	buf := make([]byte, 1<<20)
	for i := 0; i < 150; i++ {
		f.Write(buf)
	}
	f.Sync()
	f.Close()

	time.Sleep(1200 * time.Millisecond)

	s, err := c.Sample(time.Now())
	if err != nil {
		t.Fatalf("第 2 帧: %v", err)
	}
	for _, d := range s.DiskIO {
		if d.IOTimePercent < 0 || d.IOTimePercent > 100 {
			t.Errorf("%s 的 io_time_percent 越界: %v", d.Name, d.IOTimePercent)
		}
	}
}
