package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// dockerUserReader 是 service.DockerUserLookup 的替身（未命中 = 仓储哨兵，
// 与 handler 测试的 dockerDeviceReader 同语义）。
type dockerUserReader struct{ users map[uint64]*entity.SysUser }

func (d dockerUserReader) FindByID(_ context.Context, id uint64) (*entity.SysUser, error) {
	if u, ok := d.users[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

// tasksTestEnv 在既有 handler 夹具上装配任务面（真 CmdStore + 替身联查）。
func tasksTestEnv(t *testing.T, perms []string) *dockerTestEnv {
	t.Helper()
	env := newTestDockerHandler(t, perms)
	dev := &entity.Device{Hostname: "h7"}
	dev.ID = 7
	users := dockerUserReader{users: map[uint64]*entity.SysUser{1: {Username: "alice"}}}
	taskSvc := service.NewDockerTaskService(env.cmds,
		dockerDeviceReader{devs: map[uint64]*entity.Device{7: dev}}, users, logger.NewNop())
	env.handler.WithTasks(taskSvc)
	return env
}

func seedTaskRecord(t *testing.T, cs *dockerstate.CmdStore, ref, statusMsg string) {
	t.Helper()
	rec := &dockerstate.CmdRecord{Ref: ref, DeviceID: 7, UserID: 1,
		Action: agentproto.DockerActionImagePull, Target: "alpine", CreatedAt: time.Now().UnixMilli()}
	if err := cs.Create(context.Background(), rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if statusMsg == "" {
		return // pending
	}
	if statusMsg == "ok" {
		if err := cs.Complete(context.Background(), rec, &agentproto.DockerCmdResult{Ref: ref, OK: true}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := cs.Complete(context.Background(), rec, &agentproto.DockerCmdResult{Ref: ref, OK: false, Error: statusMsg}); err != nil {
		t.Fatal(err)
	}
}

// TestTasksHandlerEnvelope 钉住端点契约：200 信封 + items 形状 + 结论句条目。
func TestTasksHandlerEnvelope(t *testing.T) {
	env := tasksTestEnv(t, []string{"docker:list"})
	seedTaskRecord(t, env.cmds, "t-ok", "ok")
	seedTaskRecord(t, env.cmds, "t-failed", "拉取镜像失败")
	seedTaskRecord(t, env.cmds, "t-pending", "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/tasks", nil)
	env.handler.Tasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"msg":"success"`) || !strings.Contains(body, `"ref":"t-failed"`) {
		t.Fatalf("信封与条目缺失: %s", body)
	}
	if !strings.Contains(body, `"summary":"拉取镜像失败"`) || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("终态结论句/状态投影缺失: %s", body)
	}
	if !strings.Contains(body, `"username":"alice"`) || !strings.Contains(body, `"hostname":"h7"`) {
		t.Fatalf("发起人/主机联查缺失: %s", body)
	}
	if !strings.Contains(body, `"hostId":"7"`) {
		t.Fatalf("hostId 必须 string 编码（雪花值前端精度）: %s", body)
	}
}

// TestTasksHandlerBadQuery 钉住 400：非法 status/action 结论句（service 已测
// 值域，这里钉 handler 的转达形态）。
func TestTasksHandlerBadQuery(t *testing.T) {
	env := tasksTestEnv(t, []string{"docker:list"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/tasks?status=finished", nil)
	env.handler.Tasks(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 status 必须 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "status 仅支持 pending 或 done") {
		t.Fatalf("必须给结论句: %s", w.Body.String())
	}
}

// TestTasksRoutePermGuard 钉住路由级权限：无 docker:list 的登录用户拿 403 ——
// 用真 PermissionGuard 挂在裸引擎上（与路由挂载同形态：claims 中间件 → perm →
// handler），无 list 档 403、有 list 档 200。
func TestTasksRoutePermGuard(t *testing.T) {
	guarded := func(perms []string) *httptest.ResponseRecorder {
		env := tasksTestEnv(t, perms)
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())
		r := gin.New()
		// 登录态由 auth 中间件提供（路由组前一层）；这里代之以直接写 claims。
		r.Use(func(c *gin.Context) { c.Set(middleware.CtxClaims, testClaims(1)); c.Next() })
		r.GET("/docker/tasks", guard.Permission(permission.PermDockerList), env.handler.Tasks)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/docker/tasks", nil)
		r.ServeHTTP(w, req)
		return w
	}

	if w := guarded([]string{"docker:list"}); w.Code != http.StatusOK {
		t.Fatalf("有 docker:list 必须 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := guarded([]string{"docker:manage"}); w.Code != http.StatusForbidden {
		t.Fatalf("无 list 档必须 403（路由静态 perm），got %d (%s)", w.Code, w.Body.String())
	}
}
