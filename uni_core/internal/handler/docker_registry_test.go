package handler

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// memRegistryRepo 是 service.RegistryCredentialRepo 的内存替身：
// 保留**落库形态**（含密文盒），断言「库里没有明文」与「掩码只出现在响应里」。
type memRegistryRepo struct {
	rows map[string]*entity.DockerRegistryCredential
}

func newMemRegistryRepo() *memRegistryRepo { return &memRegistryRepo{rows: map[string]*entity.DockerRegistryCredential{}} }

func (r *memRegistryRepo) FindByRegistry(_ context.Context, registry string) (*entity.DockerRegistryCredential, error) {
	row, ok := r.rows[registry]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (r *memRegistryRepo) Create(_ context.Context, row *entity.DockerRegistryCredential) error {
	cp := *row
	r.rows[row.Registry] = &cp
	return nil
}

func (r *memRegistryRepo) Save(_ context.Context, row *entity.DockerRegistryCredential) error {
	cur, ok := r.rows[row.Registry]
	if !ok {
		return gorm.ErrRecordNotFound
	}
	cur.Username, cur.PasswordEnc, cur.Remark = row.Username, row.PasswordEnc, row.Remark
	return nil
}

func (r *memRegistryRepo) Delete(_ context.Context, registry string) error {
	if _, ok := r.rows[registry]; !ok {
		return gorm.ErrRecordNotFound
	}
	delete(r.rows, registry)
	return nil
}

func (r *memRegistryRepo) List(_ context.Context) ([]entity.DockerRegistryCredential, error) {
	out := make([]entity.DockerRegistryCredential, 0, len(r.rows))
	for _, v := range r.rows {
		out = append(out, entity.DockerRegistryCredential{
			Registry: v.Registry, Username: v.Username, PasswordEnc: v.PasswordEnc, Remark: v.Remark,
		})
	}
	return out, nil
}

// registryHandlerKeyGetter 是主密钥替身：hex 测试密钥（生产走 sys 配置键，
// 见 service.RegistryMasterKeyConfigKey 的说明）。
type registryHandlerKeyGetter struct{ val string }

func (f *registryHandlerKeyGetter) GetString(_ context.Context, key, defaultVal string) string {
	if key == service.RegistryMasterKeyConfigKey && f.val != "" {
		return f.val
	}
	return defaultVal
}

// registryHandlerTestKey 是合法的 32 字节测试主密钥（hex 形式）。
func registryHandlerTestKey() string { return hex.EncodeToString([]byte(strings.Repeat("hk", 16))) }

func newRegistryHandler() (*DockerRegistryHandler, *memRegistryRepo) {
	repo := newMemRegistryRepo()
	svc := service.NewDockerRegistryService(repo, &registryHandlerKeyGetter{val: registryHandlerTestKey()}, nil)
	return NewDockerRegistryHandler(svc), repo
}

// newRegistryContext 构造一条 gin 测试上下文（照 docker_test.go 的 newCmdContext 模式）。
// registry 非空时写入路径参数（裸引擎没有路由匹配,params 需手工注入）。
func newRegistryContext(method, path, body string, registry ...string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	if len(registry) > 0 && registry[0] != "" {
		c.Params = gin.Params{{Key: "registry", Value: registry[0]}}
	}
	return w, c
}

// TestRegistryHandlerCRUD：创建（409 冲突）→ 更新（404/200、重输密码）→
// 删除（200/404）→ 列表掩码 —— 全部断言 HTTP 状态与信封行为。
func TestRegistryHandlerCRUD(t *testing.T) {
	h, repo := newRegistryHandler()
	const secret = "handler-secret-4096"

	w, c := newRegistryContext(http.MethodPost, "/docker/registries",
		`{"registry":"harbor.example.com","username":"robot$ci","password":"`+secret+`","remark":"r"}`)
	h.Create(c)
	if w.Code != http.StatusOK {
		t.Fatalf("创建应 200,实际 %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("创建响应不得包含密码字面量")
	}
	if !strings.Contains(w.Body.String(), "****") {
		t.Fatalf("创建响应密码必须是掩码: %s", w.Body.String())
	}
	// 库里必须是密文,且不含明文。
	stored := repo.rows["harbor.example.com"]
	if stored.PasswordEnc == secret || strings.Contains(stored.PasswordEnc, secret) {
		t.Fatal("落库形态不得是明文")
	}

	// 同仓库重复创建 → 409（信封照抄现有端点：HTTP 状态取 apperror 语义）。
	w2, c2 := newRegistryContext(http.MethodPost, "/docker/registries",
		`{"registry":"Harbor.Example.com","username":"u","password":"p"}`)
	h.Create(c2)
	if w2.Code != http.StatusConflict || !strings.Contains(w2.Body.String(), "已有凭据") {
		t.Fatalf("重复创建应给 409 冲突结论句, code=%d body=%s", w2.Code, w2.Body.String())
	}

	// 更新不存在 → 404 结论句。
	w3, c3 := newRegistryContext(http.MethodPut, "/docker/registries",
		`{"registry":"no.example.com","username":"u","password":"p"}`)
	h.Update(c3)
	if w3.Code != http.StatusNotFound || !strings.Contains(w3.Body.String(), "没有凭据记录") {
		t.Fatalf("更新不存在应给 404 结论句, code=%d body=%s", w3.Code, w3.Body.String())
	}

	// 更新：重输密码后生效（列表可见新用户名与备注）。
	w4, c4 := newRegistryContext(http.MethodPut, "/docker/registries",
		`{"registry":"harbor.example.com","username":"robot$ci-v2","password":"new-pass-9","remark":"备注二"}`)
	h.Update(c4)
	if w4.Code != http.StatusOK || strings.Contains(w4.Body.String(), "new-pass-9") {
		t.Fatalf("更新应 200 且不含新密码, code=%d body=%s", w4.Code, w4.Body.String())
	}

	w5, c5 := newRegistryContext(http.MethodGet, "/docker/registries", "")
	h.List(c5)
	if w5.Code != http.StatusOK {
		t.Fatalf("列表应 200,实际 %d", w5.Code)
	}
	if !strings.Contains(w5.Body.String(), `"username":"robot$ci-v2"`) {
		t.Fatalf("列表应看到更新后的用户名: %s", w5.Body.String())
	}
	if !strings.Contains(w5.Body.String(), `"password":"****"`) {
		t.Fatalf("列表密码必须是常量掩码 ****: %s", w5.Body.String())
	}
	if strings.Contains(w5.Body.String(), "new-pass-9") || strings.Contains(w5.Body.String(), secret) {
		t.Fatal("列表响应不得包含任何密码字面量")
	}

	// 删除 → 200；再删 → 404。删除后库里没有这一行（删除即失效）。
	w6, c6 := newRegistryContext(http.MethodDelete, "/docker/registries/harbor.example.com", "", "harbor.example.com")
	h.Delete(c6)
	if w6.Code != http.StatusOK {
		t.Fatalf("删除应 200,实际 %d body=%s", w6.Code, w6.Body.String())
	}
	if _, ok := repo.rows["harbor.example.com"]; ok {
		t.Fatal("删除后替身库里不得还有这一行")
	}
	w7, c7 := newRegistryContext(http.MethodDelete, "/docker/registries/harbor.example.com", "", "harbor.example.com")
	h.Delete(c7)
	if w7.Code != http.StatusNotFound || !strings.Contains(w7.Body.String(), "没有凭据记录") {
		t.Fatalf("重复删除应给 404 结论句, code=%d body=%s", w7.Code, w7.Body.String())
	}
}

// TestRegistryHandlerBadRequests：绑定失败/非法仓库地址/空路径参数的 400 语义。
func TestRegistryHandlerBadRequests(t *testing.T) {
	h, _ := newRegistryHandler()
	w, c := newRegistryContext(http.MethodPost, "/docker/registries", `{"registry":"x"}`)
	h.Create(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "请求参数不合法") {
		t.Fatalf("缺字段应 400 语义: code=%d body=%s", w.Code, w.Body.String())
	}

	w2, c2 := newRegistryContext(http.MethodPost, "/docker/registries",
		`{"registry":"https://harbor.example.com","username":"u","password":"p"}`)
	h.Create(c2)
	if !strings.Contains(w2.Body.String(), "仓库地址格式不合法") {
		t.Fatalf("非法仓库地址应给结论句: %s", w2.Body.String())
	}

	w3, c3 := newRegistryContext(http.MethodDelete, "/docker/registries/", "")
	h.Delete(c3)
	if w3.Code != http.StatusBadRequest || !strings.Contains(w3.Body.String(), "请求参数不合法") {
		t.Fatalf("空路径参数应 400 语义: code=%d body=%s", w3.Code, w3.Body.String())
	}
}

// TestRegistryHandlerPasswordNeverLeaks：满链路密码零泄漏断言（4c 的核心纪律）——
// 创建/更新/列表的响应体可被完整 grep,任何路径都不该携带密码字面量。
func TestRegistryHandlerPasswordNeverLeaks(t *testing.T) {
	h, _ := newRegistryHandler()
	const secret = "zero-leak-password-!!"
	full := func(method, path, body string) string {
		w, c := newRegistryContext(method, path, body)
		switch method {
		case http.MethodPost:
			h.Create(c)
		case http.MethodPut:
			h.Update(c)
		case http.MethodGet:
			h.List(c)
		}
		return w.Body.String()
	}
	created := full(http.MethodPost, "/docker/registries",
		`{"registry":"h.example.com","username":"u","password":"`+secret+`"}`)
	updated := full(http.MethodPut, "/docker/registries",
		`{"registry":"h.example.com","username":"u","password":"`+secret+`"}`)
	listed := full(http.MethodGet, "/docker/registries", "")
	for name, body := range map[string]string{"create": created, "update": updated, "list": listed} {
		if strings.Contains(body, secret) {
			t.Fatalf("%s 响应泄漏密码字面量: %s", name, body)
		}
		if strings.Contains(body, "zero-leak") {
			t.Fatalf("%s 响应泄漏密码片段: %s", name, body)
		}
	}
}