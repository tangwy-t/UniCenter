package agentproto

// Heartbeat 是 agent 在无指标变化时的兜底心跳。
// 载荷固定为空对象：RTT 由 WebSocket ping/pong 承担，不往心跳里塞字段。
type Heartbeat struct{}
