package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── P3·安全面：image:scan 的 trivy 执行器 ───────────────────────────────
//
// 选型根因（扫描器放 agent）：core 没有 docker 面 —— 它连不上 docker.sock，也不该
// 为「扫一个镜像」去拥有主机的执行通道；agent 已是「在这台主机上做事」的唯一执行面
//（compose CLI、exec、save/load 都从它走），trivy 是同一族的**主机级 CLI**。
//
// 执行纪律与 compose CLI 同款（compose_exec.go 的三条硬纪律）：
//   1. **固定 argv、永不经 shell**：target 在拼接前已被协议层校验（镜像引用白名单，
//      不含空白/引号/分号），这里不再有第二套判断；
//   2. **stdout 只装 JSON**：用 Output() 而不是 CombinedOutput() —— trivy 的进度
//      与错误都写 stderr，混进 stdout 会把 JSON 解析炸掉（--quiet 再压一道）；
//   3. **失败是结论句**：trivy 的 FATAL 原文只进 detail（页面文案纪律）。
//
// trivy 探测照 compose flavor 的先例（probeComposeOnce）但**不落盘**：flavor 决定
// argv 形态、错一次就整族指令拼错命令，故要「主机上定下不再改」；trivy 只是一份
// 能力，缺席时回结论句（「未安装」而不是静默降级成「扫描完成 0 条」—— 后者会把
// 「没扫」伪装成「干净」，安全面最不可接受的失败形态）。缓存口径见 trivyPath。

const (
	// trivyBinary 是主机上要找的扫描器二进制名。
	trivyBinary = "trivy"
	// trivyExecTimeout 是交给 trivy 自身的 --timeout（14 分钟）。
	//
	// 比外层执行档（writeTimeouts 的 15 分钟）短一截：让「trivy 自己报超时」先于
	//「agent 杀进程」发生 —— 前者带原因（stderr 原文进 detail），后者只有一句
	// "signal: killed"，排障价值天差地别。这与「agent 超时要早于 core sweep」是
	// 同一条「先到先解释」的纪律，只是往里再垫一层。
	trivyExecTimeout = 14 * time.Minute
)

// trivyScanner 是 trivy 的执行器：探测（LookPath）+ 固定 argv 执行。
//
// 两个注入点（execFn / lookPath）都由 NewWriteExecutor 走缺省、测试换替身 ——
// 与 compose CLI 的 exec 注入同一模式，测试因此不需要主机上真装 trivy。
type trivyScanner struct {
	// execFn 是固定 argv 的执行入口（由 WriteExecutor 的注入传进来，同 compose CLI）。
	execFn func(ctx context.Context, name string, args ...string) *exec.Cmd
	// lookPath 是探测入口（缺省 exec.LookPath）。
	lookPath func(string) (string, error)

	// mu 守住 path 缓存：Do 在分派器 worker 里跑，探测由 scanImage 触发，
	// 与配置热更（Set*）同处一个读写竞争面。
	mu sync.Mutex
	// path 是已找到的 trivy 路径（空 = 还没找到过）。
	path string
}

// newTrivyScanner 构造扫描器。execFn 为 nil 时用 exec.CommandContext（与
// NewWriteExecutor 的缺省同源）。
func newTrivyScanner(execFn func(context.Context, string, ...string) *exec.Cmd) *trivyScanner {
	if execFn == nil {
		execFn = exec.CommandContext
	}
	return &trivyScanner{execFn: execFn, lookPath: exec.LookPath}
}

// trivyPath 探测 trivy 的可执行路径。
//
// 缓存口径是**非对称**的：
//   - 找到过 → 缓存到进程退出（装好的 trivy 不会自己消失，重复 LookPath 是白跑）；
//   - 没找到 → **不缓存**，每次扫描现探：管理员装上 trivy 的下一扫立刻可用，
//     不需要重启 agent —— 扫描是分钟级操作，多付一次 PATH 目录扫描（微秒级）
//     换「装完即生效」值得。
//
// 与 compose flavor 的「探测一次永不改」刻意不同：flavor 错了会让**整族** argv 拼错
// 命令（静默执行成别的动作），一致性比新鲜度重要；trivy 缺席只会得到一句结论，
// 新鲜度没有代价。
func (s *trivyScanner) trivyPath() (string, error) {
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	if path != "" {
		return path, nil
	}
	p, err := s.lookPath(trivyBinary)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.path = p
	s.mu.Unlock()
	return p, nil
}

// scanImage 执行 image:scan（write.go 的 Do 在 scan 分支上调用）。
//
// 顺序：先 daemon inspect 解析镜像 ID（也是「镜像存在」的预检 —— 不存在时给
// 「读取镜像信息失败」而不是让 trivy 去撞同一个 404），再探测 trivy（缺席给
// 结论句），最后跑扫描。镜像 ID 在扫描**前**解析：它是报告的内容寻址键，
// 用 daemon 的答案而不是 trivy 报告里的自述值 —— 协议键以 daemon 为唯一事实源。
func (e *WriteExecutor) scanImage(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	o := &cmd.Options
	d, err := e.api.ImageInspect(ctx, o.Target)
	if err != nil {
		return nil, wrapDocker("读取镜像信息失败", err)
	}
	sc := e.scannerValue()
	bin, err := sc.trivyPath()
	if err != nil {
		// 能力缺席的结论句（照 compose flavor 先例：不静默降级）。「0 条漏洞」
		// 与「没有扫描器」是两个相反的结论，混起来就是安全面的静默失败。
		return nil, &ExecError{
			Msg:    "主机未安装 trivy，无法扫描镜像（请先在这台主机上安装 trivy）",
			Detail: "lookPath " + trivyBinary + ": " + err.Error(),
		}
	}
	// 固定 argv、零 shell：target 已过协议的镜像引用白名单（无空白/元字符），
	// --timeout 让 trivy 自己的表先到（见 trivyExecTimeout 的注释）。
	c := sc.execFn(ctx, bin,
		"image", "--format", "json", "--quiet",
		"--timeout", trivyExecTimeout.String(),
		o.Target)
	out, err := c.Output()
	if err != nil {
		// 区分「超时」与「trivy 自己失败」：外层 ctx 到点时进程被杀，Output 的
		// 错误只剩 "signal: killed"，把它翻成超时结论句；trivy 自报的失败
		//（含首扫漏洞库下载失败 —— 那条 stderr 原文会说 DB download）进 detail。
		if ctx.Err() != nil {
			return nil, &ExecError{
				Msg:    "镜像扫描超时（首次扫描需要下载漏洞库，可能明显偏慢，请稍后重试）",
				Detail: agentproto.NormalizeDockerDetail(err.Error()),
			}
		}
		detail := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = string(ee.Stderr)
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, &ExecError{Msg: "镜像扫描失败", Detail: agentproto.NormalizeDockerDetail(detail)}
	}
	report, err := normalizeTrivyReport(out, d.ID, e.nowValue())
	if err != nil {
		return nil, &ExecError{Msg: "解析扫描报告失败", Detail: agentproto.NormalizeDockerDetail(err.Error())}
	}
	return marshalWritePayload(report)
}

// ── trivy JSON → DockerScanReport 的归一化 ───────────────────────────────
//
// 解析纪律照 scanBuildContext（build 上下文的报告归一化先例）：只解码要用的字段
//（未知字段忽略 —— trivy 加字段不炸 agent），条目缺关键字段（无 CVE 号/无包名）
// 跳过而不是拒整份报告 —— 一条坏数据不该杀死 3000 条好数据。

// trivyReport / trivyResult / trivyVuln 是 trivy JSON 的最小解码面。
type trivyReport struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Target          string      `json:"Target"`
	Vulnerabilities []trivyVuln `json:"Vulnerabilities"`
}

type trivyVuln struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
}

// scanSeverityRank 是 severity 的排序权重（critical 最高、unknown 最低）。
// 同档次序按 id 字典序 —— 数组的形状对前后端都是「确定性」的（同一份 trivy
// 报告两次归一化得到逐字节相同的载荷，diff 与测试才有意义）。
func scanSeverityRank(s string) int {
	switch s {
	case agentproto.DockerScanSeverityCritical:
		return 4
	case agentproto.DockerScanSeverityHigh:
		return 3
	case agentproto.DockerScanSeverityMedium:
		return 2
	case agentproto.DockerScanSeverityLow:
		return 1
	default:
		return 0
	}
}

// normalizeScanSeverity 把 trivy 的 Severity（大写、可能空、可能白名单外）折成
// 协议的五档小写。白名单外的值（trivy 新增档位/上游数据漂移）折到 unknown ——
// 「未知」比「丢弃」诚实：丢弃会让计数与条目数对不上。
func normalizeScanSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case agentproto.DockerScanSeverityCritical:
		return agentproto.DockerScanSeverityCritical
	case agentproto.DockerScanSeverityHigh:
		return agentproto.DockerScanSeverityHigh
	case agentproto.DockerScanSeverityMedium:
		return agentproto.DockerScanSeverityMedium
	case agentproto.DockerScanSeverityLow:
		return agentproto.DockerScanSeverityLow
	default:
		return agentproto.DockerScanSeverityUnknown
	}
}

// normalizeTrivyReport 把 trivy 的 JSON 报告折成协议载荷。
//
// 三条归一化口径（都写进测试）：
//   - **折平**：Results[].Vulnerabilities[] 全部折平成一份条目数组（镜像扫描常驻
//     一份 os-pkgs Result，但多阶段构建/语言依赖会出现多份 ——「一张表」是页面的
//     阅读形态，Result 的划分是 trivy 的内部结构）；
//   - **去重 (id, pkg)**：同一 CVE 打同一个包的多条（多 Result 重复报告、同包
//     多版本 —— 协议条目不带 InstalledVersion，同包不同版本对「升级该包」这个
//     修复动作没有额外信息）折成一条，保留 severity 更高的一条；
//   - **计数全量、条目截断**：Counts 对去重后的全集求和；Vulns 截到
//     MaxDockerScanVulnEntries 并置 Truncated（协议注释里的体积账）。
func normalizeTrivyReport(raw []byte, imageID string, at time.Time) (*agentproto.DockerScanReport, error) {
	var tr trivyReport
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, err
	}
	// 去重：键 (id, pkg)，保留 severity 更高的一条（同键不同档是上游数据漂移，
	// 按更糟的档报 —— 安全面取悲观值）。
	type key struct{ id, pkg string }
	dedup := map[key]agentproto.DockerScanVuln{}
	for _, res := range tr.Results {
		for _, v := range res.Vulnerabilities {
			if v.VulnerabilityID == "" || v.PkgName == "" {
				// 无 CVE 号/无包名的条目无法行动（页面的排序、跳转、修复建议
				// 全靠这两元），跳过而不是占一个「未知」名额 —— 它不是漏洞事实，
				// 是上游数据缺角。
				continue
			}
			entry := agentproto.DockerScanVuln{
				ID:           v.VulnerabilityID,
				Pkg:          v.PkgName,
				Severity:     normalizeScanSeverity(v.Severity),
				FixedVersion: v.FixedVersion,
				Title:        truncateScanTitle(v.Title),
			}
			k := key{v.VulnerabilityID, v.PkgName}
			if old, ok := dedup[k]; !ok || scanSeverityRank(entry.Severity) > scanSeverityRank(old.Severity) {
				dedup[k] = entry
			}
		}
	}
	// 排序：severity 降序 → id 字典序（确定性形状，见 scanSeverityRank 的注释）。
	all := make([]agentproto.DockerScanVuln, 0, len(dedup))
	for _, v := range dedup {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool {
		ri, rj := scanSeverityRank(all[i].Severity), scanSeverityRank(all[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if all[i].ID != all[j].ID {
			return all[i].ID < all[j].ID
		}
		return all[i].Pkg < all[j].Pkg
	})
	report := &agentproto.DockerScanReport{
		ImageID:   imageID,
		ScannedAt: at.Unix(),
		Counts:    scanCountsOf(all),
		Vulns:     make([]agentproto.DockerScanVuln, 0, min(len(all), agentproto.MaxDockerScanVulnEntries)),
		Truncated: len(all) > agentproto.MaxDockerScanVulnEntries,
	}
	if len(all) > agentproto.MaxDockerScanVulnEntries {
		// 截断**必须**发生在排序之后：只展示前 500 条时，被留下的必须是
		// severity 最高的那 500 条 —— 「截断截掉了 critical」比「少 2700 条
		// low」严重得多。
		all = all[:agentproto.MaxDockerScanVulnEntries]
	}
	report.Vulns = append(report.Vulns, all...)
	return report, nil
}

// scanCountsOf 对条目全集求 severity 计数（在截断前调用 —— 计数是全量事实）。
func scanCountsOf(vulns []agentproto.DockerScanVuln) agentproto.DockerScanCounts {
	var c agentproto.DockerScanCounts
	for _, v := range vulns {
		switch v.Severity {
		case agentproto.DockerScanSeverityCritical:
			c.Critical++
		case agentproto.DockerScanSeverityHigh:
			c.High++
		case agentproto.DockerScanSeverityMedium:
			c.Medium++
		case agentproto.DockerScanSeverityLow:
			c.Low++
		default:
			c.Unknown++
		}
	}
	return c
}

// truncateScanTitle 把 title 截到 MaxDockerScanVulnTitleBytes（按字节截但在
// UTF-8 边界上回退，不劈半个字符 —— title 是英文长句为主，但防线不为统计规律开）。
func truncateScanTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= agentproto.MaxDockerScanVulnTitleBytes {
		return s
	}
	cut := s[:agentproto.MaxDockerScanVulnTitleBytes]
	// 回退到完整 UTF-8 序列的边界（-3 最多覆盖一个 4 字节字符的残段）。
	for len(cut) > 0 && !isUTF8Boundary(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// isUTF8Boundary 报告 s 的末尾是否落在一个完整 UTF-8 序列之后。
func isUTF8Boundary(s string) bool {
	// 从尾部回看连续的 10xxxxxx 前导字节：0 个或恰好 N 个（N = 首字节的长度标记）。
	n := 0
	for i := len(s) - 1; i >= 0 && n < 4; i-- {
		if s[i]&0xC0 == 0x80 {
			n++
			continue
		}
		// 剩余位是序列首字节：完整序列的期望长度由它的高位决定。
		want := 1
		switch {
		case s[i]&0xE0 == 0xC0:
			want = 2
		case s[i]&0xF0 == 0xE0:
			want = 3
		case s[i]&0xF8 == 0xF0:
			want = 4
		}
		return n == want-1
	}
	return n == 0
}
