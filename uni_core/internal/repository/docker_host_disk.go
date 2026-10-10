package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DockerHostDiskRepo 读一批设备的**宿主磁盘水位**：设备指标宽表（device_metric_5m）
// 里每台设备**最近一条**整机行的磁盘列（disk_used_percent / disk_total_gb / disk_used_gb）。
//
// 为什么单开一个仓储（而不是给 DeviceMetricRepo 加方法）：它与 DeviceMetricRepo 的
// 其它读法（按设备/区间读趋势、下钻明细）是不同的取数口径 ——「跨一批设备的**最新一条**」
// 是一类独立的批量读，消费方是 docker 读面（宿主磁盘水位 join 进主机清单/总览），
// 与设备监控页的取数没有交集。命名的 docker 前缀指的是**消费方**而非数据源：
// 读的仍是设备指标表（internal/repository 仍是唯一查询指标表的地方，本文件不破这条）。
//
// 它答的是运维第一问「哪台机器的盘快满了」的**分母**（总容量）——与 docker
// `system df` 的占用账目（快照里的 DiskUsage，只有分子）是两回事，两者互不覆盖。
type DockerHostDiskRepo struct {
	db *gorm.DB
}

func NewDockerHostDiskRepo(db *gorm.DB) *DockerHostDiskRepo {
	return &DockerHostDiskRepo{db: db}
}

// LatestWideDisk 批量返回每个 deviceID 的最近一条宽表行（只取磁盘水位列；其余列留零值）。
//
// **一次查询**（不是每设备一条 —— N+1 闸）：先用派生表取每设备的 max(bucket_ts)，再 JOIN
// 回主表取该桶的行。未上报指标的设备**不在返回 map 里**（缺席 = 不知道，不是 0）。
// deviceIDs 为空直接返回空 map（不发 `IN ()` 这种无意义查询）。
//
// 取的是宽表 _5m 的**整机口径**列（disk_total_gb / disk_used_gb / disk_used_percent 是
// 同桶明细子表聚合而来的整机合计）——与设备侧水位（Redis 最新快照的 disk_used_percent）
// 同口径，两处对「这台机器磁盘用了百分之几」给出同一个数。
//
// 只投影磁盘三列（宽表 45 列，搬全行没有收益）：未选中的字段在返回结构体里保持 nil，
// 调用方按「nil = 未采集」处理（绝不与 0 混淆）。
func (r *DockerHostDiskRepo) LatestWideDisk(ctx context.Context, deviceIDs []uint64) (map[uint64]entity.DeviceMetricWide, error) {
	out := map[uint64]entity.DeviceMetricWide{}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	// 每设备的最近桶：device_id → max(bucket_ts)。
	latest := r.db.WithContext(ctx).Table(entity.TableNameMetric5m).
		Select("device_id, MAX(bucket_ts) AS bucket_ts").
		Where("device_id IN ?", deviceIDs).
		Group("device_id")
	var rows []entity.DeviceMetricWide
	err := r.db.WithContext(ctx).Table(entity.TableNameMetric5m+" AS m").
		Select("m.device_id, m.disk_total_gb, m.disk_used_gb, m.disk_used_percent").
		Joins("JOIN (?) AS latest ON latest.device_id = m.device_id AND latest.bucket_ts = m.bucket_ts", latest).
		Where("m.device_id IN ?", deviceIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].DeviceID] = rows[i]
	}
	return out, nil
}
