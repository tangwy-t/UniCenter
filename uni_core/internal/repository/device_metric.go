package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// MetricSubRows 是 4 张资源明细子表待写行。
type MetricSubRows struct {
	Disks   []entity.DeviceMetricDisk
	DiskIO  []entity.DeviceMetricDiskIO
	NICs    []entity.DeviceMetricNIC
	Sensors []entity.DeviceMetricSensor
}

// DeviceMetricRepo 是 6 张指标表的数据访问层，也是唯一对它们执行 DML 的地方。
type DeviceMetricRepo struct {
	db *gorm.DB
}

func NewDeviceMetricRepository(db *gorm.DB) *DeviceMetricRepo {
	return &DeviceMetricRepo{db: db}
}

// wideValueColumns 是宽表冲突时需要覆盖的值列。
// **不含** device_id / bucket_ts（键列），也不含任何 id。
var wideValueColumns = []string{
	"cpu_used_percent", "cpu_iowait", "load1", "load5", "load15",
	"mem_used_percent", "mem_used_mb", "mem_available_mb", "swap_used_percent", "swap_used_mb",
	"tcp_total", "tcp_established", "tcp_listen", "tcp_time_wait", "tcp_close_wait",
	"udp_total", "proc_count", "uptime_sec", "samples",
	"disk_total_gb", "disk_used_gb", "disk_used_percent",
	"disk_io_read_bytes_sec", "disk_io_write_bytes_sec", "disk_io_read_ops_sec", "disk_io_write_ops_sec",
	"nic_rx_bytes_sec", "nic_tx_bytes_sec", "nic_rx_packets_sec", "nic_tx_packets_sec",
	"nic_rx_errors_sec", "nic_tx_errors_sec", "nic_rx_dropped_sec", "max_temperature_c",
	"agent_collect_duration_ms", "agent_report_success_count", "agent_report_error_count",
	"agent_ws_reconnect_count", "agent_last_report_error", "agent_mem_resident_mb",
	"agent_pending_backlog", "agent_last_report_latency_ms", "agent_report_drop_count", "agent_uptime_sec",
}

// WriteBucket 在**单个事务（事务 A）**内 UPSERT 宽表 _5m 的 1 行 + 4 张子表的 K 行。
//
// 与 WriteHour 分成两个方法的理由：1h 是**可推导**数据（能从 5m 重算），
// 绝不能让它的写失败把 5m —— 唯一真值来源 —— 一起回滚掉。
// 拆成两个方法后，「共用事务」在签名层面就写不出来。
func (r *DeviceMetricRepo) WriteBucket(ctx context.Context, w *entity.DeviceMetricWide, subs MetricSubRows) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := upsertWide(tx, entity.TableNameMetric5m, w); err != nil {
			return err
		}
		if err := upsertRows(tx, subs.Disks); err != nil {
			return err
		}
		if err := upsertRows(tx, subs.DiskIO); err != nil {
			return err
		}
		if err := upsertRows(tx, subs.NICs); err != nil {
			return err
		}
		return upsertRows(tx, subs.Sensors)
	})
}

// WriteHour 在**独立事务（事务 B）**内 UPSERT 1h 宽表的 1 行。
func (r *DeviceMetricRepo) WriteHour(ctx context.Context, w *entity.DeviceMetricWide) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertWide(tx, entity.TableNameMetric1h, w)
	})
}

func upsertWide(tx *gorm.DB, table string, w *entity.DeviceMetricWide) error {
	if w == nil {
		return nil
	}
	return tx.Table(table).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "bucket_ts"}},
		DoUpdates: clause.AssignmentColumns(wideValueColumns),
	}).Create(w).Error
}

// upsertRows 对任意子表做批量 UPSERT；冲突键统一为 (resource_id, bucket_ts)。
// 空切片直接跳过（空桶不写行，与 spec §7.1 一致）。
func upsertRows[T any](tx *gorm.DB, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "resource_id"}, {Name: "bucket_ts"}},
		DoUpdates: clause.AssignmentColumns(subValueColumns(firstTable(tx, rows))),
	}).CreateInBatches(rows, 100).Error
}

// firstTable 返回这批行对应的表名（用于取该表的值列清单）。
func firstTable[T any](_ *gorm.DB, rows []T) string {
	var zero T
	switch any(zero).(type) {
	case entity.DeviceMetricDisk:
		return entity.TableNameMetricDisk
	case entity.DeviceMetricDiskIO:
		return entity.TableNameMetricDiskIO
	case entity.DeviceMetricNIC:
		return entity.TableNameMetricNIC
	case entity.DeviceMetricSensor:
		return entity.TableNameMetricSensor
	default:
		return ""
	}
}

var subValueColumnsByTable = map[string][]string{
	entity.TableNameMetricDisk:   {"used_percent", "used_gb", "total_gb", "inodes_used_percent"},
	entity.TableNameMetricDiskIO: {"read_bytes_per_sec", "write_bytes_per_sec", "read_ops_per_sec", "write_ops_per_sec", "io_time_percent"},
	entity.TableNameMetricNIC: {"rx_bytes_per_sec", "tx_bytes_per_sec", "rx_packets_per_sec",
		"tx_packets_per_sec", "rx_errors_per_sec", "tx_errors_per_sec", "rx_dropped_per_sec"},
	entity.TableNameMetricSensor: {"temperature_c"},
}

func subValueColumns(table string) []string { return subValueColumnsByTable[table] }

// metricQueryColumns 是允许出现在白名单投影里的列（防止 SQL 注入与误投影）。
var metricQueryColumns = map[string]map[string]bool{}

func init() {
	for _, table := range MetricTables() {
		allowed := map[string]bool{"bucket_ts": true}
		if ddl, ok := MetricColumnDDL(table); ok {
			for _, line := range strings.Split(ddl, ",") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) == 0 {
					continue
				}
				name := strings.Trim(f[0], "`\"")
				if name == "" || strings.EqualFold(name, "PRIMARY") {
					continue
				}
				allowed[name] = true
			}
		}
		metricQueryColumns[table] = allowed
	}
}

// sanitizeColumns 校验并返回投影列白名单；为空时只取 bucket_ts。
func sanitizeColumns(table string, columns []string) ([]string, error) {
	allowed, ok := metricQueryColumns[table]
	if !ok {
		return nil, fmt.Errorf("agentmetrics: 未知指标表 %q（选表即选档，必须在 6 张表内）", table)
	}
	if len(columns) == 0 {
		return []string{"bucket_ts"}, nil
	}
	out := make([]string, 0, len(columns))
	for _, c := range columns {
		if !allowed[c] {
			return nil, fmt.Errorf("agentmetrics: 列 %q 不在 %s 的白名单内", c, table)
		}
		out = append(out, c)
	}
	return out, nil
}

// ReadTrend 按 (device_id, bucket_ts 范围) 读取整机趋势，只投影白名单列。
// 一条 SQL 取齐（多指标不拆 N 条）。
func (r *DeviceMetricRepo) ReadTrend(ctx context.Context, table string, deviceID uint64, from, to int64, columns []string) ([]map[string]any, error) {
	cols, err := sanitizeColumns(table, columns)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	err = r.db.WithContext(ctx).Table(table).
		Select(strings.Join(cols, ", ")).
		Where("device_id = ? AND bucket_ts BETWEEN ? AND ?", deviceID, from, to).
		Order("bucket_ts ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadResourceTrend 按 (resource_id, bucket_ts 范围) 读取明细 drill，只投影白名单列。
func (r *DeviceMetricRepo) ReadResourceTrend(ctx context.Context, table string, resourceID uint64, from, to int64, columns []string) ([]map[string]any, error) {
	cols, err := sanitizeColumns(table, columns)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	err = r.db.WithContext(ctx).Table(table).
		Select(strings.Join(cols, ", ")).
		Where("resource_id = ? AND bucket_ts BETWEEN ? AND ?", resourceID, from, to).
		Order("bucket_ts ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountWide 统计某设备在某时间范围内的宽表行数（回滚时用于判断「5m 行数是否为 12」）。
func (r *DeviceMetricRepo) CountWide(ctx context.Context, table string, deviceID uint64, from, to int64) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Table(table).
		Where("device_id = ? AND bucket_ts BETWEEN ? AND ?", deviceID, from, to).
		Count(&n).Error
	return n, err
}
