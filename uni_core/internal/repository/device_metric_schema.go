package repository

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// 列 DDL 手写并按表集中在此文件。
//
// 为什么不复用 AutoMigrate：6 张指标表必须是**分区表**，而 AutoMigrate 只能建
// 普通表；MySQL 事后转分区是整表 COPY 重建，PG 官方明确不允许把普通表转成分区表。
// 为什么列类型与方言无关：统一用 BIGINT / INT / DOUBLE PRECISION / VARCHAR 这个
// 可移植子集，MySQL、PG、sqlite 都接受，于是方言差异只落在分区包裹上。
//
// 「列 DDL ↔ 实体字段」的一致性由 device_metric_schema_test.go 的
// TestMetricDDLColumnsMatchEntityTags 守卫（DDL 无法参数化，这是唯一防线）。

// MetricTables 返回 6 张指标表名（顺序稳定：宽表在前）。
func MetricTables() []string {
	return []string{
		entity.TableNameMetric5m,
		entity.TableNameMetric1h,
		entity.TableNameMetricDisk,
		entity.TableNameMetricDiskIO,
		entity.TableNameMetricNIC,
		entity.TableNameMetricSensor,
	}
}

var metricColumnDDL = map[string]string{
	entity.TableNameMetric5m: `
	device_id BIGINT NOT NULL,
	bucket_ts BIGINT NOT NULL,
	cpu_used_percent DOUBLE PRECISION,
	cpu_iowait DOUBLE PRECISION,
	load1 DOUBLE PRECISION,
	load5 DOUBLE PRECISION,
	load15 DOUBLE PRECISION,
	mem_used_percent DOUBLE PRECISION,
	mem_used_mb DOUBLE PRECISION,
	mem_available_mb DOUBLE PRECISION,
	swap_used_percent DOUBLE PRECISION,
	swap_used_mb DOUBLE PRECISION,
	tcp_total BIGINT,
	tcp_established BIGINT,
	tcp_listen BIGINT,
	tcp_time_wait BIGINT,
	tcp_close_wait BIGINT,
	udp_total BIGINT,
	proc_count BIGINT,
	uptime_sec BIGINT,
	samples INT NOT NULL,
	disk_total_gb DOUBLE PRECISION,
	disk_used_gb DOUBLE PRECISION,
	disk_used_percent DOUBLE PRECISION,
	disk_io_read_bytes_sec DOUBLE PRECISION,
	disk_io_write_bytes_sec DOUBLE PRECISION,
	disk_io_read_ops_sec DOUBLE PRECISION,
	disk_io_write_ops_sec DOUBLE PRECISION,
	nic_rx_bytes_sec DOUBLE PRECISION,
	nic_tx_bytes_sec DOUBLE PRECISION,
	nic_rx_packets_sec DOUBLE PRECISION,
	nic_tx_packets_sec DOUBLE PRECISION,
	nic_rx_errors_sec DOUBLE PRECISION,
	nic_tx_errors_sec DOUBLE PRECISION,
	nic_rx_dropped_sec DOUBLE PRECISION,
	max_temperature_c DOUBLE PRECISION,
	agent_collect_duration_ms DOUBLE PRECISION,
	agent_report_success_count BIGINT,
	agent_report_error_count BIGINT,
	agent_ws_reconnect_count BIGINT,
	agent_last_report_error VARCHAR(1024),
	agent_mem_resident_mb DOUBLE PRECISION,
	agent_pending_backlog BIGINT,
	agent_last_report_latency_ms DOUBLE PRECISION,
	agent_report_drop_count BIGINT,
	agent_uptime_sec BIGINT,
	PRIMARY KEY (device_id, bucket_ts)`,
	entity.TableNameMetric1h: `__SAME_AS_5M__`,
	entity.TableNameMetricDisk: `
	resource_id BIGINT NOT NULL,
	bucket_ts BIGINT NOT NULL,
	used_percent DOUBLE PRECISION,
	used_gb DOUBLE PRECISION,
	total_gb DOUBLE PRECISION,
	inodes_used_percent DOUBLE PRECISION,
	PRIMARY KEY (resource_id, bucket_ts)`,
	entity.TableNameMetricDiskIO: `
	resource_id BIGINT NOT NULL,
	bucket_ts BIGINT NOT NULL,
	read_bytes_per_sec DOUBLE PRECISION,
	write_bytes_per_sec DOUBLE PRECISION,
	read_ops_per_sec DOUBLE PRECISION,
	write_ops_per_sec DOUBLE PRECISION,
	io_time_percent DOUBLE PRECISION,
	PRIMARY KEY (resource_id, bucket_ts)`,
	entity.TableNameMetricNIC: `
	resource_id BIGINT NOT NULL,
	bucket_ts BIGINT NOT NULL,
	rx_bytes_per_sec DOUBLE PRECISION,
	tx_bytes_per_sec DOUBLE PRECISION,
	rx_packets_per_sec DOUBLE PRECISION,
	tx_packets_per_sec DOUBLE PRECISION,
	rx_errors_per_sec DOUBLE PRECISION,
	tx_errors_per_sec DOUBLE PRECISION,
	rx_dropped_per_sec DOUBLE PRECISION,
	PRIMARY KEY (resource_id, bucket_ts)`,
	entity.TableNameMetricSensor: `
	resource_id BIGINT NOT NULL,
	bucket_ts BIGINT NOT NULL,
	temperature_c DOUBLE PRECISION,
	PRIMARY KEY (resource_id, bucket_ts)`,
}

// ddlColumnNameRe 只接受**裸标识符**列名。约束续行（PRIMARY KEY 被 ',' 切开的
// "bucket_ts)"）与大写关键字（PRIMARY/UNIQUE/KEY）都不匹配。
var ddlColumnNameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// parseDDLColumnNames 从列 DDL 片段解析出列名，是**唯一**的解析实现：
// device_metric.go 的查询白名单 init() 与 device_metric_schema_test.go 的
// 「DDL 列名 ↔ 实体字段」守卫**都调用它**（评审 F5）。
//
// 为什么必须同源：两处各写一套解析必然漂移 —— Task 6 的守卫修过「约束续行被当成列」
// 之后，生产侧 init() 仍在用裸 strings.Split 把 "bucket_ts)" 收进查询白名单，
// 且 sanitizeColumns 会放行它（实测探针：6 张表白名单各多 1 个假列，
// sanitizeColumns(t, ["bucket_ts)"]) 无错返回）。
//
// 规则（与守卫的期望值一致）：
//  1. 按 ',' 切分（每个列定义一段，约束尾随的续行自成一类 token）；
//  2. 取每段首个 token 并去掉反引号/双引号；
//  3. 只接受 ^[a-z0-9_]+$ —— 其余（约束续行、大写关键字）一律不是列定义。
func parseDDLColumnNames(ddl string) []string {
	names := make([]string, 0, 48)
	for _, line := range strings.Split(ddl, ",") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 {
			continue
		}
		name := strings.Trim(f[0], "`\"")
		if !ddlColumnNameRe.MatchString(name) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// MetricColumnDDL 返回某张指标表的列定义片段。
//
// _1h 的列集与 _5m **完全相同**（一套结构两处表名）：省那 11 个恒 NULL 的列
// 只值几 MB（MySQL 实测 +2.10 B/行、PG +0 B/行），不值得维护第二份 schema。
func MetricColumnDDL(table string) (string, bool) {
	ddl, ok := metricColumnDDL[table]
	if !ok {
		return "", false
	}
	if ddl == "__SAME_AS_5M__" {
		ddl = metricColumnDDL[entity.TableNameMetric5m]
	}
	return ddl, true
}

// DeviceMetricSchemaRepo 负责 6 张指标表的结构与分区维护。
// 它是唯一对指标表执行 DDL 的地方。
type DeviceMetricSchemaRepo struct {
	db *gorm.DB
}

func NewDeviceMetricSchemaRepository(db *gorm.DB) *DeviceMetricSchemaRepo {
	return &DeviceMetricSchemaRepo{db: db}
}

// EnsureMetricSchema 是 EnsureSchema 的**包级入口**，专供迁移的 pre-AutoMigrate
// 钩子调用 —— 钩子在 wireup 之前执行，那时还没有 DI 出来的仓储实例。
func EnsureMetricSchema(ctx context.Context, db *gorm.DB, dialect string) error {
	return NewDeviceMetricSchemaRepository(db).EnsureSchema(ctx, dialect, time.Now().UTC())
}

// EnsureSchema 以分区形态建表（幂等），并为每张表建立当前所需的全部分区。
//
// 方言行为：
//   - mysql：CREATE TABLE ... PARTITION BY RANGE COLUMNS(bucket_ts)（分区内联）
//   - postgres：CREATE TABLE ... PARTITION BY RANGE (bucket_ts) + 逐分区 CREATE TABLE ... PARTITION OF
//   - sqlite / 其它：普通表（不支持分区）；协调器走 DELETE 降级路径
func (r *DeviceMetricSchemaRepo) EnsureSchema(ctx context.Context, dialect string, now time.Time) error {
	for _, spec := range agentmetrics.TableSpecs() {
		cols, ok := MetricColumnDDL(spec.Table)
		if !ok {
			return fmt.Errorf("agentmetrics: %s 缺少列 DDL", spec.Table)
		}
		bounds := agentmetrics.Desired(spec, now, 3)

		create := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", spec.Table, cols)
		switch dialect {
		case "postgres":
			// PG 的父表需要 PARTITION BY，分区由子表承担
			create += " PARTITION BY RANGE (bucket_ts)"
		default:
			clause, err := agentmetrics.InlineClause(dialect, spec, bounds)
			if err != nil {
				return err
			}
			if clause != "" {
				create += " " + clause
			}
		}
		if err := r.db.WithContext(ctx).Exec(create).Error; err != nil {
			return fmt.Errorf("agentmetrics: 建表 %s 失败: %w", spec.Table, err)
		}

		children, err := agentmetrics.ChildDDL(dialect, spec, bounds)
		if err != nil {
			return err
		}
		for _, stmt := range children {
			if err := r.db.WithContext(ctx).Exec(stmt).Error; err != nil {
				return fmt.Errorf("agentmetrics: 建分区失败: %w", err)
			}
		}
	}
	return nil
}

// ExistingPartitions 返回某张表已存在的分区名（用于协调器对账）。
// MySQL 读 information_schema.PARTITIONS；其它方言返回空（走降级路径）。
func (r *DeviceMetricSchemaRepo) ExistingPartitions(ctx context.Context, table string) ([]string, error) {
	if r.db.Dialector.Name() != "mysql" {
		return nil, nil
	}
	var names []string
	err := r.db.WithContext(ctx).
		Raw("SELECT PARTITION_NAME FROM information_schema.PARTITIONS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL", table).
		Scan(&names).Error
	if err != nil {
		return nil, err
	}
	return names, nil
}
