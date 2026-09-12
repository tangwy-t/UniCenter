// Package metricshistory 提供监控时序数据的共享基础设施:
// Redis 滚动窗口存储(Window)与绝对时间对齐的分桶计算(Buckets)。
// serverstats / sqlhistory / database.SQLStats 三处历史聚合共用,
// 各领域只保留自己的 Point / Bucket / Snapshot 契约与累加逻辑。
package metricshistory

import (
	"fmt"
	"time"
)

// maxBuckets 单次查询最多返回的桶数;窗口过大或步长过细时
// AlignBuckets 自动放大 step 控制响应体积(全仓同值 4000)。
const maxBuckets = 4000

// Buckets 描述一次聚合查询的分桶方案(纯值,无锁)。
type Buckets struct {
	WindowSec int64 // 生效窗口秒数
	StepSec   int64 // 生效步长(可能因桶数上限被放大)
	T0Unix    int64 // 首桶起点:绝对时间对齐后的 epoch 秒
	N         int64 // 桶数
	Now       time.Time
}

// AlignBuckets 校验 window/step,以 time.Now() 为查询时刻返回分桶方案。
func AlignBuckets(window, step time.Duration) (Buckets, error) {
	return AlignBucketsAt(time.Now(), window, step)
}

// AlignBucketsAt 校验 window/step,以 now 为查询时刻返回分桶方案。
// 规则与历史三处实现逐一核对:
//   - step 必须 >= 1s,且 <= window;
//   - 桶数 = windowSec/stepSec,超过 maxBuckets 时向上放大 step;
//   - t0 = (now-window) 对 stepSec 向下取整(两次轮询桶划分稳定);
//   - window 为 1.5s 等非整秒值时按 int64(window.Seconds()) 截断,与旧行为一致。
func AlignBucketsAt(now time.Time, window, step time.Duration) (Buckets, error) {
	if window <= 0 || step <= 0 {
		return Buckets{}, fmt.Errorf("metricshistory: window 与 step 必须为正数")
	}
	if step < time.Second {
		return Buckets{}, fmt.Errorf("metricshistory: step 最小 1s")
	}
	if step > window {
		return Buckets{}, fmt.Errorf("metricshistory: step 不得超过 window")
	}

	stepSec := int64(step.Seconds())
	windowSec := int64(window.Seconds())
	if windowSec < stepSec {
		return Buckets{}, fmt.Errorf("metricshistory: window 必须 >= step")
	}

	nBuckets := windowSec / stepSec
	if nBuckets > maxBuckets {
		stepSec = (windowSec + maxBuckets - 1) / maxBuckets
		if stepSec < 1 {
			stepSec = 1
		}
		nBuckets = windowSec / stepSec
	}
	if nBuckets < 1 {
		nBuckets = 1
	}

	t0 := now.Add(-time.Duration(windowSec) * time.Second).Unix()
	t0 -= t0 % stepSec
	return Buckets{WindowSec: windowSec, StepSec: stepSec, T0Unix: t0, N: nBuckets, Now: now}, nil
}

// Cutoff 窗口起点:早于该时刻(由调用方按自身精度比较)的点不属于本窗口。
func (b Buckets) Cutoff() time.Time {
	return b.Now.Add(-time.Duration(b.WindowSec) * time.Second)
}

// RecentCut 最近一个 step 的起点:用于 recent 统计(如 RecentQPS)。
func (b Buckets) RecentCut() time.Time {
	return b.Now.Add(-time.Duration(b.StepSec) * time.Second)
}

// Pos 返回 unixSec 所在桶的下标。仅做算术分桶与边界折叠:
// 恰落在 [now, now+step) 半开区间外的边界点折叠进最后一桶;
// 调用方需先用 Cutoff 做窗口过滤(pos<0 时仍返回 !ok 兜底)。
func (b Buckets) Pos(unixSec int64) (int, bool) {
	pos := (unixSec - b.T0Unix) / b.StepSec
	if pos < 0 {
		return 0, false
	}
	if pos >= b.N {
		return int(b.N - 1), true
	}
	return int(pos), true
}

// Timestamp 桶 i 的对齐时间戳。
func (b Buckets) Timestamp(i int) time.Time {
	return time.Unix(b.T0Unix+int64(i)*b.StepSec, 0)
}
