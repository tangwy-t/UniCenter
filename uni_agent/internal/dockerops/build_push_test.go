package dockerops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── P2·分发闭环（image:build / image:push）的 agent 面测试 ───────────────────
//
// 重心在 build 的**输入面**（上下文 tar 的穿越校验 —— 本切片最大的安全面）：
// 校验必须在 daemon 之前把恶意上下文拦下；然后是执行器的定型映射、进度折叠/
// 终态对账（照 4b 的折叠器结构）、取消与零泄漏（照 4c 的钉法）。

// writeTar 在内存里打一个 tar（entries 是 [name, body] 或 [name, "symlink", target]）。
func writeTar(t *testing.T, entries ...[]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		switch len(e) {
		case 2:
			if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(e[1])); err != nil {
				t.Fatal(err)
			}
		case 3:
			if err := tw.WriteHeader(&tar.Header{Name: e[0], Typeflag: tar.TypeSymlink, Linkname: e[2], Mode: 0o777}); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("entries 必须是 2 或 3 元: %v", e)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeGzTar 打一个 gzip 压缩的上下文（上下文的两种合法形态之一）。
func writeGzTar(t *testing.T, entries ...[]string) []byte {
	t.Helper()
	raw := writeTar(t, entries...)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeContextFile 把上下文字节落进目录并返回文件名。
func writeContextFile(t *testing.T, dir string, data []byte) string {
	t.Helper()
	name := "ctx" + time.Now().Format("150405.000000000") + ".tar"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// minimalContext 是含 Dockerfile 的最小合法上下文。
func minimalContext(t *testing.T) []byte {
	return writeTar(t,
		[]string{"Dockerfile", "FROM scratch\n"},
		[]string{"app/main.go", "package main\n"},
	)
}

// TestScanBuildContextRejectsTraversal：穿越三件套（../、绝对路径、反斜杠）与
// 链接目标穿越都必须**在 daemon 之前**被拒 —— 这是「上下文是新输入面」的护栏，
// 放行任何一条都等于让 tar 决定 agent 能写宿主哪里的文件。
func TestScanBuildContextRejectsTraversal(t *testing.T) {
	cases := []struct {
		name string
		tar  []byte
	}{
		{"穿越根外", writeTar(t, []string{"../../etc/cron.d/x", "evil"})},
		{"中段穿越", writeTar(t, []string{"a/../../evil", "x"})},
		{"绝对路径", writeTar(t, []string{"/etc/passwd", "x"})},
		{"反斜杠形态", writeTar(t, []string{`..\evil`, "x"})},
		{"链接绝对目标", writeTar(t, []string{"Dockerfile", "FROM scratch\n"}, []string{"link", "/etc/passwd", "/etc/passwd"})},
		{"链接穿越目标", writeTar(t, []string{"Dockerfile", "FROM scratch\n"}, []string{"link", "../../outside", "../../outside"})},
		{"gzip 包着的穿越", writeGzTar(t, []string{"../../evil", "x"})},
	}
	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, writeContextFile(t, dir, c.tar))
			err := scanBuildContext(path, "Dockerfile")
			if err == nil || !strings.Contains(err.Error(), "非法") {
				t.Fatalf("穿越条目必须被拒，实际 %v", err)
			}
		})
	}
}

// TestScanBuildContextAcceptsLegitShapes：合法上下文的**常见打包形态**不被误伤：
// tar -C dir . 的 "./" 前缀、gzip 形态、子目录 Dockerfile、符号链接（指向上下文内）。
func TestScanBuildContextAcceptsLegitShapes(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		tar    []byte
		docker string
	}{
		{"极简", writeTar(t, []string{"Dockerfile", "FROM scratch\n"}), "Dockerfile"},
		{"点前缀打包", writeTar(t, []string{"./Dockerfile", "FROM scratch\n"}), "Dockerfile"},
		{"双斜杠名字", writeTar(t, []string{"app//main.go", "x"}, []string{"Dockerfile", "FROM scratch\n"}), "Dockerfile"},
		{"子目录 Dockerfile", writeTar(t, []string{"docker/Dockerfile.prod", "FROM scratch\n"}), "docker/Dockerfile.prod"},
		{"点前缀子目录 Dockerfile", writeTar(t, []string{"./docker/Dockerfile", "FROM scratch\n"}), "docker/Dockerfile"},
		{"gzip 形态", writeGzTar(t, []string{"Dockerfile", "FROM scratch\n"}), "Dockerfile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, writeContextFile(t, dir, c.tar))
			if err := scanBuildContext(path, c.docker); err != nil {
				t.Fatalf("合法上下文不应被拒: %v", err)
			}
		})
	}
}

// TestScanBuildContextRequiresDockerfile：Dockerfile 存在性在扫描时一并判定
// （扫描**逐条头**的顺路事实 —— 不另开一条提取路径），缺失给出指名道姓的拒绝。
func TestScanBuildContextRequiresDockerfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, writeContextFile(t, dir, writeTar(t, []string{"app/main.go", "x"})))
	err := scanBuildContext(path, "docker/Dockerfile")
	if err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Fatalf("缺失 Dockerfile 必须被点名拒绝，实际 %v", err)
	}
}

// TestScanBuildContextCapsEntriesAndSize：条目闸与尺寸闸（解压炸弹的两半）。
// 条目闸用包内变量下调测试；尺寸闸用稀疏文件（truncate 不占磁盘）。
func TestScanBuildContextCapsEntriesAndSize(t *testing.T) {
	dir := t.TempDir()
	saved := maxBuildContextEntries
	maxBuildContextEntries = 5
	defer func() { maxBuildContextEntries = saved }()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 6; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: "f" + string(rune('a'+i)), Mode: 0o644, Size: 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, writeContextFile(t, dir, buf.Bytes()))
	if err := scanBuildContext(path, "Dockerfile"); err == nil || !strings.Contains(err.Error(), "条目过多") {
		t.Fatalf("超条目必须被拒，实际 %v", err)
	}

	// 稀疏文件越过 512MB 闸（不真写磁盘）。
	big := filepath.Join(dir, "big.tar")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(agentproto.MaxDockerBuildContextBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := scanBuildContext(big, "Dockerfile"); err == nil || !strings.Contains(err.Error(), "512MB") {
		t.Fatalf("超尺寸必须被拒，实际 %v", err)
	}
}

// buildCmd 编一条 image:build 指令（ctx.tar 落在 transferDir）。
func buildCmd(t *testing.T, dir string, dockerfile, tag string) *agentproto.DockerCmd {
	t.Helper()
	name := writeContextFile(t, dir, minimalContext(t))
	o := agentproto.DockerCmdOptions{Context: name, Tag: tag}
	if dockerfile != "" {
		o.Dockerfile = dockerfile
	}
	return &agentproto.DockerCmd{Ref: buildRef, Action: agentproto.DockerActionImageBuild, Options: o}
}

var buildRef = "1790000000000000017"

// pushCmd 编一条 image:push 指令（可带 4c 凭据）。
func pushCmd(auth *agentproto.DockerRegistryAuth) *agentproto.DockerCmd {
	o := agentproto.DockerCmdOptions{Target: "app:1"}
	if auth != nil {
		o.Registry = auth.Registry
	}
	return &agentproto.DockerCmd{Ref: pushRef, Action: agentproto.DockerActionImagePush, Options: o, Auth: auth}
}

// pushCmdTarget 编一条推送到指定 target 的指令（整仓推送/多 tag 场景用）。
func pushCmdTarget(target string, auth *agentproto.DockerRegistryAuth) *agentproto.DockerCmd {
	o := agentproto.DockerCmdOptions{Target: target}
	if auth != nil {
		o.Registry = auth.Registry
	}
	return &agentproto.DockerCmd{Ref: pushRef, Action: agentproto.DockerActionImagePush, Options: o, Auth: auth}
}

var pushRef = "1790000000000000019"

// TestBuildImageSpecMappingAndConclusion：执行器把 options 定型成 BuildSpec
// （transferDir 拼路径、dockerfile 补缺省、tag 补 :latest、args 原样），成功回
// ok 无载荷；stub 失败回「构建镜像失败」结论句 + daemon 原文进 detail。
func TestBuildImageSpecMappingAndConclusion(t *testing.T) {
	dir := t.TempDir()
	api := &stubAPI{}
	// 7c 起会话管理器是必接依赖：与折叠测试同用 newPullFixture（断言的是
	// spec 映射与结论句，进度帧只是伴随产物）。
	w, _, _, _ := newPullFixture(api)
	w.SetTransferDir(dir)

	cmd := buildCmd(t, dir, "", "app")
	cmd.Options.Args = map[string]string{"NODE_ENV": "prod"}
	if payload, err := w.Do(context.Background(), cmd); err != nil || payload != nil {
		t.Fatalf("成功构建应无载荷无错误: %v %v", payload, err)
	}
	if len(api.built) != 1 {
		t.Fatalf("应恰好一次构建调用: %d", len(api.built))
	}
	spec := api.built[0].spec
	if spec.Dockerfile != "Dockerfile" || spec.Tag != "app:latest" ||
		spec.BuildArgs["NODE_ENV"] != "prod" ||
		filepath.Base(spec.ContextPath) != cmd.Options.Context {
		t.Fatalf("定型映射不符: %+v", spec)
	}
	if !strings.HasPrefix(spec.ContextPath, dir) {
		t.Fatalf("上下文路径必须在 transferDir 内: %s", spec.ContextPath)
	}
	if spec.BuildArgs["NODE_ENV"] == "" {
		t.Fatal("build-args 必须原样传递")
	}

	fail := &stubAPI{buildErr: errors.New("failed to solve: process \"/bin/sh -c exit 1\" did not complete")}
	w2, _, _, _ := newPullFixture(fail)
	w2.SetTransferDir(dir)
	_, err := w2.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "构建镜像失败" ||
		!strings.Contains(ee.Detail, "did not complete") {
		t.Fatalf("失败结论句不符: %v", err)
	}
}

// TestBuildImageContextPathDiscipline：执行器的文件名白名单是**第二道**（协议
// 是第一道，core 可被绕过）—— 路径形态的 context 在 agent 上仍被拒。
func TestBuildImageContextPathDiscipline(t *testing.T) {
	w := NewWriteExecutor(&stubAPI{}, nil, t.TempDir(), "", nil)
	cmd := &agentproto.DockerCmd{Ref: buildRef, Action: agentproto.DockerActionImageBuild,
		Options: agentproto.DockerCmdOptions{Context: "../escape.tar", Tag: "app:1"}}
	_, err := w.Do(context.Background(), cmd)
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "文件名不合法") {
		t.Fatalf("路径形态 context 必须被拒: %v", err)
	}
}

// buildLinesOf 解析一帧里的全部构建进度行。
func buildLinesOf(t *testing.T, f *agentproto.DockerFrame) []agentproto.DockerBuildProgressItem {
	t.Helper()
	var out []agentproto.DockerBuildProgressItem
	for _, raw := range bytes.Split(f.Data, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		var p agentproto.DockerBuildProgressItem
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("帧 data 必须是 JSON 构建进度行: %v (%q)", err, raw)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("构建进度行必须过协议校验: %+v %v", p, err)
		}
		out = append(out, p)
	}
	return out
}

// TestBuildImageProgressFoldsThrottlesAndTerminates：构建进度照 4b 的折叠纪律
// （同 id 取最新、文本行按序保留、跨窗去重），终态 Done 项恰在流末、eof 先于
// result（Do 返回即已发出）。
func TestBuildImageProgressFoldsThrottlesAndTerminates(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 16)
	api := &stubAPI{buildCh: ch}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()

	ch <- BuildProgress{Stream: "#1 [internal] load build definition from Dockerfile"}
	ch <- BuildProgress{ID: "Step 1/2", Status: "FROM node:20"}
	ch <- BuildProgress{ID: "Step 1/2", Status: "FROM node:20"} // 同步骤抖动只留最新
	ch <- BuildProgress{Stream: "#3 [2/2] RUN npm install"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	items := buildLinesOf(t, sink.all()[0])
	if len(items) != 3 {
		t.Fatalf("一帧应 3 条（2 文本行 + 1 步骤折叠），实际 %d: %+v", len(items), items)
	}
	// 折叠批的形状与 4b 一致：消息行（无步骤 id 的文本）按到达顺序在前、
	// 折叠后的步骤行在后 —— 断言的是这个既有形状，而不是到达交错序。
	if items[0].Stream != "#1 [internal] load build definition from Dockerfile" ||
		items[1].Stream != "#3 [2/2] RUN npm install" ||
		items[2].ID != "Step 1/2" || items[2].Status != "FROM node:20" {
		t.Fatalf("折叠后的行不符: %+v", items)
	}

	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("成功构建不得报错: %v", err)
	}
	frames := sink.all()
	last := frames[len(frames)-1]
	if !last.EOF {
		t.Fatal("Do 返回时 eof 帧必须已发出")
	}
	term := buildLinesOf(t, last)
	if len(term) != 1 || !term[0].Done {
		t.Fatalf("终态项必须恰一条且 Done: %+v", term)
	}
	if m.count() != 0 {
		t.Fatalf("会话结束后槽位必须释放: %d", m.count())
	}
}

// TestBuildImageProgressTerminatesWithError：daemon 失败 → 终态 Error 项挂流末
// + 结论句「构建镜像失败」。
func TestBuildImageProgressTerminatesWithError(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	api := &stubAPI{buildCh: ch, buildErr: errors.New("failed to solve: exit code 1")}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#4 RUN ./broken.sh"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	close(ch)
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建镜像失败" {
		t.Fatalf("构建失败结论句不符: %v", err)
	}
	waitFor(t, "终态项", func() bool {
		fs := sink.all()
		return len(fs) > 1 && fs[len(fs)-1].EOF
	})
	term := buildLinesOf(t, sink.all()[len(sink.all())-1])
	if len(term) != 1 || term[0].Error != "failed to solve: exit code 1" || term[0].Done {
		t.Fatalf("终态 Error 项不符: %+v", term)
	}
	if m.count() != 0 {
		t.Fatalf("槽位必须释放: %d", m.count())
	}
}

// TestBuildImageCancelStops：cancel 控制帧（core 按 build_<ref> 句柄下发）→
// 构建即停，结论句「构建已取消」，不再产帧、不发 eof、槽位立刻释放。
func TestBuildImageCancelStops(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	api := &stubAPI{buildCh: ch}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#1 load build definition"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool {
		return m.lookup(agentproto.DockerBuildSessionID(buildRef)) != nil
	})
	before := sink.count()

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("取消结论句不符: %v", err)
	}
	if m.count() != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", m.count())
	}
	time.Sleep(50 * time.Millisecond)
	if sink.count() != before {
		t.Fatalf("取消后不得再产帧: %d -> %d", before, sink.count())
	}
}

// TestBuildImageSessionIDMatchesCoreRegistration：build 会话句柄 = 协议派生
// DockerBuildSessionID（两端同源，core 受理时预登记的就是它）。
func TestBuildImageSessionIDMatchesCoreRegistration(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 4)
	api := &stubAPI{buildCh: ch}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)
	_ = clk
	_ = sink

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#1 load build definition"}
	waitFor(t, "会话建立", func() bool {
		return m.lookup(agentproto.DockerBuildSessionID(buildRef)) != nil
	})
	if m.lookup(agentproto.DockerBuildSessionID(buildRef)) == nil ||
		m.lookup(agentproto.DockerBuildSessionID(buildRef)).kind != streamBuild {
		t.Fatal("构建进度会话的 kind 必须是 streamBuild")
	}
	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("成功构建不得报错: %v", err)
	}
}

// TestPushImageAuthConfigMapping：push 带 4c 凭据时 Auth 三元组**原样**进入
// adapter（stub 记录 ImageAuth —— SDK 的 AuthConfig 映射在 encodeImageAuth，
// 往返测试在 adapter_test）；无凭据的 push 保持 nil。
// （7c 起会话管理器必接：与 pull 的认证测试同用 newPullFixture。）
func TestPushImageAuthConfigMapping(t *testing.T) {
	api := &stubAPI{}
	w, _, _, _ := newPullFixture(api)

	auth := &agentproto.DockerRegistryAuth{Registry: "harbor.example.com", Username: "robot$ci", Password: "s3cret"}
	if _, err := w.Do(context.Background(), pushCmd(auth)); err != nil {
		t.Fatalf("带凭据推送应成功: %v", err)
	}
	if len(api.pushed) != 1 || api.pushed[0].ref != "app:1" {
		t.Fatalf("推送调用不符: %+v", api.pushed)
	}
	got := api.pushed[0].auth
	if got == nil || got.Registry != "harbor.example.com" || got.Username != "robot$ci" || got.Password != "s3cret" {
		t.Fatalf("推送凭据映射不符: %+v", got)
	}

	if _, err := w.Do(context.Background(), pushCmd(nil)); err != nil {
		t.Fatalf("无凭据推送应成功: %v", err)
	}
	if api.pushed[1].auth != nil {
		t.Fatal("无凭据推送的 auth 必须为 nil（与 4b 之前拉取的自由度一致）")
	}
}

// TestPushImageProgressAndConclusion：推送进度照折叠纪律（终态项恰在流末、eof
// 先于 result），失败回「推送镜像失败」。
func TestPushImageProgressAndConclusion(t *testing.T) {
	ch := make(chan PullProgress, 16)
	api := &stubAPI{pushCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()
	_ = done

	ch <- PullProgress{Status: "The push refers to repository [harbor.example.com/app]"}
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 1, Total: 100}
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 2, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	items := pullLinesOf(t, sink.all()[0])
	if len(items) != 2 || items[0].Status != "The push refers to repository [harbor.example.com/app]" ||
		items[1].ID != "aaa" || items[1].Current != 2 {
		t.Fatalf("推送折叠不符: %+v", items)
	}
	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("成功推送不得报错: %v", err)
	}
	frames := sink.all()
	last := frames[len(frames)-1]
	if !last.EOF {
		t.Fatal("Do 返回时 eof 必须已发出")
	}
	term := pullLinesOf(t, last)
	if len(term) != 1 || !term[0].Done {
		t.Fatalf("终态项必须恰一条且 Done: %+v", term)
	}
	if m.count() != 0 {
		t.Fatal("槽位必须释放")
	}

	// 失败结论句（带会话的路径：终态 Error 项 + eof 照发，结论句不变）。
	fail := &stubAPI{pushErr: errors.New("denied: requested access to the resource is denied")}
	w2, _, _, _ := newPullFixture(fail)
	_, err := w2.Do(context.Background(), pushCmd(nil))
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "推送镜像失败" {
		t.Fatalf("推送失败结论句不符: %v", err)
	}
}

// TestPushImageCancelStops：cancel 控制帧（push_<ref> 句柄）→ 推送即停，
// 结论句「推送已取消」。
func TestPushImageCancelStops(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()
	ch <- PullProgress{Status: "Pushing"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool {
		return m.lookup(agentproto.DockerPushSessionID(pushRef)) != nil
	})
	before := sink.count()

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("取消结论句不符: %v", err)
	}
	if m.count() != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", m.count())
	}
	time.Sleep(50 * time.Millisecond)
	if sink.count() != before {
		t.Fatalf("取消后不得再产帧: %d -> %d", before, sink.count())
	}
}

// TestPushAuthNeverLeaksIntoFramesOrResult：密码零泄漏钉（4c 纪律照抄到 push）——
// 凭据只进 adapter 的 AuthConfig 映射（stub 记录在案），进度帧与结果里搜不到
// 密码明文。daemon 错误的原文进帧/进 detail 是 4b 的既有语义（错误文本是
// daemon 写的话），本测试用的是**不含密码**的典型错误原文 —— 零泄漏要证的是
// 「执行器没有任何把凭据写进载荷的路径」，而不是「daemon 永不回声凭据」。
func TestPushAuthNeverLeaksIntoFramesOrResult(t *testing.T) {
	const secret = "push-secret-4c"
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushCh: ch, pushErr: errors.New("denied: requested access to the resource is denied")}
	w, _, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(&agentproto.DockerRegistryAuth{
			Registry: "harbor.example.com", Username: "robot", Password: secret}))
		done <- err
	}()
	ch <- PullProgress{Status: "Pushing"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	close(ch)
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送镜像失败" {
		t.Fatalf("失败结论句不符: %v", err)
	}
	// 结论句与 detail 都不含密码（detail 是 daemon 原文，与凭据无关）。
	if strings.Contains(ee.Msg, secret) || strings.Contains(ee.Detail, secret) {
		t.Fatalf("推送凭据不得泄漏进结论句/detail: %+v", ee)
	}
	for _, f := range sink.all() {
		if strings.Contains(string(f.Data), secret) {
			t.Fatal("推送凭据不得泄漏进进度帧")
		}
	}
	// 凭据确实递到了 adapter（零泄漏与「真递到」是同一枚硬币的两面）。
	if len(api.pushed) != 1 || api.pushed[0].auth == nil || api.pushed[0].auth.Password != secret {
		t.Fatal("凭据必须原样递到 adapter 的映射入口")
	}
}

// TestBuildProgressFrameCarriesNoContextSecrets：构建进度帧只承载 daemon 文本行，
// 不含上下文路径（宿主路径是产物面信息，不进帧）—— 与 4c 的零泄漏同向的复扫。
func TestBuildProgressFrameCarriesNoContextSecrets(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 4)
	api := &stubAPI{buildCh: ch}
	w, _, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#1 build"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	close(ch)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("成功构建不得报错: %v", err)
	}
	for _, f := range sink.all() {
		if strings.Contains(string(f.Data), dir) {
			t.Fatalf("构建进度帧不得含上下文宿主路径: %s", f.Data)
		}
	}
}

// ── 取消/完成的竞态（B4 同款扩面：build / push）──────────────────────────────
//
// 背景与 pull 的 B4 同一场景（cancel 只由显式取消端点下发：POST /cmds/:ref/cancel
// → CancelProgress → cancel 帧 → 会话收摊；断流只停观看）：用户在构建/推送进行中
// 经端点下发取消；若 daemon 恰在同时把活干完，旧实现按「ctx 被中断」把一场**已经
// 完成**的活记成失败（任务中心红字，而产物/清单其实都已落地）。用例把结算规则钉死：
// 完成的事实优先于迟到的取消，且**只有**各自的完成证据能翻案（防止顺手把真取消也
// 放行）。

// build 的完成证据：目标 tag 的本机镜像 ID 变了（构建前后各问一次 ImageRefID）——
// 独立于流的事实，产物从无到有（空串 → 非空）也算变。
func TestBuildImageCancelAfterProductLandedStaysSucceeded(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	// 观测序列：构建前（第一问）= 目标 tag 本机没有；取消结算时（第二问）= 产物已落地。
	api := &stubAPI{buildCh: ch, imageRefIDs: []string{"", "sha256:built"}}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()

	ch <- BuildProgress{Stream: "#6 exporting layers"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	// 用户经显式取消端点下发 cancel（core 端点 → cancel 帧）→ 会话收摊 → buildCtx
	// 被中断，ImageBuild 以 ctx 错误返回（替身与 SDK 同款）。
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("产物已落地（目标 tag 从无到有）必须按成功结算，实际: %v", err)
	}
	if got := m.count(); got != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", got)
	}
	if len(api.imageRefCalls) != 2 {
		t.Fatalf("完成判据必须做构建前后两次对照，实际 %d 次: %v", len(api.imageRefCalls), api.imageRefCalls)
	}
}

// 反向守卫：重构建一个**本机已有**的 tag、被取消且 ID 没变 —— 不得因为「现在有这个
// tag」就说成功（那是反向的不诚实：这次构建的产物并没有兑现）。
func TestBuildImageCancelWithPreexistingTagStaysCanceled(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	api := &stubAPI{buildCh: ch, imageRefIDs: []string{"sha256:old1", "sha256:old1"}}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()

	ch <- BuildProgress{Stream: "#2 [1/2] RUN make"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("tag 没变（旧产物仍在）时必须记取消，实际: %v", err)
	}
}

// 反向守卫：构建前查不了（daemon 抖动）= 没有对照基线 —— 单看「现在有这个 tag」不可
// 作判据（那可能是本来就有的），落回「取消」而不是编一条完成。
func TestBuildImageCancelWithUnknownBaselineStaysCanceled(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	api := &stubAPI{buildCh: ch, imageRefErr: errors.New("daemon busy"), imageRefIDs: []string{"sha256:any"}}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()

	ch <- BuildProgress{Stream: "#2 [1/2] RUN make"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("没有构建前基线时不得凭「现在有 tag」判完成，实际: %v", err)
	}
}

// push 的完成证据一：daemon 的收尾行（"<tag>: digest: …" = 清单已提交进 registry）
// 已经到达读循环 —— 此后到达的 cancel 是 no-op，终态按完成结算。顺带钉住短路：
// 收尾行已到时**不再**问第二问（本机 digest 对照），结算不多花一次 daemon 调用。
func TestPushImageCancelAfterDaemonCompletedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushCh: ch}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()

	ch <- PullProgress{Status: "The push refers to repository [harbor.example.com/app]"}
	ch <- PullProgress{ID: "aaa", Status: "Pushed"}
	ch <- PullProgress{Status: "1: digest: sha256:deadbeef size: 1234"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("daemon 已完成的推送在迟到 cancel 后必须按成功结算，实际: %v", err)
	}
	if got := m.count(); got != 0 {
		t.Fatalf("取消必须立刻释放槽位: %d", got)
	}
	if len(api.repoDigestCalls) != 1 {
		t.Fatalf("收尾行已到即已结算，不应再问本机 digest 对照（只该有一次推送前基线），实际 %d 次: %v",
			len(api.repoDigestCalls), api.repoDigestCalls)
	}
}

// push 的完成证据二：收尾行随中断的连接一起丢了（ctx 取消直接关连接，缓冲里的尾数据
// 不复存在）—— 用独立于流的事实补判：推送前后本机给这个镜像记的 canonical 引用
// 多了一条（daemon 在清单提交成功后落的笔）。
func TestPushImageCancelAfterDigestRefLandedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	// 观测序列：推送前（第一问）= 只有上次留下的旧 digest 引用；结算时（第二问）= 多了一条。
	api := &stubAPI{pushCh: ch, repoDigests: [][]string{
		{"app@sha256:old"},
		{"app@sha256:old", "app@sha256:new"},
	}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 90, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("本机 digest 引用多了一条（清单已提交的本地痕迹）必须按成功结算，实际: %v", err)
	}
	if len(api.repoDigestCalls) != 2 {
		t.Fatalf("完成判据必须做推送前后两次对照，实际 %d 次: %v", len(api.repoDigestCalls), api.repoDigestCalls)
	}
}

// 反向守卫：重推**同一份内容**（daemon 不会再落一笔 digest 引用）、被取消且引用集合
// 没变 —— 不得因为「本机有 digest 引用」就说成功（那是反向的不诚实：重推并没有兑现）；
// 收尾行没到（被截断）时落回「取消」。
func TestPushImageCancelWithUnchangedDigestRefsStaysCanceled(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushCh: ch, repoDigests: [][]string{
		{"app@sha256:same"},
		{"app@sha256:same"},
	}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 10, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("digest 引用没变（可能是上次留下的）时必须记取消，实际: %v", err)
	}
}

// 反向守卫：推送前查不了（daemon 抖动）= 没有对照基线 —— 单看「现在有 digest 引用」
// 不可作判据（那可能是本来就有的），落回「取消」而不是编一条完成。
func TestPushImageCancelWithUnknownDigestBaselineStaysCanceled(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushCh: ch, repoDigestErr: errors.New("daemon busy"), repoDigests: [][]string{{"app@sha256:any"}}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()

	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 10, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("没有推送前基线时不得凭「现在有 digest 引用」判完成，实际: %v", err)
	}
}

// isPushCompletionLine 是推送完成判据的**唯一**解析口（纯函数，逐条钉住形态）：
// 只认无层 id 的 "<tag>: digest: …" 消息行 —— 带 id 的层行（"Pushed" /
// "Layer already exists" 整场都在发）与起手消息行都不算，pull 的大写 "Digest:" 行
// 与推送的收尾行形近（都含 sha256）也刻意不误命中。
func TestIsPushCompletionLine(t *testing.T) {
	cases := []struct {
		name string
		in   PullProgress
		want bool
	}{
		{"daemon 的收尾行", PullProgress{Status: "latest: digest: sha256:abc size: 1234"}, true},
		{"无 tag 推送时按 tag 名发的收尾行", PullProgress{Status: "v1.2: digest: sha256:abc size: 9"}, true},
		{"pull 的大写 Digest 行不算", PullProgress{Status: "Digest: sha256:abc"}, false},
		{"层行不算（即使状态含 digest）", PullProgress{ID: "aaa", Status: "latest: digest: sha256:abc size: 1"}, false},
		{"层行的 Pushed 不算", PullProgress{ID: "aaa", Status: "Pushed"}, false},
		{"Layer already exists 不算", PullProgress{ID: "aaa", Status: "Layer already exists"}, false},
		{"起手消息行不算", PullProgress{Status: "The push refers to repository [harbor.example.com/app]"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPushCompletionLine(c.in); got != c.want {
				t.Fatalf("isPushCompletionLine(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// （原 TestBuildPushWithoutSessionsFallsBackToBlackBox 已随 7c 删除：它钉住的
// 「流通道未装配 = 黑盒快路径」分支已从 buildImage/pushImage 删除，构造契约改为
// 必须非 nil（见 write.go 的 SetSessions 与 pull_progress_test 的
// TestSetSessionsNilFailsFast —— 旧守卫对象不存在，新守卫钉 fail fast）。）

// ── 结算的 2026 收口版（判据链 / 逐 tag 计数 / 证据三）────────────────────────
//
// 上面那批 B4 用例钉的是「迟到 cancel 不得改写成取消」；这批钉的是**判据本身**：
// 换了什么、为什么换、反向病例（不许把没完成的读成完成）逐条在此。

// buildFixtureAPI 造一个「真机流夹具 + 观测序列」的构建替身：
//   - fixture 走 adapter 的同一解析器（生产接线逐字相同）；
//   - refs 是 ImageRefID 的观测序列（开工前一次、结算时一次）。
func buildFixtureAPI(fixture string, refs ...string) *stubAPI {
	return &stubAPI{buildFixture: fixture, imageRefIDs: refs}
}

// TestBuildImageAuxIdDecidesCacheHitAsSucceeded：**全缓存命中**的构建 —— 产物 ID
// 与开工前逐字节相同（tag 早就指着同一个镜像），旧判据（ID 变了）开不了口。
// aux 行的产物 ID 与「tag 现在指向谁」一对照即成立：tag 指向本场产物 = 本场完成。
func TestBuildImageAuxIdDecidesCacheHitAsSucceeded(t *testing.T) {
	dir := t.TempDir()
	const id = "sha256:ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715"
	api := buildFixtureAPI(buildStreamLegacyCacheHit, id, id)
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "cap-c:1"))
		done <- err
	}()
	// 夹具放完 = daemon 干完活；等会话建立（帧已出）再下 cancel。
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("全缓存命中（产物 ID 未变但 tag 指向本场产物）必须按成功结算，实际: %v", err)
	}
	if len(api.imageRefCalls) != 2 {
		t.Fatalf("判据必须做开工前/结算两次对照，实际 %d 次: %v", len(api.imageRefCalls), api.imageRefCalls)
	}
}

// 反向守卫：aux 读数到手但 **tag 指向别处**（本场产物没打上去）—— 必须记取消。
// 这条挡住「知道构建出过东西 ≠ 本场承诺兑现」的偷换：判据链在产物 ID 已知时
// 只认「tag 指向它」，不再看别的证据。
func TestBuildImageAuxIdButTagPointsElsewhereStaysCanceled(t *testing.T) {
	dir := t.TempDir()
	api := buildFixtureAPI(buildStreamLegacyFresh,
		"sha256:401d0eb0befded5c5c0000000000000000000000000000000000000000000000",
		"sha256:401d0eb0befded5c5c0000000000000000000000000000000000000000000000")
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "cap-b:1"))
		done <- err
	}()
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("tag 没落到本场产物上时必须记取消，实际: %v", err)
	}
}

// TestBuildImageKitFixtureDecidesCacheHitAsSucceeded：BuildKit 夹具（产物 ID 只在
// moby.image.id 的 aux 行里）走同一条判据链 —— 两代 builder 的读数在结算侧没有分叉。
func TestBuildImageKitFixtureDecidesCacheHitAsSucceeded(t *testing.T) {
	dir := t.TempDir()
	const id = "sha256:3b7ada8d7f83cbcfcf1f93639ecead46e139e05260b32889b79a0e9392a4d4ea"
	api := buildFixtureAPI(buildStreamBuildKit, id, id)
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "cap-a:1"))
		done <- err
	}()
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("BuildKit 形态必须走同一判据链（产物 ID == tag 指向）: %v", err)
	}
}

// TestBuildImageAuxReadingNeverEntersFrames：aux 的产物读数是判据、不是进度 ——
// 它不得进帧面（帧里既搜不到产物 ID，也不该多出空行）。
func TestBuildImageAuxReadingNeverEntersFrames(t *testing.T) {
	dir := t.TempDir()
	api := buildFixtureAPI(buildStreamLegacyFresh, "", "")
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "cap-b:1"))
		done <- err
	}()
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	_ = pullResultOf(t, done) // 结局不是本用例关心的（只看帧面）

	for _, f := range sink.all() {
		// 只针对 aux 携带的**完整**产物 ID：daemon 自己的文本行里带着 12 位截断形态
		//（" ---> ac8fcc79148b" / "Successfully built ac8fcc79148b"）是合法进度，不算泄漏。
		if strings.Contains(string(f.Data), "ac8fcc79148b3e73c2efd278a4a6b91cb8d23dcaf9d932a69585108fadd58715") {
			t.Fatalf("aux 的产物 ID 不得进帧面: %s", f.Data)
		}
		for _, item := range buildLinesOf(t, f) {
			if !item.Done && item.Error == "" && item.ID == "" && item.Status == "" && item.Stream == "" {
				t.Fatalf("帧里不得有空行（aux 读数漏进帧面的形态）: %+v", item)
			}
		}
	}
}

// TestBuildImageCompletionLineLostWithTagEventStaysSucceeded：收尾行随连接丢了
// （只有进度通道、没有 aux 读数）、产物 ID 与开工前相同（全缓存命中）——
// 唯一剩下的事实是 daemon 的 tag 事件：窗口内这个 tag 被（重新）打过，且写进去的
// 就是它现在指向的镜像。
func TestBuildImageCompletionLineLostWithTagEventStaysSucceeded(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	const id = "sha256:cached0000000000000000000000000000000000000000000000000000000000"
	api := &stubAPI{
		buildCh:     ch,
		imageRefIDs: []string{id, id}, // 全缓存命中：tag 指向的镜像没变
		tagEvents: []EventItem{
			{Type: "image", Action: "tag", ActorName: "app:1", ActorID: id},
		},
	}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#8 exporting layers done"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("窗口内该 tag 被写过（缓存命中的重打 tag 也会发事件）必须按成功结算: %v", err)
	}
	if api.tagEventCalls != 1 {
		t.Fatalf("tag 事件回放必须恰好问一次，实际 %d", api.tagEventCalls)
	}
}

// 反向守卫：窗口内的 tag 事件**写的是别的镜像** —— 不得说成功（那可能是别人重打
// 的 tag，与本场构建无关）。
func TestBuildImageTagEventForOtherImageStaysCanceled(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	const id = "sha256:cached0000000000000000000000000000000000000000000000000000000000"
	api := &stubAPI{
		buildCh:     ch,
		imageRefIDs: []string{id, id},
		tagEvents: []EventItem{
			{Type: "image", Action: "tag", ActorName: "app:1", ActorID: "sha256:somebodyelse"},
		},
	}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#8 exporting layers done"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("tag 事件写的是别的镜像时必须记取消，实际: %v", err)
	}
}

// 反向守卫：tag 事件读不到（回放失败）→ 该证据开不了口，落回取消。
func TestBuildImageTagEventQueryFailsStaysCanceled(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	const id = "sha256:cached0000000000000000000000000000000000000000000000000000000000"
	api := &stubAPI{
		buildCh:     ch,
		imageRefIDs: []string{id, id},
		tagEventErr: errors.New("events log unavailable"),
	}
	w, m, clk, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#8 exporting layers done"}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerBuildSessionID(buildRef), Op: agentproto.DockerFrameOpCancel,
	})
	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "构建已取消" {
		t.Fatalf("证据读不到时必须落回取消（不许猜）: %v", err)
	}
}

// TestBuildImageDeadlineAfterProductLandedStaysSucceeded：**真截止**（执行时限到点、
// 会话仍在）与迟到 cancel 同一条纪律 —— 活干完了就记完成，终态项与 eof 照发
// （页面不会看到一条没有终态的流）。
func TestBuildImageDeadlineAfterProductLandedStaysSucceeded(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan BuildProgress, 8)
	api := &stubAPI{
		buildCh:     ch,
		buildErr:    errors.New("context deadline exceeded"),
		imageRefIDs: []string{"", "sha256:built"},
	}
	w, _, _, sink := newPullFixture(api)
	w.SetTransferDir(dir)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), buildCmd(t, dir, "", "app:1"))
		done <- err
	}()
	ch <- BuildProgress{Stream: "#6 exporting layers"}
	close(ch) // 通道关闭 = 构建以 buildErr 收场（会话还在）
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("产物已落地时真截止也必须按完成结算: %v", err)
	}
	fs := sink.all()
	if len(fs) == 0 || !fs[len(fs)-1].EOF {
		t.Fatal("完成档必须补发终态项与 eof（否则页面看到一条没有终态的流）")
	}
	term := buildLinesOf(t, fs[len(fs)-1])
	if last := term[len(term)-1]; !last.Done {
		t.Fatalf("终态项必须恰在流末且 Done: %+v", term)
	}
}

// pushFixture 造一个「本机有 N 个 tag + 可指定 registry 策略」的推送替身。
func pushFixture(api *stubAPI, ch chan PullProgress) (*WriteExecutor, *SessionManager, *fakeClock, *frameSink) {
	api.pushCh = ch
	return newPullFixture(api)
}

// cancelPush 等会话建立后下发 cancel（推送取消的唯一入口 = core 显式取消端点；本
// 函数直接投递该端点会下发的 cancel 帧）。
func cancelPush(t *testing.T, m *SessionManager, clk *fakeClock, sink *frameSink) {
	t.Helper()
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool {
		return m.lookup(agentproto.DockerPushSessionID(pushRef)) != nil && sink.count() >= 1
	})
	m.OnFrame(&agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPushSessionID(pushRef), Op: agentproto.DockerFrameOpCancel,
	})
}

// TestPushImageUntaggedPartialLandedIsHonest：**整仓推送**（target 不带 tag）的
// 部分兑现 —— daemon 逐个 tag 推，只有第一个 tag 的收尾行到了就被 cancel：
// 结论必须如实说「1/3 已推送」，而不是笼统的「已取消」（旧形态见首行即判完成，
// 会把这一场读成成功 —— 两个方向都不对）。
func TestPushImageUntaggedPartialLandedIsHonest(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{images: []ImageInfo{
		{RepoTags: []string{"app:1", "app:2", "app:3"}},
	}}
	w, m, clk, sink := pushFixture(api, ch)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget("app", nil))
		done <- err
	}()
	ch <- PullProgress{Status: "The push refers to repository [docker.io/library/app]"}
	ch <- PullProgress{Status: "1: digest: sha256:aaa size: 476"}
	cancelPush(t, m, clk, sink)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "部分 tag 已推送（1/3）" {
		t.Fatalf("部分兑现必须如实计数，实际: %v", err)
	}
}

// TestPushImageUntaggedAllTagsLandedStaysSucceeded：三个 tag 的收尾行都到了 ——
// 全部兑现 = 完成（迟到 cancel 是 no-op）。
func TestPushImageUntaggedAllTagsLandedStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{images: []ImageInfo{
		{RepoTags: []string{"app:1", "app:2", "app:3"}},
	}}
	w, m, clk, sink := pushFixture(api, ch)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget("app", nil))
		done <- err
	}()
	for _, tag := range []string{"1", "2", "3"} {
		ch <- PullProgress{Status: tag + ": digest: sha256:aaa size: 476"}
	}
	cancelPush(t, m, clk, sink)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("三个 tag 全部兑现必须按成功结算: %v", err)
	}
}

// TestPushImageEvidenceThreeRescuesLostCompletionLine：**证据三端到端** ——
// 收尾行一条都没到（模拟随连接丢掉的尾数据），但 registry 侧探测显示该 tag 的
// manifest 从无到有：这场推送确实落 registry 了，必须按成功结算。
//
// 这也是 containerd 存储上唯一能开口的那条（RepoDigests 由本地 tag 合成，
// 证据二在那里恒不开火）。
func TestPushImageEvidenceThreeRescuesLostCompletionLine(t *testing.T) {
	f := newFakeRegistry(t)
	ch := make(chan PullProgress, 8)
	api := probeAPI()
	api.pushCh = ch
	target := f.host() + "/app:1"
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget(target, nil))
		done <- err
	}()
	// 先等基线探完再改 registry 状态：首帧出现 ⇒ ImagePush 已在跑 ⇒ 基线（在
	// ImagePush 之前同步完成）必已落地 —— 否则会撞上「基线看到的就是新 digest」
	// 的竞态，用例会闪。
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 90, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	// 推送「执行期间」registry 被写入了 —— 真机上的时序就是如此。
	f.set("app", "1", "sha256:landed")
	cancelPush(t, m, clk, sink)

	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("registry 侧从无到有 = 这场推送已落 registry，必须按成功结算: %v", err)
	}
}

// 反向守卫：registry 侧的 digest **没变**（上次留下的 tag 还在那儿）——
// 不得凭「现在 registry 上有这个 tag」说成功（反向的不诚实：重推可能被截止了）。
func TestPushImageEvidenceThreeUnchangedDigestStaysCanceled(t *testing.T) {
	f := newFakeRegistry(t)
	f.set("app", "1", "sha256:same")
	ch := make(chan PullProgress, 8)
	api := probeAPI()
	api.pushCh = ch
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget(f.host()+"/app:1", nil))
		done <- err
	}()
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 10, Total: 100}
	cancelPush(t, m, clk, sink)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("registry 内容没变时必须记取消，实际: %v", err)
	}
}

// TestPushImageEvidenceThreeCountsPartial：整仓推送 + 收尾行全丢 —— 计数交给
// registry 侧探测：三个 tag 里两个落了 → 「2/3」。
func TestPushImageEvidenceThreeCountsPartial(t *testing.T) {
	f := newFakeRegistry(t)
	ch := make(chan PullProgress, 8)
	api := probeAPI()
	api.pushCh = ch
	// 本机 tag 是**带仓库限定**的形态（真实形态：推 127.0.0.1:PORT/app 的机器上，
	// 本地 tag 就是 127.0.0.1:PORT/app:1）。
	api.images = []ImageInfo{{RepoTags: []string{
		f.host() + "/app:1", f.host() + "/app:2", f.host() + "/app:3",
	}}}
	w, m, clk, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget(f.host()+"/app", nil))
		done <- err
	}()
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 10, Total: 100}
	advanceUntil(t, clk, 50*time.Millisecond, 400*time.Millisecond, func() bool { return sink.count() >= 1 })
	// 推送期间：1 与 2 落 registry，3 没赶上（基线已探完，见上一条用例的口径）。
	f.set("app", "1", "sha256:a")
	f.set("app", "2", "sha256:b")
	cancelPush(t, m, clk, sink)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "部分 tag 已推送（2/3）" {
		t.Fatalf("registry 侧计数不符，实际: %v", err)
	}
}

// TestPushImageRegistryUnreachableStaysCanceled：探不动 registry（端口没人听）——
// 该证据开不了口，落回取消（宁可漏救，不编一条完成）。
func TestPushImageRegistryUnreachableStaysCanceled(t *testing.T) {
	// 占一个端口再立刻关掉：拿到一个「刚被释放」的端口号（连接必被拒）。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadHost := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	ch := make(chan PullProgress, 8)
	api := probeAPI()
	api.pushCh = ch
	w, m, clk, sink := newPullFixture(api)
	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget(deadHost+"/app:1", nil))
		done <- err
	}()
	ch <- PullProgress{ID: "aaa", Status: "Pushing", Current: 10, Total: 100}
	cancelPush(t, m, clk, sink)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("探不动 registry 时必须落回取消，实际: %v", err)
	}
}

// 反向守卫：应推集合不可考（本机镜像清单读不到）—— **不结算**（N 无从谈起，
// 任何 M==N 都是编的），即使收尾行到了也落取消。
func TestPushImageTagSetUnknownStaysCanceled(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{imagesErr: errors.New("daemon busy")}
	w, m, clk, sink := pushFixture(api, ch)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget("app", nil))
		done <- err
	}()
	ch <- PullProgress{Status: "1: digest: sha256:aaa size: 476"}
	cancelPush(t, m, clk, sink)

	var ee *ExecError
	if err := pullResultOf(t, done); !errors.As(err, &ee) || ee.Msg != "推送已取消" {
		t.Fatalf("应推集合不可考时必须退回保守，实际: %v", err)
	}
}

// TestPushImageUnknownTagLandedGrowsDenominator：daemon 在推送那一刻多枚举到一个
// tag（我方快照里没有）—— 它的收尾行到过即已落，必须进分母，否则会出现
// M > N 的自相矛盾结论。
func TestPushImageUnknownTagLandedGrowsDenominator(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{images: []ImageInfo{{RepoTags: []string{"app:1"}}}}
	w, m, clk, sink := pushFixture(api, ch)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmdTarget("app", nil))
		done <- err
	}()
	ch <- PullProgress{Status: "1: digest: sha256:aaa size: 476"}
	ch <- PullProgress{Status: "9: digest: sha256:bbb size: 476"}
	cancelPush(t, m, clk, sink)
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("多出来的已落 tag 必须进分母（M==N 即完成）: %v", err)
	}
}

// TestPushImageDeadlineAfterFullLandingStaysSucceeded：真截止落在全量兑现之后 ——
// 完成事实优先，终态 Done 项与 eof 照发。
func TestPushImageDeadlineAfterFullLandingStaysSucceeded(t *testing.T) {
	ch := make(chan PullProgress, 8)
	api := &stubAPI{pushErr: errors.New("context deadline exceeded"), pushCh: ch}
	w, _, _, sink := newPullFixture(api)

	done := make(chan error, 1)
	go func() {
		_, err := w.Do(context.Background(), pushCmd(nil))
		done <- err
	}()
	ch <- PullProgress{Status: "1: digest: sha256:aaa size: 476"}
	close(ch) // 会话还在、以 pushErr 收场（真截止）
	if err := pullResultOf(t, done); err != nil {
		t.Fatalf("全量兑现后真截止必须按完成结算: %v", err)
	}
	fs := sink.all()
	if len(fs) == 0 || !fs[len(fs)-1].EOF {
		t.Fatal("完成档必须补发终态项与 eof")
	}
	term := pullLinesOf(t, fs[len(fs)-1])
	if last := term[len(term)-1]; !last.Done {
		t.Fatalf("终态项必须恰在流末且 Done: %+v", term)
	}
}
