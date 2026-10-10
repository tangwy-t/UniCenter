package entity

import "time"

// Device 是注册到平台的受管设备（客户端或服务器上运行的 uni_agent）。
//
// 表名不带 sys_ 前缀：它不是 RBAC 系统实体，而是业务域实体。
// online 不落库，由 last_seen_at 与 sys.agent.offlineThreshold 实时比较得出。
type Device struct {
	BaseEntity

	// InstanceID 是 agent 首启生成并落盘的去重指纹（UUID），enroll 幂等键。
	InstanceID   string  `gorm:"column:instance_id;size:64;not null;uniqueIndex:uk_device_instance" json:"instanceId"`
	Hostname     string  `gorm:"column:hostname;size:128"                                       json:"hostname"`
	OS           string  `gorm:"column:os;size:64"                                              json:"os"`
	Platform     string  `gorm:"column:platform;size:64"                                        json:"platform"`
	PlatformVer  string  `gorm:"column:platform_ver;size:64"                                    json:"platformVer"`
	Kernel       string  `gorm:"column:kernel;size:128"                                         json:"kernel"`
	Arch         string  `gorm:"column:arch;size:32"                                            json:"arch"`
	CPUModel     string  `gorm:"column:cpu_model;size:128"                                      json:"cpuModel"`
	CPUCores     int     `gorm:"column:cpu_cores"                                               json:"cpuCores"`
	MemTotalMB   float64 `gorm:"column:mem_total_mb"                                            json:"memTotalMb"`
	AgentVersion string  `gorm:"column:agent_version;size:32"                                   json:"agentVersion"`
	// Status 是管理侧属性（启用/停用），与在线状态正交。
	Status int8 `gorm:"column:status;not null;default:1" json:"status"`
	// TokenHash 只存 sha256(agent_token)，绝不落明文。
	TokenHash  string     `gorm:"column:token_hash;size:64;uniqueIndex:uk_device_token"       json:"-"`
	BootTime   int64      `gorm:"column:boot_time"                                           json:"bootTime"`
	LastSeenAt *time.Time `gorm:"column:last_seen_at;index:idx_device_last_seen"             json:"lastSeenAt"`

	// PrimaryIP 是**服务端观测到**的 agent 来源 IP（不含端口/掩码）。
	// 取自 WS 升级握手时的 ClientIP（代理感知：trustedProxies 非空时读
	// X-Forwarded-For/X-Real-IP，否则取对端地址），**不来自 hello 载荷**——
	// 载荷里的 IP 是客户端自述，不可作为定位依据。
	// 空串表示「尚未观测到」（老设备在升级前 enroll 的行）。
	PrimaryIP string `gorm:"column:primary_ip;size:64" json:"primaryIp,omitempty"`

	// ── 主机分组 / 标签（平台级能力）──────────────────────────────────────
	//
	// 分组与标签是**平台级**事实（不是 docker 私有）：同一份数据同时服务于设备
	// 列表与 docker 的主机清单/总览 —— docker 侧直接从设备行带出，不另存一份
	// （否则「改了分组，docker 页却还显示旧的」这类分裂不可避免）。
	//
	// GroupName 是设备所属分组（空串 = 未分组）；分组名由运维自由填写。
	// Tags 是设备标签，**逗号分隔**（如 "prod,web,cn"），空串 = 无标签。
	//
	// 为什么用 varchar 存逗号分隔串、不用 JSON 列：测试夹具是 SQLite，JSON 列的
	// 方言差异大；逗号分隔的读写与过滤都只走普通字符串比较，跨方言零负担。
	//
	// not null;default:''（而非可空）：一次消除「NULL vs 空串」在筛选/展示上的
	// 二义，也免掉把 NULL 扫进 Go string 的边界顾虑；这同样满足滚动发布对加列的
	// 硬要求（给默认值比「允许 NULL」更强一档）—— 旧二进制不写该列时落 ''。
	//
	// omitempty：空串不出现（前端显示「未分组 / 无标签」，不臆造空串）。
	GroupName string `gorm:"column:group_name;size:64;not null;default:''" json:"groupName,omitempty"`
	Tags      string `gorm:"column:tags;size:255;not null;default:''"       json:"tags,omitempty"`

	// ── Agent 升级（设计 §5.1）────────────────────────────────────────────
	//
	// 这里**只存终态**：「待升级 / 升级中」是读时推导出来的，不落库 ——
	// 生效目标是读时计算的（设备级 ?? 全站键），若把过程态也写进本行，
	// 全站目标一改，所有设备行的状态当场过期，要等各台设备重连才收敛。
	// 推导规则与仓库既有的 online（last_seen_at 比阈值）同一哲学：
	//   待升级 = 生效目标存在且 ≠ agent_version 且无未终结尝试
	//   升级中 = 存在未终结尝试（agent_upgrade_attempt.finished_at IS NULL）

	// TargetAgentVersion 是设备级目标版本（空 = 跟随全站目标）。
	TargetAgentVersion string `gorm:"column:target_agent_version;size:32" json:"targetAgentVersion,omitempty"`
	// AgentUpgradeState 是**最近一次终态**（见下方 DeviceUpgrade* 常量）。
	AgentUpgradeState int8 `gorm:"column:agent_upgrade_state;not null;default:0" json:"agentUpgradeState"`
	// AgentUpgradeReason 存**原因码**而不是译文：文案是展示层的事（设计 §10），
	// 库里存译文会让「同一件事在两种语言/两种措辞下成为两个值」。
	AgentUpgradeReason string `gorm:"column:agent_upgrade_reason;size:32" json:"agentUpgradeReason,omitempty"`
	// AgentUpgradeAt 是最近一次终态的时间（不是每次状态上报的时间）。
	AgentUpgradeAt *time.Time `gorm:"column:agent_upgrade_at" json:"agentUpgradeAt,omitempty"`
	// AgentUpgradeSupported 是设备自报的「我这一版能被远程升级」。
	// 现场存量 0.1.0 没有升级能力，对着它下发目标会毫无反应 —— 有了这个自报位，
	// 界面可以直接禁用按钮并给出结论式提示，而不是让人对着「点了没动静」猜原因。
	AgentUpgradeSupported int8 `gorm:"column:agent_upgrade_supported;not null;default:0" json:"agentUpgradeSupported"`
}

// TableName 见 entity 包约定：表名不带 sys_ 前缀（业务域实体）。
func (Device) TableName() string { return "device" }

const (
	DeviceStatusDisabled int8 = 0
	DeviceStatusEnabled  int8 = 1
)

// 设备行的升级终态。刻意**不含「待升级 / 升级中」**：那两态是读时推导的
// （见 Device 结构体里 Agent 升级段的注释），落库就会与生效目标的时钟失同步。
const (
	// DeviceUpgradeNone 表示尚无升级终态（含从未升级过）。
	DeviceUpgradeNone int8 = 0
	// DeviceUpgradeAchieved 表示最近一次升级已达成（版本与生效目标一致）。
	DeviceUpgradeAchieved int8 = 1
	// DeviceUpgradeFailed 表示最近一次升级失败（含巡检超时）。
	DeviceUpgradeFailed int8 = 2
	// DeviceUpgradeRolledBack 表示最近一次升级后设备**自行回滚**了
	// （新版本起来但连不上）。它与失败分开：回滚意味着设备仍在线且可用，
	// 而失败可能是设备根本没起来 —— 处置方向完全不同。
	DeviceUpgradeRolledBack int8 = 3
)
