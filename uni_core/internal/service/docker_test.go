package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// fakeDeviceReader 是 DockerDeviceReader 的替身：按 id 给预置设备，或统一返回一个错误。
type fakeDeviceReader struct {
	devs map[uint64]*entity.Device
	err  error
}

func (f fakeDeviceReader) FindByID(ctx context.Context, id uint64) (*entity.Device, error) {
	if f.err != nil {
		return nil, f.err
	}
	if d, ok := f.devs[id]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func TestDockerServiceHostList(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	dev := &entity.Device{Hostname: "bogon", Status: entity.DeviceStatusEnabled}
	dev.ID = 7
	dev.LastSeenAt = &now
	svc := NewDockerService(store, fakeDeviceReader{devs: map[uint64]*entity.Device{7: dev}},
		fakeConfigGetter{vals: map[string]string{"sys.docker.snapshotInterval": "30",
			"sys.agent.offlineThreshold": "90"}}, nil)

	// 从未上报：清单里**不出现**（docker:hosts 是唯一枚举源）
	if hosts, err := svc.Hosts(ctx); err != nil || len(hosts.List) != 0 {
		t.Fatalf("从未上报快照的设备不该出现在可管主机里: %+v %v", hosts, err)
	}

	st := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{{ID: "c1", Name: "mysql", Image: "mysql:8.0.22", State: "running"}},
		Images:     []agentproto.DockerImage{{ID: "i1", SizeMB: 545}},
		Compose:    &agentproto.DockerComposeInfo{Flavor: agentproto.DockerComposeFlavorPlugin, Version: "v2.27.0"}}
	if err := store.Save(ctx, 7, st, now); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("应有一台主机: %+v", resp.List)
	}
	h := resp.List[0]
	if !h.DockerOK || h.Containers != 1 || h.Images != 1 || h.ComposeFlavor != "plugin" {
		t.Fatalf("主机摘要不符: %+v", h)
	}
	if h.Stale || h.LastSync == 0 {
		t.Fatalf("刚上报的快照不该判陈旧: %+v", h)
	}
	if resp.SnapshotInterval != 30 {
		t.Fatalf("响应必须带上快照周期（前端文案与后端判定同源）: %d", resp.SnapshotInterval)
	}
	if h.Hostname != "bogon" || !h.Online {
		t.Fatalf("设备字段未带出: %+v", h)
	}

	// 陈旧：直接把快照的收到时刻改到 2 分钟前
	if _, err := rdb.Set(ctx, dockerstate.StateKey(7), func() string {
		env, _ := store.Get(ctx, 7)
		env.ReceivedAt = now.Add(-2 * time.Minute).UnixMilli()
		b, _ := json.Marshal(env)
		return string(b)
	}(), 0).Result(); err != nil {
		t.Fatal(err)
	}
	resp, _ = svc.Hosts(ctx)
	if !resp.List[0].Stale {
		t.Fatal("2 分钟未更新在 30 秒周期下必须判陈旧（阈值 90s）")
	}
}

func TestDockerServiceStateNeverReported(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	dev := &entity.Device{Hostname: "x"}
	dev.ID = 7
	svc := NewDockerService(store, fakeDeviceReader{devs: map[uint64]*entity.Device{7: dev}}, fakeConfigGetter{}, nil)
	resp, err := svc.State(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.NeverReported || resp.Stale {
		t.Fatalf("从未上报与陈旧必须可区分: %+v", resp)
	}
	if resp.Containers == nil || len(resp.Containers) != 0 {
		t.Fatal("清单必须是空数组而非 null（前端少一层判空）")
	}
}

// TestDockerServiceStateMapsFullSnapshot 逐字段断言五类资源的映射。
//
// 对着**整个结构**断言而不是抽查两三个字段：这类映射的错误形态是「漏写一个字段」，
// 症状是页面上某一列永远是空的 —— 抽查恰好漏掉那个字段时测试照样绿。
func TestDockerServiceStateMapsFullSnapshot(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	// 来源 IP 是 IPv4-mapped 形态：响应必须是归一后的点分十进制（复用设备列表的
	// normalizedIP），否则页面上会出现 `::ffff:192.168.12.105` 这种奇怪字符串。
	dev := &entity.Device{Hostname: "uni-105", PrimaryIP: "::ffff:192.168.12.105"}
	dev.ID = 7
	dev.LastSeenAt = &now
	// 第二台机器：docker 不可用（能力信号）—— 摘要里必须给出「不可用 + 原因句 + 零计数」。
	down := &entity.Device{Hostname: "uni-106"}
	down.ID = 8
	svc := NewDockerService(store,
		fakeDeviceReader{devs: map[uint64]*entity.Device{7: dev, 8: down}},
		fakeConfigGetter{}, nil)
	svc.now = func() time.Time { return now } // 固定「现在」：年龄断言才不随执行时刻漂移

	sizeMB := 12.5
	st := &agentproto.DockerState{
		T: now.UnixMilli(), DockerOK: true,
		Compose: &agentproto.DockerComposeInfo{Flavor: agentproto.DockerComposeFlavorV1, Version: "1.29.2"},
		Containers: []agentproto.DockerContainer{{
			ID: "c1", Name: "uni-center-core", Image: "uni-center-core:latest", State: "running",
			StatusText: "Up 16 hours", Created: 1789000000, StartedAt: 1789000100,
			CPUPercent: 0.6, MemUsageMB: 91, MemLimitMB: 1024,
			NetRXBytesSec: 2400, NetTXBytesSec: 1100,
			ComposeProject: "uni-center", ComposeService: "uni_core", Protected: true,
			Ports: []agentproto.DockerPort{{IP: "0.0.0.0", PrivatePort: 8088, PublicPort: 20080, Type: "tcp"}},
		}},
		Images: []agentproto.DockerImage{{
			ID: "sha256:545b", RepoTags: []string{"mysql:8.0.22"}, SizeMB: 545,
			Created: 1780000000, InUse: true, Dangling: false, InUseBy: []string{"mysql"},
		}},
		Volumes: []agentproto.DockerVolume{{
			Name: "uni-center-uploads", Driver: "local", SizeMB: &sizeMB,
			InUse: true, MountedBy: []string{"uni-center-core"},
		}},
		Networks: []agentproto.DockerNetwork{{
			Name: "bridge", Driver: "bridge", Scope: "local", Internal: true, ContainersCount: 8,
		}},
		Projects: []agentproto.DockerProject{{
			Name: "uni-center", ConfigFiles: []string{"/data/UniCenter/docker-compose.yml"},
			State: "running", Services: 2, ContainersCount: 2,
		}},
	}
	if err := store.Save(ctx, 7, st, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.State(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if resp.NeverReported || resp.Stale || resp.AgeSeconds != 5 ||
		resp.LastSync != now.Add(-5*time.Second).Unix() {
		t.Fatalf("陈旧度/年龄/同步时刻必须按 core 收到的时刻算: %+v", resp)
	}
	if !resp.DockerOK || resp.Error != "" {
		t.Fatalf("可用时不该带结论句: %+v", resp)
	}
	if resp.Compose == nil || resp.Compose.Flavor != "v1" || resp.Compose.Version != "1.29.2" {
		t.Fatalf("compose 形态未带出: %+v", resp.Compose)
	}

	wantContainer := response.DockerContainerItem{
		ID: "c1", Name: "uni-center-core", Image: "uni-center-core:latest", State: "running",
		StatusText: "Up 16 hours", Created: 1789000000, StartedAt: 1789000100,
		CPUPercent: 0.6, MemUsageMB: 91, MemLimitMB: 1024,
		NetRXBytesSec: 2400, NetTXBytesSec: 1100,
		ComposeProject: "uni-center", ComposeService: "uni_core", Protected: true,
		Ports: []response.DockerPortItem{{IP: "0.0.0.0", PrivatePort: 8088, PublicPort: 20080, Type: "tcp"}},
	}
	if len(resp.Containers) != 1 || !reflect.DeepEqual(resp.Containers[0], wantContainer) {
		t.Fatalf("容器字段未逐项映射: got %+v want %+v", resp.Containers, wantContainer)
	}
	wantImage := response.DockerImageItem{
		ID: "sha256:545b", RepoTags: []string{"mysql:8.0.22"}, SizeMB: 545,
		Created: 1780000000, InUse: true, InUseBy: []string{"mysql"},
	}
	if len(resp.Images) != 1 || !reflect.DeepEqual(resp.Images[0], wantImage) {
		t.Fatalf("镜像字段未逐项映射: got %+v want %+v", resp.Images, wantImage)
	}
	wantVolume := response.DockerVolumeItem{
		Name: "uni-center-uploads", Driver: "local", SizeMB: &sizeMB,
		InUse: true, MountedBy: []string{"uni-center-core"},
	}
	if len(resp.Volumes) != 1 || !reflect.DeepEqual(resp.Volumes[0], wantVolume) {
		t.Fatalf("卷字段未逐项映射: got %+v want %+v", resp.Volumes, wantVolume)
	}
	wantNetwork := response.DockerNetworkItem{Name: "bridge", Driver: "bridge", Scope: "local", Internal: true, ContainersCount: 8}
	if len(resp.Networks) != 1 || !reflect.DeepEqual(resp.Networks[0], wantNetwork) {
		t.Fatalf("网络字段未逐项映射: got %+v want %+v", resp.Networks, wantNetwork)
	}
	wantProject := response.DockerProjectItem{
		Name: "uni-center", ConfigFiles: []string{"/data/UniCenter/docker-compose.yml"},
		State: "running", Services: 2, ContainersCount: 2,
	}
	if len(resp.Projects) != 1 || !reflect.DeepEqual(resp.Projects[0], wantProject) {
		t.Fatalf("项目字段未逐项映射: got %+v want %+v", resp.Projects, wantProject)
	}

	// 主机清单同一份快照：online 按设备表的 last_seen_at、IP 归一、计数取清单长度。
	hosts, err := svc.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts.List) != 1 {
		t.Fatalf("应有一台可管主机: %+v", hosts.List)
	}
	if got := hosts.List[0]; got.PrimaryIP != "192.168.12.105" || !got.Online || got.Stale ||
		got.Containers != 1 || got.Images != 1 || got.LastSync != now.Add(-5*time.Second).Unix() {
		t.Fatalf("主机摘要未逐项映射: %+v", got)
	}

	// docker 不可用：能力信号必须原样带出，且计数为 0（不是上一份清单的残留）。
	downState := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: false, Error: "无法连接 docker.sock"}
	if err := store.Save(ctx, 8, downState, now); err != nil {
		t.Fatal(err)
	}
	hosts, err = svc.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts.List) != 2 {
		t.Fatalf("两台主机都应出现在清单里: %+v", hosts.List)
	}
	// docker:hosts 是集合，行序不保证 —— 按 id 定位而不是按位置取。
	var bad *response.DockerHostItem
	for i := range hosts.List {
		if hosts.List[i].ID == 8 {
			bad = &hosts.List[i]
		}
	}
	if bad == nil {
		t.Fatalf("第二台主机缺失: %+v", hosts.List)
	}
	if bad.DockerOK || bad.Error != "无法连接 docker.sock" || bad.Containers != 0 || bad.Images != 0 {
		t.Fatalf("不可用主机的能力信号不符: %+v", *bad)
	}
}

// TestDockerServiceHostListPurgePolicy 「集合里的设备已删除」这条自愈路径有两个分支，
// 而它们的正确行为相反：确认删除才清理，读库失败必须原地跳过。
func TestDockerServiceHostListPurgePolicy(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	st := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true}
	if err := store.Save(ctx, 7, st, now); err != nil {
		t.Fatal(err)
	}

	// ① 读库失败：不编造主机行，也**不许**清理 —— 把 DB 抖动当成删除，
	// 会一次抹掉全部主机的快照键，页面从此空白到下一次上报。
	broken := NewDockerService(store, fakeDeviceReader{err: errors.New("db down")}, fakeConfigGetter{}, nil)
	resp, err := broken.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.List) != 0 {
		t.Fatalf("读库失败时不该给出主机行: %+v", resp.List)
	}
	if env, err := store.Get(ctx, 7); err != nil || env == nil {
		t.Fatal("读库失败不是「设备已删除」：快照键必须留着")
	}

	// ② 设备确已删除（仓储未命中哨兵）：顺手清理，否则每次列表都要跳过它、键永久泄漏。
	deleted := NewDockerService(store, notFoundReader{}, fakeConfigGetter{}, nil)
	if resp, err = deleted.Hosts(ctx); err != nil || len(resp.List) != 0 {
		t.Fatalf("已删设备不该再出现: %+v %v", resp, err)
	}
	if env, err := store.Get(ctx, 7); err != nil || env != nil {
		t.Fatal("已删设备的快照键必须清掉")
	}
	if ids, err := store.Hosts(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("已删设备必须从 docker:hosts 集合里移除: %v %v", ids, err)
	}
}

// notFoundReader 模拟「设备已删除」：仓储的未命中哨兵（与其它错误必须区别对待）。
type notFoundReader struct{}

func (notFoundReader) FindByID(context.Context, uint64) (*entity.Device, error) {
	return nil, repository.ErrNotFound
}
