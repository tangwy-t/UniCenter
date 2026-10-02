package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传（v1.3）：浏览器 → core → WSS 二进制帧 → agent ──────────
//
// 本服务的存在是传输拓扑逼出来的：agent 只出一根出站 WSS 长连接，浏览器上传
// 的 tar 字节**必须经 core 中转**。三条由此定下的纪律（每一条都是「为什么」级）：
//
//  1. **core 不落盘**：中转是顺路搬运，不是落库 —— 请求体只在内存里驻留
//     「一个 256KB 分片 + 一块读缓冲」，即读即发即弃。落盘会退化成
//     「core 先存一份、agent 再收一份」，还多出 core 侧 512MB×N 的残留清理面。
//  2. **背压沿栈传导**：分片帧经 Conn.EnqueueBinary **阻塞式**入队 —— 写 WSS
//     慢时队列满，队列满时 HTTP handler 慢，handler 慢时浏览器 TCP 收窗关闭。
//     任何一环**丢弃**，字节流就断（agent 哈希终验对不上），所以这条链上
//     没有「丢弃并计数」，只有「等一等」。
//  3. **全程 sha256 累计**：core 在搬运中顺手累计哈希（对 CPU 是零增量成本），
//     转完随完成控制帧发给 agent —— 传输完整性由 agent 终验证实，core 自己
//     **不需要**知道结果（v1 无回执：见下方 Transfer 的注释，取舍留在那里）。
//
// 安全面：上传通道是新的**输入面**（此前 build context 以「文件已在主机上」的
// 前提存在），所以区分两道校验的管辖权：上传段只验「这像个 tar.gz」（gzip
// 魔数）+ 512MB 大小（在上传端点即拒，不等传完）；tar 条目/穿越/Dockerfile
// 这类**深度校验留给 image:build 执行**时的 scanBuildContext —— 单一校验事实源，
// 上传段抢着做纵深校验只会把同一份判断拆成两处漂移的复制品。

// DockerBuildContextChannel 是上传中转对 agent 通道的**全部**要求
// （由 agenthub.Hub 满足：SendBinaryToDevice / SendToDevice 两个方法）。
//
// 为什么用窄接口而不是具体 *agenthub.Hub：测试要用「账本替身」驱动分片计数、
// 中途断链与离线拒绝 —— 替身只记帧与厂造错误，具体 Hub 需要真实连接注册表。
type DockerBuildContextChannel interface {
	// SendBinaryToDevice 阻塞式送一帧二进制数据（上下文中断可解除等待）。
	// 返回 agenthub.ErrDeviceOffline = 设备离线。
	SendBinaryToDevice(ctx context.Context, deviceID uint64, payload []byte) error
	// SendToDevice 送文本信封（完成/中止控制帧；丢弃语义由 Hub 既有实现负责）。
	SendToDevice(deviceID uint64, msg *agentproto.Message) error
}

// DockerBuildContextService 是构建上下文上传的中转服务。
type DockerBuildContextService struct {
	ch         DockerBuildContextChannel
	log        logger.LoggerInterface
	sessionGen func() uint64
	// online 是设备在线性的预检（吃请求体**之前**就给出 503 —— 一条 500MB 的
	// 请求体不该为离线设备被白吃；nil = 跳过预检，由首帧发送时的
	// ErrDeviceOffline 兜住 —— 与 dockerstream 的 WithDeviceOnline 同款接线）。
	online func(deviceID uint64) bool
}

// NewDockerBuildContextService 构造上传中转服务。
func NewDockerBuildContextService(ch DockerBuildContextChannel, log logger.LoggerInterface) *DockerBuildContextService {
	return &DockerBuildContextService{
		ch:  ch,
		log: log,
		// 会话号用**专属的 uint64 生成器**，不沿用指令 ref 的 NewRequestID：
		// 后者是「JS 侧不丢精度」的十进制字符串域（纳秒 + 自增，21~22 位
		// ≈1.7e21），超出 uint64 值域 —— 而会话号的下游**全是数字形态**
		// （帧头 8 字节定长 session 槽、控制帧 SessionID、build-ctx-<十进制>
		// 推导名）。把它 ParseUint 塞回 uint64 就是 QA 撞上的恒 500
		// （ErrRange）；两个值域各自用对生成器，见 newUploadSessionID。
		sessionGen: newUploadSessionID,
	}
}

// WithOnline 注入设备在线预检（wireup 用 hub.Get 包装；测试用固定布尔驱动离线拒绝）。
func (s *DockerBuildContextService) WithOnline(fn func(deviceID uint64) bool) *DockerBuildContextService {
	s.online = fn
	return s
}

// uploadSessionSeq 是会话号低位序号段的进程内自增。
var uploadSessionSeq atomic.Uint64

const (
	// uploadSessionSeqBits 是会话号里序号段占的位数（21 bit = 每毫秒 200 万个
	// 会话；上传是人触发的动作，取值域富余四个数量级）。
	uploadSessionSeqBits = 21
	// uploadSessionSeqMask 是序号段的掩码。
	uploadSessionSeqMask = (uint64(1) << uploadSessionSeqBits) - 1
)

// newUploadSessionID 生成一个上传会话号（uint64，恒非 0）。
//
// 形态：UnixMilli 左移 21 位 | 自增序号低 21 位。为什么是这个形态 —— 会话号
// 的下游**全是数字**（帧头 8 字节定长 session 槽、控制帧 SessionID、
// build-ctx-<十进制> 推导名），所以它必须装得进 uint64：
//   - 时间位保证跨毫秒互异（UnixMilli 到 2^43，即公元 2248 年，左移后仍 < 2^64）；
//   - 序号位保证同毫秒内互异（原子自增，21 位回绕需要单毫秒 200 万次上传）。
//
// 重启后计数器归零：与重启前撞号需「同一毫秒 + 同序号」—— 重启耗时远超
// 1ms，且真撞上也不过是 agent 侧同会话号首帧的 seq 校验作废旧半成品
// （弃会话不断连），语义自愈，不留坏账。
//
// 为什么不是「全程 string」（把 22 位请求号原样当会话号）：那要把字符串塞进
// 二进制帧头，定长 20 字节头就得变长 —— 动的是一份有意为之的线上格式
// （见协议 build_context.go 头格式注释：「不含变长字段，头解析没有长度字段
// 被截断的二义性」），而不是一处 bug。会话号本就自成一个数值域，给它一个
// 装得下的生成器即同时保住协议形状与唯一性；降精度截串那条路（截前 19 位
// 再 ParseUint）丢唯一性保证，不在选项里。
func newUploadSessionID() uint64 {
	return uint64(time.Now().UnixMilli())<<uploadSessionSeqBits |
		(uploadSessionSeq.Add(1) & uploadSessionSeqMask)
}

// gzip 魔数：上传段「这像个 tar.gz」的判定就是它（深度校验在 image:build）。
var buildCtxGzipMagic = []byte{0x1f, 0x8b}

// uploadMaxBytes 是上传端点的字节闸（协议 MaxDockerBuildContextBytes）。
// var 而非 const：测试调小它以驱动「流式中超限」而不真传 512MB。
var uploadMaxBytes = int64(agentproto.MaxDockerBuildContextBytes)

// uploadChunkBytes 是读块/分片大小（协议 MaxDockerBuildCtxChunkBytes）。
var uploadChunkBytes = int(agentproto.MaxDockerBuildCtxChunkBytes)

// Transfer 流式中转一次上传：读 body → 分片 → WSS 二进制帧 → 完成控制帧，
// 返回产物文件名（image:build 的 context 值）。
//
// contentLen 是请求的 Content-Length（-1 = 缺席，chunked）：正的超上限值在
// **读一个字节之前**就拒（512MB 上限「即拒不等传完」的两段之一）；无长度头时
// 由 MaxBytesReader 在 handler 层收住（第二段），本函数仍保留流式账目兜底。
//
// 失败路径统一「收尾句号」：中止控制帧尽力送达（agent 删半成品）+ 带结论句的
// 错误。浏览器断开（ctx 取消）时中止帧照发 —— 那是一条独立于 HTTP 生命周期的
// 呼救信号，agent 靠它知道「别等了」。
func (s *DockerBuildContextService) Transfer(ctx context.Context, deviceID uint64, body io.Reader, contentLen int64) (string, error) {
	// ① 离线预检：在吃请求体之前 —— 见 DockerBuildContextChannel 的注释。
	if s.online != nil {
		if !s.online(deviceID) {
			return "", apperror.Unavailable("设备当前离线，无法接收构建上下文")
		}
	}
	// ② 尺寸闸（Content-Length 形态）：即拒不等传完。
	if contentLen >= 0 && contentLen > uploadMaxBytes {
		return "", apperror.BadRequest("构建上下文超过 512MB 上限")
	}

	// ③ 会话号与产物名：名字由会话号唯一推导（agent 不接受客户端命名，
	// 协议 Validate 把推导关系钉死 —— 这里只是把它算出来）。会话号由专属
	// uint64 生成器**直出** —— 不再有「字符串 id 塞回 uint64」的 ParseUint
	// 环节（QA 恒 500 的根因，选型论证见 newUploadSessionID）。
	session := s.sessionGen()
	name := agentproto.DockerBuildCtxTransferName(session)
	// 控制帧的信封 id 用会话号的十进制形态：信封 id 在两端都不参与关联
	// （唯一的消费是 hello/ack 回显），控制帧的关联键是载荷里的 SessionID ——
	// 同源只是让日志能按会话号一 grep 到底。
	sessionStr := strconv.FormatUint(session, 10)

	// ④ 魔数预读：恰 2 字节（gzip magic）。不足 2 字节的 body 不可能是合法
	// 上下文 —— 在发出**任何**一帧之前拒绝（agent 侧零状态需要清理）。
	var head [2]byte
	if _, err := io.ReadFull(body, head[:]); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return "", apperror.BadRequest("上传内容为空")
		case errors.Is(err, io.ErrUnexpectedEOF):
			return "", apperror.BadRequest("内容不是 gzip 压缩的 tar 归档")
		default:
			return "", apperror.BadRequest("读取上传内容失败")
		}
	}
	if head[0] != buildCtxGzipMagic[0] || head[1] != buildCtxGzipMagic[1] {
		return "", apperror.BadRequest("内容不是 gzip 压缩的 tar 归档")
	}

	// ⑤ 分片中转主循环（一帧前瞻：末帧的 FINAL 位要等下一轮读回 EOF 才定）。
	h := sha256.New()
	total := int64(2)
	h.Write(head[:])
	pending := head[:] // 待发分片；首帧含魔数 2 字节（seq=1）
	buf := make([]byte, uploadChunkBytes)
	var seq uint64

	// sendPack 给下一片组帧送出；err 细分三种（离线/取消/通道断），见 failTransfer。
	sendChunk := func(p []byte, final bool) error {
		seq++
		frame, err := agentproto.PackBinaryFrame(session, seq, final, p)
		if err != nil {
			// 只可能是分片超上限的头文件级错误：会话数理上到不了这里。
			return err
		}
		return s.ch.SendBinaryToDevice(ctx, deviceID, frame)
	}
	// failTransfer 是全部**中转期**失败的收尾：中止控制帧（尽力）+ 结论句。
	failTransfer := func(err error) (string, error) {
		s.abort(deviceID, session)
		switch {
		case errors.Is(err, agenthub.ErrDeviceOffline):
			return "", apperror.Unavailable("设备当前离线，无法接收构建上下文")
		case errors.Is(err, context.Canceled):
			// 浏览器断开：响应写给没人 —— 原样传出让 handler 静默收场。
			return "", err
		default:
			// 通道断 / 队列异常：可重试性故障，句号说「没传完」。
			if s.log != nil {
				s.log.Warn("构建上下文上传中转中断",
					zap.Uint64("device_id", deviceID),
					zap.Uint64("session", session),
					zap.Error(err))
			}
			return "", apperror.Unavailable("与主机的通道中断，上传未完成，请重试", err)
		}
	}

	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if pending != nil {
				// 上一片现在才知道不是末片：送出（FINAL=0）。
				if err := sendChunk(pending, false); err != nil {
					return failTransfer(err)
				}
			}
			pending = append([]byte{}, buf[:n]...) // 拷贝：buf 下一轮要被覆盖
			total += int64(n)
			h.Write(pending)
			if total > uploadMaxBytes {
				// 流式账目的兜底（Content-Length 缺席且未走 MaxBytesReader 的
				// 调用形态）：与 MaxBytesError 同一条收尾。
				return s.oversize(
					errors.New("构建上下文流式账目超过上限"), deviceID, session)
			}
		}
		switch {
		case rerr == nil:
			continue
		case errors.Is(rerr, io.EOF):
			// ⑥ 末片（FINAL=1）+ 完成控制帧（期望哈希/大小）。
			if total == 0 {
				return "", apperror.BadRequest("上传内容为空")
			}
			if err := sendChunk(pending, true); err != nil {
				return failTransfer(err)
			}
			finish, err := agentproto.NewMessage(sessionStr, agentproto.TypeCoreDockerBuildCtxFinish,
				&agentproto.CoreDockerBuildCtxFinish{
					SessionID: session,
					Name:      name,
					SHA256Hex: hex.EncodeToString(h.Sum(nil)),
					SizeBytes: total,
				})
			if err != nil {
				// 构造失败 = 我们自己的契约问题（NewMessage 自校验），按内部错误收场。
				s.abort(deviceID, session)
				return "", apperror.Internal("内部错误", err)
			}
			if err := s.ch.SendToDevice(deviceID, finish); err != nil {
				// 完成帧没进队列（离线/背压丢弃）：agent 不会终检，半成品等 24h
				// 清扫 —— 但 HTTP 必须如实说「这次上传不算数」。
				return failTransfer(err)
			}
			return name, nil
		default:
			// 读失败（含 handler 层 MaxBytesReader 的「超上限」错误）：
			// 中止 + 句号。超上限与普通读失败的分句在 oversize 里做。
			var maxErr *http.MaxBytesError
			if errors.As(rerr, &maxErr) {
				return s.oversize(rerr, deviceID, session)
			}
			return failTransfer(rerr)
		}
	}
}

// oversize 是「超过 512MB」的收尾：中止帧 + 400 句号（尺寸闸的第二段 ——
// 只剩流式中途才发现超限这一种进入路径，第一段在 Content-Length 预检）。
//
// 为什么 err 也传进来：日志带原始错误（MaxBytesError / 账目触线），结论句
// 对用户说干净的一句话，两件事各走各的出口。
func (s *DockerBuildContextService) oversize(err error, deviceID, session uint64) (string, error) {
	s.abort(deviceID, session)
	if s.log != nil {
		s.log.Warn("构建上下文上传超上限",
			zap.Uint64("device_id", deviceID), zap.Uint64("session", session), zap.Error(err))
	}
	return "", apperror.BadRequest("构建上下文超过 512MB 上限")
}

// abort 尽力下发一条中止控制帧。失败**不影响**返回值 —— 它本来就是「收尾
// 呼救」：agent 侧还有断连清理与 24h 清扫两道兜底，中止帧是三保险里
// 最先到的那一保。
func (s *DockerBuildContextService) abort(deviceID, session uint64) {
	// 信封 id 与完成帧同源（会话号十进制）：两帧至多一帧会真正进入语义
	//（完成帧送不达才补中止），且信封 id 本就无人关联 —— 见 Transfer ③ 的说明。
	msg, err := agentproto.NewMessage(strconv.FormatUint(session, 10), agentproto.TypeCoreDockerBuildCtxAbort,
		&agentproto.CoreDockerBuildCtxAbort{SessionID: session})
	if err != nil {
		return // 构造失败 = 会话号 0？生成器不会产出；防御性短路。
	}
	if err := s.ch.SendToDevice(deviceID, msg); err != nil && s.log != nil {
		s.log.Debug("构建上下文中止帧未送达",
			zap.Uint64("device_id", deviceID), zap.Uint64("session", session), zap.Error(err))
	}
}
