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
}

// TableName 见 entity 包约定：表名不带 sys_ 前缀（业务域实体）。
func (Device) TableName() string { return "device" }

const (
	DeviceStatusDisabled int8 = 0
	DeviceStatusEnabled  int8 = 1
)
