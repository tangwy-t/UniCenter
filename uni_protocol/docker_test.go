package agentproto

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden 文件必须在**载荷层**也能解码并过校验。
//
// 为什么需要这条（而不是只靠 snapshot_test.go 的 TestGoldenFilesRoundTrip）：
// 那条只做「信封解码 + 信封字段回环」，看不见载荷形状错误。实测抓到过一例：
// agent.docker.result 的 payload 被写成 JSON 对象，而线上是 []byte（base64 串）——
// 信封门禁全绿，真解码时才炸。golden 是「规范线上形态」的样本，形状错了就是在
// 教后来者写错的代码，故在这一层再钉一次。
//
// DecodeTyped 连载荷的 Validate 一起跑，故它同时守住「样本是语义合法的」。
func TestDockerGoldenPayloadsDecode(t *testing.T) {
	dir := filepath.Join("testdata", "golden", "v1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("golden 目录不可读: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), "docker") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: 信封解码失败: %v", e.Name(), err)
		}
		if _, err := DecodeTyped(m); err != nil {
			t.Fatalf("%s 的载荷无法解码或语义非法（golden 必须是真的线上形态）: %v", e.Name(), err)
		}
		checked++
	}
	if checked != 8 {
		t.Fatalf("应检查 8 个 docker golden（5 条消息 + hello_ack 的配置块 + 2 条上传控制帧），实际 %d 个", checked)
	}
}

// 白名单与总表必须双向一致：漏一条 → 该 action 永远收不到（agent 判未知）；
// 多一条 → 协议承诺了一个没有策略的动作。
func TestDockerActionWhiteListIsComplete(t *testing.T) {
	all := AllDockerActions()
	if len(all) != 36 {
		t.Fatalf("action 白名单应为 36 条（含创建面 container:create、监控面 container:stats、常驻事件流 docker:events、聚合日志 compose:logs、P2 分发闭环 image:build/image:push 与 P3 安全面 image:scan），实际 %d 条: %v", len(all), all)
	}
	seen := map[string]bool{}
	for _, a := range all {
		if seen[a] {
			t.Fatalf("白名单含重复项 %q", a)
		}
		seen[a] = true
		if !IsDockerAction(a) {
			t.Fatalf("%q 在 AllDockerActions 里但 IsDockerAction 说不是", a)
		}
		spec, ok := LookupDockerAction(a)
		if !ok {
			t.Fatalf("%q 没有 DockerActionSpec", a)
		}
		if spec.Action != a {
			t.Fatalf("Spec.Action(%q) 与枚举键(%q) 不一致", spec.Action, a)
		}
	}
	if IsDockerAction("container:destroy") {
		t.Fatal("未登记的 action 不应通过 IsDockerAction")
	}
	if IsDockerAction("") {
		t.Fatal("空 action 不应通过")
	}
}

// intPtr 是测试用的小工具（缩容到 0 这类「指针 + 零值有意义」的字段要用它）。
func intPtr(v int) *int { return &v }

// boolPtr 是 start 这类「显式 false 与缺席不同义」的字段的测试工具。
func boolPtr(v bool) *bool { return &v }

// 必填清单里的字段名必须真的存在：名字写错会让必填校验恒真（永远「已填」），
// 这类错误在协议层是静默的 —— 只有把「填了就该非空」钉住才看得见。
func TestDockerOptionFieldNamesResolve(t *testing.T) {
	filled := &DockerCmdOptions{
		Target: "x", Tail: 1, Since: 1, N: intPtr(1), Force: true,
		Filename: "x.tar", Overwrite: true, All: true, RemoveOrphans: true,
		Volumes: true, Content: "x", BaseHash: strings.Repeat("a", 64),
		Src: "a", Dst: "b", Patch: map[string]any{"services": map[string]any{}},
		Follow: true, Command: []string{"/bin/sh"}, Backup: "20260101-000000",
		Image: "img:1", Name: "n", Ports: []string{"8080:80"}, Env: []string{"A=B"},
		Mounts: []string{"v:/d"}, RestartPolicy: "always", CPULimit: 1, MemLimitMB: 1,
		Network: "net", Start: boolPtr(false),
		Registry: "harbor.example.com:8443",
		Context:  "src.tar.gz", Dockerfile: "docker/Dockerfile.prod", Tag: "app:1",
		Args: map[string]string{"NODE_ENV": "production"},
	}
	for _, f := range dockerOptionFields {
		if dockerOptionValue(filled, f) == "" {
			t.Errorf("字段 %q 已填但取值函数返回空 —— 必填校验会恒真", f)
		}
	}
	if dockerOptionValue(filled, "nonexistent") != "" {
		t.Error("未知字段名必须返回空串（名字写错 = 必填失败，而不是静默放行）")
	}
	// 每条 spec 的必填项与互斥组成员都必须在字段清单里（名字写错 = 校验静默失效）
	for _, spec := range dockerActionSpecs {
		names := append([]string{}, spec.Required...)
		for _, group := range spec.OneOf {
			names = append(names, group...)
		}
		for _, f := range names {
			found := false
			for _, known := range dockerOptionFields {
				if known == f {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("action %s 的必填/互斥项 %q 不在字段清单里", spec.Action, f)
			}
		}
	}
}

func TestDockerOptionValidation(t *testing.T) {
	ok := func(o *DockerCmdOptions) *DockerCmdOptions { return o }
	cases := []struct {
		name   string
		action string
		opts   *DockerCmdOptions
		want   error
	}{
		{"logs 缺 target", DockerActionContainerLogs, &DockerCmdOptions{}, ErrMissingField},
		{"logs 合法", DockerActionContainerLogs, ok(&DockerCmdOptions{Target: "mysql", Tail: 100}), nil},
		{"logs tail 越界", DockerActionContainerLogs, ok(&DockerCmdOptions{Target: "mysql", Tail: 100001}), ErrInvalidPayload},
		{"inspect 缺 target", DockerActionImageInspect, &DockerCmdOptions{}, ErrMissingField},
		{"项目名含斜杠", DockerActionComposeUp, ok(&DockerCmdOptions{Target: "a/b"}), ErrInvalidPayload},
		{"项目名合法", DockerActionComposeUp, ok(&DockerCmdOptions{Target: "uni-center"}), nil},
		{"服务动作 target 必须是 项目/服务", DockerActionComposeServiceScale, ok(&DockerCmdOptions{Target: "uni-center", N: intPtr(2)}), ErrInvalidPayload},
		{"服务动作合法", DockerActionComposeServiceScale, ok(&DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(2)}), nil},
		{"scale 缺 n", DockerActionComposeServiceScale, ok(&DockerCmdOptions{Target: "uni-center/uni_core"}), ErrMissingField},
		{"镜像 ref 合法", DockerActionImagePull, ok(&DockerCmdOptions{Target: "mysql:8.0.22"}), nil},
		{"镜像 ref 带 digest", DockerActionImagePull, ok(&DockerCmdOptions{Target: "reg.local:5000/a/b@sha256:abc"}), nil},
		{"文件名非法（带路径）", DockerActionImageLoad, ok(&DockerCmdOptions{Filename: "../x.tar"}), ErrInvalidPayload},
		{"文件名合法", DockerActionImageLoad, ok(&DockerCmdOptions{Filename: "mysql-8.0.22.tar"}), nil},
		{"base_hash 非 sha256", DockerActionComposeFilePatch,
			ok(&DockerCmdOptions{Target: "uni-center", BaseHash: "abc", Patch: map[string]any{"services": map[string]any{}}}), ErrInvalidPayload},
		{"content 超 1MB", DockerActionComposeFileWrite,
			ok(&DockerCmdOptions{Target: "uni-center", Content: strings.Repeat("x", MaxDockerComposeFileBytes+1),
				BaseHash: strings.Repeat("0", 64)}), ErrInvalidPayload},
		// events 是 host 级 action：零必填、零确认 —— 空 options 就是完整形态；
		// 带上 target 也被放行（host 级动作无视 options 内容，机制上它不产生任何指令）。
		{"events 空 options 合法", DockerActionEvents, &DockerCmdOptions{}, nil},
		{"events 带 target 也放行", DockerActionEvents, ok(&DockerCmdOptions{Target: "anything"}), nil},
		// compose:logs（5a）：target 走项目名白名单（复用 compose 项目的校验）；
		// since 显式拒绝（CLI 公共子集没有 --since —— 静默无视会答非所问）。
		{"聚合日志缺项目名", DockerActionComposeLogs, &DockerCmdOptions{}, ErrMissingField},
		{"聚合日志合法", DockerActionComposeLogs, ok(&DockerCmdOptions{Target: "uni-center", Follow: true, Tail: 100}), nil},
		{"聚合日志不带 follow 也合法（读完即 eof）", DockerActionComposeLogs, ok(&DockerCmdOptions{Target: "uni-center"}), nil},
		{"聚合日志项目名含斜杠被拒", DockerActionComposeLogs, ok(&DockerCmdOptions{Target: "uni-center/uni_core"}), ErrInvalidPayload},
		{"聚合日志带 since 被拒", DockerActionComposeLogs, ok(&DockerCmdOptions{Target: "uni-center", Since: 1790000000}), ErrInvalidPayload},
		{"聚合日志 tail 越界被拒", DockerActionComposeLogs, ok(&DockerCmdOptions{Target: "uni-center", Tail: 100001}), ErrInvalidPayload},
		// image:scan（P3·安全面）：target 与 inspect/pull 同族（镜像引用白名单），
		// 无确认档 —— 只读语义，无破坏性动作。
		{"扫描缺 target", DockerActionImageScan, &DockerCmdOptions{}, ErrMissingField},
		{"扫描 target 合法（带 tag）", DockerActionImageScan, ok(&DockerCmdOptions{Target: "nginx:1.27"}), nil},
		{"扫描 target 合法（digest）", DockerActionImageScan, ok(&DockerCmdOptions{Target: "reg.local:5000/a/b@sha256:abc"}), nil},
		{"扫描 target 含空白被拒", DockerActionImageScan, ok(&DockerCmdOptions{Target: "nginx 1.27"}), ErrInvalidPayload},
		{"扫描 target 横线开头被拒（flag 形态）", DockerActionImageScan, ok(&DockerCmdOptions{Target: "-nginx"}), ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDockerCmdOptions(c.action, c.opts)
			if c.want == nil {
				if err != nil {
					t.Fatalf("应通过，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
}

// ── 四支柱·创建面（4a）：container:create 的 options 逐字段校验 ───────────────
//
// 每个字段独立的正反例：正例证「合法形态不误伤」，反例证「注入/越界形态出不去」。
// 失败结论句带字段名（decodeErr 的 field 就是它）——core 的 400 与 agent 的执行
// 拒绝都引用这个字段名。
func TestDockerContainerCreateOptions(t *testing.T) {
	okCreate := func(o *DockerCmdOptions) *DockerCmdOptions { return o }
	cases := []struct {
		name string
		opts *DockerCmdOptions
		want error
	}{
		{"缺 image", &DockerCmdOptions{}, ErrMissingField},
		{"只给 image 就是完整形态", okCreate(&DockerCmdOptions{Image: "nginx:1.27"}), nil},
		// create 没有 target（对象语义在 image/name）：带 target 是字段归属错误。
		{"带 target 被拒", okCreate(&DockerCmdOptions{Image: "nginx", Target: "web-1"}), ErrInvalidPayload},
		{"完整形态合法", okCreate(&DockerCmdOptions{
			Image: "reg.local:5000/app@sha256:abc", Name: "web-1",
			Ports: []string{"8080:80", "53:53/udp"}, Env: []string{"MODE=prod", "EMPTY="},
			Mounts:        []string{"data:/var/lib/app", "/srv/app:/etc/app:ro"},
			RestartPolicy: "unless-stopped", CPULimit: 2, MemLimitMB: 2048,
			Network: "app-net", Start: boolPtr(false),
		}), nil},

		{"image 含空白", okCreate(&DockerCmdOptions{Image: "nginx latest"}), ErrInvalidPayload},
		{"name 含斜杠", okCreate(&DockerCmdOptions{Image: "nginx", Name: "a/b"}), ErrInvalidPayload},
		{"name 短横开头", okCreate(&DockerCmdOptions{Image: "nginx", Name: "-x"}), ErrInvalidPayload},
		{"name 合法", okCreate(&DockerCmdOptions{Image: "nginx", Name: "web_1.0"}), nil},

		{"端口 host:container:tcp", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"8080:80/tcp"}}), nil},
		{"端口缺协议默认 tcp", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"8080:80"}}), nil},
		{"端口 udp", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"53:53/udp"}}), nil},
		{"端口宿主为 0", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"0:80"}}), ErrInvalidPayload},
		{"端口容器为 0", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"8080:0"}}), ErrInvalidPayload},
		{"端口越界", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"8080:99999"}}), ErrInvalidPayload},
		{"端口三段", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"1:80:80"}}), ErrInvalidPayload},
		{"端口协议非白名单", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{"8080:80/sctp"}}), ErrInvalidPayload},
		{"端口空宿主段", okCreate(&DockerCmdOptions{Image: "nginx", Ports: []string{":80"}}), ErrInvalidPayload},
		{"端口条数超限", okCreate(&DockerCmdOptions{Image: "nginx", Ports: repeatDocker("8080:80", 33)}), ErrInvalidPayload},

		{"env 合法", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"MYSQL_ROOT_PASSWORD=x"}}), nil},
		{"env 值可为空", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"EMPTY="}}), nil},
		{"env 缺等号", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"FOO"}}), ErrInvalidPayload},
		{"env 键数字开头", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"1FOO=x"}}), ErrInvalidPayload},
		{"env 值含换行", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"FOO=a\nb"}}), ErrInvalidPayload},
		{"env 值含 NUL", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"FOO=a\x00b"}}), ErrInvalidPayload},
		{"env 超长", okCreate(&DockerCmdOptions{Image: "nginx", Env: []string{"K=" + strings.Repeat("x", MaxDockerStringBytes)}}), ErrInvalidPayload},
		{"env 条数超限", okCreate(&DockerCmdOptions{Image: "nginx", Env: repeatDocker("K=v", 33)}), ErrInvalidPayload},

		{"挂载命名卷", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"data:/var/lib/mysql"}}), nil},
		{"挂载命名卷 ro", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"data:/d:ro"}}), nil},
		{"挂载 bind 路径", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"/srv/conf:/etc/conf"}}), nil},
		{"挂载 bind ro", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"/a:/b:ro"}}), nil},
		{"挂载模式非 ro", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"data:/d:z"}}), ErrInvalidPayload},
		{"挂载源相对路径", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"data/x:/d"}}), ErrInvalidPayload},
		{"挂载源点段", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"../x:/d"}}), ErrInvalidPayload},
		{"挂载目的地相对", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"v:rel"}}), ErrInvalidPayload},
		{"挂载四段", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"a:b:c:d"}}), ErrInvalidPayload},
		{"挂载无冒号", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: []string{"nodst"}}), ErrInvalidPayload},
		{"挂载条数超限", okCreate(&DockerCmdOptions{Image: "nginx", Mounts: repeatDocker("v:/d", 33)}), ErrInvalidPayload},

		{"restart always", okCreate(&DockerCmdOptions{Image: "nginx", RestartPolicy: "always"}), nil},
		{"restart 非枚举", okCreate(&DockerCmdOptions{Image: "nginx", RestartPolicy: "on-success"}), ErrInvalidPayload},
		{"cpu_limit 上限内", okCreate(&DockerCmdOptions{Image: "nginx", CPULimit: 32}), nil},
		{"cpu_limit 越界", okCreate(&DockerCmdOptions{Image: "nginx", CPULimit: 32.5}), ErrInvalidPayload},
		{"cpu_limit 负", okCreate(&DockerCmdOptions{Image: "nginx", CPULimit: -0.5}), ErrInvalidPayload},
		{"cpu_limit NaN", okCreate(&DockerCmdOptions{Image: "nginx", CPULimit: math.NaN()}), ErrInvalidPayload},
		{"mem_limit_mb 上限内", okCreate(&DockerCmdOptions{Image: "nginx", MemLimitMB: 32768}), nil},
		{"mem_limit_mb 越界", okCreate(&DockerCmdOptions{Image: "nginx", MemLimitMB: 32769}), ErrInvalidPayload},
		{"mem_limit_mb 负", okCreate(&DockerCmdOptions{Image: "nginx", MemLimitMB: -1}), ErrInvalidPayload},
		{"network 合法", okCreate(&DockerCmdOptions{Image: "nginx", Network: "app-net"}), nil},
		{"network 含斜杠", okCreate(&DockerCmdOptions{Image: "nginx", Network: "a/b"}), ErrInvalidPayload},
		{"start 显式 false 合法", okCreate(&DockerCmdOptions{Image: "nginx", Start: boolPtr(false)}), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDockerCmdOptions(DockerActionContainerCreate, c.opts)
			if c.want == nil {
				if err != nil {
					t.Fatalf("应通过，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
}

// create 专属字段挂在别的 action 上 = 字段归属错误：必须拒绝而不是无视
// （照 backup 的纪律 —— 协议上字段归属唯一的 action）。
func TestDockerCreateFieldsRejectedOnOtherActions(t *testing.T) {
	for _, c := range []struct {
		name   string
		action string
		opts   *DockerCmdOptions
	}{
		{"image 挂在 logs", DockerActionContainerLogs, &DockerCmdOptions{Target: "mysql", Image: "nginx"}},
		{"ports 挂在 stats", DockerActionContainerStats, &DockerCmdOptions{Target: "mysql", Ports: []string{"80:80"}}},
		{"start 挂在 exec", DockerActionContainerExec, &DockerCmdOptions{Target: "mysql", Start: boolPtr(false)}},
		{"restart_policy 挂在 pull", DockerActionImagePull, &DockerCmdOptions{Target: "nginx:1", RestartPolicy: "always"}},
		// events 是零参数 host 级 action：它对 target 的宽松（带也放行）不应扩大到
		// create 专属字段 —— 那仍是归属错误。
		{"image 挂在 events", DockerActionEvents, &DockerCmdOptions{Image: "nginx"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateDockerCmdOptions(c.action, c.opts); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("create 专属字段挂在 %s 上必须拒，实际 %v", c.action, err)
			}
		})
	}
}

// 确认档：create 是标准档 —— ExpectedDockerConfirm 恒空，confirm 带不带都放行。
func TestDockerContainerCreateConfirmIsStandard(t *testing.T) {
	opts := &DockerCmdOptions{Image: "nginx:1.27"}
	if got := ExpectedDockerConfirm(DockerActionContainerCreate, opts); got != "" {
		t.Fatalf("create 是标准档，期望空确认值，实际 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionContainerCreate, opts, ""); err != nil {
		t.Fatalf("标准档不带 confirm 应放行，实际 %v", err)
	}
	if err := CheckDockerConfirm(DockerActionContainerCreate, opts, "whatever"); err != nil {
		t.Fatalf("标准档带 confirm 也应放行（带不带都可），实际 %v", err)
	}
}

// 形状纪律：create 的十个新字段必须全部带 omitempty —— 协议只能新增可选字段
// （加字段是纯粹的增量，老 agent 收不到新字段、老协议不认识它们都无碍；去掉
// omitempty 会触发形状漂移守卫的破坏性变更判定）。
func TestDockerContainerCreateFieldsAreAdditive(t *testing.T) {
	shapes := map[string]FieldShape{}
	for _, f := range Snapshot()["DockerCmdOptions"] {
		shapes[f.Name] = f
	}
	for _, name := range []string{"Image", "Name", "Ports", "Env", "Mounts",
		"RestartPolicy", "CPULimit", "MemLimitMB", "Network", "Start"} {
		f, ok := shapes[name]
		if !ok {
			t.Fatalf("DockerCmdOptions 缺少 %s", name)
		}
		if !f.OmitEmpty {
			t.Fatalf("%s 必须带 omitempty（创建面是纯增量，不能收紧形状）", name)
		}
	}
	if shapes["Start"].Type != "*bool" {
		t.Fatalf("start 的字段类型应为 *bool（显式 false 与缺席不同义），实际 %s", shapes["Start"].Type)
	}
}

// 线上形态回环：带全量 create options 的指令编码后必须能原样解码 ——
// 钉住 JSON tag（snake_case）与载荷结构不被工程化悄悄改动。
func TestDockerContainerCreateRoundTrip(t *testing.T) {
	cmd := &DockerCmd{Ref: "1700000000001", Action: DockerActionContainerCreate,
		Options: DockerCmdOptions{
			Image: "nginx:1.27", Name: "web-1", Ports: []string{"8080:80"},
			Env: []string{"MODE=prod"}, Mounts: []string{"data:/d:ro"},
			RestartPolicy: "always", CPULimit: 1.5, MemLimitMB: 512,
			Network: "app-net", Start: boolPtr(false),
		}}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("全量 create 应通过: %v", err)
	}
	m, err := NewMessage("1", TypeCoreDockerCmd, cmd)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// omitempty 语义：显式 false 必须在线上（缺省 true 由 agent 判），其余按蛇形 tag。
	for _, want := range []string{
		`"image":"nginx:1.27"`, `"restart_policy":"always"`, `"cpu_limit":1.5`,
		`"mem_limit_mb":512`, `"start":false`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("编码缺 %s: %s", want, raw)
		}
	}
	m2, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got DockerCmd
	if err := m2.DecodeData(&got); err != nil {
		t.Fatal(err)
	}
	o := got.Options
	if o.Image != "nginx:1.27" || o.Name != "web-1" || len(o.Ports) != 1 || o.Ports[0] != "8080:80" ||
		len(o.Env) != 1 || len(o.Mounts) != 1 || o.RestartPolicy != "always" ||
		o.CPULimit != 1.5 || o.MemLimitMB != 512 || o.Network != "app-net" ||
		o.Start == nil || *o.Start {
		t.Fatalf("create options 回环不符: %+v", o)
	}
}

// repeatDocker 造一个 n 项同值切片（条目上限的反例）。
func repeatDocker(v string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// v1.2.3 纯增量：container:logs 的 follow 与 container:exec 的 command。
//
// 这里钉住三件事：
//   - 两个字段确实在线上存在且能原样回环（否则 core 发的 follow/argv 到 agent 就没了）；
//   - 两者都带 omitempty（additive-only 守卫据此放行：老 agent 不发、老 core 收到不认得）；
//   - command 的越界形态被协议层拒掉（argv 直传不经 shell，尺寸与 NUL/换行是唯一护栏）。
func TestDockerFollowAndCommandRoundTrip(t *testing.T) {
	// 形状：新字段必须是**带 omitempty 的可选字段**（否则 TestShapeDriftAdditiveOnly 会红）。
	shapes := map[string]FieldShape{}
	for _, f := range Snapshot()["DockerCmdOptions"] {
		shapes[f.Name] = f
	}
	for _, name := range []string{"Follow", "Command"} {
		f, ok := shapes[name]
		if !ok {
			t.Fatalf("DockerCmdOptions 缺少 %s", name)
		}
		if !f.OmitEmpty {
			t.Fatalf("%s 必须带 omitempty（v1.2.3 是纯增量，不能收紧形状）", name)
		}
	}
	if shapes["Command"].Type != "[]string" || shapes["Follow"].Type != "bool" {
		t.Fatalf("新字段类型不符契约: follow=%s command=%s",
			shapes["Follow"].Type, shapes["Command"].Type)
	}

	cmd := &DockerCmd{Ref: "1700000000001", Action: DockerActionContainerExec,
		Options: DockerCmdOptions{Target: "mysql", Command: []string{"/bin/bash", "-lc", "ls -la"}}}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("带 argv 的 exec 应通过: %v", err)
	}
	m, err := NewMessage("1", TypeCoreDockerCmd, cmd)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"command":["/bin/bash","-lc","ls -la"]`) {
		t.Fatalf("exec 的 argv 必须按数组原样编码: %s", raw)
	}
	m2, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got DockerCmd
	if err := m2.DecodeData(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Options.Command) != 3 || got.Options.Command[2] != "ls -la" {
		t.Fatalf("argv 必须原样回环: %+v", got.Options.Command)
	}

	// logs + follow：真值时在线，假值/缺席时不出现（omitempty）。
	logs := &DockerCmd{Ref: "1700000000002", Action: DockerActionContainerLogs,
		Options: DockerCmdOptions{Target: "mysql", Follow: true}}
	lm, err := NewMessage("2", TypeCoreDockerCmd, logs)
	if err != nil {
		t.Fatal(err)
	}
	lraw, err := lm.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lraw), `"follow":true`) {
		t.Fatalf("follow=true 必须编码进载荷: %s", lraw)
	}
	noFollow := &DockerCmd{Ref: "1700000000003", Action: DockerActionContainerLogs,
		Options: DockerCmdOptions{Target: "mysql"}}
	nm, err := NewMessage("3", TypeCoreDockerCmd, noFollow)
	if err != nil {
		t.Fatal(err)
	}
	nraw, err := nm.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nraw), "follow") || strings.Contains(string(nraw), "command") {
		t.Fatalf("零值不得出现在线上（omitempty 是增量演进的前提）: %s", nraw)
	}
	var gotFollow DockerCmd
	dm, err := Decode(lraw)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.DecodeData(&gotFollow); err != nil {
		t.Fatal(err)
	}
	if !gotFollow.Options.Follow {
		t.Fatal("follow 必须原样回环")
	}
}

// exec 的 argv 校验：直传不经 shell，故尺寸/形态是唯一护栏。
// 越界形态必须被**协议层**拒掉（core 受理时先拦，agent 执行前再拦）。
func TestDockerExecCommandValidation(t *testing.T) {
	okCmd := func(argv []string) *DockerCmdOptions {
		return &DockerCmdOptions{Target: "mysql", Command: argv}
	}
	if err := ValidateDockerCmdOptions(DockerActionContainerExec, okCmd([]string{"/bin/sh"})); err != nil {
		t.Fatalf("单个 argv 应通过: %v", err)
	}
	// 缺省即「未给 command」：合法（agent 侧用缺省 ["/bin/sh"]）。
	if err := ValidateDockerCmdOptions(DockerActionContainerExec, &DockerCmdOptions{Target: "mysql"}); err != nil {
		t.Fatalf("未给 command 应通过（缺省 /bin/sh）: %v", err)
	}
	// 32 项整：恰好放行。
	if err := ValidateDockerCmdOptions(DockerActionContainerExec, okCmd(makeArgv(32, 8))); err != nil {
		t.Fatalf("32 项 argv 应通过: %v", err)
	}
	bad := []struct {
		name string
		argv []string
	}{
		{"33 项", makeArgv(33, 8)},
		{"单项超 256B", []string{strings.Repeat("x", 257)}},
		{"空元素", []string{"/bin/sh", ""}},
		{"含 NUL", []string{"/bin/sh", "a\x00b"}},
		{"含换行", []string{"/bin/sh", "a\nb"}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateDockerCmdOptions(DockerActionContainerExec, okCmd(c.argv)); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("err = %v, want ErrInvalidPayload", err)
			}
		})
	}
	// 256B 整：恰好放行（边界是「≤」）。
	if err := ValidateDockerCmdOptions(DockerActionContainerExec, okCmd([]string{strings.Repeat("x", 256)})); err != nil {
		t.Fatalf("256B 单参数应通过: %v", err)
	}
	// exec 的必填仍是 target（command 不是必填）。
	if err := ValidateDockerCmdOptions(DockerActionContainerExec, &DockerCmdOptions{}); !errors.Is(err, ErrMissingField) {
		t.Fatalf("exec 缺 target 必须被拒，实际 %v", err)
	}
}

// makeArgv 造 n 项、每项 size 字节的参数（取值无意义，只求尺寸形态合法）。
func makeArgv(n, size int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.Repeat("a", size)
	}
	return out
}

// v1.2.4 纯增量（四期配置编辑）：Backup 选项 + Backups 载荷。
//
// 钉住四件事：
//   - 两个新字段都带 omitempty（additive-only 守卫据此放行）；
//   - Backup 能原样回环（core 发的回滚令牌到 agent 不能丢）；
//   - 备份清单能按线上形态编解码（token/hash/size_bytes/at）；
//   - 协议上永远不出现路径：令牌形态之外的取值一律拒（见 TestDockerBackupTokenShape）。
func TestDockerBackupOptionAndBackupsPayloadRoundTrip(t *testing.T) {
	shapes := map[string]FieldShape{}
	for _, f := range Snapshot()["DockerCmdOptions"] {
		shapes[f.Name] = f
	}
	bf, ok := shapes["Backup"]
	if !ok {
		t.Fatal("DockerCmdOptions 缺少 Backup")
	}
	if !bf.OmitEmpty || bf.Type != "string" {
		t.Fatalf("Backup 必须是带 omitempty 的 string（纯增量）: %+v", bf)
	}
	payloadShapes := map[string]FieldShape{}
	for _, f := range Snapshot()["DockerComposeFilePayload"] {
		payloadShapes[f.Name] = f
	}
	bfs, ok := payloadShapes["Backups"]
	if !ok {
		t.Fatal("DockerComposeFilePayload 缺少 Backups")
	}
	if !bfs.OmitEmpty || bfs.Type != "[]agentproto.DockerComposeBackup" {
		t.Fatalf("Backups 必须是带 omitempty 的 []DockerComposeBackup: %+v", bfs)
	}
	for _, f := range Snapshot()["DockerComposeBackup"] {
		switch f.Name {
		case "Token", "Hash", "SizeBytes", "At":
			if f.JSON == "" {
				t.Fatalf("DockerComposeBackup.%s 缺少 json tag", f.Name)
			}
		default:
			t.Fatalf("DockerComposeBackup 出现契约外字段 %s", f.Name)
		}
	}

	// 回滚指令：target + base_hash + backup（不带 content）——协议必须放行且原样回环。
	cmd := &DockerCmd{Ref: "1700000000001", Action: DockerActionComposeFileWrite,
		Options: DockerCmdOptions{Target: "uni-center", BaseHash: strings.Repeat("a", 64),
			Backup: "20260928-153000"}}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("回滚模式（backup 代替 content）应通过: %v", err)
	}
	m, err := NewMessage("1", TypeCoreDockerCmd, cmd)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"backup":"20260928-153000"`) {
		t.Fatalf("回滚令牌必须编码进载荷: %s", raw)
	}
	dm, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got DockerCmd
	if err := dm.DecodeData(&got); err != nil {
		t.Fatal(err)
	}
	if got.Options.Backup != "20260928-153000" {
		t.Fatalf("回滚令牌必须原样回环: %+v", got.Options)
	}

	// 读载荷带备份清单：编码形态按契约的 json tag，解码后逐项一致。
	p := DockerComposeFilePayload{Content: "services: {}\n", Hash: strings.Repeat("b", 64),
		Path: "/data/docker-compose.yml",
		Backups: []DockerComposeBackup{
			{Token: "20260928-153000", Hash: strings.Repeat("c", 64), SizeBytes: 128, At: 1790000000},
		}}
	praw, err := json.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	want := `"backups":[{"token":"20260928-153000","hash":"` + strings.Repeat("c", 64) +
		`","size_bytes":128,"at":1790000000}]`
	if !strings.Contains(string(praw), want) {
		t.Fatalf("备份清单的线上形态不符契约:\n%s\nwant 包含 %s", praw, want)
	}
	var back DockerComposeFilePayload
	if err := json.Unmarshal(praw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Backups) != 1 || back.Backups[0].Token != "20260928-153000" ||
		back.Backups[0].SizeBytes != 128 || back.Backups[0].At != 1790000000 {
		t.Fatalf("备份清单必须原样回环: %+v", back.Backups)
	}
	// omitempty：空清单不得出现在线上（老前端在不认识它时读到的形态不变）。
	empty, err := json.Marshal(&DockerComposeFilePayload{Content: "x", Hash: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "backups") {
		t.Fatalf("空备份清单必须省略: %s", empty)
	}
}

// compose.file:write 的正文来源是「content 或 backup」二选一（v1.2.4）：
//   - 只有内容 / 只有令牌 → 放行；
//   - 都缺 → 缺字段；都给 → 非法（**不猜优先级**：猜错就等于静默丢弃用户的一个意图）。
//
// store 里存的是**语义**而不是某次实现：把「都给」判成合法会在生产里表现为
// 「用户以为在回滚，文件却被另一份正文覆盖」——没有第二次机会。
func TestDockerComposeFileWriteContentOrBackup(t *testing.T) {
	hash := strings.Repeat("a", 64)
	base := func() DockerCmdOptions {
		return DockerCmdOptions{Target: "uni-center", BaseHash: hash}
	}
	withContent := base()
	withContent.Content = "services:\n  web:\n    image: nginx\n"
	if err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, &withContent); err != nil {
		t.Fatalf("全文写应通过: %v", err)
	}
	withBackup := base()
	withBackup.Backup = "20260928-153000"
	if err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, &withBackup); err != nil {
		t.Fatalf("回滚模式应通过: %v", err)
	}
	neither := base()
	if err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, &neither); !errors.Is(err, ErrMissingField) {
		t.Fatalf("content/backup 都缺必须是缺字段，实际 %v", err)
	}
	both := base()
	both.Content = "services: {}\n"
	both.Backup = "20260928-153000"
	if err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, &both); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("content/backup 都给必须被拒（不许猜优先级），实际 %v", err)
	}
	// base_hash 在两个模式下都仍是必填（乐观锁对回滚同样成立）。
	noHash := withBackup
	noHash.BaseHash = ""
	if err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, &noHash); !errors.Is(err, ErrMissingField) {
		t.Fatalf("回滚同样要 base_hash，实际 %v", err)
	}
}

// 备份令牌的形态校验：它是协议上**唯一**能指到文件系统的现场输入，故白名单必须严格。
// 路径分隔符、`..`、空白、非挂钟时间形态一律拒（§8 路径白名单）。
func TestDockerBackupTokenShape(t *testing.T) {
	good := []string{"20260928-153000", "00000000-000000", "99991231-235959"}
	for _, s := range good {
		if !IsDockerBackupToken(s) {
			t.Fatalf("%q 应是合法令牌", s)
		}
	}
	bad := []string{
		"", "20260928-15300", "20260928-1530000", "2026-0928-153000", "2026092-8153000",
		"20260928_153000", "20260928-153000 ", " 20260928-153000",
		"abcd0928-153000", "20260928-15300a",
		"../20260928-153000", "20260928-153000/../../etc/passwd", "a/b", "..",
		"20260928153000", "20260928-153000\n", "20260928-153000\x00",
	}
	for _, s := range bad {
		if IsDockerBackupToken(s) {
			t.Fatalf("%q 必须被拒（形态白名单是路径不出现的依据）", s)
		}
		o := &DockerCmdOptions{Target: "uni-center", BaseHash: strings.Repeat("a", 64), Backup: s}
		err := ValidateDockerCmdOptions(DockerActionComposeFileWrite, o)
		if !errors.Is(err, ErrMissingField) && !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("非法令牌 %q 必须被协议层拒，实际 err=%v", s, err)
		}
	}
	// 令牌只在 compose.file:write 上合法：别的 action 带它一律拒（协议上不存在第二个用点）。
	o := &DockerCmdOptions{Target: "uni-center", Content: "x", Backup: "20260928-153000"}
	if err := ValidateDockerCmdOptions(DockerActionComposeFileValidate, o); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("validate 带 backup 必须被拒，实际 %v", err)
	}
}

// 补丁形状（compose.file:patch）：顶层段落白名单 + 值为「名字 → 键值映射 | null」。
// 形状不明/越界的补丁必须在协议层出不去 —— 它要进 agent 的文本手术，半懂状态最危险。
func TestDockerPatchShapeValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	check := func(patch map[string]any, want error) {
		t.Helper()
		o := &DockerCmdOptions{Target: "uni-center", BaseHash: hash, Patch: patch}
		err := ValidateDockerCmdOptions(DockerActionComposeFilePatch, o)
		if want == nil {
			if err != nil {
				t.Fatalf("应通过，实际 %v", err)
			}
			return
		}
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want errors.Is(..., %v)", err, want)
		}
	}
	check(map[string]any{"services": map[string]any{
		"web": map[string]any{"image": "nginx:alpine", "ports": nil},
		"db":  nil, // 删整个服务
	}}, nil)
	check(map[string]any{"networks": map[string]any{"default": map[string]any{"driver": "bridge"}}}, nil)
	check(map[string]any{"volumes": map[string]any{"uploads": nil}}, nil)
	check(map[string]any{"x-custom": map[string]any{}}, ErrInvalidPayload)               // 段落越界
	check(map[string]any{"services": "web"}, ErrInvalidPayload)                          // 段落值不是映射
	check(map[string]any{"services": map[string]any{"web": "nginx"}}, ErrInvalidPayload) // 名字值不是映射/null
	check(map[string]any{"services": nil}, ErrInvalidPayload)                            // 顶层 null 不表达「删光」
}

// 确认档：标准档不需要 confirm；强确认档必须逐字匹配（人对目标的「照抄一遍」，
// 不做大小写模糊匹配）；扩缩容到 0 才要确认，扩容到 N>0 不要。
func TestDockerConfirmRules(t *testing.T) {
	del := &DockerCmdOptions{Target: "zentao"}
	if got := ExpectedDockerConfirm(DockerActionContainerRemove, del); got != "" {
		t.Fatalf("单删容器是标准档，不该要 confirm，实际要求 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionContainerRemove, del, ""); err != nil {
		t.Fatalf("标准档不带 confirm 应通过: %v", err)
	}
	prune := &DockerCmdOptions{}
	if got := ExpectedDockerConfirm(DockerActionImagePrune, prune); got != DockerConfirmDelete {
		t.Fatalf("prune 的确认值应是 DELETE，实际 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionImagePrune, prune, "delete"); err == nil {
		t.Fatal("小写 delete 不应通过（确认值是照抄，不做模糊匹配）")
	}
	if err := CheckDockerConfirm(DockerActionImagePrune, prune, "DELETE"); err != nil {
		t.Fatalf("DELETE 应通过: %v", err)
	}
	down := &DockerCmdOptions{Target: "uni-center"}
	if got := ExpectedDockerConfirm(DockerActionComposeDown, down); got != "uni-center" {
		t.Fatalf("down 的确认值应是项目名，实际 %q", got)
	}
	scale := &DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(0)}
	if got := ExpectedDockerConfirm(DockerActionComposeServiceScale, scale); got != "uni_core" {
		t.Fatalf("缩容到 0 的确认值应是服务名，实际 %q", got)
	}
	if got := ExpectedDockerConfirm(DockerActionComposeServiceScale, &DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(3)}); got != "" {
		t.Fatalf("扩容到 3 不该要确认，实际要求 %q", got)
	}
	// image:save 是「覆盖时强」：确认档挂在第二段（overwrite=true）上，不是无条件。
	save := &DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}
	if got := ExpectedDockerConfirm(DockerActionImageSave, save); got != "" {
		t.Fatalf("首次导出（未带 overwrite）不该要 confirm，实际要求 %q", got)
	}
	saveOver := &DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar", Overwrite: true}
	if got := ExpectedDockerConfirm(DockerActionImageSave, saveOver); got != "mysql.tar" {
		t.Fatalf("覆盖重发的确认值应是文件名，实际 %q", got)
	}
	// compose:logs 是标准档（与 container:logs 对齐）：读日志不改状态，
	//「照抄项目名」的强确认对它没有意义（稀缺性一失，确认档就形同虚设）。
	logs := &DockerCmdOptions{Target: "uni-center", Follow: true}
	if got := ExpectedDockerConfirm(DockerActionComposeLogs, logs); got != "" {
		t.Fatalf("聚合日志不该要 confirm，实际要求 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionComposeLogs, logs, ""); err != nil {
		t.Fatalf("聚合日志不带 confirm 应通过: %v", err)
	}
}

// image:save 的确认档是「覆盖时强(文件名)」（spec §4.3.1；§7.5 两段确认复用 cmd/result）：
//
//	第一段：首次导出不带 confirm → 正常受理；agent 以 O_EXCL 创建，目标已存在时
//	        结果回 already_exists（不执行）；
//	第二段：UI 问过用户后带 overwrite=true + confirm=<文件名> 重发 → 逐字匹配才放行。
//
// 回归点：旧实现无条件要文件名，把「正常首次导出」挡成一次假失败（生产实测复现）。
// 本测试把「第一段无需确认、第二段才要」钉住；顺带把大小写敏感（照抄语义）也钉住。
func TestDockerImageSaveConfirmIsOverwriteGated(t *testing.T) {
	first := &DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}
	if got := ExpectedDockerConfirm(DockerActionImageSave, first); got != "" {
		t.Fatalf("首次导出（未带 overwrite）不该要 confirm，实际要求 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionImageSave, first, ""); err != nil {
		t.Fatalf("首次导出不带 confirm 应受理（否则正常导出变假失败）: %v", err)
	}

	redo := &DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar", Overwrite: true}
	if got := ExpectedDockerConfirm(DockerActionImageSave, redo); got != "mysql.tar" {
		t.Fatalf("覆盖重发的确认值应是文件名，实际要求 %q", got)
	}
	if err := CheckDockerConfirm(DockerActionImageSave, redo, "other.tar"); err == nil {
		t.Fatal("覆盖重发带错文件名必须被拒")
	}
	if err := CheckDockerConfirm(DockerActionImageSave, redo, "MYSQL.TAR"); err == nil {
		t.Fatal("大小写不符必须被拒（确认是照抄一遍，不做模糊匹配）")
	}
	if err := CheckDockerConfirm(DockerActionImageSave, redo, "mysql.tar"); err != nil {
		t.Fatalf("覆盖重发照抄文件名应受理: %v", err)
	}
}

// docker_ok 与 error 是一对：可用却带错误结论、或不可用却不给结论句，都是
// 自相矛盾的帧；不可用还带清单会让页面「一边说不可用、一边列出上一次的容器」。
func TestDockerStateValidate(t *testing.T) {
	good := &DockerState{T: 1, DockerOK: true, Containers: []DockerContainer{{ID: "a", Name: "a", Image: "i", State: "running"}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("合法快照应通过: %v", err)
	}
	bad := []struct {
		name  string
		state *DockerState
		want  error
	}{
		{"t 为 0", &DockerState{DockerOK: true}, ErrInvalidPayload},
		{"可用却带错误结论", &DockerState{T: 1, DockerOK: true, Error: "连不上"}, ErrInvalidPayload},
		{"不可用却没结论句", &DockerState{T: 1}, ErrMissingField},
		// 注意这条必须带 Error：否则先红在「不可用却没结论句」上，测不到清单那条规则。
		{"不可用却带清单", &DockerState{T: 1, Error: "无法连接 docker.sock",
			Containers: []DockerContainer{{ID: "a", Name: "a", Image: "i", State: "running"}}}, ErrInvalidPayload},
		{"名字超长", &DockerState{T: 1, DockerOK: true, Containers: []DockerContainer{
			{ID: strings.Repeat("x", MaxDockerStringBytes+1), Name: "a", Image: "i", State: "running"}}}, ErrInvalidPayload},
		{"条目超上限", &DockerState{T: 1, DockerOK: true,
			Containers: make([]DockerContainer, MaxDockerStateEntries+1)}, ErrInvalidPayload},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.state.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
	if err := (&DockerState{T: 1, DockerOK: false, Error: "无法连接 docker.sock"}).Validate(); err != nil {
		t.Fatalf("不可用但结论句齐备应通过: %v", err)
	}
}

// 项目 / 服务 / 卷条目的 protected 与容器条目同义：agent 按 sys.docker.protected
// 算好结论，前端**不重复实现判断**（否则两份口径迟早说不同的话）。
//
// 这里钉住三件事：
//   - 真值能被编码进载荷、能被解码回来（字段确实在线上存在）；
//   - 它带 omitempty（纯增量演进：老 agent 不发、老 core 收到也不认得，都不影响）；
//   - 加进形状快照后，additive-only 守卫（TestShapeDriftAdditiveOnly）放行。
func TestDockerProjectVolumeProtectedRoundTrip(t *testing.T) {
	st := &DockerState{T: 1, DockerOK: true,
		Volumes: []DockerVolume{
			{Name: "uni-center-uploads", Protected: true},
			{Name: "plain"},
		},
		Projects: []DockerProject{
			{Name: "uni-center", Protected: true, Services: 2, ContainersCount: 2},
			{Name: "plain"},
		},
	}
	m, err := NewMessage("1", TypeAgentDockerState, st)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// 线形态：受保护条目带 "protected":true；未受保护条目**不带该键**（omitempty，
	// 与已登记的容器条目不同 —— 那是必填，因为它承载「裸容器也判定」的结论）。
	if !strings.Contains(string(raw), `"protected":true`) {
		t.Fatalf("受保护条目（项目/卷）的 protected 必须编码进载荷: %s", raw)
	}
	var decoded DockerState
	m2, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.DecodeData(&decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("回环后的快照必须语义合法: %v", err)
	}
	gotVol := map[string]bool{}
	for _, v := range decoded.Volumes {
		gotVol[v.Name] = v.Protected
	}
	if !gotVol["uni-center-uploads"] || gotVol["plain"] {
		t.Fatalf("卷的 protected 必须原样回环: %+v", decoded.Volumes)
	}
	gotProj := map[string]bool{}
	for _, p := range decoded.Projects {
		gotProj[p.Name] = p.Protected
	}
	if !gotProj["uni-center"] || gotProj["plain"] {
		t.Fatalf("项目的 protected 必须原样回环: %+v", decoded.Projects)
	}
	// omitempty：false 不得出现在线上（增量字段的形态纪律，守卫同样按它放行）。
	if strings.Count(string(raw), `"protected"`) != 2 {
		t.Fatalf("只有两个受保护条目的 protected 应出现，实际载荷: %s", raw)
	}
}

// df 汇总（6a 磁盘治理）的三件事：
//   - 真值能被编码进载荷、能被解码回来（字段确实在线上存在）；
//   - 带快照级 omitempty（nil = 这帧没有 df 数据：df 失败或老 agent 不发，页面显示
//     「数据不可用」而不是 0）；不可达帧带 df 与带清单一样被拒；
//   - 负数占用被拒（-1 是 daemon 的「未知」哨兵，必须在 agent 求和前排除，
//     溜进来会被渲染成负数占用）。
func TestDockerDiskUsageRoundTripAndValidate(t *testing.T) {
	st := &DockerState{T: 1, DockerOK: true,
		DiskUsage: &DockerDiskUsage{ImagesTotalMB: 1234.5, ImagesDanglingMB: 367.25,
			VolumesTotalMB: 13.5, BuildCacheMB: 0}}
	m, err := NewMessage("1", TypeAgentDockerState, st)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// 线形态：disk_usage 编码进载荷（build_cache_mb 为 0 也出现 —— 汇总数字 0 是
	// 真值「没有占用」，不是「没有数据」；nil 整块缺席才是「没有数据」）。
	if !strings.Contains(string(raw), `"disk_usage"`) || !strings.Contains(string(raw), `"build_cache_mb":0`) {
		t.Fatalf("df 汇总必须编码进载荷: %s", raw)
	}
	var decoded DockerState
	m2, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.DecodeData(&decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("回环后的快照必须语义合法: %v", err)
	}
	if decoded.DiskUsage == nil || decoded.DiskUsage.ImagesTotalMB != 1234.5 ||
		decoded.DiskUsage.ImagesDanglingMB != 367.25 || decoded.DiskUsage.VolumesTotalMB != 13.5 ||
		decoded.DiskUsage.BuildCacheMB != 0 {
		t.Fatalf("df 汇总必须原样回环: %+v", decoded.DiskUsage)
	}

	// 缺席形态合法（老 agent / df 失败的那帧）。
	if err := (&DockerState{T: 1, DockerOK: true}).Validate(); err != nil {
		t.Fatalf("不带 df 的快照必须合法: %v", err)
	}
	// 不可达帧带 df：与带清单同一句话 —— 「一边说不可用、一边带着账目」。
	if err := (&DockerState{T: 1, Error: "无法连接 docker.sock",
		DiskUsage: &DockerDiskUsage{}}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("不可达帧不得携带 df 汇总，实际 %v", err)
	}
	// 负数占用：采集侧把未知哨兵求了进去，必须在协议层出不去。
	for name, du := range map[string]*DockerDiskUsage{
		"镜像合计为负": {ImagesTotalMB: -1},
		"悬空合计为负": {ImagesDanglingMB: -0.5},
		"卷合计为负":  {VolumesTotalMB: -1},
		"构建缓存为负": {BuildCacheMB: -1},
	} {
		if err := (&DockerState{T: 1, DockerOK: true, DiskUsage: du}).Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("%s必须被拒，实际 %v", name, err)
		}
	}
}

// 结果与指令是一对：ok 与 error 互斥；会话句柄必须是不可枚举的随机 id；
// detail 是排障线索，超限即拒不静默截断（agent 侧必须先 NormalizeDockerDetail）。
func TestDockerCmdAndResultValidate(t *testing.T) {
	cmd := &DockerCmd{Ref: "1700000000001", Action: DockerActionContainerLogs,
		Options: DockerCmdOptions{Target: "mysql"}}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("合法指令应通过: %v", err)
	}
	if err := (&DockerCmd{Ref: "abc", Action: DockerActionContainerLogs, Options: DockerCmdOptions{Target: "m"}}).Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("非十进制 ref 必须被拒，实际 %v", err)
	}
	if err := (&DockerCmd{Ref: "1", Action: "container:destroy", Options: DockerCmdOptions{Target: "m"}}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("未登记 action 必须被拒，实际 %v", err)
	}

	res := &DockerCmdResult{Ref: "1", OK: true}
	if err := res.Validate(); err != nil {
		t.Fatalf("成功结果应通过: %v", err)
	}
	if err := (&DockerCmdResult{Ref: "1", OK: true, Error: "同时也报错"}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("ok=true 同时带 error 必须被拒")
	}
	if err := (&DockerCmdResult{Ref: "1"}).Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatal("ok=false 必须带结论句")
	}
	if err := (&DockerCmdResult{Ref: "1", OK: true, SessionID: "short"}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("会话句柄短于 16 字节必须被拒（可枚举即可劫持）")
	}
	if err := (&DockerCmdResult{Ref: "1", OK: true, SessionID: "0123456789abcdef"}).Validate(); err != nil {
		t.Fatalf("16 字节随机句柄应通过: %v", err)
	}
	if err := (&DockerCmdResult{Ref: "1", OK: true,
		Detail: strings.Repeat("x", MaxDockerResultDetailBytes+1)}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("detail 超限必须被拒（而不是悄悄截断）")
	}
	if err := (&DockerCmdResult{Ref: "1", OK: false, Error: "失败", AlreadyExists: true}).Validate(); err != nil {
		t.Fatalf("save 目标已存在是合法结果: %v", err)
	}
	if err := (&DockerCmdResult{Ref: "1", OK: true, AlreadyExists: true}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("成功却带 already_exists 必须被拒（自相矛盾）")
	}
}

func TestDockerFrameValidate(t *testing.T) {
	sid := "0123456789abcdef"
	if err := (&DockerFrame{SessionID: sid, Seq: 1, Data: []byte("hi")}).Validate(); err != nil {
		t.Fatalf("合法上行帧应通过: %v", err)
	}
	if err := (&DockerFrame{SessionID: sid, Seq: 1,
		Data: make([]byte, MaxDockerFrameDataBytes+1)}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("单帧超过 64KB 必须被拒（合帧纪律的上限在协议层钉住）")
	}
	if err := (&DockerFrame{SessionID: "short", Seq: 1}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("会话句柄非法必须被拒")
	}

	if err := (&CoreDockerFrame{SessionID: sid, Op: DockerFrameOpInput, Data: []byte("ls\n")}).Validate(); err != nil {
		t.Fatalf("input 帧应通过: %v", err)
	}
	if err := (&CoreDockerFrame{SessionID: sid, Op: DockerFrameOpResize, Cols: 120, Rows: 40}).Validate(); err != nil {
		t.Fatalf("resize 帧应通过: %v", err)
	}
	if err := (&CoreDockerFrame{SessionID: sid, Op: DockerFrameOpResize}).Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatal("resize 缺 cols/rows 必须被拒")
	}
	if err := (&CoreDockerFrame{SessionID: sid, Op: DockerFrameOpInput}).Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatal("input 缺 data 必须被拒")
	}
	if err := (&CoreDockerFrame{SessionID: sid, Op: "paste", Data: []byte("x")}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("未登记的 op 必须被拒")
	}
}

func TestDockerConfigValidate(t *testing.T) {
	if err := (&DockerConfig{ConfigVersion: 3, SnapshotInterval: 30}).Validate(); err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	if err := (&DockerConfig{SnapshotInterval: 5}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("快照周期小于 10 秒必须被拒（spec §4.3 最小 10）")
	}
	if err := (&DockerConfig{TransferDir: "relative/path"}).Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("产物目录必须是绝对路径")
	}
}

// 结果载荷必须能回环（它们是命令结果的数据面，前端按同一形状解析）。
func TestDockerReadPayloadRoundTrip(t *testing.T) {
	raw := []byte(`{"lines":"a\nb\n","truncated":true}`)
	var logs DockerLogsPayload
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	if logs.Lines != "a\nb\n" || !logs.Truncated {
		t.Fatalf("日志结果载荷解码不符: %+v", logs)
	}
	var insp DockerImageInspectPayload
	if err := json.Unmarshal([]byte(`{"id":"sha256:x","size_bytes":10,"history":[{"size_bytes":1,"created_by":"CMD x"}]}`), &insp); err != nil {
		t.Fatal(err)
	}
	if len(insp.History) != 1 || insp.History[0].CreatedBy != "CMD x" {
		t.Fatalf("镜像 inspect 载荷解码不符: %+v", insp)
	}
}

// 扫描报告（P3·安全面）的回环：它是 result.payload 通道里的数据面（前端按同一
// 形状解析 agent 直发与缓存回放两种来源），且 Vulns 的上限语义（计数全量、
// 条目截断、Truncated 标注）是协议的口径 —— 这里把「编解码往返不丢字段」钉住。
func TestDockerScanReportRoundTrip(t *testing.T) {
	report := &DockerScanReport{
		ImageID:   "sha256:" + strings.Repeat("ab", 32),
		ScannedAt: 1790000000,
		Counts:    DockerScanCounts{Critical: 2, High: 1, Medium: 0, Low: 3, Unknown: 1},
		Vulns: []DockerScanVuln{
			{ID: "CVE-2026-0001", Pkg: "openssl", Severity: DockerScanSeverityCritical, FixedVersion: "3.0.12", Title: "openssl: X.509 chain issue"},
			{ID: "CVE-2026-0002", Pkg: "libxml2", Severity: DockerScanSeverityHigh},
			{ID: "CVE-2026-0003", Pkg: "zlib", Severity: DockerScanSeverityUnknown, Title: "no advisory text"},
		},
		Truncated: true,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back DockerScanReport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ImageID != report.ImageID || back.ScannedAt != report.ScannedAt || back.Truncated != true {
		t.Fatalf("扫描报告回环不符: %+v", back)
	}
	if back.Counts != report.Counts {
		t.Fatalf("severity 计数回环不符: %+v", back.Counts)
	}
	if len(back.Vulns) != 3 {
		t.Fatalf("CVE 条目回环不符: %+v", back.Vulns)
	}
	// 无修复版/无 title 的条目：omitempty 形态（fixed_version/title 缺席），
	// 解码侧拿到空串 —— 缺席本身是「修复未发布」的信息，不允许被误读成别的值。
	if back.Vulns[1].FixedVersion != "" || back.Vulns[1].Title != "" {
		t.Fatalf("omitempty 字段应解码为空串: %+v", back.Vulns[1])
	}
	// Vulns 用非 omitempty 数组：空清单是「扫了，确实没有 CVE」的显式表达，
	// 与「字段缺席（没扫过）」两个答案（与 DockerState 五清单同一条纪律）。
	empty, err := json.Marshal(&DockerScanReport{ImageID: "sha256:x", ScannedAt: 1, Vulns: []DockerScanVuln{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"vulns":[]`) {
		t.Fatalf("空清单必须是显式空数组而不是字段缺席: %s", empty)
	}
}

// 登记五处是机械的，但漏一处的后果各不相同：漏 registry → 解码报未知类型（**静默忽略**，
// 现象是「docker 数据永远不出现」而不是报错）；漏 snapshot/typeNameOf → 契约守卫红；
// 漏 baseline → 形状漂移不可见。故这里把「五条消息都已登记」钉成一条测试。
func TestDockerMessageTypesAreRegistered(t *testing.T) {
	want := map[string]Direction{
		TypeAgentDockerState:  DirAgentToCore,
		TypeCoreDockerCmd:     DirCoreToAgent,
		TypeAgentDockerResult: DirAgentToCore,
		TypeAgentDockerFrame:  DirAgentToCore,
		TypeCoreDockerFrame:   DirCoreToAgent,
	}
	for ty, dir := range want {
		spec, ok := LookupType(ty)
		if !ok {
			t.Fatalf("类型 %q 未登记进注册表", ty)
		}
		if spec.Direction != dir {
			t.Fatalf("%q 方向应为 %v，实际 %v", ty, dir, spec.Direction)
		}
		if spec.Kind != KindPayload {
			t.Fatalf("%q 应携带载荷", ty)
		}
		if typeNameOf(spec.New()) == "" {
			t.Fatalf("%q 的载荷未在 typeNameOf 登记", ty)
		}
	}
}

// hello_ack 的 docker 块是**可选**字段：老 core 不带它时 agent 用内置默认值，
// 故它必须是指针 + omitempty（additive-only 守卫也据此放行）。
func TestHelloAckDockerBlockIsOptional(t *testing.T) {
	shapes := map[string]FieldShape{}
	for _, f := range Snapshot()["HelloAck"] {
		shapes[f.Name] = f
	}
	f, ok := shapes["Docker"]
	if !ok {
		t.Fatal("HelloAck 缺少 Docker 字段")
	}
	if !f.Pointer || !f.OmitEmpty {
		t.Fatalf("Docker 必须是指针 + omitempty（可选下发块），实际 %+v", f)
	}
	// 被拒的握手不得携带配置（与升级指令同一条纪律）
	rejected := &HelloAck{Accepted: false, RejectReason: "x", Docker: &DockerConfig{ConfigVersion: 1}}
	if err := rejected.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("被拒的连接带 docker 配置必须被拒，实际 %v", err)
	}
	accepted := &HelloAck{Accepted: true, DeviceID: "1", ReportInterval: 10,
		Docker: &DockerConfig{ConfigVersion: 3, Protected: "mysql", SnapshotInterval: 30, TransferDir: "/var/lib/uni_agent/transfer"}}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("合法握手应通过: %v", err)
	}
	bad := &HelloAck{Accepted: true, DeviceID: "1", ReportInterval: 10, Docker: &DockerConfig{SnapshotInterval: 3}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("快照周期小于 10 秒的握手必须被拒")
	}
}

// ── 拉取进度（image:pull，4b 进度面）─────────────────────────────────────

// TestPullSessionIDIsDerivedAndValid：进度会话句柄是 ref 的**确定性派生**（core 在
// 受理时预登记、agent 在开始拉取时重建 —— 两端必须得到同一个串），派生结果过
// IsDockerSessionID（帧校验与其他会话同一条闸）。
func TestPullSessionIDIsDerivedAndValid(t *testing.T) {
	ref := "1790000000000000001"
	got := DockerPullSessionID(ref)
	if got != "pull_"+ref {
		t.Fatalf("会话句柄必须是确定性派生: %q", got)
	}
	if !IsDockerSessionID(got) {
		t.Fatalf("派生结果必须过会话 id 校验: %q", got)
	}
	// 同一 ref 恒得同一句柄（core 与 agent 双方各自的派生必须一致）。
	if DockerPullSessionID(ref) != got {
		t.Fatal("派生必须稳定（同 ref 同句柄）")
	}
	// 不同 ref 不得撞号。
	if DockerPullSessionID("1790000000000000002") == got {
		t.Fatal("不同 ref 的句柄不得相同")
	}
}

// TestPullProgressItemValidate：终态标记互斥、负进度与超界进度拒绝、字段长度闸、
// 合法形态（进度行 / 消息行 / 终态行）放行。
func TestPullProgressItemValidate(t *testing.T) {
	ok := func(p DockerPullProgressItem) {
		t.Helper()
		if err := p.Validate(); err != nil {
			t.Fatalf("合法记录被拒: %+v %v", p, err)
		}
	}
	bad := func(p DockerPullProgressItem) {
		t.Helper()
		if err := p.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("非法记录必须被拒: %+v %v", p, err)
		}
	}

	ok(DockerPullProgressItem{T: 1, ID: "sha256:abc", Status: "Downloading",
		Current: 4096, Total: 8192})
	ok(DockerPullProgressItem{T: 1, Status: "Pulling from library/nginx"})
	ok(DockerPullProgressItem{T: 1, Done: true})
	ok(DockerPullProgressItem{T: 1, Error: "denied: requested access to the resource is denied"})

	bad(DockerPullProgressItem{Done: true, Error: "x"})      // 双终态标记
	bad(DockerPullProgressItem{})                            // 无时刻
	bad(DockerPullProgressItem{T: 1, Current: -1})           // 负进度
	bad(DockerPullProgressItem{T: 1, Current: 10, Total: 5}) // 超界进度
	bad(DockerPullProgressItem{T: 1, Status: strings.Repeat("x", MaxDockerStringBytes+1)})
}

// TestDockerRegistryAddrValidate：仓库地址形态闸 —— 主机名/IP + 可选端口，
// 协议头、镜像路径、空白、超长一律拒（它是 RegistryAuth.ServerAddress 与
// 凭据键的同一个值，非法形态不能进入凭据查找）。
func TestDockerRegistryAddrValidate(t *testing.T) {
	ok := []string{"docker.io", "gcr.io", "harbor.example.com:8443", "registry:5000",
		"localhost:5000", "192.168.1.5:5000", "REGISTRY.INTERNAL"}
	for _, s := range ok {
		if !IsDockerRegistryAddr(s) {
			t.Errorf("合法仓库地址被拒: %q", s)
		}
	}
	bad := []string{"", "https://registry.io", "registry.io/library/nginx",
		"registry.io:99999", " registry.io", "reg istry.io", "registry.io/v1",
		"-registry.io", "registry..io", strings.Repeat("a", 256)}
	for _, s := range bad {
		if IsDockerRegistryAddr(s) {
			t.Errorf("非法仓库地址必须被拒: %q", s)
		}
	}
}

// TestDockerCmdAuthValidate：认证三元组只许挂在 image:pull 上、registry 与
// options.registry 必须同一把键、空用户名/空密码/超长密码拒。
func TestDockerCmdAuthValidate(t *testing.T) {
	auth := &DockerRegistryAuth{Registry: "harbor.example.com", Username: "robot$ci", Password: "s3cret"}
	ok := &DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "harbor.example.com/app:1", Registry: "harbor.example.com"}, Auth: auth}
	if err := ok.Validate(); err != nil {
		t.Fatalf("带凭据的拉取指令应通过: %v", err)
	}

	bad := func(cmd *DockerCmd) {
		t.Helper()
		if err := cmd.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("非法认证指令必须被拒: %+v %v", cmd, err)
		}
	}
	bad(&DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "x"}, Auth: auth}) // options 没有 registry
	bad(&DockerCmd{Ref: "1", Action: DockerActionImageRemove,
		Options: DockerCmdOptions{Target: "harbor.example.com/app", Registry: "harbor.example.com"}, Auth: auth}) // 非 pull
	bad(&DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "x", Registry: "other.example.com"}, Auth: auth}) // 键漂移
	bad(&DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "x", Registry: "harbor.example.com"},
		Auth:    &DockerRegistryAuth{Registry: "harbor.example.com", Username: "u", Password: ""}}) // 空密码
	bad(&DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "x", Registry: "harbor.example.com"},
		Auth:    &DockerRegistryAuth{Registry: "harbor.example.com", Username: "", Password: "p"}}) // 空用户名
	bad(&DockerCmd{Ref: "1", Action: DockerActionImagePull,
		Options: DockerCmdOptions{Target: "x", Registry: "harbor.example.com"},
		Auth: &DockerRegistryAuth{Registry: "harbor.example.com", Username: "u",
			Password: strings.Repeat("p", maxDockerRegistrySecretBytes+1)}}) // 超长密码
}

// TestDockerCmdOptionsRegistryOwnership：registry 字段只归 image:pull；
// 挂在其它 action 上是字段归属错误（照 backup 的先例），而不是静默无视。
func TestDockerCmdOptionsRegistryOwnership(t *testing.T) {
	if err := ValidateDockerCmdOptions(DockerActionImagePull,
		&DockerCmdOptions{Target: "x", Registry: "harbor.example.com"}); err != nil {
		t.Fatalf("pull 带仓库字段应通过: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionContainerLogs,
		&DockerCmdOptions{Target: "x", Registry: "harbor.example.com"}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("logs 带仓库字段必须被拒: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionImagePull,
		&DockerCmdOptions{Target: "x", Registry: "https://harbor.example.com"}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("带协议头的仓库地址必须被拒: %v", err)
	}
}

// TestDockerEventExitCodeRoundTrip：exit_code 是 die 事件的可选属性（omitempty）——
// 帧行里 nil 时**不出键**（旧 core 也照常解码，字段是追加式的），非 nil 时数值
// 原样往返。旧行（无 exit_code）解码后 ExitCode 必须是 nil（不可考与 0 是两个语义）。
func TestDockerEventExitCodeRoundTrip(t *testing.T) {
	item := DockerEventItem{T: 1, Type: "container", Action: "die",
		ActorName: "web", ActorID: "ab12"}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "exit_code") {
		t.Fatalf("nil 退出码不得出键（旧解析器兼容）: %s", b)
	}
	var dec DockerEventItem
	if err := json.Unmarshal(b, &dec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dec.ExitCode != nil {
		t.Fatalf("无 exit_code 的行解码后应为 nil，实际 %v", *dec.ExitCode)
	}
	code := int32(137)
	item.ExitCode = &code
	b, err = json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal with code: %v", err)
	}
	if err := json.Unmarshal(b, &dec); err != nil {
		t.Fatalf("unmarshal with code: %v", err)
	}
	if dec.ExitCode == nil || *dec.ExitCode != 137 {
		t.Fatalf("退出码往返不符: %v", dec.ExitCode)
	}
}
