package service

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// rewindingReader 是 FlushRawReader 的包装：在读到**指定桶**的那一刻模拟
// 「backfill 并发把水位回退到更早」（唯一起点就是 flush 与 backfill 的 cron
// 同时唤醒：flush 已经读到轮初水位，backfill 正在回退）。
//
// 为什么用「包装真 RawStore」而不是替身：flush 的设备枚举（Index）与桶读取（Bucket）
// 必须与生产同源，否则断言的不是真实的读路径。这里只插入一个**时序钩子**。
//
// 回退本身用裸 SET 写（而不是 CursorStore.Rewind）：它就是**一个并发写者**，
// 并发者不会等我们的 Lua；用裸 SET 才能真的构造出「水位在两步之间被改动」。
type rewindingReader struct {
	inner        FlushRawReader
	rdb          goredis.Cmdable
	deviceID     uint64
	fireOnBucket int64 // 读到这个桶起点（unix 秒）时触发一次
	rewindTo     int64
	fired        bool
}

func (r *rewindingReader) Index(ctx context.Context) ([]uint64, error) { return r.inner.Index(ctx) }

func (r *rewindingReader) Bucket(ctx context.Context, deviceID uint64,
	fromMs, toMs int64) ([]agentproto.MetricsSample, error) {

	if !r.fired && deviceID == r.deviceID && fromMs/1000 == r.fireOnBucket {
		r.fired = true
		if err := r.rdb.Set(ctx, flushCursorKey(deviceID), r.rewindTo, 0).Err(); err != nil {
			return nil, err
		}
	}
	return r.inner.Bucket(ctx, deviceID, fromMs, toMs)
}

// TestFlushDoesNotPushBackCursorAfterConcurrentRewind 是 S1 缺陷的**端到端最小复现**。
//
// 缺陷本体（修复前）：flush 的收尾写入是 `SET key lastBucketed`（无条件），
// 而 lastBucketed 是由**轮初**读到的游标推导出来的。于是：
//
//	① flush 轮读到 cursor_5m = base−300，开始处理 [base, base+600)；
//	② 处理第一个桶的过程中 backfill 把水位回退到 rewindTo（N 小时前），
//	   准备重放 [rewindTo+300, base) 这段桶；
//	③ flush 轮结束，无条件写上 lastBucketed = base+300。
//
// ③ 之后水位是 base+300，而它下面那段回退出来的桶**本轮没有被处理**
// —— 下一轮从 base+600 起步，那些桶永远不会再被 flush 读到（「回退被覆写」）。
//
// 断言：水位必须停在 backfill 回退后的 rewindTo 上（本轮让位，回退生效）。
func TestFlushDoesNotPushBackCursorAfterConcurrentRewind(t *testing.T) {
	ctx := context.Background()
	f := newFlushFixture(t, flushBaseTS+900, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	// 回退目标对齐到 5min 栅格（backfill 的 target 语义），取 base−2h。
	const rewindTo = flushBaseTS - 7200

	rw := &rewindingReader{
		inner: f.raw, rdb: f.rdb, deviceID: flushDevID,
		fireOnBucket: flushBaseTS, rewindTo: rewindTo,
	}
	// 用包装后的读路径重建服务（其余依赖与夹具完全一致）。
	f.svc = NewAgentMetricsFlushService(rw, f.repo, f.res, f.cfg, logger.NewNop()).
		WithCursorStore(f.rdb).
		WithClock(f.clock.now)

	stats, err := f.svc.FlushOnce(ctx)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	// 夹具前提：回退**确实**在轮内发生了，否则本测试测的是别的路径。
	if !rw.fired {
		t.Fatalf("夹具失效：桶 %d 的读取没有触发回退钩子（本测试要构造的是并发回退）", flushBaseTS)
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("BucketsWritten = %d, want 1（本轮确实写了那个有数据的桶）", stats.BucketsWritten)
	}

	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_5m 键丢失")
	}
	if cur != rewindTo {
		t.Fatalf("cursor_5m = %d, want %d（并发回退后的水位不得被本轮 flush 推回：\n"+
			"本轮只处理了 [%d, %d)，回退出来的 [%d, %d) 一个桶都没处理，\n"+
			"把水位写回 %d 等于用一次合法前移把那段桶永久跳过）",
			cur, rewindTo, flushBaseTS, flushBaseTS+600, rewindTo+300, flushBaseTS, flushBaseTS+300)
	}
}
