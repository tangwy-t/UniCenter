package wireup

// 本文件是 Plan 2D Task 4 的产物：把 Plan 2C 收尾时那份**一次性 E2E 探针**
// 固化成可复跑的自动化测试。
//
// 它是四份计划最终交付物（agent 上报 → Redis 热层 → 5m/1h 落库 → REST 趋势查询
// → 停机排空）**唯一**的回归保护，所以链路里**没有任何替身**：真 sqlite、真
// miniredis、真 router + 真 HTTP、真 websocket.Dialer、真 JWT 鉴权中间件、
// 真 lifecycle 停机相位。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/config"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/datascope"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/jwt"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/lifecycle"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/router"
	"github.com/tangwy-t/UniCenter/uni_core/internal/scheduler"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"
)

// ── 夹具参数（全部写成常量：断言与夹具必须看到同一组数）──────────────────

const (
	// e2eJWTSecret 是种子进 sys_config 的签名密钥（≥32 字节，且**不是**已知的
	// 默认值，否则 wireup 启动期会把它轮换成随机值，测试签的 token 立刻失效）。
	e2eJWTSecret = "e2e-test-jwt-secret-0123456789abcdef"
	// e2eUserID 是伪造的登录用户（只用于 token 主体与权限缓存键，不需要 DB 行）。
	e2eUserID = uint64(1001)

	// e2eReportIntervalSec 是 sys.agent.reportInterval 的夹具值，与 v008 种子同值。
	// Redis 档的 resolution_seconds 断言依赖它（= 间隔 × 升档倍数）。
	e2eReportIntervalSec = 10
	// e2eBucketSec / e2eSampleCount 是上报夹具的形状：3 条样本落在**同一个**
	// 5min 桶内、每 30s 一条（30s 也正好是 range=86400 那档的桶宽，故热层里
	// 每桶恰好一条样本，"首桶 cpu" 因此是单值而不是均值 —— 见下方断言）。
	e2eBucketSec   = int64(300)
	e2eSampleCount = 3
	e2eSampleGapMS = int64(30_000)
	// e2eMountpoint 是样本里唯一的挂载点明细（资源维度与 disk 子表的断言锚点）。
	e2eMountpoint = "/e2e"
	// e2eDiskUsedGB / e2eDiskTotalGB：整机 disk_used_percent 由明细 Σ 重算
	// （Σused/Σtotal×100 = 50），不是对明细 used_percent 求平均。
	e2eDiskUsedGB  = 100.0
	e2eDiskTotalGB = 200.0

	// e2eFlushJobID / e2eRollupJobID 是触发后台任务用的 SysJob.ID（只用于调度器
	// 的锁键与执行日志，不需要 sys_job 行 —— RunOnce 收的是内存里的 job）。
	e2eFlushJobID  = uint64(9001)
	e2eRollupJobID = uint64(9002)
	// e2eJobTargetFlush / e2eJobTargetRollup 必须与 tasks.All 注册的 Name() 逐字一致。
	e2eJobTargetFlush  = "agent-metrics-flush"
	e2eJobTargetRollup = "agent-metrics-rollup"
)

// ── 装配策略：为什么**不是**「另建一份 flush/rollup」─────────────────────
//
// wireup.Init 只返回 *router.Dependencies，**不导出** agentFlushSvc / agentRollupSvc
// （它们是 Init 体内的局部变量）。计划给的退路是「在测试里按 wireup 的同一组参数
// 另建一份」，本测试**没有**走那条路，理由：
//
//   - 另建一份 = 同一份参数写两处：rawStore 的 Step/MaxPoints、同一个 Redis、
//     同一族水位键（cursor_5m / cursor_1h）、同一个 configSvc……任一处漂移，
//     E2E **仍然是绿的**，但它测的已经不是生产那一份（例如 Step 只影响分桶，
//     漂移不会让任何断言变红）。而"参数一致性"没有任何机制保证，只能靠注释。
//
// 用的是更好的那条路：**initHooks.aroundScheduler** —— wireup 为"装配契约可观测"
// 留的真实接缝，它在**真实构造点**包裹真调度器并把它交给测试。调度器的 registry
// 里就是 tasks.All(tasks.Deps{AgentFlush: agentFlushSvc, AgentRollup: agentRollupSvc, ...})
// 造出来的**同一批任务实例**，任务再把调用原样转给那两个服务
// （AgentMetricsFlushTask.Execute → svc.FlushOnce，任务层没有任何业务规则）。
// 于是本 E2E 触发的**就是 wireup 装配的那一份** flush/rollup：一致性由
// "根本只有一份实例"保证，而不是由注释保证。顺带把任务层
// （scheduler → registry → task → service）也纳入覆盖 —— 那正是生产里被 cron
// 触发的路径（RunOnce 与 cron 共用同一个 Scheduler.execute）。
//
// 为什么不去改 wireup.go 加第三个接缝（例如 onBackgroundServicesBuilt）：那要动
// 生产文件，而 Task 4 的交付物只有本文件（提交也只 add 这一个文件）。已有的两个
// 接缝足够，不需要为了"更直接"而扩大改动面。

// e2eEnv 是一次 E2E 的全部句柄。
type e2eEnv struct {
	// f 复用 wireup_test.go 的装配夹具（initFixture + initWith 方法），只是
	// 数据库 DSN 与建表清单不同（见 newE2EFixture）。
	f *initFixture
	// sched 是 Init 内部构造的**真实**调度器（经 aroundScheduler 接缝取出），
	// 后台任务经它触发 —— 见文件头"装配策略"。
	sched *scheduler.Scheduler
	// srv 是真实 HTTP 服务（router.Setup + httptest）。
	srv *httptest.Server
	// deps 是 Init 返回的依赖束（HTTP 断言要用它的 SessionStore 造会话白名单）。
	deps *router.Dependencies
	// token 是带鉴权的 access token（JWT + 会话白名单都已就绪）。
	token string
}

// newE2EFixture 建一个 E2E 用的真实基础设施环境。
//
// 与 wireup_test.go 的 newInitFixture 的差别只有两处，都是 E2E 的硬要求：
//  1. DSN 用 `file:<t.Name()>-<纳秒>?mode=memory&cache=shared`。计划写的是
//     `file:<t.Name()>?mode=memory`，但**实测它不成立**：sqlite 的内存库是
//     **每连接一个**，不带 `cache=shared` 时 GORM 连接池开到第二条连接就会看到一个
//     空库，症状是同一个测试里 `no such table: device` 与"查得到行"交替出现
//     （本任务第一次运行就是这个形态：heartbeat 的 UPDATE 报 no such table，
//     而同一时刻另一条连接上的 SELECT 却有结果）。`cache=shared` 是修复，
//     也与仓库既有测试同款（internal/repository/agent_partition_log_test.go、
//     internal/service/agent_metrics_partition_test.go 都这么写）。名字里的纳秒后缀是
//     为了**每次运行都拿到全新库**：`cache=shared` 的名字在进程内共享，不带后缀时
//     `-count=2` 的第二次运行会看到上一次留下的行与配置（实测：第二次跑会读到上一次
//     热更后的 reportInterval=20，断言立刻错位）；
//  2. 多建几张本任务才会碰到的表：sys_menu（权限回源）、sys_job_log（调度器执行
//     日志）、sys_config / device / device_resource（Init 与上报链路本来就需要的）。
//     **仍然不预建 6 张指标表** —— 它们必须由启动期 reconcile 建出来（这是复用
//     既有夹具取舍的原因之一，另一条断言已由 TestInit_StartupReconcile* 覆盖）。
func newE2EFixture(t *testing.T) *e2eEnv {
	t.Helper()

	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 生产同款雪花回调：device / device_resource / sys_job_log 的 id 只有这一条来源。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)

	if err := db.AutoMigrate(
		&entity.SysConfig{}, &entity.SysMenu{},
		&entity.Device{}, &entity.DeviceResource{},
		&entity.SysJobLog{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	seedConfig(t, db, "sys.agent.enrollToken", testEnrollToken)
	seedConfig(t, db, "sys.agent.reportInterval", strconv.Itoa(e2eReportIntervalSec))
	seedConfig(t, db, "sys.agent.heartbeatInterval", "30")
	seedConfig(t, db, "sys.jwt.secret", e2eJWTSecret)

	// 权限回源（FindMenuPerms）读的是 sys_menu.perms；种一个真实的设备查询码
	// 进去，让"200 过了鉴权"这件事不依赖于 admin 通配符这一条路。
	perm := permission.PermDeviceQuery
	if err := db.Create(&entity.SysMenu{Name: "e2e 设备查询", Type: "menu", Perms: &perm}).Error; err != nil {
		t.Fatalf("seed menu: %v", err)
	}

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	log := logger.NewNop()
	lc := lifecycle.New(log)
	lc.DefinePhases(
		lifecycle.Phase{Name: "drain", Timeout: 3 * time.Second},
		lifecycle.Phase{Name: "cleanup", Timeout: 3 * time.Second},
	)

	return &e2eEnv{
		f: &initFixture{
			db:  db,
			rdb: rdb,
			lc:  lc,
			log: log,
			cfg: &config.Config{Server: config.ServerConfig{APIPrefix: "/api/v1", Mode: "release"}},
		},
	}
}

// ─ 主测试 ─────────────────────────────────────────────────────────────

// TestE2E_AgentReportToRestQuery 走完整链路并逐步断言：
//
//	真 WS：hello(enroll) → hello_ack(agent_token) → 3 条 report.metrics → 1 条 heartbeat
//	→ 真 flush（5m 落库）→ 真 rollup（1h 回滚）
//	→ 真 DB 断言 → 真 HTTP（带鉴权）：四档选档 + 配置热更栅格 + 401 负向对照
//	→ 真 drain（在线 agent 收到 4007）
func TestE2E_AgentReportToRestQuery(t *testing.T) {
	env := newE2EFixture(t)

	// 1) 真实装配：Init（内含启动期分区 reconcile，6 张指标表由此建出）+ 接缝取调度器。
	//    接缝是**包裹**而不是替换：schema 状态、装配顺序、服务实例与生产完全一致。
	deps, err := env.f.initWith(t, initHooks{
		aroundScheduler: func(build func() (*scheduler.Scheduler, error)) (*scheduler.Scheduler, error) {
			s, berr := build()
			if berr != nil {
				return nil, berr
			}
			env.sched = s
			return s, nil
		},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if env.sched == nil {
		t.Fatal("aroundScheduler 接缝没有拿到调度器 —— 后台任务无法按生产路径触发")
	}
	env.deps = deps

	// 启动期 reconcile 的端到端前置：6 张指标表确实已存在（否则后面全是"表不存在"的噪声失败）。
	if present := metricTablesPresent(t, env.f.db); len(present) != repositoryMetricTableCount {
		t.Fatalf("Init 之后指标表只有 %d 张（%v），启动期 reconcile 没有建表", len(present), present)
	}

	// 2) 真实 HTTP 服务（router.Setup 的完整中间件链：鉴权、权限码、scope、操作日志）。
	env.srv = httptest.NewServer(router.Setup(*deps))
	defer env.srv.Close()

	// 3) 真实 WebSocket：握手 enroll，拿 agent_token。
	client := dialAgentWS(t, env.srv.URL)
	deviceID, agentToken := e2eEnroll(t, client, env.f)
	t.Logf("E2E 步骤1 hello(enroll)：device_id=%d agent_token=%s…（库里只存 sha256）",
		deviceID, agentToken[:8])

	// 4) 3 条上报（同一 5min 桶，cpu 11.5 / 22.5 / 33.5）+ 1 条心跳。
	bucketTS := e2eClosedBucketTS()
	cpus := []float64{11.5, 22.5, 33.5}
	for i, cpu := range cpus {
		client.send(strconv.Itoa(2+i), agentproto.TypeAgentReportMetrics,
			e2eSample(bucketTS*1000+int64(i)*e2eSampleGapMS, cpu))
	}
	client.send("5", agentproto.TypeAgentHeartbeat, &agentproto.Heartbeat{})

	// 读循环是异步的：帧发出去 ≠ 已被处理。按**spec §7.1 的 Redis 键契约**字面量
	// 等待热层真的收到了 3 条样本（不复用实现里的未导出构造函数：那样改错了也自洽）。
	historyKey := fmt.Sprintf("agent:device:%d:history", deviceID)
	waitUntil(t, "3 条原始样本进入热层 "+historyKey, func() bool {
		n, lerr := env.f.rdb.LLen(context.Background(), historyKey).Result()
		return lerr == nil && n == e2eSampleCount
	})
	t.Logf("E2E 步骤2 上报：热层 %s 有 %d 条样本，bucket_ts=%d（cpu=%v）",
		historyKey, e2eSampleCount, bucketTS, cpus)

	// 心跳的可见副作用：device.last_seen_at 被 Touch 刷新（帧是**顺序**处理的，
	// 它被处理即证明前面 3 条上报也都已被处理完）。
	waitUntil(t, "心跳刷新 device.last_seen_at", func() bool {
		var n int64
		cerr := env.f.db.Model(&entity.Device{}).
			Where("id = ? AND last_seen_at IS NOT NULL", deviceID).Count(&n).Error
		return cerr == nil && n == 1
	})
	var dev entity.Device
	if err := env.f.db.First(&dev, deviceID).Error; err != nil {
		t.Fatalf("回查设备: %v", err)
	}
	t.Logf("E2E 步骤2 心跳：device.last_seen_at=%v（status=%d）", dev.LastSeenAt.Format(time.RFC3339), dev.Status)

	// 5) 后台服务：经真实调度器触发（等价于 cron 到点），走的就是 wireup 装配的
	//    那一份 flush / rollup —— 见文件头"装配策略"。
	runAgentJob(t, env.sched, e2eFlushJobID, e2eJobTargetFlush, "FlushOnce")
	runAgentJob(t, env.sched, e2eRollupJobID, e2eJobTargetRollup, "RollupOnce")

	// 6) DB 断言：5m 一行（均值口径）、1h 一行、资源维度与 disk 子表。
	e2eAssertMetricTables(t, env.f.db, deviceID, bucketTS)

	// 7) HTTP 断言（**带鉴权**：真 JWT + 真会话白名单 + 真权限码）。
	env.token = e2eIssueToken(t, env.deps)
	e2eAssertRestTiers(t, env, deviceID, bucketTS)

	// 负向对照：同一个 URL 不带 token → 401。它证明上面那些 200 确实**穿过**了
	// 鉴权中间件，而不是因为中间件没挂上/被短路才"顺路成功"。
	e2eAssertUnauthorizedWithoutToken(t, env.srv.URL, deviceID)

	// 8) drain：在线 agent 收到 CloseServerShutdown(4007)。
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- env.f.lc.ShutdownStaged() }()

	code, reason := readCloseCode(t, client)
	if code != agentproto.CloseServerShutdown {
		t.Fatalf("在线 agent 收到的关闭码 = %d (%s)，期望 CloseServerShutdown=%d(4007) —— "+
			"drain 钩子没有排空连接（或装配漏了注册）", code, reason, agentproto.CloseServerShutdown)
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("ShutdownStaged: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ShutdownStaged 未返回：drain 相位的等待是无界的")
	}
	t.Logf("E2E 步骤5 drain：在线 agent 收到 %d（%s），device_id=%d", code, reason, deviceID)
}

// repositoryMetricTableCount 是 6 张指标表的期望张数（写成常量而不是 import
// repository：本文件的其余部分不直接依赖仓储包，少一个 import 少一处耦合）。
const repositoryMetricTableCount = 6

// ── 步骤实现 ───────────────────────────────────────────────────────────

// e2eEnroll 完成握手并**保留** hello_ack 下发的 agent_token（明文只此一次）。
//
// 与 wireup_test.go 的 dialAndEnroll 的差别：那条测试只断言"token 非空"就丢掉了；
// 这里进一步钉住 enroll 的落库契约 —— 库里必须只有 sha256(agent_token)，
// 明文绝不落库（spec §10.2）。这个 sha256 在测试里**故意**按字面算法重写一遍
// （实现里的 hashToken 未导出）：算法漂移时这里应该响铃，而不是自动跟着漂移。
func e2eEnroll(t *testing.T, client *wsClient, f *initFixture) (uint64, string) {
	t.Helper()

	client.send("1", agentproto.TypeAgentHello, &agentproto.Hello{
		InstanceID:   testInstanceID,
		Hostname:     testHostname,
		OS:           "linux",
		Arch:         "amd64",
		AgentVersion: "0.1.0-e2e",
		EnrollToken:  testEnrollToken,
	})

	ack := &agentproto.HelloAck{}
	client.readInto(ack)
	if !ack.Accepted {
		t.Fatalf("hello 被拒：%s", ack.RejectReason)
	}
	deviceID, err := strconv.ParseUint(ack.DeviceID, 10, 64)
	if err != nil || deviceID == 0 {
		t.Fatalf("hello_ack.device_id = %q（解析失败或为 0：%v）", ack.DeviceID, err)
	}
	if ack.AgentToken == "" {
		t.Fatal("enroll 分支的 hello_ack 必须下发 agent_token（明文只此一次）")
	}
	if ack.ReportInterval != e2eReportIntervalSec {
		t.Fatalf("hello_ack.report_interval = %d，期望夹具配置的 %d —— "+
			"上报节奏下发与配置脱节（agent 会按错的节奏上报）",
			ack.ReportInterval, e2eReportIntervalSec)
	}

	var dev entity.Device
	if err := f.db.First(&dev, deviceID).Error; err != nil {
		t.Fatalf("enroll 后回查设备 %d: %v", deviceID, err)
	}
	sum := sha256.Sum256([]byte(ack.AgentToken))
	if want := hex.EncodeToString(sum[:]); dev.TokenHash != want {
		t.Fatalf("device.token_hash = %q，期望 sha256(agent_token) = %q（明文绝不落库）",
			dev.TokenHash, want)
	}
	return deviceID, ack.AgentToken
}

// e2eClosedBucketTS 返回夹具使用的 5min 桶起点：**2 小时前**对齐到 5min。
//
// 为什么必须往前挪 2h 而不是"当前桶"：
//   - flush 只落**已闭**桶（上界 = alignDown(now − CloseGrace, 300)），当前桶里的
//     样本还在陆续到达，落库会把不完整的桶写成权威真值；
//   - rollup 只回滚**已闭**小时，且 5m 行必须已经存在（它读的就是 5m 表）。
//
// 2h 同时满足：桶已闭、小时已闭、且仍在热层 24h 窗口与 5m 表 30d 保留期内。
func e2eClosedBucketTS() int64 {
	return (time.Now().Unix()/e2eBucketSec)*e2eBucketSec - 2*3600
}

// e2eSample 造一条必填字段齐全的合法样本（带一个挂载点明细）。
// 字段集合照 wireup_test.go 的 metricsSample 体例扩展。
func e2eSample(tMs int64, cpu float64) *agentproto.MetricsSample {
	return &agentproto.MetricsSample{
		T:              tMs,
		CPUUsedPercent: cpu,
		Load1:          0.5,
		Load5:          0.4,
		Load15:         0.3,
		MemUsedPercent: 32,
		MemUsedMB:      1024,
		MemAvailableMB: 2048,
		Disks: []agentproto.DiskMetric{{
			Mountpoint: e2eMountpoint,
			FSType:     "ext4",
			UsedGB:     e2eDiskUsedGB,
			TotalGB:    e2eDiskTotalGB,
		}},
		TCPTotal:       10,
		TCPEstablished: 4,
		TCPListen:      3,
		UDPTotal:       2,
		ProcCount:      120,
		UptimeSec:      3600,
	}
}

// runAgentJob 经**真实调度器**触发一次指标任务。
//
// 为什么经调度器而不是直接调任务/服务：调度器的 registry 里就是 wireup 装配的
// 那一批任务实例（见文件头"装配策略"），而 RunOnce 与 cron 触发共用同一个
// Scheduler.execute —— 任务查找、分布式锁、执行超时、执行日志都在链路上，
// 一个都不会被绕过。params 留空：flush / rollup 对非空 params 只记 Warn
// （两者都恒为全量一轮）。
func runAgentJob(t *testing.T, sched *scheduler.Scheduler, jobID uint64, target, what string) {
	t.Helper()

	assertTaskTargetRegistered(t, target)

	job := &entity.SysJob{
		BaseEntity:   entity.BaseEntity{ID: jobID},
		Name:         target,
		JobGroup:     "e2e",
		InvokeTarget: target,
	}
	if err := sched.RunOnce(context.Background(), job); err != nil {
		t.Fatalf("经调度器触发 %s（%s）失败: %v", target, what, err)
	}
}

// assertTaskTargetRegistered 用 tasks.All 的清单钉住目标名。
//
// 为什么值得单独一条：RunOnce 找不到目标时返回 "task not found: xxx"，那是可读的
// 失败，但读者会先怀疑调度器而不是"名字漂移了"。这里提前给出原因，也把
// "E2E 触发的是注册表里的真实任务"这条耦合写成断言。
func assertTaskTargetRegistered(t *testing.T, target string) {
	t.Helper()
	for _, task := range tasks.All(tasks.Deps{}) {
		if task.Name() == target {
			return
		}
	}
	t.Fatalf("tasks.All 里没有名为 %q 的任务 —— 目标名漂移（E2E 触发的不是真实任务）", target)
}

// waitUntil 有界等待条件成立（异步写侧的就绪信号）。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待「%s」超时（5s）—— 上报链路的写侧没有把数据送进热层/DB", what)
}

// e2eAssertMetricTables 断言落库结果。
func e2eAssertMetricTables(t *testing.T, db *gorm.DB, deviceID uint64, bucketTS int64) {
	t.Helper()

	// ─ 5m：一行、均值口径、samples = 样本数 ──
	wide5m := e2eOneWide(t, db, entity.TableNameMetric5m, deviceID)
	if wide5m.BucketTS != bucketTS {
		t.Fatalf("device_metric_5m.bucket_ts = %d，期望 %d", wide5m.BucketTS, bucketTS)
	}
	if wide5m.CPUUsedPercent == nil {
		t.Fatal("device_metric_5m.cpu_used_percent 为 NULL（均值列被写成空）")
	}
	// 均值口径：mean(11.5, 22.5, 33.5) = 22.5。LAST 会是 33.5、首值会是 11.5，
	// 三个值互不相同 —— 所以这条断言能区分"均值"与任何其它口径。
	if got := *wide5m.CPUUsedPercent; !e2eAlmostEqual(got, 22.5) {
		t.Fatalf("device_metric_5m.cpu_used_percent = %v，期望 22.5（桶内均值；"+
			"LAST=33.5 / 首值=11.5 都会在此暴露）", got)
	}
	if wide5m.Samples != e2eSampleCount {
		t.Fatalf("device_metric_5m.samples = %d，期望 %d", wide5m.Samples, e2eSampleCount)
	}
	// 整机合计由明细 Σ 重算：Σused/Σtotal×100 = 50。
	if wide5m.DiskUsedPercent == nil || !e2eAlmostEqual(*wide5m.DiskUsedPercent, 50) {
		t.Fatalf("device_metric_5m.disk_used_percent = %v，期望 50（由明细 Σ 重算，不是对明细比值求平均）",
			wide5m.DiskUsedPercent)
	}
	t.Logf("E2E 步骤3 DB：device_metric_5m 1 行 → bucket_ts=%d cpu=%v samples=%d disk_used_percent=%v",
		wide5m.BucketTS, *wide5m.CPUUsedPercent, wide5m.Samples, *wide5m.DiskUsedPercent)

	// 1h 由 5m 加权回滚而来：只有一行 5m，故均值与 samples 都与它一致。
	wide1h := e2eOneWide(t, db, entity.TableNameMetric1h, deviceID)
	wantHour := bucketTS - bucketTS%3600
	if wide1h.BucketTS != wantHour {
		t.Fatalf("device_metric_1h.bucket_ts = %d，期望小时起点 %d", wide1h.BucketTS, wantHour)
	}
	if wide1h.CPUUsedPercent == nil || !e2eAlmostEqual(*wide1h.CPUUsedPercent, 22.5) {
		t.Fatalf("device_metric_1h.cpu_used_percent = %v，期望 22.5（按 samples 加权的回滚值）",
			wide1h.CPUUsedPercent)
	}
	if wide1h.Samples != e2eSampleCount {
		t.Fatalf("device_metric_1h.samples = %d，期望 %d（1h 的 samples 是 5m 的 Σ）",
			wide1h.Samples, e2eSampleCount)
	}
	t.Logf("E2E 步骤3 DB：device_metric_1h 1 行 → bucket_ts=%d cpu=%v samples=%d",
		wide1h.BucketTS, *wide1h.CPUUsedPercent, wide1h.Samples)

	// ── 资源维度：name → id 的幂等解析（flush 的第一步）──
	var resources []entity.DeviceResource
	if err := db.Where("device_id = ?", deviceID).Find(&resources).Error; err != nil {
		t.Fatalf("查 device_resource: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("device_resource 行数 = %d，期望 1（样本里只有 1 个挂载点）：%+v",
			len(resources), resources)
	}
	res := resources[0]
	if res.Kind != entity.ResourceKindDisk || res.Name != e2eMountpoint || res.FSType != "ext4" {
		t.Fatalf("device_resource = %+v，期望 kind=%s name=%s fs_type=ext4",
			res, entity.ResourceKindDisk, e2eMountpoint)
	}
	if res.ID == 0 {
		t.Fatal("device_resource.id = 0：雪花回调没注册（name→id 解析会退化成 0 号资源）")
	}

	// ── disk 子表：明细行按 resource_id 落库 ──
	var disks []entity.DeviceMetricDisk
	if err := db.Table(entity.TableNameMetricDisk).
		Where("resource_id = ? AND bucket_ts = ?", res.ID, bucketTS).Find(&disks).Error; err != nil {
		t.Fatalf("查 device_metric_disk: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("device_metric_disk 行数 = %d，期望 1", len(disks))
	}
	if disks[0].UsedGB == nil || !e2eAlmostEqual(*disks[0].UsedGB, e2eDiskUsedGB) ||
		disks[0].TotalGB == nil || !e2eAlmostEqual(*disks[0].TotalGB, e2eDiskTotalGB) {
		t.Fatalf("device_metric_disk 明细 = %+v，期望 used_gb=%v total_gb=%v",
			disks[0], e2eDiskUsedGB, e2eDiskTotalGB)
	}
	t.Logf("E2E 步骤3 DB：device_resource 1 行（id=%d kind=%s name=%s）→ device_metric_disk 1 行（used_gb=%v total_gb=%v）",
		res.ID, res.Kind, res.Name, *disks[0].UsedGB, *disks[0].TotalGB)
}

// e2eOneWide 取某个宽表里该设备的**唯一**一行，并断言行数恰好为 1。
func e2eOneWide(t *testing.T, db *gorm.DB, table string, deviceID uint64) entity.DeviceMetricWide {
	t.Helper()
	var rows []entity.DeviceMetricWide
	if err := db.Table(table).Where("device_id = ?", deviceID).Find(&rows).Error; err != nil {
		t.Fatalf("查 %s: %v", table, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s 里 device_id=%d 的行数 = %d，期望 1（空桶不写行、非空桶恰好一行）",
			table, deviceID, len(rows))
	}
	return rows[0]
}

// e2eIssueToken 造一个**真的能过鉴权**的 access token：
// 真 JWT（密钥取自 sys_config 的 sys.jwt.secret）+ 真会话白名单（Redis）+ 真权限
// （role 维度 ScopeAll，与管理员登录同形；sys_menu 里还种了真实的 device:query）。
func e2eIssueToken(t *testing.T, deps *router.Dependencies) string {
	t.Helper()
	token, err := jwt.GenerateAccessToken(e2eUserID, nil,
		[]jwt.ScopeClaim{{Dimension: datascope.DimRole, Level: datascope.ScopeAll, SelfID: e2eUserID}},
		e2eJWTSecret, 7200)
	if err != nil {
		t.Fatalf("生成 access token: %v", err)
	}
	// 会话白名单：middleware.Auth 校验 access token 是否仍在白名单（登出/吊销的落点）。
	if err := deps.Infra.SessionStore.StoreAccess(context.Background(), token, e2eUserID, time.Hour); err != nil {
		t.Fatalf("把 token 写入会话白名单: %v", err)
	}
	return token
}

// e2eAssertRestTiers 断言四档查询的选档结果与首个桶。
//
// 三档的 resolution_seconds 互不相同（30 / 300 / 3600 / 900），因此"栅格写死"
// 或"选档错档"都会在这里变红。
func e2eAssertRestTiers(t *testing.T, env *e2eEnv, deviceID uint64, bucketTS int64) {
	t.Helper()

	// ─ 档 1：range=86400（24h）→ 热层 Redis ──
	// resolution_seconds = 配置间隔 × Scale = 10 × 3，其中 3 = ceil(86400 / (10×4000))
	// （响应桶数上限 4000）：超上限按**原生步长的整数倍**升档，故它既不是裸的
	// 上报间隔 10，也不是任何冷层档位（300/3600）—— 四个值互相区分。
	redisTier := e2eQueryMetrics(t, env, deviceID, "range=86400")
	if redisTier.Source != "redis" {
		t.Fatalf("range=86400 → source=%q，期望 redis（≤24h 必须走热层）", redisTier.Source)
	}
	if redisTier.ResolutionSeconds != 30 {
		t.Fatalf("range=86400 → resolution_seconds=%d，期望 30（= reportInterval 10 × Scale 3；"+
			"硬编码 10 或误用冷层 300 都会在此暴露）", redisTier.ResolutionSeconds)
	}
	if len(redisTier.Buckets) != e2eSampleCount {
		t.Fatalf("range=86400 → 桶数 = %d，期望 %d（3 条样本各占一个 30s 桶）",
			len(redisTier.Buckets), e2eSampleCount)
	}
	// 首桶：t 必须是样本所在桶的**起点**（t 定位契约），cpu 是那条样本的值。
	// 样本间隔 = 30s = 该档桶宽，所以"首桶 cpu = 11.5"是单值，不掺均值。
	first := redisTier.Buckets[0]
	if first.T != bucketTS {
		t.Fatalf("range=86400 首桶 t = %d，期望 %d（桶起点；按下标推算时间会在此暴露）",
			first.T, bucketTS)
	}
	if first.CPUUsedPercent == nil || !e2eAlmostEqual(*first.CPUUsedPercent, 11.5) {
		t.Fatalf("range=86400 首桶 cpu_used_percent = %v，期望 11.5", first.CPUUsedPercent)
	}
	t.Logf("E2E 步骤4 HTTP：range=86400 → source=%s resolution=%ds 桶数=%d 首桶{t=%d cpu=%v}",
		redisTier.Source, redisTier.ResolutionSeconds, len(redisTier.Buckets), first.T, *first.CPUUsedPercent)

	// ─ 档 2：range=604800（7d）→ 5m 表，原生 300s（不受 reportInterval 影响）──
	db5m := e2eQueryMetrics(t, env, deviceID, "range=604800")
	if db5m.Source != "db" || db5m.ResolutionSeconds != 300 {
		t.Fatalf("range=604800 → source=%q resolution=%d，期望 db/300（5min 档）",
			db5m.Source, db5m.ResolutionSeconds)
	}
	e2eAssertSingleBucket(t, db5m, "range=604800", bucketTS, 22.5)
	// 日志里不打印 samples：它不在默认投影列里，响应中的 0 是"未请求该列"而不是
	// 数据缺失 —— 打印它只会误导读者（这正是不把它写成断言的原因）。
	t.Logf("E2E 步骤4 HTTP：range=604800 → source=%s resolution=%ds 桶数=%d 首桶{t=%d cpu=%v}",
		db5m.Source, db5m.ResolutionSeconds, len(db5m.Buckets),
		db5m.Buckets[0].T, *db5m.Buckets[0].CPUUsedPercent)

	// ── 档 3：range=2592000（恰好 30d）→ **仍是** 5m 档（边界闭区间），
	//    8640 行超上限 → Scale=3 → 900s。这个 off-by-one 是实测确认过的语义：
	//    "30d 及以内"走 5m，"超过 30d"才走 1h。──
	exact30d := e2eQueryMetrics(t, env, deviceID, "range=2592000")
	if exact30d.Source != "db" || exact30d.ResolutionSeconds != 900 {
		t.Fatalf("range=2592000（恰好 30d）→ source=%q resolution=%d，期望 db/900（5m 档升 3 倍）；"+
			"若这里是 db/3600，说明选档边界被改成了开区间", exact30d.Source, exact30d.ResolutionSeconds)
	}
	e2eAssertSingleBucket(t, exact30d, "range=2592000", 0, 22.5)

	// ── 档 4：range=2592001（30d + 1s）→ 1h 表，3600s ──
	db1h := e2eQueryMetrics(t, env, deviceID, "range=2592001")
	if db1h.Source != "db" || db1h.ResolutionSeconds != 3600 {
		t.Fatalf("range=2592001 → source=%q resolution=%d，期望 db/3600（1h 档）",
			db1h.Source, db1h.ResolutionSeconds)
	}
	e2eAssertSingleBucket(t, db1h, "range=2592001", bucketTS-bucketTS%3600, 22.5)
	t.Logf("E2E 步骤4 HTTP：range=2592000 → source=%s resolution=%ds（恰好 30d 仍走 5m 档）；"+
		"range=2592001 → source=%s resolution=%ds 首桶{t=%d cpu=%v}",
		exact30d.Source, exact30d.ResolutionSeconds,
		db1h.Source, db1h.ResolutionSeconds,
		db1h.Buckets[0].T, *db1h.Buckets[0].CPUUsedPercent)

	// ─ 档 5：栅格由**配置驱动**，而不是硬编码的上报间隔 ──
	//
	// 为什么单独来一条：上面 range=86400 的期望值 30 在夹具的默认间隔 10 下与
	// "硬编码 10 × Scale 3" **恰好相等** —— 它证明不了"读了配置"。所以这里把
	// sys.agent.reportInterval 热更为 20（查询侧每次调用都读配置），断言
	// resolution_seconds 随之变成 20：硬编码 10 会留 10，两者可区分。
	//
	// 窗口取 10800（3h）而不是 3600：夹具的样本在 2h 前，1h 窗口会把它们整段排除。
	// 3h 窗口下 20s 栅格未触 4000 上限（Scale=1），且 300s 对齐的夹具桶同时也是
	// 20s 对齐的（300 = 15×20），因此首桶 t 与 cpu 仍可精确断言。
	//
	// 热更只影响查询侧的栅格：raw 热层的 Step 与两个档位的宽限是 Init 期快照，
	// 故前面已经完成的落库与对齐关系不受影响（这也是"改配置不重启"的语义边界）。
	env.f.setConfigValue(t, "sys.agent.reportInterval", "20")
	hot := e2eQueryMetrics(t, env, deviceID, "range=10800")
	if hot.Source != "redis" {
		t.Fatalf("reportInterval 热更为 20 后 range=10800 → source=%q，期望 redis", hot.Source)
	}
	if hot.ResolutionSeconds != 20 {
		t.Fatalf("reportInterval 热更为 20 后 range=10800 → resolution_seconds=%d，期望 20"+
			"（把配置读进来才会是 20；硬编码 10 会在此暴露）", hot.ResolutionSeconds)
	}
	if len(hot.Buckets) != e2eSampleCount {
		t.Fatalf("reportInterval=20 时 range=10800 → 桶数 = %d，期望 %d", len(hot.Buckets), e2eSampleCount)
	}
	if hot.Buckets[0].T != bucketTS || hot.Buckets[0].CPUUsedPercent == nil ||
		!e2eAlmostEqual(*hot.Buckets[0].CPUUsedPercent, 11.5) {
		t.Fatalf("reportInterval=20 时首桶 = {t=%d cpu=%v}，期望 {t=%d cpu=11.5}",
			hot.Buckets[0].T, hot.Buckets[0].CPUUsedPercent, bucketTS)
	}
	t.Logf("E2E 步骤4 HTTP：reportInterval 热更为 20 → range=10800 → source=%s resolution=%ds 桶数=%d 首桶{t=%d cpu=%v}",
		hot.Source, hot.ResolutionSeconds, len(hot.Buckets),
		hot.Buckets[0].T, *hot.Buckets[0].CPUUsedPercent)
}

// e2eAssertSingleBucket 断言"只有一行数据的档位"返回的唯一桶。
//
// wantT <= 0 表示不断言 t（升档归并会把 t 对齐到**升档后**的栅格上，而夹具桶
// 只是 300s 对齐，两者不一定落在同一个 900s 桶起点 —— 那是归并语义的一部分）。
func e2eAssertSingleBucket(t *testing.T, resp response.DeviceMetricsResp, what string, wantT int64, wantCPU float64) {
	t.Helper()
	if len(resp.Buckets) != 1 {
		t.Fatalf("%s → 桶数 = %d，期望 1（只有一行 5m/1h 数据）", what, len(resp.Buckets))
	}
	b := resp.Buckets[0]
	if wantT > 0 && b.T != wantT {
		t.Fatalf("%s 首桶 t = %d，期望 %d", what, b.T, wantT)
	}
	if b.CPUUsedPercent == nil || !e2eAlmostEqual(*b.CPUUsedPercent, wantCPU) {
		t.Fatalf("%s 首桶 cpu_used_percent = %v，期望 %v（冷层读的是落库后的均值）",
			what, b.CPUUsedPercent, wantCPU)
	}
}

// e2eQueryMetrics 发一次**带鉴权**的趋势查询并解出响应体。
func e2eQueryMetrics(t *testing.T, env *e2eEnv, deviceID uint64, query string) response.DeviceMetricsResp {
	t.Helper()

	url := env.srv.URL + "/api/v1/devices/" + strconv.FormatUint(deviceID, 10) + "/metrics?" + query
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求 %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer "+env.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体（%s）: %v", query, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s → HTTP %d（带鉴权却被拒：JWT/白名单/权限码哪一环没造齐？）body=%s",
			query, resp.StatusCode, body)
	}
	var env2 struct {
		Code int                        `json:"code"`
		Msg  string                     `json:"msg"`
		Data response.DeviceMetricsResp `json:"data"`
	}
	if err := json.Unmarshal(body, &env2); err != nil {
		t.Fatalf("解析响应体失败: %v（body=%s）", err, body)
	}
	if env2.Code != 0 {
		t.Fatalf("%s → 业务码 = %d（%s）", query, env2.Code, env2.Msg)
	}
	return env2.Data
}

// e2eAssertUnauthorizedWithoutToken 是**负向对照**：同一个端点、同一个设备，
// 不带头 → 401。它证明上面那些 200 是"过了鉴权中间件"而不是"中间件没挂上"。
func e2eAssertUnauthorizedWithoutToken(t *testing.T, srvURL string, deviceID uint64) {
	t.Helper()

	url := srvURL + "/api/v1/devices/" + strconv.FormatUint(deviceID, 10) + "/metrics?range=86400"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("请求 %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("不带 token 的请求 → HTTP %d，期望 401（同一端点的 200 因此才证明鉴权真的生效）body=%s",
			resp.StatusCode, body)
	}

	// 伪造签名的 token 同样必须被拒（401 而不是 500/200）：只断言"缺头"不够，
	// 因为"解析失败"和"缺头"是两条不同的分支，前者才是常态化的攻击面。
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+e2eForgedToken(t))
	forgedResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("伪造 token 请求: %v", err)
	}
	defer func() { _ = forgedResp.Body.Close() }()
	forgedBody, _ := io.ReadAll(forgedResp.Body)
	if forgedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("伪造密钥签的 token → HTTP %d，期望 401 body=%s", forgedResp.StatusCode, forgedBody)
	}
	t.Logf("E2E 步骤4 负向对照：无 token → 401；伪造密钥签的 token → 401（同一端点带真 token 为 200）")
}

// e2eForgedToken 用**错误的密钥**签一个结构合法、密钥不对的 access token。
func e2eForgedToken(t *testing.T) string {
	t.Helper()
	forged, err := jwt.GenerateAccessToken(e2eUserID, []string{"admin"},
		[]jwt.ScopeClaim{{Dimension: datascope.DimRole, Level: datascope.ScopeAll, SelfID: e2eUserID}},
		"forged-secret-that-is-at-least-32-bytes-long", 7200)
	if err != nil {
		t.Fatalf("生成伪造 token: %v", err)
	}
	return forged
}

// e2eAlmostEqual 是浮点比较：指标列经过 mean/加权/合并的多次运算，用 == 比较
// 会把"实现正确但最后一位不同"判成回归。
func e2eAlmostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
