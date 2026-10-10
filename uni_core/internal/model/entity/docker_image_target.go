package entity

// DockerImageTarget 是**声明式目标版本**的存储行（Wave 4c）：为某个 repoTag
// 声明一个目标内容摘要（digest），漂移视图据此算出「哪些主机还没对齐」。
//
// 产品语义：舰队分发 = 声明式目标版本，蓝本是同产品 device 侧的「全站目标版本 +
// 待对齐 N 台」。docker 侧的对应物就是本表 —— **只做声明与待对齐（读+写目标）**，
// 分发执行由前端复用既有指令通道（不在本表职责内）。
//
// 三条口径：
//   - **repoTag 是唯一键**：一个标签只能有一个目标（声明是 upsert/覆盖语义；
//     两个目标并存会让「对没对齐」失去唯一答案）。repo_tag 定宽 255 —— 与标签
//     校验上限同档，按键寻址与展示都不需要更大。
//   - **targetDigest 存内容摘要**：漂移视图里的 digest 是 docker 的镜像 ID
//     （`sha256:<hex>`，协议 DockerImage.ID）。存同一个东西,比对才是纯字符串相等。
//   - **无软删语义的删除**：清除目标 = 硬删（Unscoped）。声明不是审计资产，
//     墓碑只会让「重新声明同一 repoTag」撞唯一索引 —— 而「清掉再重设」本该夺回
//     同一把键（与 docker_registry_credential 的删除纪律同款）。
//
// 时间戳与 updatedBy 走 BaseEntity：审计回调（database.Callbacks）从请求上下文
// 自动填 created_by/updated_by，本实体不手写这两列。
type DockerImageTarget struct {
	BaseEntity
	// RepoTag 是镜像引用（如 mysql:8.0.22），唯一键 —— 声明是「按标签覆盖」。
	RepoTag string `gorm:"column:repo_tag;size:255;not null;uniqueIndex" json:"repoTag"`
	// TargetDigest 是声明的目标内容摘要（`sha256:<hex>`，与 docker 镜像 ID 同格式）。
	TargetDigest string `gorm:"column:target_digest;size:128;not null" json:"targetDigest"`
}

// TableName 见 entity 包约定：表名不带 sys_ 前缀（业务域实体）。
func (DockerImageTarget) TableName() string { return "docker_image_target" }