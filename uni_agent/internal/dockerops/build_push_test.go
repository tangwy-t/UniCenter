package dockerops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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

// （原 TestBuildPushWithoutSessionsFallsBackToBlackBox 已随 7c 删除：它钉住的
// 「流通道未装配 = 黑盒快路径」分支已从 buildImage/pushImage 删除，构造契约改为
// 必须非 nil（见 write.go 的 SetSessions 与 pull_progress_test 的
// TestSetSessionsNilFailsFast —— 旧守卫对象不存在，新守卫钉 fail fast）。）
