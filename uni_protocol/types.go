package agentproto

// 消息类型常量：契约层的唯一枚举源。改名即破坏性变更。
const (
	TypeAgentHello         = "agent.hello"
	TypeCoreHelloAck       = "core.hello_ack"
	TypeAgentHeartbeat     = "agent.heartbeat"
	TypeAgentReportMetrics = "agent.report.metrics"
	// TypeCoreAgentUpgrade 是升级指令（也用于「立即对账」的催办）。
	TypeCoreAgentUpgrade = "core.agent.upgrade"
	// TypeAgentUpgradeStatus 是设备对一次升级尝试的状态汇报。
	TypeAgentUpgradeStatus = "agent.upgrade.status"
	// ── Docker 管理（v1.2.0 新增）────────────────────────────────────────
	// TypeAgentDockerState 是资源快照（周期上报；Docker 不可达时也发，带 docker_ok=false）。
	TypeAgentDockerState = "agent.docker.state"
	// TypeCoreDockerCmd 是一条 Docker 操作指令（ref 关联）。
	TypeCoreDockerCmd = "core.docker.cmd"
	// TypeAgentDockerResult 是指令结果（ref 回关联）。
	TypeAgentDockerResult = "agent.docker.result"
	// TypeAgentDockerFrame 是流会话的数据帧（日志 chunk / PTY 输出共用）。
	TypeAgentDockerFrame = "agent.docker.frame"
	// TypeCoreDockerFrame 是流会话的控制帧（input / resize / cancel）。
	TypeCoreDockerFrame = "core.docker.frame"
)

// Direction 表示一条消息的允许方向。
// 零值为 DirUnknown，避免零值被误判为某个合法方向。
type Direction uint8

const (
	DirUnknown Direction = iota
	DirAgentToCore
	DirCoreToAgent
)

// Kind 表示一条消息是否携带载荷。
type Kind uint8

const (
	KindEmpty Kind = iota
	KindPayload
)

// MaxTypeLen 是 type 串的字节长度上限。
const MaxTypeLen = 64

// ValidTypeName 校验 type 串的字符集（小写字母/数字/点/下划线）与长度。
// 方向前缀约定为 agent.* 与 core.*，由注册表（Task 8）强制。
func ValidTypeName(t string) bool {
	if t == "" || len(t) > MaxTypeLen {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_':
		default:
			return false
		}
	}
	return true
}
