package dockerops

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Logger 是本包依赖的最小日志接口（与 transport.Logger 同形，避免把 zap 拖进 agent）。
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Debug(msg string, kv ...any)
}

// nopLogger 是 Logger 的空实现（NewSnapshotter 允许传 nil）。
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}

// statsConcurrency 是并发取容器读数的上限。
//
// 16 个容器并发 16 个 stats 请求会把 daemon 的连接池打满，也让采集自身变慢 ——
// 而采集是有周期的，慢一拍就会挤到下一拍。取值 4 是「快于串行、又不打扰 daemon」。
const statsConcurrency = 4

// statsTimeout 是单次采集的总时限（含并发等待）。
//
// 30s 周期、10s 上限：一次采集必须在下一个 tick 之前结束，否则周期就不是周期了。
const statsTimeout = 10 * time.Second

// Snapshotter 采集一帧 DockerState。
//
// 它**持有跨帧状态**（上一次的网络累计计数），因为速率只能由两次读数求差得出 ——
// 与 collect 包的网卡速率同一做法（一次读数里没有「速率」这个量）。
type Snapshotter struct {
	api       DockerAPI
	protected *ProtectedList
	// projects 是「项目 → 配置文件」持久化索引：Collect 从容器标签持续学习（见
	// project_index.go）。没有它，down 之后的项目就解析不到配置文件路径。
	projects *projectIndex
	log      Logger
	now      func() time.Time

	mu        sync.Mutex
	prevNet   map[string]netCounters
	prevAt    time.Time
	flavor    string
	flavorVer string
}

type netCounters struct {
	rx, tx uint64
}

// NewSnapshotter 构造采集器。
func NewSnapshotter(api DockerAPI, protected *ProtectedList, log Logger, now func() time.Time) *Snapshotter {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = nopLogger{}
	}
	return &Snapshotter{api: api, protected: protected, log: log, now: now, prevNet: map[string]netCounters{}}
}

// SetCompose 记录 compose 形态（来自 Runtime 的探测缓存，采集不自己探测 ——
// 单一 flavor 纪律要求探测只发生一次）。
func (s *Snapshotter) SetCompose(flavor, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flavor, s.flavorVer = flavor, version
}

// SetProtected 替换保护清单（hello_ack 的配置下发会调用）。
func (s *Snapshotter) SetProtected(p *ProtectedList) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.protected = p
}

// SetProjectIndex 注入「项目 → 配置文件」索引（Runtime 构造后调用，与
// SetProtected/SetCompose 同款）：每一帧采集都会把标签里的路径学进去。
func (s *Snapshotter) SetProjectIndex(idx *projectIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = idx
}

// Collect 采集一帧快照。**永不返回 nil**（调用方会直接把它发出去）。
//
// 失败时的取舍：Ping 失败 → 不可用帧（能力信号）；任何**名单**读取失败 → 整帧判不可用。
// 后者刻意不做「部分成功」：返回「容器有、镜像空」会被页面当成「这台机器确实没有镜像」，
// 而一句「Docker 不可用（名单读取异常）」是诚实的。
func (s *Snapshotter) Collect(ctx context.Context) *agentproto.DockerState {
	ctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()

	now := s.now()
	st := &agentproto.DockerState{T: now.UnixMilli()}

	if err := s.api.Ping(ctx); err != nil {
		st.Error = err.Error() // adapter 保证：这里的错误已是结论句
		return st
	}
	containers, err := s.api.Containers(ctx)
	if err != nil {
		st.Error = listErrorConclusion(err)
		return st
	}
	images, err := s.api.Images(ctx)
	if err != nil {
		st.Error = listErrorConclusion(err)
		return st
	}
	volumes, df, err := s.api.VolumesAndDf(ctx)
	if err != nil {
		st.Error = listErrorConclusion(err)
		return st
	}
	networks, err := s.api.Networks(ctx)
	if err != nil {
		st.Error = listErrorConclusion(err)
		return st
	}

	metrics := s.collectStats(ctx, containers)
	mountedBy := volumeConsumers(containers)
	imageUsers := imageConsumers(containers)

	s.mu.Lock()
	protected := s.protected
	projects := s.projects
	flavor, flavorVer := s.flavor, s.flavorVer
	s.mu.Unlock()

	// 快照是 30s 一次的常态路径：每一帧都从容器标签刷新「项目 → 配置文件」索引，
	// 于是 down 之后（容器没了、标签没了）写路径仍能解析出上次学到的位置。
	learnProjects(projects, containers)

	st.DockerOK = true
	st.Containers = make([]agentproto.DockerContainer, 0, len(containers))
	for _, c := range containers {
		m := metrics[c.ID]
		st.Containers = append(st.Containers, agentproto.DockerContainer{
			ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, StatusText: c.Status,
			Created: c.Created, StartedAt: startedAtOf(c),
			CPUPercent: m.cpuPercent, MemUsageMB: m.memUsageMB, MemLimitMB: m.memLimitMB,
			NetRXBytesSec: m.rxBytesSec, NetTXBytesSec: m.txBytesSec,
			ComposeProject: c.Labels[composeProjectLabel], ComposeService: c.Labels[composeServiceLabel],
			Ports:     toProtoPorts(c.Ports),
			Protected: protected.ContainerProtected(c.Name, c.Labels[composeProjectLabel], c.Labels[composeServiceLabel]),
		})
	}
	st.Images = make([]agentproto.DockerImage, 0, len(images))
	for _, im := range images {
		inUseBy := imageUsers[im.ID]
		st.Images = append(st.Images, agentproto.DockerImage{
			ID: im.ID, RepoTags: im.RepoTags, SizeMB: round2(float64(im.SizeBytes) / (1024 * 1024)),
			Created: im.Created, InUse: len(inUseBy) > 0, Dangling: isDanglingImage(im), InUseBy: inUseBy,
		})
	}
	st.Volumes = make([]agentproto.DockerVolume, 0, len(volumes))
	for _, v := range volumes {
		names := mountedBy[v.Name]
		out := agentproto.DockerVolume{Name: v.Name, Driver: v.Driver, InUse: len(names) > 0, MountedBy: names,
			// 卷是**独立粒度**（volume:<名>）：停止容器后它就是无主的，只有它自己能堵住
			// 「先停容器、再删卷」那条两步绕过路径。
			Protected: protected.Volume(v.Name)}
		if v.SizeBytes != nil {
			size := round2(float64(*v.SizeBytes) / (1024 * 1024))
			out.SizeMB = &size
		}
		st.Volumes = append(st.Volumes, out)
	}
	st.Networks = make([]agentproto.DockerNetwork, 0, len(networks))
	for _, n := range networks {
		st.Networks = append(st.Networks, agentproto.DockerNetwork{
			Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, ContainersCount: n.ContainersCount,
		})
	}
	// df 汇总（6a 磁盘治理）：adapter 在卷采集的**同一趟** system df 里已经算好字节数，
	// 这里只做字节 → MB 的折算（mb/round2 与镜像条目的 SizeMB 同一精度口径 —— 两个
	// 精度会变成「面板一个数、列表一个数」的永久疑问）。df 缺席（失败退化 volume ls
	// 的那帧）保持 nil：调用方拿 nil 说「数据不可用」，0 与「没采到」不混同。
	if df != nil {
		st.DiskUsage = &agentproto.DockerDiskUsage{
			ImagesTotalMB:    mb(float64(df.ImagesTotalBytes)),
			ImagesDanglingMB: mb(float64(df.ImagesDanglingBytes)),
			VolumesTotalMB:   mb(float64(df.VolumesTotalBytes)),
			BuildCacheMB:     mb(float64(df.BuildCacheBytes)),
		}
	}
	// 项目归纳读的是**本域**类型而不是 st.Containers：config_files 只存在于容器标签里，
	// 而 proto 的 DockerContainer 不带 Labels（把它塞进协议会让每帧多背一份标签）。
	// 归纳面 = 容器标签 ∪ 网络/卷标签（见 projectsFrom 的两条修复说明）。
	st.Projects = projectsFrom(containers, networks, volumes, projects)
	// 项目条目的 protected 按 `project:<名>` **项目粒度**填（服务粒度已由成员容器条目的
	// Protected 承载，见 ContainerProtected）；结论只在 agent 算一次，前端不重算。
	for i := range st.Projects {
		st.Projects[i].Protected = protected.Project(st.Projects[i].Name)
	}
	if flavor != "" {
		st.Compose = &agentproto.DockerComposeInfo{Flavor: flavor, Version: flavorVer}
	}
	sortState(st)
	return st
}

// listErrorConclusion 把名单读取失败折成结论句（页面显示它）。
func listErrorConclusion(err error) string {
	if err == nil {
		return ""
	}
	return "无法读取资源清单（Docker 响应异常）"
}

// collectStats 并发取各容器的即时读数并算出速率。
//
// 失败的单个容器**只置零并记 Warn**：一次 stats 失败（容器刚被删、daemon 忙）
// 不该让整台主机的快照消失。
func (s *Snapshotter) collectStats(ctx context.Context, cs []ContainerInfo) map[string]containerMetrics {
	out := make(map[string]containerMetrics, len(cs))
	now := s.now()

	s.mu.Lock()
	prev, prevAt := s.prevNet, s.prevAt
	s.mu.Unlock()
	dt := now.Sub(prevAt).Seconds()

	type result struct {
		id string
		m  containerMetrics
		// rx/tx 是本次读数的**原始累计值**，它要成为下一帧求差的基线。基线必须与速率
		// 出自同一次 stats 返回：收尾时再问一遍 daemon 会拿到另一个时刻的计数，
		// 两次读数之间的差就再也对不上 dt。
		rx, tx uint64
	}
	ch := make(chan result, len(cs))
	sem := make(chan struct{}, statsConcurrency)
	var wg sync.WaitGroup
	for _, c := range cs {
		if c.State != "running" {
			// 非运行容器没有读数（Docker 对已停止容器不返回 stats）。
			// 页面按 state 决定显示「—」而不是 0% —— 协议里 cpu_percent 是 float64，
			// 0 与「读不到」在这一版不可区分，故展示层用状态补足语义。
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := s.api.ContainerStats(ctx, id)
			if err != nil {
				s.log.Warn("docker container stats failed", "container", id, "err", err.Error())
				// 取不到读数就**不回结果**：既不写基线（把 0 当成上一次读数，下一帧会
				// 算出一个凭空的巨大速率 —— 计数器是自容器启动以来的累计值），
				// out 里也没有这一项（零值即「读数置零」，与页面按 state 显示「—」一致）。
				return
			}
			m := containerMetrics{cpuPercent: st.CPUPercent, memUsageMB: st.MemUsageMB, memLimitMB: st.MemLimitMB}
			if p, ok := prev[id]; ok && dt > 0 {
				// 累计计数器求差；回绕（容器重启后计数归零）时给 0 而不是负数。
				if st.NetRXBytes >= p.rx {
					m.rxBytesSec = round2(float64(st.NetRXBytes-p.rx) / dt)
				}
				if st.NetTXBytes >= p.tx {
					m.txBytesSec = round2(float64(st.NetTXBytes-p.tx) / dt)
				}
			}
			ch <- result{id: id, m: m, rx: st.NetRXBytes, tx: st.NetTXBytes}
		}(c.ID)
	}
	wg.Wait()
	close(ch)

	next := make(map[string]netCounters, len(cs))
	for r := range ch {
		out[r.id] = r.m
		next[r.id] = netCounters{rx: r.rx, tx: r.tx}
	}
	// 本次读数整体替换基线：消失的容器随之出队（否则 s.prevNet 会一直长），
	// 而没取到读数的容器下一帧从头开始（速率 0 一帧，比给个假值好）。
	s.mu.Lock()
	s.prevNet = next
	s.prevAt = now
	s.mu.Unlock()
	return out
}

type containerMetrics struct {
	cpuPercent, memUsageMB, memLimitMB float64
	rxBytesSec, txBytesSec             float64
}

// projectsFrom 归纳 compose 项目：容器标签 ∪ 网络/卷标签，服务数取「声明 ∪ 有容器」。
//
// 不调 compose CLI 发现（§6.1：项目发现读 SDK 标签，config_files 也来自标签）——
// 少一次进程调用、少一种 flavor 差异。
//
// ── 修复一：归纳面是容器标签 ∪ 网络/卷标签（scale 0 之后项目不再失联）────────
// 「项目在不在」不能只由容器回答：scale → 0 之后容器全没了，项目却没消失 —— 它的
// 默认网络与数据卷还留在本机，而 compose 给它们都打了 `com.docker.compose.project`
// 标签。只看容器会让这个项目从快照里整条消失：页面没有行、工作台进不去、也就无从
// Up 回来。故：
//   - 容器：全量事实（服务名、配置文件路径、容器计数、运行态）；
//   - 网络/卷：只补「项目存在」这一条事实（它们给不出服务与容器数，也不编造）——
//     一条痕迹就够让项目行留下，如实显示 0 容器、0 运行。
//
// ── 修复二：Services = 声明网元 ∪ 有容器的网元（缺服务数不再恒 0）────────────
// 容器标签只能给出「有容器的服务」，拿它与页面列出的服务行相减，差恒等于 0（同一
// 份数据相减），「另有 N 个网元没有容器」因此永远不可达。项目声明了几个服务只有
// compose 配置文件写着，故 Services 取「文件声明的集合 ∪ 容器观察到的集合」的大小
// （多文件取并集 —— 与 compose 的 merge 语义同向）：与页面上「有容器的网元行」
// 相减，差恰好是「有声明、没容器」的那部分（服务停掉或缩到 0、配置改了还没 up）。
// 一个文件都读不到时退回容器标签归纳的已知网元数（宁可小、不编造：读不到 ≠ 零个网元）。
//
// 顺带把**配置文件路径**也在没有容器时从项目索引补上（见下）：没有它，工作台的
// 配置区与 Up 都无从下手，而索引里记的正是上次从容器标签学到的同一个事实。
func projectsFrom(cs []ContainerInfo, ns []NetworkInfo, vs []VolumeInfo, idx *projectIndex) []agentproto.DockerProject {
	type acc struct {
		services map[string]bool
		files    map[string]bool
		total    int
		running  int
	}
	byName := map[string]*acc{}
	touch := func(project string) *acc {
		a := byName[project]
		if a == nil {
			a = &acc{services: map[string]bool{}, files: map[string]bool{}}
			byName[project] = a
		}
		return a
	}
	for _, c := range cs {
		project := c.Labels[composeProjectLabel]
		if project == "" {
			continue
		}
		a := touch(project)
		if svc := c.Labels[composeServiceLabel]; svc != "" {
			a.services[svc] = true
		}
		// config_files 可能是多个文件（`-f a.yml -f b.yml`），标签里是逗号分隔的一串；
		// 去重是因为同一项目的每个容器都写着同一份清单。
		for _, f := range strings.Split(c.Labels[composeConfigFilesLabel], ",") {
			if f = strings.TrimSpace(f); f != "" {
				a.files[f] = true
			}
		}
		a.total++
		if c.State == "running" {
			a.running++
		}
	}
	// 网络/卷只说明「项目还有痕迹」：不产服务名、不产配置文件、不计容器数。
	for _, n := range ns {
		if project := n.Labels[composeProjectLabel]; project != "" {
			touch(project)
		}
	}
	for _, v := range vs {
		if project := v.Labels[composeProjectLabel]; project != "" {
			touch(project)
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	// 项目按名字升序输出：map 的遍历顺序是随机的，直接输出会让同一份数据每帧的顺序都不同
	//（前端 diff 会把它读成「变了」）。
	sort.Strings(names)
	out := make([]agentproto.DockerProject, 0, len(names))
	for _, name := range names {
		a := byName[name]
		files := sortedKeys(a.files)
		if len(files) == 0 {
			// 没有容器 = 没有标签可看：回落到项目索引里记着的上次已知主配置文件。
			// 索引也没有该项时保持空列表（页面显示「未知」而不是编一条路径）。
			if p, ok := idx.Lookup(name); ok {
				files = []string{p}
			}
		}
		services := len(a.services)
		// Services = 声明集合 ∪ 容器观察集合 的大小。为什么是并集：页面拿它与「有容器的
		// 网元行」相减，差恰好是「有声明、没容器」的那部分（服务停掉或缩到 0、配置改了
		// 还没 up）。只取声明数会在「容器比文件新」（改过文件、还是旧容器）时把在跑的
		// 网元数说小；只取观察数则恒等于行数、差永远为 0。
		if declared, ok := declaredServiceNames(files); ok {
			for name := range declared {
				a.services[name] = true
			}
			services = len(a.services)
		}
		out = append(out, agentproto.DockerProject{
			Name: name, ConfigFiles: files, State: projectState(a.running, a.total),
			Services: services, ContainersCount: a.total,
		})
	}
	return out
}

// declaredServiceNames 取一个项目在配置文件里**声明**的网元名集合（多文件取并集，
// 与 compose 的 merge 语义同向：override 里的新服务同样是这个项目的服务）。
//
// ok=false = 一个文件都没读到（没有路径 / 全部不存在 / 全部解不开）—— 调用方据此
// 只报容器标签归纳的已知网元，而不是把「读不到」当成「零个网元」（那是两个相反的
// 结论，与 SizeMB=nil 不折成 0 同一条纪律）。读到了但一个网元也没有（文件里没有
// services 键）时 ok=true、集合为空 —— 那是「文件说了没有」，同样如实。
//
// 三条口径：
//   - 路径只来自本机事实（容器标签 / 项目索引），协议上从不出现路径 —— 与配置编辑
//     路径同一把尺（§8），上限也复用同一个 1MB 闸（readComposeFileAt）；
//   - 单文件读失败/解析失败**跳过**：这不是项目坏了（多文件里 override 先删掉、
//     旧版 YAML 有怪语法都可能），快照不该因此少一行项目；
//   - 只在**有文件可读**时给结论：一个都读不到时返回 ok=false，不猜。
//
// 刻意不记日志：它在 30s 一拍的快照路径上，文件长期不可读会变成每拍一条的告警噪音，
// 而「读不到配置」在同一快照的配置区/Up 路径上有可操作的结论句（那里才知道用户要干什么）。
func declaredServiceNames(files []string) (map[string]bool, bool) {
	if len(files) == 0 {
		return nil, false
	}
	names := map[string]bool{}
	readable := false
	for _, f := range files {
		b, err := readComposeFileAt(f)
		if err != nil {
			continue
		}
		var doc struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			continue
		}
		readable = true
		for name := range doc.Services {
			names[name] = true
		}
	}
	if !readable {
		return nil, false
	}
	return names, true
}

// projectState 由成员容器的运行态归纳项目状态。
//
// 三态而非布尔：compose 项目里「一半起来了」是运维最常见的中间态（依赖没就绪、某个服务
// 反复重启），把它显示成 running 或 stopped 都会让人做出错误判断。
func projectState(running, total int) string {
	switch {
	case total > 0 && running == total:
		return "running"
	case running == 0:
		return "stopped"
	default:
		return "partial"
	}
}

// startedAtOf 报告容器的启动时刻 —— **恒为 0**，由容器详情的 inspect 提供精确值。
//
// 容器**列表项不含启动时刻**：docker ps 只给一句本地化的 Status 文案（"Up 16 hours"），
// 解析它等于依赖 daemon 的措辞与语言（v1/v2 compose、不同语言环境都会变形），而
// 「Up 16 hours」本身也只有小时级精度。宁可让列表页显示「—」，也不给一个看起来精确的假值。
func startedAtOf(c ContainerInfo) int64 { return 0 }

// toProtoPorts 逐项映射端口映射（字段同名同义，不做任何加工）。
func toProtoPorts(in []PortInfo) []agentproto.DockerPort {
	if len(in) == 0 {
		return nil
	}
	out := make([]agentproto.DockerPort, 0, len(in))
	for _, p := range in {
		out = append(out, agentproto.DockerPort{
			IP: p.IP, PrivatePort: p.Private, PublicPort: p.Public, Type: p.Type,
		})
	}
	return out
}

// toProtoMounts 逐项映射挂载点（字段同名同义，不做任何加工）。
func toProtoMounts(in []MountInfo) []agentproto.DockerMount {
	if len(in) == 0 {
		return nil
	}
	out := make([]agentproto.DockerMount, 0, len(in))
	for _, m := range in {
		out = append(out, agentproto.DockerMount{
			Type: m.Type, Source: m.Source, Destination: m.Destination, Mode: m.Mode, RW: m.RW,
		})
	}
	return out
}

// imageConsumers 按 ImageID 聚合「哪些容器在用这个镜像」。
//
// 键用 ImageID 而不是 RepoTag：同一个镜像可以有多个标签，而容器记录的是 ID；
// 用标签当键会漏掉「容器用的是这个镜像但名字对不上」的情况。
func imageConsumers(cs []ContainerInfo) map[string][]string {
	acc := map[string]map[string]bool{}
	for _, c := range cs {
		if c.ImageID == "" || c.Name == "" {
			continue
		}
		if acc[c.ImageID] == nil {
			acc[c.ImageID] = map[string]bool{}
		}
		acc[c.ImageID][c.Name] = true
	}
	return sortedSets(acc)
}

// volumeConsumers 按挂载来源聚合「哪些容器挂了这个卷」。
//
// 只认 Type == "volume"：bind 挂载的 Source 是**宿主路径**而不是具名资源，
// 混进来会让「卷的列表」里出现一堆目录名，而删卷的判断（in_use）也会张冠李戴。
func volumeConsumers(cs []ContainerInfo) map[string][]string {
	acc := map[string]map[string]bool{}
	for _, c := range cs {
		if c.Name == "" {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Source == "" {
				continue
			}
			if acc[m.Source] == nil {
				acc[m.Source] = map[string]bool{}
			}
			acc[m.Source][c.Name] = true
		}
	}
	return sortedSets(acc)
}

// sortedSets 把「名字集合」的 map 收成排序去重的切片（顺序稳定 → 前端 diff 稳定）。
func sortedSets(acc map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(acc))
	for key, set := range acc {
		out[key] = sortedKeys(set)
	}
	return out
}

// sortedKeys 把集合 map 收成升序切片。
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortState 把五类清单各按名字升序排好。
//
// 为什么排序发生在 agent 而不是页面：daemon 的返回顺序**无保证**（同一份数据两次 list
// 的顺序可以不同），而前端翻页、快照 diff、以及「这台机器有没有变化」的判断都建立在
// 顺序稳定之上（Plan 1b 明确：顺序由 agent 保证 —— 页面不重排）。
func sortState(st *agentproto.DockerState) {
	sort.SliceStable(st.Containers, func(i, j int) bool { return st.Containers[i].Name < st.Containers[j].Name })
	sort.SliceStable(st.Images, func(i, j int) bool { return imageSortKey(st.Images[i]) < imageSortKey(st.Images[j]) })
	sort.SliceStable(st.Volumes, func(i, j int) bool { return st.Volumes[i].Name < st.Volumes[j].Name })
	sort.SliceStable(st.Networks, func(i, j int) bool { return st.Networks[i].Name < st.Networks[j].Name })
	sort.SliceStable(st.Projects, func(i, j int) bool { return st.Projects[i].Name < st.Projects[j].Name })
}

// imageSortKey 是镜像的排序键：有标签时用第一个 repo tag（运维认得的是标签），
// 悬空镜像没有标签则退回 id —— 否则一堆 <none>:<none> 全撞在同一个键上，顺序不稳定。
func imageSortKey(im agentproto.DockerImage) string {
	if len(im.RepoTags) > 0 {
		return im.RepoTags[0]
	}
	return im.ID
}

// isDanglingImage 判定镜像是否悬空：**没有任何标签**（`RepoTags` 为空）。
//
// 为什么就是「无标签」而不是「既无标签也无 digest」—— 2026-09-25 在生产(.105)实测过三种口径，
// 结论以 daemon 为准：
//   - daemon 的 `GET /images/json?filters={"dangling":["true"]}` 返回 14 条，**包含**那个按
//     digest 拉下来的 `tomcat@sha256:…`（它的 RepoTags 是空的）→ daemon 的悬空判据就是 RepoTags 空；
//   - `docker images -f dangling=true -q` 同为 14（CLI 与 daemon 一致）；
//   - 但 `docker images -f dangling=false -q` 是 32，而总数是 45 —— **32+14 ≠ 45**：CLI 的展示层
//     把 digest 镜像画成 `tomcat:<none>` 并算进「非悬空」，两个过滤器因此不是互补关系。
//     拿 `dangling=false` 当对照就会得出「我们的判定多了一个」的错误结论。
//   - `docker image prune` 删的正是 daemon 的悬空集合，故「无标签」这一口径**与清理动作一致** ——
//     镜像页的「可回收」数字必须与它对齐，多报一个 367MB 就是多报一份空间。
func isDanglingImage(im ImageInfo) bool {
	return len(im.RepoTags) == 0
}
