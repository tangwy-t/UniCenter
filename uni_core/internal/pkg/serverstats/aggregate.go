package serverstats

import (
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// bucketAcc 聚合单个时间桶的中间状态。
type bucketAcc struct {
	cpuSum, cpuN     float64
	memSum, memN     float64
	heapSum, heapN   float64
	sysSum, sysN     float64
	gorSum, gorN     float64
	pauseSum, pauseN float64
	diskSum, diskN   float64
	loadSum, loadN   float64

	lastT      int64 // 桶内时间戳最大的采样点
	gcNumLast  uint32
	uptimeLast int64
}

// aggregate 把原始采样点聚合为按绝对时间对齐的时间桶。
// 分桶方案(校验 / step 放大 / t0 对齐 / 边界桶折叠)由
// metricshistory.Buckets 统一提供,本函数只保留服务器指标自身的
// 累加语义:
//   - cpu/memSys/heapAlloc/sysMem/goroutines/gcPauseMs/disk/load1 取均值,
//     缺值点不参与平均;桶内该字段全缺则输出 nil(JSON 省略)
//   - 所有均值输出前统一 round2 量化到 2 位小数(与采集端一致)
//   - gcNum/uptime 取桶内最新采样点的值(单调递增量,均值无意义)
//   - 空桶剔除;窗口外的点按 Cutoff 丢弃
func aggregate(points []Point, b metricshistory.Buckets) (*Snapshot, error) {
	accs := make([]bucketAcc, b.N)
	for _, p := range points {
		sec := p.T / 1000
		if time.Unix(sec, 0).Before(b.Cutoff()) {
			continue
		}
		pos, ok := b.Pos(sec)
		if !ok {
			continue
		}
		a := &accs[pos]
		if p.T > a.lastT {
			a.lastT = p.T
			a.gcNumLast = p.GCNum
			a.uptimeLast = p.Uptime
		}
		a.cpuSum += p.CPU
		a.cpuN++
		a.memSum += p.MemSys
		a.memN++
		a.heapSum += p.HeapAlloc
		a.heapN++
		a.sysSum += p.SysMem
		a.sysN++
		a.gorSum += float64(p.Goroutines)
		a.gorN++
		if p.GCPauseMs != nil {
			a.pauseSum += *p.GCPauseMs
			a.pauseN++
		}
		a.diskSum += p.Disk
		a.diskN++
		if p.Load1 != nil {
			a.loadSum += *p.Load1
			a.loadN++
		}
	}

	f64 := func(v float64) *float64 { return &v }
	buckets := make([]Bucket, 0, b.N)
	for i := range accs {
		a := &accs[i]
		if a.lastT == 0 {
			continue // 空桶
		}
		bk := Bucket{
			Timestamp: util.JSONTime(b.Timestamp(i)),
		}
		if a.cpuN > 0 {
			bk.CPU = f64(util.Round2(a.cpuSum / a.cpuN))
		}
		if a.memN > 0 {
			bk.MemSys = f64(util.Round2(a.memSum / a.memN))
		}
		if a.heapN > 0 {
			bk.HeapAlloc = f64(util.Round2(a.heapSum / a.heapN))
		}
		if a.sysN > 0 {
			bk.SysMem = f64(util.Round2(a.sysSum / a.sysN))
		}
		if a.gorN > 0 {
			bk.Goroutines = f64(util.Round2(a.gorSum / a.gorN))
		}
		if a.pauseN > 0 {
			bk.GCPauseMs = f64(util.Round2(a.pauseSum / a.pauseN))
		}
		if a.diskN > 0 {
			bk.Disk = f64(util.Round2(a.diskSum / a.diskN))
		}
		if a.loadN > 0 {
			bk.Load1 = f64(util.Round2(a.loadSum / a.loadN))
		}
		gc := a.gcNumLast
		up := a.uptimeLast
		bk.GCNum = &gc
		bk.Uptime = &up
		buckets = append(buckets, bk)
	}

	return &Snapshot{
		WindowSeconds: b.WindowSec,
		StepSeconds:   b.StepSec,
		Buckets:       buckets,
	}, nil
}
