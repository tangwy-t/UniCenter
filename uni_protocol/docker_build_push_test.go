package agentproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── P2·分发闭环（image:build / image:push）的协议面测试 ───────────────────
//
// 覆盖四个面：options 逐字段白名单（正反例）、字段归属（build 专属字段与 registry
// 的扩展归属）、Auth 三元组的扩展归属（pull|push）、进度记录载荷与派生会话句柄。

// bOpts 构造带 context/tag 的合法 build options（其余字段按需补）。
func bOpts(mut func(*DockerCmdOptions)) *DockerCmdOptions {
	o := &DockerCmdOptions{Context: "app.tar", Tag: "app:1"}
	if mut != nil {
		mut(o)
	}
	return o
}

func TestDockerBuildOptionValidation(t *testing.T) {
	cases := []struct {
		name   string
		opts   *DockerCmdOptions
		action string
		ok     bool
	}{
		{"最小合法", bOpts(nil), DockerActionImageBuild, true},
		{"缺 context", &DockerCmdOptions{Tag: "app:1"}, DockerActionImageBuild, false},
		{"缺 tag", &DockerCmdOptions{Context: "app.tar"}, DockerActionImageBuild, false},
		{"dockerfile 合法子目录", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "docker/Dockerfile.prod" }), DockerActionImageBuild, true},
		{"dockerfile 缺省形态留空", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "" }), DockerActionImageBuild, true},
		{"tag 带 registry 与 digest", bOpts(func(o *DockerCmdOptions) {
			o.Tag = "harbor.example.com:8443/team/app@sha256:" + strings.Repeat("a", 64)
		}), DockerActionImageBuild, true},
		{"args 合法", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"NODE_ENV": "prod", "_K": "v", "A1": ""} }), DockerActionImageBuild, true},
		{"args 空值合法（ARG X= 的 CLI 同义）", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"EMPTY": ""} }), DockerActionImageBuild, true},

		// ── context 反例：路径逃逸形态在协议层必须出不去 ──────────────
		{"context 带路径", bOpts(func(o *DockerCmdOptions) { o.Context = "dir/app.tar" }), DockerActionImageBuild, false},
		{"context 反向穿越", bOpts(func(o *DockerCmdOptions) { o.Context = "../app.tar" }), DockerActionImageBuild, false},
		{"context 绝对路径", bOpts(func(o *DockerCmdOptions) { o.Context = "/tmp/app.tar" }), DockerActionImageBuild, false},
		{"context 后缀不合法", bOpts(func(o *DockerCmdOptions) { o.Context = "app.zip" }), DockerActionImageBuild, false},
		{"context 空名", bOpts(func(o *DockerCmdOptions) { o.Context = ".tar" }), DockerActionImageBuild, false},
		{"context tgz", bOpts(func(o *DockerCmdOptions) { o.Context = "app.tgz" }), DockerActionImageBuild, true},
		{"context tar.gz", bOpts(func(o *DockerCmdOptions) { o.Context = "webapp.src.tar.gz" }), DockerActionImageBuild, true},
		{"context 大写后缀不给", bOpts(func(o *DockerCmdOptions) { o.Context = "APP.TAR" }), DockerActionImageBuild, false},

		// ── dockerfile 反例：段白名单拦逃逸 ────────────────────────────
		{"dockerfile 反向穿越", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "../Dockerfile" }), DockerActionImageBuild, false},
		{"dockerfile 中段穿越", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "a/../Dockerfile" }), DockerActionImageBuild, false},
		{"dockerfile 绝对路径", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "/etc/Dockerfile" }), DockerActionImageBuild, false},
		{"dockerfile 反斜杠", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = `a\b` }), DockerActionImageBuild, false},
		{"dockerfile 空段", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "a//b" }), DockerActionImageBuild, false},
		{"dockerfile 点段", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "./Dockerfile" }), DockerActionImageBuild, false},
		{"dockerfile 非法字符", bOpts(func(o *DockerCmdOptions) { o.Dockerfile = "docker file" }), DockerActionImageBuild, false},

		// ── tag 反例 ──────────────────────────────────────────────────
		{"tag 含空白", bOpts(func(o *DockerCmdOptions) { o.Tag = "app 1" }), DockerActionImageBuild, false},
		{"tag 含 NUL", bOpts(func(o *DockerCmdOptions) { o.Tag = "app\x00" }), DockerActionImageBuild, false},

		// ── args 反例 ─────────────────────────────────────────────────
		{"args 键数字开头", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"1A": "v"} }), DockerActionImageBuild, false},
		{"args 键含横线", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"A-B": "v"} }), DockerActionImageBuild, false},
		{"args 值含换行", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"A": "v\nv"} }), DockerActionImageBuild, false},
		{"args 值超长", bOpts(func(o *DockerCmdOptions) { o.Args = map[string]string{"A": strings.Repeat("v", 513)} }), DockerActionImageBuild, false},
		{"args 条目超限", bOpts(func(o *DockerCmdOptions) {
			m := map[string]string{}
			for i := 0; i < 33; i++ {
				m["K"+string(rune('a'+i%26))+string(rune('a'+i))] = "v"
			}
			o.Args = m
		}), DockerActionImageBuild, false},

		// ── build 没有 target（与 create 同型）─────────────────────────
		{"build 带 target 被拒", bOpts(func(o *DockerCmdOptions) { o.Target = "something" }), DockerActionImageBuild, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDockerCmdOptions(c.action, c.opts)
			if c.ok && err != nil {
				t.Fatalf("应放行，实际被拒: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("应被拒，实际放行")
			}
		})
	}
}

// TestDockerBuildFieldsRejectedOnOtherActions：build 专属字段挂在别的 action 上
// 是字段归属错误（照 create 专属字段的先例）—— 显式拒绝而不是静默当作载荷噪音。
func TestDockerBuildFieldsRejectedOnOtherActions(t *testing.T) {
	pull := &DockerCmdOptions{Target: "nginx:latest", Context: "app.tar"}
	push := &DockerCmdOptions{Target: "app:1", Dockerfile: "Dockerfile"}
	create := &DockerCmdOptions{Image: "app:1", Tag: "other:1"}
	tag := &DockerCmdOptions{Src: "a", Dst: "b", Args: map[string]string{"A": "B"}}
	for _, c := range []struct {
		action string
		opts   *DockerCmdOptions
	}{
		{DockerActionImagePull, pull},
		{DockerActionImagePush, push},
		{DockerActionContainerCreate, create},
		{DockerActionImageTag, tag},
	} {
		if err := ValidateDockerCmdOptions(c.action, c.opts); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("%s 挂上 build 专属字段必须被拒，实际 %v", c.action, err)
		}
	}
}

func TestDockerPushOptionValidation(t *testing.T) {
	cases := []struct {
		name   string
		opts   *DockerCmdOptions
		action string
		ok     bool
	}{
		{"最小合法", &DockerCmdOptions{Target: "app:1"}, DockerActionImagePush, true},
		{"digest 形态", &DockerCmdOptions{Target: "harbor.example.com/x@sha256:" + strings.Repeat("b", 64)}, DockerActionImagePush, true},
		{"带 registry 凭据键", &DockerCmdOptions{Target: "app:1", Registry: "harbor.example.com:8443"}, DockerActionImagePush, true},
		{"缺 target", &DockerCmdOptions{}, DockerActionImagePush, false},
		{"target 含空白", &DockerCmdOptions{Target: "app 1"}, DockerActionImagePush, false},
		{"registry 带协议头", &DockerCmdOptions{Target: "app:1", Registry: "https://harbor.example.com"}, DockerActionImagePush, false},
		{"registry 带镜像路径", &DockerCmdOptions{Target: "app:1", Registry: "harbor.example.com/app"}, DockerActionImagePush, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDockerCmdOptions(c.action, c.opts)
			if c.ok && err != nil {
				t.Fatalf("应放行，实际被拒: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("应被拒，实际放行")
			}
		})
	}
}

// TestDockerRegistryFieldOwnershipExtendedToPush：registry 的归属从 image:pull
// 扩到 image:pull|image:push（P2），其余 action 上的归属拒绝维持不变 ——
// 包括 build（构建不用 4c 凭据面：基础镜像的拉取走宿主侧 docker login 或
// 公共仓库，与 4b 之前 pull 的自由度一致）。
func TestDockerRegistryFieldOwnershipExtendedToPush(t *testing.T) {
	good := &DockerCmdOptions{Target: "app:1", Registry: "harbor.example.com"}
	if err := ValidateDockerCmdOptions(DockerActionImagePush, good); err != nil {
		t.Fatalf("push 带 registry 应放行: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionImagePull, good); err != nil {
		t.Fatalf("pull 带 registry 应照旧放行: %v", err)
	}
	for _, c := range []struct {
		action string
		opts   *DockerCmdOptions
	}{
		{DockerActionImageBuild, bOpts(func(o *DockerCmdOptions) { o.Registry = "harbor.example.com" })},
		{DockerActionImageTag, &DockerCmdOptions{Src: "a", Dst: "b", Registry: "h"}},
		{DockerActionContainerCreate, &DockerCmdOptions{Image: "a:1", Registry: "h"}},
	} {
		if err := ValidateDockerCmdOptions(c.action, c.opts); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("%s 带 registry 必须被拒，实际 %v", c.action, err)
		}
	}
}

// TestDockerBuildPushAuthOwnership：Auth 三元组的归属与 registry 字段同步扩到
// image:push —— registry 与 auth.registry 必须是同一把键（4c 的不漂移纪律）。
func TestDockerBuildPushAuthOwnership(t *testing.T) {
	pushAuth := &DockerRegistryAuth{Registry: "harbor.example.com", Username: "u", Password: "p"}
	ok := func() *DockerCmd {
		return &DockerCmd{Ref: "1700000000001", Action: DockerActionImagePush,
			Options: DockerCmdOptions{Target: "app:1", Registry: "harbor.example.com"}, Auth: pushAuth}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("push 带匹配 registry 的 auth 应通过: %v", err)
	}
	drift := ok()
	drift.Auth = &DockerRegistryAuth{Registry: "other.example.com", Username: "u", Password: "p"}
	if err := drift.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("auth.registry 与 options.registry 漂移必须被拒: %v", err)
	}
	badAction := ok()
	badAction.Action = DockerActionImageBuild
	badAction.Options = DockerCmdOptions{Context: "app.tar", Tag: "app:1"}
	if err := badAction.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("auth 挂在 build 上必须被拒: %v", err)
	}
	badAction.Action = DockerActionImagePull
	badAction.Options = DockerCmdOptions{Target: "nginx:latest"}
	if err := badAction.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("auth 挂在 pull（无 registry 键）上必须被拒: %v", err)
	}
}

// TestDockerBuildPushConfirmIsStandard：build/push 都是标准确认档 ——
// 既不需要照抄目标，也不需要固定文本。
func TestDockerBuildPushConfirmIsStandard(t *testing.T) {
	for _, action := range []string{DockerActionImageBuild, DockerActionImagePush} {
		if want := ExpectedDockerConfirm(action, bOpts(nil)); want != "" {
			t.Fatalf("%s 应是标准档（无需确认值），实际要求 %q", action, want)
		}
		if err := CheckDockerConfirm(action, bOpts(nil), ""); err != nil {
			t.Fatalf("%s 标准档空 confirm 应通过: %v", action, err)
		}
	}
}

// TestDockerBuildPushSessionIDsDerivedAndValid：build/push 的进度会话句柄照
// DockerPullSessionID 的派生模式（prefix + ref），必须过 IsDockerSessionID 的
// 形态闸、且前缀各不相同（三个进度族不共串一把句柄）。
func TestDockerBuildPushSessionIDsDerivedAndValid(t *testing.T) {
	ref := "1790000000000000001"
	for _, got := range []string{DockerBuildSessionID(ref), DockerPushSessionID(ref)} {
		if !IsDockerSessionID(got) {
			t.Fatalf("派生句柄 %q 必须过 IsDockerSessionID", got)
		}
	}
	if DockerBuildSessionID(ref) != "build_"+ref || DockerPushSessionID(ref) != "push_"+ref {
		t.Fatalf("句柄必须是固定前缀 + ref")
	}
	// 三族前缀互不相同：帧通道按句柄路由，串把 = 帧找错主人。
	seen := map[string]bool{
		DockerPullSessionID(ref):  true,
		DockerBuildSessionID(ref): true,
		DockerPushSessionID(ref):  true,
	}
	if len(seen) != 3 {
		t.Fatal("pull/build/push 三个派生句柄必须互不相同")
	}
}

// TestDockerPushProgressItemValidate：push 进度记录的校验与 pull 同款同阈
// （终态互斥、计数非负、字符串上限）。
func TestDockerPushProgressItemValidate(t *testing.T) {
	ok := func() *DockerPushProgressItem {
		return &DockerPushProgressItem{T: 1790000000001, Status: "Pushing"}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("合法进度记录应通过: %v", err)
	}
	for name, mut := range map[string]func(*DockerPushProgressItem){
		"T 非正":          func(p *DockerPushProgressItem) { p.T = 0 },
		"双终态":           func(p *DockerPushProgressItem) { p.Done, p.Error = true, "x" },
		"计数为负":          func(p *DockerPushProgressItem) { p.Current = -1 },
		"current超total": func(p *DockerPushProgressItem) { p.Total, p.Current = 10, 11 },
		"status超长":      func(p *DockerPushProgressItem) { p.Status = strings.Repeat("s", MaxDockerStringBytes+1) },
	} {
		p := ok()
		mut(p)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("%s 必须被拒，实际 %v", name, err)
		}
	}
	// 成功的终态项：Done=true、Error 为空 —— 是合法形态。
	done := &DockerPushProgressItem{T: 1790000000001, Done: true}
	if err := done.Validate(); err != nil {
		t.Fatalf("Done 终态项应通过: %v", err)
	}
}

func TestDockerBuildProgressItemValidate(t *testing.T) {
	ok := func() *DockerBuildProgressItem {
		return &DockerBuildProgressItem{T: 1790000000001, Status: "FROM node:20"}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("合法构建进度记录应通过: %v", err)
	}
	streamOK := ok()
	streamOK.Stream = strings.Repeat("x", 4096)
	if err := streamOK.Validate(); err != nil {
		t.Fatalf("文本行在帧上限内应通过: %v", err)
	}
	for name, mut := range map[string]func(*DockerBuildProgressItem){
		"T 非正":       func(p *DockerBuildProgressItem) { p.T = 0 },
		"双终态":        func(p *DockerBuildProgressItem) { p.Done, p.Error = true, "x" },
		"stream超帧上限": func(p *DockerBuildProgressItem) { p.Stream = strings.Repeat("x", MaxDockerFrameDataBytes+1) },
		"error超长":    func(p *DockerBuildProgressItem) { p.Error = strings.Repeat("e", MaxDockerStringBytes+1) },
	} {
		p := ok()
		mut(p)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("%s 必须被拒，实际 %v", name, err)
		}
	}
	done := &DockerBuildProgressItem{T: 1790000000001, Done: true}
	if err := done.Validate(); err != nil {
		t.Fatalf("Done 终态项应通过: %v", err)
	}
}

// TestDockerBuildPushRoundTrip：build/push 指令（含 args 与 auth）经协议 JSON
// 编解码回环后逐字节保真 —— 新字段与既有字段共用一条载荷边界的回归钉。
func TestDockerBuildPushRoundTrip(t *testing.T) {
	build := &DockerCmd{Ref: "1790000000000000001", Action: DockerActionImageBuild,
		Options: DockerCmdOptions{Context: "app.tar.gz", Dockerfile: "docker/Dockerfile.prod",
			Tag: "harbor.example.com/team/app:1", Args: map[string]string{"NODE_ENV": "prod"}}}
	if err := build.Validate(); err != nil {
		t.Fatalf("build 指令应通过校验: %v", err)
	}
	push := &DockerCmd{Ref: "1790000000000000001", Action: DockerActionImagePush,
		Options: DockerCmdOptions{Target: "app:1", Registry: "harbor.example.com"},
		Auth:    &DockerRegistryAuth{Registry: "harbor.example.com", Username: "u", Password: "p"}}
	if err := push.Validate(); err != nil {
		t.Fatalf("push 指令应通过校验: %v", err)
	}
	for _, cmd := range []*DockerCmd{build, push} {
		raw, err := json.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		var back DockerCmd
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.Action != cmd.Action || !reflectDeepOptionsEqual(back.Options, cmd.Options) {
			t.Fatalf("回环后 options 不保真: %+v", back.Options)
		}
		if cmd.Auth != nil && (back.Auth == nil || back.Auth.Password != cmd.Auth.Password) {
			t.Fatal("回环后 auth 不保真")
		}
		if err := back.Validate(); err != nil {
			t.Fatalf("回环后必须仍过校验: %v", err)
		}
	}
}

// reflectDeepOptionsEqual 比对两个 options 的语义等价（map 用 DeepEqual，
// 避免为测试引入第三方比较器）。
func reflectDeepOptionsEqual(a, b DockerCmdOptions) bool {
	if a.Context != b.Context || a.Dockerfile != b.Dockerfile || a.Tag != b.Tag ||
		a.Target != b.Target || a.Registry != b.Registry {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for k, v := range a.Args {
		if b.Args[k] != v {
			return false
		}
	}
	return true
}

// TestDockerBuildContextFilenameShapes：文件名白名单的相邻判定（合法/非法边界
// 各正反例），钉住正则与 IsDockerBuildContextFilename 的行为一致。
func TestDockerBuildContextFilenameShapes(t *testing.T) {
	for _, s := range []string{"app.tar", "app.tar.gz", "app.tgz", "a1.tar", "x_y-z.tar.gz"} {
		if !IsDockerBuildContextFilename(s) {
			t.Fatalf("%q 应是合法上下文文件名", s)
		}
	}
	for _, s := range []string{"", ".tar", "../a.tar", "a/b.tar", "a.tar.gz/", "a.zip",
		"a.tarv", "a.tar.g", "a gz.tar", "A.TAR.gz", "..tar", "-a.tar"} {
		if IsDockerBuildContextFilename(s) {
			t.Fatalf("%q 应是非法上下文文件名", s)
		}
	}
}

func TestDockerBuildSubpathShapes(t *testing.T) {
	for _, s := range []string{"Dockerfile", "docker/Dockerfile.prod", "a.b/c-d_e.f"} {
		if !IsDockerBuildSubpath(s) {
			t.Fatalf("%q 应是合法子路径", s)
		}
	}
	for _, s := range []string{"", "/Dockerfile", "../Dockerfile", "a/../b", "./a",
		"a//b", "a\\b", "\x00", "a b", "a/", "/", "docker file/Dockerfile"} {
		if IsDockerBuildSubpath(s) {
			t.Fatalf("%q 应是非法子路径", s)
		}
	}
}
