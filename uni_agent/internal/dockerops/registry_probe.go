package dockerops

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
)

// ── registry 侧探测（完成判据的**证据三**）─────────────────────────────────
//
// 要回答的问题只有一个：「这场推送要落的 ref，此刻在 registry 上是什么」。
// 推送完成时 agent 本机**可能没有任何位移**（containerd 存储的 RepoDigests 是本地
// tag 合成的，推送不改变它；classic 存储重推同一份内容也不再落笔），于是判据必须
// 有一半站在 registry 那侧：推送前记下每个待推 tag 的 manifest 现状，异常收尾时
// 再探一次 —— digest 从无到有 / 变了，就是「这场推送的清单已经交进 registry」这件
// 事本身，**与 daemon 用哪种镜像存储无关**（classic 与 containerd 通吃）。
//
// 三条口径，逐条与推送路径对齐（探测必须与推送同一套信任，见 RegistryPolicy）：
//   - 信任规则来自 daemon 的仓库配置（insecure-registries / 自签 CA / localhost 特例）；
//   - 端点由 target 按 docker 的引用语法推导（host/仓库名/引用三段，Docker Hub 的
//     默认 host 与 library/ 前缀补全都在 ParseNormalizedNamed 里）；
//   - 凭据只在**仓库键同源**时带上（推送指令带来的那把瞬时凭据），并支持
//     Bearer token 挑战（Docker Hub / Harbor 的常态形态）与 Basic 挑战两条路。
//
// 明确的能力边界：探测只是**补判**，失败（连不上、无权限、事件读不到）一律返回错误、
// 由调用方当作「这条证据开不了口」——绝不猜测、绝不降级成明文或跳过校验。

const (
	// registryProbeTimeout 是单次探测请求（含 token 交换）的时限。取 10s：
	// 探测发生在推送前后各一次，局域网/公网仓库都在这个量级内完成；超时就当
	// 证据不可用（保守方向），不让一次卡死的连接把指令的执行时限吃光。
	registryProbeTimeout = 10 * time.Second
	// registryLookupTimeout 是仓库域名解析（insecure 网段判定用）的时限。
	registryLookupTimeout = 3 * time.Second
	// registryMaxManifestBytes 是读清单体的上限：清单是 KB 级文档，1MB 是
	// 防御性上限（恶意/异常 registry 不该让 agent 把内存交出去）；超限即判探测失败。
	registryMaxManifestBytes = 1 << 20
	// dockerHubAPIHost 是 Docker Hub 的**registry API** 主机名：引用里的
	// "docker.io" 只是索引名，实际端点在这一台（daemon 侧的 DefaultRegistryHost 同值）。
	dockerHubAPIHost = "registry-1.docker.io"
)

// manifestState 是 registry 上一个引用（tag）的一次观测。
type manifestState struct {
	// exists 报告这个 tag 在 registry 上有没有清单。
	exists bool
	// digest 是 registry 报出的清单摘要（Docker-Content-Digest；缺失时按清单体自算）。
	// exists=false 时为空。
	digest string
}

// manifestBaseline 是「待推 tag → 推送前的观测」的对照表（证据三的基线）。
//
// 刻意做成 map 而不是切片：结算按 tag 逐个对账，缺项（基线没取到）即该 tag 的
// 证据开不了口 —— 与 pull/push 既有基线的「查不了就不判」同一条纪律。
type manifestBaseline map[string]manifestState

// landed 报告 tag 在结算观测下**是否已落 registry**：基线是「没有」而现在有了，
// 或基线与现在的 digest 不同。基线缺席（该 tag 当时没探到）→ 开不了口。
//
// 为什么是「变化」而不是「现在有」：registry 上本来就有的 tag（上次推送留下的）
// 在取消之后依然在，单看「有」会把「重推被截止」也说成成功（反向的不诚实）。
func (b manifestBaseline) landed(tag string, now manifestState) bool {
	base, ok := b[tag]
	if !ok {
		return false
	}
	if !now.exists {
		return false
	}
	return !base.exists || base.digest != now.digest
}

// registryProber 是**已定型**的探测会话：端点、信任规则、凭据都算好了，
// 之后每次 manifest 调用只做一次 HTTP。
type registryProber struct {
	client *http.Client
	// baseURL 形如 https://harbor.example.com:8443 或 http://127.0.0.1:5000。
	baseURL string
	// repo 是仓库名（docker 惯例补全后，如 library/nginx）。
	repo string
	// username / password 是随指令来的瞬时凭据；空 = 匿名探测。
	username string
	password string

	mu    sync.Mutex
	token string // Bearer token 缓存（一场指令内复用；过期后 401 会再换一次）
}

// registryTargetOf 把协议里的 target 折成探测需要的三段：registry API 主机、
// 仓库名、引用（tag 或 digest；空 = 这个 target 是**仓库**而不是某个 tag）。
//
// 用 distribution/reference 的 ParseNormalizedNamed 而不是自己切字符串：
// 「第一段带 . 或 : 或叫 localhost 才是 host」「docker.io 的官方镜像补 library/」
// 这些规则是 docker 的既有语法，自己实现一遍就是把两套语法放在同一个系统里。
func registryTargetOf(target string) (host, repo, ref string, err error) {
	named, err := reference.ParseNormalizedNamed(target)
	if err != nil {
		return "", "", "", fmt.Errorf("解析镜像引用失败: %w", err)
	}
	host = reference.Domain(named)
	if host == "docker.io" || host == "index.docker.io" {
		// 索引名 → API 主机名（Docker Hub 的引用语法与实际端点不是同一台）。
		host = dockerHubAPIHost
	}
	repo = reference.Path(named)
	switch t := named.(type) {
	case reference.Tagged:
		ref = t.Tag()
	case reference.Canonical:
		ref = t.Digest().String()
	}
	return host, repo, ref, nil
}

// sameRegistry 报告两个仓库地址是不是同一台（凭据键与 target 推导出的 host 对齐用）。
//
// 归一化：大小写无关、尾部斜杠无关，docker.io / index.docker.io / registry-1.docker.io
// 三个别名同一台（同一台机器的三种写法不能因为写法不同就丢掉凭据，也不能反过来
// 把凭据发给别的 host）。
func sameRegistry(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(s, "/")))
		switch s {
		case "docker.io", "index.docker.io", dockerHubAPIHost:
			return dockerHubAPIHost
		}
		return s
	}
	return a != "" && b != "" && norm(a) == norm(b)
}

// newRegistryProber 构造探测会话：端点推导 + daemon 策略定信任 + 凭据按同源判定带上。
//
// api 只被用来读一次 daemon 的仓库策略（RegistryPolicy）；读不到即整体失败 ——
// 探测宁可不可用，也不自己拍一套信任规则（见 RegistryPolicy 的说明）。
func newRegistryProber(ctx context.Context, api DockerAPI, target string, auth *ImageAuth) (*registryProber, error) {
	host, repo, _, err := registryTargetOf(target)
	if err != nil {
		return nil, err
	}
	policy, err := api.RegistryPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 daemon 仓库策略失败: %w", err)
	}
	scheme := "https"
	if policy.IsInsecure(host) {
		scheme = "http"
	}
	client, err := registryHTTPClient(host, scheme)
	if err != nil {
		return nil, err
	}
	p := &registryProber{client: client, baseURL: scheme + "://" + host, repo: repo}
	// 凭据只发给**它自己的仓库**：target 推导出的 host 与凭据键不同源时不带
	// （把 A 仓库的密码发给 B 仓库是本域最不该发生的泄漏，宁可不探）。
	if auth != nil && sameRegistry(auth.Registry, host) {
		p.username, p.password = auth.Username, auth.Password
	}
	return p, nil
}

// registryHTTPClient 造探测客户端：https 走系统信任库（外加 daemon 自己的
// certs.d 自签 CA），http 只在 daemon 明确判为 insecure 时出现（由调用方决定）。
//
// 自签 CA 的来源与 daemon 一致：/etc/docker/certs.d/<host>/ca.crt（moby 的
// registry.NewTLSConfig 读的就是这个目录）。agent 与 daemon 同机时这份文件就是
// 「daemon 凭什么信任这台自签仓库」的答案；读不到就只用系统信任库（自签仓库的
// 探测会失败 —— 判据开不了口，而不是降级成不校验）。
func registryHTTPClient(host, scheme string) (*http.Client, error) {
	if scheme != "https" {
		return &http.Client{Transport: &http.Transport{}}, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// 系统信任库读不出来（极简容器镜像）：起一个空的 —— 下面的 certs.d
		// 文件仍能把它补齐；补不齐的仓库探测自然失败（保守方向）。
		pool = x509.NewCertPool()
	}
	if ca, err := os.ReadFile(filepath.Join("/etc/docker/certs.d", host, "ca.crt")); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}, nil
}

// manifest 探测一个引用（tag 或 digest）在 registry 上的现状。
//
// 形态：GET /v2/<repo>/manifests/<ref>（带 Accept 四类清单媒体类型）。
// 用 GET 而不是 HEAD：两者在标准 registry 上等价（registry:2 实测 HEAD 也回
// Docker-Content-Digest），但 GET 对「HEAD 不回摘要」的实现更稳；体是 KB 级、
// 且 digest 可以由体自算（见下）。
//
// 401 处理两条路（凭据在手上时都走得通）：Bearer 挑战 → 取 token 重试一次；
// Basic 挑战 → 带 Basic 重试一次。都失败即报错（证据不可用）。
func (p *registryProber) manifest(ctx context.Context, ref string) (manifestState, error) {
	st, challenge, err := p.tryManifest(ctx, ref, p.authHeader())
	if err == nil {
		return st, nil
	}
	if challenge == "" {
		return manifestState{}, err
	}
	switch challengeKind(challenge) {
	case "bearer":
		token, terr := p.fetchToken(ctx, challenge)
		if terr != nil {
			return manifestState{}, terr
		}
		st, _, err = p.tryManifest(ctx, ref, "Bearer "+token)
		return st, err
	case "basic":
		if p.username == "" {
			return manifestState{}, fmt.Errorf("registry 要求认证但没有凭据: %w", err)
		}
		st, _, err = p.tryManifest(ctx, ref, p.authHeader())
		return st, err
	}
	return manifestState{}, err
}

// authHeader 返回当前的 Authorization 头（缓存 token 优先，其次 Basic，最后空 = 匿名）。
func (p *registryProber) authHeader() string {
	p.mu.Lock()
	token := p.token
	p.mu.Unlock()
	if token != "" {
		return "Bearer " + token
	}
	if p.username != "" {
		req := &http.Request{Header: http.Header{}}
		req.SetBasicAuth(p.username, p.password)
		return req.Header.Get("Authorization")
	}
	return ""
}

// tryManifest 发一次清单请求。返回 (观测, 挑战头, 错误)：
//   - 200：正常观测（digest 取 Docker-Content-Digest，缺失按体自算）；
//   - 404：这不是错误 —— 「没有这个引用」是探测要回答的事实之一；
//   - 401：返回挑战头（不带错误），由调用方按挑战形态走 token / basic 重试；
//   - 其它状态码与传输失败：错误（证据不可用）。
func (p *registryProber) tryManifest(ctx context.Context, ref, authHeader string) (manifestState, string, error) {
	pctx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet,
		p.baseURL+"/v2/"+p.repo+"/manifests/"+url.PathEscape(ref), nil)
	if err != nil {
		return manifestState{}, "", err
	}
	// 四类清单媒体类型一次带上（docker 客户端同款）：漏了某个类型会换来
	// 406/404 —— 探测就开不了口。
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return manifestState{}, "", fmt.Errorf("探测 registry 失败: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, registryMaxManifestBytes+1))
		if err != nil {
			return manifestState{}, "", fmt.Errorf("读取清单失败: %w", err)
		}
		if len(body) > registryMaxManifestBytes {
			return manifestState{}, "", fmt.Errorf("清单超过 %d 字节上限", registryMaxManifestBytes)
		}
		digest := resp.Header.Get("Docker-Content-Digest")
		if digest == "" {
			// registry 没给摘要头时按清单体自算：registry 存的就是这份字节，
			// 「按 digest 寻址」的语义就是这么定义的。
			sum := sha256.Sum256(body)
			digest = "sha256:" + hex.EncodeToString(sum[:])
		}
		return manifestState{exists: true, digest: digest}, "", nil
	case resp.StatusCode == http.StatusNotFound:
		return manifestState{exists: false}, "", nil
	case resp.StatusCode == http.StatusUnauthorized:
		return manifestState{}, resp.Header.Get("WWW-Authenticate"), fmt.Errorf("registry 要求认证（HTTP 401）")
	default:
		return manifestState{}, "", fmt.Errorf("探测 registry 返回 HTTP %d", resp.StatusCode)
	}
}

// challengeKind 判一个 WWW-Authenticate 头的认证方案（小写首词）。
func challengeKind(challenge string) string {
	kind := strings.TrimSpace(challenge)
	if i := strings.IndexByte(kind, ' '); i >= 0 {
		kind = kind[:i]
	}
	return strings.ToLower(kind)
}

// fetchToken 走 Bearer 挑战的 token 交换（Docker Hub / Harbor / GCR 的常态形态）：
// GET <realm>?service=<service>&scope=repository:<repo>:pull，凭据走 Basic。
// realm/service 从挑战头里解析；scope 固定为**只读**（判据只需要看清单）。
func (p *registryProber) fetchToken(ctx context.Context, challenge string) (string, error) {
	params := parseAuthParams(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry 的认证挑战缺少 realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("认证挑战的 realm 不可用: %w", err)
	}
	q := u.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	q.Set("scope", "repository:"+p.repo+":pull")
	u.RawQuery = q.Encode()

	tctx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if p.username != "" {
		req.SetBasicAuth(p.username, p.password)
	}
	// token 端点可能不在同一台（Docker Hub 的 auth.docker.io）：用系统信任库 +
	// daemon certs.d 里该 realm 主机的 CA（同一份信任来源，见 registryHTTPClient）。
	client, err := registryHTTPClient(u.Host, u.Scheme)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("取 registry token 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("取 registry token 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, registryMaxManifestBytes))
	if err != nil {
		return "", err
	}
	// 响应形如 {"token":"…"}（标准）或 {"access_token":"…"}（老的 OAuth 形态）。
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("token 响应不可解析: %w", err)
	}
	token := tok.Token
	if token == "" {
		token = tok.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("token 响应缺少 token 字段")
	}
	p.mu.Lock()
	p.token = token
	p.mu.Unlock()
	return token, nil
}

// parseAuthParams 解析 WWW-Authenticate 的参数表（`Bearer realm="…",service="…"`）。
//
// 只认逗号分隔的 key="value" 形态（RFC 7235 challenge 的公共子集，registry 与
// 主流实现都发这一种）；解析不出来就是空表 —— 调用方据缺失的 realm 报错，
// 不猜、不拼。
func parseAuthParams(challenge string) map[string]string {
	out := map[string]string{}
	rest := challenge
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		rest = rest[i+1:]
	}
	for _, part := range splitQuoted(rest) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return out
}

// splitQuoted 按逗号切分，但**不切引号内的逗号**（realm 里可能带逗号：
// 某些实现会带查询串）。引号配对按「前面没有转义」算 —— 认证头里没有转义逗号的
// 现实形态，这里的规则简单到可以逐字复述。
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// localTagsOf 从本机镜像清单里挑出**与 target 同一仓库的全部本地 tag**
// （不带 tag 的推送「应推集合」的来源，见 build_push.go 的 pushTagSet）。
//
// 匹配用 docker 的引用归一化（docker.io 的 library/ 前缀、大小写、index.docker.io
// 别名）而不是字符串前缀比较 —— "app"、"docker.io/library/app"、"library/app"
// 是同一个仓库的三种写法，字符串比较会把它们割成三个仓库。
func localTagsOf(images []ImageInfo, target string) []string {
	want, err := reference.ParseNormalizedNamed(target)
	if err != nil {
		return nil
	}
	wantHost, wantPath := normalizedHost(want), reference.Path(want)
	var out []string
	for _, img := range images {
		for _, rt := range img.RepoTags {
			named, err := reference.ParseNormalizedNamed(rt)
			if err != nil {
				continue
			}
			if normalizedHost(named) != wantHost || reference.Path(named) != wantPath {
				continue
			}
			if tagged, ok := named.(reference.Tagged); ok {
				out = append(out, tagged.Tag())
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// normalizedHost 把引用里的索引名归一（index.docker.io 与 docker.io 同一台）。
func normalizedHost(named reference.Named) string {
	host := reference.Domain(named)
	if host == "index.docker.io" {
		return "docker.io"
	}
	return host
}
