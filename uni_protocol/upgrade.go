package agentproto

import "strings"

// ── 升级：服务端下发与设备上报 ────────────────────────────────────────────
//
// 两条消息构成一个闭环，方向相反、以 request_id 关联：
//
//	core.agent.upgrade    服务端 → 设备：现在升到 X（或催办：你该对账了）
//	agent.upgrade.status  设备 → 服务端：我走到哪一步了 / 我失败了 / 我回滚了
//
// **没有 succeeded 状态**：进程内自报成功不可信 —— 它可能刚自报完就在替换后
// 崩掉或连不上。成功由服务端在设备**下一次 hello** 里用版本号裁决。

// UpgradeDirective 是一次升级指令。
//
// 刻意**不含下载 URL**：设备由自己的 -url（ws://host:port/api/v1/agent/ws）推导同源
// http(s) 地址即可。服务端因此不必知道自己的对外地址 —— 那是个在反代后面往往
// 说不清的东西（容器内地址、宿主地址、对外域名可能是三个答案，而设备只会连其中一个）。
type UpgradeDirective struct {
	// RequestID 是服务端生成的十进制 id，串起「指令 → 状态上报 → 记录行」。
	//
	// 它是**行标识**而不是「每次下发都换新」的序号：声明式模型下 hello_ack 每次
	// 重连都会带指令，若每次都换新 id，一次网络闪断就会在任务里冒出一行「新尝试」。
	// 复用规则（服务端保证）：同一设备同一目标只有一条未终结的尝试，重连对账
	// 复用它的 id。
	RequestID string `json:"request_id"`
	// TargetVersion 是目标版本（合法 semver，但允许低于当前版本 —— 那就是回滚）。
	TargetVersion string `json:"target_version"`
	// SHA256 是产物的 64 位小写 hex 摘要，设备**必须**校验后才替换二进制。
	SHA256 string `json:"sha256"`
	// SizeBytes 是产物字节数：既是下载上限（防止下到一半被塞进无限流），
	// 也是下载进度的分母。
	SizeBytes int64 `json:"size_bytes"`
}

// Validate 校验升级指令。
func (d *UpgradeDirective) Validate() error {
	if !isDecimalID(d.RequestID) {
		return decodeErr(StagePayload, "request_id", ErrMissingField)
	}
	if !IsSemver(d.TargetVersion) {
		return decodeErr(StagePayload, "target_version", ErrInvalidPayload)
	}
	if !IsSHA256Hex(d.SHA256) {
		return decodeErr(StagePayload, "sha256", ErrInvalidPayload)
	}
	if d.SizeBytes <= 0 {
		return decodeErr(StagePayload, "size_bytes", ErrInvalidPayload)
	}
	return nil
}

// IsSHA256Hex 报告 s 是否是 64 位小写 hex 的 sha256 摘要。
//
// 只接受小写：摘要由服务端计算与下发，两端不该对大小写有不同的理解 ——
// 允许大写会让「同一份产物在两边算出不同字符串」这种事有机会发生。
func IsSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

// 设备在升级过程中上报的阶段。终态只有 failed / rolled_back ——
// 成功不在其中（见文件头注释）。
const (
	// UpgradeStateDownloading 正在下载产物；**只有这个阶段带 Progress**。
	UpgradeStateDownloading = "downloading"
	// UpgradeStateVerifying 正在校验摘要 / 做替换前的冒烟自检。
	UpgradeStateVerifying = "verifying"
	// UpgradeStateInstalling 正在替换二进制文件（毫秒级）。
	UpgradeStateInstalling = "installing"
	// UpgradeStateRestarting 已替换完成，即将（或已经）重启进程。
	UpgradeStateRestarting = "restarting"
	// UpgradeStateFailed 本次尝试失败，设备仍在旧版本上运行。
	UpgradeStateFailed = "failed"
	// UpgradeStateRolledBack 新版本启动后未能连上，设备已自动回滚到旧版本。
	UpgradeStateRolledBack = "rolled_back"
)

// upgradeStates 是状态白名单（顺序 = 流水线顺序，末两个是终态）。
var upgradeStates = []string{
	UpgradeStateDownloading, UpgradeStateVerifying, UpgradeStateInstalling,
	UpgradeStateRestarting, UpgradeStateFailed, UpgradeStateRolledBack,
}

// AllUpgradeStates 返回全部合法状态（供守卫测试与展示层枚举）。
func AllUpgradeStates() []string {
	return append([]string(nil), upgradeStates...)
}

// IsUpgradeState 报告 s 是否在状态白名单内。
func IsUpgradeState(s string) bool {
	for _, v := range upgradeStates {
		if v == s {
			return true
		}
	}
	return false
}

// 失败/回滚的原因码。**受限枚举**，展示层翻译成结论式中文 ——
// 自由文本会被各种实现写花，而「页面只讲结论」是本仓库已立的纪律。
//
// 这里只放**设备会上报**的码。服务端推导的终态原因（巡检超时、被新下发取代、
// 版本与目标无关）不进协议白名单：它们从不出现在设备上报里，放进白名单只会
// 让「agent 能不能发它」这个问题的答案变得含混。
const (
	// ReasonDownloadFailed 下载失败（网络不通、端点缺失、超时）。
	ReasonDownloadFailed = "download_failed"
	// ReasonChecksumMismatch 摘要不符 —— 产物损坏或中间被改写。
	ReasonChecksumMismatch = "checksum_mismatch"
	// ReasonSmokeTestFailed 冒烟自检不过（跑不起来、自述版本不符）。
	ReasonSmokeTestFailed = "smoke_test_failed"
	// ReasonNoWritePermission 设备上的二进制目录不可写（非 root / 只读挂载）。
	ReasonNoWritePermission = "no_write_permission"
	// ReasonReplaceFailed 原子替换失败（rename 失败、备份失败）。
	ReasonReplaceFailed = "replace_failed"
	// ReasonExecFailed 自重启失败（exec 返回错误）。
	ReasonExecFailed = "exec_failed"
	// ReasonNotConnectedAfterUpgrade 试用期内未能连上服务端（含探测后仍失败）→ 本地自动回滚。
	ReasonNotConnectedAfterUpgrade = "not_connected_after_upgrade"
	// ReasonPlatformUnsupported 设备平台不支持自替换（例如 Windows 上无法覆盖运行中的 exe）。
	ReasonPlatformUnsupported = "platform_unsupported"
	// ReasonArtifactMissing 服务端没有该设备平台的产物（正常不会下发到设备，故这是兜底）。
	ReasonArtifactMissing = "artifact_missing"
)

var upgradeReasonCodes = []string{
	ReasonDownloadFailed, ReasonChecksumMismatch, ReasonSmokeTestFailed,
	ReasonNoWritePermission, ReasonReplaceFailed, ReasonExecFailed,
	ReasonNotConnectedAfterUpgrade, ReasonPlatformUnsupported, ReasonArtifactMissing,
}

// AllUpgradeReasonCodes 返回全部合法的设备上报原因码。
func AllUpgradeReasonCodes() []string {
	return append([]string(nil), upgradeReasonCodes...)
}

// IsUpgradeReasonCode 报告 s 是否在（设备上报的）原因码白名单内。
func IsUpgradeReasonCode(s string) bool {
	for _, v := range upgradeReasonCodes {
		if v == s {
			return true
		}
	}
	return false
}

// UpgradeStatus 是设备对一次升级尝试的状态汇报。
type UpgradeStatus struct {
	RequestID     string `json:"request_id"`
	State         string `json:"state"`
	TargetVersion string `json:"target_version"`
	FromVersion   string `json:"from_version,omitempty"`
	// Progress 是**下载阶段**的真实字节百分比（0–100）。
	// 指针 + omitempty 表示「本阶段没有百分比」—— 这与「0%」是可以区分的两件事。
	Progress *int `json:"progress,omitempty"`
	// ReasonCode 是受限枚举（failed / rolled_back 必填）。
	ReasonCode string `json:"reason_code,omitempty"`
	// ReasonDetail 是给人排障看的原始细节（HTTP 码、stderr 片段）。
	// 落库但**不渲染**：页面只讲结论。
	ReasonDetail string `json:"reason_detail,omitempty"`
}

// Validate 校验状态汇报。
//
// 三条硬约束值得留意：
//  1. **Progress 只允许出现在 downloading 阶段** —— 「进度只在下载阶段」是进度三层的
//     底层约定；在协议层钉住它，是为了让「假百分比」在物理上无法被上报（校验 /
//     替换 / exec 是毫秒级且没有天然刻度，任何权重都是编的）。
//  2. failed / rolled_back **必须**带原因码 —— 否则运维只看到「失败了」而无从处置，
//     这正是「失败必须汇报上来」要防的事。
//  3. **中间阶段不得带原因码** —— 那会让页面出现「下载中（原因：校验不过）」这种
//     自相矛盾的行；它说明上报方把「某种异常」与「失败」混为一谈了。
func (s *UpgradeStatus) Validate() error {
	if !isDecimalID(s.RequestID) {
		return decodeErr(StagePayload, "request_id", ErrMissingField)
	}
	if !IsUpgradeState(s.State) {
		return decodeErr(StagePayload, "state", ErrInvalidPayload)
	}
	if !IsSemver(s.TargetVersion) {
		return decodeErr(StagePayload, "target_version", ErrInvalidPayload)
	}
	if s.FromVersion != "" && !IsSemver(s.FromVersion) {
		return decodeErr(StagePayload, "from_version", ErrInvalidPayload)
	}
	if s.Progress != nil {
		if s.State != UpgradeStateDownloading {
			return decodeErr(StagePayload, "progress", ErrInvalidPayload)
		}
		if *s.Progress < 0 || *s.Progress > 100 {
			return decodeErr(StagePayload, "progress", ErrInvalidPayload)
		}
	}
	if s.ReasonCode != "" && !IsUpgradeReasonCode(s.ReasonCode) {
		return decodeErr(StagePayload, "reason_code", ErrInvalidPayload)
	}
	switch s.State {
	case UpgradeStateFailed, UpgradeStateRolledBack:
		if s.ReasonCode == "" {
			return decodeErr(StagePayload, "reason_code", ErrMissingField)
		}
	default:
		if s.ReasonCode != "" {
			return decodeErr(StagePayload, "reason_code", ErrInvalidPayload)
		}
	}
	return nil
}

// NormalizeReasonDetail 截断细节文本。
//
// 上限定在 255 字节：它只是排障线索（HTTP 码、stderr 首行），而不是日志通道 ——
// 把整段 stderr 塞进协议消息会挤掉同一帧里的有效载荷，也会让它悄悄长成第二份日志。
func NormalizeReasonDetail(s string) string {
	s = strings.TrimSpace(s)
	const max = 255
	if len(s) <= max {
		return s
	}
	return s[:max]
}
