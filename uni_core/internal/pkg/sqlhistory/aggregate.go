package sqlhistory

import (
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// bucketAcc 聚合中间累加器。
type bucketAcc struct {
	count                          int64
	avgSum, p50Sum, p95Sum, p99Sum float64
	max                            float64
	errs, slows                    int64
}

// aggregate 把采样点按绝对时间对齐归并为 step 桶(纯函数,便于表驱动单测)。
// 分桶方案(校验 / step 放大 / t0 对齐 / 边界桶折叠 / 窗口 cutoff)由
// metricshistory.Buckets 统一提供,本函数只保留 SQL 指标自身的累加语义:
//   - avg/p50/p95/p99 采用 count 加权平均 —— 跨 3s 采样合并成大桶时的
//     近似值,可能低估孤立尖峰;监控场景可接受(spec 已注明)
//   - 所有派生浮点(QPS/耗时)输出前经 util.Round2 量化到 2 位小数
//   - 无样本的桶剔除,不产生误导性零值
func aggregate(points []Point, b metricshistory.Buckets) (*Snapshot, error) {
	raw := make([]bucketAcc, b.N)
	var recentCount int64

	for _, p := range points {
		ts := time.UnixMilli(p.T)
		if ts.Before(b.Cutoff()) {
			continue
		}
		pos, ok := b.Pos(ts.Unix())
		if !ok {
			continue
		}
		if !ts.Before(b.RecentCut()) {
			recentCount += p.Count
		}
		bk := &raw[pos]
		bk.count += p.Count
		bk.avgSum += p.AvgMs * float64(p.Count)
		bk.p50Sum += p.P50Ms * float64(p.Count)
		bk.p95Sum += p.P95Ms * float64(p.Count)
		bk.p99Sum += p.P99Ms * float64(p.Count)
		if p.MaxMs > bk.max {
			bk.max = p.MaxMs
		}
		bk.errs += p.ErrorCount
		bk.slows += p.SlowCount
	}

	snap := &Snapshot{
		WindowSeconds: b.WindowSec,
		StepSeconds:   b.StepSec,
		RecentQPS:     util.Round2(float64(recentCount) / float64(b.StepSec)),
		Buckets:       make([]Bucket, 0, b.N),
	}
	for i := range raw {
		bk := &raw[i]
		if bk.count == 0 {
			continue
		}
		snap.Buckets = append(snap.Buckets, Bucket{
			Timestamp:  util.JSONTime(b.Timestamp(i)),
			Count:      bk.count,
			QPS:        util.Round2(float64(bk.count) / float64(b.StepSec)),
			AvgMs:      util.Round2(bk.avgSum / float64(bk.count)),
			P50Ms:      util.Round2(bk.p50Sum / float64(bk.count)),
			P95Ms:      util.Round2(bk.p95Sum / float64(bk.count)),
			P99Ms:      util.Round2(bk.p99Sum / float64(bk.count)),
			MaxMs:      util.Round2(bk.max),
			ErrorCount: bk.errs,
			SlowCount:  bk.slows,
		})
	}
	return snap, nil
}
