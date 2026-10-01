package response

// Docker 域的 HTTP 响应 DTO。
//
// 三条与既有模块一致的约定：
//   - 时间一律 unix 秒 + omitempty；
//   - 展示层要的中文结论（如 docker_ok 的原因句）由服务端算好给出，前端不重复判断；
//   - **不用 map**：apigen 生成不出可用的 TS 类型。
//
// 字段名是 camelCase（与协议的 snake_case 是两套）：中间的映射在 service 层做，
// 契约的形状由本文件 + apigen 生成物共同锁定。

// DockerHostItem 是一台可管主机（主机切换器的数据源）。
type DockerHostItem struct {
	ID        uint64 `json:"id,string"`
	Hostname  string `json:"hostname"`
	PrimaryIP string `json:"primaryIp,omitempty"`
	// Online 由 last_seen_at 与 sys.agent.offlineThreshold 折算（与设备列表同口径）。
	Online bool `json:"online"`
	// DockerOK 是 agent 自报的能力信号；false 时前端不渲染任何操作按钮。
	DockerOK bool `json:"dockerOk"`
	// Error 是 dockerOK=false 的结论句。
	Error          string `json:"error,omitempty"`
	ComposeFlavor  string `json:"composeFlavor,omitempty"`
	ComposeVersion string `json:"composeVersion,omitempty"`
	Containers     int    `json:"containers"`
	Images         int    `json:"images"`
	// LastSync 是 core 收到快照的时刻（unix 秒）；0 = 从未上报。
	LastSync int64 `json:"lastSync,omitempty"`
	// Stale 是服务端算好的结论（>max(3×周期, 90s) 未更新）。
	Stale bool `json:"stale"`
	// Disk 是该主机的磁盘占用账目（system df 汇总 + 可回收计数）。**nil = 无 df 数据**
	//（那帧 df 失败、或旧版 agent 不发 disk_usage）—— 页面显示「数据不可用」而不是 0
	//（与卷条目 SizeMB=nil 同一纪律：不知道 ≠ 零）。只在总览响应填充（磁盘面板的
	// 专供数据面）；主机清单页不消费它，omitempty 让字段在那边缺席。
	Disk *DockerHostDiskItem `json:"disk,omitempty"`
}

// DockerHostListResp 是主机清单响应。
type DockerHostListResp struct {
	List []DockerHostItem `json:"list"`
	// SnapshotInterval 是当前快照周期（秒）：前端据此算「同步于 N 秒前」的文案，
	// 与后端 stale 判定同源（各自硬编码会让两边对「多久算陈旧」给出不同答案）。
	SnapshotInterval int `json:"snapshotInterval"`
}

// ── 控制塔总览（跨主机聚合，GET /docker/overview）─────────────────────

// DockerOverviewResp 是控制塔总览响应：航队 KPI + 主机清单 + 异常容器清单。
//
// 数据边界：聚合快照里**已有**的静态事实（计数、状态、结论、6a 起的 system df 磁盘
// 汇总）。三块数据的关系：
//   - fleet：全量聚合（**stale 主机的数据照常计入** —— 陈旧是「最后已知事实
//     已过期」，不是「事实不存在」，页面用 stale 标记告诉读者这句话）；
//   - hosts：与 GET /docker/hosts 的主机条目**同一类型**（DockerHostItem 复用），
//     控制塔每一行与主机清单页说同一句话；disk 字段是总览专供（见 DockerHostItem）；
//   - anomalies：抽查清单（上限 50 条），要闻按主机排序 —— 与 fleet 的全量计数
//     口径互补。
type DockerOverviewResp struct {
	Fleet DockerOverviewFleet `json:"fleet"`
	Hosts []DockerHostItem    `json:"hosts"`
	// Anomalies 是非 running 容器清单；Total 是截断前的全量数。
	Anomalies DockerOverviewAnomalies `json:"anomalies"`
}

// DockerOverviewFleet 是航队 KPI（fleet＝舰队：把全部可管主机当成一支舰队看总账）。
type DockerOverviewFleet struct {
	Hosts      DockerFleetHosts      `json:"hosts"`
	Containers DockerFleetContainers `json:"containers"`
	Images     DockerFleetImages     `json:"images"`
	Volumes    DockerFleetVolumes    `json:"volumes"`
	Networks   DockerFleetNetworks   `json:"networks"`
	Projects   DockerFleetProjects   `json:"projects"`
	// Disk 是磁盘占用的航队汇总（6a 磁盘治理）。值形态而非指针：fleet 恒在
	//（零 df 主机时报 Hosts=0 与零合计 —— 「全部主机都没有 df 数据」本身就是一个
	// 如实的舰队结论，页面上磁贴显示「数据不可用」）。
	Disk DockerFleetDisk `json:"disk"`
}

// DockerFleetDisk 是磁盘占用的航队汇总（跨主机求和）。
//
// 求和只统计**有 df 数据**的主机；stale 主机的 df 照常计入（与其它 fleet KPI 同一
// 句话：陈旧是「最后已知事实过期」，不是「事实不存在」）。MB 是每台主机各自 round2
// 后的值再求和 —— 跨主机的累计误差被前端进位到 GB 后不可见，不值得为它把字节
// 口径再贯穿一层。
type DockerFleetDisk struct {
	// Hosts 是**有 df 数据**的主机数（快照 disk_usage 非 nil 的主机数）。与
	// fleet.hosts.total 并排读就是「4 台里 3 台报了磁盘账」—— 没报的那台不是
	// 0 占用，是不知道；计数如实标注而不是把缺报主机折算成零。
	Hosts int `json:"hosts"`
	// ImagesTotalMB / VolumesTotalMB / BuildCacheMB 是三类占用合计（MB）。
	ImagesTotalMB  float64 `json:"imagesTotalMb"`
	VolumesTotalMB float64 `json:"volumesTotalMb"`
	BuildCacheMB   float64 `json:"buildCacheMb"`
	// ImagesDanglingMB 是悬空镜像占用合计（image:prune 默认目标的体积：
	//「能安全收回多少」的镜像半边）。卷的可回收数不在此列 —— volume:prune 只回收
	// 匿名未用卷，合计口径在协议上拿不到（df 不区分匿名），入口在卷页的清理流。
	ImagesDanglingMB float64 `json:"imagesDanglingMb"`
}

// DockerHostDiskItem 是一台主机的磁盘占用账目（总览磁盘面板一行的数据面）。
type DockerHostDiskItem struct {
	// ImagesMB / VolumesMB / BuildCacheMB 是三类占用（MB；system df 的同帧口径）。
	ImagesMB     float64 `json:"imagesMb"`
	VolumesMB    float64 `json:"volumesMb"`
	BuildCacheMB float64 `json:"buildCacheMb"`
	// ImagesDanglingMB 是悬空镜像占用（image:prune 默认目标的体积）。
	ImagesDanglingMB float64 `json:"imagesDanglingMb"`
	// DanglingImages 是悬空镜像计数（与镜像页「可回收」计数同一判据：agent 的
	// Dangling 结论，前端不重算）。
	DanglingImages int `json:"danglingImages"`
	// UnusedVolumes 是未被任何容器挂载的卷计数。注意它**不是** volume:prune 的
	// 回收数：prune 只收匿名未用卷，命名卷走卷页的独立删除流 —— 面板只给计数
	// 与入口，清理动作在列表页的确认档流里发生。
	UnusedVolumes int `json:"unusedVolumes"`
}

// DockerFleetHosts 是主机维度的航队计数。
type DockerFleetHosts struct {
	// Total 是本响应 hosts 列表的行数（读失败的主机也算一台 —— 行还在，err 如实）。
	Total int `json:"total"`
	// DockerOK 是 dockerOk=true 的主机数（agent 自报的能力信号）。
	DockerOK int `json:"dockerOk"`
}

// DockerFleetContainers 是容器维度的航队计数。
type DockerFleetContainers struct {
	Total int `json:"total"`
	// Running 是 state=running 的容器数。
	Running int `json:"running"`
	// Stopped = Total - Running：泛指一切非 running（exited/created/paused/dead…），
	// 与异常清单的收录口径同一句话（两边各写一套就又出现一次口径分裂）。
	Stopped int `json:"stopped"`
	// Protected 是受保护容器的数量（不论运行态；结论由 agent 按保护清单算好）。
	Protected int `json:"protected"`
}

// DockerFleetImages 是镜像维度的航队计数。
type DockerFleetImages struct {
	Total  int `json:"total"`
	Unused int `json:"unused"`
}

// DockerFleetVolumes 是数据卷维度的航队计数。
type DockerFleetVolumes struct {
	Total  int `json:"total"`
	Unused int `json:"unused"`
}

// DockerFleetNetworks 是网络维度的航队计数（只有总量：网络没有「在不在用」的结论）。
type DockerFleetNetworks struct {
	Total int `json:"total"`
}

// DockerFleetProjects 是编排项目维度的航队计数。
type DockerFleetProjects struct {
	Total int `json:"total"`
	// Running 是 state=running 的项目数（项目态 ∈ running|partial|stopped，
	// 由 agent 按成员容器归纳 —— 非 running 项目不上异常清单，那是容器粒度的清单）。
	Running int `json:"running"`
}

// DockerOverviewAnomalies 是异常容器清单（带截断口径）。
type DockerOverviewAnomalies struct {
	// Total 是**截断前**的非 running 容器全量数；Items 至多 50 条。
	// 两者并存：控制塔要一眼知道「舰队里有几个不正常的容器」，详情只给前 50 ——
	// 超限时 Total 与 Items 长度不等，即「截断了」的显式口径（不靠前端猜）。
	Total int `json:"total"`
	// Items 按主机 id 升序（同机按容器名）排序。
	Items []DockerOverviewAnomalyItem `json:"items"`
}

// DockerOverviewAnomalyItem 是异常清单的一行（一台主机上的一个非 running 容器）。
//
// 刻意**不内联** DockerContainerItem：控制塔只关心「哪台机、哪个容器、什么态」，
// 完整条目里的 CPU/内存/端口对这张清单都是噪音（50 行全量字段会拖肥响应，
// 而页面并不渲染它们）。
type DockerOverviewAnomalyItem struct {
	// ID 是容器的完整 ID（同 DockerContainerItem.ID）：异常行点进去要能深链到
	// 容器详情页（/docker/container-detail/:id），没有它前端只能干列名字。
	ID       string `json:"id"`
	HostID   uint64 `json:"hostId,string"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`
	// StatusText 是 docker ps 的原生状态句（"Exited (137) 3 days ago"），展示层直接显示。
	StatusText string `json:"statusText,omitempty"`
	// Protected 是 agent 按保护清单算好的结论：控制塔**不**据此拦截操作（拦在
	// 指令受理面），只用来让运维一眼看出「这是个动不得的容器」。
	Protected bool `json:"protected"`
}

// ── 跨主机统一工作负载表（GET /docker/containers）───────────────────────

// DockerWorkloadListResp 是统一工作负载表响应：全部可管主机的容器并成一张表。
type DockerWorkloadListResp struct {
	Items []DockerWorkloadItem `json:"items"`
	// Total 是**截断前**的全量数（过滤后）；Items 至多 500 条。两者并存的口径
	// 与总览 anomalies 同一句话：Total 与 Items 长度不等即「截断了」的显式信号
	//（不靠前端猜「还有多少」）。
	Total int `json:"total"`
}

// DockerWorkloadItem 是统一表的一行（一台主机上的一个容器）。
//
// **内嵌** DockerContainerItem 而不是平行造一套字段：单主机快照页与统一表
// 是同一个条目形态（详情深链、写动作载荷都按容器条目走），归属两列是统一表
// 唯一的新增维度 —— 平行造字段会在 DockerContainerItem 下次加列时静默漏掉
// 这一侧（页面上少一列，没人报错）。
type DockerWorkloadItem struct {
	DockerContainerItem
	// HostID 是容器所在主机的设备 ID：统一表的核心新增（容器在哪台机的归属）。
	HostID uint64 `json:"hostId,string"`
	// Hostname 是容器所在主机的主机名（与主机清单/总览的 hostname 同源）。
	Hostname string `json:"hostname"`
}

// DockerStateResp 是一台主机的完整快照。
type DockerStateResp struct {
	LastSync int64 `json:"lastSync,omitempty"`
	Stale    bool  `json:"stale"`
	// AgeSeconds 是距上次上报的秒数（0 = 刚刚）。
	AgeSeconds int64 `json:"ageSeconds"`
	// NeverReported 表示该主机从未上报过快照（与「陈旧」是两句不同的话）。
	NeverReported bool                   `json:"neverReported"`
	DockerOK      bool                   `json:"dockerOk"`
	Error         string                 `json:"error,omitempty"`
	Compose       *DockerComposeInfoResp `json:"compose,omitempty"`
	Containers    []DockerContainerItem  `json:"containers"`
	Images        []DockerImageItem      `json:"images"`
	Volumes       []DockerVolumeItem     `json:"volumes"`
	Networks      []DockerNetworkItem    `json:"networks"`
	Projects      []DockerProjectItem    `json:"projects"`
}

// DockerComposeInfoResp 是 compose 形态。
type DockerComposeInfoResp struct {
	Flavor  string `json:"flavor"`
	Version string `json:"version,omitempty"`
}

// DockerContainerItem 是容器列表/详情头的一行。
type DockerContainerItem struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Image          string           `json:"image"`
	State          string           `json:"state"`
	StatusText     string           `json:"statusText,omitempty"`
	Created        int64            `json:"created,omitempty"`
	StartedAt      int64            `json:"startedAt,omitempty"`
	CPUPercent     float64          `json:"cpuPercent"`
	MemUsageMB     float64          `json:"memUsageMb"`
	MemLimitMB     float64          `json:"memLimitMb"`
	NetRXBytesSec  float64          `json:"netRxBytesSec"`
	NetTXBytesSec  float64          `json:"netTxBytesSec"`
	ComposeProject string           `json:"composeProject,omitempty"`
	ComposeService string           `json:"composeService,omitempty"`
	Ports          []DockerPortItem `json:"ports,omitempty"`
	Protected      bool             `json:"protected"`
}

// DockerPortItem 是一条端口映射。
type DockerPortItem struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort,omitempty"`
	Type        string `json:"type,omitempty"`
}

// DockerImageItem 是镜像列表的一行。
type DockerImageItem struct {
	ID       string   `json:"id"`
	RepoTags []string `json:"repoTags,omitempty"`
	SizeMB   float64  `json:"sizeMb"`
	Created  int64    `json:"created,omitempty"`
	InUse    bool     `json:"inUse"`
	Dangling bool     `json:"dangling"`
	InUseBy  []string `json:"inUseBy,omitempty"`
}

// DockerVolumeItem 是数据卷列表的一行。
type DockerVolumeItem struct {
	Name      string   `json:"name"`
	Driver    string   `json:"driver,omitempty"`
	SizeMB    *float64 `json:"sizeMb,omitempty"`
	InUse     bool     `json:"inUse"`
	MountedBy []string `json:"mountedBy,omitempty"`
	// Protected 来自 agent 的结论（volume:<名>），前端不重复实现判断。
	// 不带 omitempty：与 DockerContainerItem.Protected 同一形态（0 是真实值）。
	Protected bool `json:"protected"`
}

// DockerNetworkItem 是网络列表的一行。
type DockerNetworkItem struct {
	Name            string `json:"name"`
	Driver          string `json:"driver,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Internal        bool   `json:"internal"`
	ContainersCount int    `json:"containersCount"`
}

// DockerProjectItem 是一个 compose 项目。
type DockerProjectItem struct {
	Name            string   `json:"name"`
	ConfigFiles     []string `json:"configFiles,omitempty"`
	State           string   `json:"state,omitempty"`
	Services        int      `json:"services"`
	ContainersCount int      `json:"containersCount"`
	// Protected 来自 agent 的结论（project:<名>）；服务粒度由容器条目承载。
	Protected bool `json:"protected"`
}

// DockerCmdResp 是受理响应的载荷（202）。
type DockerCmdResp struct {
	// Ref 是指令号：前端用它轮询结果。
	Ref string `json:"ref"`
}

// DockerCmdResultResp 是轮询结果。
type DockerCmdResultResp struct {
	// Status ∈ pending | succeeded | failed | timeout。
	Status string `json:"status"`
	// Error 是终态结论句（页面直接显示）。
	Error string `json:"error,omitempty"`
	// Detail 是排障细节（**页面不渲染**；留给「复制诊断信息」这类入口）。
	Detail        string `json:"detail,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	AlreadyExists bool   `json:"alreadyExists,omitempty"`
	// StreamTicket 是一次性流票据（三期）：指令建立了流会话时，**每次**轮询响应
	// 都新签一张（TTL 30s、单次使用，绑定 userId+sessionId+deviceId）。前端接流
	// 时用**最新**的那张；票据不进日志、不上报，只在该响应里出现。
	StreamTicket string `json:"streamTicket,omitempty"`
	// Payload 是结果数据面（按 action 形态不同；前端按 action 解析）。
	Payload any `json:"payload,omitempty"`
}

// ── 私有仓库凭据（4c）───────────────────────────────────────────────

// DockerRegistryPasswordMask 是凭据密码在一切读路径上的唯一形态。
// 刻意取 4 个星号的短常量：它不是「按长度的省略」，而是「存在但不可读」的
// 固定符号 —— 不随密码真实长度变化，读响应不可能反推出长度。
const DockerRegistryPasswordMask = "****"

// DockerRegistryItem 是仓库凭据列表/详情的一行。
//
// **密码任何读路径都不回明文**：Password 恒为 DockerRegistryPasswordMask，
// 明文只在两条瞬态路径上存在 —— 写请求（读完后立刻加密）与 image:pull 的
// 受理注入（解出后随指令下发 agent）。本 DTO 不存在明文字段，是掩码纪律的
// 结构级保证（谁也别想在响应结构体上加出一个明文密码段）。
type DockerRegistryItem struct {
	// Registry 是仓库地址（唯一键；不含协议头与镜像路径）。
	Registry string `json:"registry"`
	// Username 是仓库登录名。
	Username string `json:"username"`
	// Remark 是备注。
	Remark string `json:"remark,omitempty"`
	// Password 恒为掩码「****」。
	Password string `json:"password"`
	// CreatedAt 是创建时刻（unix 秒）。
	CreatedAt int64 `json:"createdAt,omitempty"`
}

// DockerRegistryListResp 是凭据列表响应。
type DockerRegistryListResp struct {
	List []DockerRegistryItem `json:"list"`
}

// ── 任务中心（6b：GET /docker/tasks）───────────────────────────────

// DockerTaskItem 是任务中心的一行（一条指令从受理到终态的照实投影）。
//
// Status 的取值集合是**指令记录状态的照实投影**：pending/succeeded/failed/timeout。
// 为什么没有 cancelled —— 盘点结论（6b 查现状、零行为改动纪律下的裁决）：
// 指令记录没有「取消」生产者：流取消（三期）作用在**流会话**上、与指令记录
// 分家（会话建立后记录已终结）；pull 被用户取消时 agent 以 failed + 结论句
// 「拉取已取消」回写，取消语义由 Summary 这一句承载。本切片不造新状态 ——
// 那要改状态机与 agent（协议/agent 侧皆不在本切片边界内，且违反
// 「既有受理/轮询/流零行为改动」的纪律）。timeout 照实暴露：它是 sweep 的
// 服务端推断（与「执行失败」是两类事实），冒充 failed 会让排障的人去查一个
// 不存在的执行错误。
type DockerTaskItem struct {
	// Ref 是指令号（轮询 /cmds/:ref 与拉取进度流 /cmds/:ref/pull 的钥匙）。
	Ref string `json:"ref"`
	// HostID 是目标主机的设备 ID（雪花值，string 编码与全站同纪律）。
	HostID uint64 `json:"hostId,string"`
	// Hostname 是目标主机名（联查设备表；设备已删时为空 —— hostId 仍是可导航主键）。
	Hostname string `json:"hostname"`
	// Action 是动作码（前端凭它判断「可流」：image:pull/compose:pull 可开进度流，
	// 不需要额外的 streamable 字段 —— action 就是判定依据）。
	Action string `json:"action"`
	// Target 是操作目标（镜像名/容器名/项目名…）。
	Target string `json:"target"`
	// Username 是发起人用户名（联查用户表；用户已删时为空，审计仍按 UserID 记账）。
	Username string `json:"username"`
	// CreatedAt 是受理时刻（unix 毫秒，与指令记录同单位）。
	CreatedAt int64 `json:"createdAt"`
	// Status 终态照实投影：pending 在执行、succeeded 成功、failed 失败、timeout 超时。
	Status string `json:"status"`
	// Summary 是终态结论句原文（pending 时为空；成功兜底「执行成功」——与审计
	// 入账的结论句同一句话，见 service/docker_audit.go 的 terminalSummary）。
	Summary string `json:"summary"`
}

// DockerTaskListResp 是最近任务列表响应（≤100 条、受理时刻降序）。
//
// **不报 Total**：枚举窗口本身是契约（「最近」不是全量承诺），窗口外的条目数
// 无从知晓 —— 报一个「窗口内匹配数」会被当成全量口径误读（与 /docker/containers
// 的 Total 语义刻意不同：那边的枚举是完整的）。
// Items 空时为空数组而非 null（与全站清单约定一致，前端少一层判空）。
type DockerTaskListResp struct {
	Items []DockerTaskItem `json:"items"`
}

// ── 容器 stats 历史（P2：GET /docker/hosts/:id/containers/:cid/stats-history）──

// DockerStatsHistoryResp 是一个容器的 stats 留存序列响应。
//
// 窗口契约：30s 快照节奏 × 60 样本 ≈ 30 分钟（dockerstate.StatsHistoryKeep）；
// 容器消失后序列冻结在最后一次读数上、30 分钟后由 TTL 收走。**不报窗口宽度/
// 样本上限字段**：那是后端容量决策，前端按「有什么画什么」渲染，承诺窗口形状
// 反而会在容量调整时变成前端要适配的第二个口径。
type DockerStatsHistoryResp struct {
	// Samples 按时刻**升序**（曲线从左往右）；无历史时空数组而非 null ——
	// 「没有留存」是正常答案（刚建的容器、升级前、容器死满 30 分钟后），
	// 与 404（设备不存在）各说各的话。
	Samples []DockerStatsHistorySample `json:"samples"`
}

// DockerStatsHistorySample 是留存序列的一个读数点。
//
// 字段与快照条目 / stats 实时流的同名字段**同一口径**（cpu_percent/mem_usage_mb/
// mem_limit_mb，单位与 round2 一致）：抽屉里「历史曲线、实时曲线、表格读数」三处
// 是同一套数据约定。差异只有两点：T 是 **core 收帧时刻**（不是 agent 时钟 ——
// 留存侧无法逐样本改写流样本的时钟，两条曲线在时钟偏斜主机上的衔接由前端按
// 先后拼接）；**不带网络字段**（30 分钟窗口内价值低且体积翻倍，取舍见
// dockerstate/stats_history.go 的体积账）。
type DockerStatsHistorySample struct {
	// T 是 core 收到该快照帧的时刻（unix 毫秒），曲线的 x 轴刻度。
	T          int64   `json:"t"`
	CPUPercent float64 `json:"cpuPercent"`
	MemUsageMB float64 `json:"memUsageMb"`
	MemLimitMB float64 `json:"memLimitMb"`
}

// ── 构建上下文上传（v1.3：POST /docker/hosts/:id/build-context）────────────

// DockerBuildContextUploadResp 是构建上下文上传的响应：filename 是 image:build
// 的 `options.context` 要填的值（core 由会话号推导的 transferDir 内产物文件名，
// 形态即 IsDockerBuildContextFilename 白名单 —— 构建对话框拿到它原样塞回指令）。
//
// 为什么只有 filename 一个字段：上传段不承诺任何 tar 内容事实（条目/层次是
// build 执行时 scanBuildContext 的深度校验），尺寸与哈希是**传输完整性**的内部
// 账目（随完成控制帧核对），对用户不可行动 —— 报出去只会变成第二个口径源头。
type DockerBuildContextUploadResp struct {
	FileName string `json:"filename"`
}
