package dockerops

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// stubAPI 是 DockerAPI 的手写替身（CI 没有 docker daemon，全部采集逻辑走它）。
//
// 它一次定义**全部**会被后续任务用到的字段（含只读执行器 Task B4 需要的 inspect/logs/yml
// 返回值）：替身散在两个测试文件里各长一半，会让「加一个方法」变成两处改动 + 一次编译失败。
type stubAPI struct {
	pingErr    error
	containers []ContainerInfo
	images     []ImageInfo
	volumes    []VolumeInfo
	networks   []NetworkInfo
	stats      map[string]StatsInfo
	statsErr   map[string]error
	flavor     string
	flavorVer  string

	// 列表读取的失败注入（B3 的 TestSnapshotFailsClosedOnListError 用）。
	imagesErr error
	// 以下四项供 Task B4 的只读执行器测试使用。
	containerDetail ContainerDetail
	imageDetail     ImageDetail
	logLines        string
	logTruncated    bool
	composeContent  string
}

func (s *stubAPI) Ping(context.Context) error { return s.pingErr }
func (s *stubAPI) Containers(context.Context) ([]ContainerInfo, error) {
	return s.containers, nil
}
func (s *stubAPI) ContainerStats(_ context.Context, id string) (StatsInfo, error) {
	if err := s.statsErr[id]; err != nil {
		return StatsInfo{}, err
	}
	return s.stats[id], nil
}
func (s *stubAPI) Images(context.Context) ([]ImageInfo, error) {
	if s.imagesErr != nil {
		return nil, s.imagesErr
	}
	return s.images, nil
}
func (s *stubAPI) Volumes(context.Context) ([]VolumeInfo, error)   { return s.volumes, nil }
func (s *stubAPI) Networks(context.Context) ([]NetworkInfo, error) { return s.networks, nil }
func (s *stubAPI) ContainerInspect(context.Context, string) (ContainerDetail, error) {
	return s.containerDetail, nil
}
func (s *stubAPI) ContainerLogs(context.Context, string, int, int64) (string, bool, error) {
	return s.logLines, s.logTruncated, nil
}
func (s *stubAPI) ImageInspect(context.Context, string) (ImageDetail, error) {
	return s.imageDetail, nil
}
func (s *stubAPI) ComposeVersion(context.Context) (string, string, error) {
	return s.flavor, s.flavorVer, nil
}

func testLogger() *recLogger { return &recLogger{} }

type recLogger struct{ warns []string }

func (l *recLogger) Info(string, ...any)  {}
func (l *recLogger) Debug(string, ...any) {}
func (l *recLogger) Warn(msg string, kv ...any) {
	l.warns = append(l.warns, msg)
}

func newTestSnapshotter(api DockerAPI, protected string, log Logger) *Snapshotter {
	s := NewSnapshotter(api, ParseProtected(protected), log, func() time.Time { return time.Unix(1790000000, 0) })
	s.SetCompose(agentproto.DockerComposeFlavorPlugin, "v2.27.0")
	return s
}

// 按下标取条目会把「daemon 恰好给的顺序」也钉进断言 —— 而快照的五类清单**在 agent 侧
// 已按名字升序排好**（sortState，Plan 1b：「顺序由 agent 保证 —— 页面不重排」），下标与
// 输入顺序不再对应。故一律按名字/ID 取条目，排序本身另用显式断言守住。
func findContainer(t *testing.T, cs []agentproto.DockerContainer, name string) agentproto.DockerContainer {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("快照里没有容器 %q", name)
	return agentproto.DockerContainer{}
}

func findImage(t *testing.T, is []agentproto.DockerImage, id string) agentproto.DockerImage {
	t.Helper()
	for _, im := range is {
		if im.ID == id {
			return im
		}
	}
	t.Fatalf("快照里没有镜像 %q", id)
	return agentproto.DockerImage{}
}

func findVolume(t *testing.T, vs []agentproto.DockerVolume, name string) agentproto.DockerVolume {
	t.Helper()
	for _, v := range vs {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("快照里没有卷 %q", name)
	return agentproto.DockerVolume{}
}

func containerNames(cs []agentproto.DockerContainer) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// imageSortKeys 复刻「有标签用第一个 tag、无标签用 id」这条排序键，用来独立断言镜像顺序。
func imageSortKeys(is []agentproto.DockerImage) []string {
	out := make([]string, 0, len(is))
	for _, im := range is {
		key := im.ID
		if len(im.RepoTags) > 0 {
			key = im.RepoTags[0]
		}
		out = append(out, key)
	}
	return out
}

// 不可达时必须是**能力信号**：docker_ok=false + 结论句 + 空清单（协议强制），
// 且采集绝不能返回 nil（调用方会直接把它发出去）。
func TestSnapshotDockerUnavailable(t *testing.T) {
	api := &stubAPI{pingErr: errors.New("无法连接 docker.sock（Docker 服务未运行？）")}
	st := newTestSnapshotter(api, "", testLogger()).Collect(context.Background())
	if st == nil {
		t.Fatal("不可达时必须返回一帧（而不是 nil）")
	}
	if st.DockerOK {
		t.Fatal("Ping 失败时 docker_ok 必须为 false")
	}
	if st.Error != "无法连接 docker.sock（Docker 服务未运行？）" {
		t.Fatalf("结论句必须原样带上（页面显示它），实际 %q", st.Error)
	}
	if len(st.Containers)+len(st.Images)+len(st.Volumes)+len(st.Networks)+len(st.Projects) != 0 {
		t.Fatal("不可达时清单必须为空（协议层会拒，这里先自己守住）")
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("不可达帧必须能过协议校验: %v", err)
	}
}

// 正常采集：四类资源 + 项目归纳 + 保护标记 + compose 形态。
func TestSnapshotCollectsEverything(t *testing.T) {
	api := &stubAPI{
		containers: []ContainerInfo{
			{ID: "c1", Name: "uni-center-core", Image: "uni-center-core:latest", ImageID: "sha256:aaa",
				State: "running", Status: "Up 16 hours", Created: 1789000000,
				Labels: map[string]string{composeProjectLabel: "uni-center", composeServiceLabel: "uni_core",
					composeConfigFilesLabel: "/data/UniCenter/docker-compose.yml"},
				Ports:  []PortInfo{{Private: 8088, Public: 20080, Type: "tcp"}},
				Mounts: []MountInfo{{Type: "volume", Source: "uni-center-uploads", Destination: "/app/uploads", RW: true}}},
			{ID: "c2", Name: "mysql", Image: "mysql:8.0.22", ImageID: "sha256:bbb",
				State: "running", Status: "Up 3 months", Created: 1770000000},
			{ID: "c3", Name: "zentao", Image: "zentao:21.7", ImageID: "sha256:ccc",
				State: "exited", Status: "Exited (0) 8 months ago", Created: 1700000000},
		},
		stats: map[string]StatsInfo{
			"c1": {CPUPercent: 0.6, MemUsageMB: 91, MemLimitMB: 1024, NetRXBytes: 2400, NetTXBytes: 1100},
		},
		images: []ImageInfo{
			{ID: "sha256:aaa", RepoTags: []string{"uni-center-core:latest"}, SizeBytes: 96 << 20, Created: 1789000000},
			{ID: "sha256:ddd", RepoTags: nil, SizeBytes: 1 << 30, Created: 1788000000},
		},
		volumes:  []VolumeInfo{{Name: "uni-center-uploads", Driver: "local", SizeBytes: ptrInt64(13 << 20)}, {Name: "other", Driver: "local"}},
		networks: []NetworkInfo{{Name: "bridge", Driver: "bridge", Scope: "local", ContainersCount: 8}},
	}
	st := newTestSnapshotter(api, defaultProtected, testLogger()).Collect(context.Background())
	if err := st.Validate(); err != nil {
		t.Fatalf("采集结果必须能过协议校验: %v", err)
	}
	if !st.DockerOK || len(st.Containers) != 3 || len(st.Projects) != 1 || len(st.Images) != 2 {
		t.Fatalf("采集不完整: ok=%v containers=%d projects=%d images=%d", st.DockerOK, len(st.Containers), len(st.Projects), len(st.Images))
	}
	// 顺序即契约：daemon 的返回顺序无保证，而前端翻页、快照 diff 与「这台机器有没有变化」
	// 的判断都建立在顺序稳定之上 —— 五类清单必须各按名字升序（镜像按第一个 tag，无标签按 id）。
	if want := []string{"mysql", "uni-center-core", "zentao"}; !slices.Equal(containerNames(st.Containers), want) {
		t.Fatalf("容器必须按名字升序，实际 %v", containerNames(st.Containers))
	}
	if want := []string{"sha256:ddd", "uni-center-core:latest"}; !slices.Equal(imageSortKeys(st.Images), want) {
		t.Fatalf("镜像必须按第一个 repo tag（无标签按 id）升序，实际 %v", imageSortKeys(st.Images))
	}
	if st.Volumes[0].Name != "other" || st.Volumes[1].Name != "uni-center-uploads" {
		t.Fatalf("卷必须按名字升序，实际 %s,%s", st.Volumes[0].Name, st.Volumes[1].Name)
	}
	// compose 元数据 + 保护标记
	c1 := findContainer(t, st.Containers, "uni-center-core")
	if c1.ComposeProject != "uni-center" || c1.ComposeService != "uni_core" {
		t.Fatalf("compose 标签未带出: %+v", c1)
	}
	if !c1.Protected || !findContainer(t, st.Containers, "mysql").Protected {
		t.Fatal("底座容器与 mysql 都必须被标记为受保护（清单来自默认种子值）")
	}
	if findContainer(t, st.Containers, "zentao").Protected {
		t.Fatal("zentao 不在清单里，不该被标记")
	}
	// 镜像：悬空 + 使用中（in_use_by 用容器名，比 id 对运维有意义）
	dangling := findImage(t, st.Images, "sha256:ddd")
	if !dangling.Dangling || dangling.InUse {
		t.Fatalf("无标签镜像应判悬空且未被使用: %+v", dangling)
	}
	inUse := findImage(t, st.Images, "sha256:aaa")
	if !inUse.InUse || len(inUse.InUseBy) != 1 || inUse.InUseBy[0] != "uni-center-core" {
		t.Fatalf("在用镜像必须带上使用它的容器名: %+v", inUse)
	}
	// 卷：用量有值/未知两种
	uploads := findVolume(t, st.Volumes, "uni-center-uploads")
	if uploads.SizeMB == nil || *uploads.SizeMB == 0 {
		t.Fatalf("有用量数据的卷必须带上体积: %+v", uploads)
	}
	if other := findVolume(t, st.Volumes, "other"); other.SizeMB != nil {
		t.Fatalf("用量未知必须是 nil（页面显示「—」而不是 0）: %+v", other)
	}
	if !uploads.InUse || uploads.MountedBy[0] != "uni-center-core" {
		t.Fatalf("被挂载的卷必须标记在用并给出容器名: %+v", uploads)
	}
	// 项目归纳
	pr := st.Projects[0]
	if pr.Name != "uni-center" || pr.Services != 1 || pr.ContainersCount != 1 {
		t.Fatalf("项目归纳不符: %+v", pr)
	}
	if pr.State != "running" {
		t.Fatalf("项目内全部容器在运行 → running，实际 %q", pr.State)
	}
	if len(pr.ConfigFiles) != 1 || pr.ConfigFiles[0] != "/data/UniCenter/docker-compose.yml" {
		t.Fatalf("配置文件路径应来自标签: %+v", pr)
	}
	if st.Compose == nil || st.Compose.Flavor != agentproto.DockerComposeFlavorPlugin || st.Compose.Version != "v2.27.0" {
		t.Fatalf("compose 形态未带出: %+v", st.Compose)
	}
	// 速率：首帧没有基线 → 0（与 collect 包的预热帧同一取舍）
	if c1.NetRXBytesSec != 0 {
		t.Fatalf("首帧速率必须为 0（没有上一次读数）: %v", c1.NetRXBytesSec)
	}
	// 端口映射逐项带出（容器详情页与列表页共用同一套字段）
	if len(c1.Ports) != 1 || c1.Ports[0].PrivatePort != 8088 || c1.Ports[0].PublicPort != 20080 || c1.Ports[0].Type != "tcp" {
		t.Fatalf("端口映射未带出: %+v", c1.Ports)
	}
}

// 第二帧才有速率：累计计数器的差除以真实间隔。
func TestSnapshotComputesNetRateOnSecondTick(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}},
		stats: map[string]StatsInfo{"c1": {NetRXBytes: 1000, NetTXBytes: 500}}}
	now := time.Unix(1790000000, 0)
	s := NewSnapshotter(api, ParseProtected(""), testLogger(), func() time.Time { return now })
	if st := s.Collect(context.Background()); st.Containers[0].NetRXBytesSec != 0 {
		t.Fatal("首帧速率必须为 0")
	}
	// 30 秒后累计值 +3000/+1500 → 100 B/s、50 B/s
	api.stats["c1"] = StatsInfo{NetRXBytes: 4000, NetTXBytes: 2000}
	now = now.Add(30 * time.Second)
	st := s.Collect(context.Background())
	if got := st.Containers[0].NetRXBytesSec; got != 100 {
		t.Fatalf("RX 速率应为 100 B/s，实际 %v", got)
	}
	if got := st.Containers[0].NetTXBytesSec; got != 50 {
		t.Fatalf("TX 速率应为 50 B/s，实际 %v", got)
	}
}

// 单个容器的 stats 取不到：该容器读数置零，但**整帧仍然有效**（一次 stats 失败
// 不该让整台主机的快照消失）。
func TestSnapshotToleratesPerContainerStatsFailure(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}},
		statsErr: map[string]error{"c1": errors.New("no such container")}}
	log := testLogger()
	st := newTestSnapshotter(api, "", log).Collect(context.Background())
	if !st.DockerOK || len(st.Containers) != 1 {
		t.Fatal("单容器读数失败不该让整帧失败")
	}
	if len(log.warns) == 0 {
		t.Fatal("必须留下告警（否则「为什么这一台没有读数」无从排查）")
	}
	// 跨帧的基线纪律：取不到读数的那一帧**不得充当基线** —— 把它的 0 当成上一次读数，
	// 下一帧（哪怕读到了）会从 0 起算，凭空造出一个巨大的假速率。
	api2 := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}},
		stats: map[string]StatsInfo{"c1": {NetRXBytes: 1000}}}
	now := time.Unix(1790000000, 0)
	s2 := NewSnapshotter(api2, ParseProtected(""), testLogger(), func() time.Time { return now })
	s2.Collect(context.Background()) // 首帧：建立基线
	api2.statsErr = map[string]error{"c1": errors.New("daemon busy")}
	now = now.Add(30 * time.Second)
	if got := s2.Collect(context.Background()).Containers[0].NetRXBytesSec; got != 0 {
		t.Fatalf("取不到读数的那一帧速率必须为 0，实际 %v", got)
	}
	delete(api2.statsErr, "c1")
	api2.stats["c1"] = StatsInfo{NetRXBytes: 5000}
	now = now.Add(30 * time.Second)
	if got := s2.Collect(context.Background()).Containers[0].NetRXBytesSec; got != 0 {
		t.Fatalf("失败帧不得留下基线（否则下一帧的速率是假值），实际 %v", got)
	}
}

// 清单读取失败时整帧判为不可用：宁可说「Docker 不可用（名单读取异常）」，
// 也不给出「容器有、镜像空」这种会被当成「确实没有镜像」的一半数据。
func TestSnapshotFailsClosedOnListError(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}}}
	api.imagesErr = errors.New("daemon busy")
	st := newTestSnapshotter(api, "", testLogger()).Collect(context.Background())
	if st.DockerOK {
		t.Fatal("名单读取失败必须整帧判为不可用（一半数据比说不可用更糟）")
	}
	if st.Error == "" || len(st.Containers) != 0 {
		t.Fatalf("应是结论句 + 空清单: %+v", st)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("必须能过协议校验: %v", err)
	}
}

// 一帧快照必须远小于协议单消息上限（1MB），否则它就是那个「把整条通道撑爆」的载荷。
// 用 200 个容器的极端规模做上界：真实 .105 是 10 个容器级。
func TestSnapshotSizeStaysUnderMessageLimit(t *testing.T) {
	cs := make([]ContainerInfo, 0, 200)
	stats := map[string]StatsInfo{}
	for i := 0; i < 200; i++ {
		id := "c" + strconv.Itoa(i)
		cs = append(cs, ContainerInfo{ID: id, Name: "container-with-a-rather-long-name-" + id,
			Image: "registry.example.com/team/service:" + id, ImageID: "sha256:" + id,
			State: "running", Status: "Up 3 days (healthy)", Created: 1789000000,
			Labels: map[string]string{composeProjectLabel: "proj", composeServiceLabel: "svc" + id},
			Ports:  []PortInfo{{Private: 8080, Public: 20000 + i, Type: "tcp"}}})
		stats[id] = StatsInfo{CPUPercent: 12.34, MemUsageMB: 512.5, MemLimitMB: 1024, NetRXBytes: 1 << 20}
	}
	api := &stubAPI{containers: cs, stats: stats}
	st := newTestSnapshotter(api, defaultProtected, testLogger()).Collect(context.Background())
	msg, err := agentproto.NewMessage("1", agentproto.TypeAgentDockerState, st)
	if err != nil {
		t.Fatal(err)
	}
	b, err := msg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 512<<10 {
		t.Fatalf("200 容器的快照 %d 字节 —— 已逼近单消息上限，需要收窄字段或分片", len(b))
	}
}

func ptrInt64(v int64) *int64 { return &v }
