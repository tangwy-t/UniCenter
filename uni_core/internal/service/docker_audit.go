package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// Docker 域的**结果审计面**（6c）：agent 结果 ingest 的终态转换点上，把执行结果
// 结构化地写进 sys_operation_log —— 功能审计原文「执行结果不入审计」的闭环。
//
// 三条纪律（各自对应既有先例）：
//   - **观察者不是参与者**：挂钩只记账、绝不改判 —— 指令的受理/轮询/流路径
//     零行为改动；写入失败只告警，不入账也不阻塞 ingest 主链（与中间件
//     saveOperationLog 的「异步 + 失败 warn」同款）；
//   - **敏感字段纪律照抄中间件**：结论句来自受控生成面（agent 的结论句），但仍
//     照样过一遍 middleware.DesensitizeJSON —— 敏感字段集合只有一份（中间件的
//     sensitiveFieldSet），复制一份就会在它加字段时审计侧静默落伍；
//   - **幂等**：入账的前提是「本次回写真的完成了一次终态转换」（判定在
//     AgentIngestService.CompleteDockerCmd，见那里唯一性保证的注释）；本类型
//     本身是纯消费者，重复调用由调用方的前置守卫挡住。

// 常量与 router 挂载 docker 组时的 SetModuleName("Docker 管理") **逐字一致**：
// 受理（HTTP 中间件）与结果（本挂钩）两条审计在操作日志页必须同模块并现，
// 否则「谁受理、谁执行、结果如何」这三段叙事查不到一张表里。
const (
	dockerOpLogModule = "Docker 管理"
	// dockerOpLogTaskResult 是结果审计的操作类型：与中间件的「新增/修改/删除」
	// 区分开 —— 这条不是 HTTP 写操作，是「受理后任务到达终态」的结果陈述。
	dockerOpLogTaskResult = "任务结果"
	// dockerCmdResultFailed 是「执行失败」的结果码，取值在字典 sys_opt_result_code
	// 里由 v017 种子注册（Label 执行失败，Class danger）—— 操作日志页的结果列靠
	// 字典渲染，没有这条码失败任务会被显示成「成功」或借一个语义错位的既有码。
	dockerCmdResultFailed = 70001
	// dockerTaskSummarySucceeded 是成功终态的兜底结论句（成功记录通常不带 Error，
	// 任务中心与审计都得给读者一句话）。
	dockerTaskSummarySucceeded = "执行成功"
)

// DockerCmdAuditWriter 是 sys_operation_log 的写入窄接口（由 OperationLogService 满足）。
//
// 为什么复用 OperationLogService 而不是拿 repository 裸写：它的 Create 会反查用户名
// 补全 Username —— 受理审计与结果审计走**同一条写路径**，用户名解析、落库口径只有一份。
type DockerCmdAuditWriter interface {
	Create(ctx context.Context, log *entity.SysOperationLog) error
}

// DockerCmdAuditor 把一条指令的终态折成操作日志条目并入账。
type DockerCmdAuditor struct {
	writer  DockerCmdAuditWriter
	devices DockerDeviceReader
	log     logger.LoggerInterface
	// now 可替换（测试要一个确定的入账时刻）。
	now func() time.Time
}

// NewDockerCmdAuditor 构造结果审计面（nil devices = 主机名解析关闭，hostname 留空）。
func NewDockerCmdAuditor(w DockerCmdAuditWriter, devices DockerDeviceReader, log logger.LoggerInterface) *DockerCmdAuditor {
	return &DockerCmdAuditor{writer: w, devices: devices, log: log, now: time.Now}
}

// RecordTerminal 异步入账一条终态审计。
//
// 调用前提（幂等契约，见 CompleteDockerCmd）：调用方只在「非终态 → 终态」的真实
// 转换后调它，同一 ref 恰入一条。docker:events 常驻订阅（UserID=0）不是用户任务：
// 这里豁免 —— 它们是 core 自己的订阅机制，其成败是监控面的事，不是用户操作审计。
//
// 异步边界与中间件同款：条目**同步**构建（rec 只在构建期读），写入进 goroutine
// 用 context.Background()（agent 通道的 ctx 与连接同寿，且会随帧处理结束失效）。
// 中间件显式重建 traceID/scope 的做法在这里不适用 —— agent 通道没有登录态，
// scope 无从谈起；用户名反查（userRepo.FindByID）是对**受理时已记录**的 UserID
// 做工具性解析，不是数据权限判定的查询面。
func (a *DockerCmdAuditor) RecordTerminal(rec *dockerstate.CmdRecord) {
	if a == nil || a.writer == nil || rec == nil || rec.UserID == 0 {
		return
	}
	if a.now == nil {
		// 测试用结构体字面量装配时的兜底（常量缺省在构造器里，这里守一次）。
		a.now = time.Now
	}
	entry := a.buildEntry(rec)
	go func() {
		defer func() {
			if r := recover(); r != nil && a.log != nil {
				a.log.Error("docker cmd 终态审计 panic（主链不受影响）",
					zap.String("ref", rec.Ref), zap.Any("panic", r))
			}
		}()
		if err := a.writer.Create(context.Background(), entry); err != nil && a.log != nil {
			// 审计失败**不阻塞主链**：结果是可观测的（任务中心/轮询），审计是它的
			// 旁证 —— 丢一条旁证比卡住结果回写轻得多。
			a.log.Warn("docker cmd 终态审计写入失败",
				zap.String("ref", rec.Ref), zap.Error(err))
		}
	}()
}

// buildEntry 把终态记录折成操作日志条目（纯函数，便于测试；不碰任何 IO 之外的 rec）。
//
// 字段归属（沿用 sys_operation_log 既有语义，无新列）：
//   - Code：0=成功 / 70001=执行失败（字典注册）；
//   - ErrorMsg：终态结论句原文（过脱敏、按列宽 1024 截断）；成功时留空；
//   - RequestParams：结构化 JSON {ref,action,target,hostId,hostname}（过脱敏、
//     MaxLogBodyBytes 截断）—— 操作日志页的详情抽屉用它还原「什么事、在哪、目标」；
//   - ResponseResult：结论句原文（正文展示位；与 ErrorMsg 的短结论是两回事 ——
//     它承载「结果陈述」而 ErrorMsg 是「错误摘要」）；
//   - CostTime：受理到终态的耗时（毫秒，记录里 CreatedAt/FinishedAt 已有，不新算）。
func (a *DockerCmdAuditor) buildEntry(rec *dockerstate.CmdRecord) *entity.SysOperationLog {
	summary := terminalSummary(rec)
	entry := &entity.SysOperationLog{
		UserID:        rec.UserID,
		Module:        dockerOpLogModule,
		OperationType: dockerOpLogTaskResult,
		Code:          apperror.CodeOK,
		OperTime:      a.now(),
	}
	if rec.Status != dockerstate.StatusSucceeded {
		entry.Code = dockerCmdResultFailed
		// 终态结论句（含「拉取已取消」这类取消句式）原文照录：取消没有独立状态码
		//（盘点结论见 docker_tasks.go 的注释），它的语义由这句话承载。
		s := middleware.TruncateString(middleware.DesensitizeJSON(summary), 1024)
		entry.ErrorMsg = &s
	}
	if rec.FinishedAt >= rec.CreatedAt {
		cost := int(rec.FinishedAt - rec.CreatedAt)
		entry.CostTime = &cost
	}

	// 主机名 best-effort 同步解析：条目构建在主链上（一条主键查询，代价可忽略），
	// 失败降级为空 hostname —— hostId 仍在，审计不失主键叙事；只有真故障（非
	// 未命中）值得告警。
	hostname := ""
	if a.devices != nil {
		if d, err := a.devices.FindByID(context.Background(), rec.DeviceID); err == nil && d != nil {
			hostname = d.Hostname
		} else if err != nil && !errors.Is(err, repository.ErrNotFound) && a.log != nil {
			a.log.Warn("docker cmd 终态审计：主机名解析失败（条目照常，hostname 留空）",
				zap.Uint64("deviceId", rec.DeviceID), zap.String("ref", rec.Ref), zap.Error(err))
		}
	}
	if params, err := json.Marshal(map[string]any{
		"ref":      rec.Ref,
		"action":   rec.Action,
		"target":   rec.Target,
		"hostId":   rec.DeviceID,
		"hostname": hostname,
	}); err == nil {
		p := middleware.TruncateString(middleware.DesensitizeJSON(string(params)), middleware.MaxLogBodyBytes)
		entry.RequestParams = &p
	}
	if summary != "" {
		s := middleware.TruncateString(middleware.DesensitizeJSON(summary), middleware.MaxLogBodyBytes)
		entry.ResponseResult = &s
	}
	return entry
}

// terminalSummary 是终态结论句（任务中心条目与结果审计**同一句话** —— 结论句是
// 治理口径，两处各写一遍就会在改文案时只有一个地方被改到）：
//   - 失败/超时：记录里的 Error（agent 的结论句原文，页面同款文案）；
//   - 成功：Error 非空用 Error（少数成功也会带附言），否则兜底「执行成功」；
//   - pending：空（没有结论）。
func terminalSummary(rec *dockerstate.CmdRecord) string {
	if rec == nil {
		return ""
	}
	switch rec.Status {
	case dockerstate.StatusFailed, dockerstate.StatusTimeout:
		return rec.Error
	case dockerstate.StatusSucceeded:
		if rec.Error != "" {
			return rec.Error
		}
		return dockerTaskSummarySucceeded
	default:
		return ""
	}
}
