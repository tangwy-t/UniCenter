package entity

import "time"

// DeviceResource 是指标明细的**资源维度表**：把挂载点 / 磁盘设备名 / 网卡名 /
// 传感器名这些长字符串从事实表挪出来，事实表只存窄整型的 resource_id。
//
// 不嵌 BaseEntity：资源维度表不需要软删与审计，只需要 id + 时间戳。
// 但**必须保留名为 ID 的字段**——全局雪花回调靠 LookUpField("ID") 取字段，
// 缺了它主键永远为 0（见 internal/pkg/migration/migrate.go 的注释）。
type DeviceResource struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"          json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"          json:"updatedAt"`

	DeviceID uint64 `gorm:"column:device_id;not null;uniqueIndex:uk_resource_identity,priority:1;index:idx_resource_device_kind,priority:1" json:"deviceId,string"`
	Kind     string `gorm:"column:kind;size:16;not null;uniqueIndex:uk_resource_identity,priority:2;index:idx_resource_device_kind,priority:2" json:"kind"`
	Name     string `gorm:"column:name;size:255;not null;uniqueIndex:uk_resource_identity,priority:3" json:"name"`
	// FSType 仅 kind=disk 有值。
	FSType string `gorm:"column:fs_type;size:32" json:"fsType"`
	// LastSeenAt 是资源最近一次被观测到的时间；本轮未出现的资源不删，
	// 由调用方据它标记「已消失」（spec §8 的 /resources stale）。
	LastSeenAt time.Time `gorm:"column:last_seen_at" json:"lastSeenAt"`
}

func (DeviceResource) TableName() string { return "device_resource" }

// 资源种类。取值直接用作 device_resource.kind，同时也是
// 4 张明细子表的一一对应关系（disk → _disk 等）。
const (
	ResourceKindDisk   = "disk"
	ResourceKindDiskIO = "disk_io"
	ResourceKindNIC    = "nic"
	ResourceKindSensor = "sensor"
)
