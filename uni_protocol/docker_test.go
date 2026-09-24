package agentproto

import (
	"encoding/json"
	"errors"
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
	if checked != 6 {
		t.Fatalf("应检查 6 个 docker golden（5 条消息 + hello_ack 的配置块），实际 %d 个", checked)
	}
}

// 白名单与总表必须双向一致：漏一条 → 该 action 永远收不到（agent 判未知）；
// 多一条 → 协议承诺了一个没有策略的动作。
func TestDockerActionWhiteListIsComplete(t *testing.T) {
	all := AllDockerActions()
	if len(all) != 29 {
		t.Fatalf("action 白名单应为 29 条（spec §12.1），实际 %d 条: %v", len(all), all)
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

// 必填清单里的字段名必须真的存在：名字写错会让必填校验恒真（永远「已填」），
// 这类错误在协议层是静默的 —— 只有把「填了就该非空」钉住才看得见。
func TestDockerOptionFieldNamesResolve(t *testing.T) {
	filled := &DockerCmdOptions{
		Target: "x", Tail: 1, Since: 1, N: intPtr(1), Force: true,
		Filename: "x.tar", Overwrite: true, All: true, RemoveOrphans: true,
		Volumes: true, Content: "x", BaseHash: strings.Repeat("a", 64),
		Src: "a", Dst: "b", Patch: map[string]any{"services": map[string]any{}},
	}
	for _, f := range dockerOptionFields {
		if dockerOptionValue(filled, f) == "" {
			t.Errorf("字段 %q 已填但取值函数返回空 —— 必填校验会恒真", f)
		}
	}
	if dockerOptionValue(filled, "nonexistent") != "" {
		t.Error("未知字段名必须返回空串（名字写错 = 必填失败，而不是静默放行）")
	}
	// 每条 spec 的必填项都必须在字段清单里
	for _, spec := range dockerActionSpecs {
		for _, f := range spec.Required {
			found := false
			for _, known := range dockerOptionFields {
				if known == f {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("action %s 的必填项 %q 不在字段清单里", spec.Action, f)
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
	save := &DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}
	if got := ExpectedDockerConfirm(DockerActionImageSave, save); got != "mysql.tar" {
		t.Fatalf("save 覆盖的确认值应是文件名，实际 %q", got)
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
