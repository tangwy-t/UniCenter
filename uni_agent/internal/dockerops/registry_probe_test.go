package dockerops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// ── registry 侧探测（完成判据的证据三）的测试 ────────────────────────────────
//
// 分两层：
//   - 纯函数（端点推导 / 挑战解析 / 基线对账 / insecure 判定）：表测试逐条钉形态；
//   - 探针本身：走**进程内的假 registry**（httptest，真 HTTP）—— URL 拼装、状态码
//     分支、Bearer/Basic 挑战、digest 取值都被真实覆盖，只有「谁在应答」是假的。

// fakeRegistry 是进程内的假 registry：只实现探测用到的 /v2 接口。
//
// 状态可以随时改（模拟「推送把它写进去了」）：测试因此在同一次运行里既能摆出
// 推送前的基线，也能摆出结算时的观测。
type fakeRegistry struct {
	srv *httptest.Server

	mu sync.Mutex
	// manifests 键 = "<repo>@<ref>"（ref 是 tag 或 digest）→ 该 manifest 的 digest。
	// 缺席 = 404（这个 ref 在 registry 上不存在）。
	manifests map[string]string
	// challenge 非空 = 先回 401 并带这个 WWW-Authenticate 头（认证面测试用）。
	challenge string
	// token 是非空 challenge 走到 token 端点时发放的 bearer token。
	token string
	// sawAuth 记录每次清单请求带来的 Authorization 头（断言凭据确实带上了）。
	sawAuth []string
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{manifests: map[string]string{}, token: "tok-1"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/token") {
		// Bearer 挑战的 token 端点（realm 指回本服务）：凭据走 Basic（匿名也可）。
		_ = json.NewEncoder(w).Encode(map[string]string{"token": f.token})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawAuth = append(f.sawAuth, r.Header.Get("Authorization"))
	if f.challenge != "" {
		// 授权面：认证不过一律 401 + **挑战头**（挑战头必须每次都带 —— 少了它
		// 客户端就无从知道该怎么认证，这正是 daemon/registry 的既有形态）。
		auth := r.Header.Get("Authorization")
		ok := auth == "Bearer "+f.token
		if !ok && !strings.HasPrefix(f.challenge, "Bearer") {
			ok = strings.HasPrefix(auth, "Basic ")
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", f.challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	i := strings.Index(r.URL.Path, "/manifests/")
	if !strings.HasPrefix(r.URL.Path, "/v2/") || i < 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	repo := strings.TrimPrefix(r.URL.Path[:i], "/v2/")
	ref := r.URL.Path[i+len("/manifests/"):]
	digest, ok := f.manifests[repo+"@"+ref]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"schemaVersion":2}`))
}

// set 摆一个 ref 的现状（digest 空串 = 删掉它）。
func (f *fakeRegistry) set(repo, ref, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if digest == "" {
		delete(f.manifests, repo+"@"+ref)
		return
	}
	f.manifests[repo+"@"+ref] = digest
}

// host 是假 registry 的 host:port（拼进 target 用）。
func (f *fakeRegistry) host() string {
	return strings.TrimPrefix(f.srv.URL, "http://")
}

// probeAPI 造一个只把策略设成「127.0.0.0/8 与 ::1 不安全」的替身：探测面的
// 信任规则与真机 daemon 的默认值同形（本机测试用的 registry 都在回环上）。
func probeAPI() *stubAPI {
	return &stubAPI{
		registryPolicy:    testLoopbackPolicy(),
		registryPolicySet: true,
	}
}

// testLoopbackPolicy 是 daemon 默认仓库策略的等价形态（回环网段 + ::1 明文）。
func testLoopbackPolicy() RegistryPolicy {
	return RegistryPolicy{
		InsecureCIDRs: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		},
		IndexSecure: map[string]bool{},
	}
}

// TestRegistryTargetOf：端点推导按 docker 的引用语法逐条钉住 —— 主机、仓库名
// （Docker Hub 的 library/ 补全）、引用（tag / digest / 空 = 仓库本身）。
func TestRegistryTargetOf(t *testing.T) {
	cases := []struct {
		target  string
		host    string
		repo    string
		ref     string
		wantErr bool
	}{
		{target: "harbor.example.com/app:1", host: "harbor.example.com", repo: "app", ref: "1"},
		{target: "127.0.0.1:5000/ns/app:v1.2", host: "127.0.0.1:5000", repo: "ns/app", ref: "v1.2"},
		{target: "app:1", host: "registry-1.docker.io", repo: "library/app", ref: "1"},
		{target: "library/nginx:latest", host: "registry-1.docker.io", repo: "library/nginx", ref: "latest"},
		{target: "docker.io/library/nginx", host: "registry-1.docker.io", repo: "library/nginx", ref: ""},
		{target: "index.docker.io/app:2", host: "registry-1.docker.io", repo: "library/app", ref: "2"},
		{target: "localhost:5000/app", host: "localhost:5000", repo: "app", ref: ""},
		{target: "harbor.example.com/app", host: "harbor.example.com", repo: "app", ref: ""},
		{
			target: "harbor.example.com/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			host:   "harbor.example.com", repo: "app",
			ref: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{target: "", wantErr: true},
		{target: "INVALID..REF", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			host, repo, ref, err := registryTargetOf(c.target)
			if c.wantErr {
				if err == nil {
					t.Fatalf("非法引用必须报错: %s", c.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("推导失败: %v", err)
			}
			if host != c.host || repo != c.repo || ref != c.ref {
				t.Fatalf("推导不符: host=%q repo=%q ref=%q（want %q %q %q）", host, repo, ref, c.host, c.repo, c.ref)
			}
		})
	}
}

// TestRegistryPolicyIsInsecure：insecure 判定逐条钉住 —— 具名条目优先、IP 落网段、
// **localhost 的域名解析特例**（解析到 127.0.0.1 → 与回环 IP 同档）、解析不了的
// 名字按安全处理（https）。
func TestRegistryPolicyIsInsecure(t *testing.T) {
	policy := RegistryPolicy{
		InsecureCIDRs: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
			netip.MustParsePrefix("10.10.0.0/16"),
		},
		IndexSecure: map[string]bool{
			"docker.io":            true,
			"insecure.example.com": false,
		},
	}
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"127.0.0.1", true},
		{"10.10.3.4:5000", true},
		{"10.11.3.4:5000", false},
		{"localhost:5000", true}, // 解析到 127.0.0.1 → 落回环网段
		{"[::1]:5000", true},
		{"insecure.example.com", true},
		{"docker.io", false},
		{"harbor.example.com", false},
		{"unresolvable.invalid", false}, // 解析不了 → 按安全处理
	}
	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			if got := policy.IsInsecure(c.host); got != c.want {
				t.Fatalf("IsInsecure(%q) = %v, want %v", c.host, got, c.want)
			}
		})
	}
}

// TestManifestBaselineLanded：基线对账的三条 —— 从无到有 = 落地；digest 变了 =
// 落地；基线缺席 / 现在没有 / digest 没变 = 开不了口。
func TestManifestBaselineLanded(t *testing.T) {
	base := manifestBaseline{
		"fresh":    {exists: false},
		"replaced": {exists: true, digest: "sha256:old"},
		"same":     {exists: true, digest: "sha256:same"},
	}
	cases := []struct {
		name string
		tag  string
		now  manifestState
		want bool
	}{
		{"从无到有", "fresh", manifestState{exists: true, digest: "sha256:new"}, true},
		{"digest 变了", "replaced", manifestState{exists: true, digest: "sha256:new"}, true},
		{"digest 没变（可能是上次留下的）", "same", manifestState{exists: true, digest: "sha256:same"}, false},
		{"现在没有", "replaced", manifestState{exists: false}, false},
		{"基线缺席（没探到过）", "unknown", manifestState{exists: true, digest: "sha256:x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := base.landed(c.tag, c.now); got != c.want {
				t.Fatalf("landed(%s) = %v, want %v", c.tag, got, c.want)
			}
		})
	}
}

// TestRegistryProberManifest：探针走真 HTTP（假 registry），三条状态分支 ——
// 200 取 Docker-Content-Digest、404 = 「没有这个 ref」、缺少摘要头时按清单体自算。
func TestRegistryProberManifest(t *testing.T) {
	f := newFakeRegistry(t)
	f.set("app", "1", "sha256:aaa")
	target := f.host() + "/app:1"
	prober, err := newRegistryProber(context.Background(), probeAPI(), target, nil)
	if err != nil {
		t.Fatalf("探针构造失败: %v", err)
	}
	if prober.baseURL != "http://"+f.host() {
		t.Fatalf("回环仓库必须走明文 HTTP（daemon 策略判定）: %s", prober.baseURL)
	}

	st, err := prober.manifest(context.Background(), "1")
	if err != nil || !st.exists || st.digest != "sha256:aaa" {
		t.Fatalf("有清单的 ref 必须回 200 + 摘要: %+v %v", st, err)
	}

	st, err = prober.manifest(context.Background(), "nope")
	if err != nil || st.exists {
		t.Fatalf("没有的 ref 必须折算成 exists=false（不是错误）: %+v %v", st, err)
	}

	// 摘要头缺席的形态：按清单体自算（registry 存的就是这份字节）。
	f.mu.Lock()
	delete(f.manifests, "app@nosum")
	f.mu.Unlock()
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
	})
	st, err = prober.manifest(context.Background(), "nosum")
	if err != nil || !st.exists || !strings.HasPrefix(st.digest, "sha256:") {
		t.Fatalf("缺摘要头时必须按清单体自算: %+v %v", st, err)
	}
}

// TestRegistryProberBearerChallenge：401 + Bearer 挑战 → 取 token（realm 指回假
// registry 的 /token）→ 带 Bearer 重试成功。这条是 Docker Hub / Harbor 的常态形态，
// 在无外网的环境里用假 registry 走通同一段代码。
func TestRegistryProberBearerChallenge(t *testing.T) {
	f := newFakeRegistry(t)
	f.set("app", "1", "sha256:aaa")
	f.mu.Lock()
	f.challenge = `Bearer realm="` + f.srv.URL + `/token",service="fake-registry"`
	f.mu.Unlock()

	auth := &ImageAuth{Registry: f.host(), Username: "robot", Password: "s3cret"}
	prober, err := newRegistryProber(context.Background(), probeAPI(), f.host()+"/app:1", auth)
	if err != nil {
		t.Fatalf("探针构造失败: %v", err)
	}
	st, err := prober.manifest(context.Background(), "1")
	if err != nil || !st.exists || st.digest != "sha256:aaa" {
		t.Fatalf("Bearer 挑战必须被走通: %+v %v", st, err)
	}
	// 凭据发给了 token 端点（清单请求带的是换取来的 token）。
	if prober.token != "tok-1" {
		t.Fatalf("token 必须被缓存下来: %q", prober.token)
	}
}

// 反向守卫：挑战在场但**没有凭据**且 token 端点也不放行时，探测必须失败
// （判据开不了口），绝不能绕过认证把 401 当「没有这个 ref」。
func TestRegistryProberAuthFailureIsError(t *testing.T) {
	f := newFakeRegistry(t)
	f.set("app", "1", "sha256:aaa")
	f.mu.Lock()
	// Basic 挑战 + 空凭据：重试仍 401。
	f.challenge = `Basic realm="fake"`
	f.mu.Unlock()
	prober, err := newRegistryProber(context.Background(), probeAPI(), f.host()+"/app:1", nil)
	if err != nil {
		t.Fatalf("探针构造失败: %v", err)
	}
	if _, err := prober.manifest(context.Background(), "1"); err == nil {
		t.Fatal("401 必须折成错误（不是「不存在」）：把未授权当事实会编出错误的完成判据")
	}
}

// TestRegistryProberCredentialScoping：凭据只在**同源**时带上 —— 给 A 仓库的
// 用户名密码绝不能发给 B 仓库（本域最不该发生的泄漏）。
func TestRegistryProberCredentialScoping(t *testing.T) {
	f := newFakeRegistry(t)
	ctx := context.Background()

	same, err := newRegistryProber(ctx, probeAPI(), f.host()+"/app:1",
		&ImageAuth{Registry: f.host(), Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if same.username != "u" {
		t.Fatal("仓库键同源时必须带上凭据（否则私库探不动）")
	}

	other, err := newRegistryProber(ctx, probeAPI(), f.host()+"/app:1",
		&ImageAuth{Registry: "harbor.example.com", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if other.username != "" {
		t.Fatal("仓库键不同源时必须裸探：凭据只发给它自己的仓库")
	}

	// Docker Hub 的三个别名算同一台（凭据不会因为写法不同而丢掉）。
	hub, err := newRegistryProber(ctx, probeAPI(), "app:1",
		&ImageAuth{Registry: "docker.io", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if hub.username != "u" {
		t.Fatal("docker.io 与 registry-1.docker.io 是同一台：凭据必须带上")
	}
}

// TestSameRegistry：仓库地址归一（别名 / 大小写 / 尾斜杠）。
func TestSameRegistry(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"docker.io", "registry-1.docker.io", true},
		{"index.docker.io", "docker.io", true},
		{"Harbor.Example.com", "harbor.example.com", true},
		{"harbor.example.com/", "harbor.example.com", true},
		{"harbor.example.com:8443", "harbor.example.com", false},
		{"", "harbor.example.com", false},
	}
	for _, c := range cases {
		if got := sameRegistry(c.a, c.b); got != c.want {
			t.Fatalf("sameRegistry(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestParseAuthParams：挑战头参数解析（引号内的逗号不切、键大小写无关、缺失即缺席）。
func TestParseAuthParams(t *testing.T) {
	got := parseAuthParams(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/app:pull"`)
	if got["realm"] != "https://auth.docker.io/token" || got["service"] != "registry.docker.io" {
		t.Fatalf("挑战参数解析不符: %+v", got)
	}
	if kind := challengeKind(`bearer realm="x"`); kind != "bearer" {
		t.Fatalf("方案名必须小写归一: %q", kind)
	}
	if got := parseAuthParams(`Basic realm="fake"`); got["realm"] != "fake" {
		t.Fatalf("Basic 挑战也要能解析: %+v", got)
	}
}

// TestLocalTagsOf：整仓推送的「应推集合」——按仓库归一拍（docker.io 的 library/
// 前缀、别名、大小写），dangling（<none>）与别的仓库的 tag 不进集合。
func TestLocalTagsOf(t *testing.T) {
	images := []ImageInfo{
		{RepoTags: []string{"app:1", "app:2", "docker.io/library/app:3"}},
		{RepoTags: []string{"harbor.example.com:8443/ns/app:v1"}},
		{RepoTags: []string{"other:1"}},
		{RepoTags: []string{"<none>:<none>"}},
	}
	got := localTagsOf(images, "app")
	want := []string{"1", "2", "3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("app 的本地 tag 集合不符: %v want %v", got, want)
	}
	got = localTagsOf(images, "library/app")
	if strings.Join(got, ",") != "1,2,3" {
		t.Fatalf("library/app 与 app 是同一仓库: %v", got)
	}
	got = localTagsOf(images, "harbor.example.com:8443/ns/app")
	if strings.Join(got, ",") != "v1" {
		t.Fatalf("带端口仓库的 tag 集合不符: %v", got)
	}
	if got := localTagsOf(images, "nothing/here"); len(got) != 0 {
		t.Fatalf("没有本地 tag 的仓库必须回空集: %v", got)
	}
}
