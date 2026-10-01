package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/jwt"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 测试替身 ────────────────────────────────────────────────────────────

// fakeDockerSender 是 service.DockerCmdSender 的替身：记录**解码后**的指令与
// 下行控制帧（input/resize/cancel）。
//
// 断言原始消息只能证明「发出去了点什么」；受理路径的契约是「agent 能解回一条 DockerCmd」，
// 流通道的契约是「agent 能解回一条 CoreDockerFrame」。
type fakeDockerSender struct {
	mu      sync.Mutex
	sent    []*agentproto.DockerCmd
	frames  []*agentproto.CoreDockerFrame
	devices []uint64
	// offline 打开时返回 agenthub.ErrDeviceOffline（受理流程据此不受理、不发 ref）。
	offline bool
}

func (f *fakeDockerSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	if f.offline {
		return agenthub.ErrDeviceOffline
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = append(f.devices, deviceID)
	switch msg.Type {
	case agentproto.TypeCoreDockerCmd:
		var cmd agentproto.DockerCmd
		if err := msg.DecodeData(&cmd); err == nil {
			f.sent = append(f.sent, &cmd)
		}
	case agentproto.TypeCoreDockerFrame:
		var fr agentproto.CoreDockerFrame
		if err := msg.DecodeData(&fr); err == nil {
			f.frames = append(f.frames, &fr)
		}
	}
	return nil
}

// frameOps 返回已送达控制帧的 (op, cols, rows, data) 快照（并发安全）。
func (f *fakeDockerSender) frameOps() []*agentproto.CoreDockerFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*agentproto.CoreDockerFrame, len(f.frames))
	copy(out, f.frames)
	return out
}

// deviceIDs 返回已送达消息的设备序列快照（并发安全）。
func (f *fakeDockerSender) deviceIDs() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint64, len(f.devices))
	copy(out, f.devices)
	return out
}

// permAuthSvc / permStore / permCfg 是 PermissionGuard 的三个协作者的最小桩。
//
// 这里用**真实的** middleware.PermissionGuard（而不是再造一个「按列表判定」的
// PermChecker 替身）：替身会把「403 是谁写的、放行条件是什么」变成对替身本身的断言，
// 而本文件要钉住的恰恰是 handler 与守卫的集成事实。三个桩只替换 IO
// （回源/缓存/配置），判定逻辑原样是真代码。
type permAuthSvc struct{ perms []string }

func (s *permAuthSvc) GetUserPermissions(context.Context, uint64) ([]string, error) {
	return s.perms, nil
}

type permStore struct{}

func (permStore) LoadPerms(context.Context, uint64, string) ([]string, error) { return nil, nil }
func (permStore) StorePerms(context.Context, uint64, string, []string, time.Duration) error {
	return nil
}

type permCfg struct{}

func (permCfg) GetString(context.Context, string, string) string { return "" }
func (permCfg) GetInt(context.Context, string, int) int          { return 7200 }
func (permCfg) GetBool(context.Context, string, bool) bool       { return false }

// dockerDeviceReader 是 service.DockerDeviceReader 的替身。
//
// 未命中返回仓储哨兵（repository.ErrNotFound），与真仓储同语义：读面据此分辨
// 「设备已删（404 / 顺手清理）」与「读库故障（500 / 原地跳过）」——
// 返回 (nil, nil) 会把这两种情况混成「从未上报」。
type dockerDeviceReader struct{ devs map[uint64]*entity.Device }

func (d dockerDeviceReader) FindByID(_ context.Context, id uint64) (*entity.Device, error) {
	if dev, ok := d.devs[id]; ok {
		return dev, nil
	}
	return nil, repository.ErrNotFound
}

// dockerCfg 是 service.AgentConfigGetter 的替身：未配置时回落缺省。
type dockerCfg struct{}

func (dockerCfg) GetString(context.Context, string, string) string { return "" }
func (dockerCfg) GetInt(_ context.Context, _ string, def int) int  { return def }

// ── 夹具 ────────────────────────────────────────────────────────────────

// dockerTestEnv 汇集一次 docker handler 测试的观测点。
type dockerTestEnv struct {
	handler  *DockerHandler
	cmds     *dockerstate.CmdStore
	store    *dockerstate.Store
	sender   *fakeDockerSender
	sessions *dockerstream.Registry
	tickets  *dockerstate.TicketStore
	streams  *service.DockerStreamService
	redis    *goredis.Client
	// mr 是 miniredis 实例：票据过期要能被 FastForward 确定性驱动。
	mr *miniredis.Miniredis
}

// newTestDockerHandler 构造最小可用的 handler：真守卫（桩掉三个 IO 协作者）+
// 真实构造的三个 docker 服务（miniredis），sender 为记录型替身。
func newTestDockerHandler(t *testing.T, perms []string) *dockerTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	store := dockerstate.NewStore(rdb)
	cmds := dockerstate.NewCmdStore(rdb)
	tickets := dockerstate.NewTicketStore(rdb)
	sessions := dockerstream.NewRegistry(logger.NewNop())
	sender := &fakeDockerSender{}
	guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())

	cmdsSvc := service.NewDockerCmdService(cmds, sender, store, logger.NewNop()).WithStreamSessions(sessions)
	readSvc := service.NewDockerService(store, dockerDeviceReader{}, dockerCfg{}, logger.NewNop())
	streamSvc := service.NewDockerStreamService(sessions, tickets, sender, logger.NewNop())
	return &dockerTestEnv{
		handler:  NewDockerHandler(readSvc, cmdsSvc, streamSvc, guard, logger.NewNop()),
		cmds:     cmds,
		store:    store,
		sender:   sender,
		sessions: sessions,
		tickets:  tickets,
		streams:  streamSvc,
		redis:    rdb,
		mr:       mr,
	}
}

// testClaims 造一个登录态（身份唯一来源是 middleware.CtxClaims）。
func testClaims(uid uint64) *jwt.Claims { return &jwt.Claims{UserID: uid} }

// newCmdContext 造一个 POST /docker/hosts/:id/cmds 的测试上下文。
func newCmdContext(body string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/docker/hosts/7/cmds", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	c.Set(middleware.CtxClaims, testClaims(1))
	return w, c
}

// ── 指令受理：权限按 action 变化 ────────────────────────────────────────

// TestSendCmdRequiresActionPermission 钉住「每个 action 类别无权限即 403」：
// 指令面的权限码随 action 变化（logs→docker:inspect），因此无权限时必须是 403，
// 而不是 401（那是未登录）也不是放行（那会让任一登录用户都能读容器日志）。
func TestSendCmdRequiresActionPermission(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		havePerm   []string
		wantStatus int
	}{
		{"无任何 docker 权限 → 403", `{"action":"container:logs","target":"mysql"}`, nil, http.StatusForbidden},
		{"有 docker:list 但 action 要 inspect → 403", `{"action":"container:logs","target":"mysql"}`,
			[]string{"docker:list"}, http.StatusForbidden},
		{"有 docker:inspect 且 action 一致 → 进入受理", `{"action":"container:logs","target":"mysql"}`,
			[]string{"docker:inspect"}, http.StatusAccepted},
		{"admin 通配 → 放行", `{"action":"container:logs","target":"mysql"}`, []string{"admin"}, http.StatusAccepted},
		// 创建面（4a）：权限与启停同级 —— docker:manage；有只读权限不够，无权限必 403。
		{"create 无权限 → 403", `{"action":"container:create","options":{"image":"nginx:1.27"}}`, nil, http.StatusForbidden},
		{"有 docker:list 但 create 要 manage → 403", `{"action":"container:create","options":{"image":"nginx:1.27"}}`,
			[]string{"docker:list"}, http.StatusForbidden},
		{"有 docker:manage → create 受理", `{"action":"container:create","options":{"image":"nginx:1.27"}}`,
			[]string{"docker:manage"}, http.StatusAccepted},
	}
	// create 没有 target（动作对象是「将要诞生的容器」，语义都在 options.image）：
	// 它不需要给这张按 action 变化的表加任何特判 —— 权限码仍由策略表的一行给出，
	// 这里的用例只是钉住「docker:manage 才放行」。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestDockerHandler(t, tc.havePerm)
			w, c := newCmdContext(tc.body)
			env.handler.SendCmd(c)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, tc.wantStatus, w.Body.String())
			}
			switch tc.wantStatus {
			case http.StatusForbidden:
				// 被拒时**不得**下发：403 只是响应，指令若已上路就是越权执行。
				if len(env.sender.sent) != 0 {
					t.Fatalf("无权限的请求不得下发指令: %+v", env.sender.sent)
				}
				// 拒绝话术与其他权限入口同源（apperror.Forbidden）。
				if !strings.Contains(w.Body.String(), "无操作权限") {
					t.Fatalf("拒绝话术必须与路由级权限一致, body=%s", w.Body.String())
				}
			case http.StatusAccepted:
				if len(env.sender.sent) != 1 || env.sender.devices[0] != 7 {
					t.Fatalf("受理后必须下发到该设备: %+v devices=%v", env.sender.sent, env.sender.devices)
				}
			}
		})
	}
}

// TestSendCmdCreateOptionsPassThrough：create 专属 options 必须原样进入协议载荷
// （HTTP 驼峰 → 协议蛇形的一次性映射入口是 toProtocolOptions；start=false 的
// 「显式不启动」与缺席不同义，指针必须保住）。
func TestSendCmdCreateOptionsPassThrough(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	w, c := newCmdContext(`{"action":"container:create","options":{"image":"nginx:1.27","name":"web-1",` +
		`"ports":["8080:80"],"env":["MODE=prod"],"mounts":["data:/d:ro"],"restartPolicy":"always",` +
		`"cpuLimit":1.5,"memLimitMb":512,"network":"app-net","start":false}}`)
	env.handler.SendCmd(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", w.Code, w.Body.String())
	}
	if len(env.sender.sent) != 1 {
		t.Fatalf("受理后必须下发，实际 %+v", env.sender.sent)
	}
	o := env.sender.sent[0].Options
	if o.Image != "nginx:1.27" || o.Name != "web-1" || len(o.Ports) != 1 || o.Ports[0] != "8080:80" ||
		len(o.Env) != 1 || o.Env[0] != "MODE=prod" || len(o.Mounts) != 1 || o.Mounts[0] != "data:/d:ro" ||
		o.RestartPolicy != "always" || o.CPULimit != 1.5 || o.MemLimitMB != 512 ||
		o.Network != "app-net" || o.Start == nil || *o.Start {
		t.Fatalf("create options 未原样进入协议载荷: %+v", o)
	}
}

// 等价于 root shell（spec §10 保护档）—— 它能越过保护清单的默认拒绝去停/删/重建
// 底座。因此它要求 docker:exec 级（或 admin），与「能不能删东西」（docker:delete）
// 分开授予：只有 docker:delete 的人**不能** force，这正是「先停再删」两步绕过
// 保护清单被堵住的地方。
func TestSendCmdForceRequiresExecPermission(t *testing.T) {
	cases := []struct {
		name       string
		perms      []string
		body       string
		wantStatus int
		wantForce  bool
	}{
		{"有 delete 但无 exec，force 被拒", []string{"docker:delete"},
			`{"action":"container:remove","target":"mysql","options":{"force":true},"confirm":"DELETE"}`,
			http.StatusForbidden, false},
		{"有 delete 与 exec，force 放行", []string{"docker:delete", "docker:exec"},
			`{"action":"container:remove","target":"mysql","options":{"force":true},"confirm":"DELETE"}`,
			http.StatusAccepted, true},
		{"admin 通配放行", []string{"admin"},
			`{"action":"container:remove","target":"mysql","options":{"force":true},"confirm":"DELETE"}`,
			http.StatusAccepted, true},
		{"不带 force 只需 delete", []string{"docker:delete"},
			`{"action":"container:remove","target":"mysql"}`,
			http.StatusAccepted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestDockerHandler(t, tc.perms)
			w, c := newCmdContext(tc.body)
			env.handler.SendCmd(c)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, tc.wantStatus, w.Body.String())
			}
			switch tc.wantStatus {
			case http.StatusForbidden:
				// force 权限不足时**不得**下发：403 只是响应，指令若已上路就是越权执行。
				if len(env.sender.sent) != 0 {
					t.Fatalf("force 权限不足的请求不得下发指令: %+v", env.sender.sent)
				}
				if !strings.Contains(w.Body.String(), "无操作权限") {
					t.Fatalf("拒绝话术必须与权限入口同源, body=%s", w.Body.String())
				}
			case http.StatusAccepted:
				if len(env.sender.sent) != 1 || env.sender.devices[0] != 7 {
					t.Fatalf("受理后必须下发到该设备: %+v devices=%v", env.sender.sent, env.sender.devices)
				}
				// force 位必须原样到达 agent（保护强制在 agent 侧按 force 判定）。
				if env.sender.sent[0].Options.Force != tc.wantForce {
					t.Fatalf("force 位与请求不符: got %v want %v", env.sender.sent[0].Options.Force, tc.wantForce)
				}
			}
		})
	}
}

// TestSendCmdAcceptedEnvelopeMatchesSuccess：202 的信封必须与 app.Success **逐字同形**
// （同一个 app.Response 结构体、同一个 code/message），否则前端拦截器认不出这个响应；
// 同时 ref 必须是十进制串（协议 isDecimalID 的硬要求，JS 侧不能丢精度）。
func TestSendCmdAcceptedEnvelopeMatchesSuccess(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	w, c := newCmdContext(`{"action":"container:logs","target":"mysql","options":{"tail":50}}`)
	env.handler.SendCmd(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", w.Code, w.Body.String())
	}
	var got struct {
		Code    int             `json:"code"`
		Message string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, w.Body.String())
	}
	if got.Code != 0 || got.Message != "success" {
		t.Fatalf("信封必须与 app.Success 同形（code=0/msg=success）, got code=%d msg=%q", got.Code, got.Message)
	}
	if strings.Contains(w.Body.String(), `"message"`) {
		t.Fatalf(`信封字段名只能是 "msg"（app.Response 的 tag）, body=%s`, w.Body.String())
	}
	var data struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(got.Data, &data); err != nil || data.Ref == "" {
		t.Fatalf("data.ref 缺失: %s", w.Body.String())
	}
	if strings.Trim(data.Ref, "0123456789") != "" {
		t.Fatalf("ref 必须是十进制串（协议硬要求）, got %q", data.Ref)
	}
	// 受理即下发：载荷里的 options 必须是解析后的值（不是零值）。
	if len(env.sender.sent) != 1 || env.sender.sent[0].Options.Target != "mysql" ||
		env.sender.sent[0].Options.Tail != 50 {
		t.Fatalf("下发的载荷与请求不符: %+v", env.sender.sent)
	}
}

// TestSendCmdRejectsBadRequests：三类 400 各自对应一个「不该走到下发」的输入。
func TestSendCmdRejectsBadRequests(t *testing.T) {
	t.Run("未知 action", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"admin"})
		w, c := newCmdContext(`{"action":"container:teleport","target":"mysql"}`)
		env.handler.SendCmd(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("未知 action 必须 400, got %d (%s)", w.Code, w.Body.String())
		}
		if len(env.sender.sent) != 0 {
			t.Fatal("未知 action 不得下发")
		}
	})

	t.Run("四期 action 已过期次闸（缺参数仍 400）", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"admin"})
		// 四期（配置编辑）已交付：期次闸不再拦这两条 —— 它们会走到参数校验，缺
		// content/base_hash 时以「指令参数不合法」400 收场（与「尚未开放」是两回事：
		// 前者要补参数，后者要升级 agent）。
		for _, body := range []string{
			`{"action":"compose.file:patch","target":"uni-center"}`,
			`{"action":"compose.file:write","target":"uni-center"}`,
		} {
			w, c := newCmdContext(body)
			env.handler.SendCmd(c)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "参数不合法") {
				t.Fatalf("四期 action 缺参数必须 400「参数不合法」, body=%s got %d (%s)",
					body, w.Code, w.Body.String())
			}
		}
		if len(env.sender.sent) != 0 {
			t.Fatalf("参数校验拒绝的请求不得下发: %+v", env.sender.sent)
		}
	})

	t.Run("body 不合法", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"admin"})
		w, c := newCmdContext(`{"target":`)
		env.handler.SendCmd(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("坏 body 必须 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("路径 id 非法", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:inspect"})
		w, c := newCmdContext(`{"action":"container:logs","target":"mysql"}`)
		c.Params = gin.Params{{Key: "id", Value: "abc"}}
		env.handler.SendCmd(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非十进制 id 必须 400（不得悄悄当成 0 号设备）, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// TestSendCmdWithoutClaims：身份缺失必须是 401（Ensure 的失败路径之一），
// 且不得下发 —— 指令记录要落 user_id，「不知道是谁」时受理等同伪造归属。
func TestSendCmdWithoutClaims(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	w, c := newCmdContext(`{"action":"container:logs","target":"mysql"}`)
	c.Keys = map[any]any{} // 清掉 claims（未登录 / 中间件未跑）
	env.handler.SendCmd(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无身份必须 401, got %d (%s)", w.Code, w.Body.String())
	}
	if len(env.sender.sent) != 0 {
		t.Fatal("无身份的请求不得下发")
	}
}

// TestSendCmdOfflineDeviceNotAccepted：设备离线时**不受理**（不发 ref）——
// 给离线设备发一个 ref 只会让用户对着「超时」等 30 秒，而真实原因是它没连上。
func TestSendCmdOfflineDeviceNotAccepted(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	env.sender.offline = true
	w, c := newCmdContext(`{"action":"container:logs","target":"mysql"}`)
	env.handler.SendCmd(c)
	if w.Code == http.StatusAccepted {
		t.Fatalf("离线设备不得返回 202（那会让前端去轮询一个不存在的指令）, body=%s", w.Body.String())
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("离线语义是 5xx（服务端此刻给不出结果）, got %d (%s)", w.Code, w.Body.String())
	}
}

// ── 指令轮询 ────────────────────────────────────────────────────────────

// TestCmdResultEnforcesRecordedPermission：轮询的权限码取自**记录**（服务端写的，
// 用户改不了）——有 docker:list 但没有该记录要求的 docker:inspect 时必须 403，
// 否则「能看主机列表」就等于「能读所有人查过的日志与 inspect 结果」。
func TestCmdResultEnforcesRecordedPermission(t *testing.T) {
	ctx := context.Background()

	newEnvWithRecord := func(t *testing.T) (*dockerTestEnv, string) {
		env := newTestDockerHandler(t, nil)
		rec := &dockerstate.CmdRecord{Ref: "123456789", DeviceID: 7, Action: "container:logs",
			Target: "mysql", UserID: 1, Perm: "docker:inspect", CreatedAt: time.Now().UnixMilli()}
		if err := env.cmds.Create(ctx, rec, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		return env, rec.Ref
	}

	newResultContext := func(ref string) (*httptest.ResponseRecorder, *gin.Context) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+ref, nil)
		c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: ref}}
		c.Set(middleware.CtxClaims, testClaims(1))
		return w, c
	}

	t.Run("无该记录的权限 → 403", func(t *testing.T) {
		env, ref := newEnvWithRecord(t)
		// 重新构造一个只有 docker:list 的守卫环境（同一份 store/cmds）。
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: []string{"docker:list"}}, permStore{}, permCfg{}, logger.NewNop())
		env.handler.guard = guard
		w, c := newResultContext(ref)
		env.handler.CmdResult(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("记录要求 docker:inspect → 403, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("有该记录的权限 → 200", func(t *testing.T) {
		env, ref := newEnvWithRecord(t)
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: []string{"docker:inspect"}}, permStore{}, permCfg{}, logger.NewNop())
		env.handler.guard = guard
		w, c := newResultContext(ref)
		env.handler.CmdResult(c)
		if w.Code != http.StatusOK {
			t.Fatalf("有权限必须 200, got %d (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"status":"pending"`) {
			t.Fatalf("响应必须带记录状态, body=%s", w.Body.String())
		}
	})

	t.Run("换一台设备查同一个 ref → 404", func(t *testing.T) {
		env, ref := newEnvWithRecord(t)
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: []string{"admin"}}, permStore{}, permCfg{}, logger.NewNop())
		env.handler.guard = guard
		w, c := newResultContext(ref)
		c.Params = gin.Params{{Key: "id", Value: "8"}, {Key: "ref", Value: ref}}
		env.handler.CmdResult(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("设备不匹配必须 404（否则 ref 成了探测别的主机的钥匙）, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// ── 读面 ────────────────────────────────────────────────────────────────

// TestDockerReadPathsEnvelope：读面两条端点的成功/失败都必须走 app 的响应助手
// （前端拦截器只认这一种信封），且不存在的设备是 404 而不是一份空快照。
func TestDockerReadPathsEnvelope(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})

	t.Run("主机清单空集合 → 200 + data.list 为数组（不是 null）", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts", nil)
		env.handler.Hosts(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"list":[]`) {
			t.Fatalf("空清单必须是空数组（前端少一层判空）, body=%s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"msg":"success"`) {
			t.Fatalf("读面必须走 app.Success 的信封, body=%s", w.Body.String())
		}
	})

	t.Run("统一表空舰队 → 200 + data.items 为数组（不是 null）", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/containers", nil)
		env.handler.Workloads(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Fatalf("空表必须是空数组（前端少一层判空）, body=%s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"msg":"success"`) {
			t.Fatalf("读面必须走 app.Success 的信封, body=%s", w.Body.String())
		}
	})

	t.Run("统一表 state 非法 → 400 结论句", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/containers?state=paused", nil)
		env.handler.Workloads(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "state 仅支持 running 或 stopped") {
			t.Fatalf("400 必须给结论句（不得静默当成不过滤）, body=%s", w.Body.String())
		}
	})

	t.Run("不存在的设备 → 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/9/state", nil)
		c.Params = gin.Params{{Key: "id", Value: "9"}}
		env.handler.State(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "设备不存在") {
			t.Fatalf("404 话术必须与其他设备端点一致, body=%s", w.Body.String())
		}
	})

	t.Run("从未上报的设备 → 200 + neverReported", func(t *testing.T) {
		dev := &entity.Device{Hostname: "h1"}
		dev.ID = 7
		readSvc := service.NewDockerService(env.store, dockerDeviceReader{devs: map[uint64]*entity.Device{7: dev}},
			dockerCfg{}, logger.NewNop())
		env.handler.svc = readSvc

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/state", nil)
		c.Params = gin.Params{{Key: "id", Value: "7"}}
		env.handler.State(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
		var body struct {
			Data struct {
				NeverReported bool `json:"neverReported"`
				Containers    any  `json:"containers"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Data.NeverReported {
			t.Fatalf("从未上报必须 neverReported=true（而不是伪装成零容器的正常主机）: %s", w.Body.String())
		}
		if _, ok := body.Data.Containers.([]any); !ok {
			t.Fatalf("五个清单必须是空数组而非 null: %s", w.Body.String())
		}
	})
}
