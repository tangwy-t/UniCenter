package service

import (
	"context"
	"encoding/json"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/redis/pubsub"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 三个配置键（与 v015 播种的键名逐字一致）。
const (
	ConfigDockerSnapshotInterval = "sys.docker.snapshotInterval"
	ConfigDockerProtected        = "sys.docker.protected"
	ConfigDockerTransferDir      = "sys.docker.transferDir"
	// dockerConfigPrefix 是「哪些配置变更要动版本号」的判据。
	dockerConfigPrefix = "sys.docker."
	// minSnapshotIntervalSec 是协议允许的最小周期（DockerConfig.Validate 同值）。
	minSnapshotIntervalSec = 10
)

// DockerConfigSubscriber 是订阅配置变更的能力面（由既有 pubsub.Broker 满足）。
//
// handler 参数用 pubsub.Handler（**具名**函数类型）而不是逐字展开的非具名函数类型：
// 与 scheduler.BrokerInterface 同一形态 —— 用非具名类型时 *pubsub.Broker 不满足本接口
// （接口实现要求方法签名逐字一致，具名与非具名函数类型并不同一）。
type DockerConfigSubscriber interface {
	Subscribe(eventType string, handler pubsub.Handler)
}

// DockerConfigProvider 生成 hello_ack 下发的 docker 配置块，并维护配置版本号。
type DockerConfigProvider struct {
	cfg AgentConfigGetter
	rdb goredis.UniversalClient
	log logger.LoggerInterface
}

// NewDockerConfigProvider 构造提供者。
func NewDockerConfigProvider(cfg AgentConfigGetter, rdb goredis.UniversalClient, log logger.LoggerInterface) *DockerConfigProvider {
	return &DockerConfigProvider{cfg: cfg, rdb: rdb, log: log}
}

// DockerConfig 读取三键并组装下发块。
//
// 每次握手直接读（不做缓存）：握手是分钟级低频事件，一次 GET/HGET 的成本远低于
// 「缓存失效导致下发旧值」的风险 —— 而旧值的后果是 agent 用着上一份保护清单。
//
// **非法值一律省略而不是下发**：一条非法的 hello_ack 会让整次握手被判载荷非法
// （agent 侧 CloseMalformedMessage），比「用内置默认值」糟得多。
func (p *DockerConfigProvider) DockerConfig(ctx context.Context) *agentproto.DockerConfig {
	out := &agentproto.DockerConfig{
		ConfigVersion: p.currentVersion(ctx),
		Protected:     p.cfg.GetString(ctx, ConfigDockerProtected, ""),
		TransferDir:   p.cfg.GetString(ctx, ConfigDockerTransferDir, ""),
	}
	if secs := p.cfg.GetInt(ctx, ConfigDockerSnapshotInterval, 0); secs >= minSnapshotIntervalSec {
		out.SnapshotInterval = secs
	} else if secs != 0 && p.log != nil {
		p.log.Warn("sys.docker.snapshotInterval 非法，本次不下发",
			zap.Int("value", secs), zap.Int("min", minSnapshotIntervalSec))
	}
	if err := out.Validate(); err != nil {
		// 兜底：任何让 Validate 不过的值都被剔掉，只保留版本号（最少信息、最大兼容）。
		if p.log != nil {
			p.log.Warn("docker 配置块未通过协议校验，降级为仅版本号", zap.Error(err))
		}
		return &agentproto.DockerConfig{ConfigVersion: out.ConfigVersion}
	}
	return out
}

// currentVersion 读配置版本号（键不存在 = 0，表示「从未改过配置」）。
func (p *DockerConfigProvider) currentVersion(ctx context.Context) uint64 {
	v, err := p.rdb.Get(ctx, dockerstate.ConfigVersionKey).Uint64()
	if err != nil {
		return 0
	}
	return v
}

// BumpConfigVersion 自增配置版本号。
//
// 用**计数器**而不是配置值的哈希：值哈希感知不到「先改 A、又改回 A」这种回摆，
// 而那种情况恰好是运维最需要看到「配置确实动过」的场景。
func (p *DockerConfigProvider) BumpConfigVersion(ctx context.Context) error {
	return p.rdb.Incr(ctx, dockerstate.ConfigVersionKey).Err()
}

// Watch 注册 config.changed 订阅：仅当变更键属于 sys.docker.* 时自增版本号。
//
// 为什么挂在既有 PubSub 而不是新造一条通道：配置变更已经有广播机制（scheduler
// 就用它做热更新），本模块只是多一个订阅者。
func (p *DockerConfigProvider) Watch(broker DockerConfigSubscriber, log logger.LoggerInterface) {
	if broker == nil {
		return
	}
	broker.Subscribe(ConfigChangedChannel, func(ctx context.Context, _ string, payload []byte) error {
		var msg ConfigChangedMsg
		if err := json.Unmarshal(payload, &msg); err != nil {
			if log != nil {
				log.Warn("docker config watch: bad message", zap.Error(err))
			}
			return nil
		}
		if !strings.HasPrefix(msg.Key, dockerConfigPrefix) {
			return nil
		}
		if err := p.BumpConfigVersion(ctx); err != nil && log != nil {
			log.Warn("docker config version bump failed", zap.String("key", msg.Key), zap.Error(err))
		}
		return nil
	})
}
