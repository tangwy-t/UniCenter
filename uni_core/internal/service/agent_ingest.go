package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// AgentDeviceRepository 是本服务真正用到的设备仓储方法集。
type AgentDeviceRepository interface {
	Create(ctx context.Context, d *entity.Device) error
	FindByID(ctx context.Context, id uint64) (*entity.Device, error)
	FindByInstanceID(ctx context.Context, instanceID string) (*entity.Device, error)
	FindByTokenHash(ctx context.Context, hash string) (*entity.Device, error)
	UpdateEnroll(ctx context.Context, d *entity.Device) error
	RefreshStaticFromHello(ctx context.Context, d *entity.Device) error
	Touch(ctx context.Context, id uint64, at time.Time) error
}

// AgentRawStore 是热层原始窗的能力面。
type AgentRawStore interface {
	Append(ctx context.Context, deviceID uint64, sample *agentproto.MetricsSample) error
}

// AgentLatestStore 是水位的能力面。
type AgentLatestStore interface {
	Set(ctx context.Context, deviceID uint64, sample *agentproto.MetricsSample) error
}

// AgentConfigGetter 读取运行期可热更的 agent 配置。
type AgentConfigGetter interface {
	GetString(ctx context.Context, key, defaultVal string) string
	GetInt(ctx context.Context, key string, defaultVal int) int
}

const (
	// ConfigEnrollToken 是 enroll 校验用的共享令牌（spec §10.2）。
	ConfigEnrollToken = "sys.agent.enrollToken"
)

// AgentIngestService 负责 agent 侧的全部入站逻辑：注册、鉴权、心跳、指标入湖。
//
// **只写 Redis，不碰 DB 的指标表**（spec §7.1）：DB 的落库由 2C 的 flush 负责。
type AgentIngestService struct {
	repo   AgentDeviceRepository
	raw    AgentRawStore
	latest AgentLatestStore
	// docker / cmd 是 docker 域的两个存储（快照与指令记录）：agent 通道的
	// docker 三面（快照落库、结果回写）由本服务承接，读面与下发面见 C6 的两个服务。
	docker *dockerstate.Store
	cmd    *dockerstate.CmdStore
	cfg    AgentConfigGetter
	log    logger.LoggerInterface
	// streams 是流会话登记面（三期；由 dockerstream.Registry 实现）。
	// nil = 未装配：result 里的 session_id 被忽略（流端点会因此找不到会话），
	// 不影响指令结果回写本身。
	streams DockerSessionRegisterer
	// events 是事件流常驻订阅的结果出口（六期；由 dockerevents.Manager 实现）。
	// nil = 未装配：docker:events 的 result 照常回写完记录即止（不会进用户流注册表，
	// 常驻管理器不在，这条指令也就没有别的去向）。
	events DockerEventsSinker
	// audit 是终态审计挂钩（6c；由 DockerCmdAuditor 实现）。
	// nil = 未装配：结果照常回写，审计关闭 —— 挂钩是**观察者不是参与者**，
	// 它的缺失只关治理可视性，不关指令功能。
	audit DockerTerminalAuditor
	// statsHistory 是快照 ingest 的 stats 留存挂钩（P2；由 dockerstate.StatsHistoryStore
	// 实现）：把每帧快照的容器 CPU/内存投影进 30 分钟环形序列。nil = 未装配：
	// 快照照常落库，留存关闭 —— 同样是观察者不是参与者，它的缺失只关抽屉的
	// 历史曲线，不关快照主链。
	statsHistory DockerStatsRecorder
	// scanCache 是 image:scan 结果的缓存写入面（P3·安全面；由 dockerstate.ScanCacheStore
	// 实现）：一次成功扫描回写后把报告按镜像内容键写进 24h 缓存。nil = 未装配：
	// 结果照常回写，缓存关闭 —— 观察者纪律同上：它的缺失只影响下一次同镜像扫描
	// 的秒回，不影响指令主链。
	scanCache DockerScanRecorder
	// now 是扫描缓存挂钩的时钟（可替换 —— 测试要一个确定的收帧时刻来断言
	// scanned_at 的重盖语义；与 DockerCmdService.now 同一条纪律）。
	now func() time.Time
}

// DockerSessionRegisterer 是登记一条流会话的能力面（由 dockerstream.Registry 满足）。
type DockerSessionRegisterer interface {
	Register(meta dockerstream.Meta) error
}

// DockerEventsSinker 是常驻订阅指令的结果出口（由 dockerevents.Manager 满足）。
//
// 为什么 dockerevents 不进 DockerSessionRegisterer：常驻订阅的用户态（UserID）为零、
// 且不属于用户流注册表的账（槽位/sweep/接入语义全部不同）—— 结果必须原样交还给
// **发起它的**管理器认领（按 ref），而不是登记成一条用户会话。
type DockerEventsSinker interface {
	OnEventsResult(deviceID uint64, res *agentproto.DockerCmdResult)
}

// DockerTerminalAuditor 是终态审计的入口（由 DockerCmdAuditor 满足）：ingest 在
// 完成一次真实的终态转换后调它，异步入账一条执行结果（见 docker_audit.go 的
// 「观察者不是参与者」纪律）。
type DockerTerminalAuditor interface {
	RecordTerminal(rec *dockerstate.CmdRecord)
}

// DockerStatsRecorder 是快照 ingest 的 stats 留存挂钩入口（由
// dockerstate.StatsHistoryStore 满足）：每帧快照落库后调它，把容器读数投影进
// 环形序列（30 分钟窗口，见 dockerstate/stats_history.go 的键设计与体积账）。
type DockerStatsRecorder interface {
	Record(ctx context.Context, deviceID uint64, st *agentproto.DockerState, at time.Time) error
}

// DockerScanRecorder 是 image:scan 结果的缓存写入面（由 dockerstate.ScanCacheStore
// 满足）：一次成功扫描的 result 回写完成后调它，报告按镜像内容键落进 24h 缓存
// （键设计与「缓存 DTO 与报告同形」的口径见 dockerstate/scan_cache.go）。
type DockerScanRecorder interface {
	Save(ctx context.Context, imageID string, report *agentproto.DockerScanReport, at time.Time) error
}

func NewAgentIngestService(repo AgentDeviceRepository, raw AgentRawStore, latest AgentLatestStore,
	docker *dockerstate.Store, cmd *dockerstate.CmdStore,
	cfg AgentConfigGetter, log logger.LoggerInterface) *AgentIngestService {
	return &AgentIngestService{repo: repo, raw: raw, latest: latest,
		docker: docker, cmd: cmd, cfg: cfg, log: log, now: time.Now}
}

// WithDockerSessions 注入流会话登记面（三期）。装配在 wireup 一处完成。
func (s *AgentIngestService) WithDockerSessions(r DockerSessionRegisterer) *AgentIngestService {
	s.streams = r
	return s
}

// WithDockerEvents 注入事件流常驻订阅的结果出口（六期）。装配在 wireup 一处完成。
func (s *AgentIngestService) WithDockerEvents(e DockerEventsSinker) *AgentIngestService {
	s.events = e
	return s
}

// WithDockerAudit 注入终态审计挂钩（6c）。装配在 wireup 一处完成。
func (s *AgentIngestService) WithDockerAudit(a DockerTerminalAuditor) *AgentIngestService {
	s.audit = a
	return s
}

// WithDockerStatsHistory 注入 stats 留存挂钩（P2）。装配在 wireup 一处完成。
func (s *AgentIngestService) WithDockerStatsHistory(r DockerStatsRecorder) *AgentIngestService {
	s.statsHistory = r
	return s
}

// WithDockerScanCache 注入扫描缓存写入面（P3·安全面）。装配在 wireup 一处完成；
// 与 DockerCmdService 的读面共用同一个 store 实例。
func (s *AgentIngestService) WithDockerScanCache(c *dockerstate.ScanCacheStore) *AgentIngestService {
	if c != nil {
		s.scanCache = c
	}
	return s
}

// nowValue 返回注入挂钟（未注入时 time.Now —— 构造函数已填缺省，这里兜底
// 零值形态的测试构造）。
func (s *AgentIngestService) nowValue() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Enroll 处理带 enroll_token 的首次注册。
//
// 幂等键是 instance_id（device 表上有 UNIQUE 约束兜底并发）：
//   - 已存在 → 复用既有 device_id，并**轮换** agent_token（同机重装/重注册的正常路径）
//   - 不存在 → 新建设备，签发 device_id 与 agent_token
//
// 返回的 agent_token 是**明文**（只此一次），DB 里只存 sha256。
func (s *AgentIngestService) Enroll(ctx context.Context, h *agentproto.Hello, remoteIP string) (uint64, string, error) {
	if h == nil {
		return 0, "", apperror.BadRequest("缺少 hello 载荷")
	}
	want := s.cfg.GetString(ctx, ConfigEnrollToken, "")
	if want == "" {
		return 0, "", apperror.Internal("sys.agent.enrollToken 未配置")
	}
	if h.EnrollToken != want {
		return 0, "", apperror.BadRequest("enroll token 无效")
	}

	token, hash, err := newAgentToken()
	if err != nil {
		return 0, "", apperror.Internal("生成 agent token 失败", err)
	}

	existing, err := s.repo.FindByInstanceID(ctx, h.InstanceID)
	if err == nil && existing != nil {
		// 已注册：更新库存字段 + 轮换 token_hash。
		// **不动 status**：停用态的机器重新 enroll 不自动启用，
		// 否则会绕过后台「停用」的管理意图（spec §10.2）。
		existing.Hostname = h.Hostname
		existing.OS = h.OS
		existing.Arch = h.Arch
		existing.Kernel = h.Kernel
		existing.AgentVersion = h.AgentVersion
		existing.Platform = h.Platform
		existing.PlatformVer = h.PlatformVer
		existing.CPUModel = h.CPUModel
		existing.CPUCores = h.CPUCores
		existing.MemTotalMB = h.MemTotalMB
		existing.BootTime = h.BootTime
		existing.TokenHash = hash
		existing.AgentUpgradeSupported = boolToInt8(h.UpgradeSupported)
		// 每次 enroll 都刷新观测 IP（设备换网络/换机房后重新注册即更新）。
		// 空串**不覆盖**已有值：remoteIP 取不到时（测试态未接管 socket、
		// 或某些 net.Addr 实现拿不到 host）宁可保留上次观测到的值，
		// 也不要因为一次取不到就把设备 IP 擦成空 —— 那会让 UI 突然显示「—」，
		// 而真实原因只是本次观测失败。
		if remoteIP != "" {
			existing.PrimaryIP = remoteIP
		}
		if err := s.repo.UpdateEnroll(ctx, existing); err != nil {
			return 0, "", apperror.Internal("更新设备注册信息失败", err)
		}
		s.log.Info("agent re-enrolled, token rotated",
			zap.Uint64("deviceId", existing.ID), zap.String("instanceId", h.InstanceID))
		return existing.ID, token, nil
	}

	d := &entity.Device{
		InstanceID: h.InstanceID, Hostname: h.Hostname, OS: h.OS, Arch: h.Arch,
		Kernel: h.Kernel, AgentVersion: h.AgentVersion, Platform: h.Platform,
		PlatformVer: h.PlatformVer, CPUModel: h.CPUModel, CPUCores: h.CPUCores,
		MemTotalMB: h.MemTotalMB, BootTime: h.BootTime,
		Status:    entity.DeviceStatusEnabled,
		TokenHash: hash,
		// 自报的升级能力：0.1.0 等老 agent 不发该字段 → 0（不支持远程升级），
		// 控制台据此禁用按钮并给出结论式提示，而不是让人对着「点了没动静」猜。
		AgentUpgradeSupported: boolToInt8(h.UpgradeSupported),
		// 来源 IP 来自服务端观测（socket/代理头），不是 hello 载荷里的自述。
		PrimaryIP: remoteIP,
	}
	if err := s.repo.Create(ctx, d); err != nil {
		// 并发 enroll：唯一约束冲突说明另一请求已建好同一台设备，回读它（幂等）。
		//
		// 回读成功后**必须把本次签发的 hash 落库再返回**：我们返回给客户端的明文
		// token 是本次新生成的（token 的明文只存在于本次调用的内存里，事后无从取回），
		// 若只回读 device 而不落 hash，返回的 token 的 sha256 就不在库中
		// —— 客户端拿它鉴权必然失败（enroll 成功却立刻 401）。
		// 语义上也与「已注册 → 轮换 token」一致：并发窗口内两者本就是同一次注册。
		if database.IsDuplicateKey(err) {
			if again, e2 := s.repo.FindByInstanceID(ctx, h.InstanceID); e2 == nil && again != nil {
				again.TokenHash = hash
				if e3 := s.repo.UpdateEnroll(ctx, again); e3 != nil {
					return 0, "", apperror.Internal("并发注册后更新设备失败", e3)
				}
				s.log.Info("agent enroll raced, token rotated on existing device",
					zap.Uint64("deviceId", again.ID), zap.String("instanceId", h.InstanceID))
				return again.ID, token, nil
			}
		}
		return 0, "", apperror.Internal("创建设备失败", err)
	}
	s.log.Info("agent enrolled",
		zap.Uint64("deviceId", d.ID), zap.String("instanceId", h.InstanceID))
	return d.ID, token, nil
}

// Authenticate 处理带 agent_token 的鉴权。
//
// 错误判别必须分辨未命中与故障（S5）：
//   - `repository.ErrNotFound`（查无此 token_hash）→ 400「agent token 无效」；
//   - 其它任何错误（DB 故障）→ 500 Internal 且带 cause。
//
// 曾把任何错误都当成「token 无效」：一次数据库抖动会让**所有** agent 同时
// 收到「token 失效」，运维会去逐个排查 agent 凭据，而真正的问题在数据库。
func (s *AgentIngestService) Authenticate(ctx context.Context, h *agentproto.Hello) (uint64, error) {
	if h == nil || h.AgentToken == "" {
		return 0, apperror.BadRequest("缺少 agent token")
	}
	d, err := s.repo.FindByTokenHash(ctx, hashToken(h.AgentToken))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return 0, apperror.BadRequest("agent token 无效")
		}
		return 0, apperror.Internal("内部错误", err)
	}
	if d == nil {
		return 0, apperror.BadRequest("agent token 无效")
	}
	// 用本次 hello 刷新设备自述的静态信息（版本 / 平台 / 主机名 / 内存…）。
	//
	// 此前**只有 enroll 会写这些列**，于是「agent 升级完重连」在库里完全看不见：
	// agent_version 还是旧值，控制台据此显示的版本、以及升级成功的判定全部失真。
	//
	// 刷新失败**绝不影响鉴权结果**：设备能连上并上报是第一位的，
	// 静态信息下一轮握手还有机会补上（它是可自愈的推断量，不是凭据）。
	if err := s.refreshStatic(ctx, d, h); err != nil {
		s.log.Warn("agent static info refresh failed",
			zap.Uint64("deviceId", d.ID), zap.Error(err))
	}
	return d.ID, nil
}

// refreshStatic 把 hello 里的静态字段写到设备行。
//
// 只碰「设备自述」这一类列：不动 status（管理意图）、不动 target_agent_version
// （运维意图 —— 一次重连就把升级目标清掉会是最难查的 bug 之一）。
func (s *AgentIngestService) refreshStatic(ctx context.Context, d *entity.Device, h *agentproto.Hello) error {
	d.Hostname = h.Hostname
	d.OS = h.OS
	d.Arch = h.Arch
	d.Kernel = h.Kernel
	d.AgentVersion = h.AgentVersion
	d.Platform = h.Platform
	d.PlatformVer = h.PlatformVer
	d.CPUModel = h.CPUModel
	d.CPUCores = h.CPUCores
	d.MemTotalMB = h.MemTotalMB
	d.BootTime = h.BootTime
	d.AgentUpgradeSupported = boolToInt8(h.UpgradeSupported)
	return s.repo.RefreshStaticFromHello(ctx, d)
}

// AuthenticateDownload 用 agent token 鉴权一次**下载请求**（与 WS 首帧同一套凭据）。
//
// 与 WS 侧同一条纪律：三种失败（无此 token / 设备已删 / 设备停用）返回**同一个**
// 结论式错误，调用方映射成同一个状态码 —— 否则状态码会泄露「该设备是否存在」，
// 而设备存在与否本身就是攻击者想要的信息。
//
// token 本身绝不进日志（只记 device_id 与「鉴权失败」）。
func (s *AgentIngestService) AuthenticateDownload(ctx context.Context, token string) (*entity.Device, error) {
	if token == "" {
		return nil, apperror.BadRequest("凭据无效")
	}
	d, err := s.repo.FindByTokenHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.BadRequest("凭据无效")
		}
		return nil, apperror.Internal("内部错误", err)
	}
	if d == nil || d.Status != entity.DeviceStatusEnabled {
		return nil, apperror.BadRequest("凭据无效")
	}
	return d, nil
}

// IsAccepting 报告设备是否处于可接受上报的状态（启用态）。
//
// 错误必须**上抛**（S5）：原先 `if err != nil || d == nil { return false, nil }`
// 把仓储的**任何**错误吞成「不接受上报」——DB 故障时调用方（2C 的 AgentHub）
// 只看到「设备都被停用了」，无从知道数据库已经不可用，故障被伪装成一个业务结论。
//
// 未命中是**合法答案**而不是错误：设备不存在/已删除 → 就是不可接受上报
// （返回 false, nil），调用方据此拒绝该连接；只有真故障才上抛 500。
func (s *AgentIngestService) IsAccepting(ctx context.Context, deviceID uint64) (bool, error) {
	d, err := s.repo.FindByID(ctx, deviceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, apperror.Internal("内部错误", err)
	}
	if d == nil {
		return false, nil
	}
	return d.Status == entity.DeviceStatusEnabled, nil
}

// Touch 刷新 last_seen_at（心跳与任一消息到达时调用）。
func (s *AgentIngestService) Touch(ctx context.Context, deviceID uint64) error {
	return s.repo.Touch(ctx, deviceID, time.Now())
}

// Ingest 把一条已校验的样本写入 Redis 热层（原始窗 + 水位）。
// 写失败只记日志、返回错误由调用方决定是否计数，**不阻断上报通道**。
func (s *AgentIngestService) Ingest(ctx context.Context, deviceID uint64, sample *agentproto.MetricsSample) error {
	if sample == nil {
		return nil
	}
	if err := s.raw.Append(ctx, deviceID, sample); err != nil {
		s.log.Warn("agent raw append failed",
			zap.Uint64("deviceId", deviceID), zap.Int64("sampleTs", sample.T), zap.Error(err))
		return err
	}
	if err := s.latest.Set(ctx, deviceID, sample); err != nil {
		s.log.Warn("agent latest set failed",
			zap.Uint64("deviceId", deviceID), zap.Int64("sampleTs", sample.T), zap.Error(err))
		return err
	}
	return nil
}

// SaveDockerState 落一帧 docker 快照（agent 通道的 docker 面）。
//
// 不碰 DB：快照只存 Redis（spec §2 存储决策）。
func (s *AgentIngestService) SaveDockerState(ctx context.Context, deviceID uint64, st *agentproto.DockerState) error {
	if st == nil {
		return nil
	}
	now := time.Now()
	if err := s.docker.Save(ctx, deviceID, st, now); err != nil {
		return err
	}
	// stats 留存挂钩（P2，观察者不是参与者，与 6c 审计挂钩同款纪律）：
	//   - **失败只 warn、不改返回值** —— 主链的答案永远是「快照落库是否成功」，
	//     留存丢一帧只是抽屉历史少一个点（下帧 30s 后就补上）；
	//   - 与审计挂钩不同，这里**同步**调用而不是 goroutine：留存是时间序列，
	//     异步会让相邻两帧的样本赛跑（环形里出现乱序的时间轴）；而它的代价
	//     只是同一量级的一次 Redis pipeline（与上面的 Save 同价），不值得为
	//     它引入乱序风险。ctx 也因此直接复用帧处理的 ctx（同步返回前有效，
	//     不存在审计那边「goroutine 逃出帧生命周期」的问题）。
	//   - 收帧时刻与 Envelope.ReceivedAt 用**同一个** now：历史样本的 t 与页面的
	//     lastSync 逐字对齐，两条叙事不出现一帧之差。
	if s.statsHistory != nil {
		if err := s.statsHistory.Record(ctx, deviceID, st, now); err != nil {
			s.log.Warn("docker stats history 留存失败（快照主链不受影响）",
				zap.Uint64("deviceId", deviceID), zap.Error(err))
		}
	}
	return nil
}

// CompleteDockerCmd 回写一条指令结果。
//
// 归属校验（设备的 ref 只由该设备回）在这一层做不了 —— agent 通道只知道
// 「这一帧来自哪台设备」，而记录里也存了 device_id，两者不一致时**丢弃并告警**：
// 那是一台设备在回别人的 ref（伪造或串号）。
func (s *AgentIngestService) CompleteDockerCmd(ctx context.Context, deviceID uint64, res *agentproto.DockerCmdResult) error {
	if res == nil {
		return nil
	}
	rec, err := s.cmd.Get(ctx, res.Ref)
	if err != nil {
		return err
	}
	if rec == nil {
		s.log.Warn("docker result 命中不到指令（已过期或伪造）",
			zap.Uint64("deviceId", deviceID), zap.String("ref", res.Ref))
		return nil
	}
	if rec.DeviceID != deviceID {
		s.log.Warn("docker result 归属不符，丢弃",
			zap.Uint64("fromDevice", deviceID), zap.Uint64("expectDevice", rec.DeviceID), zap.String("ref", res.Ref))
		return nil
	}
	// 终态转换的唯一性保证（审计的幂等键）：一条 ref 只有一次「非终态 → 终态」的
	// 真实转换。回写**前**把旧状态摘下 —— 重复 result（agent 重发/网络重放）到达时
	// 记录已是 succeeded/failed，本次回写不再是转换，审计不会入第二条。
	// sweep 的 timeout 是服务端推断、不经过本挂钩（不入审计 —— 审计记录的是
	// 被执行的事实，不是推断）；迟到的 result 覆盖 timeout（事实优先于推断）时，
	// 入账的正是**事实**那条。
	prev := rec.Status
	if err := s.cmd.Complete(ctx, rec, res); err != nil {
		return err
	}
	if s.audit != nil && prev != dockerstate.StatusSucceeded && prev != dockerstate.StatusFailed {
		// 审计挂钩（观察者不是参与者）：只记账、绝不改判 —— 异步与失败豁免都在
		// DockerCmdAuditor 内部（warn 不阻塞），指令的轮询/流路径零影响。
		// docker:events 常驻订阅（UserID=0）由挂钩自行豁免。
		s.audit.RecordTerminal(rec)
	}
	// 扫描缓存挂钩（P3·安全面）：成功的 image:scan 把报告按镜像内容键写进 24h
	// 缓存。同步调用（与 statsHistory 的留存同理 —— 但理由不同：这里是「结果
	// 刚落地、下一个同类请求可能立刻到」的收尾动作，异步窗口里到达的请求会
	// 白白等一次分钟级扫描），失败只 warn —— 缓存是加速器，写不进去的最坏
	// 结果是下一扫不秒回，绝不能反向影响已经成功的结果回写。
	if s.scanCache != nil && rec.Action == agentproto.DockerActionImageScan &&
		res.OK && len(res.Payload) > 0 {
		var report agentproto.DockerScanReport
		if err := json.Unmarshal(res.Payload, &report); err != nil {
			s.log.Warn("docker scan 报告解码失败（缓存未写入，指令主链不受影响）",
				zap.String("ref", rec.Ref), zap.Error(err))
		} else if !dockerstate.IsDockerScanImageID(report.ImageID) {
			// agent 填的 image_id 形态不合法（agent 缺陷/被伪造）：不写缓存也不猜键 ——
			// 键空间只有「sha256:hex64」一种形状（scan_cache 的形态闸）。
			s.log.Warn("docker scan 报告的 image_id 形态不合法（缓存未写入）",
				zap.String("ref", rec.Ref), zap.String("imageId", report.ImageID))
		} else if err := s.scanCache.Save(ctx, report.ImageID, &report, s.nowValue()); err != nil {
			s.log.Warn("docker scan cache 写入失败（下一扫不秒回，指令主链不受影响）",
				zap.String("ref", rec.Ref), zap.String("imageId", report.ImageID), zap.Error(err))
		}
	}
	if rec.Action == agentproto.DockerActionEvents {
		// docker:events 是 core 的**常驻订阅**：不进用户流注册表（无发起人、不占用户
		// 槽位、不参与用户 sweep），result 原样交还管理器认领。带内调用（本帧处理
		// 返回之前）保证认领先于该连接后续的帧 —— 与 registerStreamSession 的
		// 「帧一定晚于 result」是同一时序纪律。
		if s.events != nil {
			s.events.OnEventsResult(deviceID, res)
		}
		return nil
	}
	s.registerStreamSession(rec, res)
	return nil
}

// registerStreamSession 把「建立了会话的结果」登记进流注册表（三期）。
//
// 必须发生在**结果回写成功之后**（记录里要先有 session_id，轮询才能据此签 ticket）；
// 也必须在本帧处理返回之前 —— 同一连接的读循环按序处理消息，帧一定晚于 result，
// 登记晚于帧会让会话前几帧（PTY 的首屏输出）永远找不到主人。
//
// 登记失败只记日志（不返回错误）：结果已经落库、轮询路径完好，用户最坏看到
// 「会话不存在或已结束」，而不是「指令失败」。
func (s *AgentIngestService) registerStreamSession(rec *dockerstate.CmdRecord, res *agentproto.DockerCmdResult) {
	if s.streams == nil || rec == nil || res == nil || !res.OK || res.SessionID == "" {
		return
	}
	meta := dockerstream.Meta{
		SessionID: res.SessionID,
		DeviceID:  rec.DeviceID,
		UserID:    rec.UserID,
		Action:    rec.Action,
		Ref:       rec.Ref,
		Kind:      dockerstream.KindForAction(rec.Action),
		CreatedAt: time.Now(),
	}
	if err := s.streams.Register(meta); err != nil {
		s.log.Warn("docker stream session 登记失败（流端点将找不到会话）",
			zap.String("ref", rec.Ref), zap.String("session", res.SessionID), zap.Error(err))
	}
}

// ── 内部工具 ───────────────────────────────────────────

// newAgentToken 生成 32 字节随机 token 与它的 sha256 hex。
func newAgentToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
