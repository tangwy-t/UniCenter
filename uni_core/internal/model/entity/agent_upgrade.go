package entity

import "time"

// 升级任务与任务明细（两次升级尝试的记录）。
//
// 一张任务 = **一次下发**（单台 / 多选 / 按筛选 / 全站 / 回滚），明细每台一行，
// 每行是一次「尝试」。同一份明细供三个入口：任务页（这次操作）、设备详情
// 「升级记录」（这台机器）、汇总读数（现在全站什么状态）——禁止另建记录。
const (
	TableNameAgentUpgradeTask    = "agent_upgrade_task"
	TableNameAgentUpgradeAttempt = "agent_upgrade_attempt"
)

// 任务来源。取值直接进库并展示（展示层翻译成中文）。
const (
	AgentUpgradeSourceManual   = "manual"   // 单台
	AgentUpgradeSourceBatch    = "batch"    // 多选
	AgentUpgradeSourceFilter   = "filter"   // 按筛选全量
	AgentUpgradeSourceGlobal   = "global"   // 全站目标变更
	AgentUpgradeSourceRollback = "rollback" // 回滚（批量回滚到统一版本）
)

// 尝试状态。
//
// 前六个与协议 `agent.upgrade.status` 的 state 同名同形（设备上报直接落库，
// 不设翻译层）；后四个**只有服务端会写**：
//   - pending：下发时插入，等设备上线推进；
//   - succeeded：hello 报回的版本等于目标版本时置上（成功的唯一裁决点）；
//   - timeout：巡检兜底（15 分钟无动静）；
//   - superseded：被新一次下发取代（一台设备同时至多一条未终结，见设计 §5.4）。
//
// **没有从设备来的 succeeded**：进程内自报成功不可信，协议白名单里也没有它。
const (
	AttemptStatePending     = "pending"
	AttemptStateDownloading = "downloading"
	AttemptStateVerifying   = "verifying"
	AttemptStateInstalling  = "installing"
	AttemptStateRestarting  = "restarting"
	AttemptStateFailed      = "failed"
	AttemptStateRolledBack  = "rolled_back"
	AttemptStateSucceeded   = "succeeded"
	AttemptStateTimeout     = "timeout"
	AttemptStateSuperseded  = "superseded"
)

// attemptTerminalStates 是终态集合（`finished_at` 非空的那些）。
var attemptTerminalStates = map[string]bool{
	AttemptStateFailed:     true,
	AttemptStateRolledBack: true,
	AttemptStateSucceeded:  true,
	AttemptStateTimeout:    true,
	AttemptStateSuperseded: true,
}

// IsAttemptTerminal 报告某个状态是否是终态。
// 「一台设备同时至多一条未终结尝试」这条不变式靠它表达。
func IsAttemptTerminal(state string) bool { return attemptTerminalStates[state] }

// AttemptTerminalStates 返回终态清单。
// 仓储侧要用它做 `state NOT IN (...)`（取未终结行、巡检统计），
// 因此它必须与上面的 map 同源 —— 两处各写一份清单必然漂移。
func AttemptTerminalStates() []string {
	out := make([]string, 0, len(attemptTerminalStates))
	for s := range attemptTerminalStates {
		out = append(out, s)
	}
	return out
}

// 服务端推导的终态原因码（设备永不上报这三个）。
//
// 它们不进协议白名单（uni_protocol.upgradeReasonCodes）：那会让「agent 能不能发它」
// 这个问题的答案变含混；但它们必须和协议码一起出现在展示层的翻译表里。
const (
	// AttemptReasonTimeout 巡检判定「已开始但长时间没动静」。
	AttemptReasonTimeout = "timeout"
	// AttemptReasonSuperseded 被新一次下发取代。
	AttemptReasonSuperseded = "superseded"
	// AttemptReasonUnexpectedVersion hello 报回的版本既不是目标也不是原版本
	// （例如有人手工在设备上换了二进制）。
	AttemptReasonUnexpectedVersion = "unexpected_version"
)

// AgentUpgradeTask 是一次下发。**不存冗余计数**（已达成几台、失败几台）：
// 计数读时按明细聚合 —— 明细只有 10~100 行，而增量计数是最容易跟明细对不上的代码。
type AgentUpgradeTask struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"          json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"          json:"updatedAt"`

	TargetVersion string `gorm:"column:target_version;size:32;not null" json:"targetVersion"`
	Source        string `gorm:"column:source;size:16;not null" json:"source"`
	// Actor 是发起人用户名**快照**（不是外键）：升级记录要能回答「谁在什么时候
	// 做了什么」，而用户名会改、用户会被删 —— 联表取当前用户名会让历史记录
	// 随时间变形，甚至变成空白。
	Actor string `gorm:"column:actor;size:64" json:"actor"`
	// Total 是下发时快照的命中台数。它不等于明细行数（明细还会随重试增长），
	// 但它是「这次操作影响面」的原始事实。
	Total int `gorm:"column:total;not null;default:0" json:"total"`
	// FinishedAt 仅在「无进行中且无等待」时写入；**不做自动超时关闭** ——
	// 声明式目标对离线设备仍然生效，「未完成」是事实而不是卡住。
	FinishedAt *time.Time `gorm:"column:finished_at" json:"finishedAt"`
	Remark     string     `gorm:"column:remark;size:255" json:"remark"`
}

func (AgentUpgradeTask) TableName() string { return TableNameAgentUpgradeTask }

// AgentUpgradeAttempt 是一台设备的一次升级尝试（任务明细 / 设备升级记录）。
//
// 不嵌 BaseEntity（与 AgentPartitionLog 同理）：它是**流水**——
//   - 没有软删：删一条尝试记录不是任何业务动作，只是销毁证据；
//   - 没有 created_by/updated_by：发起人记在任务上；这里的时间戳各自有明确含义
//     （started_at / last_report_at / finished_at），比 created_at/updated_at 更准确。
//
// **必须有名为 ID 的字段**（雪花回调，见 DeviceResource 的注释）。
type AgentUpgradeAttempt struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"          json:"createdAt"`

	// TaskID 可空：设备侧**本地自动回滚**是自愈，不属于任何一次下发。
	TaskID *uint64 `gorm:"column:task_id;index:idx_attempt_task_state,priority:1" json:"taskId,string"`
	// DeviceID 不加外键：设备被软删后尝试记录**保留**（审计），
	// 列表 join 不到设备名时显示「设备已删除」，而不是过滤掉。
	DeviceID uint64 `gorm:"column:device_id;not null;index:idx_attempt_device_created,priority:1;uniqueIndex:uk_attempt_request,priority:1" json:"deviceId,string"`

	FromVersion string `gorm:"column:from_version;size:32" json:"fromVersion"`
	ToVersion   string `gorm:"column:to_version;size:32;not null" json:"toVersion"`
	// RequestID 与协议指令里的 request_id 一致：它是**行标识**，hello_ack 与催办
	// 复用同一行（重连对账不算新尝试）。唯一索引 (device_id, request_id) 兼防重放。
	RequestID string `gorm:"column:request_id;size:32;not null;uniqueIndex:uk_attempt_request,priority:2" json:"requestId"`

	State string `gorm:"column:state;size:16;not null;index:idx_attempt_task_state,priority:2" json:"state"`
	// Progress 只在下钻阶段有值（协议层已钉死），指针以区分「0%」与「没有百分比」。
	Progress *int `gorm:"column:progress" json:"progress"`
	// ReasonCode 是机器码（协议白名单 9 个 + 服务端推导 3 个），展示层翻译；
	// ReasonDetail 是排障细节（HTTP 码、stderr 首行），落库但**不渲染**。
	ReasonCode   string `gorm:"column:reason_code;size:32" json:"reasonCode"`
	ReasonDetail string `gorm:"column:reason_detail;size:255" json:"-"`

	StartedAt    *time.Time `gorm:"column:started_at" json:"startedAt"`
	LastReportAt *time.Time `gorm:"column:last_report_at" json:"lastReportAt"`
	FinishedAt   *time.Time `gorm:"column:finished_at" json:"finishedAt"`
}

func (AgentUpgradeAttempt) TableName() string { return TableNameAgentUpgradeAttempt }
