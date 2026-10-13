package dockerstate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return NewStore(rdb), mr
}

func sampleState() *agentproto.DockerState {
	return &agentproto.DockerState{T: 1790000000000, DockerOK: true,
		Containers: []agentproto.DockerContainer{{ID: "c1", Name: "mysql", Image: "mysql:8.0.22", State: "running"}}}
}

func TestSaveGetAndHosts(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1790000000, 0)
	if err := s.Save(ctx, 7, sampleState(), now); err != nil {
		t.Fatal(err)
	}
	env, err := s.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if env == nil || !env.State.DockerOK || len(env.State.Containers) != 1 {
		t.Fatalf("快照未存上: %+v", env)
	}
	if env.ReceivedAt != now.UnixMilli() {
		t.Fatalf("ReceivedAt 必须是服务端收到时刻，实际 %d", env.ReceivedAt)
	}
	if mr.Exists(StateKey(7)) == false {
		t.Fatal("键名必须与 spec §4.3.2 一致（docker:state:<device_id>）")
	}
	if mr.Exists(HostsKey) == false {
		t.Fatal("docker:hosts 集合必须被写入")
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0] != 7 {
		t.Fatalf("主机集合不符: %v %v", hosts, err)
	}
	// 重复上报不产生重复成员
	if err := s.Save(ctx, 7, sampleState(), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := s.Hosts(ctx); len(hosts) != 1 {
		t.Fatalf("重复上报必须幂等: %v", hosts)
	}
	// 快照无 TTL（离线仍要可读 —— 设备列表「最后已知状态」同一心智）
	if ttl := mr.TTL(StateKey(7)); ttl != 0 {
		t.Fatalf("快照不得设 TTL（离线时仍要可读），实际 %v", ttl)
	}
}

func TestPurgeRemovesStateAndHostMembership(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_ = s.Save(ctx, 7, sampleState(), time.Now())
	_ = s.Save(ctx, 8, sampleState(), time.Now())
	if err := s.Purge(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if env, _ := s.Get(ctx, 7); env != nil {
		t.Fatal("设备删除后快照必须被清掉（否则键永久泄漏）")
	}
	hosts, _ := s.Hosts(ctx)
	if len(hosts) != 1 || hosts[0] != 8 {
		t.Fatalf("docker:hosts 必须同步 SREM: %v", hosts)
	}
}

// 陈旧阈值 = max(3×周期, 90s)：3 个周期无帧大概率异常，而 90s 下限防周期很小时阈值过短。
func TestStaleThreshold(t *testing.T) {
	cases := []struct {
		interval int
		want     time.Duration
	}{
		{30, 90 * time.Second},   // 3×30=90 → 取 90
		{10, 90 * time.Second},   // 3×10=30 → 被下限抬到 90
		{120, 360 * time.Second}, // 3×120=360
		{0, 90 * time.Second},    // 未配置 → 按默认 30 的 3 倍与下限
	}
	for _, c := range cases {
		if got := StaleThreshold(c.interval); got != c.want {
			t.Errorf("StaleThreshold(%d) = %v, want %v", c.interval, got, c.want)
		}
	}
}

// 陈旧度用**服务端收到时刻**算：agent 的时钟可能偏（.106 实测快 8 小时），
// 用载荷时间戳算会得出「数据来自未来」这种荒谬结论。
func TestStaleUsesServerClock(t *testing.T) {
	now := time.Unix(1790000000, 0)
	future := &Envelope{State: agentproto.DockerState{T: now.Add(8 * time.Hour).UnixMilli(), DockerOK: true},
		ReceivedAt: now.Add(-10 * time.Second).UnixMilli()}
	stale, ageMs := Stale(future, now, 30)
	if stale {
		t.Fatal("10 秒前收到的帧不该判陈旧（无论载荷时间戳说什么）")
	}
	if ageMs != 10000 {
		t.Fatalf("年龄应为 10000ms，实际 %d", ageMs)
	}
	old := &Envelope{State: agentproto.DockerState{T: 1, DockerOK: true},
		ReceivedAt: now.Add(-2 * time.Minute).UnixMilli()}
	if stale, _ := Stale(old, now, 30); !stale {
		t.Fatal("2 分钟前收到的帧在 30 秒周期下必须判陈旧（阈值 90s）")
	}
	if stale, _ := Stale(nil, now, 30); stale {
		t.Fatal("没有快照不是「陈旧」，是「从未上报」——两者在页面上是不同的话")
	}
}

// ── 批量读（MGet）：主机枚举的 N+1 闸 ──────────────────────────────────

// countingRedis 是 UniversalClient 的计数包装：只覆盖被观察的两条路径
// （逐条 Get 与批量 Pipeline），其余原样转发给真客户端。
// 「批量读 = 一次 pipeline、零次逐条 Get」这条纪律就钉在这两枚计数上。
type countingRedis struct {
	goredis.UniversalClient
	gets      int
	pipelines int
}

func (c *countingRedis) Get(ctx context.Context, key string) *goredis.StringCmd {
	c.gets++
	return c.UniversalClient.Get(ctx, key)
}

func (c *countingRedis) Pipeline() goredis.Pipeliner {
	c.pipelines++
	return c.UniversalClient.Pipeline()
}

// TestMGetReadsWholeBatchInOnePipeline 批量读的一条硬性质：N 台主机只发**一次**
// pipeline、**零次**逐条 Get —— 逐台 Get 正是这次要消灭的 N+1（100 台 = 100 次往返）。
// 同时钉住结果与逐台 Get 逐字等价（读到的照给、没上报的缺席）。
func TestMGetReadsWholeBatchInOnePipeline(t *testing.T) {
	mr := miniredis.RunT(t)
	counter := &countingRedis{UniversalClient: goredis.NewClient(&goredis.Options{Addr: mr.Addr()})}
	s := NewStore(counter)
	ctx := context.Background()
	now := time.Unix(1790000000, 0)
	for _, id := range []uint64{7, 8, 9} {
		if err := s.Save(ctx, id, sampleState(), now); err != nil {
			t.Fatal(err)
		}
	}
	// 计数从「读之前」开始：Save 走的是 TxPipeline（与读路径不是同一条），不该混进来。
	getsBefore, pipesBefore := counter.gets, counter.pipelines

	// 99 从未上报：与 7/8/9 一起请求，它必须缺席且**不是**错误。
	envs, err := s.MGet(ctx, []uint64{7, 8, 9, 99})
	if err != nil {
		t.Fatalf("从未上报不得算失败: %v", err)
	}
	if len(envs) != 3 {
		t.Fatalf("应读到 3 台（99 从未上报，缺席）: %+v", envs)
	}
	if got := counter.gets - getsBefore; got != 0 {
		t.Fatalf("批量读不得走逐条 Get（N+1）：过路 %d 次", got)
	}
	if got := counter.pipelines - pipesBefore; got != 1 {
		t.Fatalf("N 台必须只用一次 pipeline，实际 %d 次", got)
	}
	for _, id := range []uint64{7, 8, 9} {
		if envs[id] == nil || envs[id].ReceivedAt != now.UnixMilli() {
			t.Fatalf("设备 %d 的信封不符: %+v", id, envs[id])
		}
		// 与逐台 Get 同一份结果（批量只是把往返合一，不换口径）。
		single, err := s.Get(ctx, id)
		if err != nil || single == nil || !reflect.DeepEqual(*single, *envs[id]) {
			t.Fatalf("设备 %d 的批量结果与逐台 Get 不一致: %+v / %+v", id, envs[id], single)
		}
	}

	// 空 ids：不发空 pipeline（一次纯浪费的往返），返回空 map。
	before := counter.pipelines
	if envs, err := s.MGet(ctx, nil); err != nil || len(envs) != 0 {
		t.Fatalf("空 ids 应返回空 map 且无错: %+v %v", envs, err)
	}
	if counter.pipelines != before {
		t.Fatal("空 ids 不该发 pipeline")
	}
}

// TestMGetOneBadKeyDoesNotPoisonBatch 一批里坏一台：成功的那几台照常返回，
// 坏的那台**缺席于 map 但在失败名单里** ——「读不到」与「从未上报」是两句不同的话，
// 合成一句会让坏掉的主机被降级成「零容器零镜像的正常主机」。
func TestMGetOneBadKeyDoesNotPoisonBatch(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1790000000, 0)
	for _, id := range []uint64{7, 9} {
		if err := s.Save(ctx, id, sampleState(), now); err != nil {
			t.Fatal(err)
		}
	}
	// 主机 8 的键值损坏（键在、值读不出来）：Get 会返回 error 而不是 (nil, nil)。
	mr.Set(StateKey(8), "{not json")

	envs, err := s.MGet(ctx, []uint64{7, 8, 9, 99})
	if err == nil {
		t.Fatal("有一台读不到就必须如实报出（否则它会被当成「从未上报」）")
	}
	var mge *MGetError
	if !errors.As(err, &mge) {
		t.Fatalf("失败名单必须是 *MGetError: %T %v", err, err)
	}
	if len(mge.Failed) != 1 || mge.Failed[8] == nil {
		t.Fatalf("失败名单应恰含设备 8: %+v", mge.Failed)
	}
	if _, ok := envs[8]; ok {
		t.Fatal("读失败的那台必须缺席于结果 map")
	}
	if len(envs) != 2 || envs[7] == nil || envs[9] == nil {
		t.Fatalf("一批里坏一台不得拖垮另外两台: %+v", envs)
	}
	if _, ok := mge.Failed[99]; ok {
		t.Fatal("从未上报不是失败（缺席即可）")
	}
	// 逐台 Get 对同一台给出的是同一句话（读失败 → error）：批量没有换口径。
	if _, err := s.Get(ctx, 8); err == nil {
		t.Fatal("逐台 Get 对坏键也报错 —— 批量必须与它同参")
	}
	// 原因可穿透（Unwrap 摊平逐台错误）：调用方要能把反序列化错误记进日志。
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("失败原因应能穿透到反序列化错误: %v", err)
	}
}

// TestMGetWholeBatchFailureAttributesEveryHost 整批不可执行（Redis 不可达）：
// **每一台**都落进失败名单 —— 与逐台 Get 的「每台都读失败」逐字等价。
// 少了这条，Redis 全不可达会被伪装成「全部从未上报」（页面从此显示一片零计数）。
func TestMGetWholeBatchFailureAttributesEveryHost(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	addr := mr.Addr()
	mr.Close()
	// MaxRetries=-1：不重试（重试会把这条用例拖成秒级）；DialTimeout 短，快速失败。
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStore(rdb)

	ids := []uint64{7, 8, 9}
	envs, err := s.MGet(context.Background(), ids)
	if err == nil {
		t.Fatal("Redis 不可达必须报错（否则整批会被当成「从未上报」）")
	}
	var mge *MGetError
	if !errors.As(err, &mge) {
		t.Fatalf("必须是 *MGetError: %T %v", err, err)
	}
	if len(mge.Failed) != len(ids) {
		t.Fatalf("整批不可执行时每台都该在失败名单里: %+v", mge.Failed)
	}
	for _, id := range ids {
		if mge.Failed[id] == nil {
			t.Fatalf("设备 %d 未落进失败名单: %+v", id, mge.Failed)
		}
	}
	if len(envs) != 0 {
		t.Fatalf("失败时不该凭空造出信封: %+v", envs)
	}
}
