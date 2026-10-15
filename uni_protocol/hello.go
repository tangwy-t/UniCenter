package agentproto

// Hello 是 agent 建立连接后的首条消息：注册（带 enroll_token）或鉴权（带 agent_token）。
type Hello struct {
	InstanceID   string  `json:"instance_id"`
	Hostname     string  `json:"hostname"`
	OS           string  `json:"os"`
	Arch         string  `json:"arch"`
	AgentVersion string  `json:"agent_version"`
	Kernel       string  `json:"kernel,omitempty"`
	EnrollToken  string  `json:"enroll_token,omitempty"`
	AgentToken   string  `json:"agent_token,omitempty"`
	Platform     string  `json:"platform,omitempty"`
	PlatformVer  string  `json:"platform_ver,omitempty"`
	CPUModel     string  `json:"cpu_model,omitempty"`
	CPUCores     int     `json:"cpu_cores,omitempty"`
	MemTotalMB   float64 `json:"mem_total_mb,omitempty"`
	BootTime     int64   `json:"boot_time,omitempty"`
	// UpgradeSupported 表示「我这一版带升级运行时，可以被远程升级」。
	//
	// 为什么必须由设备自报而不是服务端按版本号猜：现场存量的 0.1.0 没有任何升级
	// 能力，对着它下发目标版本会**毫无反应**（它不认识新字段与新消息）。有了这个
	// 自报位，控制台可以据此禁用按钮并给出结论式提示，而不是让运维对着一个
	// 「点了没动静」的界面猜原因。老 agent 不发该字段 → 零值 false，语义正确。
	UpgradeSupported bool `json:"upgrade_supported,omitempty"`
}

// CredentialKind 描述 Hello 携带的凭据种类。
type CredentialKind uint8

const (
	// CredentialNone 表示两种 token 都没有提供。
	CredentialNone CredentialKind = iota
	// CredentialEnroll 表示仅提供 enroll_token（首次注册）。
	CredentialEnroll
	// CredentialAgent 表示仅提供 agent_token（已注册设备重连鉴权）。
	CredentialAgent
)

// Credential 判定 Hello 的凭据种类。
// 两种 token 都缺失或同时存在都返回错误——歧义时不暗自取优先级。
func (h *Hello) Credential() (CredentialKind, error) {
	hasEnroll := h.EnrollToken != ""
	hasAgent := h.AgentToken != ""
	switch {
	case !hasEnroll && !hasAgent:
		return CredentialNone, decodeErr(StagePayload, "enroll_token,agent_token", ErrMissingField)
	case hasEnroll && hasAgent:
		return CredentialNone, decodeErr(StagePayload, "enroll_token,agent_token", ErrInvalidPayload)
	case hasEnroll:
		return CredentialEnroll, nil
	default:
		return CredentialAgent, nil
	}
}

// Validate 校验 Hello 的必填字段与取数量程。
func (h *Hello) Validate() error {
	required := []struct {
		field string
		value string
	}{
		{"instance_id", h.InstanceID},
		{"hostname", h.Hostname},
		{"os", h.OS},
		{"arch", h.Arch},
		{"agent_version", h.AgentVersion},
	}
	for _, r := range required {
		if r.value == "" {
			return decodeErr(StagePayload, r.field, ErrMissingField)
		}
	}
	if _, err := h.Credential(); err != nil {
		return err
	}
	if h.CPUCores < 0 {
		return decodeErr(StagePayload, "cpu_cores", ErrInvalidPayload)
	}
	if h.MemTotalMB < 0 {
		return decodeErr(StagePayload, "mem_total_mb", ErrInvalidPayload)
	}
	if h.BootTime < 0 {
		return decodeErr(StagePayload, "boot_time", ErrInvalidPayload)
	}
	return nil
}

// HelloAck 是 core 对 Hello 的应答。
type HelloAck struct {
	Accepted       bool   `json:"accepted"`
	DeviceID       string `json:"device_id,omitempty"`
	AgentToken     string `json:"agent_token,omitempty"`
	ReportInterval int    `json:"report_interval,omitempty"`
	RejectReason   string `json:"reject_reason,omitempty"`
	ServerTime     int64  `json:"server_time,omitempty"`
	V              int    `json:"v,omitempty"`
	// Upgrade 是本次握手携带的升级指令（无目标版本或该平台无产物时为 nil）。
	//
	// 放在 hello_ack 里而不是只靠 core.agent.upgrade 推送，是声明式模型的关键：
	// hello_ack 是**每次重连都会到达**的那一帧，于是离线设备一上线就自动对账，
	// 不需要服务端记得「哪些设备还没收到」。催办消息只是让在线设备不必等下一次重连。
	Upgrade *UpgradeDirective `json:"upgrade,omitempty"`
}

// Validate 校验 HelloAck：accepted=true 必须有 device_id 且 report_interval 满足下限；
// accepted=false 必须带 reject_reason。
func (a *HelloAck) Validate() error {
	// report_interval 是字段级约束：非 0 时必须 ≥2，与 accepted 取值无关。
	if a.ReportInterval != 0 && a.ReportInterval < 2 {
		return decodeErr(StagePayload, "report_interval", ErrInvalidPayload)
	}
	if a.Accepted {
		if a.DeviceID == "" {
			return decodeErr(StagePayload, "device_id", ErrMissingField)
		}
		// 指令只在「被接受」时有意义，故只在接受分支校验。
		if a.Upgrade != nil {
			return a.Upgrade.Validate()
		}
		return nil
	}
	// 被拒的连接里带升级指令是构造错误：agent 拿它没用（连都没连上），
	// 而放任它会让人误以为「拒绝了但升级照样开始了」。
	if a.Upgrade != nil {
		return decodeErr(StagePayload, "upgrade", ErrInvalidPayload)
	}
	if a.RejectReason == "" {
		return decodeErr(StagePayload, "reject_reason", ErrMissingField)
	}
	return nil
}
