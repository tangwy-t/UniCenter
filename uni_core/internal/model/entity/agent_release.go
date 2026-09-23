package entity

import "time"

// TableNameAgentRelease 是 agent 发布物表：一个 (版本, 平台, 架构) 对应一份二进制。
const TableNameAgentRelease = "agent_release"

// 发布状态。
//
// 草稿态的存在理由：上传是流式的（200MB 级文件要落盘并算摘要），若上传即发布，
// 中间态就可能被设备拉到一份不完整的产物。**只有已发布**才可被选为目标版本。
const (
	AgentReleaseDraft     int8 = 0
	AgentReleasePublished int8 = 1
)

// AgentRelease 是一份已上传的 agent 二进制产物。
//
// 不嵌 BaseEntity：它是**发布物清单**而不是业务实体 ——
//   - 没有软删：删一份产物要么允许（历史未用过的）要么拒绝（用过的，见回滚余量），
//     都不需要「软删后再捞回来」这种语义；
//   - 没有 created_by/updated_by：改产物的是运维动作，审计在 sys_operation_log。
//
// **必须有名为 ID 的字段**：全局雪花回调靠 LookUpField("ID") 取字段
// （见 internal/pkg/migration/migrate.go 与 DeviceResource 的注释）。
type AgentRelease struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"          json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"          json:"updatedAt"`

	// Version 是合法 semver（服务端校验，见 uni_protocol.IsSemver）。
	Version string `gorm:"column:version;size:32;not null;uniqueIndex:uk_release_platform,priority:1" json:"version"`
	// OS / Arch 是**目标设备**的平台（linux/amd64 等），取自 hello 里的 GOOS/GOARCH。
	OS   string `gorm:"column:os;size:16;not null;uniqueIndex:uk_release_platform,priority:2" json:"os"`
	Arch string `gorm:"column:arch;size:16;not null;uniqueIndex:uk_release_platform,priority:3" json:"arch"`

	FileName string `gorm:"column:file_name;size:255" json:"fileName"`
	// SizeBytes 既是展示值，也是设备侧下载上限与进度分母（随指令下发）。
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0" json:"sizeBytes"`
	// SHA256 是服务端**边落盘边算**的摘要（不接受客户端自报），随指令下发给设备校验。
	SHA256 string `gorm:"column:sha256;size:64;not null" json:"sha256"`

	// StorageType/StorageKey 指向 storage.Backend 里的对象（local 为相对路径，s3 为对象键）。
	// 字节本身**不进数据库**（见设计 §3.1 边界规矩）。
	StorageType string `gorm:"column:storage_type;size:16;not null;default:local" json:"storageType"`
	StorageKey  string `gorm:"column:storage_key;size:512;not null" json:"-"`

	Status int8       `gorm:"column:status;not null;default:0" json:"status"`
	Notes  string     `gorm:"column:notes;size:255" json:"notes"`
	PubAt  *time.Time `gorm:"column:published_at" json:"publishedAt"`
}

func (AgentRelease) TableName() string { return TableNameAgentRelease }
