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
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/config"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/lifecycle"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
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
