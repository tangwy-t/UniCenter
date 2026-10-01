package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 控制塔总览（Overview）的测试。夹具沿用 docker_test.go 的 fakeDeviceReader +
// fakeConfigGetter + miniredis 组合；「现在」一律固定（svc.now），否则陈旧结论
// 会随执行时刻漂移。

// newDockerOverviewSvc 搭一个确定「现在」的读面，并把四台主机（3/7/8/9）登记进
// 设备表。多登记无妨：可管主机只按 docker:hosts 集合枚举（集合里有谁才读谁），
// 设备表里多几台不会被列出来。
func newDockerOverviewSvc(t *testing.T, store *dockerstate.Store, now time.Time) *DockerService {
	t.Helper()
	devs := map[uint64]*entity.Device{}
	for id, name := range map[uint64]string{3: "uni-103", 7: "uni-105", 8: "uni-106", 9: "uni-109"} {
		d := &entity.Device{Hostname: name}
		d.ID = id
		d.LastSeenAt = &now
		devs[id] = d
	}
	svc := NewDockerService(store,
		fakeDeviceReader{devs: devs},
		fakeConfigGetter{vals: map[string]string{
			"sys.docker.snapshotInterval": "30",
			"sys.agent.offlineThreshold":  "90"}}, nil)
	svc.now = func() time.Time { return now }
	return svc
}

// overviewContainer 是测试里容器的紧凑构造（state 是运行态，protected 是保护结论）。
func overviewContainer(name, state string, protected bool) agentproto.DockerContainer {
	return agentproto.DockerContainer{ID: "id-" + name, Name: name, Image: name + ":latest",
		State: state, StatusText: state + " text", Protected: protected}
}

// hostByID 从总览的 hosts 列表里按 id 取条目（集合枚举的行序不保证，按位置取会
// 跨实现漂移 —— 与 TestDockerServiceStateMapsFullSnapshot 的取法同款）。
func hostByID(t *testing.T, hosts []response.DockerHostItem, id uint64) response.DockerHostItem {
	t.Helper()
	for _, h := range hosts {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("hosts 里没有 id=%d 的主机: %+v", id, hosts)
	return response.DockerHostItem{}
}

// TestDockerOverviewFleetAggregation 多主机聚合的 KPI 数学：
// running/stopped/protected/unused 的计数与「每台主机各报一份」加起来要全等。
func TestDockerOverviewFleetAggregation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 7：2 容器（1 running 且受保护 / 1 exited）、2 镜像（1 用 1 闲）、
	// 2 卷（1 用 1 闲）、1 网络、1 项目（running）。
	st7 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("core", "running", true),
			overviewContainer("backup", "exited", false),
		},
		Images: []agentproto.DockerImage{
			{ID: "i1", InUse: true}, {ID: "i2", InUse: false},
		},
		Volumes: []agentproto.DockerVolume{
			{Name: "v1", InUse: true}, {Name: "v2", InUse: false},
		},
		Networks: []agentproto.DockerNetwork{{Name: "bridge"}},
		Projects: []agentproto.DockerProject{{Name: "uni-center", State: "running"}},
	}
	// 主机 8：1 容器（running）、1 镜像（闲）、0 卷 0 网络、1 项目（stopped）。
	st8 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("console", "running", false)},
		Images:     []agentproto.DockerImage{{ID: "i3", InUse: false}},
		Projects:   []agentproto.DockerProject{{Name: "other", State: "stopped"}},
	}
	if err := store.Save(ctx, 7, st7, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, st8, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Fleet
	if f.Hosts.Total != 2 || f.Hosts.DockerOK != 2 {
		t.Fatalf("主机 KPI 不符: %+v", f.Hosts)
	}
	if f.Containers.Total != 3 || f.Containers.Running != 2 || f.Containers.Stopped != 1 ||
		f.Containers.Protected != 1 {
		t.Fatalf("容器 KPI 不符（total/running/stopped/protected 应全等聚合）: %+v", f.Containers)
	}
	if f.Images.Total != 3 || f.Images.Unused != 2 {
		t.Fatalf("镜像 KPI 不符: %+v", f.Images)
	}
	if f.Volumes.Total != 2 || f.Volumes.Unused != 1 {
		t.Fatalf("卷 KPI 不符: %+v", f.Volumes)
	}
	if f.Networks.Total != 1 {
		t.Fatalf("网络 KPI 不符: %+v", f.Networks)
	}
	if f.Projects.Total != 2 || f.Projects.Running != 1 {
		t.Fatalf("项目 KPI 不符: %+v", f.Projects)
	}

	// hosts 条目复用 DockerHostItem 形态：每一行与 GET /docker/hosts 同一处口径。
	if len(resp.Hosts) != 2 {
		t.Fatalf("hosts 应有 2 台: %+v", resp.Hosts)
	}
	h7, h8 := hostByID(t, resp.Hosts, 7), hostByID(t, resp.Hosts, 8)
	if !h7.DockerOK || h7.Containers != 2 || h7.Images != 2 || h7.Hostname != "uni-105" {
		t.Fatalf("主机 7 的条目不符: %+v", h7)
	}
	if h8.Containers != 1 || h8.Images != 1 || h8.Hostname != "uni-106" {
		t.Fatalf("主机 8 的条目不符: %+v", h8)
	}

	// 异常清单：只有 exited 的 backup（running 一个都不能进）。
	if resp.Anomalies.Total != 1 || len(resp.Anomalies.Items) != 1 {
		t.Fatalf("异常清单应恰含 1 条: %+v", resp.Anomalies)
	}
	a := resp.Anomalies.Items[0]
	if a.ID != "id-backup" || a.HostID != 7 || a.Hostname != "uni-105" || a.Name != "backup" ||
		a.State != "exited" || a.Image != "backup:latest" || a.StatusText != "exited text" || a.Protected {
		t.Fatalf("异常条目未逐字段映射（含容器 ID，供详情页深链）: %+v", a)
	}
}

// TestDockerOverviewDiskAggregation 磁盘账目（6a）的聚合口径：
//   - 求和只统计**有 df 数据**的主机（disk.hosts 如实计数），缺报主机（旧版 agent
//     不发 disk_usage）不折算成零 ——「没报」不是「没占用」；
//   - 主机行的 disk 与快照同帧映射（占用四数 + 悬空镜像/未用卷计数），缺报主机
//     的 disk 保持 nil（页面渲染「数据不可用」）。
func TestDockerOverviewDiskAggregation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 7：带 df（2 镜像：1 用 + 1 悬空；2 卷：1 用 + 1 闲）。
	st7 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("core", "running", false)},
		Images: []agentproto.DockerImage{
			{ID: "i1", InUse: true}, {ID: "i2", InUse: false, Dangling: true},
		},
		Volumes: []agentproto.DockerVolume{
			{Name: "v1", InUse: true}, {Name: "v2", InUse: false},
		},
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 12.5, ImagesDanglingMB: 2.25,
			VolumesTotalMB: 3.5, BuildCacheMB: 0},
	}
	// 主机 8：**没有 df**（旧版 agent 的形态：字段缺席），资源清单照常。
	st8 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("console", "running", false)},
		Images:     []agentproto.DockerImage{{ID: "i3", InUse: false}},
	}
	if err := store.Save(ctx, 7, st7, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, st8, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := resp.Fleet.Disk
	// 只有主机 7 报了磁盘账：Hosts=1（不是 2），合计里没有主机 8 的假零。
	if d.Hosts != 1 {
		t.Fatalf("有 df 数据的主机数应为 1（缺报主机不计数）: %+v", d)
	}
	if d.ImagesTotalMB != 12.5 || d.ImagesDanglingMB != 2.25 || d.VolumesTotalMB != 3.5 || d.BuildCacheMB != 0 {
		t.Fatalf("df 求和必须只算报了磁盘账的主机: %+v", d)
	}
	// 计数类 KPI 不受 df 缺报影响（那是清单事实，另一条通道）。
	if resp.Fleet.Images.Total != 3 || resp.Fleet.Images.Unused != 2 {
		t.Fatalf("镜像计数照常聚合（与 df 无关）: %+v", resp.Fleet.Images)
	}

	h7, h8 := hostByID(t, resp.Hosts, 7), hostByID(t, resp.Hosts, 8)
	if h7.Disk == nil {
		t.Fatal("有 df 数据的主机行必须带 disk 账目")
	}
	if h7.Disk.ImagesMB != 12.5 || h7.Disk.VolumesMB != 3.5 || h7.Disk.BuildCacheMB != 0 ||
		h7.Disk.ImagesDanglingMB != 2.25 {
		t.Fatalf("主机行 df 四数必须同帧映射: %+v", h7.Disk)
	}
	// 悬空/未用计数来自清单判据（与镜像页、卷页同一口径）。
	if h7.Disk.DanglingImages != 1 || h7.Disk.UnusedVolumes != 1 {
		t.Fatalf("悬空镜像/未用卷计数必须来自清单: %+v", h7.Disk)
	}
	if h8.Disk != nil {
		t.Fatalf("缺报主机的 disk 必须保持 nil（不是零值账目）: %+v", h8.Disk)
	}

	// 多主机求和：主机 9 也带 df → 合计是两台的账、Hosts=2。
	st9 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 10, ImagesDanglingMB: 1,
			VolumesTotalMB: 0.5, BuildCacheMB: 4}}
	if err := store.Save(ctx, 9, st9, now); err != nil {
		t.Fatal(err)
	}
	resp2, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d2 := resp2.Fleet.Disk
	if d2.Hosts != 2 || d2.ImagesTotalMB != 22.5 || d2.ImagesDanglingMB != 3.25 ||
		d2.VolumesTotalMB != 4 || d2.BuildCacheMB != 4 {
		t.Fatalf("两台有 df 的主机应逐项求和: %+v", d2)
	}
}

// TestDockerOverviewDiskStaleCounts 缺 df 的三种形态都不折算成零：stale 主机照常计入
// （最后已知事实），读失败/从未上报主机不计入（那台连清单都没有，磁盘账更无从谈起）。
func TestDockerOverviewDiskStaleCounts(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 7：2 分钟前的 df（stale，但照常计入 —— 与容器 KPI 同一句话）。
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 5}}, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 主机 8：键值损坏（读失败，行还在、无资源事实）。
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 100}}, now); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, dockerstate.StateKey(8), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Fleet.Disk.Hosts != 1 || resp.Fleet.Disk.ImagesTotalMB != 5 {
		t.Fatalf("stale 主机的 df 照常计入，读失败主机不计入: %+v", resp.Fleet.Disk)
	}
	if !hostByID(t, resp.Hosts, 7).Stale || hostByID(t, resp.Hosts, 7).Disk == nil {
		t.Fatalf("stale 主机的行要带 stale 标记与最后已知的磁盘账: %+v", hostByID(t, resp.Hosts, 7))
	}
	if hostByID(t, resp.Hosts, 8).Disk != nil {
		t.Fatalf("读失败主机不得编造磁盘账: %+v", hostByID(t, resp.Hosts, 8))
	}
}

// TestDockerOverviewAnomaliesOnlyNonRunning 异常清单只收非 running（exited/created/
// paused/dead 同口径），且**按主机 id 排序**（与集合的枚举顺序无关）。
func TestDockerOverviewAnomaliesOnlyNonRunning(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	st9 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("a-paused", "paused", true),
			overviewContainer("b-running", "running", false),
			overviewContainer("c-created", "created", false),
		}}
	// 主机 3（比 9 小）：故意**后**写入 —— 若排序依赖枚举顺序，3 会排在 9 后面。
	st3 := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("d-dead", "dead", false),
			overviewContainer("e-running", "running", true),
		}}
	if err := store.Save(ctx, 9, st9, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 3, st3, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Anomalies.Total != 3 || len(resp.Anomalies.Items) != 3 {
		t.Fatalf("异常清单应恰含 3 条（2 running 不得进入）: %+v", resp.Anomalies)
	}
	wantOrder := []struct {
		host uint64
		name string
	}{
		{3, "d-dead"},
		{9, "a-paused"},
		{9, "c-created"},
	}
	for i, want := range wantOrder {
		got := resp.Anomalies.Items[i]
		if got.HostID != want.host || got.Name != want.name {
			t.Fatalf("异常清单第 %d 条 = %d/%s, want %d/%s（须按主机 id 升序、同机按名称）",
				i, got.HostID, got.Name, want.host, want.name)
		}
	}
	// protected 结论要带进清单（控制塔要让运维一眼看出「这是动不得的容器」）。
	if !resp.Anomalies.Items[1].Protected {
		t.Fatalf("受保护结论未带入异常条目: %+v", resp.Anomalies.Items[1])
	}
}

// TestDockerOverviewAnomalyCap 异常清单上限 50 条且**截断先报全量**：
// Total 是截断前的真数，Items 只留前 50（按主机 id 排）。
func TestDockerOverviewAnomalyCap(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 7 报 60 个 exited 容器（超过上限），主机 8 报 1 个 running（不进清单）。
	big := make([]agentproto.DockerContainer, 0, 60)
	for i := 0; i < 60; i++ {
		big = append(big, overviewContainer(fmt.Sprintf("dead-%02d", i), "exited", false))
	}
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: big}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("ok", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Anomalies.Total != 60 {
		t.Fatalf("Anomalies.Total 必须是截断前全量 = 60, got %d（fleet 的 stopped 也应是 60）",
			resp.Anomalies.Total)
	}
	if len(resp.Anomalies.Items) != 50 {
		t.Fatalf("异常清单必须截到 50 条, got %d", len(resp.Anomalies.Items))
	}
	for i, it := range resp.Anomalies.Items {
		if i > 0 && it.Name <= resp.Anomalies.Items[i-1].Name {
			t.Fatalf("截断后的条目必须保持排序: %s 在 %s 后", it.Name, resp.Anomalies.Items[i-1].Name)
		}
	}
	// 截断只砍清单不动 KPI：stopped 仍应是全量 60（Total=Running+Stopped 恒等式）。
	if resp.Fleet.Containers.Stopped != 60 || resp.Fleet.Containers.Total != 61 {
		t.Fatalf("截断不得影响 KPI 全量计数: %+v", resp.Fleet.Containers)
	}
}

// TestDockerOverviewReadFailureDegradesOneHost 单台快照读失败（键值损坏）不炸整体：
// 失败主机仍出现在 hosts（error 字段如实），只是它的资源不参与 KPI ——
// 部分聚合优于整体 500。
func TestDockerOverviewReadFailureDegradesOneHost(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("core", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("console", "running", false),
			overviewContainer("stopped-1", "exited", false),
		}}, now); err != nil {
		t.Fatal(err)
	}
	// 直接把主机 7 的快照键覆盖成**坏 JSON**（模拟键值损坏/反序列化失败）：
	// 这时 store.Get 返回错误而不是「从未上报」的 (nil, nil)。
	if err := rdb.Set(ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatalf("单台读失败不得让总览整体失败: %v", err)
	}
	if resp.Fleet.Hosts.Total != 2 {
		t.Fatalf("读失败的主机仍是一台主机（hosts.total 必须含它）: %+v", resp.Fleet.Hosts)
	}
	if resp.Fleet.Hosts.DockerOK != 1 {
		t.Fatalf("读失败主机不得计入 dockerOk: %+v", resp.Fleet.Hosts)
	}
	bad := hostByID(t, resp.Hosts, 7)
	if bad.Error != "读取资源快照失败" || bad.DockerOK || bad.Containers != 0 || bad.Stale {
		t.Fatalf("读失败主机必须如实标注 error 且不编造计数: %+v", bad)
	}
	if bad.Hostname != "uni-105" {
		t.Fatalf("读失败主机的设备行信息仍要带出（主机名等）: %+v", bad)
	}
	// KPI 只聚合读到的：只算主机 8 的 2 个容器、1 个异常。
	if resp.Fleet.Containers.Total != 2 || resp.Fleet.Containers.Running != 1 {
		t.Fatalf("读失败主机的资源不得参与 KPI: %+v", resp.Fleet.Containers)
	}
	if resp.Anomalies.Total != 1 || resp.Anomalies.Items[0].Name != "stopped-1" {
		t.Fatalf("异常清单只收读到的: %+v", resp.Anomalies)
	}
}

// TestDockerOverviewStaleHostsCountInKPI stale 主机的数据**仍计入 KPI**（它是最后
// 已知事实），但条目带 stale 标记告诉读者「打过折再看」。
func TestDockerOverviewStaleHostsCountInKPI(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	stale := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("old-core", "running", false)}}
	if err := store.Save(ctx, 7, stale, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fresh := &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("new-core", "running", false)}}
	if err := store.Save(ctx, 8, fresh, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 30s 周期下陈旧阈值 90s：2 分钟前的必须判 stale（与 Hosts 的判定同一函数）。
	if !hostByID(t, resp.Hosts, 7).Stale {
		t.Fatalf("2 分钟前的快照必须带 stale 标记: %+v", resp.Hosts)
	}
	if hostByID(t, resp.Hosts, 8).Stale {
		t.Fatalf("刚上报的快照不得判 stale: %+v", resp.Hosts)
	}
	// stale ≠ 不存在：它的容器照样进 KPI 与 anomalies 口径。
	if resp.Fleet.Containers.Total != 2 || resp.Fleet.Containers.Running != 2 {
		t.Fatalf("stale 主机的容器必须计入 KPI（最后已知事实）: %+v", resp.Fleet.Containers)
	}
	if resp.Fleet.Hosts.DockerOK != 2 {
		t.Fatalf("stale 主机的 dockerOk 结论仍计入: %+v", resp.Fleet.Hosts)
	}
}
