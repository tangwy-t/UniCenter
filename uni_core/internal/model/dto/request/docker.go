package request

import "encoding/json"

// DockerCmdReq 是一条 docker 指令的受理请求（形状对齐 spec §4.1）。
//
// target 是顶层字段（与 HTTP 契约一致），而 core 组装协议载荷时把它并进 options ——
// 协议侧 target 属于 options（§4.3.1 总表是 options 归属的唯一事实源）。
type DockerCmdReq struct {
	Action  string               `json:"action" binding:"required"`
	Target  string               `json:"target"`
	Options *DockerCmdOptionsReq `json:"options"`
	// Confirm 是强确认档的确认值（服务端强制校验，前端 MessageBox 只是它的输入界面）。
	Confirm string `json:"confirm"`
}

// DockerCmdOptionsReq 是 options 的**可选参数集**。
//
// 指针用于布尔与数值：`all=false` 与「没传 all」在语义上不同（前者显式要求「只清悬空」，
// 后者用默认值）—— 用值类型会让「显式 false」被默认值悄悄覆盖。
type DockerCmdOptionsReq struct {
	Tail          *int            `json:"tail"`
	Since         *int64          `json:"since"`
	N             *int            `json:"n"`
	Force         *bool           `json:"force"`
	Filename      string          `json:"filename"`
	Overwrite     *bool           `json:"overwrite"`
	All           *bool           `json:"all"`
	RemoveOrphans *bool           `json:"removeOrphans"`
	Volumes       *bool           `json:"volumes"`
	Content       string          `json:"content"`
	BaseHash      string          `json:"baseHash"`
	Src           string          `json:"src"`
	Dst           string          `json:"dst"`
	Patch         json.RawMessage `json:"patch"`
	// Follow 是 container:logs 的流式开关（三期）：true 时 agent 建日志流会话，
	// 结果回 session_id，数据走 /cmds/:ref/stream。缺省/显式 false = 一次性取。
	Follow *bool `json:"follow"`
	// Command 是 container:exec 的 argv（三期）；缺省由协议定（["/bin/sh"]）。
	// 校验在协议层（≤32 项、每项 ≤256B、不得含 NUL/换行），core 不重复实现。
	Command []string `json:"command"`
	// Backup 是 compose.file:write 的**回滚模式**（四期）：值是备份令牌
	// （`YYYYMMDD-HHMMSS`），带它则不需要 content。**协议上永远不出现路径** ——
	// agent 用令牌自行重建 `<配置文件>.bak-<令牌>`（§8 路径白名单纪律）。
	Backup string `json:"backup"`
	// ── 四支柱·创建面（container:create 专属，4a）─────────────────────────
	// 逐字段白名单校验在协议层，core 只负责透传；这里照既有纪律选指针/值类型：
	// start 必须区分「没传」（默认 true）与「显式 false」（只创建不启动）。
	Image         string   `json:"image"`
	Name          string   `json:"name"`
	Ports         []string `json:"ports"`
	Env           []string `json:"env"`
	Mounts        []string `json:"mounts"`
	RestartPolicy string   `json:"restartPolicy"`
	CPULimit      float64  `json:"cpuLimit"`
	MemLimitMB    int      `json:"memLimitMb"`
	Network       string   `json:"network"`
	Start         *bool    `json:"start"`
	// Registry 是私有仓库凭据的键（4c）：image:pull 可选 —— 填了就用凭据库
	// 解出的凭据拉取（密码由 core 受理时注入,本字段只是仓库地址、不含秘密）；
	// 不填与 4b 之前的拉取逐字一致。形态校验在协议层（IsDockerRegistryAddr）。
	Registry string `json:"registry"`
}

// DockerWorkloadQuery 是跨主机统一工作负载表（GET /docker/containers）的查询参数。
//
// 三个过滤都**可选**且相互独立（先主机、再状态、再关键字，逐层收窄）。
// 非法值返回 400 结论句而不是静默忽略 —— 静默忽略会让用户以为「筛了但没生效」
// （与 DeviceOverviewQuery.Ids 的取舍同一句话）。
type DockerWorkloadQuery struct {
	// HostID 限定单主机（兼容旧容器页按 host 切换的语义）。0 = 不过滤：
	// 设备 id 是雪花值永不为 0，0 与「未传」同形，无需指针区分。
	HostID uint64 `form:"hostId"`
	// State 过滤运行态：running / stopped。stopped 是「一切非 running」的统称
	// （exited/created/paused/dead…），与总览 KPI 的 Stopped=Total-Running 同一句。
	// 合法值在 service 校验（那里给出 400 结论句）。
	State string `form:"state"`
	// Keyword 是容器名或镜像名的子串匹配，大小写不敏感。
	Keyword string `form:"keyword"`
}

// DockerTasksQuery 是任务中心（GET /docker/tasks，6b）的查询参数。
//
// 三个过滤都**可选**且相互独立。status 只收 pending/done 两档：done = 一切终态
// （succeeded/failed/timeout —— 「pending 之外皆终态」与指令状态机同句），
// 任务中心不需要按具体终态筛选（条目上的 status 字段已经细分）；action 是动作码
// （如 image:pull），先在 service 与策略表对账（未登记即 400，与受理处同款）。
// 非法值 400 结论句（与 Workloads 同一句纪律）。
type DockerTasksQuery struct {
	// HostID 限定单主机。0 = 跨主机（缺省）。
	HostID uint64 `form:"hostId"`
	// Status 过滤阶段：pending（仍在执行）/ done（一切终态）。
	Status string `form:"status"`
	// Action 过滤动作码（如 image:pull、compose:up）。
	Action string `form:"action"`
}
