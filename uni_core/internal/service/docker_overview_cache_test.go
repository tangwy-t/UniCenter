package service

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 总览短缓存（TTL + singleflight）的测试。四条要钉住的性质：
//   - TTL 内 N 次调用只算一次（命中缓存 = 零 I/O，且回吐同一份共享只读对象）；
//   - TTL 到期后正常重算（并看到这期间的变化）；
//   - 并发 N 次只算一次（singleflight 合并击穿）；
//   - 缓存**不改答案**：缓存回吐的响应与同刻的逐次重算逐字段等价。
//
// 时钟（svc.now）是可替换的：验过期把时钟往前拨即可，**不 sleep** —— sleep 会让
//「几秒 TTL」在 -race 全量跑时变成随机的红。

// blockingDeviceReader 在批量读上阻塞（entered/release 通道）：让并发用例确定地观察
// 「并发 miss 是否被合并成一次聚合」—— 若没合并，其余请求会各自进入聚合，
// FindByIDs 会被再次调用（entered 里就会多出条目）。
type blockingDeviceReader struct {
	inner   *countingDeviceReader
	entered chan struct{}
	release chan struct{}
}

func (d *blockingDeviceReader) FindByID(ctx context.Context, id uint64) (*entity.Device, error) {
	return d.inner.FindByID(ctx, id)
}

func (d *blockingDeviceReader) FindByIDs(ctx context.Context, ids []uint64) ([]entity.Device, error) {
	select {
	case d.entered <- struct{}{}:
		// 记下「有一个请求进来聚合了」。非阻塞：容量是请求数，多余的自然丢弃。
	default:
	}
	<-d.release
	return d.inner.FindByIDs(ctx, ids)
}

// overviewCacheEnv 是短缓存用例的夹具：**可推进的时钟** + 计数替身 + 真 miniredis
//（快照与可管主机集合是真的，只有设备行与时钟是替身）。
type overviewCacheEnv struct {
	svc    *DockerService
	store  *dockerstate.Store
	reader *countingDeviceReader
	rdb    *countingRedis
	ctx    context.Context
	clock  time.Time
}

// aggregations 返回底层聚合跑了几次（一次聚合 = 一趟 hostRecords 的批量设备读）。
func (e *overviewCacheEnv) aggregations() int64 { return e.reader.byIDs.Load() }

// advance 把夹具的时钟往前拨（验 TTL 过期，不 sleep）。
func (e *overviewCacheEnv) advance(d time.Duration) { e.clock = e.clock.Add(d) }

// addHost 增一台主机（快照 + 设备行）：TTL 内不可见、过期后可见 —— 这正是缓存的定义。
func (e *overviewCacheEnv) addHost(t *testing.T, id uint64, hostname string) {
	t.Helper()
	dev := &entity.Device{Hostname: hostname}
	dev.ID = id
	dev.LastSeenAt = &e.clock
	e.reader.devs[id] = dev
	if err := e.store.Save(e.ctx, id, overviewFleetState(e.clock), e.clock); err != nil {
		t.Fatal(err)
	}
}

// overviewFleetState 是一台主机的快照样板：一台 running 容器（KPI 有可比的数）。
func overviewFleetState(now time.Time) *agentproto.DockerState {
	return &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("core", "running", false)}}
}

// newOverviewCacheEnv 构造夹具：ids 是预置主机。wrap 非 nil 时替换默认的计数替身
//（并发用例换成阻塞替身）。TTL 默认沿用生产缺省，用例需要时自己覆盖。
func newOverviewCacheEnv(t *testing.T, ids []uint64,
	wrap func(*countingDeviceReader) DockerHostDeviceReader) *overviewCacheEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := &countingRedis{UniversalClient: goredis.NewClient(&goredis.Options{Addr: mr.Addr()})}
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	clock := time.Now()
	devs := map[uint64]*entity.Device{}
	for _, id := range ids {
		dev := &entity.Device{Hostname: fmt.Sprintf("uni-%d", id)}
		dev.ID = id
		dev.LastSeenAt = &clock
		devs[id] = dev
		if err := store.Save(ctx, id, overviewFleetState(clock), clock); err != nil {
			t.Fatal(err)
		}
	}
	reader := &countingDeviceReader{devs: devs}
	var devices DockerHostDeviceReader = reader
	if wrap != nil {
		devices = wrap(reader)
	}
	svc := NewDockerService(store, devices, fakeConfigGetter{vals: map[string]string{
		"sys.docker.snapshotInterval": "30",
		"sys.agent.offlineThreshold":  "90"}}, nil)
	env := &overviewCacheEnv{svc: svc, store: store, reader: reader, rdb: rdb, ctx: ctx, clock: clock}
	svc.now = func() time.Time { return env.clock }
	return env
}

// canonOverview 把主机清单按 id 排序后回吐一份副本：集合枚举的行序不保证
//（miniredis 的 SMEMBERS 直接迭代 map），逐字段比对前必须先归一顺序 ——
// 与「按 id 取条目」（hostByID）同一纪律。
func canonOverview(r *response.DockerOverviewResp) *response.DockerOverviewResp {
	out := *r
	out.Hosts = slices.Clone(r.Hosts)
	slices.SortFunc(out.Hosts, func(a, b response.DockerHostItem) int { return cmp.Compare(a.ID, b.ID) })
	out.Anomalies.Items = slices.Clone(r.Anomalies.Items)
	return &out
}

// TestDockerOverviewCacheServesWithinTTLAndRecomputesAfter 短缓存的两半：
// TTL 内只算一次（命中 = 零 I/O、回吐同一份共享只读对象），TTL 到期后重算并看到
// 这期间的变化；最后钉住「缓存不改答案」（与同刻的逐次重算逐字段等价）。
func TestDockerOverviewCacheServesWithinTTLAndRecomputesAfter(t *testing.T) {
	env := newOverviewCacheEnv(t, []uint64{7, 8}, nil)
	env.svc.WithOverviewCache(2 * time.Second)

	first, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.aggregations(); got != 1 {
		t.Fatalf("首次调用必须真的聚合: %d", got)
	}
	if len(first.Hosts) != 2 || first.Fleet.Hosts.Total != 2 || first.Fleet.Containers.Running != 2 {
		t.Fatalf("聚合结果不符: %+v", first)
	}

	// TTL 内：命中缓存 —— 一次 Retis 都不碰（零 I/O），且是同一份共享只读对象。
	before := dockerReadSnapshot(env.rdb, env.reader)
	second, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := dockerReadSnapshot(env.rdb, env.reader); got != before {
		t.Fatalf("TTL 内命中缓存必须零 I/O: before=%+v after=%+v", before, got)
	}
	if first != second {
		t.Fatal("TTL 内应回吐同一份共享只读对象（缓存不是复制）")
	}

	// TTL 内新增一台主机：不可见（这就是缓存的定义），也不触发聚合。
	env.addHost(t, 9, "uni-9")
	if _, err := env.svc.Overview(env.ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.aggregations(); got != 1 {
		t.Fatalf("TTL 内不该因任何调用而聚合: %d", got)
	}

	// 时钟拨到 TTL 边界（age >= TTL 即过期）：重算，并看到这期间新增的主机。
	env.advance(2 * time.Second)
	third, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.aggregations(); got != 2 {
		t.Fatalf("TTL 到期必须重算: %d", got)
	}
	if third == second {
		t.Fatal("过期后必须给新的对象（不是那份旧缓存）")
	}
	if len(third.Hosts) != 3 || third.Fleet.Hosts.Total != 3 {
		t.Fatalf("过期后必须看到期间新增的主机: %+v", third.Hosts)
	}

	// 缓存**不改答案**：缓存回吐的响应与同刻的逐次重算逐字段等价
	//（顺序归一后比对 —— 集合枚举行序不保证，见 canonOverview）。
	fresh, err := env.svc.computeOverview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonOverview(third), canonOverview(fresh)) {
		t.Fatalf("缓存结果必须与逐次重算逐字段一致:\n缓存 %+v\n重算 %+v", third, fresh)
	}
}

// TestDockerOverviewCacheDisabledRecomputesEveryCall 回退开关：TTL<=0 时逐次重算
// （与引入短缓存之前逐字一致），连并发合并也一并关掉。
func TestDockerOverviewCacheDisabledRecomputesEveryCall(t *testing.T) {
	env := newOverviewCacheEnv(t, []uint64{7}, nil)
	env.svc.WithOverviewCache(0)

	first, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.aggregations(); got != 2 {
		t.Fatalf("关闭缓存后每次调用都必须重算: %d", got)
	}
	if first == second {
		t.Fatal("关闭缓存后不该回吐同一份对象")
	}
	env.addHost(t, 8, "uni-8")
	third, err := env.svc.Overview(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Hosts) != 2 {
		t.Fatalf("关闭缓存后应立即看到新增主机: %+v", third.Hosts)
	}
}

// TestDockerOverviewSingleflightMergesConcurrentMisses 并发击穿：缓存空、N 个请求
// 同时到达（都是 miss），只允许**一次**聚合 —— 其余等在 singleflight 上共享同一份
// 结果。没有合并的话，每个请求都会各自进聚合（entered 会多出条目、聚合数会 >1）。
func TestDockerOverviewSingleflightMergesConcurrentMisses(t *testing.T) {
	const n = 32
	var blk *blockingDeviceReader
	env := newOverviewCacheEnv(t, []uint64{7, 8}, func(c *countingDeviceReader) DockerHostDeviceReader {
		blk = &blockingDeviceReader{inner: c, entered: make(chan struct{}, n), release: make(chan struct{})}
		return blk
	})
	env.svc.WithOverviewCache(2 * time.Second)

	results := make([]*response.DockerOverviewResp, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var started, done sync.WaitGroup
	started.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			started.Done()
			<-start
			results[i], errs[i] = env.svc.Overview(env.ctx)
		}(i)
	}
	started.Wait() // 全部就位
	close(start)   // 同时冲进来

	select {
	case <-blk.entered: // 领头已进入聚合
	case <-time.After(2 * time.Second):
		t.Fatal("没有请求进入聚合")
	}
	// 领头此刻阻塞在聚合里、缓存还没回填：其余 31 个请求必须等在 singleflight 上。
	// 未合并的话它们会各自进入聚合（entered 再收到条目）。这段等待只为给并发者
	// 到达的时间 —— 即使它们还没到，下面的断言也只是「没检出」而不会误报。
	time.Sleep(100 * time.Millisecond)
	if extra := len(blk.entered); extra != 0 {
		t.Fatalf("并发 miss 未被合并：另有 %d 个请求各自进入了聚合", extra)
	}
	close(blk.release)
	done.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatal("并发者应共享同一份只读结果（singleflight / 缓存命中）")
		}
	}
	if got := env.aggregations(); got != 1 {
		t.Fatalf("并发 %d 次只该聚合一次，实际 %d 次", n, got)
	}
	if len(results[0].Hosts) != 2 || results[0].Fleet.Hosts.Total != 2 {
		t.Fatalf("并发结果内容不符: %+v", results[0])
	}
}
