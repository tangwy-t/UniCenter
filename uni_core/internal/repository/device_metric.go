package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
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
		if err := upsertRows(tx, entity.TableNameMetricDisk, subs.Disks); err != nil {
			return err
		}
		if err := upsertRows(tx, entity.TableNameMetricDiskIO, subs.DiskIO); err != nil {
			return err
		}
		if err := upsertRows(tx, entity.TableNameMetricNIC, subs.NICs); err != nil {
			return err
		}
		return upsertRows(tx, entity.TableNameMetricSensor, subs.Sensors)
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
//
// table 由调用方**显式**给出，与 upsertWide 的形状一致（评审 F3）：
//   - 原先靠泛型 `firstTable[T]` 在运行期判类型，未知类型返回 ""，于是
//     subValueColumns("") 为 nil → DoUpdates 为空 → 冲突时**静默退化成 DO NOTHING**
//     （不报错、也不更新；实测重放后值仍是旧值）。删掉类型判定后这条静默路径不复存在。
//   - 显式表名还避免了「结构体的 TableName() 未必等于目标表」这类陷阱
//     （_1h 就是同一结构体两个表名，upsertWide 也必须靠显式表名）。
//
// 表名未登记（subValueColumnsByTable 缺条目）时**硬失败**：这是配置错误，
// 若返回 nil 值列清单就会重演上面的静默 DO NOTHING。
func upsertRows[T any](tx *gorm.DB, table string, rows []T) error {
	cols, ok := subValueColumnsByTable[table]
	if !ok {
		return fmt.Errorf("agentmetrics: 子表 %q 未登记值列清单（subValueColumnsByTable 缺条目会让冲突静默退化成 DO NOTHING），拒绝写入", table)
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Table(table).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "resource_id"}, {Name: "bucket_ts"}},
		DoUpdates: clause.AssignmentColumns(cols),
	}).CreateInBatches(rows, 100).Error
}

var subValueColumnsByTable = map[string][]string{
	entity.TableNameMetricDisk:   {"used_percent", "used_gb", "total_gb", "inodes_used_percent"},
	entity.TableNameMetricDiskIO: {"read_bytes_per_sec", "write_bytes_per_sec", "read_ops_per_sec", "write_ops_per_sec", "io_time_percent"},
	entity.TableNameMetricNIC: {"rx_bytes_per_sec", "tx_bytes_per_sec", "rx_packets_per_sec",
		"tx_packets_per_sec", "rx_errors_per_sec", "tx_errors_per_sec", "rx_dropped_per_sec"},
	entity.TableNameMetricSensor: {"temperature_c"},
}

// metricQueryColumns 是允许出现在白名单投影里的列（防止 SQL 注入与误投影）。
var metricQueryColumns = map[string]map[string]bool{}

func init() {
	for _, table := range MetricTables() {
		allowed := map[string]bool{"bucket_ts": true}
		if ddl, ok := MetricColumnDDL(table); ok {
			// 与一致性守卫**同源**解析（评审 F5）：只收裸标识符列名。
			// 原先这里用裸 strings.Split，把 `PRIMARY KEY (device_id, bucket_ts)`
			// 的续行 token "bucket_ts)" 也收进了白名单，且 sanitizeColumns 会放行它。
			for _, name := range parseDDLColumnNames(ddl) {
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

// ReadTrendPoints 按 (device_id, bucket_ts 范围) 读取整机趋势，只投影白名单列，
// 并**直接扫描进 agentmetrics.TrendPoint**。
//
// 为什么不再返回 []map[string]any：map 的值类型随驱动而变（sqlite 给
// int64/float64/string/[]byte），把 40 个字段逐个类型断言既脆弱又冗长。
// 类型化之后由 GORM 负责「列名 → 字段」，未选中的列保持 nil —— 「白名单投影」
// 这条契约不但仍然成立，还变得可测（见 TestReadTrendProjectsOnlyWhitelistedColumns）。
func (r *DeviceMetricRepo) ReadTrendPoints(ctx context.Context, table string, deviceID uint64,
	from, to int64, columns []string) ([]agentmetrics.TrendPoint, error) {
	cols, err := sanitizeColumns(table, columns)
	if err != nil {
		return nil, err
	}
	var out []agentmetrics.TrendPoint
	err = r.db.WithContext(ctx).Table(table).
		Select(aliasBucketTS(cols)).
		Where("device_id = ? AND bucket_ts BETWEEN ? AND ?", deviceID, from, to).
		Order("bucket_ts ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// aliasBucketTS 把 select 列表里的 bucket_ts 改写成 `bucket_ts AS t`：
// agentmetrics.TrendPoint 的时间字段是 T（响应契约里叫 t），必须显式对齐，
// 否则扫出来的每一点 t 都是 0，前端按 t 定位时间就全落在 1970。
func aliasBucketTS(cols []string) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		if c == "bucket_ts" {
			parts = append(parts, "bucket_ts AS t")
			continue
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, ", ")
}

// ReadResourceTrendPoints 按 (resource_id, bucket_ts 范围) 读取明细 drill，只投影白名单列。
// 与 ReadTrendPoints 同形，差别只在过滤键。
func (r *DeviceMetricRepo) ReadResourceTrendPoints(ctx context.Context, table string, resourceID uint64,
	from, to int64, columns []string) ([]agentmetrics.TrendPoint, error) {
	cols, err := sanitizeColumns(table, columns)
	if err != nil {
		return nil, err
	}
	var out []agentmetrics.TrendPoint
	err = r.db.WithContext(ctx).Table(table).
		Select(aliasBucketTS(cols)).
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
