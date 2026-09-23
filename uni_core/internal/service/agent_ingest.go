package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"go.uber.org/zap"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
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
	cfg    AgentConfigGetter
	log    logger.LoggerInterface
}

func NewAgentIngestService(repo AgentDeviceRepository, raw AgentRawStore, latest AgentLatestStore,
	cfg AgentConfigGetter, log logger.LoggerInterface) *AgentIngestService {
	return &AgentIngestService{repo: repo, raw: raw, latest: latest, cfg: cfg, log: log}
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
