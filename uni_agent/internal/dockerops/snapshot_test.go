package dockerops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// df 是卷采集同趟 system df 算出的占用汇总（6a）；nil = 这帧没有 df 数据。
	df        *DiskUsageSummary
	networks  []NetworkInfo
	stats     map[string]StatsInfo
	statsErr  map[string]error
	flavor    string
	flavorVer string

	// 列表读取的失败注入（B3 的 TestSnapshotFailsClosedOnListError 用）。
	imagesErr error
	// 以下四项供 Task B4 的只读执行器测试使用。
	containerDetail ContainerDetail
	imageDetail     ImageDetail
	logLines        string
	logTruncated    bool
	composeContent  string

	// ── 二期写操作的记录型实现（Task B1）────────────────────────────────────
	// 每个写方法只把收到的参数记进下面的切片（不碰 docker.sock、不真跑 docker 命令、
	// 不改变状态），供断言「执行器到底调了什么、带没带 force/overwrite/拼出的路径」。
	started         []string
	stopped         []string
	restarted       []string
	removed         []containerRemoveCall
	imagesRemoved   []imageRemoveCall
	imagePrunedAll  []bool
	imagePruneFreed int64
	pulled          []string
	// pullAuths 记录每次 ImagePull 收到的认证（4c）：nil 槽 = 无凭据拉取。
	pullAuths []*ImageAuth
	// pullCh / pullErr 是 image:pull 的**进度流替身**（4b）：pullCh 非 nil 时逐条把
	// 进度记录交给 emit（关闭 = 拉取结束、返回 pullErr），ctx 取消即时返回；nil =
	// 一次性成功/失败（老用例的一期语义）。
	pullCh  chan PullProgress
	pullErr error
	// imageRefIDs / imageRefErr 是 ImageRefID 的替身面（B4 完成判据的对照项）：
	// imageRefIDs 是**逐次调用的观测序列**（用尽后沿用最后一项）—— 拉取路径会先问
	// 「拉取前有没有」（第一项），结算时再问「现在有没有」（第二项），用序列而不是
	// 固定值才能在一个用例里确定性地表达「拉取期间镜像落地了」。imageRefErr 注入
	// 「查询本身失败」（不可作判据的那一档）。
	imageRefIDs   []string
	imageRefErr   error
	imageRefN     int
	imageRefCalls []string
	// repoDigests / repoDigestErr 是 ImageRepoDigests 的替身面（推送完成判据的对照项，
	// 与 imageRefIDs 同一形态）：repoDigests 是**逐次调用的观测序列**（用尽后沿用最后
	// 一项）—— 推送路径会先问「推送前有哪些 digest 引用」（第一项），结算时再问
	// 「现在有哪些」（第二项）；repoDigestErr 注入「查询本身失败」（不可作判据的那一档）。
	repoDigests     [][]string
	repoDigestErr   error
	repoDigestN     int
	repoDigestCalls []string
	tagged          []tagCall
	saved           []saveCall
	loaded          []string
	// P2·分发面：build/push 的**定型记录 + 进度流替身**（与 pull 同款语义：
	// buildCh/pushCh 非 nil 时逐条交给 emit，关闭返回 buildErr/pushErr；
	// nil = 一次性成功/失败）。
	built            []buildCall
	buildCh          chan BuildProgress
	buildErr         error
	pushed           []pushCall
	pushCh           chan PullProgress
	pushErr          error
	volumesRemoved   []volumeRemoveCall
	volumePruneCalls int
	volumePruneFreed int64
	networksRemoved  []string
	// saveAlreadyExists：目标已存在且调用方未要求覆盖时，ImageSave 返回 already_exists。
	saveAlreadyExists bool

	// ── 四支柱·创建面（4a）──────────────────────────────────────────────────
	// created 记录每次 ContainerCreate 收到的定型参数（断言执行器的解析与映射）；
	// createReply 是替身回给执行器的容器 ID；createErr / startErr 注入失败。
	created     []containerCreateCall
	createReply string
	createErr   error
	startErr    error

	// ── 三期流会话（S1）────────────────────────────────────────────────────
	// 记录与读取都可能发生在会话 goroutine 里，故这一组统一走 streamMu。
	streamMu       sync.Mutex
	logStream      io.ReadCloser
	logStreamFn    func() io.ReadCloser
	logStreamErr   error
	logFollowCalls []logsFollowCall
	execSession    *ExecSession
	execErr        error
	execCalls      []execCall
	resized        []termSize

	// ── 监控面 stats 流 ────────────────────────────────────────────────────
	statsStream      chan StatsSample
	statsCloser      *fakeCloser
	statsStreamErr   error
	statsStreamCalls []string

	// ── 事件流（docker:events，常驻订阅）────────────────────────────────────
	eventsStream chan EventItem
	eventsCloser io.Closer
	eventsErr    error
	eventsCalls  int
}

// 记录型替身用的参数快照（字段名与被记的方法参数一一对应）。
type containerRemoveCall struct {
	id    string
	force bool
}

type imageRemoveCall struct {
	ref   string
	force bool
}

type tagCall struct{ src, dst string }

type saveCall struct {
	ref, path string
	overwrite bool
}

// buildCall / pushCall 是 P2 的定型参数记录（断言执行器的解析与映射）。
type buildCall struct{ spec BuildSpec }

type pushCall struct {
	ref  string
	auth *ImageAuth
}

type volumeRemoveCall struct {
	name  string
	force bool
}

// containerCreateCall 是 ContainerCreate 替身记录的一次调用参数。
type containerCreateCall struct {
	spec ContainerCreateSpec
}

func (s *stubAPI) ContainerCreate(_ context.Context, spec ContainerCreateSpec) (string, error) {
	if s.createErr != nil {
		return "", s.createErr
	}
	s.created = append(s.created, containerCreateCall{spec})
	if s.createReply == "" {
		return "a1b2c3d4e5f6a7b8c9d0e1f", nil
	}
	return s.createReply, nil
}

func (s *stubAPI) ContainerStart(_ context.Context, id string) error {
	if s.startErr != nil {
		return s.startErr
	}
	s.started = append(s.started, id)
	return nil
}

func (s *stubAPI) ContainerStop(_ context.Context, id string) error {
	s.stopped = append(s.stopped, id)
	return nil
}

func (s *stubAPI) ContainerRestart(_ context.Context, id string) error {
	s.restarted = append(s.restarted, id)
	return nil
}

func (s *stubAPI) ContainerRemove(_ context.Context, id string, force bool) error {
	s.removed = append(s.removed, containerRemoveCall{id: id, force: force})
	return nil
}

func (s *stubAPI) ImageRemove(_ context.Context, ref string, force bool) error {
	s.imagesRemoved = append(s.imagesRemoved, imageRemoveCall{ref: ref, force: force})
	return nil
}

func (s *stubAPI) ImagePrune(_ context.Context, all bool) (int64, error) {
	s.imagePrunedAll = append(s.imagePrunedAll, all)
	return s.imagePruneFreed, nil
}

func (s *stubAPI) ImagePull(ctx context.Context, ref string, auth *ImageAuth, emit func(PullProgress)) error {
	s.pulled = append(s.pulled, ref)
	s.pullAuths = append(s.pullAuths, auth)
	if s.pullCh == nil {
		return s.pullErr
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-s.pullCh:
			if !ok {
				return s.pullErr
			}
			if emit != nil {
				emit(p)
			}
		}
	}
}

func (s *stubAPI) ImageTag(_ context.Context, src, dst string) error {
	s.tagged = append(s.tagged, tagCall{src: src, dst: dst})
	return nil
}

func (s *stubAPI) ImageSave(_ context.Context, ref, path string, overwrite bool) (bool, error) {
	s.saved = append(s.saved, saveCall{ref: ref, path: path, overwrite: overwrite})
	if s.saveAlreadyExists && !overwrite {
		return true, nil
	}
	return false, nil
}

func (s *stubAPI) ImageLoad(_ context.Context, path string) error {
	s.loaded = append(s.loaded, path)
	return nil
}

// ImageBuild / ImagePush 替身（P2）：与 ImagePull 同一记录型实现 —— 只把收到的
// 定型参数记进切片（不碰 docker.sock、不做上下文 tar 校验 —— 校验在 adapter 的
// scanBuildContext，替身只让它「通过」），进度通道逐条交给 emit。
func (s *stubAPI) ImageBuild(ctx context.Context, spec BuildSpec, emit func(BuildProgress)) error {
	s.built = append(s.built, buildCall{spec: spec})
	if s.buildCh == nil {
		return s.buildErr
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-s.buildCh:
			if !ok {
				return s.buildErr
			}
			if emit != nil {
				emit(b)
			}
		}
	}
}

func (s *stubAPI) ImagePush(ctx context.Context, ref string, auth *ImageAuth, emit func(PullProgress)) error {
	s.pushed = append(s.pushed, pushCall{ref: ref, auth: auth})
	if s.pushCh == nil {
		return s.pushErr
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-s.pushCh:
			if !ok {
				return s.pushErr
			}
			if emit != nil {
				emit(p)
			}
		}
	}
}

func (s *stubAPI) VolumeRemove(_ context.Context, name string, force bool) error {
	s.volumesRemoved = append(s.volumesRemoved, volumeRemoveCall{name: name, force: force})
	return nil
}

func (s *stubAPI) VolumePrune(context.Context) (int64, error) {
	s.volumePruneCalls++
	return s.volumePruneFreed, nil
}

func (s *stubAPI) NetworkRemove(_ context.Context, name string) error {
	s.networksRemoved = append(s.networksRemoved, name)
	return nil
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
func (s *stubAPI) VolumesAndDf(context.Context) ([]VolumeInfo, *DiskUsageSummary, error) {
	// df 汇总由用例显式注入（nil = 「这一帧没有 df 数据」的缺席形态，模拟 df 失败
	// 退化 volume ls 的路径 —— SDK 分支在 CI 没有 daemon，测不到，契约由这里钉住）。
	return s.volumes, s.df, nil
}
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

// ImageRefID 按观测序列给答案（见 imageRefIDs 的说明）；序列用尽后沿用最后一项。
func (s *stubAPI) ImageRefID(_ context.Context, ref string) (string, error) {
	s.imageRefCalls = append(s.imageRefCalls, ref)
	if s.imageRefErr != nil {
		return "", s.imageRefErr
	}
	if len(s.imageRefIDs) == 0 {
		return "", nil
	}
	id := s.imageRefIDs[s.imageRefN]
	if s.imageRefN < len(s.imageRefIDs)-1 {
		s.imageRefN++
	}
	return id, nil
}

// ImageRepoDigests 按观测序列给答案（见 repoDigests 的说明）；序列用尽后沿用最后一项。
func (s *stubAPI) ImageRepoDigests(_ context.Context, ref string) ([]string, error) {
	s.repoDigestCalls = append(s.repoDigestCalls, ref)
	if s.repoDigestErr != nil {
		return nil, s.repoDigestErr
	}
	if len(s.repoDigests) == 0 {
		return nil, nil
	}
	d := s.repoDigests[s.repoDigestN]
	if s.repoDigestN < len(s.repoDigests)-1 {
		s.repoDigestN++
	}
	return d, nil
}
func (s *stubAPI) ComposeVersion(context.Context) (string, string, error) {
	return s.flavor, s.flavorVer, nil
}

// ── 三期流会话的替身（记录型）──────────────────────────────────────────────
//
// 这两个方法的调用发生在会话的 goroutine 里（不是测试 goroutine），故记录用独立的
// 互斥锁保护 —— 否则 -race 会把「测试读、生产写」报成数据竞争。

func (s *stubAPI) ContainerLogsFollow(_ context.Context, name string, tail int, since int64) (io.ReadCloser, error) {
	s.streamMu.Lock()
	s.logFollowCalls = append(s.logFollowCalls, logsFollowCall{name: name, tail: tail, since: since})
	fn, rc, err := s.logStreamFn, s.logStream, s.logStreamErr
	s.streamMu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn != nil {
		return fn(), nil
	}
	if rc != nil {
		return rc, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (s *stubAPI) ContainerExecAttach(_ context.Context, name string, argv []string) (*ExecSession, error) {
	s.streamMu.Lock()
	s.execCalls = append(s.execCalls, execCall{name: name, argv: append([]string(nil), argv...)})
	es, err := s.execSession, s.execErr
	s.streamMu.Unlock()
	if err != nil {
		return nil, err
	}
	if es != nil {
		return es, nil
	}
	return &ExecSession{Reader: strings.NewReader(""), Writer: io.Discard, Close: func() error { return nil }}, nil
}

// ContainerStatsStream 的替身：默认起一个**只随 ctx 结束**的流（不产样本、不关闭 ——
// 供「会话一直活着」的场景，与 exec 的「不结束管道」同一目的）；要产样本的用例直接
// 设 statsStream。
func (s *stubAPI) ContainerStatsStream(ctx context.Context, name string) (<-chan StatsSample, io.Closer, error) {
	s.streamMu.Lock()
	s.statsStreamCalls = append(s.statsStreamCalls, name)
	ch, closer, err := s.statsStream, s.statsCloser, s.statsStreamErr
	s.streamMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if closer == nil {
		closer = &fakeCloser{}
	}
	if ch == nil {
		ch = make(chan StatsSample)
		go func() { <-ctx.Done(); close(ch) }()
	}
	return ch, closer, nil
}

func (s *stubAPI) statsCalls() []string {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return append([]string(nil), s.statsStreamCalls...)
}

// Events 的替身：与 ContainerStatsStream 同型 —— 默认起一个只随 ctx 结束的流
// （不产事件、不关闭），要产事件的用例直接设 eventsStream。
func (s *stubAPI) Events(ctx context.Context) (<-chan EventItem, io.Closer, error) {
	s.streamMu.Lock()
	s.eventsCalls++
	ch, closer, err := s.eventsStream, s.eventsCloser, s.eventsErr
	s.streamMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if closer == nil {
		closer = &fakeCloser{}
	}
	if ch == nil {
		ch = make(chan EventItem)
		go func() { <-ctx.Done(); close(ch) }()
	}
	return ch, closer, nil
}

func (s *stubAPI) eventsCallCount() int {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.eventsCalls
}

// fakeCloser 记录关闭动作（流的 close 由会话 teardown 调用）。
type fakeCloser struct {
	mu     sync.Mutex
	closed bool
}

func (c *fakeCloser) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeCloser) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// logsFollowCall / execCall 是流接口的调用快照。
type logsFollowCall struct {
	name  string
	tail  int
	since int64
}

type execCall struct {
	name string
	argv []string
}

func (s *stubAPI) followCalls() []logsFollowCall {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return append([]logsFollowCall(nil), s.logFollowCalls...)
}

func (s *stubAPI) execArgvs() []execCall {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return append([]execCall(nil), s.execCalls...)
}

// resizeCalls 记录 Terminal resize（由 ExecSession.Resize 包装写入）。
func (s *stubAPI) resizes() []termSize {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return append([]termSize(nil), s.resized...)
}

func (s *stubAPI) recordResize(cols, rows int) {
	s.streamMu.Lock()
	s.resized = append(s.resized, termSize{cols: cols, rows: rows})
	s.streamMu.Unlock()
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

func findProject(t *testing.T, ps []agentproto.DockerProject, name string) agentproto.DockerProject {
	t.Helper()
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("快照里没有项目 %q", name)
	return agentproto.DockerProject{}
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
			// 无标签但**按 digest 拉下来**的镜像（如 `tomcat@sha256:…`）：daemon 的悬空过滤器
			// **包含**它（实测：`/images/json?filters={"dangling":["true"]}` 命中），而 CLI 的
			// `-f dangling=false` 展示层把它画成 `tomcat:<none>` 并算作非悬空 —— 两个过滤器因此
			// 不是互补关系。这里钉住「以 daemon 为准」这条口径。
			{ID: "sha256:eee", RepoTags: nil, SizeBytes: 367 << 20, Created: 1788000000},
		},
		volumes:  []VolumeInfo{{Name: "uni-center-uploads", Driver: "local", SizeBytes: ptrInt64(13 << 20)}, {Name: "other", Driver: "local"}},
		networks: []NetworkInfo{{Name: "bridge", Driver: "bridge", Scope: "local", ContainersCount: 8}},
	}
	st := newTestSnapshotter(api, defaultProtected, testLogger()).Collect(context.Background())
	if err := st.Validate(); err != nil {
		t.Fatalf("采集结果必须能过协议校验: %v", err)
	}
	if !st.DockerOK || len(st.Containers) != 3 || len(st.Projects) != 1 || len(st.Images) != 3 {
		t.Fatalf("采集不完整: ok=%v containers=%d projects=%d images=%d", st.DockerOK, len(st.Containers), len(st.Projects), len(st.Images))
	}
	// 顺序即契约：daemon 的返回顺序无保证，而前端翻页、快照 diff 与「这台机器有没有变化」
	// 的判断都建立在顺序稳定之上 —— 五类清单必须各按名字升序（镜像按第一个 tag，无标签按 id）。
	if want := []string{"mysql", "uni-center-core", "zentao"}; !slices.Equal(containerNames(st.Containers), want) {
		t.Fatalf("容器必须按名字升序，实际 %v", containerNames(st.Containers))
	}
	if want := []string{"sha256:ddd", "sha256:eee", "uni-center-core:latest"}; !slices.Equal(imageSortKeys(st.Images), want) {
		t.Fatalf("镜像必须按第一个 repo tag（无标签按 id）升序，实际 %v", imageSortKeys(st.Images))
	}
	// digest-only 的镜像没有标签，排序键也是 id（它是「无标签」但「不悬空」，两件事分开）
	if got := imageSortKeys(st.Images)[1]; got != "sha256:eee" {
		t.Fatalf("digest-only 镜像的排序键应为 id，实际 %q", got)
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
	// 无标签但按 digest 拉下来的镜像：**算悬空**（daemon 的悬空过滤器包含它，
	// 而 `docker image prune` 删的正是那个集合）。这条钉住「以 daemon 为准」而不是
	// 以 CLI 的 `-f dangling=false` 列表为准 —— 后者把 digest 镜像算作非悬空，
	// 拿它当对照会得出相反结论（2026-09-25 生产实测踩过一次）。
	digestOnly := findImage(t, st.Images, "sha256:eee")
	if !digestOnly.Dangling {
		t.Fatalf("按 digest 拉下来、没有标签的镜像应判悬空（与 daemon 的悬空过滤器一致）: %+v", digestOnly)
	}
	if digestOnly.InUse {
		t.Fatal("它没有被任何容器使用，in_use 应为 false（悬空与在用是两个维度）")
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
	if other := findVolume(t, st.Volumes, "other"); other.SizeMB != nil || other.Protected {
		t.Fatalf("用量未知必须是 nil（页面显示「—」而不是 0），未列出的卷不得被标记: %+v", other)
	}
	if !uploads.InUse || uploads.MountedBy[0] != "uni-center-core" {
		t.Fatalf("被挂载的卷必须标记在用并给出容器名: %+v", uploads)
	}
	// 卷的 protected 由 agent 按 `volume:<名>` 粒度算好（前端不重复实现判断）
	if !uploads.Protected {
		t.Fatalf("volume:uni-center-uploads 在清单里 → 卷条目必须带 protected: %+v", uploads)
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
	if !pr.Protected {
		t.Fatalf("project:uni-center 在清单里 → 项目条目必须带 protected: %+v", pr)
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

// 项目条目的 protected 是**项目粒度**（project:<名>），不是「有任何成员受保护」的合成；
// 服务粒度（project:<项目>/<服务>）仍由成员容器条目的 protected 承载 ——
// 前端拿到的每一行都是 agent 算好的结论，不在页面上重算。
func TestSnapshotProjectProtectedIsProjectGranularity(t *testing.T) {
	api := &stubAPI{
		containers: []ContainerInfo{
			// shop 项目只在**服务粒度**上受保护：项目条目本身不该被标记。
			{ID: "c1", Name: "shop-db-1", Image: "postgres:16", State: "running",
				Labels: map[string]string{composeProjectLabel: "shop", composeServiceLabel: "db"}},
			{ID: "c2", Name: "shop-web-1", Image: "nginx:1", State: "running",
				Labels: map[string]string{composeProjectLabel: "shop", composeServiceLabel: "web"}},
			// blog 项目整体受保护：项目条目与成员容器都要带标记。
			{ID: "c3", Name: "blog-web-1", Image: "nginx:1", State: "running",
				Labels: map[string]string{composeProjectLabel: "blog", composeServiceLabel: "web"}},
		},
		volumes: []VolumeInfo{{Name: "data"}, {Name: "logs"}},
	}
	st := newTestSnapshotter(api, "project:shop/db,project:blog,volume:data", testLogger()).Collect(context.Background())
	if err := st.Validate(); err != nil {
		t.Fatalf("采集结果必须能过协议校验: %v", err)
	}
	if findProject(t, st.Projects, "shop").Protected {
		t.Fatal("shop 只有服务粒度保护 → 项目条目不得标记为受保护（两份口径会打架）")
	}
	if !findProject(t, st.Projects, "blog").Protected {
		t.Fatal("project:blog 必须标记整个项目")
	}
	// 服务粒度落在容器条目上：db 受保护、web 不受（同项目不误伤）。
	if !findContainer(t, st.Containers, "shop-db-1").Protected {
		t.Fatal("project:shop/db 必须标记 shop 项目下的 db 服务容器")
	}
	if findContainer(t, st.Containers, "shop-web-1").Protected {
		t.Fatal("project:shop/db 不得标记同项目下的 web 服务（服务粒度必须成对命中）")
	}
	if !findVolume(t, st.Volumes, "data").Protected || findVolume(t, st.Volumes, "logs").Protected {
		t.Fatalf("卷的 protected 口径不符: %+v", st.Volumes)
	}
}

// 快照是「项目 → 配置文件」索引的常态学习路径：带两个标签的容器必须被学进索引
// （多文件取第一个），缺 config_files 的（旧版 compose）不得学习 —— 学了就是编造路径。
func TestSnapshotLearnsProjectConfigIndex(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{
		{ID: "c1", Name: "core", State: "running", Labels: map[string]string{
			composeProjectLabel:     "uni-center",
			composeServiceLabel:     "uni_core",
			composeConfigFilesLabel: "/data/UniCenter/docker-compose.yml, /data/UniCenter/override.yml",
		}},
		{ID: "c2", Name: "old", State: "running", Labels: map[string]string{
			composeProjectLabel: "legacy",
		}},
	}}
	idx := newProjectIndex(t.TempDir(), testLogger())
	s := newTestSnapshotter(api, "", testLogger())
	s.SetProjectIndex(idx)

	if st := s.Collect(context.Background()); !st.DockerOK {
		t.Fatalf("采集应成功: %+v", st)
	}
	got, ok := idx.Lookup("uni-center")
	if !ok || got != "/data/UniCenter/docker-compose.yml" {
		t.Fatalf("必须学到主配置文件（多文件取第一个、去空白），got %q ok=%v", got, ok)
	}
	if _, ok := idx.Lookup("legacy"); ok {
		t.Fatal("缺 config_files 标签的容器不得学习（不能编造路径）")
	}
}

// scale → 0 之后项目必须仍在：容器没了，网络的 compose 标签还在 —— 归纳面是
// 容器标签 ∪ 网络/卷标签，项目行如实留在这里（0 容器），配置文件路径从项目索引
// 里补回来（没有它工作台的配置区与 Up 都无从下手）。
func TestSnapshotProjectSurvivesScaleToZero(t *testing.T) {
	api := &stubAPI{
		// 容器清空 = scale 0 之后的那一帧。
		networks: []NetworkInfo{{
			Name: "shop_default", Driver: "bridge", Scope: "local",
			Labels: map[string]string{composeProjectLabel: "shop"},
		}},
		volumes: []VolumeInfo{{
			Name: "shop-data", Driver: "local",
			Labels: map[string]string{composeProjectLabel: "shop"},
		}},
	}
	idx := newProjectIndex(t.TempDir(), testLogger())
	// 索引里记着上一次从容器标签学到的主配置文件（scale 0 之前学到的那个事实）。
	idx.Learn("shop", "/data/shop/docker-compose.yml")
	s := newTestSnapshotter(api, "", testLogger())
	s.SetProjectIndex(idx)

	st := s.Collect(context.Background())
	if err := st.Validate(); err != nil {
		t.Fatalf("采集结果必须能过协议校验: %v", err)
	}
	pr := findProject(t, st.Projects, "shop")
	if pr.ContainersCount != 0 || pr.State != "stopped" {
		t.Fatalf("scale 0 的项目必须如实显示 0 容器 / stopped: %+v", pr)
	}
	if len(pr.ConfigFiles) != 1 || pr.ConfigFiles[0] != "/data/shop/docker-compose.yml" {
		t.Fatalf("没有容器时配置文件路径必须回落到项目索引: %+v", pr)
	}
	// 网络/卷给不出服务名：这里不编数（0 是「没有已知服务」，前端另有声明数可读）。
	if pr.Services != 0 {
		t.Fatalf("网络/卷标签不携带服务信息，不得凭空给出服务数: %+v", pr)
	}
}

// Services 取「文件声明的网元 ∪ 有容器的网元」的大小（不是「有容器的网元数」）：
// 文件声明 3 个、容器里出现其中 1 个 + 1 个文件没写的（改过文件、容器还是旧的），
// Services=4 —— 页面据此才说得清「另有 N 个网元没有容器」。
func TestSnapshotDeclaredServicesFromComposeFile(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(main, []byte(strings.Join([]string{
		"services:",
		"  web:",
		"    image: nginx:1",
		"  db:",
		"    image: postgres:16",
		"  cache:",
		"    image: redis:7",
		"volumes:",
		"  data: {}",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("写测试 compose 文件失败: %v", err)
	}
	api := &stubAPI{containers: []ContainerInfo{
		{ID: "c1", Name: "shop-web-1", Image: "nginx:1", State: "running",
			Labels: map[string]string{
				composeProjectLabel: "shop", composeServiceLabel: "web", composeConfigFilesLabel: main,
			}},
		// 文件里没有这个网元（配置改过、容器还是旧的）：观察到的同样算数 ——
		// 页面上它有一行卡片，就不该被「声明数」裁掉。
		{ID: "c2", Name: "shop-extra-1", Image: "busybox:1", State: "running",
			Labels: map[string]string{
				composeProjectLabel: "shop", composeServiceLabel: "extra", composeConfigFilesLabel: main,
			}},
	}}
	s := newTestSnapshotter(api, "", testLogger())

	pr := findProject(t, s.Collect(context.Background()).Projects, "shop")
	if pr.Services != 4 {
		t.Fatalf("Services 必须是声明 ∪ 观察的大小（4），实际 %d: %+v", pr.Services, pr)
	}
	if pr.ContainersCount != 2 {
		t.Fatalf("容器计数照旧只数容器: %+v", pr)
	}
}

// 配置文件读不到（路径不存在 / 权限不足 / YAML 坏了）时退回容器标签归纳的已知服务数
// —— 「读不到」不是「零个服务」，更不是编造声明数。
func TestSnapshotServicesFallBackWhenComposeFileUnreadable(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{
		{ID: "c1", Name: "shop-web-1", Image: "nginx:1", State: "running",
			Labels: map[string]string{
				composeProjectLabel: "shop", composeServiceLabel: "web",
				composeConfigFilesLabel: filepath.Join(t.TempDir(), "no-such.yml"),
			}},
		{ID: "c2", Name: "shop-db-1", Image: "postgres:16", State: "stopped",
			Labels: map[string]string{
				composeProjectLabel: "shop", composeServiceLabel: "db",
				composeConfigFilesLabel: filepath.Join(t.TempDir(), "no-such.yml"),
			}},
	}}
	s := newTestSnapshotter(api, "", testLogger())

	pr := findProject(t, s.Collect(context.Background()).Projects, "shop")
	if pr.Services != 2 {
		t.Fatalf("文件读不到时必须退回容器标签归纳的已知服务数（2），实际 %d: %+v", pr.Services, pr)
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

// ── df 磁盘占用汇总（6a 磁盘治理）─────────────────────────────────────────

// df 有数据的那帧：字节 → MB 的折算（round2，与镜像条目 SizeMB 同一精度），
// 且折好的帧必须能过协议校验（负数/携带时机都在协议层有守卫）。
func TestSnapshotCarriesDiskUsage(t *testing.T) {
	api := &stubAPI{
		containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}},
		volumes:    []VolumeInfo{{Name: "data", Driver: "local", SizeBytes: ptrInt64(13 << 20)}},
		df: &DiskUsageSummary{
			ImagesTotalBytes:    96<<20 + 512<<10, // 96.5 MB：半 MB 余数验证 round2 保留两位
			ImagesDanglingBytes: 367 << 20,        // 367 MB：悬空镜像的「可收回」半边
			VolumesTotalBytes:   13 << 20,         // 与卷条目的 SizeBytes 同源（同一次 df）
			BuildCacheBytes:     1 << 30,          // 1024 MB：构建缓存
		},
	}
	st := newTestSnapshotter(api, "", testLogger()).Collect(context.Background())
	if st.DiskUsage == nil {
		t.Fatal("df 有数据时快照必须带 disk_usage（nil 会被页面读成「数据不可用」）")
	}
	if st.DiskUsage.ImagesTotalMB != 96.5 {
		t.Fatalf("镜像合计应为 96.5 MB，实际 %v", st.DiskUsage.ImagesTotalMB)
	}
	if st.DiskUsage.ImagesDanglingMB != 367 {
		t.Fatalf("悬空合计应为 367 MB，实际 %v", st.DiskUsage.ImagesDanglingMB)
	}
	if st.DiskUsage.VolumesTotalMB != 13 {
		t.Fatalf("卷合计应为 13 MB，实际 %v", st.DiskUsage.VolumesTotalMB)
	}
	if st.DiskUsage.BuildCacheMB != 1024 {
		t.Fatalf("构建缓存应为 1024 MB，实际 %v", st.DiskUsage.BuildCacheMB)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("带 df 的帧必须能过协议校验: %v", err)
	}
}

// df 没有数据的那帧（df 失败、采集退化 volume ls 的路径）：disk_usage 整块缺席
// 而不是零值 —— 「没有数据」与「没有占用」是两个相反的结论，帧本身照常有效。
func TestSnapshotWithoutDiskUsageStaysAbsent(t *testing.T) {
	api := &stubAPI{
		containers: []ContainerInfo{{ID: "c1", Name: "c1", Image: "i", State: "running"}},
		volumes:    []VolumeInfo{{Name: "data", Driver: "local"}},
	}
	st := newTestSnapshotter(api, "", testLogger()).Collect(context.Background())
	if !st.DockerOK || st.DiskUsage != nil {
		t.Fatalf("df 缺席必须保持 nil（不是零值结构），帧照常可用: %+v", st.DiskUsage)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("缺席形态必须能过协议校验: %v", err)
	}
	// 不可达帧同样不得携带 df（协议层拒，采集侧的早退路径自己先守住）。
	api2 := &stubAPI{pingErr: errors.New("无法连接 docker.sock（Docker 服务未运行？）"),
		df: &DiskUsageSummary{ImagesTotalBytes: 1}}
	st2 := newTestSnapshotter(api2, "", testLogger()).Collect(context.Background())
	if st2.DockerOK || st2.DiskUsage != nil {
		t.Fatalf("不可达帧不得携带 df: %+v", st2)
	}
}
