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
}

// DockerHostListResp 是主机清单响应。
type DockerHostListResp struct {
	List []DockerHostItem `json:"list"`
	// SnapshotInterval 是当前快照周期（秒）：前端据此算「同步于 N 秒前」的文案，
	// 与后端 stale 判定同源（各自硬编码会让两边对「多久算陈旧」给出不同答案）。
	SnapshotInterval int `json:"snapshotInterval"`
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
