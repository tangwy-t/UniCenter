package dockerstate

import (
	"context"
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
