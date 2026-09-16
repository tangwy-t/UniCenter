package service

import (
	"context"
	"time"
)

// 本文件是**指标链路时间参数推导的唯一来源**：flush（5m 桶）与 rollup（1h 小时）
// 都从配置项 `sys.agent.reportInterval` 推出自己的宽限（CloseGrace），
// 而这条推导曾经在两个服务里各写了一份（连文档注释都几乎逐字重复）。
//
// 为什么必须是一处（而不是「两边各写一份、注释说同源」）：宽限决定「当前桶/当前小时
// 什么时候算闭」，而两者是**同一条数据链的上下游** —— 1h 的输入就是 5m 的产物。
// 两份实现一旦漂移（例如一处改成 3×、另一处忘了改，或默认值被复制成不同的数），
// 症状是「某些小时被判成残缺/整小时空洞」，而两边的单测各自都绿。
//
// 注意 agenthub 包里还有一个同名常量（defaultReportIntervalSeconds，用于给 agent
// 下发 hello_ack.ReportInterval）：那是**协议层**的下发值（进线上报文，受
// minReportIntervalSeconds 下限约束），与这里的「服务侧宽限推导」不是同一个概念，
// 故不合并不共享（合并会让「协议下限」与「配置默认值」两个约束纠缠在一起）。

const (
	// configAgentReportInterval 是 agent 上报间隔（秒），CloseGrace 由它推导。
	configAgentReportInterval = "sys.agent.reportInterval"
	// defaultAgentReportIntervalSec 取 v008 种子（sys.agent.reportInterval=10）的值。
	//
	// 缺配置时**不得**退化成别的数（例如 1）：那会让 CloseGrace 缩到 2s，
	// 还在收数据的当前桶被当成已闭桶写进唯一真值；也不得退化成 0
	//（agent 上报节奏是"秒级"的语义，0 不是一个合法的上报间隔）。
	defaultAgentReportIntervalSec = 10
)

// agentReportInterval 返回 agent 的上报间隔（秒 → Duration）。
//
// 非法值（<=0，含配置被误改成负数）回落默认值：它是宽限推导的**分母**，
// 退化会让两个档位的边界一起错位。
func agentReportInterval(cfg AgentConfigGetter) time.Duration {
	sec := cfg.GetInt(context.Background(), configAgentReportInterval, defaultAgentReportIntervalSec)
	if sec <= 0 {
		sec = defaultAgentReportIntervalSec
	}
	return time.Duration(sec) * time.Second
}

// agentCloseGrace 取 `reportInterval×2`：避免把**还在收数据**的当前桶/当前小时写坏。
//
// 为什么是 2× 而不是 1×：10s 上报的桶在边界处最多可能有一条样例在途（网络抖动 +
// agent 侧批量缓冲），1× 只留一个上报间隔等于没有余量。2× 是最小安全余量，
// 代价是「当前桶/当前小时延后一格才落库」—— 对 5min 栅格与 1h 栅格的消费方均无感。
//
// 为什么两个服务取**同一个值**（而不是各按自己的档位放宽）：它们防的是同一件事
// （「还在收数据的当前桶不得被写成权威值」）。若 rollup 的宽限比 flush 的更小，
// 它会比 flush 先看到一个小时并把它算成「空/残缺」；repair 集合能兜住「残缺」，
// 兜不住「整小时都还没落库」。同源同值让两者的边界只差一个档位。
//
// 必须 > CloseGrace 的不变量之一（见 defaultEmptyHourGrace）：rollup 的空小时等待
// 窗口（2 小时）必须远大于本宽限（默认 20 秒），否则等待窗口比「小时何时算闭」还短。
func agentCloseGrace(cfg AgentConfigGetter) time.Duration {
	return 2 * agentReportInterval(cfg)
}

// agentHistoryRetentionDays 返回 **5m 档的保留期（天）**：`sys.agent.historyRetentionDays`
// （v008 种子 30；非法/缺失回落默认值）。
//
// 为什么把它收进本文件（而不是让每个消费方各读一次配置）：本文件是「指标链路时间参数推导的
// 唯一来源」，而保留期就是这条链上**另一个**决定边界的时间量。它有两个消费方：
//
//  1. 回退窗口的上界（flush 的 rewindBoundHours：1h 重算读的是库里的 5m 行，退到没有
//     5m 行的地方必然什么都算不出来）；
//  2. 「空小时是**暂时**空还是**永久**空洞」的判定（rollup 的 hourBeyondRetention：
//     超出保留期的 5m 行已被分区回收，那些小时永远补不回来）。
//
// 两处各读一次配置看着无害，但缺省值一旦被复制成两个不同的数（30 / 45），症状是
// 「回退能退到的下界」与「判定永久空洞的边界」不一致 —— 两边各自都自洽，只有交叉看才
// 看得出来（与本文件开头那段「宽限必须在两处同源」是同一类问题）。故这**不是**新增事实
// 来源：配置键与缺省值仍然只有 agent_metrics_partition.go 里那一对常量，
// 本函数只是把它变成一个可复用的读法。
func agentHistoryRetentionDays(cfg AgentConfigGetter) int {
	days := cfg.GetInt(context.Background(),
		configAgentHistoryRetentionDays, defaultAgentHistoryRetentionDays)
	if days <= 0 {
		days = defaultAgentHistoryRetentionDays
	}
	return days
}
