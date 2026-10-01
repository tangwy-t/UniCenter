package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 注入替身：记录 argv + 输出可控的 trivy 进程 ──────────────────────────
//
// 照 composeExecStub 的模式：宿主无关的假 trivy 进程（测试二进制自身 +
// TestTrivyExecHelperProcess 入口），stdout 装报告 JSON、stderr/exitCode 控制失败
// 形态 —— scanImage 的契约是「stdout 只有 JSON」（Output() 而不是
// CombinedOutput()），替身如实还原这个前提，坏 JSON 与失败路径都不依赖
// CI 主机上装 trivy。

// trivyExecStub 是 trivy CLI 的执行替身：记录 (name, args)，按注入的
// stdout/stderr/exitCode 表演一次执行。
type trivyExecStub struct {
	mu       sync.Mutex
	calls    []composeCall
	stdout   string
	stderr   string
	exitCode int
}

func (s *trivyExecStub) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	s.mu.Lock()
	s.calls = append(s.calls, composeCall{name: name, args: append([]string(nil), args...)})
	stdout, stderr, code := s.stdout, s.stderr, s.exitCode
	s.mu.Unlock()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestTrivyExecHelperProcess$")
	cmd.Env = append(os.Environ(),
		"UNI_TEST_TRIVY_HELPER=1",
		"UNI_TEST_TRIVY_STDOUT="+stdout,
		"UNI_TEST_TRIVY_STDERR="+stderr,
		"UNI_TEST_TRIVY_EXIT="+strconv.Itoa(code),
	)
	return cmd
}

func (s *trivyExecStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *trivyExecStub) last(t *testing.T) composeCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("trivy 没有被执行")
	}
	return s.calls[len(s.calls)-1]
}

// TestTrivyExecHelperProcess 不是测试：它是 trivyExecStub 启动的假 trivy 进程。
func TestTrivyExecHelperProcess(t *testing.T) {
	if os.Getenv("UNI_TEST_TRIVY_HELPER") != "1" {
		return
	}
	if s := os.Getenv("UNI_TEST_TRIVY_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("UNI_TEST_TRIVY_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code, _ := strconv.Atoi(os.Getenv("UNI_TEST_TRIVY_EXIT"))
	os.Exit(code)
}

// scanFixture 是一组扫描测试的公共事实：镜像引用、daemon 解析出的镜像 ID、
// trivy 的假路径（lookPath 替身固定返回它 —— argv 断言据此核对「执行的是找到的
// 路径」而不是裸命令名）。
const (
	scanTarget    = "nginx:1.27"
	scanImageID   = "sha256:" + "ab0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	fakeTrivyPath = "/usr/bin/trivy"
)

// newScanExecutor 构造一个接好替身的写执行器：api 提供 imageDetail，
// exec 是 trivy 替身，lookPath 恒定找到 trivy（要测「未安装」的用例自己换）。
func newScanExecutor(t *testing.T, api DockerAPI, stub *trivyExecStub) *WriteExecutor {
	e := NewWriteExecutor(api, ParseProtected(""), t.TempDir(), "", stub.command)
	e.scanner.lookPath = func(string) (string, error) { return fakeTrivyPath, nil }
	return e
}

// scanCmd 折一条 image:scan 指令。
func scanCmd() *agentproto.DockerCmd {
	return &agentproto.DockerCmd{Ref: "1790000000000000901", Action: agentproto.DockerActionImageScan,
		Options: agentproto.DockerCmdOptions{Target: scanTarget}}
}

// scanAPI 提供 ImageInspect 成功形态的 stub。
func scanAPI() *stubAPI {
	return &stubAPI{imageDetail: ImageDetail{ID: scanImageID, RepoTags: []string{scanTarget}}}
}

// scanTrivyJSON 是一份「两条 Result、含重复条目与缺角条目」的 trivy 报告样本。
//
// 覆盖四个归一化事实：
//   - 第二条 Result 里 CVE-2026-0100/openssl 是第一条的重复（severity 更低）→
//     去重按更糟的档保留 critical；
//   - CVE-2026-0103 缺 PkgName → 跳过（不占计数）；
//   - Severity 空串与白名单外（"NEGLIGIBLE"）→ unknown；
//   - 高低档交错出现 → 输出必须按 severity 降序重排。
const scanTrivyJSON = `{
 "SchemaVersion": 2, "ArtifactName": "` + scanTarget + `", "ArtifactType": "container_image",
 "Results": [
  {"Target": "nginx:1.27 (debian 12)", "Class": "os-pkgs", "Type": "debian",
   "Vulnerabilities": [
    {"VulnerabilityID": "CVE-2026-0102", "PkgName": "libxml2", "InstalledVersion": "2.9.13",
     "Severity": "MEDIUM", "Title": "libxml2: use after free"},
    {"VulnerabilityID": "CVE-2026-0100", "PkgName": "openssl", "InstalledVersion": "3.0.11",
     "FixedVersion": "3.0.12", "Severity": "CRITICAL", "Title": "openssl: X.509 chain issue"},
    {"VulnerabilityID": "CVE-2026-0103", "Severity": "HIGH", "Title": "no package recorded"}
   ]},
  {"Target": "nginx:1.27 (node 20)", "Class": "lang-pkgs", "Type": "npm",
   "Vulnerabilities": [
    {"VulnerabilityID": "CVE-2026-0100", "PkgName": "openssl", "InstalledVersion": "3.0.11",
     "Severity": "LOW"},
    {"VulnerabilityID": "CVE-2026-0104", "PkgName": "zlib", "Severity": ""},
    {"VulnerabilityID": "CVE-2026-0101", "PkgName": "glibc", "Severity": "NEGLIGIBLE"}
   ]}
 ]
}`

// TestScanImageReport：成功路径 —— argv 逐字、报告归一化（去重/排序/缺角跳过/
// severity 折算）、image_id 用 daemon 的答案。
func TestScanImageReport(t *testing.T) {
	stub := &trivyExecStub{stdout: scanTrivyJSON}
	e := newScanExecutor(t, scanAPI(), stub)
	at := time.Unix(1790000000, 0)
	e.SetNow(func() time.Time { return at })

	payload, err := e.Do(context.Background(), scanCmd())
	if err != nil {
		t.Fatalf("扫描应成功: %v", err)
	}
	// argv：固定形态、零 shell —— 目标引用是唯一的可变片段且已过协议白名单。
	call := stub.last(t)
	if call.name != fakeTrivyPath {
		t.Fatalf("必须执行 lookPath 找到的路径，实际 %q", call.name)
	}
	want := []string{"image", "--format", "json", "--quiet", "--timeout", trivyExecTimeout.String(), scanTarget}
	if strings.Join(call.args, " ") != strings.Join(want, " ") {
		t.Fatalf("trivy argv 不符:\n got %v\nwant %v", call.args, want)
	}
	var report agentproto.DockerScanReport
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("载荷不是合法的扫描报告: %v", err)
	}
	if report.ImageID != scanImageID {
		t.Fatalf("image_id 必须是 daemon inspect 的答案，实际 %q", report.ImageID)
	}
	if report.ScannedAt != at.Unix() {
		t.Fatalf("scanned_at 必须取注入挂钟，实际 %d", report.ScannedAt)
	}
	// 计数：去重后 4 条（0100 去掉重复、0103 缺包名跳过）——
	// critical 1 / medium 1 / unknown 2（空串与 NEGLIGIBLE 都折 unknown）。
	if report.Counts != (agentproto.DockerScanCounts{Critical: 1, Medium: 1, Unknown: 2}) {
		t.Fatalf("severity 计数不符: %+v", report.Counts)
	}
	if len(report.Vulns) != 4 {
		t.Fatalf("去重后应为 4 条，实际 %d: %+v", len(report.Vulns), report.Vulns)
	}
	// 排序：critical 在最前，同档（两条 unknown）按 id 字典序。
	if report.Vulns[0].ID != "CVE-2026-0100" || report.Vulns[0].Severity != agentproto.DockerScanSeverityCritical {
		t.Fatalf("首条必须是 critical 的 CVE-2026-0100，实际 %+v", report.Vulns[0])
	}
	if report.Vulns[0].FixedVersion != "3.0.12" || report.Vulns[0].Title != "openssl: X.509 chain issue" {
		t.Fatalf("修复版与摘要必须保留: %+v", report.Vulns[0])
	}
	if report.Vulns[3].ID != "CVE-2026-0104" || report.Vulns[2].ID != "CVE-2026-0101" {
		t.Fatalf("同档必须按 id 字典序: %+v", report.Vulns)
	}
	for _, v := range report.Vulns {
		if v.ID == "CVE-2026-0103" {
			t.Fatal("缺包名的条目必须被跳过（无法行动的数据不是漏洞事实）")
		}
		if v.Severity != agentproto.DockerScanSeverityUnknown && v.Severity != "critical" &&
			v.Severity != "medium" {
			t.Fatalf("severity 必须折成协议五档，实际 %q", v.Severity)
		}
	}
}

// TestScanImageTrivyMissing：能力缺席 → 结论句，且不执行任何命令。
// 「未安装」与「扫完 0 条」是相反的结论 —— 这条测试钉住不静默降级。
func TestScanImageTrivyMissing(t *testing.T) {
	stub := &trivyExecStub{stdout: scanTrivyJSON}
	e := newScanExecutor(t, scanAPI(), stub)
	e.scanner.lookPath = func(string) (string, error) {
		return "", &exec.Error{Name: trivyBinary, Err: exec.ErrNotFound}
	}
	_, err := e.Do(context.Background(), scanCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "未安装 trivy") {
		t.Fatalf("trivy 缺席必须给结论句，实际 %v", err)
	}
	if stub.count() != 0 {
		t.Fatal("没有扫描器就不许执行任何命令")
	}
}

// TestScanImageTrivyFailure：trivy 自报失败（非零退出 + stderr FATAL 原文）。
func TestScanImageTrivyFailure(t *testing.T) {
	stub := &trivyExecStub{stderr: "FATAL: image scan error: failed to download vulnerability DB", exitCode: 1}
	e := newScanExecutor(t, scanAPI(), stub)
	_, err := e.Do(context.Background(), scanCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "镜像扫描失败" {
		t.Fatalf("结论句必须是「镜像扫描失败」，实际 %v", err)
	}
	if !strings.Contains(ee.Detail, "vulnerability DB") {
		t.Fatalf("trivy 的 stderr 原文必须进 detail（排障线索），实际 %q", ee.Detail)
	}
}

// TestScanImageBadJSON：stdout 不是 JSON → 解析失败结论（而不是空报告成功）。
func TestScanImageBadJSON(t *testing.T) {
	stub := &trivyExecStub{stdout: "not json at all"}
	e := newScanExecutor(t, scanAPI(), stub)
	_, err := e.Do(context.Background(), scanCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "解析扫描报告失败" {
		t.Fatalf("坏 JSON 必须报解析失败，实际 %v", err)
	}
}

// TestScanImageTimeout：外层 ctx 到点 → 超时结论句（并提示首扫漏洞库下载偏慢 ——
// 那是分钟级扫描最常见的时间去向，结论句要可行动）。
func TestScanImageTimeout(t *testing.T) {
	stub := &trivyExecStub{stdout: scanTrivyJSON}
	e := newScanExecutor(t, scanAPI(), stub)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 到点的外层 ctx：exec 立刻失败（signal/ctx 错误），走超时分支
	_, err := e.Do(ctx, scanCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "镜像扫描超时") {
		t.Fatalf("ctx 取消必须折成超时结论，实际 %v", err)
	}
}

// TestScanImageInspectFails：镜像不存在/inspect 失败 → 与 image:inspect 同句
// （trivy 根本不被执行 —— daemon 的答案足够下结论，不拿 CLI 去撞同一个 404）。
func TestScanImageInspectFails(t *testing.T) {
	api := &scanInspectFailAPI{&stubAPI{}}
	stub := &trivyExecStub{stdout: scanTrivyJSON}
	e := newScanExecutor(t, api, stub)
	_, err := e.Do(context.Background(), scanCmd())
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "读取镜像信息失败" {
		t.Fatalf("inspect 失败必须先挡住扫描，实际 %v", err)
	}
	if stub.count() != 0 {
		t.Fatal("镜像都读不到就不许启动 trivy")
	}
}

// scanInspectFailAPI 只让 ImageInspect 失败（其余面照抄 stubAPI）。
type scanInspectFailAPI struct{ *stubAPI }

func (a *scanInspectFailAPI) ImageInspect(context.Context, string) (ImageDetail, error) {
	return ImageDetail{}, errors.New("Error: No such image: " + scanTarget)
}

// TestTrivyPathCaching：探测的非对称缓存 —— 找到过就缓存，没找到每次现探。
// （管理员装上 trivy 的下一扫即生效，不需要重启 agent。）
func TestTrivyPathCaching(t *testing.T) {
	probes := 0
	installed := false
	sc := newTrivyScanner(nil)
	sc.lookPath = func(string) (string, error) {
		probes++
		if !installed {
			return "", exec.ErrNotFound
		}
		return fakeTrivyPath, nil
	}
	if _, err := sc.trivyPath(); err == nil {
		t.Fatal("未安装时的首次探测必须失败")
	}
	if _, err := sc.trivyPath(); err == nil {
		t.Fatal("仍未安装，第二次探测也必须失败（但确实又探了一次）")
	}
	if probes != 2 {
		t.Fatalf("未安装时不得缓存缺席结论（每扫现探），实际探测 %d 次", probes)
	}
	installed = true
	if p, err := sc.trivyPath(); err != nil || p != fakeTrivyPath {
		t.Fatalf("装上后下一探必须找到: %v", err)
	}
	before := probes
	for i := 0; i < 3; i++ {
		if p, err := sc.trivyPath(); err != nil || p != fakeTrivyPath {
			t.Fatalf("已找到后必须命中缓存: %v", err)
		}
	}
	if probes != before {
		t.Fatalf("找到之后必须缓存（重复 LookPath 是白跑），多探了 %d 次", probes-before)
	}
}

// ── 归一化纯函数：截断 / title 上限 / 计数全量 ──────────────────────────

// buildTrivyJSON 拼一份 N 条**唯一** CVE 的 trivy 报告（第 i 条的 id/pkg 都不同，
// severity 按 i 取余轮转五档）。
func buildTrivyJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"Results":[{"Target":"x","Vulnerabilities":[`)
	sev := []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"VulnerabilityID":"CVE-2026-%04d","PkgName":"pkg%d","Severity":"%s"}`,
			i, i, sev[i%len(sev)])
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// TestNormalizeTrivyReportTruncation：500 条上限 —— 截断发生在排序之后
// （留下的必须是 severity 最高的那 500 条），计数对全量如实，Truncated 标注。
func TestNormalizeTrivyReportTruncation(t *testing.T) {
	const total = 1205 // 241×五档轮转 → 每档恰好 241 条
	raw := buildTrivyJSON(total)
	report, err := normalizeTrivyReport([]byte(raw), scanImageID, time.Unix(1790000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Vulns) != agentproto.MaxDockerScanVulnEntries {
		t.Fatalf("条目必须截到上限 %d，实际 %d", agentproto.MaxDockerScanVulnEntries, len(report.Vulns))
	}
	if !report.Truncated {
		t.Fatal("截断必须被标注（页面要能说「仅展示前 500 条」）")
	}
	// 计数是全量事实：每档 241 条（1205/5 轮转均匀）。
	if report.Counts.Critical != 241 || report.Counts.High != 241 || report.Counts.Medium != 241 ||
		report.Counts.Low != 241 || report.Counts.Unknown != 241 {
		t.Fatalf("计数必须对全量如实（不受截断影响），实际 %+v", report.Counts)
	}
	// 截断保最高档：前 500 条全部是 critical + high（241 + 241 = 482，再补 18 条
	// medium —— medium 档内部按 id 字典序）。
	for i, v := range report.Vulns {
		if i < 241 && v.Severity != agentproto.DockerScanSeverityCritical {
			t.Fatalf("截断必须保住 critical：第 %d 条是 %q", i, v.Severity)
		}
		if i >= 241 && i < 482 && v.Severity != agentproto.DockerScanSeverityHigh {
			t.Fatalf("截断必须保住 high：第 %d 条是 %q", i, v.Severity)
		}
	}
	// 截断后的末位条目可稳定复现（确定性形状）：482 条 critical+high 之后补
	// 18 条 medium（i%5==2 轮转，id 按字典序），第 500 条是 i=87。
	if report.Vulns[499].ID != "CVE-2026-0087" {
		t.Fatalf("截断点上的条目应可稳定复现，实际 %q", report.Vulns[499].ID)
	}
}

// TestNormalizeTrivyReportTitleCap：title 截到协议上限（字节截断不劈 UTF-8 字符）。
func TestNormalizeTrivyReportTitleCap(t *testing.T) {
	longTitle := strings.Repeat("x", agentproto.MaxDockerScanVulnTitleBytes) + "尾巴应该被截掉"
	raw := fmt.Sprintf(`{"Results":[{"Target":"x","Vulnerabilities":[
		{"VulnerabilityID":"CVE-2026-9999","PkgName":"p","Severity":"LOW","Title":%q}]}]}`, longTitle)
	report, err := normalizeTrivyReport([]byte(raw), "sha256:x", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := report.Vulns[0].Title
	if len(got) != agentproto.MaxDockerScanVulnTitleBytes {
		t.Fatalf("title 必须截到 %d 字节，实际 %d", agentproto.MaxDockerScanVulnTitleBytes, len(got))
	}
}

// TestNormalizeTrivyReportEmpty：零漏洞报告是合法形态（基础镜像干净）——
// 计数全零、Vulns 是显式空数组（不是缺席：「扫了没有」和「没扫」是两个答案）。
func TestNormalizeTrivyReportEmpty(t *testing.T) {
	report, err := normalizeTrivyReport([]byte(`{"Results":[{"Target":"x"}]}`), "sha256:x", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Vulns) != 0 || report.Truncated {
		t.Fatalf("干净镜像应是空条目 + 未截断，实际 %+v", report)
	}
	if report.Counts != (agentproto.DockerScanCounts{}) {
		t.Fatalf("计数应全零: %+v", report.Counts)
	}
}
