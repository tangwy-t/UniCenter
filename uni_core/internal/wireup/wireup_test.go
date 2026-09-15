package wireup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/handler"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/config"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/lifecycle"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/limiter"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/redis/cache"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	"github.com/tangwy-t/UniCenter/uni_core/internal/router"
	"github.com/tangwy-t/UniCenter/uni_core/internal/scheduler"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// ─ 夹具 ────────────────────────────────────────────────────────────────
//
// 这几条断言全都要求真实的 wireup.Init（不是替身）：本任务的产品就是装配本身，
// 把装配换成假货等于什么都没测。所以夹具要凑齐 Init 的入口参数
// （db / sqlStats / redis / log / lc / cfg）以及它读到的两张基础表。

const (
	// testEnrollToken 是夹具写进 sys_config 的注册令牌（enroll 分支的凭据）。
	testEnrollToken = "wireup-test-enroll-token"
	// testInstanceID / testHostname 是夹具 agent 的身份。
	testInstanceID = "wireup-test-instance"
	testHostname   = "wireup-test-host"
)

// initFixture 是一次 Init 所需的全部基础设施。
type initFixture struct {
	db  *gorm.DB
	rdb goredis.UniversalClient
	lc  *lifecycle.Manager
	cfg *config.Config
	log *logger.Logger
}

// newInitFixture 建一个 sqlite + miniredis 的装配环境。
//
// 关键取舍：**不** AutoMigrate 6 张指标表。它们必须由「启动期 reconcile」建出来 ——
// 夹具提前建好会让「6 张表已存在」这条断言变成永真的空断言（那是本任务最核心的
// 一条证据，不能预先满足）。
func newInitFixture(t *testing.T) *initFixture {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 生产同款雪花回调：device 的 id **只有**这一条来源（缺了它 enroll 建出的
	// 设备 id 恒为 0，而 agenthub 明确把 0 号设备判成鉴权失败）。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)

	// 只装 Init 真正会读的基础表：sys_config（ConfigService 的真相源）与设备域。
	if err := db.AutoMigrate(&entity.SysConfig{}, &entity.Device{}, &entity.DeviceResource{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	seedConfig(t, db, "sys.agent.enrollToken", testEnrollToken)
	seedConfig(t, db, "sys.agent.reportInterval", "10")
	seedConfig(t, db, "sys.agent.heartbeatInterval", "30")

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	log := logger.NewNop()
	lc := lifecycle.New(log)
	// 相位必须先定义：RegisterTo 对未定义的相位是 panic（刻意的 fail-fast）。
	lc.DefinePhases(
		lifecycle.Phase{Name: "drain", Timeout: 3 * time.Second},
		lifecycle.Phase{Name: "cleanup", Timeout: 3 * time.Second},
	)

	return &initFixture{
		db:  db,
		rdb: rdb,
		lc:  lc,
		log: log,
		// tracing 关闭、storage 走 local、scheduler 默认关闭
		// （sys.scheduler.enabled 缺配置 → false）：夹具不引入夹具之外的真实依赖。
		cfg: &config.Config{Server: config.ServerConfig{APIPrefix: "/api/v1", Mode: "release"}},
	}
}

// initWith 用给定的接缝跑一次真实装配（零值接缝即生产路径）。
func (f *initFixture) initWith(t *testing.T, hooks initHooks) (*router.Dependencies, error) {
	t.Helper()
	sqlStats := database.NewSQLStats(64, 200*time.Millisecond)
	return initWith(f.db, sqlStats, f.rdb, f.log, f.lc, f.cfg, hooks)
}

func seedConfig(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	row := &entity.SysConfig{Name: key, ConfigKey: key, ConfigValue: value, ConfigType: "N"}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed config %s: %v", key, err)
	}
}

// metricTablesPresent 返回 6 张指标表里**已存在**的表名（sqlite 走 sqlite_master）。
//
// 用 schema 查询而不是「INSERT 一下看看」：INSERT 会因为缺表而报错，把
// 「表存在」与「能写」两件事混成一条断言。
func metricTablesPresent(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	present := make([]string, 0, 6)
	for _, table := range repository.MetricTables() {
		var name string
		err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table).
			Scan(&name).Error
		if err != nil {
			t.Fatalf("read sqlite_master for %s: %v", table, err)
		}
		if name == table {
			present = append(present, table)
		}
	}
	return present
}

// ── 断言 1：Init 成功返回且两个 handler 都装配了 ──────────────────────────

func TestInit_WiresAgentAndDeviceHandlers(t *testing.T) {
	f := newInitFixture(t)

	deps, err := f.initWith(t, initHooks{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if deps == nil {
		t.Fatal("Init 返回了 nil deps（装配点必须返回可用的依赖束）")
	}
	if deps.Agent.AgentWSHdl == nil {
		t.Fatal("Deps.Agent.AgentWSHdl == nil：agent WS 入口未装配 —— " +
			"路由整条不存在，agent 会拿到 404 而不是 101")
	}
	if deps.Device.DeviceHdl == nil {
		t.Fatal("Deps.Device.DeviceHdl == nil：设备 handler 未装配")
	}
}

// ── 断言 2：Init 返回后 6 张指标表都已存在（钩子被调过的端到端证据）────────

func TestInit_StartupReconcileCreatesAllMetricTables(t *testing.T) {
	f := newInitFixture(t)

	// 前置：夹具**故意**不建指标表（否则这条断言永真）。
	if present := metricTablesPresent(t, f.db); len(present) != 0 {
		t.Fatalf("夹具前置不成立：Init 之前已存在指标表 %v", present)
	}

	if _, err := f.initWith(t, initHooks{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	present := metricTablesPresent(t, f.db)
	if len(present) != len(repository.MetricTables()) {
		t.Fatalf("Init 之后只有 %d/%d 张指标表存在（%v）—— 启动期 reconcile 没有执行",
			len(present), len(repository.MetricTables()), present)
	}
}

// ── 断言 3：drain 钩子已注册且真的会排空在线连接 ──────────────────────────

func TestInit_DrainHookDrainsOnlineAgent(t *testing.T) {
	f := newInitFixture(t)
	deps, err := f.initWith(t, initHooks{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// 真实 HTTP + 真实 WS 客户端：hub 是 Init 内部构造的，不经过 handler 与路由
	// 就拿不到任何连接 —— 所以这条断言必须走完整链路（同时它也证明了
	// agent 入口真的挂在路由上）。
	srv := httptest.NewServer(router.Setup(*deps))
	defer srv.Close()

	client, deviceID := dialAndEnroll(t, srv.URL, f)

	// drain：lifecycle 按相位顺序跑，相位内按注册逆序（LIFO）。agenthub 在 drain
	// 相位最后注册，因此最先执行：下发 CloseServerShutdown(4007) 并等连接收尾。
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- f.lc.ShutdownStaged() }()

	code, reason := readCloseCode(t, client)
	if code != agentproto.CloseServerShutdown {
		t.Fatalf("在线 agent 收到的关闭码 = %d (%s)，期望 CloseServerShutdown=%d —— "+
			"drain 钩子没有注册，或没有排空连接", code, reason, agentproto.CloseServerShutdown)
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("ShutdownStaged: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ShutdownStaged 未返回：drain 钩子的等待是无界的")
	}
	t.Logf("drain 后在线连接收到关闭码 %d（%s），device_id=%d", code, reason, deviceID)
}

// ── 断言 4：reconcile 在 scheduler.NewScheduler 之前 ────────────────────

func TestInit_ReconcileRunsBeforeSchedulerConstruction(t *testing.T) {
	f := newInitFixture(t)

	var atConstruction []string
	hooks := initHooks{
		aroundScheduler: func(build func() (*scheduler.Scheduler, error)) (*scheduler.Scheduler, error) {
			// 观测点就是**真实构造那一行**：包裹而不是替换，所以这里看到的
			// schema 状态与 scheduler.NewScheduler 看到的完全一致。
			atConstruction = metricTablesPresent(t, f.db)
			return build()
		},
	}

	if _, err := f.initWith(t, hooks); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if len(atConstruction) != len(repository.MetricTables()) {
		t.Fatalf("scheduler 构造时只有 %d/%d 张指标表存在（%v）—— "+
			"启动期 reconcile 跑在 scheduler.NewScheduler 之后（调度器会先按 cron "+
			"触发 flush，而 flush 写的就是还不存在的 device_metric_5m）",
			len(atConstruction), len(repository.MetricTables()), atConstruction)
	}
}

// ── 断言 5：reconcile 失败不阻断启动 ────────────────────────────────────

func TestInit_ReconcileFailureDoesNotBlockStartup(t *testing.T) {
	f := newInitFixture(t)

	hooks := initHooks{
		reconcile: func(context.Context) (service.PartitionStats, error) {
			return service.PartitionStats{}, fmt.Errorf("%w: DDL 被权限拒绝",
				service.ErrPartitionPartial)
		},
	}

	deps, err := f.initWith(t, hooks)
	if err != nil {
		t.Fatalf("分区对账失败让 Init 返回了 error（%v）—— 一次 DDL 失败会让整个服务起不来，"+
			"而控制面本可继续服务", err)
	}
	if deps == nil {
		t.Fatal("分区对账失败让 Init 返回了 nil deps")
	}
	if deps.Agent.AgentWSHdl == nil || deps.Device.DeviceHdl == nil {
		t.Fatal("分区对账失败后依赖束不完整（handler 缺失）")
	}
}

// ─ 断言 6：lc == nil 也能装配（lifecycle.Manager 的方法 nil-safe）──────

func TestInit_NilLifecycleManagerIsTolerated(t *testing.T) {
	f := newInitFixture(t)

	// lc 是 *lifecycle.Manager，其方法对 nil 接收者是 no-op —— 本任务新增的
	// drain 钩子注册必须同样容忍 nil（测试与「不要生命周期管理」的调用方会传它）。
	sqlStats := database.NewSQLStats(64, 200*time.Millisecond)
	deps, err := initWith(f.db, sqlStats, f.rdb, f.log, nil, f.cfg, initHooks{})
	if err != nil {
		t.Fatalf("lc=nil 时 Init 失败：%v", err)
	}
	if deps == nil || deps.Agent.AgentWSHdl == nil {
		t.Fatal("lc=nil 时依赖束不完整")
	}
}

// ── 适配器：两个配置键 → agenthub.Policy ─────────────────────────────────

func TestAgentIntervalPolicy_DefaultsAndOverrides(t *testing.T) {
	cases := []struct {
		name     string
		reportIn string
		hbIn     string
		wantRep  time.Duration
		wantHb   time.Duration
		why      string
	}{
		{
			name: "缺配置退化到 v008 种子值", reportIn: "", hbIn: "",
			wantRep: 10 * time.Second, wantHb: 30 * time.Second,
			why: "退化成 agent 侧的 2s 下限会让上报频率变成 5 倍",
		},
		{
			name: "配置生效", reportIn: "30", hbIn: "45",
			wantRep: 30 * time.Second, wantHb: 45 * time.Second,
			why: "热更必须即时可见（每次调用都读配置）",
		},
		{
			name: "非法值退化", reportIn: "0", hbIn: "-5",
			wantRep: 10 * time.Second, wantHb: 30 * time.Second,
			why: "hello_ack 的契约层只接受 0（省略）或 ≥2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.reportIn != "" {
				values[configAgentReportInterval] = tc.reportIn
			}
			if tc.hbIn != "" {
				values[configAgentHeartbeatInterval] = tc.hbIn
			}
			policy := newAgentIntervalPolicy(fakeIntervalConfig{values: values})
			if got := policy.ReportInterval(); got != tc.wantRep {
				t.Fatalf("ReportInterval() = %v，期望 %v（%s）", got, tc.wantRep, tc.why)
			}
			if got := policy.HeartbeatInterval(); got != tc.wantHb {
				t.Fatalf("HeartbeatInterval() = %v，期望 %v（%s）", got, tc.wantHb, tc.why)
			}
		})
	}
}

// fakeIntervalConfig 是 agentIntervalConfig 的替身：只认两个节奏键。
type fakeIntervalConfig struct{ values map[string]string }

func (c fakeIntervalConfig) GetInt(_ context.Context, key string, defaultVal int) int {
	raw, ok := c.values[key]
	if !ok {
		return defaultVal
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultVal
	}
	return n
}

// ── 最小 WS 客户端（agent 侧）──────────────────────────────────────────

// wsClient 是测试用的 agent 侧客户端：只做「发一条信封消息」与「读一条」。
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

// dialAgentWS 连上 `<srvURL>/api/v1/agent/ws`。
//
// **不**带 Origin 头：agent 是非浏览器客户端，handler 与 console 共用的 ws.CheckOrigin 对
// 无 Origin 放行（带一个非本机 Origin 反而会被拒 —— 那是 CSWSH 防线）。
func dialAgentWS(t *testing.T, srvURL string) *wsClient {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/api/v1/agent/ws"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		status := "无 HTTP 响应"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dial %s: %v（%s）", wsURL, err, status)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &wsClient{t: t, conn: conn}
}

func (c *wsClient) send(id, typ string, payload any) {
	c.t.Helper()
	msg, err := agentproto.NewMessage(id, typ, payload)
	if err != nil {
		c.t.Fatalf("构造 %s: %v", typ, err)
	}
	raw, err := msg.Marshal()
	if err != nil {
		c.t.Fatalf("编码 %s: %v", typ, err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		c.t.Fatalf("发送 %s: %v", typ, err)
	}
}

// readInto 读一条 core→agent 消息并把载荷解码进 out。
func (c *wsClient) readInto(out any) *agentproto.Message {
	c.t.Helper()
	msg := c.readMessage()
	if err := msg.DecodeData(out); err != nil {
		c.t.Fatalf("解码 %s 载荷: %v", msg.Type, err)
	}
	return msg
}

func (c *wsClient) readMessage() *agentproto.Message {
	c.t.Helper()
	raw := c.readFrame()
	msg, err := agentproto.Decode(raw)
	if err != nil {
		c.t.Fatalf("解码信封（%s）: %v", raw, err)
	}
	return msg
}

func (c *wsClient) readFrame() []byte {
	c.t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		c.t.Fatalf("设置读期限: %v", err)
	}
	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("读 agent 通道: %v", err)
	}
	return raw
}

// readCloseCode 读一条消息并断言它是关闭帧。
//
// gorilla 收到关闭帧时 ReadMessage 返回 *websocket.CloseError（码在里面），
// 而不是一条普通消息 —— 这是 WebSocket 的协议行为，不是本仓库的实现细节。
func readCloseCode(t *testing.T, client *wsClient) (int, string) {
	t.Helper()
	if err := client.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("设置读期限: %v", err)
	}
	_, _, err := client.conn.ReadMessage()
	if err == nil {
		t.Fatal("期望连接被关闭，但对端没有下发关闭帧")
	}
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("期望关闭帧（*websocket.CloseError），实际错误：%v", err)
	}
	return ce.Code, ce.Text
}

// dialAndEnroll 连上 agent 入口并用 enroll token 完成握手，返回已在线连接与设备 ID。
func dialAndEnroll(t *testing.T, srvURL string, f *initFixture) (*wsClient, uint64) {
	t.Helper()
	client := dialAgentWS(t, srvURL)

	hello := &agentproto.Hello{
		InstanceID:   testInstanceID,
		Hostname:     testHostname,
		OS:           "linux",
		Arch:         "amd64",
		AgentVersion: "0.1.0-test",
		EnrollToken:  testEnrollToken,
	}
	// 信封 id 必须是**十进制串**（协议契约 isDecimalID），不是任意标识符。
	client.send("1", agentproto.TypeAgentHello, hello)

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
	if ack.ReportInterval < 2 {
		t.Fatalf("hello_ack.report_interval = %d，契约要求 ≥2", ack.ReportInterval)
	}
	// 设备真的落库了（enroll 的唯一可观测副作用，也是 flush 的资源归属前提）。
	var count int64
	if err := f.db.Model(&entity.Device{}).Where("id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("回查设备: %v", err)
	}
	if count != 1 {
		t.Fatalf("enroll 后 device 表里 id=%d 的行数 = %d，期望 1", deviceID, count)
	}
	return client, deviceID
}

// ─ 断言 4：Redis 档栅格由 sys.agent.reportInterval 驱动（装配真的接了线）────
//
// 为什么在 wireup 层还要再断一条：service 层的断言只证明「接口按 policy 工作」，
// 而本任务要修的缺陷形态恰恰是**装配没接线** —— 服务侧测试可以全绿，生产路径
// 却仍按硬编码 10s 聚合（栅格与数据错位、且不报错）。
//
// 这条断言走真实 Init + 真实 DeviceHandler：若 Init 漏传 policy（或仍用旧的四参
// 构造），`resolution_seconds` 会是 10（3600/10），与期望的 30 不符而变红。
func TestInit_QueryRedisGridFollowsConfiguredReportInterval(t *testing.T) {
	f := newInitFixture(t)
	// 夹具默认种下 10s；这里改成 30s —— 装配必须看到 30。
	f.setConfigValue(t, "sys.agent.reportInterval", "30")

	deps, err := f.initWith(t, initHooks{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if deps.Device.DeviceHdl == nil {
		t.Fatal("Deps.Device.DeviceHdl == nil：设备 handler 未装配")
	}

	// 1h 窗口：30s 上报 → 3600/30 = 120 桶，未触上限 → resolution_seconds = 30。
	// （按硬编码 10s 会是 3600/10 = 360 桶 → 10，两者不同，故能击穿「没接线」。）
	const noSuchDevice = uint64(999999)
	got := queryTrendResolution(t, deps.Device.DeviceHdl, noSuchDevice, 3600)
	if got.Source != "redis" {
		t.Fatalf("1h 窗口必须走 Redis 档, got %q", got.Source)
	}
	if got.ResolutionSeconds != 30 {
		t.Fatalf("reportInterval=30 → resolution_seconds = %d, want 30（装配未接 policy 时会是 10）",
			got.ResolutionSeconds)
	}

	// 热更：同一个 Init 装配的实例，改配置后**无需重启**就该换栅格（适配器每次调用
	// 都读配置）。这一条同时否掉了「Init 里取了值快照」的实现方式。
	f.setConfigValue(t, "sys.agent.reportInterval", "5")
	got5 := queryTrendResolution(t, deps.Device.DeviceHdl, noSuchDevice, 3600)
	if got5.ResolutionSeconds != 5 {
		t.Fatalf("配置热更为 5 后 resolution_seconds = %d, want 5（栅格必须每次读配置）",
			got5.ResolutionSeconds)
	}

	// DB 两档由表结构决定，**不受** reportInterval 影响：冷层一行就是 5min/1h 一行，
	// 把配置值渗进 DB 档会把冷层的真实栅格也报错。
	for _, c := range []struct {
		rangeSec int64
		table    string
		want     int64
	}{
		{604800, entity.TableNameMetric5m, 300}, // 7d → 5min 档
		{2592001, entity.TableNameMetric1h, 3600},
	} {
		gotDB := queryTrendResolution(t, deps.Device.DeviceHdl, noSuchDevice, c.rangeSec)
		if gotDB.Source != "db" || gotDB.ResolutionSeconds != c.want {
			t.Fatalf("range=%d（%s）→ source=%q resolution=%d, want db/%d（DB 档不受 policy 影响）",
				c.rangeSec, c.table, gotDB.Source, gotDB.ResolutionSeconds, c.want)
		}
	}
}

// setConfigValue 改一个已种下的配置值（DB 是真相源），并清掉 Redis 里的读前置缓存。
//
// 为什么必须清缓存：ConfigService.getByKey 先 HGET 再回落 DB，只改 DB 不清缓存
// 会让断言读到旧值 —— 那会让「栅格跟着配置走」这条断言在改配置后变成永真的假绿。
func (f *initFixture) setConfigValue(t *testing.T, key, value string) {
	t.Helper()
	if err := f.db.Model(&entity.SysConfig{}).Where("config_key = ?", key).
		Update("config_value", value).Error; err != nil {
		t.Fatalf("update config %s: %v", key, err)
	}
	if err := f.rdb.HDel(context.Background(), service.ConfigHashKey, key).Err(); err != nil {
		t.Fatalf("drop cached config %s: %v", key, err)
	}
}

// queryTrendResolution 直接调真实 DeviceHandler 的趋势分支并解出响应体。
//
// 为什么不经过 HTTP 路由：本断言只问「装配进去的 policy 有没有被查询服务消费」，
// 而 /devices/:id/metrics 还挂着 JWT + 权限码中间件（那是另一件事，已由既有断言
// 覆盖）。直接调 handler 走的仍是生产同一条 handler → service → rawStore 路径，
// 只有中间件被绕过。
func queryTrendResolution(t *testing.T, hdl *handler.DeviceHandler, deviceID uint64, rangeSec int64) response.DeviceMetricsResp {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/devices/"+strconv.FormatUint(deviceID, 10)+"/metrics?range="+strconv.FormatInt(rangeSec, 10), nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(deviceID, 10)}}

	hdl.Metrics(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("range=%d 趋势查询 HTTP %d: %s", rangeSec, rec.Code, rec.Body.String())
	}
	var env struct {
		Code int                        `json:"code"`
		Msg  string                     `json:"msg"`
		Data response.DeviceMetricsResp `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应体失败: %v（body=%s）", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("range=%d 趋势查询业务码 = %d（%s）", rangeSec, env.Code, env.Msg)
	}
	return env.Data
}

// ─ 入站帧级限流适配器（agentFrameLimiter）────────────────────────────────
//
// 为什么装配层也要有自己的断言：agenthub 侧的断言只证明「连接按窄接口工作」，
// 而限流是否真的生效取决于**装配进去的那个实现**——阈值有没有读配置键、键名是不是
// agenthub 定的那两个、计数有没有真落 Redis、原语会不会因为参数给错而每次报错
// （报错 = fail-open = 限流静默失效）。这些只有打到真实 Redis（miniredis 真跑 Lua）
// 才看得见。

// newFrameLimitFixture 建一个 miniredis 支撑的帧限流适配器 + 可改的配置替身。
func newFrameLimitFixture(t *testing.T, values map[string]string) (agentFrameLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return newAgentFrameLimiter(cache.NewStore(rdb), fakeIntervalConfig{values: values}), mr
}

// TestAgentFrameLimiter_EnforcesConfiguredThresholdOnPinnedKeys 钉住四件事：
// 阈值来自 sys.agent.maxFramesPerMin、键名与 agenthub 的规则逐字一致（含 TTL=窗口）、
// 已鉴权额度跟着设备走（换 IP 不重置）、未鉴权走 IP 键且与设备额度互不干扰。
func TestAgentFrameLimiter_EnforcesConfiguredThresholdOnPinnedKeys(t *testing.T) {
	ctx := context.Background()
	lim, mr := newFrameLimitFixture(t, map[string]string{configAgentMaxFramesPerMin: "3"})

	for i := 1; i <= 3; i++ {
		ok, err := lim.Allow(ctx, 1001, "192.0.2.7")
		if err != nil || !ok {
			t.Fatalf("第 %d 帧（额度 3）应放行: ok=%v err=%v", i, ok, err)
		}
	}
	if ok, err := lim.Allow(ctx, 1001, "192.0.2.7"); err != nil {
		t.Fatalf("第 4 帧: %v", err)
	} else if ok {
		t.Fatal("第 4 帧在 maxFramesPerMin=3 下仍被放行 —— 阈值没有被消费（配置键读错了？）")
	}

	// 键名与 TTL：键必须是 agenthub.FrameLimitKey 给出的那一个（可直接在 Redis 检索），
	// TTL 必须等于窗口长度（固定窗口，不随每次调用续期）。
	if keys := mr.Keys(); len(keys) != 1 || keys[0] != "agent:device:1001:frames" {
		t.Fatalf("Redis 键 = %v, want [agent:device:1001:frames]", keys)
	}
	if ttl := mr.TTL("agent:device:1001:frames"); ttl != agentFrameWindowSecs*time.Second {
		t.Fatalf("窗口键 TTL = %v, want %v（窗口长度即 maxFramesPerMin 的分母）", ttl, agentFrameWindowSecs*time.Second)
	}

	// 已鉴权 → 额度跟着**设备**走：同一个设备的另一个源 IP 共用同一份额度
	//（否则「同一个 agent 换 IP」就等于重置额度）。
	if ok, _ := lim.Allow(ctx, 1001, "198.51.100.9"); ok {
		t.Fatal("已鉴权帧的额度必须按设备计：换远端 IP 不得重置（键里不该出现 IP）")
	}
	// 另一台设备有独立的额度。
	if ok, err := lim.Allow(ctx, 1002, "192.0.2.7"); err != nil || !ok {
		t.Fatalf("另一台设备必须有自己的额度: ok=%v err=%v", ok, err)
	}

	// 未鉴权 → 键是 agent:ws:preauth:{IP}（**不含端口**），且与设备键是两份额度。
	if ok, err := lim.Allow(ctx, 0, "192.0.2.7"); err != nil || !ok {
		t.Fatalf("未鉴权帧必须同样受限（且此处是它的第 1 帧，应放行）: ok=%v err=%v", ok, err)
	}
	found := false
	for _, k := range mr.Keys() {
		if k == "agent:ws:preauth:192.0.2.7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未鉴权帧的键必须是 agent:ws:preauth:192.0.2.7（实际键 %v）", mr.Keys())
	}
}

// TestAgentFrameLimiter_DefaultThresholdAndNonPositiveClamp 钉住缺省 900 与
// 「非正数按缺省处理」（0 不得被解释成「不限流」：那会让防护静默失效且无任何症状）。
func TestAgentFrameLimiter_DefaultThresholdAndNonPositiveClamp(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"配置缺失 → 缺省 900", map[string]string{}},
		{"配置为 0 → 按缺省 900（不得解释成不限流）", map[string]string{configAgentMaxFramesPerMin: "0"}},
		{"配置为负 → 按缺省 900", map[string]string{configAgentMaxFramesPerMin: "-5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lim, _ := newFrameLimitFixture(t, tc.values)
			for i := 1; i <= defaultAgentMaxFramesPerMin; i++ {
				if ok, err := lim.Allow(ctx, 1001, "192.0.2.7"); err != nil || !ok {
					t.Fatalf("第 %d 帧应放行（缺省额度 %d）: ok=%v err=%v", i, defaultAgentMaxFramesPerMin, ok, err)
				}
			}
			if ok, err := lim.Allow(ctx, 1001, "192.0.2.7"); err != nil {
				t.Fatalf("第 %d 帧: %v", defaultAgentMaxFramesPerMin+1, err)
			} else if ok {
				t.Fatalf("第 %d 帧仍被放行 —— 缺省阈值不是 %d（或非正数被当成了「不限流」）",
					defaultAgentMaxFramesPerMin+1, defaultAgentMaxFramesPerMin)
			}
		})
	}
}

// errFrameStore / badShapeFrameStore 驱动适配器的两条故障分支：报错必须**原样上抛**
// （fail-open 的取向在消费方，实现不得自己猜），返回形状不符同样按错误处理。
type errFrameStore struct{}

func (errFrameStore) SlidingWindowIncr(context.Context, []string, int) (any, error) {
	return nil, errors.New("redis down")
}

type badShapeFrameStore struct{}

func (badShapeFrameStore) SlidingWindowIncr(context.Context, []string, int) (any, error) {
	return "not-an-array", nil
}

func TestAgentFrameLimiter_PropagatesFailureInsteadOfGuessing(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		store limiter.CacheStoreInterface
	}{
		{"存储报错", errFrameStore{}},
		{"原语返回形状不符", badShapeFrameStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lim := newAgentFrameLimiter(tc.store, fakeIntervalConfig{})
			ok, err := lim.Allow(ctx, 1001, "192.0.2.7")
			if err == nil {
				t.Fatalf("应把故障上抛（消费方据此 fail-open）：ok=%v err=%v", ok, err)
			}
		})
	}
}

// TestInit_AgentFrameRateLimitClosesWith4006 是**装配真的接了线**的端到端证据：
// 真实 Init（含真实注入）+ 真实 HTTP + 真实 WS + 真实 Redis。
//
// 阈值改成 3（正常约 8 帧/分远在 900 之下，只有调小才可触发），然后逐帧发上报：
// hello 帧以**未鉴权身份**（IP 键）计 1 帧，之后每帧计在**设备键**上，额度 3 →
// 第 4 条上报触发 4006。同时断言 Redis 里的未鉴权键**只含 host、不含源端口**——
// 那是这条规则唯一的失效方式，而真实 TCP 的源端口是随机的，只有走到真连接才测得到。
func TestInit_AgentFrameRateLimitClosesWith4006(t *testing.T) {
	f := newInitFixture(t)
	seedConfig(t, f.db, configAgentMaxFramesPerMin, "3")

	deps, err := f.initWith(t, initHooks{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	srv := httptest.NewServer(router.Setup(*deps))
	defer srv.Close()

	client, deviceID := dialAndEnroll(t, srv.URL, f)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// 3 帧上报 = 设备键上的第 1/2/3 帧，全部放行。
	for i := 0; i < 3; i++ {
		client.send(strconv.Itoa(2+i), agentproto.TypeAgentReportMetrics, metricsSample(now))
	}
	devKey := fmt.Sprintf("agent:device:%d:frames", deviceID)
	waitRedisValue(t, f.rdb, devKey, "3")

	// 第 4 帧：超限 → 4006。
	client.send("9", agentproto.TypeAgentReportMetrics, metricsSample(now))
	code, reason := readCloseCode(t, client)
	if code != agentproto.CloseRateLimited {
		t.Fatalf("超限关闭码 = %d (%s)，期望 CloseRateLimited=%d —— "+
			"限流未装配/阈值未生效/键算错都会落到这里", code, reason, agentproto.CloseRateLimited)
	}

	// 未鉴权键只含 host：带端口的话这里会是 127.0.0.1:<随机端口>，
	// 于是每建一条 TCP 连接就换一个键，限流形同虚设。
	preKeys, err := f.rdb.Keys(ctx, "agent:ws:preauth:*").Result()
	if err != nil {
		t.Fatalf("列 preauth 键: %v", err)
	}
	if len(preKeys) != 1 || preKeys[0] != "agent:ws:preauth:127.0.0.1" {
		t.Fatalf("preauth 键 = %v, want [agent:ws:preauth:127.0.0.1]（**不得带源端口**）", preKeys)
	}
	t.Logf("device_id=%d 超限收到 %d（%s）；preauth 键=%v", deviceID, code, reason, preKeys)
}

// ── 断言 7：启动期不需要显式 flushSvc.Bootstrap（懒初始化与 Bootstrap 等价）──
//
// 裁决：**等价**，故启动期**不调** Bootstrap（见 wireup.go 同处的注释）。依据三处，
// 逐条对应下面的断言：
//
//  1. **取值同源**：service/agent_metrics_flush.go 的 Bootstrap 用 `s.bootstrapStart()`
//     （第 153 行），readCursor 的缺键分支也调 `s.bootstrapStart()`（第 496 行）——
//     同一个函数，同一个 `alignDown(now−24h, 300)`（第 467-473 行，且夹 0 下界）。
//  2. **覆盖面同源**：Bootstrap 枚举 `raw.Index()` 逐设备 Init（第 149-160 行）；
//     FlushOnce 枚举同一个集合（第 171 行），而 flushDevice 在**任何提前 return 之前**
//     就先调 readCursor（第 267-276 行）—— 故一轮 flush 必然把「Bootstrap 会建的水位键」
//     全部建出来，不多也不少。
//  3. **已有水位不被改写**：Init 是 `EXISTS → GET`，否则 `SET`（pkg/agentmetrics/cursor.go
//     的 initCursorScript，第 101-107 行，SETNX 语义 + 回读生效值），故显式 Bootstrap 对
//     已存在的水位**没有任何副作用**（既有守卫 TestCursorStoreInitNeverOverwrites）。
//
// 唯一的差异是起点的**时间锚**：显式 Bootstrap 锚在进程启动时刻，懒初始化锚在首次读取
// 时刻。两者都 ≤ 扫描时刻的 now−24h，而早于 now−24h 的桶**必然**是空的（Bootstrap 自己的
// 注释：「从 0 起步会白扫 1970 年以来的 6_000_000+ 个桶…而结果与从 24h 起步完全一致」），
// 加上 `from = cursor + 300`（第 272 行）这套约定在两条路径上都会丢掉「cursor 自己那个桶」，
// 故这个锚差在数据上不可观测。
//
// 为什么另建一份 flush 服务：wireup **不导出** agentFlushSvc（同 e2e_test.go 文件头的
// 说明）。这份实例只用来调 CursorFor / Bootstrap —— 它们读写的是**同一个 Redis 上的
// 同一个键**；下面那一轮 flush 走的仍是 wireup 装配的那一份（调度器 → 注册表 → 任务）。
func TestInit_NoStartupBootstrapAndLazyCursorMatchesBootstrap(t *testing.T) {
	env := newE2EFixture(t)
	ctx := context.Background()

	// 1) 种一个「启动前就在上报」的设备：原始点直接进热层（走生产同源的
	//    RawStore.Append，它同时把设备登记进 agent:device:index —— Bootstrap 与
	//    FlushOnce 的**同一个**枚举源）。桶取 2h 前：已闭、且仍在 24h 热层窗内。
	const devID = uint64(1001)
	rawStore := agentmetrics.NewRawStore(env.f.rdb, agentmetrics.RawOptions{
		Step: 10 * time.Second, MaxPoints: 10000, QueryTTL: time.Second,
	})
	bucketTS := (time.Now().Unix()/300)*300 - 2*3600
	for i := 0; i < 3; i++ {
		if err := rawStore.Append(ctx, devID, metricsSample(bucketTS*1000+int64(i)*30_000)); err != nil {
			t.Fatalf("seed raw append: %v", err)
		}
	}

	// 2) 真实装配。启动期分区对账**故意失败**：6 张指标表因此不存在，下面那一轮 flush 的
	//    写路径必然失败 —— 这正是需要的（「写入失败 → 水位留在轮初」让我们能**直接读到**
	//    轮初那个懒初始化的值；否则整轮成功会把水位推进到 now−300，轮初水位就再也看不见了）。
	//    顺带复用断言 5 已覆盖的「对账失败不阻断启动」这条接缝。
	if _, err := env.f.initWith(t, initHooks{
		reconcile: func(context.Context) (service.PartitionStats, error) {
			return service.PartitionStats{}, fmt.Errorf("%w: DDL 被权限拒绝", service.ErrPartitionPartial)
		},
		aroundScheduler: func(build func() (*scheduler.Scheduler, error)) (*scheduler.Scheduler, error) {
			s, berr := build()
			if berr != nil {
				return nil, berr
			}
			env.sched = s
			return s, nil
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if env.sched == nil {
		t.Fatal("aroundScheduler 接缝没有拿到调度器 —— 后台任务无法按生产路径触发")
	}

	// 前置 A：写入方确实不存在（否则下面那轮 flush 会成功并把水位推走）。
	if present := metricTablesPresent(t, env.f.db); len(present) != 0 {
		t.Fatalf("前置：对账失败后不该有指标表，实际存在 %v", present)
	}

	// 前置 B（本任务要钉住的那条决策）：Init 之后水位键**必须不存在** —— 启动期没有显式
	// Bootstrap。设备已在活跃集合里，若 wireup 真的调了 flushSvc.Bootstrap，此刻它就在。
	// 键名用**契约字面量**（不复用 agentmetrics.CursorKey）：键名是 spec §7 的契约，
	// 复用实现会让键名写错时自洽通过（同 resolution_test.go / raw_test.go 的守卫体例）。
	cursorKey := fmt.Sprintf("agent:device:%d:cursor_5m", devID)
	if _, err := env.f.rdb.Get(ctx, cursorKey).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("Init 之后 %s 已存在（err=%v）—— 启动期显式调了 Bootstrap，"+
			"而 readCursor 会在键缺失时做同一件事（两者不该同时存在）", cursorKey, err)
	}

	svc := service.NewAgentMetricsFlushService(rawStore,
		repository.NewDeviceMetricRepository(env.f.db),
		repository.NewDeviceResourceRepository(env.f.db),
		flushCfgStub{}, logger.NewNop()).WithCursorStore(env.f.rdb)

	// ① 懒初始化的起点（直接断言，毫秒级、红灯可读）。CursorFor 就是为这类断言建的接缝：
	//    起点一旦退化成 0，靠「桶计数」这种间接信号只能得到超时而不是一条可读的失败。
	var start int64
	cands := withLazyCursorCandidates(func() {
		v, cerr := svc.CursorFor(ctx, devID)
		if cerr != nil {
			t.Fatalf("CursorFor: %v", cerr)
		}
		start = v
	})
	assertLazyCursorStart(t, start, cands, "CursorFor（readCursor 的缺键分支）")

	// ② 把键删掉：让下面那轮**真实** flush 自己走一遍「缺键 → 懒初始化」
	//    （① 已经把同一个值写过一次，删掉才算把两条路径都覆盖到）。
	if err := env.f.rdb.Del(ctx, cursorKey).Err(); err != nil {
		t.Fatalf("删水位键: %v", err)
	}

	// ③ 真实一轮 flush：经调度器 → 注册表 → 任务的真实触发（与 cron 到点同一条路径）。
	//    写路径必然失败（表不存在）→「失败不推进水位」→ 轮初水位留在键上。
	assertTaskTargetRegistered(t, e2eJobTargetFlush)
	job := &entity.SysJob{
		BaseEntity:   entity.BaseEntity{ID: e2eFlushJobID},
		Name:         e2eJobTargetFlush,
		JobGroup:     "等价性",
		InvokeTarget: e2eJobTargetFlush,
	}
	var runErr error
	cands = withLazyCursorCandidates(func() {
		runErr = env.sched.RunOnce(ctx, job)
	})
	if runErr == nil {
		t.Fatal("一轮 flush 竟然成功了 —— 说明它没有真的往表里写（种下的桶没被读到），" +
			"下面的水位断言会退化成「整轮成功后推进到的值」的永真对照")
	}
	got, err := env.f.rdb.Get(ctx, cursorKey).Int64()
	if err != nil {
		t.Fatalf("读水位（一轮 flush 之后）: %v", err)
	}
	assertLazyCursorStart(t, got, cands,
		"一轮真实 flush 之后的轮初水位（键缺失 → readCursor 懒初始化，且失败不推进）")

	// ④a 缺键时 Bootstrap 写入的起点与 ① 的懒初始化**是同一个值**。
	if err := env.f.rdb.Del(ctx, cursorKey).Err(); err != nil {
		t.Fatalf("删水位键: %v", err)
	}
	var bootErr error
	cands = withLazyCursorCandidates(func() { bootErr = svc.Bootstrap(ctx) })
	if bootErr != nil {
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	bootStart, err := env.f.rdb.Get(ctx, cursorKey).Int64()
	if err != nil {
		t.Fatalf("读水位（Bootstrap 之后）: %v", err)
	}
	assertLazyCursorStart(t, bootStart, cands, "Bootstrap（键缺失时写入的起点）")

	// ④b Bootstrap 对**已存在**的水位没有任何副作用（与 ④a 合起来才叫「等价」）。
	//    刻意写一个 48h 前的水位（与 Bootstrap 的候选值 24h 前明确不同）：若现值恰好等于
	//    候选值，「没被改写」就会退化成一句永真的断言。下面那条自检不是形式主义 ——
	//    将来谁把生产窗口改成 48h，它就会响铃，提醒这条断言已经变空。
	existing := alignDown5m(time.Now().Unix() - 2*lazyBootstrapWindowSec)
	for _, c := range cands {
		if existing == c {
			t.Fatalf("前置自检：写入的 %d 与 Bootstrap 的候选值相同 —— 「不改写已有水位」会永真", existing)
		}
	}
	if err := env.f.rdb.Set(ctx, cursorKey, existing, 0).Err(); err != nil {
		t.Fatalf("写入水位: %v", err)
	}
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap（已有水位）: %v", err)
	}
	kept, err := env.f.rdb.Get(ctx, cursorKey).Int64()
	if err != nil {
		t.Fatalf("读水位（Bootstrap 之后）: %v", err)
	}
	if kept != existing {
		t.Fatalf("Bootstrap 改写了已存在的水位：%d → %d —— 水位是「已成功落库到（含）」的"+
			"记账，覆写会让最近的水位被无谓重放，也会让两个实例对同一段桶用不同的区间起点",
			existing, kept)
	}
}

// flushCfgStub 是 AgentConfigGetter 的替身：一律返回调用方给的缺省值。
//
// 上面的断言只碰 CursorFor / Bootstrap，二者都不读配置（配置只影响 CloseRound 的
// CloseGrace 与回填节奏，这两个入口都不涉及）；用替身是为了不让这个测试去构造一条
// 与主题无关的依赖链。
type flushCfgStub struct{}

func (flushCfgStub) GetString(_ context.Context, _ string, def string) string { return def }
func (flushCfgStub) GetInt(_ context.Context, _ string, def int) int          { return def }

// lazyBootstrapWindowSec 与实现的 Bootstrap 窗口**同源**（= raw 点的 Redis 保留期）。
const lazyBootstrapWindowSec = int64(24 * 3600)

// alignDown5m 是测试侧独立的 5min 对齐实现（不复用实现里的函数，否则对齐写错也自洽）。
func alignDown5m(sec int64) int64 { return sec - sec%300 }

// withLazyCursorCandidates 在 fn 前后各采一次墙钟，返回期间「now−24h 对齐」的候选值。
//
// 为什么可以有两个候选：服务用的是**真实时钟**（wireup 没有时钟接缝，WithClock 只在
// 单测里用），若 fn 恰好跨过一个 5min 边界，前后两次对齐就会差 300s。这不是放宽期望值
// 的强度 —— 起点由服务自己的 now 唯一决定，而 fn 期间的 now 被这两个采样夹住了；
// 至于「起点是不是 now−24h 而不是 0/更早」，两个候选都能把它区分开（差 6_000_000 个桶）。
func withLazyCursorCandidates(fn func()) []int64 {
	before := alignDown5m(time.Now().Unix() - lazyBootstrapWindowSec)
	fn()
	after := alignDown5m(time.Now().Unix() - lazyBootstrapWindowSec)
	if after == before {
		return []int64{before}
	}
	return []int64{before, after}
}

func assertLazyCursorStart(t *testing.T, got int64, cands []int64, what string) {
	t.Helper()
	for _, c := range cands {
		if got == c {
			return
		}
	}
	t.Fatalf("%s = %d，期望 %v（= alignDown(now−24h, 300)，与 Bootstrap 的 s.bootstrapStart() "+
		"用同一个函数算；0 或任何更早的值都说明懒初始化退化了）", what, got, cands)
}

// metricsSample 造一条必填字段齐全的合法样本（限流集成用例只需要它过契约校验）。
func metricsSample(ts int64) *agentproto.MetricsSample {
	return &agentproto.MetricsSample{
		T:              ts,
		CPUUsedPercent: 12.5,
		Load1:          0.5,
		Load5:          0.4,
		Load15:         0.3,
		MemUsedPercent: 32.0,
		MemUsedMB:      1024,
		MemAvailableMB: 2048,
		TCPTotal:       10,
		TCPEstablished: 4,
		TCPListen:      3,
		UDPTotal:       2,
		ProcCount:      120,
		UptimeSec:      3600,
	}
}

// waitRedisValue 有界等待某个 Redis 键等于期望值（读循环是异步的，不能立即断言）。
func waitRedisValue(t *testing.T, rdb goredis.UniversalClient, key, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = rdb.Get(context.Background(), key).Result()
		if lastErr == nil && last == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %s = %q 超时（实际 %q, err=%v）—— 帧没有被计数到 Redis", key, want, last, lastErr)
}
