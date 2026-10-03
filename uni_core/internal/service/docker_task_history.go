package service

import (
	"context"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// Docker 域的**任务历史持久面**（8d）：把终态任务落进 docker_task_history，
// 让任务中心的「历史」不再是 CmdStore 的 ~30 分钟易失窗口。
//
// 本文件是 CmdStore 终态钩子（dockerstate.TerminalHook）的唯一实现，也是历史表的
// 唯一写入者 —— 钩子挂在 CmdStore 的终态转换点上，agent result / 缓存秒回 /
// discard / sweep 四条路径自动全数覆盖（见 dockerstate/cmd.go 的钩子契约）。
//
// 四条纪律：
//   - **同步落库、失败只告警**：落库发生在 Redis 写入成功之后、与它同一条调用链上。
//     同步而不是 goroutine 的理由：终态是「每条指令恰好一次」的低频事件（不是热
//     路径），一次单行 upsert（~1ms）与它后面那条 Redis pipeline 同价；而异步会
//     在「用户刚看着任务结束」这个瞬间留一个落库窗口期 —— 崩溃/重启就丢了刚追的
//     那条。失败一律 fail-open：实时通道（Redis）已经写成功了，历史丢一行只是少一条
//     可追记录，绝不能把指令主链带下水（观察者不是参与者，与审计/留存挂钩同纪律）。
//   - **只记「任务」**：docker:events 常驻订阅（UserID=0）与只读动作（inspect 档）
//     在写入侧就剔除 —— 用的正是读面那把尺子（dockerTaskReadOnly），否则：
//     ① 订阅记录每台主机重连一次就多一行；② 每次打开容器详情页都会被塞进历史，
//     把真正要追的变更淹掉。
//   - **结论句与页面上的一句话逐字一致**：Summary 直接取 terminalSummary（任务
//     条目与结果审计共用的那个函数），写入时定稿 —— 读面不再重算，口径只有一处。
//   - **不脱敏、不加工**：结论句与目标原样存档（页面本来就照原样展示同一句话；
//     脱敏是审计面的治理口径，这里复制一份只会在两处口径分岔）。
type DockerTaskHistoryRecorder struct {
	writer DockerTaskHistoryWriter
	log    logger.LoggerInterface
}

// DockerTaskHistoryWriter 是历史写入的窄接口（由 repository.DockerTaskHistoryRepo 满足）。
type DockerTaskHistoryWriter interface {
	Upsert(ctx context.Context, row *entity.DockerTaskHistory) error
}

// dockerTaskTextMax 是 target/summary 两列的单值上限（字节），与审计正文同档
// （middleware.MaxLogBodyBytes = 4096）。
//
// 列是 text 而不是定宽 varchar，这里的截断只是**行宽兜底**：target 在上游没有
// 长度契约（受理侧不限长，一个超长目标不该把落库打成「数据太长」错误），而截断
// 标记（"...(truncated)"）是读者识别「被截断」的显式信号（与审计同款文案）。
const dockerTaskTextMax = middleware.MaxLogBodyBytes

// NewDockerTaskHistoryRecorder 构造历史持久面（nil writer = 未装配，Record 是空操作）。
func NewDockerTaskHistoryRecorder(w DockerTaskHistoryWriter, log logger.LoggerInterface) *DockerTaskHistoryRecorder {
	return &DockerTaskHistoryRecorder{writer: w, log: log}
}

// Record 是 dockerstate.CmdStore 终态钩子的实现：把一条终态指令折成历史行落库。
//
// 幂等由表的 ref 唯一键兜底（Upsert）：迟到的 result 覆盖 timeout、重复 result
// 重放，都收敛到同一行（与 Redis 记录逐字段一致 —— 实时胜的前提是两边说同一句话）。
func (r *DockerTaskHistoryRecorder) Record(ctx context.Context, rec *dockerstate.CmdRecord) {
	if r == nil || r.writer == nil || rec == nil {
		return
	}
	if rec.UserID == 0 {
		// docker:events 的常驻订阅记录不是用户任务（无发起人）—— 与读面同一条剔除。
		return
	}
	if dockerTaskReadOnly(rec.Action) {
		// 只读动作不进任务历史（读面剔除的写入侧镜像，理由见文件头）。
		return
	}
	row := &entity.DockerTaskHistory{
		// 行 ID 走全库同一条雪花回调（零值即生成）；CreatedBy/UpdatedBy 不在这里写
		// —— 审计字段由 GORM 回调从登录态补（agent 通道无登录态，留空是如实的，
		// 发起人在 UserID 列上）。
		Ref:        rec.Ref,
		DeviceID:   rec.DeviceID,
		Action:     rec.Action,
		Target:     middleware.TruncateString(rec.Target, dockerTaskTextMax),
		UserID:     rec.UserID,
		Status:     rec.Status,
		Summary:    middleware.TruncateString(terminalSummary(rec), dockerTaskTextMax),
		AcceptedAt: rec.CreatedAt,
		FinishedAt: rec.FinishedAt,
	}
	if err := r.writer.Upsert(ctx, row); err != nil && r.log != nil {
		// 历史落库失败不阻塞主链（Redis 侧已经写成功）：结果是可观测的（任务中心
		// 的实时面/轮询/流），历史是它的持久化副本 —— 丢一份副本比卡住结果回写轻。
		r.log.Warn("docker task history 落库失败（实时通道不受影响）",
			zap.String("ref", rec.Ref), zap.String("status", rec.Status), zap.Error(err))
	}
}
