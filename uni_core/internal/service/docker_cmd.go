package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 域的**下发面**：受理、跟踪、结果查询、sweep（读面在 docker.go）。
//
// 三个依赖各自对应一条纪律：
//   - dockerpolicy 是权限码/超时/期次的唯一事实源（本文件不写任何 action 相关的策略）；
//   - agenthub 是唯一的送达路径（本域不碰 socket，也不缓存连接）；
//   - dockerstate 承担状态机（记录/在飞索引/到期索引），本文件只做「一次性编排」。

// DockerCmdSender 是下发一条指令的能力面（由 agenthub.Hub 满足）。
//
// 它必须返回 agenthub.ErrDeviceOffline —— 受理流程据此把它折成「不受理、不发 ref」：
// 给一台离线设备发一个 ref，只会让用户对着「超时」等 30 秒，而实际原因是它没连上。
type DockerCmdSender interface {
	SendToDevice(deviceID uint64, msg *agentproto.Message) error
}

// DockerSessionCounter 是「该设备当前有几条流会话」的能力面（由 dockerstream.Registry 满足）。
//
// 上限与 agent 侧同值（3）；core 在受理处先拒是为了给出**更好的结论句** ——
// agent 的 errStreamLimit 要等一条 result 回来才可见，而用户在点下「终端」时
// 就该知道「先关掉其它日志或终端」。
type DockerSessionCounter interface {
	CountByDevice(deviceID uint64) int
}

// DockerCmdService 是 docker 域的下发面。
type DockerCmdService struct {
	cmds   *dockerstate.CmdStore
	sender DockerCmdSender
	// store 是快照读取面：受理前用它确认这台主机的 docker **此刻可用**
	//（见 Send 里的能力闸；下发面的其余部分不需要快照）。
	store *dockerstate.Store
	log   logger.LoggerInterface
	// now 可替换（测试要一个确定的现在；sweep 的到期判定也走它）。
	now func() time.Time
	// idGen 生成指令号（ref）。字段而非常量：装配与测试可以替换成
	// 进程级雪花节点生成的十进制串（见 newDockerRef 的说明）。
	idGen func() string
	// sessions 是流会话计数面（三期）。nil = 未装配：跳过上限预检
	//（agent 侧仍有 3 条上限兜底），不影响其余受理路径。
	sessions DockerSessionCounter
}

// WithStreamSessions 注入流会话计数面（三期日志 Follow / 终端的上限预检）。
func (s *DockerCmdService) WithStreamSessions(c DockerSessionCounter) *DockerCmdService {
	s.sessions = c
	return s
}

// NewDockerCmdService 构造下发面。
func NewDockerCmdService(cmds *dockerstate.CmdStore, sender DockerCmdSender,
	store *dockerstate.Store, log logger.LoggerInterface) *DockerCmdService {
	return &DockerCmdService{cmds: cmds, sender: sender, store: store, log: log,
		now: time.Now, idGen: newDockerRef}
}

// createsStreamSession 报告一条指令是否会建立流会话（上限预检的依据）：
// 会话制 action（exec），或带 follow=true 的 container:logs。
func createsStreamSession(pol dockerpolicy.Policy, action string, opts *agentproto.DockerCmdOptions) bool {
	if pol.Session {
		return true
	}
	return action == agentproto.DockerActionContainerLogs && opts != nil && opts.Follow
}

// newDockerRef 是 idGen 的缺省实现。
//
// 复用升级域的 newRequestID（纳秒时间戳 + 进程内自增序号）而不是自己造一套，
// 也不用雪花：本服务**拿不到进程的雪花节点** —— 它由 main.go 建好后交给 GORM 回调，
// workerId 只在那一处配置；在同进程里再建一个同 workerId 的节点会与既有节点撞号
// （同毫秒同序号即同一个 ID），那比复用同一套取号逻辑危险得多。ref 的要求只有两个 ——
// **十进制**（协议 isDecimalID 的硬要求：JS 侧不能丢精度）与**唯一**，而跨进程唯一性
// 由「多实例部署是已声明的限制」兜底（与 request_id 同一裁决）。
func newDockerRef() string { return newRequestID() }

// RequiredPerm 返回该 action 需要的权限码（供 handler 在受理前校验）。
func (s *DockerCmdService) RequiredPerm(action string) (string, bool) {
	return dockerpolicy.RequiredPerm(action)
}

// Send 受理一条指令：校验（策略/期次/确认/参数/能力）→ 去重 → 建记录 → 下发。
//
// 失败形态与 HTTP 语义的对应（spec §4.1）：
//   - 未登记 action / 参数不合法 / 期次未到 / 确认不符 → 400（apperror.BadRequest）；
//   - 同 (device, action, target) 已有在飞 → **409**（apperror.Conflict）；
//   - docker 不可用 / agent 离线 / 未送达 → **503** 语义。本仓库的 apperror 没有 503
//     构造函数（只有 4xx 一批 + Internal），故用 apperror.Internal 承载：500 与 503 同属
//     「服务端此刻给不出结果」的 5xx 家族，且它会让 app.Error 记一条错误日志。刻意不用
//     BadRequest —— 那会把「设备不在线」讲成「你的请求写错了」，前端据此就不会给重试入口；
//     也不与 Conflict 混用 —— 409 在本域已经被「同目标在飞」占用，两者的界面动作不同。
func (s *DockerCmdService) Send(ctx context.Context, userID, deviceID uint64, req *request.DockerCmdReq) (string, error) {
	pol, ok := dockerpolicy.Lookup(req.Action)
	if !ok {
		return "", apperror.BadRequest("未知操作")
	}
	if pol.Phase > dockerpolicy.CurrentPhase {
		// 期次闸：页面不该渲染这些按钮，服务端这道闸是它的兜底（curl 也过不去）。
		// 会话制指令（exec）三期已交付：它的期次由同一张表的 Phase 列表达，
		// 不再需要一条单独的分支（曾经的「三期未接线 → 一律拒绝」已随流通道落地）。
		return "", apperror.BadRequest("该操作尚未开放")
	}
	opts := toProtocolOptions(req)
	if err := agentproto.ValidateDockerCmdOptions(req.Action, opts); err != nil {
		return "", apperror.BadRequest("指令参数不合法")
	}
	if err := agentproto.CheckDockerConfirm(req.Action, opts, req.Confirm); err != nil {
		return "", apperror.BadRequest("缺少确认信息")
	}
	if s.store != nil {
		// 能力闸：agent 已自报 docker 不可用（快照 docker_ok=false）时不受理。
		// 它与「前端在 dockerOk=false 时不渲染任何操作按钮」是同一件事的两道防线
		//（不渲染 ≠ 禁用，服务端这道闸是兜底）：此刻下发只会换来一句「执行失败」，
		// 而在受理处就给出结论句，用户能立刻去修 docker，而不是等一次超时。
		//
		// 两处刻意的宽松：**从未上报**（env == nil）不算不可用 —— 快照是周期事件，
		// 刚接上线的 agent 第一帧还没到，它完全可能收得下这条指令；**读快照失败**
		// （Redis 抖动）也放行 —— 快照只是旁证，不能因为它读不到就关掉整条指令面。
		if env, err := s.store.Get(ctx, deviceID); err == nil && env != nil && !env.State.DockerOK {
			return "", apperror.Internal("该主机的 Docker 当前不可用，指令未下发")
		}
	}

	// 流会话上限**先拒**（三期）：与 agent 的会话槽位同上限（3）。放在在飞去重
	// 之前：这是「这台机器上已经开了几条流」的资源结论，与「同目标是否已有指令」
	// 无关 —— 用户先看到哪句更可行动，就该先说哪句。
	if s.sessions != nil && createsStreamSession(pol, req.Action, opts) &&
		s.sessions.CountByDevice(deviceID) >= dockerstream.MaxSessionsPerDevice {
		return "", apperror.Conflict("该主机同时在跑的流会话已达上限（最多 3 个），请先关闭其它日志或终端")
	}

	if ref, err := s.cmds.Inflight(ctx, deviceID, req.Action, opts.Target); err != nil {
		return "", apperror.Internal("内部错误", err)
	} else if ref != "" {
		return "", apperror.Conflict("该目标上已有同一条指令在执行")
	}

	rec := &dockerstate.CmdRecord{
		Ref: s.idGen(), DeviceID: deviceID, Action: req.Action, Target: opts.Target,
		UserID: userID, Perm: pol.Perm, Confirm: req.Confirm, CreatedAt: s.now().UnixMilli(),
	}
	// 会话制的 sweep 时限是**建立窗口**（见 dockerpolicy.SessionSetupTimeout）：
	// 会话建立后记录已终结，生命周期交给流通道。
	if err := s.cmds.Create(ctx, rec, pol.AcceptTimeout()); err != nil {
		return "", apperror.Internal("内部错误", err)
	}

	msg, err := agentproto.NewMessage(rec.Ref, agentproto.TypeCoreDockerCmd, &agentproto.DockerCmd{
		Ref: rec.Ref, Action: rec.Action, Options: *opts, Confirm: rec.Confirm,
	})
	if err != nil {
		s.discard(ctx, rec)
		return "", apperror.Internal("内部错误", err)
	}
	if err := s.sender.SendToDevice(deviceID, msg); err != nil {
		// 未送达：记录立刻撤掉（留着一个永远 pending 的记录会让同目标一直 409），
		// 并按「设备离线」这一条消息回 503 语义 —— 用户会立刻明白该去看设备，
		// 而不是等 30 秒超时后收到一句「指令超时未完成」。
		s.discard(ctx, rec)
		if errors.Is(err, agenthub.ErrDeviceOffline) {
			return "", apperror.Internal("设备当前离线，指令未下发")
		}
		return "", apperror.Internal("指令未送达设备，请稍后重试")
	}
	return rec.Ref, nil
}

// Lookup 读取一条指令记录（供轮询端点校验归属与设备匹配）。
func (s *DockerCmdService) Lookup(ctx context.Context, deviceID uint64, ref string) (*dockerstate.CmdRecord, error) {
	rec, err := s.cmds.Get(ctx, ref)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	if rec == nil || rec.DeviceID != deviceID {
		// 设备不匹配与不存在**同码**：否则 ref 就成了一把探测
		// 「别的主机上有什么指令」的钥匙（ref 是十进制串，可枚举）。
		return nil, apperror.NotFound("指令不存在或已过期")
	}
	return rec, nil
}

// Result 把记录折成轮询响应。
func (s *DockerCmdService) Result(rec *dockerstate.CmdRecord) *response.DockerCmdResultResp {
	out := &response.DockerCmdResultResp{
		Status:        rec.Status,
		Error:         rec.Error,
		Detail:        rec.Detail,
		SessionID:     rec.SessionID,
		AlreadyExists: rec.AlreadyExists,
	}
	if len(rec.Payload) > 0 {
		// 载荷原样透出（已通过协议校验）：前端按 action 解析成具体形状。
		// 用 json.RawMessage 而不是 []byte —— 后者的 JSON 形态是 base64 串，
		// 前端拿到的会是一坨需要再解码的字符串，而不是日志文本/inspect 数据本身。
		out.Payload = json.RawMessage(rec.Payload)
	}
	return out
}

// Sweep 终结到期指令（由 wireup 的后台任务周期调用）。
//
// 返回条数供调用方判定「这一轮有没有事发生」：稳态下它恒为 0，突然不再是 0
// 就是「有 agent 掉线了/指令没回结果」的第一个可观测信号。
func (s *DockerCmdService) Sweep(ctx context.Context) (int, error) {
	n, err := s.cmds.Sweep(ctx, s.now(), 200)
	if err != nil {
		return 0, err
	}
	if n > 0 && s.log != nil {
		s.log.Info("docker cmd sweep 终结超时指令", zap.Int("count", n))
	}
	return n, nil
}

// discard 撤销一条未送达的记录（best-effort：失败只记日志，sweep 会兜住）。
func (s *DockerCmdService) discard(ctx context.Context, rec *dockerstate.CmdRecord) {
	if err := s.cmds.Timeout(ctx, rec); err != nil && s.log != nil {
		s.log.Warn("docker cmd discard failed", zap.String("ref", rec.Ref), zap.Error(err))
	}
}

// toProtocolOptions 把 HTTP 请求的 options 折成协议载荷。
//
// 三处看着多余、但不能省的转换：
//   - **target 从顶层并进 options**：HTTP 契约里 target 是顶层字段，而协议侧它属于
//     options（§4.3.1 总表是归属的唯一事实源）。合并写在唯一入口，调用方就不必记得
//     合并两次；
//   - **指针逐个解引用**：`all=false` 与「没传 all」语义不同（前者是「只清悬空」的
//     显式要求），用值类型会让前者被缺省值悄悄覆盖；
//   - **缺省 = 协议侧的缺省**：本函数不替协议编造值 —— 例如 Tail 不填就是协议的
//     「默认 100 行」。在这里编一个 100，会让「用户没选」与「用户选了 100」
//     在结果里再也分不开。
func toProtocolOptions(req *request.DockerCmdReq) *agentproto.DockerCmdOptions {
	opts := &agentproto.DockerCmdOptions{Target: req.Target}
	o := req.Options
	if o == nil {
		return opts
	}
	if o.Tail != nil {
		opts.Tail = *o.Tail
	}
	if o.Since != nil {
		opts.Since = *o.Since
	}
	if o.N != nil {
		// 复制值：协议载荷会被编码进消息，不该继续持有请求体里的指针
		//（请求体在异步路径上没人为它保证生命周期）。
		n := *o.N
		opts.N = &n
	}
	if o.Force != nil {
		opts.Force = *o.Force
	}
	if o.Overwrite != nil {
		opts.Overwrite = *o.Overwrite
	}
	if o.All != nil {
		opts.All = *o.All
	}
	if o.RemoveOrphans != nil {
		opts.RemoveOrphans = *o.RemoveOrphans
	}
	if o.Volumes != nil {
		opts.Volumes = *o.Volumes
	}
	if o.Follow != nil {
		opts.Follow = *o.Follow
	}
	// argv 复制一份：协议载荷会被编码进消息，不该继续持有请求体里的切片。
	opts.Command = slices.Clone(o.Command)
	opts.Filename = o.Filename
	opts.Content = o.Content
	opts.BaseHash = o.BaseHash
	opts.Src = o.Src
	opts.Dst = o.Dst
	if len(o.Patch) > 0 {
		// HTTP 侧 patch 是 json.RawMessage（原样收下的 JSON 文档），协议侧是
		// map[string]any。解不开时**保持空**：让协议校验把这条指令判成「参数不合法」
		//（400），而不是把半截 patch 当成合法值发给 agent 去改生产 YML。
		var patch map[string]any
		if err := json.Unmarshal(o.Patch, &patch); err == nil {
			opts.Patch = patch
		}
	}
	// 回滚令牌（四期）：形态校验在协议层（严格 `YYYYMMDD-HHMMSS`），core 只负责透传 ——
	// 这里不解析、不拼路径（路径由 agent 从自己的解析结果重建）。
	opts.Backup = o.Backup
	return opts
}
