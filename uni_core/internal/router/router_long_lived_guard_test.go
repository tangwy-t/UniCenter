package router

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 长活端点必须挂 middleware.LongLived 的**源码级守卫**（P0）。
//
// 为什么需要它：http.Server 的 WriteTimeout 是**整条响应**的绝对截止（net/http 在
// 请求头解析完的那一刻设成 now+WriteTimeout），任何 >30s 的响应都必然在 30.0s 被
// 写失败掐断 —— 对 docker 的流式端点，本波「断流 = best-effort cancel」还会把它
// 升级成「取消操作本身」（QA 终审实测：>30s 的镜像拉取 100% 在 30.0s±0.3s 断路、
// 镜像不落地）。接管写截止是**逐路由挂载**的（路由表是「哪些端点长活」的事实源），
// 而漏挂的失败形态是「这个功能 30s 后莫名失败」—— handler 层测试挂在裸引擎上
// （没有 http.Server、没有 WriteTimeout）**照样全绿**，与
// TestEventsStreamRouteMountedWithPerm 同款理由：只有源码级断言拦得住。
//
// 两条判定：
//  1. **类规则**：处理器名以 Stream 结尾的注册行必须挂 —— 新加一条流式端点漏挂，
//     这里就红。唯一豁免是终端 ExecStream：它升级成 WebSocket 后由 gorilla 在
//     hijack 时清掉全部 deadline（见 middleware.LongLived 的说明），不需要也不该挂。
//  2. **清单**：非流式的长活传输（大文件流式下载 / 大请求体上传 / 慢采集）逐条列出
//     —— 它们是同一根因的另一批受害者，名字上认不出来（Download/Upload/AdaptPprof），
//     只能显式枚举；清单为空说明端点被改名/删除了，守卫要同步更新。
func TestLongLivedRoutesCarryDeadlineMiddleware(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	// 长活清单（处理器名后缀 → 为什么它长活）。
	listed := map[string]string{
		"Docker.Hdl.EventsStream)":       "事件聚合流（总览页活动流，连接常驻）",
		"Docker.Hdl.LogStream)":          "日志 Follow（container:logs / compose:logs）",
		"Docker.Hdl.StatsStream)":        "容器 stats 实时流（曲线一直画）",
		"Docker.Hdl.PullStream)":         "拉取进度流（P0 实测受害端点）",
		"Docker.Hdl.BuildStream)":        "构建进度流（RUN 一步可能几分钟无输出）",
		"Docker.Hdl.PushStream)":         "推送进度流（层大时 >30s）",
		"Docker.Hdl.BuildContextUpload)": "构建上下文上传（≤512MB 请求体 + 转完才回响应）",
		"File.FileHdl.Download)":         "文件下载（ServeContent 流式，大文件 >30s）",
		"File.FileHdl.Preview)":          "文件预览（ServeContent 流式，支持 Range 拖动）",
		"File.FileHdl.Upload)":           "文件上传（multipart 请求体，上限可配置）",
		"ReleaseHdl.Upload)":             "agent 程序包上传（默认上限 200MB，弱网下 >30s 是常态）",
		"ReleaseHdl.Download)":           "agent 发布物下载（几十 MB 二进制走内网/弱网）",
		"AdaptPprof())":                  "pprof profile/trace 采集（?seconds=N，N≥30 时写必超时）",
	}

	found := map[string]int{}
	var missing []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, ".GET(") && !strings.Contains(line, ".POST(") {
			continue
		}
		// 类规则：任何 *Stream 处理器（终端 WS 除外，它在升级时 hijack 并由
		// gorilla 清 deadline）都要挂。
		if h := handlerName(line); strings.HasSuffix(h, "Stream") && shortName(h) != "ExecStream" {
			if !strings.Contains(line, "longLived") {
				missing = append(missing, strings.TrimSpace(line))
			}
		}
		for suffix := range listed {
			if !strings.Contains(line, suffix) {
				continue
			}
			found[suffix]++
			if !strings.Contains(line, "longLived") {
				missing = append(missing, strings.TrimSpace(line))
			}
		}
	}

	for suffix, why := range listed {
		if found[suffix] == 0 {
			t.Errorf("路由表里找不到长活端点 %s（%s）—— 端点被改名/删除了？守卫清单要同步",
				suffix, why)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("以下长活路由漏挂 middleware.LongLived —— >30s 的响应必被 WriteTimeout 截断：\n%s",
			strings.Join(missing, "\n"))
	}
}

// handlerName 从一条 gin 注册行里取处理器标识（最后一个逗号之后的表达式，
// 例如 `deps.Docker.Hdl.LogStream`）；取不到返回空串。
func handlerName(line string) string {
	i := strings.LastIndex(line, ",")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[i+1:]), ")"))
}

// shortName 取处理器标识的最后一段（`deps.Docker.Hdl.LogStream` → `LogStream`）。
func shortName(handler string) string {
	if i := strings.LastIndex(handler, "."); i >= 0 {
		return handler[i+1:]
	}
	return handler
}

// ── 跨层对齐守卫：Go 长活挂载清单 ↔ nginx 长活 location 清单 ─────────────

// TestNginxLongLivedLocationsMatchRouterMounts 断言 router.go 里每个挂了
// longLived 的端点，穿过 nginx 反代（uni_console/nginx.conf）时都会命中带
// 长超时的 location（P0）。
//
// 为什么需要它：middleware.LongLived 只接管了 Go 服务端自己的 WriteTimeout；
// 请求还要穿过 nginx —— 长活端点若落回普通 location /api/（proxy_read_timeout
// 60s），「60s 没有新数据」照样被反代掐断；对 docker 的流式端点，本波
// 「断流 = best-effort cancel」语义会把掐断升级成取消操作本身（杀拉取）。
// 修复前 stats/pull/build/push/events/大文件下载逐一漏配，根因是两层清单各自
// 手写、无人对齐 —— 所以这里不维护第二份手写路径清单（必然漂移），而是从
// router.go 源码**推导**全部 longLived 挂载点，实例化成样例 URL 后按 nginx 的
// location 选择顺序（先 = 精确，再按出现序的第一个匹配正则，最后最长前缀）求出
// 真正命中的 location，断言：GET 长活（流/下载/慢采集）带够长的
// proxy_read_timeout 且禁止缓冲；POST 长活（上传）带够长的 proxy_send_timeout
// 且禁止请求体缓冲（proxy_request_buffering off —— 请求体在反代层落临时盘会
// 击穿「core 中转零落盘」语义）。
// 增删长活端点而不同步 nginx.conf，这里就红。
func TestNginxLongLivedLocationsMatchRouterMounts(t *testing.T) {
	conf, err := os.ReadFile("../../../uni_console/nginx.conf")
	if err != nil {
		t.Fatalf("读取 uni_console/nginx.conf 失败（monorepo 布局变了？）: %v", err)
	}
	locs := parseNginxLocations(t, string(conf))
	if len(locs) == 0 {
		t.Fatal("nginx.conf 里没有解析出任何 location —— 解析器与配置形态脱节了")
	}

	derived := longLivedEndpointsFromRouter(t)
	agentChecked := false
	for _, ep := range derived {
		// web 面对外入口（listen 80）：所有长活端点都要在这里命中长活 location。
		checkNginxCovers(t, locs, 80, ep)
		// agent 面出口是 20443 TLS 终端（80 上只 403 了 agent ws；下载虽然
		// 放通，agent 实际只连 TLS）——agent 前缀的端点必须在那个 server 块里
		// 也命中长活 location。
		if strings.HasPrefix(ep.url, "/api/v1/agent/") {
			checkNginxCovers(t, locs, 20443, ep)
			agentChecked = true
		}
	}
	if !agentChecked {
		t.Fatalf("推导出的 %d 个长活端点里没有 /api/v1/agent/ 前缀项 —— agent 下载通道"+
			"被改名/移除？推导器或守卫的 20443 分支需要同步", len(derived))
	}
}

// llSample 是从 router.go 推导出的一个长活端点样例。
type llSample struct {
	method string // GET（流/下载/慢采集）或 POST（上传）
	url    string // 实例化路径：:param → x，/*any → /anything
}

// longLivedEndpointsFromRouter 逐行扫描 router.go，跟踪 Group 前缀链，收集全部
// 挂了 longLived 的路由注册行并实例化成样例 URL。推导失败要响：清单为空或
// Group 链断裂说明本守卫没在守护任何东西。
func longLivedEndpointsFromRouter(t *testing.T) []llSample {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	// api 组前缀取生产/示例配置的 server.apiPrefix（configs/config.yaml）；
	// 前缀可配，但 nginx.conf 只可能按实际部署前缀书写，改了前缀这里与
	// nginx.conf 一起同步。
	prefixes := map[string]string{"api": "/api/v1"}
	groupRe := regexp.MustCompile(`^\s*(\w+)\s*:=\s*(\w+)\.Group\("([^"]*)"\)`)
	routeRe := regexp.MustCompile(`^\s*(\w+)\.(GET|POST|PUT|DELETE)\("([^"]*)"`)
	paramRe := regexp.MustCompile(`:[A-Za-z_]\w*`)

	var out []llSample
	for _, line := range strings.Split(string(src), "\n") {
		if m := groupRe.FindStringSubmatch(line); m != nil {
			parent, ok := prefixes[m[2]]
			if !ok {
				t.Fatalf("Group 链断裂：%q 的父组 %q 未知（本守卫的推导器要同步）",
					strings.TrimSpace(line), m[2])
			}
			prefixes[m[1]] = parent + m[3]
			continue
		}
		if !strings.Contains(line, "longLived") {
			continue
		}
		m := routeRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		url := paramRe.ReplaceAllString(prefixes[m[1]]+m[3], "x")
		url = strings.Replace(url, "/*any", "/anything", 1)
		out = append(out, llSample{method: m[2], url: url})
	}
	if len(out) == 0 {
		t.Fatal("router.go 里没推导出任何 longLived 路由 —— 推导器失效了？")
	}
	return out
}

// nginxLoc 是一个解析出的 nginx location。
type nginxLoc struct {
	port    int            // 所属 server 块的 listen 端口
	kind    string         // "exact"（=）/ "regex"（~/~*）/ "prefix"（普通前缀）
	pattern string         // 原始匹配式（前缀/精确定字面量，正则留源码形态）
	re      *regexp.Regexp // 仅 regex 类：编译后的匹配器
	body    string         // location 体（直到同级收尾 } 之前的原始行）
}

var (
	nginxListenRe   = regexp.MustCompile(`^\s*listen\s+(\d+)`)
	nginxLocationRe = regexp.MustCompile(`^\s*location\s+(?:(=|\^~|~\*|~)\s+)?(\S+)\s*\{`)
)

// parseNginxLocations 是守卫专用的最小 nginx 解析：跟踪 listen 端口以区分
// server 块；location 体内无嵌套块是本仓库配置的既有形态（嵌套块如 if 会让解析
// 在第一个 } 处截断 —— 真要这么写再升级解析器，别让它静默失守）。
func parseNginxLocations(t *testing.T, src string) []nginxLoc {
	t.Helper()
	var locs []nginxLoc
	port := 0
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		if m := nginxListenRe.FindStringSubmatch(lines[i]); m != nil {
			port, _ = strconv.Atoi(m[1])
			continue
		}
		m := nginxLocationRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		loc := nginxLoc{port: port, pattern: m[2]}
		switch m[1] {
		case "=":
			loc.kind = "exact"
		case "~", "~*":
			loc.kind = "regex"
			p := m[2]
			if m[1] == "~*" {
				p = "(?i)" + p
			}
			re, err := regexp.Compile(p)
			if err != nil {
				t.Fatalf("nginx 正则 %q 无法按 Go 正则编译（PCRE 子集之外？）: %v", m[2], err)
			}
			loc.re = re
		default:
			loc.kind = "prefix"
		}
		for i++; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "}") {
				break
			}
			loc.body += lines[i] + "\n"
		}
		locs = append(locs, loc)
	}
	return locs
}

// nginxWinner 按 nginx 的 location 选择顺序在指定 server 块里求命中：先 = 精确，
// 再按出现序的第一个匹配正则，最后最长前缀（本文件没有 ^~，略去该特例）。
func nginxWinner(locs []nginxLoc, port int, url string) *nginxLoc {
	for i := range locs {
		if locs[i].port == port && locs[i].kind == "exact" && locs[i].pattern == url {
			return &locs[i]
		}
	}
	var firstRegex, longestPrefix *nginxLoc
	for i := range locs {
		if locs[i].port != port {
			continue
		}
		switch locs[i].kind {
		case "regex":
			if firstRegex == nil && locs[i].re.MatchString(url) {
				firstRegex = &locs[i]
			}
		case "prefix":
			if strings.HasPrefix(url, locs[i].pattern) &&
				(longestPrefix == nil || len(locs[i].pattern) > len(longestPrefix.pattern)) {
				longestPrefix = &locs[i]
			}
		}
	}
	if firstRegex != nil {
		return firstRegex
	}
	return longestPrefix
}

// checkNginxCovers 断言样例端点在指定 server 块里命中的 location 带足了长超时：
// GET（流/下载/慢采集）看 proxy_read_timeout 且要求 proxy_buffering off（流式
// 响应不许在反代层攒缓冲）；POST（上传）看 proxy_send_timeout 且要求
// proxy_request_buffering off（请求体不许在反代层落临时盘，要逐段直通上游）。
// 命中 /api/ 兜底（60s）或除 location / 外没命中，都按漏配报出。
func checkNginxCovers(t *testing.T, locs []nginxLoc, port int, ep llSample) {
	t.Helper()
	loc := nginxWinner(locs, port, ep.url)
	if loc == nil {
		t.Errorf(":%d 无任何 location 命中长活端点 %s %s —— 补进 nginx.conf 的长活 location 群",
			port, ep.method, ep.url)
		return
	}
	want := "proxy_read_timeout"
	if ep.method == "POST" {
		want = "proxy_send_timeout"
	}
	if sec := nginxTimeoutSeconds(loc.body, want); sec <= 60 {
		t.Errorf(":%d 的长活端点 %s %s 命中 location %q，但 %s=%ds（0=未设置，≤60s 按漏配看待）—— "+
			"反代层会在 60s 静默时掐断它；收进 nginx.conf 的长活 location 群（见该文件注释）",
			port, ep.method, ep.url, loc.pattern, want, sec)
	}
	if ep.method == "GET" && !strings.Contains(loc.body, "proxy_buffering off") {
		t.Errorf(":%d 的长活流 %s 命中 location %q 缺 proxy_buffering off —— "+
			"流式响应会在反代层被攒缓冲，逐行即到被破坏", port, ep.url, loc.pattern)
	}
	if ep.method == "POST" && !strings.Contains(loc.body, "proxy_request_buffering off") {
		t.Errorf(":%d 的长活上传 %s 命中 location %q 缺 proxy_request_buffering off —— "+
			"请求体会先在反代层落一份临时盘拷（512MB 构建上下文 = 512MB 临时落盘），"+
			"击穿「core 中转零落盘」语义，且上游要等 body 收满才能开始处理", port, ep.url, loc.pattern)
	}
}

// nginxTimeoutSeconds 读 location 体里 `name 3600s;` 形态的超时秒数；
// 未设置返回 0（等价 nginx 默认 60s 口径，按漏配处理）。
func nginxTimeoutSeconds(body, name string) int {
	m := regexp.MustCompile(`(?m)^\s*` + name + `\s+(\d+)s\s*;`).FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
