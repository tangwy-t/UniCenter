package response

// Agent 升级域的响应 DTO（发布物 / 任务 / 明细 / 汇总）。
//
// 三条命名与形状约定：
//   - 时间一律 unix 秒 + `omitempty`（与设备模块既有 DTO 同款）；
//   - 状态与原因码**原样透传机器码**，翻译在展示层（设计 §10）；
//   - **不用 map**：计数与分布都写成显式字段 —— apigen 只会枚举结构体字段，
//     map 生成不出可用的 TS 类型，前端会被迫用 `Record<string, number>` 猜键。

// AgentReleaseItem 是发布物列表的一行。
type AgentReleaseItem struct {
	ID        uint64 `json:"id,string"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	FileName  string `json:"fileName,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	// SHA256 是服务端落盘时算的摘要。前端**只展示不解释**（不出现「sha256」等
	// 内部术语的地方也不该出现它的值 —— 展示层只给大小与时间）。
	SHA256    string `json:"sha256"`
	Status    int8   `json:"status"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	// PublishedAt 只在已发布时有值（撤回时置空）。
	PublishedAt *int64 `json:"publishedAt,omitempty"`
	// Deletable 是**服务端算好的结论**：该版本是否还没被任何升级尝试用过。
	// 前端据此禁用删除按钮并说明原因，而不是点了才报错 —— 回滚余量是硬约束
	// （用过即禁删），把它做成一个「按下才知道」的失败体验没有必要。
	Deletable bool `json:"deletable"`
}

// AgentReleaseListResp 是发布物列表响应。
type AgentReleaseListResp struct {
	List []AgentReleaseItem `json:"list"`
	// PublishedVersions 是已发布过的版本号（去重、新→旧），供「升级到…」下拉。
	// 与 List 一并返回：下拉与列表必须同源，否则会出现「列表里有、下拉里没有」。
	PublishedVersions []string `json:"publishedVersions"`
}

// DeviceUpgradeSkip 是下发/预演里**被跳过**的明细。
//
// 四类跳过各有明确语义，页面上也要分开报：
//   - AlreadyOnTarget：已经在该版本上（无需动）；
//   - Unsupported：agent 自报不支持远程升级（现场存量 0.1.0）→ 需先手工引导；
//   - NoArtifact：该设备平台在该版本没有已发布产物（升级会无从下载）；
//   - Disabled：设备被停用（连不上，下发没有意义）。
type DeviceUpgradeSkip struct {
	AlreadyOnTarget int64 `json:"alreadyOnTarget"`
	Unsupported     int64 `json:"unsupported"`
	NoArtifact      int64 `json:"noArtifact"`
	Disabled        int64 `json:"disabled"`
}

// Total 返回跳过总数。
func (s DeviceUpgradeSkip) Total() int64 {
	return s.AlreadyOnTarget + s.Unsupported + s.NoArtifact + s.Disabled
}

// DeviceUpgradePreviewResp 是「按筛选下发」的影响面预演（下发前的确认依据）。
type DeviceUpgradePreviewResp struct {
	TargetVersion string `json:"targetVersion"`
	// Matched 是筛选命中的台数（不管能否升级）。
	Matched int64 `json:"matched"`
	// WillUpgrade 是实际会下发的台数 = Matched - Skip.Total()。
	WillUpgrade int64             `json:"willUpgrade"`
	Skip        DeviceUpgradeSkip `json:"skip"`
}

// DeviceUpgradeDispatchResp 是下发结果（响应即影响面，页面据此给结论文案）。
type DeviceUpgradeDispatchResp struct {
	TargetVersion string `json:"targetVersion"`
	// TaskID 是本次下发建出的任务（前端用它跳到任务页看进度）。
	TaskID     string            `json:"taskId"`
	Dispatched int64             `json:"dispatched"`
	Skip       DeviceUpgradeSkip `json:"skip"`
}

// DeviceUpgradeGlobalResp 是全站目标版本变更的结果。
type DeviceUpgradeGlobalResp struct {
	// TargetVersion 是**变更后**的全站目标（空 = 已关闭全站升级）。
	TargetVersion string `json:"targetVersion"`
	// Affected 是生效目标随之改变、需要动一动的台数。
	Affected int64 `json:"affected"`
	// PinnedDevices 是设备级已指定版本、**不跟随全站**的台数。
	// 它是全站变更时最容易被忽略的一类：页面必须把它说出来，否则
	// 「全站都升了」会与「这几台没动」同时成立而没人知道为什么。
	PinnedDevices int64 `json:"pinnedDevices"`
	// TaskID 是本次变更建出的任务（为空 = 没有设备需要动）。
	TaskID string `json:"taskId,omitempty"`
}

// AgentUpgradeCounts 是任务明细的状态分布（读时聚合，不落冗余计数）。
//
// 显式字段而不是 map：apigen 只枚举结构体字段，map 生成不出可用类型。
type AgentUpgradeCounts struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	RolledBack int `json:"rolledBack"`
	Timeout    int `json:"timeout"`
	Superseded int `json:"superseded"`
}

// AgentUpgradeTaskItem 是任务列表的一行。
type AgentUpgradeTaskItem struct {
	ID            uint64             `json:"id,string"`
	TargetVersion string             `json:"targetVersion"`
	Source        string             `json:"source"`
	Actor         string             `json:"actor,omitempty"`
	Total         int                `json:"total"`
	Counts        AgentUpgradeCounts `json:"counts"`
	CreatedAt     int64              `json:"createdAt"`
	FinishedAt    *int64             `json:"finishedAt,omitempty"`
}

// AgentUpgradeAttemptItem 是一条明细（任务页的一行，也是设备「升级记录」的一行）。
type AgentUpgradeAttemptItem struct {
	ID       uint64 `json:"id,string"`
	TaskID   string `json:"taskId,omitempty"`
	DeviceID uint64 `json:"deviceId,string"`
	// Hostname / PrimaryIP 取自设备表；设备已删除时为空串且 DeviceDeleted=true。
	Hostname      string `json:"hostname"`
	DeviceDeleted bool   `json:"deviceDeleted"`
	PrimaryIP     string `json:"primaryIp,omitempty"`

	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion"`
	State       string `json:"state"`
	// Progress 只在下载阶段有值（协议层已钉死），nil = 本阶段没有百分比。
	Progress   *int   `json:"progress,omitempty"`
	ReasonCode string `json:"reasonCode,omitempty"`

	CreatedAt    int64  `json:"createdAt"`
	StartedAt    *int64 `json:"startedAt,omitempty"`
	LastReportAt *int64 `json:"lastReportAt,omitempty"`
	FinishedAt   *int64 `json:"finishedAt,omitempty"`
}

// AgentUpgradeTaskDetailResp 是任务详情（含明细分页）。
type AgentUpgradeTaskDetailResp struct {
	Task     AgentUpgradeTaskItem      `json:"task"`
	List     []AgentUpgradeAttemptItem `json:"list"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
}

// DeviceUpgradeBucket 是「按生效目标版本」的一个状态桶。
type DeviceUpgradeBucket struct {
	// TargetVersion 为空 = 无目标（既没有设备级也没有全站目标）。
	TargetVersion string `json:"targetVersion"`
	Total         int64  `json:"total"`
	// Achieved 是已达成的台数（**读时推导**：生效目标 == 当前版本）。
	Achieved int64 `json:"achieved"`
	// Running 是升级中的台数（存在未终结尝试）。
	Running    int64 `json:"running"`
	Pending    int64 `json:"pending"`
	Failed     int64 `json:"failed"`
	RolledBack int64 `json:"rolledBack"`
	// Unsupported / NoArtifact / Disabled 是三类「想升也升不了」的台数
	// （见 DeviceUpgradeSkip）。Disabled 在汇总里要单列：停用设备根本连不上，
	// 把它混进「等待上线」会让运维去等一台永远不会上线的机器。
	Unsupported int64 `json:"unsupported"`
	NoArtifact  int64 `json:"noArtifact"`
	Disabled    int64 `json:"disabled"`
}

// DeviceVersionCount 是版本分布的一项（按当前 agent_version 数）。
type DeviceVersionCount struct {
	Version string `json:"version"`
	Count   int64  `json:"count"`
}

// DeviceUpgradeSummaryResp 是「当前状态」视角的汇总。
//
// 与任务页的分工：任务页回答「**这一次操作**结果如何」，本响应回答
// 「**现在**全站处于什么状态」—— 两者不可互相替代（任务会过期，状态不会）。
type DeviceUpgradeSummaryResp struct {
	Total int64 `json:"total"`
	// GlobalTargetVersion 是当前全站目标（空 = 未开启全站升级）。
	GlobalTargetVersion string `json:"globalTargetVersion,omitempty"`
	// PinnedDevices 是设备级指定了版本的台数（不跟随全站）。
	PinnedDevices int64                 `json:"pinnedDevices"`
	Buckets       []DeviceUpgradeBucket `json:"buckets"`
	// VersionDistribution 是当前版本分布（新→旧按台数降序）。
	VersionDistribution []DeviceVersionCount `json:"versionDistribution"`
}

// DeviceUpgradeRecord 是设备详情页的一条升级记录（与任务明细同源，取最近 N 条）。
type DeviceUpgradeRecord struct {
	ID          uint64 `json:"id,string"`
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion"`
	State       string `json:"state"`
	ReasonCode  string `json:"reasonCode,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	FinishedAt  *int64 `json:"finishedAt,omitempty"`
}
