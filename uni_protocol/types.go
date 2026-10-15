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
