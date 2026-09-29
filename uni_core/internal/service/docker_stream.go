package service

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 域的**流通道面**（三期）：一次性 ticket 的签发与兑换、会话接入与释放、
// 下行控制帧（input/resize/cancel）与流会话的清理。
//
// 它与 docker_cmd.go 的分界沿用本域既有规矩：指令面管「受理 → 结果」，流通道面管
// 「会话建立之后的事」。两者共享同一个发送面（agenthub）与会话注册表。
type DockerStreamService struct {
	sessions *dockerstream.Registry
	tickets  *dockerstate.TicketStore
	sender   DockerCmdSender
	// online 报告设备当前是否在线（由 agenthub.Hub.Get 包装）。
	// nil = 跳过离线判定（测试/未装配）：仅少一层「设备掉线后会话尽快收摊」的自愈。
	online func(deviceID uint64) bool
	log    logger.LoggerInterface
	now    func() time.Time
}

// NewDockerStreamService 构造流通道面。
func NewDockerStreamService(sessions *dockerstream.Registry, tickets *dockerstate.TicketStore,
	sender DockerCmdSender, log logger.LoggerInterface) *DockerStreamService {
	return &DockerStreamService{sessions: sessions, tickets: tickets, sender: sender, log: log, now: time.Now}
}

// WithDeviceOnline 注入设备在线判定（sweep 用）。返回自身便于装配链式书写。
func (s *DockerStreamService) WithDeviceOnline(online func(deviceID uint64) bool) *DockerStreamService {
	s.online = online
	return s
}

// IssueTicket 为一条**已建立会话**的指令签发一张新的一次性票据。
//
// 契约（plan §0）：每次结果响应都带一张新票据（前端轮询会取多次，用最新那张）。
// 旧票据不会因此作废（各自 TTL 30s、各自单次使用）—— 让「刚拿到就被下一次轮询
// 顶掉」这种时序不可能发生。
func (s *DockerStreamService) IssueTicket(ctx context.Context, rec *dockerstate.CmdRecord) (string, error) {
	if rec == nil || rec.SessionID == "" {
		return "", nil
	}
	t, err := s.tickets.Issue(ctx, rec.UserID, rec.DeviceID, rec.SessionID)
	if err != nil {
		return "", err
	}
	return t.Ticket, nil
}

// RedeemTicket 消费一张票据（单次使用；不存在/已过期与消费失败在此区分）：
// 票据不存在返回 nil（handler 折成 401），Redis 故障返回 500 语义的错误。
func (s *DockerStreamService) RedeemTicket(ctx context.Context, ticket string) (*dockerstate.StreamTicket, error) {
	t, err := s.tickets.Consume(ctx, ticket)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	return t, nil
}

// AttachSession 把会话接给一个消费者（日志 NDJSON 与终端 WS 共用）。
//
// 两种调用点各自已经做过一层归属校验（日志：指令记录的发起人；终端：票据绑定的
// userId），这里再按会话登记的 userId 复核一次 —— 归属是三道闸，不是一句话。
func (s *DockerStreamService) AttachSession(sessionID string, userID, deviceID uint64) (*dockerstream.Session, error) {
	sess, err := s.sessions.Attach(sessionID, userID)
	if err != nil {
		switch {
		case errors.Is(err, dockerstream.ErrForbidden):
			return nil, apperror.Forbidden("无权接入该流会话")
		case errors.Is(err, dockerstream.ErrBusy):
			return nil, apperror.Conflict("该流会话已有连接")
		default:
			// 不存在 / 已结束 / 已清理：同码（404），不让探测者区分「从没有过」与「刚结束」。
			return nil, apperror.NotFound("流会话不存在或已结束")
		}
	}
	if deviceID != 0 && sess.DeviceID() != deviceID {
		// 路径上的主机与会话登记的设备不符：接入会接错机器。立刻释放（会话本身不动），
		// 结论与「不存在」同码 —— 细节只进服务端日志。
		s.sessions.Release(sessionID)
		if s.log != nil {
			s.log.Warn("docker stream attach device mismatch",
				zap.String("session", sessionID), zap.Uint64("pathDevice", deviceID),
				zap.Uint64("sessionDevice", sess.DeviceID()))
		}
		return nil, apperror.NotFound("流会话不存在或已结束")
	}
	return sess, nil
}

// ReleaseSession 解除接入占用（连接升级失败等「还没开始流」的路径）：
// 会话保留（agent 侧仍在跑），可凭下一次轮询签发的新票据重接。
func (s *DockerStreamService) ReleaseSession(sessionID string) {
	s.sessions.Release(sessionID)
}

// FinishSession 清理一条**已自然结束**的会话（收到 eof 并转发完毕后调用）。
func (s *DockerStreamService) FinishSession(sessionID string) {
	s.sessions.Remove(sessionID)
}

// CancelSession 取消一条会话：向 agent 下发 cancel（best-effort）并从注册表移除。
//
// cancel 必须终止 agent 侧进程并释放会话槽位（plan §0 控制语义）；发送失败
// （设备已离线等）只记日志 —— 注册表里的清理不依赖它，用户的「断开」必须立即生效。
func (s *DockerStreamService) CancelSession(ctx context.Context, sess *dockerstream.Session, reason string) {
	if sess == nil {
		return
	}
	s.sendControl(ctx, sess, agentproto.DockerFrameOpCancel, nil, 0, 0)
	s.sessions.Remove(sess.ID())
	if s.log != nil {
		s.log.Info("docker stream session cancelled",
			zap.String("session", sess.ID()), zap.Uint64("deviceId", sess.DeviceID()),
			zap.Uint64("userId", sess.UserID()), zap.String("kind", sess.Kind().String()),
			zap.String("reason", reason))
	}
}

// SendInput 把终端输入转发给 agent（input.data 必须以原样字节进协议载荷）。
func (s *DockerStreamService) SendInput(ctx context.Context, sess *dockerstream.Session, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return s.sendControl(ctx, sess, agentproto.DockerFrameOpInput, data, 0, 0)
}

// SendResize 把终端尺寸转发给 agent。
func (s *DockerStreamService) SendResize(ctx context.Context, sess *dockerstream.Session, cols, rows int) error {
	return s.sendControl(ctx, sess, agentproto.DockerFrameOpResize, nil, cols, rows)
}

// sendControl 组一条 core.docker.frame 并发往会话所属设备。
//
// 载荷先过协议校验（protocol.CoreDockerFrame.Validate）：非法尺寸/空输入在这里被
// 拦下，而不是让 agent 收到一帧它必须拒掉的东西（协议是两端共同的理解，core 不能
// 因为它「来自自己」就免检）。
//
// ctx 不参与发送（SendToDevice 没有 ctx 参数）：调用方仍按惯例传 ctx，将来若发送面
// 支持 ctx（例如带超时的队列投递）不必改签名。
func (s *DockerStreamService) sendControl(_ context.Context, sess *dockerstream.Session,
	op string, data []byte, cols, rows int) error {
	if sess == nil {
		return errors.New("dockerstream: nil session")
	}
	frame := &agentproto.CoreDockerFrame{SessionID: sess.ID(), Op: op, Data: data, Cols: cols, Rows: rows}
	if err := frame.Validate(); err != nil {
		return err
	}
	msg, err := agentproto.NewMessage(newRequestID(), agentproto.TypeCoreDockerFrame, frame)
	if err != nil {
		return err
	}
	sess.Touch()
	if s.sender == nil {
		return nil
	}
	if err := s.sender.SendToDevice(sess.DeviceID(), msg); err != nil {
		if s.log != nil {
			s.log.Warn("docker stream control frame not delivered",
				zap.String("session", sess.ID()), zap.String("op", op), zap.Error(err))
		}
		return err
	}
	return nil
}

// Sweep 清理流会话（由 wireup 的 docker sweep 周期调用）：
//   - 空闲超时（10 分钟无数据）：下发 cancel + 移除；
//   - 设备离线：会话已无数据来源，移除（不必发 cancel，发也送不到）。
//
// 返回清理条数（供调用方判定「这一轮有没有事发生」，口径与 cmd sweep 一致）。
func (s *DockerStreamService) Sweep(ctx context.Context) (int, error) {
	n := 0
	for _, sess := range s.sessions.Expired(s.now()) {
		s.CancelSession(ctx, sess, "idle timeout")
		n++
	}
	if s.online != nil {
		for _, sess := range s.sessions.All() {
			if !s.online(sess.DeviceID()) {
				s.CancelSession(ctx, sess, "device offline")
				n++
			}
		}
	}
	return n, nil
}
