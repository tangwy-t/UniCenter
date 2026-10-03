package entity

// DockerTaskHistory 是任务中心的**持久层**条目（8d）：一条指令的终态照实落库一份。
//
// 为什么要有这张表：任务面在此之前只有 Redis 一条腿 —— CmdStore 的终态记录 TTL
// 10 分钟（pending 兜底 2 小时），任务中心的「历史」实际只是「最近一次窗口」，
// 半小时后再打开页面，昨天那场失败的拉取已经无从追起。本表把终态任务的读面从
// 「易失窗口」升级为「可追历史」：实时面（CmdStore，含在途）与历史面（本表）在
// 读面按 ref 合并，实时胜、历史兜底（service/docker_tasks.go 的 Tasks）。
//
// 四条口径（与表结构一一对应）：
//   - **只记终态，不记在途**：在途任务的存在形态是 CmdStore（pending 记录 + 进度
//     会话），落库发生在终态转换的那一刻（dockerstate.CmdStore 的终态钩子）。
//     在途不回填 —— 本功能启用之前的历史记不了，不编造过去；已存在的在途任务
//     照常跑完，结果回来时经同一条钩子落库。
//   - **不是审计**：审计（sys_operation_log）记的是「谁执行了什么」且只有事实
//     （agent 的 result）入账；本表是任务中心的读面存储，照实镜像任务列表会显示
//     的每一种终态 —— 含 sweep 的服务端推断 timeout（页面上本来就有这一档，
//     只是审计不入账）。
//   - **只记「任务」，不记读面**：只读动作（inspect 档）与 docker:events 常驻订阅
//     （UserID=0）不落库 —— 与任务列表的读面剔除同一把尺子（dockerTaskReadOnly），
//     否则每次打开容器详情页都会往这张表里塞一行。
//   - **主机名/用户名不存快照**：读面照旧联查设备表/用户表（与实时条目同一条路径）
//     —— 存快照会让「历史行」与「实时行」在设备改名/删号后说两句不同的话，而
//     hostId 才是那条可导航的主键。
type DockerTaskHistory struct {
	BaseEntity
	// Ref 是指令号（唯一键）：与 Redis 的 docker:cmd:<ref> 同一把钥匙，合并读面按
	// 它去重；唯一约束让「迟到的 result 覆盖 timeout」「重复 result 重放」都收敛到
	// 同一行（Upsert），历史与实时记录逐字段一致。
	Ref string `gorm:"column:ref;size:64;not null;uniqueIndex" json:"ref"`
	// DeviceID 是目标主机（读面联查主机名；设备已删时条目仍在，hostId 可导航）。
	DeviceID uint64 `gorm:"column:device_id;not null;index:idx_dth_device_accepted,priority:1" json:"deviceId,string"`
	// Action 是动作码（任务列表的动作列；过滤与前端「可流/可取消」判定都读它）。
	Action string `gorm:"column:action;size:64;not null" json:"action"`
	// Target 是操作目标（镜像名/容器名/项目名…）。text 而不是定宽 varchar：
	// 目标长度没有上游契约（受理侧不设上限），定宽列会在超长目标上把落库打成
	// 「数据太长」错误；写入侧按 dockerTaskTextMax 截断兜底（行宽有界）。
	Target string `gorm:"column:target;type:text;not null" json:"target"`
	// UserID 是发起人（读面联查用户名；用户已删时降级为空名，条目不消失）。
	UserID uint64 `gorm:"column:user_id;not null" json:"userId,string"`
	// Status 是终态照实投影：succeeded / failed / timeout（本表不存 pending —— 见
	// 「只记终态」）。取消没有独立状态码，由 summary 那句话承载（与实时面同款）。
	Status string `gorm:"column:status;size:16;not null" json:"status"`
	// Summary 是终态结论句（与实时条目**同一句话**：service.terminalSummary 的产物，
	// 写入时定稿）—— 读面直接透出，不再重算（重算的口径只该有一处）。
	Summary string `gorm:"column:summary;type:text" json:"summary"`
	// AcceptedAt 是受理时刻（unix 毫秒，与指令记录同单位）：列表排序键与保留策略
	// 的判龄依据（idx_dth_accepted）。
	AcceptedAt int64 `gorm:"column:accepted_at;not null;index:idx_dth_accepted;index:idx_dth_device_accepted,priority:2" json:"acceptedAt"`
	// FinishedAt 是终态时刻（unix 毫秒；与受理时刻之差即耗时 —— 与审计的 CostTime
	// 同一算法）。
	FinishedAt int64 `gorm:"column:finished_at;not null" json:"finishedAt"`
}

func (DockerTaskHistory) TableName() string { return "docker_task_history" }
