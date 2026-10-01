package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
//   - dockerpolicy 是权限码/超时/会话制的唯一事实源（本文件不写任何 action 相关的策略）；
//   - agenthub 是唯一的送达路径（本域不碰 socket，也不缓存连接）；
//   - dockerstate 承担状态机（记录/在飞索引/到期索引），本文件只做「一次性编排」。

// DockerCmdSender 是下发一条指令的能力面（由 agenthub.Hub 满足）。
//
// 它必须返回 agenthub.ErrDeviceOffline —— 受理流程据此把它折成「不受理、不发 ref」：
// 给一台离线设备发一个 ref，只会让用户对着「超时」等 30 秒，而实际原因是它没连上。
type DockerCmdSender interface {
	SendToDevice(deviceID uint64, msg *agentproto.Message) error
}

// DockerSessionCounter 是「该设备当前有几条流会话」与「受理分发操作时预登记进度
// 会话」的能力面（由 dockerstream.Registry 满足）。
//
// 两个方法两件事：上限预检只看**用户流**（进度会话已被注册表排除在账目外）；
// Register 预登记的正是进度会话 —— core 在**受理时**登记、agent 在开始执行时
// 用协议派生的同一句柄开会话（pull_<ref>/build_<ref>/push_<ref>），帧才能在指令
// pending 期间找到主人（4b 的关键时序，P2 起三族共用；选型论证见 pull_progress
// 侧与协议注释）。
type DockerSessionCounter interface {
	CountByDevice(deviceID uint64) int
	Register(meta dockerstream.Meta) error
}

// RegistryAuthResolver 解出仓库凭据的认证三元组（由 service.DockerRegistryService 满足）。
//
// 这是**唯一**的解密读口（4c，选型 A）：password 只被本条指令的消息持有，
// 调用方拿到后不得落日志、不得存入任何结构。found=false 且 err=nil = 凭据不存在；
// err!=nil = 主密钥缺失/密文损坏（由调用方折成「凭据不可用」的 500 结论句）。
type RegistryAuthResolver interface {
	ResolveAuth(ctx context.Context, registry string) (username, password string, found bool, err error)
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
	// auth 是仓库凭据解析面（4c）。nil = 未装配：带 registry 的拉取被 500 拒
	//（「配了选项却没有解析能力」是装配错误，早暴露好过 agent 在 daemon 上吃 401）。
	auth RegistryAuthResolver
	// scanCache 是镜像扫描缓存面（P3·安全面）。nil = 未装配：image:scan 照常
	// 下发 agent 真扫描 —— 缓存只是加速器，缺失不挡功能（与 stats-history 挂钩
	// 的「观察者不是参与者」同一纪律）。
	scanCache *dockerstate.ScanCacheStore
}

// WithStreamSessions 注入流会话计数/登记面（三期日志 Follow / 终端的上限预检；
// 4b 拉取进度会话的受理时预登记）。
func (s *DockerCmdService) WithStreamSessions(c DockerSessionCounter) *DockerCmdService {
	s.sessions = c
	return s
}

// WithRegistryAuth 注入仓库凭据解析面（4c）：只有 image:pull 且 options.registry
// 非空时才被调用 —— 其余 action 连解析器都不经过。
func (s *DockerCmdService) WithRegistryAuth(r RegistryAuthResolver) *DockerCmdService {
	s.auth = r
	return s
}

// WithScanCache 注入镜像扫描缓存面（P3·安全面）。装配在 wireup 一处完成；
// 受理侧（image:scan 的秒回快路径）与 ingest 侧（成功扫描落缓存）共用同一个
// store 实例 —— 两边读写的是同一族键。
func (s *DockerCmdService) WithScanCache(c *dockerstate.ScanCacheStore) *DockerCmdService {
	s.scanCache = c
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

// progressSessionOf 按 action 给出**进度会话**的派生句柄与种类（4b/P2 —— pull/
// build/push 三族都在受理时预登记，句柄两端同源：core 在这里、agent 在写执行器里
// 用同一个协议派生函数）。returns false for 不产进度会话的 action。
func progressSessionOf(action, ref string) (sessionID string, kind dockerstream.Kind, ok bool) {
	switch action {
	case agentproto.DockerActionImagePull:
		return agentproto.DockerPullSessionID(ref), dockerstream.KindPull, true
	case agentproto.DockerActionImageBuild:
		return agentproto.DockerBuildSessionID(ref), dockerstream.KindBuild, true
	case agentproto.DockerActionImagePush:
		return agentproto.DockerPushSessionID(ref), dockerstream.KindPush, true
	}
	return "", 0, false
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

// Send 受理一条指令：校验（策略/确认/参数/能力）→ 去重 → 建记录 → 下发。
//
// 失败形态与 HTTP 语义的对应（spec §4.1）：
//   - 未登记 action / 参数不合法 / 确认不符 → 400（apperror.BadRequest）；
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
	// 7c 删掉了这里曾经的期次闸（Phase > CurrentPhase → 400「该操作尚未开放」）：
	// CurrentPhase=5 且全表 action 期次 ≤5，那道闸永不触发，Phase 列已从策略表删除。
	// 「尚未开放」的语义仍活在 agent 分派器的能力差集上（这一版 agent 没实现的
	// action 由 agent 回结论句），core 受理处不再有第二道发布节奏闸。
	opts := toProtocolOptions(req)
	if err := agentproto.ValidateDockerCmdOptions(req.Action, opts); err != nil {
		return "", apperror.BadRequest("指令参数不合法")
	}
	if err := agentproto.CheckDockerConfirm(req.Action, opts, req.Confirm); err != nil {
		return "", apperror.BadRequest("缺少确认信息")
	}

	// 仓库凭据注入（4c，选型 A）：image:pull / image:push 带 registry 时，受理处
	// 解出凭据、随指令消息瞬时下发 —— 凭据读取**只发生在这里**（鉴权/审计链路上
	// 的任何其它环节都接触不到密码）。P2 起推送与拉取共用同一条注入路径。
	// 三个结论：
	//   - 未装配解析器 → 500：配了选项却没有解析能力是装配错误，早暴露好过
	//     agent 在 daemon 上吃一个没法解释的 401；
	//   - 凭据不存在 → 400 结论句「没有这个仓库的凭据」（删除即失效就在这里兑现）；
	//   - 解密失败/主密钥缺失 → 500 结论句（服务端问题，不是请求写错了）。
	// 解出的明文只进 imageAuth 局部变量，绝不进记录/日志/审计。
	var imageAuth *agentproto.DockerRegistryAuth
	if (req.Action == agentproto.DockerActionImagePull ||
		req.Action == agentproto.DockerActionImagePush) && opts.Registry != "" {
		if s.auth == nil {
			return "", apperror.Internal("仓库凭据服务未装配，指令未下发")
		}
		username, password, found, err := s.auth.ResolveAuth(ctx, opts.Registry)
		if err != nil {
			return "", apperror.Internal("仓库凭据不可用，请通知管理员检查主密钥配置")
		}
		if !found {
			return "", apperror.BadRequest("没有这个仓库的凭据")
		}
		imageAuth = &agentproto.DockerRegistryAuth{Registry: opts.Registry, Username: username, Password: password}
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

	// 扫描缓存快路径（P3·安全面，见 scanFastPath 的口径说明）：排在在飞去重
	// **之后** —— 同目标正有一场真扫描在跑时，409 优先于缓存秒回（在飞的那次
	// 扫完会把缓存换新，现在秒回一份旧的反而误导）。
	if ref, hit, err := s.scanFastPath(ctx, userID, deviceID, req, opts, pol); err != nil {
		return "", err
	} else if hit {
		return ref, nil
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

	// 进度会话的**预登记**（4b/P2）：必须在消息下发之前 —— agent 收到指令即可开始
	// 拉取/构建/推送，帧可能先于任何 result 到达，届时注册表里必须已经有主人。
	// 句柄两端同源（协议 DockerPullSessionID / DockerBuildSessionID /
	// DockerPushSessionID），result 不上句柄（它只在操作结束时回）。
	//
	// 登记失败只记日志不判死指令：进度不可用是可见性的损失，操作本身照常（旧流程
	// 最坏就是退回「黑盒等待」，与 4b 之前的形态一致）。
	if s.sessions != nil {
		if sid, kind, ok := progressSessionOf(req.Action, rec.Ref); ok {
			if err := s.sessions.Register(dockerstream.Meta{
				SessionID: sid,
				DeviceID:  rec.DeviceID,
				UserID:    rec.UserID,
				Action:    rec.Action,
				Ref:       rec.Ref,
				Kind:      kind,
				CreatedAt: s.now(),
			}); err != nil && s.log != nil {
				s.log.Warn("docker progress session 预登记失败（进度流不可用，指令照常）",
					zap.String("ref", rec.Ref), zap.String("action", rec.Action), zap.Error(err))
			}
		}
	}

	msg, err := agentproto.NewMessage(rec.Ref, agentproto.TypeCoreDockerCmd, &agentproto.DockerCmd{
		Ref: rec.Ref, Action: rec.Action, Options: *opts, Confirm: rec.Confirm,
		// 4c：凭据随指令瞬时注入（无凭据的拉取/推送 auth 为 nil —— 与 4b 之前的
		// 消息逐字一致，agent 侧行为不变）。
		Auth: imageAuth,
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

// scanFastPath 是 image:scan 的缓存秒回路径（P3·安全面）。命中时受理一条指令
// 记录并**立刻**以缓存报告终结它 —— 不下发 agent。
//
// 四条口径（裁决都写在这里，别处不再复述）：
//   - **照常走 cmd、任务中心留痕**：不是「直接回 HTTP 响应里的报告」。受理返回
//     ref、轮询拿结果、任务中心出现一条 succeeded 的 image:scan —— 与真扫描
//     **同一个交互形状**（用户点按钮 → 任务出现 → 结果回来），差别只是秒回。
//     跳过记录的「HTTP 直回报告」省一次轮询，代价是任务中心看不到这次扫描
//     （「刚点了没反应」的焦虑恰恰要靠这条记录缓解）与轮询端点的归属校验
//     被绕开（result 通道的权限与审计链路就断了一截）；
//   - **不审计**：缓存的 Complete 不经 CompleteDockerCmd 的审计挂钩 —— 审计
//     记录的是「在这台主机上执行了什么」的事实，缓存回放是一次读，不是一次
//     执行（与 sweep 的 timeout 不入审计同一裁决：推断/回放不是事实）；
//   - **不依赖主机在线**：报告是镜像**内容**的事实（内容寻址键），缓存里有的
//     话离线也能回 —— 主机掉线期间用户仍能看到「N 小时前扫的报告」；
//   - **缓存只是加速器**：解析不到内容键（快照没有这个 tag）、读缓存失败
//     （Redis 抖动）、没扫过 —— 一律回落真扫描（见各分支注释），缓存面故障
//     的最坏结果是「不秒回」，不是「不能扫」。
func (s *DockerCmdService) scanFastPath(ctx context.Context, userID, deviceID uint64,
	req *request.DockerCmdReq, opts *agentproto.DockerCmdOptions, pol dockerpolicy.Policy) (string, bool, error) {
	if s.scanCache == nil || req.Action != agentproto.DockerActionImageScan {
		return "", false, nil
	}
	imageID, ok := s.scanImageIDOf(ctx, deviceID, opts.Target)
	if !ok {
		// 快照里找不到这个 target（刚 pull 快照未更新 / 目标是截断 ID）：
		// 不是错误 —— agent 的扫描前置里有 ImageInspect，它才是「镜像存不存在」
		// 的权威判定；这里只是解析不出内容键，回落真扫描（扫完报告会带 image_id
		// 把缓存补上，下一扫即可命中）。
		return "", false, nil
	}
	report, err := s.scanCache.Get(ctx, imageID)
	if err != nil {
		// Redis 抖动：回落真扫描并留一条日志（零日志会让「为什么突然不秒回了」
		// 成为无迹可查的谜）。快照读失败在能力闸里也是同一条「放行」纪律 ——
		// 旁证不可用时不能把功能关掉。
		if s.log != nil {
			s.log.Warn("docker scan cache 读取失败（回落真扫描）",
				zap.Uint64("deviceId", deviceID), zap.String("imageId", imageID), zap.Error(err))
		}
		return "", false, nil
	}
	if report == nil {
		return "", false, nil // 没扫过 / TTL 已过：正常的「没有缓存」。
	}
	rec := &dockerstate.CmdRecord{
		Ref: s.idGen(), DeviceID: deviceID, Action: req.Action, Target: opts.Target,
		UserID: userID, Perm: pol.Perm, Confirm: req.Confirm, CreatedAt: s.now().UnixMilli(),
	}
	// Create + 立刻 Complete：Create 先把记录写进任务中心索引与在飞索引（留痕），
	// Complete 随即终结它并清掉在飞/到期索引 —— 记录只以「已终结」形态存在，
	// 不会挡住同目标的后续指令。Create 的 timeout 形参在此只是到期索引的登记
	// 值（Complete 会把索引摘掉，值本身不再有意义）。
	if err := s.cmds.Create(ctx, rec, pol.AcceptTimeout()); err != nil {
		return "", false, apperror.Internal("内部错误", err)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		s.discard(ctx, rec)
		return "", false, apperror.Internal("内部错误", err)
	}
	if err := s.cmds.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: true, Payload: payload}); err != nil {
		s.discard(ctx, rec)
		return "", false, apperror.Internal("内部错误", err)
	}
	return rec.Ref, true, nil
}

// scanImageIDOf 从快照解析 target 的镜像内容键（完整镜像 ID）。
//
// 命中口径：target 等于镜像条目的 ID，或等于 RepoTags 之一；target 不带 tag
// 时按 daemon 的解析口径补隐式 :latest 再试一次（快照的 RepoTags 总是显式
// tag，「nginx」与「nginx:latest」是同一个镜像）。取不到（没这个条目 / ID
// 形态异常）返回 false —— 调用方回落真扫描，绝不在这里猜一个键。
func (s *DockerCmdService) scanImageIDOf(ctx context.Context, deviceID uint64, target string) (string, bool) {
	if s.store == nil {
		return "", false // 未装配快照面（测试构造形态）：没有旁证，回落真扫描。
	}
	env, err := s.store.Get(ctx, deviceID)
	if err != nil || env == nil {
		return "", false // 快照读失败/从未上报：没有旁证，回落真扫描。
	}
	ref := defaultTagOf(target)
	for i := range env.State.Images {
		img := &env.State.Images[i]
		if img.ID == target ||
			slices.Contains(img.RepoTags, target) ||
			slices.Contains(img.RepoTags, ref) {
			if dockerstate.IsDockerScanImageID(img.ID) {
				return img.ID, true
			}
			// 形态异常的 ID（老 agent/畸形快照）：宁可回落真扫描也不用它当键 ——
			// 键空间只有「sha256:hex64」一种形状（scan_cache 的形态闸同源）。
			return "", false
		}
	}
	return "", false
}

// defaultTagOf 给不带 tag 的镜像引用补 :latest（与 docker CLI 的解析口径一致；
// 逻辑与 agent 侧 write.go 的 defaultImageTag 互为镜像 —— 两模块各自需要、协议
// 不导出「补 tag」是因为它是 CLI 行为的复刻而不是线上契约）。digest 形态
// （repo@sha256:…）不可再补 tag，原样交出。
func defaultTagOf(ref string) string {
	if strings.ContainsRune(ref, '@') {
		return ref
	}
	seg := ref
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	if seg == "" || strings.ContainsRune(seg, ':') {
		return ref
	}
	return ref + ":latest"
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
	// 创建面（4a）：全部照抄（校验在协议层）。切片复制而不持有请求体引用
	//（与 Command 同纪律：载荷会编码进消息，请求体的生命周期没人保证）。
	opts.Image = o.Image
	opts.Name = o.Name
	opts.Ports = slices.Clone(o.Ports)
	opts.Env = slices.Clone(o.Env)
	opts.Mounts = slices.Clone(o.Mounts)
	opts.RestartPolicy = o.RestartPolicy
	opts.CPULimit = o.CPULimit
	opts.MemLimitMB = o.MemLimitMB
	opts.Network = o.Network
	if o.Start != nil {
		// 复制值：协议载荷不该继续持有请求体里的指针（同 N 的纪律）。
		s := *o.Start
		opts.Start = &s
	}
	// 4c：registry 是凭据键（不含秘密），原样进协议载荷 —— 密码在 Send 的
	// 受理段才解出注入（auth 不进 options，见协议 DockerCmd.Auth 的说明）。
	opts.Registry = o.Registry
	// P2：build 四个选项照抄（校验在协议层；args 复制一份 —— 载荷会编码进
	// 消息，不该持有请求体里的 map 引用）。
	opts.Context = o.Context
	opts.Dockerfile = o.Dockerfile
	opts.Tag = o.Tag
	if len(o.Args) > 0 {
		opts.Args = make(map[string]string, len(o.Args))
		for k, v := range o.Args {
			opts.Args[k] = v
		}
	}
	return opts
}
