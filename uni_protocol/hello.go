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
		return nil
	}
	if a.RejectReason == "" {
		return decodeErr(StagePayload, "reject_reason", ErrMissingField)
	}
	return nil
}
