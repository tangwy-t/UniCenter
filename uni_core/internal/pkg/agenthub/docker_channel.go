package agenthub

import (
	"context"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）─────────────────────────

// DockerStateIngestor 落一帧 docker 快照（由 service.AgentIngestService 实现）。
type DockerStateIngestor interface {
	SaveDockerState(ctx context.Context, deviceID uint64, st *agentproto.DockerState) error
}

// DockerCmdCompleter 回写一条指令的结果（由 service.AgentIngestService 实现）。
type DockerCmdCompleter interface {
	CompleteDockerCmd(ctx context.Context, deviceID uint64, res *agentproto.DockerCmdResult) error
}

// DockerConfigProvider 给出 hello_ack 里下发的 docker 配置块（由 service.DockerConfigProvider 实现）。
type DockerConfigProvider interface {
	DockerConfig(ctx context.Context) *agentproto.DockerConfig
}

// DockerFrameDeliverer 把一条流数据帧交付给对应的会话（由 dockerstream.Registry 实现）。
//
// 返回错误只用于日志口径（会话不存在/已结束是取消后的正常时序），**不影响连接存活**：
// docker 是搭便车能力，丢弃一帧的代价远小于断连（断连连累指标、心跳、升级）。
type DockerFrameDeliverer interface {
	DeliverDockerFrame(ctx context.Context, deviceID uint64, f *agentproto.DockerFrame) error
}

// ── 三个分派处理 ────────────────────────────────────────────────────────
//
// 共同的错误取向：docker 的载荷问题**只丢弃该帧、不断连**（与本文件里指标/升级的
// 处理相反）。理由：docker 是搭在指标通道上的**搭便车能力** —— 断连会让指标、心跳、
// 升级全部陪葬，而 docker 数据少一帧的代价只是「陈旧度多 30 秒」。
// 被丢弃的指令由服务端 sweep 终结为 timeout，是可观测的诚实结果。

// handleDockerState 落一帧资源快照。
func (c *Conn) handleDockerState(ctx context.Context, m *agentproto.Message) bool {
	decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirAgentToCore)
	if err != nil {
		c.log.Warn("docker state rejected (payload)",
			zap.Uint64("device_id", c.deviceID()), zap.Error(err))
		return true
	}
	st, ok := decoded.(*agentproto.DockerState)
	if !ok {
		c.log.Warn("docker state payload type mismatch", zap.Uint64("device_id", c.deviceID()))
		return true
	}
	if c.deps.DockerState == nil {
		c.log.Debug("agenthub: docker 存储未装配，快照被丢弃", zap.Uint64("device_id", c.deviceID()))
		return true
	}
	if err := c.deps.DockerState.SaveDockerState(ctx, c.deviceID(), st); err != nil {
		c.log.Warn("docker state not persisted",
			zap.Uint64("device_id", c.deviceID()), zap.Error(err))
	}
	return true
}

// handleDockerResult 回写指令结果。
func (c *Conn) handleDockerResult(ctx context.Context, m *agentproto.Message) bool {
	decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirAgentToCore)
	if err != nil {
		c.log.Warn("docker result rejected (payload)",
			zap.Uint64("device_id", c.deviceID()), zap.Error(err))
		return true
	}
	res, ok := decoded.(*agentproto.DockerCmdResult)
	if !ok {
		c.log.Warn("docker result payload type mismatch", zap.Uint64("device_id", c.deviceID()))
		return true
	}
	if c.deps.DockerResult == nil {
		c.log.Debug("agenthub: docker 指令存储未装配，结果被丢弃", zap.Uint64("device_id", c.deviceID()))
		return true
	}
	if err := c.deps.DockerResult.CompleteDockerCmd(ctx, c.deviceID(), res); err != nil {
		c.log.Warn("docker result not persisted",
			zap.Uint64("device_id", c.deviceID()), zap.String("ref", res.Ref), zap.Error(err))
	}
	return true
}

// handleDockerFrame 处理流数据帧：投递给会话注册表（三期接线）。
//
// 未装配（DockerFrames == nil）时与三期之前一致地丢弃并记 Debug：docker 域是
// 可选能力，装配缺失不得影响指标主链路 —— 与另外三个 docker 依赖同一取向。
func (c *Conn) handleDockerFrame(ctx context.Context, m *agentproto.Message) bool {
	decoded, err := agentproto.DecodeTypedFor(m, agentproto.DirAgentToCore)
	if err != nil {
		c.log.Warn("docker frame rejected (payload)",
			zap.Uint64("device_id", c.deviceID()), zap.Error(err))
		return true
	}
	f, ok := decoded.(*agentproto.DockerFrame)
	if !ok {
		return true
	}
	if c.deps.DockerFrames == nil {
		c.log.Debug("agenthub: docker 流会话未装配，帧被丢弃",
			zap.Uint64("device_id", c.deviceID()), zap.String("session", f.SessionID), zap.Uint64("seq", f.Seq))
		return true
	}
	if err := c.deps.DockerFrames.DeliverDockerFrame(ctx, c.deviceID(), f); err != nil {
		// 会话不存在/已结束（取消、超时、eof 后的迟到帧）是正常时序：Debug 即可。
		// 归属不符等异常由投递方 Warn（它掌握 device/session 两侧的 id）。
		c.log.Debug("docker frame not delivered",
			zap.Uint64("device_id", c.deviceID()), zap.String("session", f.SessionID),
			zap.Uint64("seq", f.Seq), zap.Error(err))
	}
	return true
}
