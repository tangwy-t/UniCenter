package entity

import "time"

// TableNameAgentPartitionLog 是分区维护审计表（spec §7.3：「不分区、不清理」）。
//
// 为什么它**不分区**：审计行的量级是「每轮几条 × 6 张表」，一年也就几千行；
// 给它分区只会引入「审计数据自己缺分区」这第二种故障（spec §7.3 的 1526 硬失败），
// 却换不来任何收益。因此它走 **AutoMigrate 建普通表**，而不是指标表那条
// pre-migrate 分区建表钩子（见 internal/pkg/migration/migrate.go 的 autoMigrateEntities）。
//
// 为什么它**不清理**：删掉旧审计行 = 删掉缺口归因的证据。「上个月 12 号这张图上
// 为什么缺了一块」要能倒查到一年前那次误删——保留期由指标表的保留期配置决定，
// 审计流水不参与任何回收（协调器的表清单 = agentmetrics.TableSpecs()，不含本表）。
const TableNameAgentPartitionLog = "agent_metric_partition_log"

// 分区审计的动作取值。只有这两个（spec §7.3 的审计表字段表只有 add/drop）。
//
// 为什么 TRUNCATE PARTITION 也记 `drop`：TRUNCATE 与 DROP 对缺口归因是**同一件事**
// ——「该分区的数据在这一刻没了」。两者的差别（边界还在不在、空间有没有释放）是
// 分区维护的内部细节，spec §7.3 要回答的问题是「这一段时间的图为什么是空的」，
// 由 `action` + `lower_bound`/`upper_bound` + `dropped_at` 完整回答。
const (
	// PartitionActionAdd 表示**补齐**了一个分区（轮到 DDL 成功之后写入）。
	PartitionActionAdd = "add"
	// PartitionActionDrop 表示**回收**了一个分区（TRUNCATE 或 DROP 成功之后写入）。
	PartitionActionDrop = "drop"
)

// AgentPartitionLog 是一条分区维护审计流水（spec §7.3）：
// `partition_name / lower / upper / 行数 / dropped_at / 当时的保留期配置`。
//
// 不嵌 BaseEntity：这是**纯运维流水**，不是业务实体——
//   - 没有软删：删一条审计行不是任何业务动作，只是销毁证据（与指标表同理）；
//   - 没有 created_by/updated_by：改分区的是后台协调器，不是某个用户，
//     认证上下文里本来就没有用户 id（database 的 audit 回调按字段名查找，
//     本结构没有那两个字段即自动 no-op）。
//
// **必须有名为 ID 的字段**：全局雪花回调靠 `LookUpField("ID")` 取字段，
// 缺了它主键永远是 0（见 internal/pkg/migration/migrate.go 与 DeviceResource 的注释）。
//
// 字段名注意：被维护的表名落在 `Table` 上，**不能**叫 `TableName`——那会与
// 本类型的 `TableName()` 方法同名，Go 不允许同名字段与方法（计划里写的
// `TableName string` 编译不过）。
type AgentPartitionLog struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`

	// Table 是被维护的指标表名（例如 device_metric_5m）。审计按表读回（缺口归因
	// 永远是「这张图的这一段时间」）。
	Table string `gorm:"column:table_name;size:64;not null;index:idx_partition_log_table" json:"tableName"`
	// PartitionName 是确定性分区名（例如 p_2026_w37）；它就是协调器的对账键。
	PartitionName string `gorm:"column:partition_name;size:64;not null" json:"partitionName"`
	// Action 取 PartitionActionAdd / PartitionActionDrop。
	Action string `gorm:"column:action;size:8;not null" json:"action"`
	// LowerBound / UpperBound 是该分区的时间区间 [Lower, Upper)，单位 unix 秒（UTC），
	// 与 agentmetrics 的 Bound 同源（同一个反解函数，不是另算一份）。
	LowerBound int64 `gorm:"column:lower_bound;not null" json:"lowerBound"`
	UpperBound int64 `gorm:"column:upper_bound;not null" json:"upperBound"`

	// RowCount 是 spec §7.3 里那个「行数」字段，但**本实现一律写 NULL**，理由：
	//
	//	TRUNCATE PARTITION 与 DROP PARTITION 都是 **O(1) 的元数据操作**。
	//	为了记下行数而先 `SELECT COUNT(*)`，会把一次瞬时回收变成**全分区扫描**
	//	（几亿行的指标宽表）——与「回收必须瞬时、且不得与 flush 抢 IO」的目的正好相反。
	//
	// 而 spec 之所以要记这个字段，是为了**缺口归因**（能看出某个分区何时被清），
	// 那件事由 `action` + `LowerBound`/`UpperBound` + `DroppedAt` 三个字段完整表达；
	// 行数不参与归因判断。**刻意不填 0**：0 会被读成「当时这个分区确实是空的」，
	// 那是一个具体、且很可能是错的结论（真实原因是我们没去数）。
	RowCount *int64 `gorm:"column:row_count" json:"rowCount,omitempty"`

	// RetentionDays 是**当时**的保留期配置（天），即本轮对账实际用来算分区窗口的那个值
	// （`sys.agent.historyRetentionDays` / `metrics1hRetentionDays`，非法值已回落默认）。
	//
	// 存快照而不是「读的时候现查配置」，是因为配置会被运维改：事后去查当前配置，
	// 只会得到「今天的保留期」，而审计要回答的是「当时的保留期是多少」——
	// 那正是判断「这次回收是正常滑出窗口、还是有人把保留期从 180 误改成 3」的唯一证据。
	// 旧行**永不改写**：改配置后再跑一轮只会新增行，历史行的值保持当时的值。
	RetentionDays *int32 `gorm:"column:retention_days" json:"retentionDays,omitempty"`

	// CreatedAt 是审计行的写入时刻（与本轮对账用同一个注入时钟；见 service 侧注释）。
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	// DroppedAt 只在 action=drop 时填（回收发生的时刻）；add 行一律 NULL
	// ——「补了一个分区」没有「丢数据」这个语义，填了会让缺口归因把补齐误读成丢数据。
	DroppedAt *time.Time `gorm:"column:dropped_at" json:"droppedAt,omitempty"`
}

func (AgentPartitionLog) TableName() string { return TableNameAgentPartitionLog }
