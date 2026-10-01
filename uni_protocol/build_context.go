package agentproto

import (
	"encoding/binary"
	"errors"
	"fmt"

	"strconv"
	"strings"
)

// ── 构建上下文上传通道（v1.3）：二进制分片帧 ──────────────────────────────
//
// 这是协议里**第一种非 JSON 的线上形态**。选型的根因是传输拓扑：
// agent 是出站 WSS 连到 core，浏览器上传的构建上下文字节必须经 core 中转走
// WSS 到 agent —— 如果把 tar 字节塞进 JSON 信封，每 256KB 都要 base64 一遍
// （放大 1/3）再 json 解析一遍，且 MaxMessageBytes=1MB 的上限会让一条 512MB
// 的上传变成「数百条超大信封 + 两端各一轮二次编码」的耗 CPU 形态。
// 于是扩二进制帧：WebSocket BinaryMessage 承载，小头 + 原始载荷，零转码。
//
// 方向纪律：二进制帧**只有 core→agent 一个方向**（v1 上传通道单向）。
// agent→core 仍然只有文本信封 —— 契约层的方向检查对二进制入站在各缓冲面
// 各自拒绝（core 读循环见 agenthub/conn.go，agent 读循环见 transport/client.go）。
//
// 头格式（20 字节定长，全部大端；载荷紧随其后）：
//
//	offset  size  字段
//	  0      2    magic    "BC"（0x42 0x43）—— 与任何 JSON 字节流可区分
//	  2      1    version  1
//	  3      1    flags    bit0 = FINAL（本会话最后一帧）
//	  4      8    session  上传会话号（core 生成；0 非法）
//	 12      8    seq      分片序号，从 1 起、每帧 +1
//	 20      n    payload  分片数据（≤ MaxDockerBuildCtxChunkBytes）
//
// 序号纪律（agent 侧 enforced，见 uni_agent/internal/dockerops/build_ctx_receive.go）：
//   - 首帧必须 seq=1，之后严格 +1：乱序/重复/跳号 → 弃会话（哈希终验必然失败，
//     且不弃的话乱序帧会绕过组装器的尺寸账目）；
//   - FINAL 之后再来任何帧 → 弃会话；FINAL 缺位（通道断了）→ 断连清理弃会话。
//
// 续传**不做**（v1）：上传失败即重传整份。取舍注释：分片只有 256KB，
// 重传代价可控；而续传要「会话状态两段对齐 + 断点恢复 + 残留裁决」，
// 状态机复杂度与 agent 长期驻留半成品文件的风险都不值得 —— 中止语义
// （控制帧 / 断连）把半成品删干净，重试永远从干净的会话开始。

const (
	// MaxDockerBuildCtxChunkBytes 是**单帧**构建上下文分片的字节上限（256KB）。
	// 与文件上限 MaxDockerBuildContextBytes（512MB）的关系是「分片 × 序号」：
	// 分片是重传与背压的原子单位，256KB 让「失败重传整份」与「core 中转的内存
	// 驻留」同时可控（一分片一帧，core 不落盘、分片即弃）。
	MaxDockerBuildCtxChunkBytes = 256 << 10

	// dockerBuildCtxMagic0/Magic1 是二进制帧魔数 "BC"（BuildContext 缩写）。
	dockerBuildCtxMagic0 = 'B'
	dockerBuildCtxMagic1 = 'C'

	// dockerBuildCtxFrameVersion 是帧头版本号。与信封 v 独立：既有 JSON 消息的
	// 版本沿续信封 v；二进制帧是新的形态轴，自带版本位，未来头结构演进不碰信封。
	dockerBuildCtxFrameVersion = 1

	// DockerBuildCtxFlagFinal 是 flags 的 bit0：置位 = 本会话的最后一帧。
	// 与 seq 的关系：FINAL 帧的 seq 是**最后一个**合法序号；FINAL 之后不得跟帧。
	DockerBuildCtxFlagFinal uint8 = 1 << 0

	// DockerBuildCtxFrameHeaderBytes 是二进制帧头的定长字节数（20 = 2+1+1+8+8）。
	// 定长而**不含变长字段**（会话号不上名字/串形态）：头解析没有「长度字段被
	// 截断」这类二义性，解析失败只可能是「字节不够 / 字段非法」两种，agent 侧
	// 的读循环可以零分配地判掉垃圾帧。
	DockerBuildCtxFrameHeaderBytes = 20
)

// ── 帧解析错误（哨兵：调用方可 errors.Is 分拣）───────────────────────────
//
// 不并入 DecodeError（那是 JSON 信封的 Stage/Code 体系，二进制帧是另一根形态轴，
// 混进信封错误族会让「哪条通路出错」在分拣处塌缩）。哨兵 + %w 包裹的用法与
// repository 层的哨兵同款；线上错误串只是包裹层的上下文消息。
var (
	// ErrBinaryFrameTooShort 帧长不足以容纳 20 字节头。
	ErrBinaryFrameTooShort = errors.New("binary frame: 帧长不足 20 字节头")
	// ErrBinaryFrameBadMagic 魔数不是 "BC"：拿错的字节流 / 走错的消息类型。
	ErrBinaryFrameBadMagic = errors.New("binary frame: 魔数不符")
	// ErrBinaryFrameUnsupportedVersion 头版本不是 1：对端在说我们没见过的帧格式。
	ErrBinaryFrameUnsupportedVersion = errors.New("binary frame: 头版本不在受支持区间")
	// ErrBinaryFrameBadFlags 置了未知标志位：契约外语义，拒绝优于猜测。
	ErrBinaryFrameBadFlags = errors.New("binary frame: 含未知标志位")
	// ErrBinaryFrameBadSession 会话号为 0（0 是「无会话」的哨兵，线上永不合法）。
	ErrBinaryFrameBadSession = errors.New("binary frame: 会话号为 0")
	// ErrBinaryFrameBadSeq 序号为 0（seq 从 1 起，0 留作「未初始化」哨兵）。
	ErrBinaryFrameBadSeq = errors.New("binary frame: 序号为 0")
	// ErrBinaryFramePayloadTooLarge 载荷超过单帧上限。
	ErrBinaryFramePayloadTooLarge = errors.New("binary frame: 载荷超过单帧上限")
)

// binaryFrameErrf 用哨兵裹上一条具体语境。
func binaryFrameErrf(kind error, format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{kind}, args...)...)
}

// BinaryFrame 是构建上下文上传的一条二进制分片帧（解包结果）。
//
// 字段与线上的头布局一一对应；Payload 是 UnpackBinaryFrame 里**复制出来**的
// 独立字节（原 buf 可被读循环复用/释放，复用帧里的切片会在下一轮读取时被覆盖）。
type BinaryFrame struct {
	SessionID uint64
	Seq       uint64
	Final     bool
	Payload   []byte
}

// PackBinaryFrame 组一帧二进制分片（magic + version + flags + session + seq + payload）。
//
// 三个语义错误在这里就拒（打包方是 core：让它发出的帧在契约内，agent 的
// 解包校验就只剩「防御恶意/畸形输入」一个职责）：
//   - SessionID=0 / Seq=0：哨兵值，永不上线；
//   - Payload 超 MaxDockerBuildCtxChunkBytes：分片纪律由打包方扛第一道。
func PackBinaryFrame(sessionID, seq uint64, final bool, payload []byte) ([]byte, error) {
	if sessionID == 0 {
		return nil, binaryFrameErrf(ErrBinaryFrameBadSession, "pack: session_id 不能为 0")
	}
	if seq == 0 {
		return nil, binaryFrameErrf(ErrBinaryFrameBadSeq, "pack: seq 从 1 起")
	}
	if len(payload) > MaxDockerBuildCtxChunkBytes {
		return nil, binaryFrameErrf(ErrBinaryFramePayloadTooLarge, "pack: 分片载荷超过单帧上限")
	}
	out := make([]byte, DockerBuildCtxFrameHeaderBytes+len(payload))
	out[0], out[1] = dockerBuildCtxMagic0, dockerBuildCtxMagic1
	out[2] = dockerBuildCtxFrameVersion
	flags := uint8(0)
	if final {
		flags |= DockerBuildCtxFlagFinal
	}
	out[3] = flags
	binary.BigEndian.PutUint64(out[4:12], sessionID)
	binary.BigEndian.PutUint64(out[12:20], seq)
	copy(out[20:], payload)
	return out, nil
}

// UnpackBinaryFrame 严格解析一条二进制消息；失败返回 typed error，绝不返回半成品。
//
// 校验顺序即防卫顺序：长度（没有它规定不了任何字段）→ 魔数（消息类型判错）
// → 版本 → 标志位（未知位=契约外）→ session/seq 形态。每条都对应一个可
// errors.Is 的错误，两端读循环据「要不要因此断连/弃会话」各自裁决 ——
// 协议只负责把「哪里不对」说清楚。
func UnpackBinaryFrame(b []byte) (BinaryFrame, error) {
	if len(b) < DockerBuildCtxFrameHeaderBytes {
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameTooShort, "帧长不足 20 字节头")
	}
	if b[0] != dockerBuildCtxMagic0 || b[1] != dockerBuildCtxMagic1 {
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameBadMagic, "二进制帧魔数不符")
	}
	if b[2] != dockerBuildCtxFrameVersion {
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameUnsupportedVersion, "帧头版本不在受支持区间")
	}
	if b[3]&^DockerBuildCtxFlagFinal != 0 {
		// 未知标志位 = 对端按我们不懂的语义发帧：拒绝优于猜测（等价于信封的
		// ErrUnsupportedVersion 取向，只是落在标志位这一轴）。
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameBadFlags, "帧含未知标志位")
	}
	session := binary.BigEndian.Uint64(b[4:12])
	if session == 0 {
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameBadSession, "会话号为 0")
	}
	seq := binary.BigEndian.Uint64(b[12:20])
	if seq == 0 {
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFrameBadSeq, "序号为 0")
	}
	payload := b[20:]
	if len(payload) > MaxDockerBuildCtxChunkBytes {
		// 防线二段：解包方也拦一次（agent 入站没有单帧上限，恶意巨帧在这里
		// 就出不去，不会进组装器）。
		return BinaryFrame{}, binaryFrameErrf(ErrBinaryFramePayloadTooLarge, "帧载荷超过单帧上限")
	}
	return BinaryFrame{
		SessionID: session,
		Seq:       seq,
		Final:     b[3]&DockerBuildCtxFlagFinal != 0,
		Payload:   append([]byte{}, payload...),
	}, nil
}

// ── 产物文件名纪律（v1.3：名字由会话号**唯一推导**）───────────────────────
//
// 为什么是推导而不是携带：上传产物要落 transferDir（路径纪律：协议上不出现
// 路径，agent 只接受文件名），而「agent 不接受客户端命名」的落点就是把名字
// 与不可伪造的会话号绑死 —— core 生成会话号 = 它命名了文件；agent 从帧的
// session 字段**重新推导**出同一名字，两边不比对任何一条自述的字符串。
// 完成控制帧里的 name 字段只是让控制帧自包含（且被 Validate 钉成
// 「必须等于推导值」，写错 = 构造错误，跑都跑不起来）。

// dockerBuildCtxNamePrefix 是上传产物文件名前缀。清扫纪律（agent 侧 24h
// 清扫）按这个前缀圈定对象 —— build-ctx-* 只可能是上传产物，不碰
// image:save/load 的 .tar 文件（那是用户起的名）。
const dockerBuildCtxNamePrefix = "build-ctx-"

// DockerBuildCtxTransferName 由上传会话号推导产物文件名：
// "build-ctx-<会话号十进制>.tar.gz"。形态过 IsDockerBuildContextFilename
// （首字符字母数字、点/横线合法、.tar.gz 后缀在三种合法形态内），
// 故 image:build 的 context 选项原样可引用。
func DockerBuildCtxTransferName(sessionID uint64) string {
	return dockerBuildCtxNamePrefix + strconv.FormatUint(sessionID, 10) + ".tar.gz"
}

// ParseDockerBuildCtxTransferName 反向解析产物名，返回 (会话号, 合法)。
// 只认「前缀 + 十进制会话号 + .tar.gz」这一种形状 —— 任何别的东西（路径
// 分隔符、别的后缀、别的前缀）都不是上传产物，agent 拒绝写入。
func ParseDockerBuildCtxTransferName(name string) (uint64, bool) {
	if !strings.HasPrefix(name, dockerBuildCtxNamePrefix) || !strings.HasSuffix(name, ".tar.gz") {
		return 0, false
	}
	core := name[len(dockerBuildCtxNamePrefix) : len(name)-len(".tar.gz")]
	if core == "" {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(core); i++ {
		c := core[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	if v == 0 {
		return 0, false
	}
	if !IsDockerBuildContextFilename(name) {
		// 形态闸补最后一刀（推导名的字符集本身已过，这里防的是有人绕过本函数）。
		return 0, false
	}
	return v, true
}

// IsHexSHA256 报告 s 是否为 64 位小写 hex 的 sha256 摘要形态
// （完成控制帧的期望哈希字段的唯一合法形态 —— 与 hex.EncodeToString 同口径，
// core 累计、agent 终验都对它）。
func IsHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
