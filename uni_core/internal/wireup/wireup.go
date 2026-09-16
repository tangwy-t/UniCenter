// Package wireup 集中管理依赖注入（DI）组装，将 main() 中的 DI 逻辑外移。
// 不引入外部 DI 框架，纯手写方式保持零新增依赖。
package wireup

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/handler"
	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/captcha"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/config"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/datascope"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/lifecycle"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/limiter"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/redis/cache"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/redis/lock"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/redis/pubsub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/serverstats"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/session"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/sqlhistory"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/storage"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/tracing"
	wsPkg "github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	"github.com/tangwy-t/UniCenter/uni_core/internal/router"
	"github.com/tangwy-t/UniCenter/uni_core/internal/scheduler"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	// configAgentReportInterval / configAgentHeartbeatInterval 是 agent 两侧节奏的
	// 配置键（v008 种子：10s / 30s）。键名在此逐字写出而不复用 service 内部的
	// 未导出常量：wireup 是装配点，键名与种子的对应关系应当一眼可见。
	configAgentReportInterval    = "sys.agent.reportInterval"
	configAgentHeartbeatInterval = "sys.agent.heartbeatInterval"
	// defaultAgentReportIntervalSec 与 v008 种子同值。缺配置时**不得**退化成
	// agent 侧的 2s 下限（那会让 agent 以 5 倍频率上报，把热层与 DB 一起压上去）。
	defaultAgentReportIntervalSec = 10
	// defaultAgentHeartbeatIntervalSec 与 v008 种子同值。
	defaultAgentHeartbeatIntervalSec = 30

	// configAgentMaxFramesPerMin / defaultAgentMaxFramesPerMin 是 agent **入站帧级**
	// 限流的阈值（种子键由 v011 种下；缺省 900 在种子缺失时兜底）。口径：
	// 10s 上报 = 6 帧/分 + 心跳 2 帧/分 → 正常设备约 8 帧/分，900 留 ~100× 余量 ——
	// 这条限流要拦的是「失控刷帧」（bug 或被攻陷的 agent），不是上报节奏的正常抖动；
	// 阈值贴住正常值会让一次重连补齐（agent 本地待发队列一次性排空）就被误杀。
	configAgentMaxFramesPerMin  = "sys.agent.maxFramesPerMin"
	defaultAgentMaxFramesPerMin = 900
	// agentFrameWindowSecs 是帧预算的窗口长度：配置键名即 maxFramesPer**Min**，
	// 窗口必须与它同源（改窗口而不改键名会让「每分钟 900 帧」变成别的东西）。
	agentFrameWindowSecs = 60

	// agentDrainPollInterval 是 drain 相位里「等连接收尾」的轮询间隔。
	// 连接收尾的正常耗时是「读循环从 SetReadDeadline(now) 返回 + handler 的
	// defer Unregister 执行完」，量级是毫秒；20ms 让停机几乎立刻完成，
	// 又不会变成忙等（drain 期的 CPU 不该被这里吃掉）。
	agentDrainPollInterval = 20 * time.Millisecond

	// rawWindowShortfallMarker 是「热层窗口跨度短于回填假设」那条启动期 Warn 的
	// **稳定前缀**：同包测试按它筛日志（文案可以再改，前缀是契约），
	// 运维也按它 grep/告警。理由见 Init 里那处启动期校验。
	rawWindowShortfallMarker = "wireup: 热层窗口实际只覆盖"
)

// Init 完成所有 repo/service/handler/scheduler 的构造和组装。
// 接受基础设施依赖，返回装配完成的 router.Dependencies。
// 直接返回 router 的结构体:此前 wireup.Deps 与 router.Dependencies
// 逐字段镜像,main 里还要手工抄送 30 个字段 —— 新增 handler 需要改三处,
// 漏一处即静默空指针。初始化失败(如 tracer/scheduler 启动失败)返回
// error 由 main 统一退出,避免带病启动后静默失效。
//
// 生产路径恒为 initHooks{}（两个接缝都不覆盖）。
func Init(db *gorm.DB, sqlStats *database.SQLStats, redis goredis.UniversalClient, log *logger.Logger, lc *lifecycle.Manager, cfg *config.Config) (*router.Dependencies, error) {
	return initWith(db, sqlStats, redis, log, lc, cfg, initHooks{})
}

// initHooks 是 Init 装配过程中的三个**可观测接缝**，零值即生产行为。
//
// 为什么需要接缝（而不是直接调 partitionSvc.Reconcile / scheduler.NewScheduler）：
// Plan 2C 有两条从外部观测不到的装配契约，只有在这两个边界上才能钉住——
//  1. 「启动期 reconcile 先于 scheduler 构造」：两者的先后顺序没有可断言的副作用，
//     除非在构造点上看一眼「此刻 6 张指标表是否已存在」；
//  2. 「reconcile 失败不阻断启动」：需要一个可控的失败注入点，否则只能靠真去把
//     DDL 弄坏（连带把后面所有装配一起弄坏），那样的测试什么也证明不了。
//
// Plan 2G 又加了第三条：热层窗口容量的 (Step, MaxPoints) 决策本身不可观测
// （RawStore 不导出 MaxPoints、也不落读数），而「冻结的容量 × 当轮的节奏」这个跨度
// 正是 Task 1 要暴露的东西（见 rawWindow 字段）。
//
// 它们不是「可配置行为」——没有任何配置项能改到它们，只在同包测试里被替换。
type initHooks struct {
	// reconcile 覆盖启动期分区对账入口；nil → 真实 partitionSvc.Reconcile。
	// 签名与 (*service.AgentMetricsPartitionService).Reconcile 一致（方法值可直接赋值）。
	reconcile func(ctx context.Context) (service.PartitionStats, error)
	// aroundScheduler **包裹**（而非替换）调度器构造：它拿到真实构造闭包，可以在
	// 调用前后观测，并照常返回真实调度器。nil → 直接调真实构造。
	aroundScheduler func(build func() (*scheduler.Scheduler, error)) (*scheduler.Scheduler, error)
	// rawWindow 覆盖热层窗口容量的 **(Step, MaxPoints) 决策**：它拿到真实推导出的两个值
	// （当轮的 sys.agent.reportInterval 与 agentmetrics.RawMaxPoints(Step)），返回**实际**
	// 用于构造 RawStore 的两个值。nil → 原样使用（生产路径）。
	//
	// 为什么非要这个接缝：MaxPoints 是「启动时按当轮 reportInterval 冻结」的条数上限，
	// 且 **RawStore 不导出它**、也不落任何读数 —— 「冻结的容量 × 当轮的节奏」这个跨度
	// 从外部无法观测，而本任务要钉住的恰恰是它。默认路径的推导又**恒自洽**
	// （容量自带 1.2 倍余量 → 跨度恒 28.8h ≥ 24h），所以「窗口不足时那条 Warn 真的会响」
	// 只能用「容量按旧间隔冻结、间隔已热更」这一状态复现（测试里返回旧容量 + 新节奏）。
	rawWindow func(step time.Duration, maxPoints int64) (time.Duration, int64)
}

func initWith(db *gorm.DB, sqlStats *database.SQLStats, redis goredis.UniversalClient, log *logger.Logger, lc *lifecycle.Manager, cfg *config.Config, hooks initHooks) (*router.Dependencies, error) {
	// ── Focused Stores & Broker ────────────────────────────────────────
	cacheStore := cache.NewStore(redis)
	sessionStore := session.NewSession(cacheStore)
	locker := lock.NewLocker(redis)
	broker := pubsub.NewBroker(redis, log, lc)

	// ── OpenTelemetry TracerProvider ─────────────────────────────────
	tp, err := tracing.NewTracer(&cfg.Observability.Tracing, lc, log)
	if err != nil {
		return nil, fmt.Errorf("wireup: init tracer: %w", err)
	}
	_ = tp // TracerProvider 已全局注册到 otel，此处仅持有引用便于未来扩展

	// ── Repositories ───────────────────────────────────────────────────
	userRepo := repository.NewUserRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	menuRepo := repository.NewMenuRepository(db)
	deptRepo := repository.NewDeptRepository(db)
	authRepo := repository.NewAuthRepository(db)
	opLogRepo := repository.NewOperationLogRepository(db)
	loginLogRepo := repository.NewLoginLogRepository(db)
	dictTypeRepo := repository.NewDictTypeRepository(db)
	dictDataRepo := repository.NewDictDataRepository(db)
	noticeRepo := repository.NewNoticeRepository(db)
	configRepo := repository.NewConfigRepository(db)
	jobRepo := repository.NewJobRepository(db)
	jobLogRepo := repository.NewJobLogRepository(db)
	fileRepo := repository.NewFileRepository(db)
	deviceRepo := repository.NewDeviceRepository(db)
	deviceResourceRepo := repository.NewDeviceResourceRepository(db)
	deviceMetricRepo := repository.NewDeviceMetricRepository(db)

	// ── DictService ────────────────────────────────────────────────────
	dictSvc := service.NewDictService(dictTypeRepo, dictDataRepo, cacheStore, log)

	// ── ConfigService (early: all downstream services inject ConfigGetterInterface) ──
	configSvc := service.NewConfigService(configRepo, cacheStore, broker, log)

	// Security: 检出已知的默认 JWT 密钥（v006 种子值）时立即轮换为随机值。
	// 必须先于任何读取/快照该密钥的组件（hub、登录）执行。
	configSvc.RotateDefaultJWTSecret(context.Background())

	captchaPkg := captcha.NewCaptcha(cacheStore, configSvc, log)

	// ── Rate Limiter ──────────────────────────────────────────────────
	rateLimiter := limiter.NewRateLimiter(cacheStore, configSvc, log)

	// ── WebSocket Hub ──────────────────────────────────────────────────
	// sessionStore 用于 WS 认证时校验 access token 是否仍在会话白名单，
	// 否则登出/吊销后的 token 仍可维持已建立的 WebSocket 连接。
	// configSvc 作为 SecretGetter 注入:WS 认证实时读取密钥,运行期
	// 轮换 sys.jwt.secret 后无需重启即可生效(与 HTTP 认证同源)。
	hub := wsPkg.NewHub(broker, configSvc, sessionStore, log, nil)

	// Register hub for graceful shutdown in drain phase.
	lc.RegisterTo("drain", "ws-hub", func(context.Context) error {
		hub.Stop()
		return nil
	})

	// ── Scope Resolver ──(前移到 AuthService 之前:登录/刷新时的访问解析
	// resolveUserAccess 依赖它构造 ScopeContext;依赖只需 authRepo/deptRepo/log)
	deptResolver := datascope.NewDeptDimensionResolver(authRepo, deptRepo)
	roleResolver := datascope.NewRoleDimensionResolver(authRepo)
	selfResolver := datascope.NewSelfDimensionResolver()
	scopeResolver := datascope.NewScopeResolver([]datascope.DimensionResolver{deptResolver, selfResolver, roleResolver}, log)

	// ── Services ───────────────────────────────────────────────────────
	userSvc := service.NewUserService(userRepo, log, sessionStore, configSvc)
	roleSvc := service.NewRoleService(roleRepo, log, sessionStore)
	menuSvc := service.NewMenuService(menuRepo, sessionStore, log)
	deptSvc := service.NewDeptService(deptRepo, log)
	loginLogSvc := service.NewLoginLogService(loginLogRepo, log)
	// ── Password Service ───────────────────────────────────────────────
	// 密码域(改密/锁屏校验/强度策略)独立于认证登录流程;AuthService 仅委托。
	passwordSvc := service.NewPasswordService(configSvc, authRepo, sessionStore, log)

	authSvc := service.NewAuthService(configSvc, authRepo, log, sessionStore, loginLogSvc, captchaPkg, cfg.Server.APIPrefix, scopeResolver, passwordSvc)
	noticeSvc := service.NewNoticeService(noticeRepo, hub, log)
	opLogSvc := service.NewOperationLogService(opLogRepo, userRepo, log)

	// ── File Service ────────────────────────────────────────────────────
	// sys.file.upload.* 配置经 ConfigService 读取,支持运行期热更。
	// 存储后端默认本地盘;配置 storage.backend=s3 时构造 S3 兼容后端(新上传走
	// 对象存储,历史 local 文件仍按 StorageType 路由到本地盘读取)。
	fileSvc := service.NewFileService(fileRepo, configSvc, log)
	if cfg.Storage.Backend == "s3" {
		s3Backend, err := storage.NewS3(storage.S3Options{
			Endpoint:  cfg.Storage.S3.Endpoint,
			AccessKey: cfg.Storage.S3.AccessKey,
			SecretKey: cfg.Storage.S3.SecretKey,
			Bucket:    cfg.Storage.S3.Bucket,
			Region:    cfg.Storage.S3.Region,
			UseSSL:    cfg.Storage.S3.UseSSL,
			PathStyle: cfg.Storage.S3.PathStyle,
		})
		if err != nil {
			return nil, fmt.Errorf("wireup: init storage backend: %w", err)
		}
		fileSvc = service.NewFileServiceWithRemoteS3(fileRepo, configSvc, log, s3Backend)
	}

	// ── Agent 指标热层（每设备原始滚动窗 + 水位投影）─────────────────────
	// Step 取 sys.agent.reportInterval（秒，默认 10）——它与 agent 的上报节奏同源；
	// 窗口容量按回填假设（agentmetrics.RawBootstrapWindow = 24h）再放 1.2 倍余量
	// （滚动窗只保留 24h，更长区间走 DB 冷层），推导走 agentmetrics.RawMaxPoints
	// 这个**单一公式**（过去这里手写 `math.Ceil(24*time.Hour.Seconds()/step*1.2)`，
	// 与 service 侧那份 bootstrapWindow 各写各的 24h —— 两份互不相干的「一天」）。
	//
	// **MaxPoints 在启动时按当轮的 reportInterval 冻结**：它是一份条数上限，不是时间
	// 上限，窗口实际覆盖的时间跨度是 agentmetrics.RawWindowSpan(MaxPoints, Step)。
	// reportInterval 可热更而 MaxPoints 不会跟着变 —— 改小 reportInterval 后**必须重启**
	// 才能重新推导 MaxPoints；不重启则热层只剩 MaxPoints × 新 Step 的点
	// （10s→5s 时约 14.4h < 24h），服务侧仍按 24h 回填/回退，那一段必然读不到点
	// （空桶 / HoursSkipped++，不报错也没有读数指向它）。启动期校验见下方；
	// 根治要把「按条数裁剪」改成「按时间裁剪」，是另一个量级的改动（不在本任务范围）。
	// wireup 里没有请求 ctx，用 context.Background()（与上方 RotateDefaultJWTSecret 同款）。
	agentStepSec := configSvc.GetInt(context.Background(), "sys.agent.reportInterval", 10)
	if agentStepSec <= 0 {
		agentStepSec = 10
	}
	agentStep := time.Duration(agentStepSec) * time.Second
	agentMaxPoints := agentmetrics.RawMaxPoints(agentStep)
	// 测试接缝（生产恒为 nil）：(Step, MaxPoints) 决策的观测与替换点，
	// 见 initHooks.rawWindow 的说明 —— 这两个值从外部不可观测。
	if hooks.rawWindow != nil {
		agentStep, agentMaxPoints = hooks.rawWindow(agentStep, agentMaxPoints)
	}
	// QueryTTL = 0 → 交给 metricshistory 取默认 1s（趋势图连点时的短时缓存）。
	rawStore := agentmetrics.NewRawStore(redis, agentmetrics.RawOptions{
		Step: agentStep, MaxPoints: agentMaxPoints, QueryTTL: 0,
	})
	latestStore := agentmetrics.NewLatestStore(redis)

	// ── 启动期热层窗口跨度校验（**必须**在 scheduler.NewScheduler 之前）────
	// 与既有 reconcile 同级的启动期动作：把「上一步那对 (Step, MaxPoints) 决定的**实际**
	// 时间跨度」与「服务侧回填/回退假设的窗口」对一次账。两者不一致时，多出来的那一段
	// 窗口里的原始点在热层里不存在 —— flush 只会得到空桶（HoursSkipped++），
	// 既不是错误也没有任何读数指向它（与 Plan 2F 修掉的 P1 是同一类静默坑）。
	// 失败**不阻断启动**：热层仍然可用，只是窗口短（控制面与冷层都不受影响），
	// 而且此时唯一的补救是重启（重新推导 MaxPoints），把服务拦下来只会扩大影响面。
	if span := agentmetrics.RawWindowSpan(agentMaxPoints, agentStep); span < agentmetrics.RawBootstrapWindow {
		log.Warn(fmt.Sprintf(
			"%s %s，短于服务侧回填/回退假设的 %s（Step=%s、MaxPoints=%d）：这段窗口内的原始点读不到，"+
				"表现为空桶 / HoursSkipped++；MaxPoints 在启动时按当轮的 reportInterval 冻结，"+
				"改 reportInterval 后需重启以重新推导 MaxPoints",
			rawWindowShortfallMarker, span, agentmetrics.RawBootstrapWindow, agentStep, agentMaxPoints),
			zap.String("rawWindowSpan", span.String()),
			zap.String("bootstrapWindow", agentmetrics.RawBootstrapWindow.String()),
			zap.String("step", agentStep.String()),
			zap.Int64("maxPoints", agentMaxPoints),
			zap.String("hint", "改 reportInterval 后需重启以重新推导 MaxPoints，否则这段窗口的点读不到（表现为空桶/HoursSkipped）"),
		)
	}

	// ── Agent 服务（入湖写路径 + 趋势/下钻查询 + 设备管理）──────────────
	// 同一个 rawStore 分别以写入面与读取面注入：*agentmetrics.RawStore 同时具备
	// Append（入湖）/ Query+Bucket（查询）/ Purge（删除连带清理）三面，
	// 各消费方只声明自己需要的窄接口（接口定义在消费方）。
	// configSvc 直接满足 AgentConfigGetter（GetString + GetInt）—— 无需适配器。
	//
	// AgentIngestService 是 agent 通道**唯一**的入站实现，它的
	// Enroll / Authenticate / Ingest / Touch / IsAccepting 逐字满足 agenthub 的
	// Enroller / Authenticator / Ingestor / Toucher / Decider 五个窄接口
	// （装配见下方 agentDeps，接口见 internal/pkg/agenthub/conn.go 与 types.go）。
	// 这就是 Plan 2B 留下的那条「2C 交接说明」的落点：这个服务至此有了真实
	// 消费者，不再需要「构造出来只能赋给 `_`」的将就写法。
	agentIngestSvc := service.NewAgentIngestService(deviceRepo, rawStore, latestStore, configSvc, log)
	// agentPolicy 是 sys.agent.* 节奏配置（reportInterval / heartbeatInterval）的
	// 适配器（定义见文末 agentIntervalPolicy），**一个实例喂两处**：
	//   - 查询服务：Redis 档的原生栅格 = reportInterval（上方的 rawStore Step 也取自
	//     同一个配置）—— 两处读到不同的值就是「栅格与数据错位」；
	//   - hub：ping/pong 节奏由 heartbeatInterval 推导。
	// 适配器每次调用都读配置（热更即时可见），所以这里取一次实例不会把值冻结。
	agentPolicy := newAgentIntervalPolicy(configSvc)
	agentQuerySvc := service.NewAgentMetricsQueryService(
		rawStore, deviceMetricRepo, deviceResourceRepo, agentPolicy, log)
	deviceSvc := service.NewDeviceService(deviceRepo, deviceResourceRepo, rawStore, latestStore, configSvc, log)

	// ── Agent 后台服务（5m 落库 / 1h 回滚 / 6 张表分区对账）─────────────
	// flush 与 rollup **显式**注入同一个 Redis 客户端：两者消费同一族水位
	// （cursor_5m / cursor_1h）与同一个 repair 集合，注入两个不同的 Redis 会让
	// 两个档位的水位互不相认（见各自 WithCursorStore 的说明）。
	agentFlushSvc := service.NewAgentMetricsFlushService(
		rawStore, deviceMetricRepo, deviceResourceRepo, configSvc, log).WithCursorStore(redis)
	agentRollupSvc := service.NewAgentMetricsRollupService(
		deviceMetricRepo, rawStore, configSvc, log).WithCursorStore(redis)
	// 分区协调器自带锁键、锁租约与每表超时（**不复用** SysJob 的 job 锁，见其
	// PartitionLocker 说明），锁走同一个 Redis。
	//
	// 第四个参数是**审计流水仓储**（spec §7.3 的 agent_metric_partition_log）：
	// 分区被补/被回收之后各写一行，供「图上少了一块」的缺口归因倒查。
	// 审计表是**普通表**、由 AutoMigrate 清单建（migration/autoMigrateEntities），
	// 不经过指标表那条分区建表钩子 —— 这里只注入仓储，不建表。
	// 漏注入不是无声的：协调器会把「审计仓储缺失」记成审计失败（log.Error +
	// AuditFailures + ErrPartitionPartial），见 auditFailure 的说明。
	agentPartitionSvc := service.NewAgentMetricsPartitionService(
		repository.NewDeviceMetricSchemaRepository(db), configSvc, locker,
		repository.NewAgentPartitionLogRepository(db), log)

	// ── Agent Hub（agent 侧 WS 注册表 + 单连接状态机的依赖束）───────────
	// PingInterval/PongWait 由 sys.agent.heartbeatInterval 推导：服务端 ping 必须
	// **不慢于** agent 的心跳节奏（conn.pingInterval 取两者中较小者），而「多久没
	// 收到任何消息即判死」取 3×hb —— 够一次丢包 + 一轮重传，同时保证 PingInterval
	// 明显小于 PongWait（否则连接会被自己的心跳判超时）。MaxMessageBytes / SendQueue
	// 不在此覆盖：零值由 agenthub 的 withDefaults 填成协议上限与 64。
	// agentPolicy 在「Agent 服务」段已构造（查询侧与 hub 共用同一个实例）。
	agentHeartbeat := agentPolicy.HeartbeatInterval()
	agentHub := agenthub.NewHub(agenthub.Options{
		PingInterval: agentHeartbeat,
		PongWait:     3 * agentHeartbeat,
	}, log)
	agentDeps := agenthub.Deps{
		// 五个入站能力面由**同一个** AgentIngestService 满足（方法集逐字匹配上面
		// 列出的五个窄接口，已逐个 go doc 核对）；名字写反在这里是编译错误，
		// 而不是运行期的静默串号。
		Enroller:      agentIngestSvc,
		Authenticator: agentIngestSvc,
		Ingestor:      agentIngestSvc,
		Toucher:       agentIngestSvc,
		Decider:       agentIngestSvc,
		// Policy 只负责「下发/校准上报与心跳节奏」，由两个配置键包成小适配器。
		Policy: agentPolicy,
		// Limiter 是**入站帧级**限流（协议登记的 CloseRateLimited=4006 至此有了实现）：
		// 阈值取 sys.agent.maxFramesPerMin（缺省 900），计数走与上方 rateLimiter 同一个
		// cacheStore（同一个 Redis 客户端）。未注入 = fail-open，所以这里必须接上。
		Limiter: newAgentFrameLimiter(cacheStore, configSvc),
	}

	// ── 启动期分区 reconcile（**必须**在 scheduler.NewScheduler 之前）─────
	// 顺序的理由：调度器一旦 Start 就会按 cron 触发指标任务（flush 是
	// `0 */5 * * * *`），而 flush 写的是 device_metric_5m —— 表/分区尚不存在时
	// 那些写入会直接失败。所以「确保 6 张指标表以分区形态存在」必须先于
	// 「任何可能触发指标写入的组件」，这是 Plan 2C 唯一的顺序契约。
	// 失败**不阻断启动**：一次 DDL 失败（权限抖动、主库正在切换、慢 DDL 超时）不该
	// 让整个服务起不来 —— 控制面（REST/console）仍然可用，且 partition 任务每天
	// 4 点会再对账一次。日志必须写明后果，否则「服务起来了但指标一行都没落」
	// 会变成无从归因的现象。
	reconcile := hooks.reconcile
	if reconcile == nil {
		reconcile = agentPartitionSvc.Reconcile
	}
	if _, err := reconcile(context.Background()); err != nil {
		log.Error("wireup: 启动期分区对账失败，分区未就绪，指标写入可能因缺分区失败（服务继续启动）",
			zap.Error(err))
	}

	// ── 启动期**不**显式调 flushSvc.Bootstrap（已裁决：与懒初始化等价）─────
	// 水位键的缺失由 readCursor 在一轮 flush 到达该设备时按 Bootstrap 语义补上
	// （同一个 s.bootstrapStart() = alignDown(now−24h, 300)），而 Bootstrap 只在键缺失时
	// 写（Init 是 SETNX 语义）；故启动期那句调用既不会多建出任何水位，也不会改写已有水位。
	// 等价性由 TestInit_NoStartupBootstrapAndLazyCursorMatchesBootstrap 断言钉住。

	// ── Task Registry ──────────────────────────────────────────────────
	// 任务清单由 tasks.All 维护(与任务实现同包),此处只提供依赖。
	taskRegistry := task.NewRegistry(tasks.All(tasks.Deps{
		OpLogRepo:    opLogRepo,
		LoginLogRepo: loginLogRepo,
		JobLogRepo:   jobLogRepo,
		ConfigRepo:   configRepo,
		DictTypeRepo: dictTypeRepo,
		DictDataRepo: dictDataRepo,
		ConfigSvc:    configSvc,
		CacheStore:   cacheStore,
		// 设备指标域（Plan 2C）：三个后台服务 + 结构化日志。
		// AgentFlush 同时供 flush / backfill 两个任务使用 —— 它们是同一实例的两个
		// 入口（落库 / 回退水位后落库），所以这里只填一次。
		AgentFlush:     agentFlushSvc,
		AgentRollup:    agentRollupSvc,
		AgentPartition: agentPartitionSvc,
		// Log 允许 nil（任务侧退化成 Nop），但装配点没有理由交 nil：4 个指标任务的
		// 全部可观测性就是那几行结构化读数。
		Log: log,
	})...)

	// ── Scheduler ──────────────────────────────────────────────────────
	realBuildScheduler := func() (*scheduler.Scheduler, error) {
		return scheduler.NewScheduler(taskRegistry, jobRepo, jobLogRepo, locker, broker, log, configSvc, lc)
	}
	buildScheduler := realBuildScheduler
	if hooks.aroundScheduler != nil {
		buildScheduler = func() (*scheduler.Scheduler, error) {
			return hooks.aroundScheduler(realBuildScheduler)
		}
	}
	jobScheduler, err := buildScheduler()
	if err != nil {
		return nil, fmt.Errorf("wireup: init scheduler: %w", err)
	}

	// ── Job Service ────────────────────────────────────────────────────
	jobSvc := service.NewJobService(jobRepo, jobLogRepo, jobScheduler, taskRegistry, log)

	// ── Handlers ───────────────────────────────────────────────────────
	userHdl := handler.NewUserHandler(userSvc, captchaPkg)
	roleHdl := handler.NewRoleHandler(roleSvc)
	menuHdl := handler.NewMenuHandler(menuSvc)
	deptHdl := handler.NewDeptHandler(deptSvc)
	authHdl := handler.NewAuthHandler(authSvc, captchaPkg, log)
	opLogHdl := handler.NewOperationLogHandler(opLogSvc)
	loginLogHdl := handler.NewLoginLogHandler(loginLogSvc)
	dictTypeHdl := handler.NewDictTypeHandler(dictSvc)
	dictDataHdl := handler.NewDictDataHandler(dictSvc)
	dictCodeHdl := handler.NewDictCodeHandler(dictSvc)
	noticeHdl := handler.NewNoticeHandler(noticeSvc)
	configHdl := handler.NewConfigHandler(configSvc)
	jobHdl := handler.NewJobHandler(jobSvc)
	fileHdl := handler.NewFileHandler(fileSvc)
	// 设备 handler 注入两个依赖：管理域 deviceSvc + 查询域 agentQuerySvc。
	// 两者不能混用（管理域不查指标表，查询域不查设备表），故不合并成一个大接口。
	deviceHdl := handler.NewDeviceHandler(deviceSvc, agentQuerySvc)
	serverMonitorHdl := handler.NewServerMonitorHandler(log, serverstats.NewHistoryStore(redis))

	// 服务器监控的采样协程随进程退出:drain 阶段优雅停止(幂等 Close)。
	lc.RegisterTo("drain", "server-monitor", func(context.Context) error {
		serverMonitorHdl.Close()
		return nil
	})

	// ── SQL Monitor Service & Handler ──────────────────────────────────
	// 历史走 Redis 滚动窗口(3s 采样、24h 保留);采样协程随进程退出:
	// drain 阶段优雅停止(幂等 Close)。
	sqlMonitorSvc := service.NewSQLMonitorService(sqlStats, sqlhistory.NewHistoryStore(redis), log)
	sqlMonitorSvc.Start()
	lc.RegisterTo("drain", "sql-monitor", func(context.Context) error {
		sqlMonitorSvc.Close()
		return nil
	})
	sqlMonitorHdl := handler.NewSQLMonitorHandler(sqlMonitorSvc)

	// ── Cache Admin Store & Handler ──────────────────────────────────
	cacheSvc := service.NewCacheService(cacheStore, log)
	cacheHdl := handler.NewCacheHandler(cacheSvc)

	// ── Pprof Handler ──────────────────────────────────────────────
	pprofHdl := handler.NewPprofHandler(configSvc, log)

	// ── Online User Handler ─────────────────────────────────────────
	onlineSvc := service.NewOnlineUserService(sessionStore, userRepo, configSvc, hub, log)
	onlineHdl := handler.NewOnlineHandler(onlineSvc)

	hub.SetOnUserOnline(noticeSvc.GetUnreadNotices)

	// ScopeResolver 上移到 AuthService 构造之前(登录/刷新访问解析依赖注入)。

	// ── Permission Guard ─────────────────────────────────────────────────
	permGuard := middleware.NewPermissionGuard(authSvc, sessionStore, configSvc, log)

	// ── Agent WS Handler（未鉴权入口）─────────────────────────────────────
	// hub 是上面的 agentHub（与 4 个后台任务共享同一个 rawStore 与同一族水位），
	// deps 是完整依赖束 —— 首帧 hello 的全部校验（enroll / 鉴权 / 停用拒绝）
	// 都落在它上面。入口挂在 api 组（鉴权组之外），见 router.go。
	agentWSHdl := handler.NewAgentWSHandler(agentHub, agentDeps, log)

	// ── Agent 连接排空（drain 相位）────────────────────────────────────────
	// 停机时先向注册表里每条连接下发 CloseServerShutdown(4007)——协议内登记的码，
	// agent 侧据此走「服务端维护」而不是「网络抖动」的重连退避；再**有界**等待
	// 连接收尾：连接从注册表移除发生在 handler 的 defer Unregister（Serve 返回
	// 之后），而 CloseWith 只是把读期限压到当下让读循环立刻退出，两者之间有一段
	// 窗口。有界 = 轮询 OnlineCount 直到清零或 ctx 到期（drain 相位的 ctx 由
	// lifecycle 按 Phase.Timeout 授权）；无界等待会让一次停机被一条卡死的连接拖住。
	// 超时**不返回 error**：关闭通知已经发出，为一条不领情的连接把整个停机判成失败
	// 只会掩盖真正的失败（lifecycle 会把错误 join 进 ShutdownStaged）。
	lc.RegisterTo("drain", "agenthub", func(ctx context.Context) error {
		notified := agentHub.DrainAll("服务端停机")
		if notified == 0 {
			return nil
		}
		ticker := time.NewTicker(agentDrainPollInterval)
		defer ticker.Stop()
		for agentHub.OnlineCount() > 0 {
			select {
			case <-ctx.Done():
				log.Warn("agent hub drain: 等待连接收尾超时（关闭通知已下发）",
					zap.Int("notified", notified), zap.Int("remaining", agentHub.OnlineCount()))
				return nil
			case <-ticker.C:
			}
		}
		log.Info("agent hub drained", zap.Int("notified", notified))
		return nil
	})

	return &router.Dependencies{
		Infra: router.InfraDeps{
			Hub:           hub,
			SessionStore:  sessionStore,
			ConfigProv:    configSvc,
			AuthSvc:       authSvc,
			OpLogSvc:      opLogSvc,
			ScopeResolver: scopeResolver,
			RateLimiter:   rateLimiter,
			PermGuard:     permGuard,
			Cfg:           cfg,
			Logger:        log,
		},
		Auth: router.AuthDeps{
			AuthHdl: authHdl,
		},
		System: router.SystemDeps{
			UserHdl: userHdl,
			RoleHdl: roleHdl,
			MenuHdl: menuHdl,
			DeptHdl: deptHdl,
		},
		Dict: router.DictDeps{
			DictTypeHdl: dictTypeHdl,
			DictDataHdl: dictDataHdl,
			DictCodeHdl: dictCodeHdl,
		},
		Monitor: router.MonitorDeps{
			OpLogHdl:         opLogHdl,
			LoginLogHdl:      loginLogHdl,
			NoticeHdl:        noticeHdl,
			ServerMonitorHdl: serverMonitorHdl,
			CacheHdl:         cacheHdl,
			SqlMonitorHdl:    sqlMonitorHdl,
			PprofHdl:         pprofHdl,
			OnlineHdl:        onlineHdl,
		},
		Job: router.JobDeps{
			JobHdl: jobHdl,
		},
		Config: router.ConfigDeps{
			ConfigHdl: configHdl,
		},
		File: router.FileDeps{
			FileHdl: fileHdl,
		},
		Device: router.DeviceDeps{
			DeviceHdl: deviceHdl,
		},
		// agent WS 入口：hub 与依赖束都是**完整装配**（enroll / 鉴权 / 入湖 /
		// touch / 策略六个能力面全部接上，且 hub 与 4 个后台任务共享同一批
		// 服务实例与同一族水位）。未挂载时路由整条不存在，agent 会拿到 404
		// 而不是 101 —— 所以这个字段不能是可选装饰。
		Agent: router.AgentDeps{
			AgentWSHdl: agentWSHdl,
		},
	}, nil
}

// ── agent 运行参数适配器 ────────────────────────────────────────────────

// agentIntervalPolicy 把 sys.agent.* 两个节奏配置键适配成 agenthub.Policy，
// 同时满足查询侧的 service.RedisIntervalPolicy（两个窄接口都只要事实，见下方断言）。
//
// 为什么每次调用都读配置（而不是在 Init 里取一次快照）：sys.agent.* 支持热更，
// agent 下一轮 ping 与新连接的 hello_ack 就该用新值。读一次快照会把「改配置」
// 变成「重启才生效」，而这两个值就是下发节奏本身 —— 热更失效时最难排查
// （配置显示改了、行为没变）。
type agentIntervalPolicy struct {
	cfg agentIntervalConfig
}

// 编译期断言：agentIntervalPolicy 满足查询侧的 service.RedisIntervalPolicy
// （该窄接口定义在消费方 internal/service，只要求 ReportInterval() time.Duration）。
// 名字或签名一旦漂移，这里编译失败 —— 而不是靠「构造参数恰好能塞进去」蒙对。
var _ service.RedisIntervalPolicy = agentIntervalPolicy{}

// agentIntervalConfig 是适配器需要的配置能力面（`GetInt`）；*service.ConfigService
// 直接满足（接口定义在消费方，与仓库既有约定一致）。**两个**适配器共用它：
// agentIntervalPolicy（节奏）与 agentFrameLimiter（帧限流阈值）都只读 int 配置。
type agentIntervalConfig interface {
	GetInt(ctx context.Context, key string, defaultVal int) int
}

func newAgentIntervalPolicy(cfg agentIntervalConfig) agentIntervalPolicy {
	return agentIntervalPolicy{cfg: cfg}
}

// ReportInterval 是 hello_ack 下发给 agent 的上报间隔。
//
// 非正数一律退化成默认值：0 或负数会让 agent 侧的心跳/上报退避到不确定的行为，
// 而 hello_ack 的契约层只接受「0（表示不带该字段）或 ≥2」—— 发一个负数过去
// 会被 agent 判成非法应答，连接直接不可用。
func (p agentIntervalPolicy) ReportInterval() time.Duration {
	return p.seconds(configAgentReportInterval, defaultAgentReportIntervalSec)
}

// HeartbeatInterval 是 agent 的心跳节奏。
//
// 它**不上线**（协议现状：hello_ack 只有 report_interval），用途是校准服务端 ping
// 的节奏（conn.pingInterval 取 opts.PingInterval 与它的较小值），并由 wireup
// 反过来推导 hub 的 PingInterval/PongWait。
func (p agentIntervalPolicy) HeartbeatInterval() time.Duration {
	return p.seconds(configAgentHeartbeatInterval, defaultAgentHeartbeatIntervalSec)
}

func (p agentIntervalPolicy) seconds(key string, fallback int) time.Duration {
	secs := p.cfg.GetInt(context.Background(), key, fallback)
	if secs <= 0 {
		secs = fallback
	}
	return time.Duration(secs) * time.Second
}

// ── agent 入站帧级限流适配器 ────────────────────────────────────────────

// agentFrameLimiter 用 limiter 包**既有的滑动窗口原语**实现 agenthub.RateLimiter：
// 超限的帧由连接以 CloseRateLimited(4006) 关闭（关闭与 fail-open 的取向都在连接侧，
// 见 agenthub 的 allowFrame）。
//
// 为什么直接用底层原语（CacheStoreInterface.SlidingWindowIncr）而不是
// limiter.RateLimiter 的 gin 中间件：中间件绑死在 *gin.Context 上 —— 身份从
// Authorization 头 / JWT 里取、拒绝时写 HTTP 429；而这里计数的对象是 **WS 帧**、
// 身份是 hello 之后的 deviceID 或 hello 之前的远端 IP。两者除了「都用 Redis 记一个
// 窗口内的计数」之外没有一处可复用，硬套中间件只会得到一个假的 gin.Context。
type agentFrameLimiter struct {
	store limiter.CacheStoreInterface
	cfg   agentIntervalConfig
}

// 编译期断言：适配器必须满足连接侧的窄接口 —— 名字或签名漂移时红灯落在**这里**，
// 而不是靠「构造参数恰好能塞进 Deps.Limiter」蒙对。
var _ agenthub.RateLimiter = agentFrameLimiter{}

func newAgentFrameLimiter(store limiter.CacheStoreInterface, cfg agentIntervalConfig) agentFrameLimiter {
	return agentFrameLimiter{store: store, cfg: cfg}
}

// Allow 判定这一帧是否放行；键由 agenthub.FrameLimitKey 按身份选出
// （已鉴权 → agent:device:{id}:frames；未鉴权 → agent:ws:preauth:{远端 IP}）。
//
// 原语语义（已用 miniredis 实测核对）：`SlidingWindowIncr(ctx, keys, ttl)` 返回
// `[]any` 的两个 int64 —— `{prevCount, currCount}`，其中 currCount **已包含本次调用**
// （Lua 脚本内先 `INCR` 再返回），`ttl` 单位是**秒**且只在窗口键首次自增时设置
// （固定窗口：不随每次调用续期，故持续刷帧不会把窗口越推越远）。原语自己**不做**
// 任何判定（超限时它照常返回计数），阈值比较在下面。
//
// 为什么 keys 传**同一个键两次**，而不是「上一窗 + 当前窗」两个键：这里要的是
// 帧级预算（键名 `maxFramesPerMin` 就是「每分钟」），只关心「本窗口内的第几帧」；
// 让 prev 与 curr 指向同一个键后，脚本的 `GET` 先于 `INCR` 执行，返回的 prevCount
// 即「本帧之前的计数」、currCount 即「含本帧的计数」，而且 Redis 里的键名**正好**
// 是 FrameLimitKey 给出的那个（可直接检索、TTL 即窗口长度）。
// **不能**只传一个键：Lua 脚本无条件读 KEYS[2]，少给一个键会让脚本编译失败、
// 每次调用都返回错误 —— 于是 fail-open 会把限流悄悄关成「永远放行」（正是本任务
// 要消除的形态）。这一条已用一次性探针实测（见任务回报）。
func (l agentFrameLimiter) Allow(ctx context.Context, deviceID uint64, remoteIP string) (bool, error) {
	limit := l.cfg.GetInt(ctx, configAgentMaxFramesPerMin, defaultAgentMaxFramesPerMin)
	if limit <= 0 {
		// 非正数按缺省处理，**不**解释成「不限流」：一个手滑的 0 会让防护静默失效，
		// 而防护失效之后没有任何可观测症状（帧照常被处理、日志里也不会有异常），
		// 属于最难发现的一类失效。
		limit = defaultAgentMaxFramesPerMin
	}

	key := agenthub.FrameLimitKey(deviceID, remoteIP)
	res, err := l.store.SlidingWindowIncr(ctx, []string{key, key}, agentFrameWindowSecs)
	if err != nil {
		// 原样上抛：fail-open 的取向由**消费方**（conn.allowFrame）决定，
		// 实现擅自猜一个放行/拒绝会让那条取向变成两处实现。
		return false, err
	}
	arr, ok := res.([]any)
	if !ok || len(arr) < 2 {
		// 形状不符也走「报错 → 消费方 fail-open」，绝不猜成拒绝：限流器读不懂自己的
		// 返回值是服务端故障，不该把它转成「踢掉 agent」。
		return false, fmt.Errorf("agentFrameLimiter: SlidingWindowIncr 返回了意外形状 %T", res)
	}
	if frameCountOf(arr[1]) > int64(limit) {
		return false, nil
	}
	return true, nil
}

// frameCountOf 把滑动窗口原语返回的计数元素折算成 int64。
//
// 与 limiter 包未导出的 toInt 同口径（Lua 的 INCR/tonumber 经 go-redis 解码为
// int64；字符串是防御性分支）：两处要改口径必须同改，否则同一个 Redis 计数在
// HTTP 侧与 WS 侧会算出不同的结果。
func frameCountOf(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(n, 10, 64)
		return parsed
	default:
		return 0
	}
}
