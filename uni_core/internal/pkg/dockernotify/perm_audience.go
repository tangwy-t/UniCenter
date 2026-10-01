package dockernotify

// ── 受众权限收敛（perm → 角色）──────────────────────────────────────────
//
// perm_audience 是受众权限收敛的**解析件**：docker 事件通知的受众从
// 「全员广播」收敛为「持有 docker:list 权限的角色」（策略持有者见 wireup 的
// dockerNoticeSink —— 它拿到这里的解析结果去定 TargetType/TargetIDs）。
//
// 它只做一件事：把权限码折成「当前持有它的角色集合」，外加一层短 TTL 的
// 内存缓存。为什么需要它独立成件：角色-权限是低频变更的管理数据，而告警
// 是可能高频发作的信号（flapping 容器每个节流窗口至多一条，但窗口可以
// 只有 1 分钟）—— 每条告警都打一次三表 join 的反查，等于把管理面查询
// 搬进了故障路径。

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// RolePermQuery 是「权限码 → 持有角色」的反查面（接口定义在消费方 —— 与
// ConfigGetter/NoticeSink 同一条纪律；由仓储层的 RoleRepo 直接满足，SQL
// 与匹配口径的「为什么」见 repository 侧的就地注释）。
//
// 返回约定：集合应已去重升序；**无人持有是合法结果**（空切片、非错误）——
// 调用方据此跳过通知（无人该收的通报是噪音），而不是把「没人能看 docker」
// 误当成故障。
type RolePermQuery interface {
	FindRoleIDsByPerm(ctx context.Context, perm string) ([]uint64, error)
}

// defaultPermAudienceTTL 是反查结果的内存缓存 TTL。
//
// 为什么是 5 分钟、且**不做主动失效**：
//   - 角色-菜单授权是低频管理动作（建角色、改授权），不会每分钟发生；
//     5 分钟内受众略陈旧的代价只是「新授权的人晚几分钟开始收通知」
//     （首条丢了，事件面仍可回看），换的是故障路径上零额外 SQL；
//   - 主动失效要挂进角色/菜单的写路径（跨模块耦合，多实例还得共享失效
//     信号）—— 对「晚 5 分钟生效」这个收益是负资产，短 TTL 自过期足够；
//   - 角色**成员**（用户-角色）变更不受此缓存影响：收件人是 notice 服务
//     在**发布时**才按角色解析的（角色定向的红利），加人减人下一条告警
//     即刻生效，无需任何失效动作。
const defaultPermAudienceTTL = 5 * time.Minute

// PermAudienceOptions 是解析件的可注入参数（测试要确定性时钟与可压短的
// TTL；与 Notifier.Options 同款纪律）。
type PermAudienceOptions struct {
	TTL time.Duration
	Now func() time.Time
}

func (o PermAudienceOptions) withDefaults() PermAudienceOptions {
	if o.TTL <= 0 {
		o.TTL = defaultPermAudienceTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// permAudienceEntry 是一条缓存账目（角色集合 + 过期时刻）。
type permAudienceEntry struct {
	roles    []uint64
	expireAt time.Time
}

// PermAudienceResolver 把权限码解析成持有角色集合（带 TTL 内存缓存）。
//
// 并发纪律：通知联动器是单工作协程消费它，但件本身加了锁 —— 测试与未来
// 的第二处消费不必继承「调用方恰好串行」这个隐含前提。锁只护账本，不护
// 查询 IO（缓存 miss 时的反查在锁外发生；并发 miss 会各查一次，无害 ——
// 幂等只读查询，结果谁后写都一样）。
type PermAudienceResolver struct {
	query RolePermQuery
	log   logger.LoggerInterface
	opts  PermAudienceOptions

	mu    sync.Mutex
	cache map[string]permAudienceEntry
}

// NewPermAudienceResolver 构造解析件（缺省参数见 PermAudienceOptions）。
func NewPermAudienceResolver(opts PermAudienceOptions, query RolePermQuery,
	log logger.LoggerInterface) *PermAudienceResolver {
	opts = opts.withDefaults()
	return &PermAudienceResolver{
		query: query,
		log:   log,
		opts:  opts,
		cache: map[string]permAudienceEntry{},
	}
}

// Resolve 返回持有 perm 的角色集合（升序去重，见 RolePermQuery 约定）。
//
// 口径：
//   - 命中未过期缓存 → 直接返回（返回**副本**：调用方拿到切片后怎么改都
//     不会污染缓存账目）；
//   - 查询失败 → 错误上抛且**不缓存**（宁可不收敛也不把一次 DB 抖动固化
//     成 5 分钟的「查不到」）—— 降级决策不在本件：受众策略（降级全员 or
//     跳过）的持有者是 wireup 的 sink，见其对「宁误报不漏报」的裁定注释；
//   - 成功但空集 → 正常缓存：空集同样是事实（权限没人持有），缓存它让
//     5 分钟内的后续告警零查询即走「跳过」分支。
func (r *PermAudienceResolver) Resolve(ctx context.Context, perm string) ([]uint64, error) {
	now := r.opts.Now()

	r.mu.Lock()
	if entry, ok := r.cache[perm]; ok && now.Before(entry.expireAt) {
		roles := append([]uint64(nil), entry.roles...)
		r.mu.Unlock()
		return roles, nil
	}
	r.mu.Unlock()

	roles, err := r.query.FindRoleIDsByPerm(ctx, perm)
	if err != nil {
		if r.log != nil {
			r.log.Warn("权限受众反查失败（不缓存，下一条告警即重试）",
				zap.String("perm", perm), zap.Error(err))
		}
		return nil, err
	}

	r.mu.Lock()
	r.cache[perm] = permAudienceEntry{
		roles:    append([]uint64(nil), roles...),
		expireAt: now.Add(r.opts.TTL),
	}
	r.mu.Unlock()
	return append([]uint64(nil), roles...), nil
}
