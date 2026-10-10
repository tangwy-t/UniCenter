package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 镜像版本漂移视图（GET /docker/image-drift）的 handler 测试：查询绑定 + 信封 +
// 空行形态 + 路由级权限。数据面语义已在 service 层钉住（docker_drift_test.go）。

// newDriftHandlerEnv 装配一个可读的漂移视图 handler：miniredis + 真读面
//（设备替身表：主机 7 属组 prod、主机 8 无组）。与既有 handler 夹具同款
//（复用 dockerDeviceReader / dockerCfg / permAuthSvc 等替身）。
func newDriftHandlerEnv(t *testing.T, perms []string) (*DockerHandler, *dockerstate.Store, *goredis.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := dockerstate.NewStore(rdb)
	dev7 := &entity.Device{Hostname: "uni-105", GroupName: "prod"}
	dev7.ID = 7
	dev8 := &entity.Device{Hostname: "uni-106"}
	dev8.ID = 8
	readSvc := service.NewDockerService(store,
		dockerDeviceReader{devs: map[uint64]*entity.Device{7: dev7, 8: dev8}}, dockerCfg{}, logger.NewNop())
	guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())
	return NewDockerHandler(readSvc, nil, nil, guard, logger.NewNop()), store, rdb
}

// TestImageDriftHandlerEnvelope 钉住端点契约：200 信封 + 漂移行形状（repoTag/
// hostCount/distinctDigests/多数派/少数派主机引用）+ 分页回显 + 空行非 null。
func TestImageDriftHandlerEnvelope(t *testing.T) {
	hdl, store, _ := newDriftHandlerEnv(t, []string{"docker:list"})
	ctx := context.Background()
	now := time.Now()
	// 主机 7（prod）= aaa、主机 8 = bbb：两摘要 → 一行带一个少数派主机引用。
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Images: []agentproto.DockerImage{{ID: "sha256:aaa", RepoTags: []string{"app:1"}}}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Images: []agentproto.DockerImage{{ID: "sha256:bbb", RepoTags: []string{"app:1"}}}}, now); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/image-drift", nil)
	hdl.ImageDrift(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"msg":"success"`) || !strings.Contains(body, `"repoTag":"app:1"`) {
		t.Fatalf("信封/漂移行缺失: %s", body)
	}
	if !strings.Contains(body, `"hostCount":2`) || !strings.Contains(body, `"distinctDigests":2`) ||
		!strings.Contains(body, `"dominantDigest":"sha256:aaa"`) || !strings.Contains(body, `"dominantHostCount":1`) {
		t.Fatalf("分布字段缺失: %s", body)
	}
	// 少数派主机引用的 hostId 必须 string 编码（雪花值前端精度，与全模块一致）。
	if !strings.Contains(body, `"hostId":"8"`) || !strings.Contains(body, `"hostname":"uni-106"`) {
		t.Fatalf("少数派主机引用缺失（hostId string 编码）: %s", body)
	}
	// 分页三件套回显（缺省 page=1 / pageSize=50）。
	if !strings.Contains(body, `"total":1`) || !strings.Contains(body, `"page":1`) || !strings.Contains(body, `"pageSize":50`) {
		t.Fatalf("分页信封缺失（total/page/pageSize）: %s", body)
	}

	// 不匹配的 keyword → 空行但 items/unavailableHosts 是空数组而非 null。
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/image-drift?keyword=nomatch", nil)
	hdl.ImageDrift(c)
	if w.Code != http.StatusOK {
		t.Fatalf("keyword 查询必须 200: %d (%s)", w.Code, w.Body.String())
	}
	body = w.Body.String()
	if !strings.Contains(body, `"items":[]`) || !strings.Contains(body, `"total":0`) ||
		!strings.Contains(body, `"unavailableHosts":[]`) {
		t.Fatalf("空行必须回空数组而非 null: %s", body)
	}

	// group 过滤经由查询串绑定到服务端（组外主机被排除）：group=prod → hostCount=1。
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/image-drift?group=prod", nil)
	hdl.ImageDrift(c)
	if !strings.Contains(w.Body.String(), `"hostCount":1`) {
		t.Fatalf("group 过滤未生效（应只统计主机 7）: %s", w.Body.String())
	}
}

// TestImageDriftRoutePermGuard 钉住路由级权限：无 docker:list 的登录用户拿 403 ——
// 用真 PermissionGuard 挂在裸引擎上（与路由挂载同形态）。
func TestImageDriftRoutePermGuard(t *testing.T) {
	guarded := func(perms []string) *httptest.ResponseRecorder {
		hdl, _, _ := newDriftHandlerEnv(t, perms)
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.CtxClaims, testClaims(1)); c.Next() })
		r.GET("/docker/image-drift", guard.Permission(permission.PermDockerList), hdl.ImageDrift)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/docker/image-drift", nil)
		r.ServeHTTP(w, req)
		return w
	}
	if w := guarded([]string{"docker:list"}); w.Code != http.StatusOK {
		t.Fatalf("有 docker:list 必须 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := guarded(nil); w.Code != http.StatusForbidden {
		t.Fatalf("无 docker:list 必须 403, got %d (%s)", w.Code, w.Body.String())
	}
}