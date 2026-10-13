package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 「100 台规模」的**读取路径**守卫：无论舰队是 1 台还是 100 台，一趟枚举只允许两次
// 批量读（快照一条 pipeline + 设备行一条 id IN (...))，逐台的 Get / FindByID 调用为 0。
// 旧实现下这里是 100 次 Redis GET + 100 条 SELECT，且全在同一个请求里 —— 而总览页
// 被每 10 秒轮询一次。（缓存是另一半，见 docker_overview_cache_test.go。）

// countingRedis 是 goredis.UniversalClient 的计数包装：只观察被守卫的两条路径
//（逐条 Get、一次 Pipeline），其余原样转发给真客户端。
type countingRedis struct {
	goredis.UniversalClient
	gets      atomic.Int64
	pipelines atomic.Int64
}

func (c *countingRedis) Get(ctx context.Context, key string) *goredis.StringCmd {
	c.gets.Add(1)
	return c.UniversalClient.Get(ctx, key)
}

func (c *countingRedis) Pipeline() goredis.Pipeliner {
	c.pipelines.Add(1)
	return c.UniversalClient.Pipeline()
}

// countingDeviceReader 是 service.DockerHostDeviceReader 的计数替身：分别数
// 「批量读」与「逐台读」各调用了多少次，并记下批量那次的入参（证明一次拿到全部 id，
// 而不是内部分批循环）。
type countingDeviceReader struct {
	devs map[uint64]*entity.Device

	byIDs   atomic.Int64
	byID    atomic.Int64
	lastIDs []uint64
}

func (d *countingDeviceReader) FindByID(_ context.Context, id uint64) (*entity.Device, error) {
	d.byID.Add(1)
	if dev, ok := d.devs[id]; ok {
		return dev, nil
	}
	return nil, repository.ErrNotFound
}

func (d *countingDeviceReader) FindByIDs(_ context.Context, ids []uint64) ([]entity.Device, error) {
	d.byIDs.Add(1)
	d.lastIDs = append([]uint64(nil), ids...)
	out := make([]entity.Device, 0, len(ids))
	for _, id := range ids {
		if dev, ok := d.devs[id]; ok {
			out = append(out, *dev)
		}
	}
	return out, nil
}

// dockerReadCounters 是一次请求的 I/O 账目（四条路径各自的累计调用次数）。
type dockerReadCounters struct {
	redisGets      int64 // 逐条 Get（N+1 的形态）
	redisPipelines int64 // 一次 pipeline（批量的形态）
	perID          int64 // FindByID（逐台）
	batch          int64 // FindByIDs（批量）
}

func dockerReadSnapshot(rdb *countingRedis, reader *countingDeviceReader) dockerReadCounters {
	return dockerReadCounters{
		redisGets:      rdb.gets.Load(),
		redisPipelines: rdb.pipelines.Load(),
		perID:          reader.byID.Load(),
		batch:          reader.byIDs.Load(),
	}
}

// assertOneBatchRead 断言「这一趟请求只做了两次批量读」：逐台路径调用为零、
// 批量路径各一次。**与舰队规模无关**是本测试的核（1 台与 100 台都要成立）。
func assertOneBatchRead(t *testing.T, what string, before, after dockerReadCounters) {
	t.Helper()
	if d := after.redisGets - before.redisGets; d != 0 {
		t.Fatalf("%s：快照读必须批量（pipeline），不得逐台 Get；实际逐台 %d 次", what, d)
	}
	if d := after.redisPipelines - before.redisPipelines; d != 1 {
		t.Fatalf("%s：快照必须一次 pipeline 读完（与主机数无关）；实际 %d 次", what, d)
	}
	if d := after.perID - before.perID; d != 0 {
		t.Fatalf("%s：设备行必须批量读，不得逐台 FindByID；实际逐台 %d 次", what, d)
	}
	if d := after.batch - before.batch; d != 1 {
		t.Fatalf("%s：设备行必须一次批量读（id IN (...)）；实际 %d 次", what, d)
	}
}

// TestDockerOverviewNoNPlusOneAcrossFleetSize 是本次「100 台规模」的核心守卫：
// 每请求只有两次批量读，且**调用次数与主机数无关**。
func TestDockerOverviewNoNPlusOneAcrossFleetSize(t *testing.T) {
	for _, n := range []int{1, 100} {
		t.Run(fmt.Sprintf("hosts=%d", n), func(t *testing.T) {
			mr := miniredis.RunT(t)
			rdb := &countingRedis{UniversalClient: goredis.NewClient(&goredis.Options{Addr: mr.Addr()})}
			store := dockerstate.NewStore(rdb)
			ctx := context.Background()
			now := time.Now()

			devs := map[uint64]*entity.Device{}
			for i := 0; i < n; i++ {
				id := uint64(1000 + i)
				dev := &entity.Device{Hostname: fmt.Sprintf("uni-%d", id)}
				dev.ID = id
				dev.LastSeenAt = &now
				devs[id] = dev
				st := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
					Containers: []agentproto.DockerContainer{
						overviewContainer(fmt.Sprintf("c-%d", id), "running", false)},
					Images: []agentproto.DockerImage{{ID: fmt.Sprintf("i-%d", id), InUse: true}}}
				if err := store.Save(ctx, id, st, now); err != nil {
					t.Fatal(err)
				}
			}

			reader := &countingDeviceReader{devs: devs}
			svc := NewDockerService(store, reader, fakeConfigGetter{vals: map[string]string{
				"sys.docker.snapshotInterval": "30",
				"sys.agent.offlineThreshold":  "90"}}, nil)
			svc.now = func() time.Time { return now }
			// 守卫的是读取路径、不是缓存：关掉短缓存，免得第二次调用被缓存吃掉。
			svc.WithOverviewCache(0)

			// 端点一：总览（本次改造的主角）。
			before := dockerReadSnapshot(rdb, reader)
			resp, err := svc.Overview(ctx)
			if err != nil {
				t.Fatal(err)
			}
			assertOneBatchRead(t, "Overview", before, dockerReadSnapshot(rdb, reader))
			if len(resp.Hosts) != n || resp.Fleet.Hosts.Total != n ||
				resp.Fleet.Containers.Total != n || resp.Fleet.Images.Total != n {
				t.Fatalf("聚合结果不符（%d 台）: hosts=%d fleet=%+v", n, len(resp.Hosts), resp.Fleet)
			}
			if len(reader.lastIDs) != n {
				t.Fatalf("批量设备读必须**一次**拿到全部 %d 台（不得内部分批循环）: %d", n, len(reader.lastIDs))
			}

			// 端点二：主机清单（同一趟 hostRecords 枚举）。
			before = dockerReadSnapshot(rdb, reader)
			hosts, err := svc.Hosts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			assertOneBatchRead(t, "Hosts", before, dockerReadSnapshot(rdb, reader))
			if len(hosts.List) != n {
				t.Fatalf("主机清单条数不符: %d", len(hosts.List))
			}

			// 单台端点是另一条路径：State 按 id 找一台，本来就该走 FindByID
			//（批量枚举不得把「单台」也改成批量 —— 那会让详情页也白读一百台）。
			before = dockerReadSnapshot(rdb, reader)
			if _, err := svc.State(ctx, 1000); err != nil {
				t.Fatal(err)
			}
			if d := reader.byID.Load() - before.perID; d != 1 {
				t.Fatalf("单台详情应恰走一次 FindByID（逐台路径仍然只服务单台）: %d", d)
			}
			if d := reader.byIDs.Load() - before.batch; d != 0 {
				t.Fatalf("单台详情不该走批量读: %d", d)
			}
		})
	}
}
