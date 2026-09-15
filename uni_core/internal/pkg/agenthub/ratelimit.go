package agenthub

import (
	"context"
	"fmt"
	"net"
)

// ─ 入站帧级限流：消费方窄接口 + 计数键 ──────────────────────────────
//
// 缺口：协议登记了 `CloseRateLimited`(4006)「限流」，但仓库里**没有任何**限流实现
// ——一条失控的 agent（bug 或被攻陷）可以无限量刷帧，把 core 的 CPU 与 Redis 带宽
// 吃满，而这条通道是**未鉴权**入口。本文件只负责「键怎么算」，判定与关闭在 conn.go，
// 计数落 Redis 的实现由 wireup 注入（见 wireup 的 agentFrameLimiter）。

// RateLimiter 是入站**帧级**限流的窄接口（消费方接口，仓库既有约定：接口定义在消费方）。
//
// 为什么签名里把 deviceID 与 remoteIP **两个身份都给出去**，而不是让连接先算好一个键：
// 限流键必须随身份变化 —— hello 之前连接还没有设备身份（只有远端 IP），hello 之后
// 才有 deviceID。若让连接决定「用哪个身份」，实现侧就无从知道键该怎么拼（它会自己
// 再判一次「算不算已鉴权」），同一个判定散落两处，迟早不一致；而若让实现决定
// 「现在算不算已鉴权」，它又看不到连接的状态机（StateAwaitHello / StateEnrolling）。
// 于是契约定死为：**连接如实报告此刻已知的身份，实现按 deviceID != 0 选键**
// （规则见 FrameLimitKey）。
//
// 语义约定（连接侧不得违背）：
//   - 返回 (true, nil) → 放行这一帧；
//   - 返回 (false, nil) → 超限：连接以 CloseRateLimited(4006) 关闭并终止读循环；
//   - 返回 (_, err) → **限流器故障**：连接必须 **fail-open**（放行 + Warn）。
//     取向写死在消费方（conn.allowFrame），实现只管如实返回错误。
type RateLimiter interface {
	Allow(ctx context.Context, deviceID uint64, remoteIP string) (bool, error)
}

// 入站帧计数键。这是 **WS 帧级**键，不属于 agentmetrics 的指标键族：
// 指标键描述的是「设备的样本 / 水位投影」，这里的键描述的是「这台设备（或这个 IP）
// 此刻刷了多少帧」。把它们混在一起会让 agentmetrics 认识限流这个与它无关的关注点，
// 所以键构造留在这里，形态自成一族。
const (
	// frameKeyDeviceFormat 是 **已鉴权**连接的键：按**设备**计数。
	//
	// 为什么按设备而不是按连接：同一台设备的重连风暴（每次都是新 TCP 连接）必须
	// 共用同一个额度，否则「断开-重连」就是绕过限流的最短路径（一次重连 = 一整份额度）。
	frameKeyDeviceFormat = "agent:device:%d:frames"
	// frameKeyPreAuthFormat 是 **未鉴权**连接的键：按**远端 IP** 计数。
	//
	// 未鉴权阶段必须同样受限：否则攻击者只要**不**发 hello，就能在握手之前把
	// CPU/带宽刷满（这条路径恰恰不需要任何凭据）。按 IP（而不是按连接）才有意义 ——
	// 开 N 条 TCP 连接就是 N 个键，等于没限流；IP 由 remoteIPOf 取 **host 部分**，
	// 带上端口同样等于「每连接一个键」，是本条规则唯一的失效方式。
	frameKeyPreAuthFormat = "agent:ws:preauth:%s"
)

// FrameLimitKey 按「此刻已知的身份」返回入站帧计数键。
//
// 规则**定死为二选一**（不得引入第三形态，也不得让调用方各自拼键）：
//   - 已鉴权（deviceID != 0）→ `agent:device:{id}:frames`
//   - 未鉴权（deviceID == 0）→ `agent:ws:preauth:{远端 IP}`（IP 不含端口）
//
// 为什么 deviceID == 0 就等同「未鉴权」：雪花 ID 不可能为 0，而 handleHello 在
// 鉴权返回 0 号设备时是**拒绝关闭**（见该处注释），所以连接上不可能出现
// 「已鉴权但 deviceID == 0」的中间态。
func FrameLimitKey(deviceID uint64, remoteIP string) string {
	if deviceID != 0 {
		return fmt.Sprintf(frameKeyDeviceFormat, deviceID)
	}
	return fmt.Sprintf(frameKeyPreAuthFormat, remoteIP)
}

// remoteIPOf 从 socket 的远端地址里取 **host 部分**（丢掉端口）。
//
// 为什么必须丢掉端口：端口**每条 TCP 连接都不一样**，把它带进键等于每次连接都开一个
// 新计数器 —— 未鉴权限流会退化成「每条连接各自一整份额度」，攻击者开 N 条连接就拿到
// N 倍额度（正是要防的那件事）。
//
// 取不到可解析的 `host:port`（部分 net.Addr 实现只有主机名，测试替身可能给 nil）时
// 退回原串：宁可键粗一点（甚至所有这类连接共用一个键），也不要因为解析失败把限流整个关掉。
func remoteIPOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	s := addr.String()
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		return host
	}
	return s
}
