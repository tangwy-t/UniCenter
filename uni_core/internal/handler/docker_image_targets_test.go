package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// 镜像目标版本（Wave 4c：GET/POST/DELETE /docker/image-targets）的 handler 测试：
// 绑定 + 信封 + 结论句 + 路由级权限。数据面语义已由 service 层测试钉住。

// hdlTargetRepo 是 handler 测试用的内存目标存储面。
type hdlTargetRepo struct{ rows map[string]entity.DockerImageTarget }

func (r *hdlTargetRepo) Upsert(_ context.Context, row *entity.DockerImageTarget) error {
	r.rows[row.RepoTag] = *row
	return nil
}
func (r *hdlTargetRepo) Delete(_ context.Context, repoTag string) error { delete(r.rows, repoTag); return nil }
func (r *hdlTargetRepo) List(_ context.Context) ([]entity.DockerImageTarget, error) {
	out := make([]entity.DockerImageTarget, 0, len(r.rows))
	for _, v := range r.rows {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoTag < out[j].RepoTag })
	return out, nil
}
func (r *hdlTargetRepo) FindByRepoTags(_ context.Context, repoTags []string) (map[string]entity.DockerImageTarget, error) {
	out := map[string]entity.DockerImageTarget{}
	for _, t := range repoTags {
		if v, ok := r.rows[t]; ok {
			out[t] = v
		}
	}
	return out, nil
}

// newImageTargetHandlerEnv 装配一个只挂目标存储面的 handler（读面三端点不碰 store）。
func newImageTargetHandlerEnv(t *testing.T, perms []string) (*DockerHandler, *hdlTargetRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &hdlTargetRepo{rows: map[string]entity.DockerImageTarget{}}
	svc := service.NewDockerService(nil, nil, nil, logger.NewNop()).WithImageTargets(repo)
	guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())
	return NewDockerHandler(svc, nil, nil, guard, logger.NewNop()), repo
}

// TestImageTargetHandlerEnvelope 钉住三端点契约：POST 声明回显 / 校验 400 结论句 /
// GET 清单形状 / DELETE（query）幂等，均走 app.Response 信封。
func TestImageTargetHandlerEnvelope(t *testing.T) {
	hdl, _ := newImageTargetHandlerEnv(t, []string{"docker:manage", "docker:list"})

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/docker/image-targets", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		hdl.SetImageTarget(c)
		return w
	}

	// 合法声明 → 200 + 回显。
	if w := post(`{"repoTag":"app:1","targetDigest":"sha256:aaa"}`); w.Code != http.StatusOK {
		t.Fatalf("合法声明应 200, got %d (%s)", w.Code, w.Body.String())
	} else if !strings.Contains(w.Body.String(), `"repoTag":"app:1"`) ||
		!strings.Contains(w.Body.String(), `"targetDigest":"sha256:aaa"`) {
		t.Fatalf("声明回显缺失: %s", w.Body.String())
	}

	// 非法声明（digest 无 sha256 前缀）→ 400 + 结论句。
	if w := post(`{"repoTag":"app:1","targetDigest":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("非法摘要应 400, got %d (%s)", w.Code, w.Body.String())
	} else if !strings.Contains(w.Body.String(), "sha256") {
		t.Fatalf("400 应带显式结论句: %s", w.Body.String())
	}

	// 非法 JSON → 400。
	if w := post(`{`); w.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400, got %d (%s)", w.Code, w.Body.String())
	}

	// GET 清单 → 200 + items 形状。
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/image-targets", nil)
		hdl.ListImageTargets(c)
		if w.Code != http.StatusOK {
			t.Fatalf("清单应 200, got %d (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"total":1`) || !strings.Contains(w.Body.String(), `"items"`) {
			t.Fatalf("清单形状缺失: %s", w.Body.String())
		}
	}

	// DELETE（query 带 repoTag）→ 200；再删一次仍 200（幂等）。
	del := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/docker/image-targets"+query, nil)
		hdl.ClearImageTarget(c)
		return w
	}
	if w := del("?repoTag=app%3A1"); w.Code != http.StatusOK {
		t.Fatalf("清除应 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := del("?repoTag=app%3A1"); w.Code != http.StatusOK {
		t.Fatalf("重复清除必须幂等 200, got %d (%s)", w.Code, w.Body.String())
	}
	// 缺 repoTag → 400。
	if w := del(""); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 repoTag 应 400, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestImageTargetRoutePermGuard 钉住路由级权限：写端点需 docker:manage、读端点需
// docker:list，缺权限一律 403（用真 PermissionGuard 挂在裸引擎上，与路由挂载同形态）。
func TestImageTargetRoutePermGuard(t *testing.T) {
	mounted := func(perms []string, method, path, perm string, h gin.HandlerFunc) *httptest.ResponseRecorder {
		guard := middleware.NewPermissionGuard(&permAuthSvc{perms: perms}, permStore{}, permCfg{}, logger.NewNop())
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.CtxClaims, testClaims(1)); c.Next() })
		// 路由模式不带 query 串（gin 的 path 不含 query）；请求 URL 保留 query。
		routePath := path
		if i := strings.IndexByte(path, '?'); i >= 0 {
			routePath = path[:i]
		}
		r.Handle(method, routePath, guard.Permission(perm), h)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(`{"repoTag":"app:1","targetDigest":"sha256:aaa"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	// POST 需 docker:manage。
	hdlWrite, _ := newImageTargetHandlerEnv(t, nil)
	if w := mounted([]string{"docker:manage"}, http.MethodPost, "/docker/image-targets", permission.PermDockerManage, hdlWrite.SetImageTarget); w.Code != http.StatusOK {
		t.Fatalf("有 docker:manage 的 POST 应 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := mounted(nil, http.MethodPost, "/docker/image-targets", permission.PermDockerManage, hdlWrite.SetImageTarget); w.Code != http.StatusForbidden {
		t.Fatalf("无 docker:manage 的 POST 应 403, got %d (%s)", w.Code, w.Body.String())
	}
	// DELETE 需 docker:manage。
	if w := mounted(nil, http.MethodDelete, "/docker/image-targets?repoTag=app%3A1", permission.PermDockerManage, hdlWrite.ClearImageTarget); w.Code != http.StatusForbidden {
		t.Fatalf("无 docker:manage 的 DELETE 应 403, got %d (%s)", w.Code, w.Body.String())
	}
	// GET 需 docker:list。
	hdlRead, _ := newImageTargetHandlerEnv(t, nil)
	if w := mounted([]string{"docker:list"}, http.MethodGet, "/docker/image-targets", permission.PermDockerList, hdlRead.ListImageTargets); w.Code != http.StatusOK {
		t.Fatalf("有 docker:list 的 GET 应 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := mounted(nil, http.MethodGet, "/docker/image-targets", permission.PermDockerList, hdlRead.ListImageTargets); w.Code != http.StatusForbidden {
		t.Fatalf("无 docker:list 的 GET 应 403, got %d (%s)", w.Code, w.Body.String())
	}
}