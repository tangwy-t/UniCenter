package dockerops

import (
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

// newWriteExecutor 构造接到替身上的写执行器（CI 没有 docker daemon：全部写路径走替身，
// 不连 docker.sock、不真跑 docker 命令）。
func newWriteExecutor(api DockerAPI, protected, transferDir string) *WriteExecutor {
	return NewWriteExecutor(api, ParseProtected(protected), transferDir, agentproto.DockerComposeFlavorPlugin, nil)
}

// writeCalls 汇总替身记下的写调用条数（判「拒绝时绝不能触达 API」）。
func writeCalls(s *stubAPI) int {
	return len(s.started) + len(s.stopped) + len(s.restarted) + len(s.removed) +
		len(s.imagesRemoved) + len(s.imagePrunedAll) + len(s.pulled) + len(s.tagged) +
		len(s.saved) + len(s.loaded) + len(s.volumesRemoved) + s.volumePruneCalls + len(s.networksRemoved)
}

// 保护清单从「标注」变成「拒绝依据」是本任务的重点：命中且未带 force 时必须
// **在触达 daemon 之前**拒绝；结论句是中文结论（不含字段名/内部术语），细节只进 detail。
//
// 容器类的项目/服务粒度只能靠 compose 标签命中，故用例里放了一个「名字不在清单、
// 但属于 project:uni-center」的容器 —— 它必须被拒，否则那两条粒度是摆设。
func TestWriteExecutorProtectedRequiresForce(t *testing.T) {
	api := &stubAPI{containers: []ContainerInfo{
		{ID: "c1", Name: "uni-center-core",
			Labels: map[string]string{composeProjectLabel: "uni-center", composeServiceLabel: "uni_core"}},
		{ID: "c2", Name: "brand-new-svc-1",
			Labels: map[string]string{composeProjectLabel: "uni-center", composeServiceLabel: "brand-new"}},
		{ID: "c3", Name: "zentao"},
		// 裸容器（无 compose 标签）：按 ID 发来的操作要换算回名字才能命中名字粒度。
		{ID: "d4", Name: "mysql"},
	}}
	e := newWriteExecutor(api, defaultProtected, t.TempDir())
	const wantMsg = "这是受保护的目标，需要显式的强制确认才能操作"

	cases := []struct {
		name    string
		action  string
		opts    agentproto.DockerCmdOptions
		wantErr bool
	}{
		{"受保护的容器按名字：无 force 拒绝", agentproto.DockerActionContainerStop,
			agentproto.DockerCmdOptions{Target: "uni-center-core"}, true},
		{"受保护的容器按 ID：也要命中名字粒度", agentproto.DockerActionContainerStop,
			agentproto.DockerCmdOptions{Target: "d4"}, true},
		{"受保护项目的**新**服务也命中（项目粒度）", agentproto.DockerActionContainerStop,
			agentproto.DockerCmdOptions{Target: "brand-new-svc-1"}, true},
		{"受保护容器删除：无 force 拒绝", agentproto.DockerActionContainerRemove,
			agentproto.DockerCmdOptions{Target: "uni-center-core"}, true},
		{"不受保护的容器：无 force 放行", agentproto.DockerActionContainerStop,
			agentproto.DockerCmdOptions{Target: "zentao"}, false},
		{"受保护容器：带 force 放行", agentproto.DockerActionContainerStop,
			agentproto.DockerCmdOptions{Target: "uni-center-core", Force: true}, false},
		{"受保护的卷：无 force 拒绝", agentproto.DockerActionVolumeRemove,
			agentproto.DockerCmdOptions{Target: "uni-center-uploads"}, true},
		{"不受保护的卷：放行", agentproto.DockerActionVolumeRemove,
			agentproto.DockerCmdOptions{Target: "other-volume"}, false},
		{"受保护的卷：带 force 放行", agentproto.DockerActionVolumeRemove,
			agentproto.DockerCmdOptions{Target: "uni-center-uploads", Force: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := writeCalls(api)
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{Action: c.action, Options: c.opts})
			var ee *ExecError
			if c.wantErr {
				if !errors.As(err, &ee) {
					t.Fatalf("必须被保护档拒绝，实际 err=%v", err)
				}
				if ee.Msg != wantMsg {
					t.Fatalf("结论句不符: %q", ee.Msg)
				}
				if !strings.Contains(ee.Detail, "protected target:") {
					t.Fatalf("detail 必须能看出命中的是哪个目标: %q", ee.Detail)
				}
				if writeCalls(api) != before {
					t.Fatal("被拒绝的指令绝不能触达 Docker API")
				}
				return
			}
			if err != nil {
				t.Fatalf("应当放行，实际 %v", err)
			}
			if writeCalls(api) == before {
				t.Fatal("放行的指令必须真的执行")
			}
		})
	}
}

// force 是「保护覆盖」与「docker rm -f / rmi -f / volume rm -f」的**同一位**：
// 放行之后必须原样传给 daemon（保护拒绝与强制删除不能各用一套开关）。
func TestWriteExecutorForwardsForce(t *testing.T) {
	api := &stubAPI{}
	e := newWriteExecutor(api, "", t.TempDir())
	ctx := context.Background()
	for _, cmd := range []*agentproto.DockerCmd{
		{Action: agentproto.DockerActionContainerRemove, Options: agentproto.DockerCmdOptions{Target: "c", Force: true}},
		{Action: agentproto.DockerActionImageRemove, Options: agentproto.DockerCmdOptions{Target: "img:1", Force: true}},
		{Action: agentproto.DockerActionVolumeRemove, Options: agentproto.DockerCmdOptions{Target: "v", Force: true}},
	} {
		if _, err := e.Do(ctx, cmd); err != nil {
			t.Fatalf("%s 应放行: %v", cmd.Action, err)
		}
	}
	if len(api.removed) != 1 || !api.removed[0].force {
		t.Fatalf("container:remove 的 force 未透传: %+v", api.removed)
	}
	if len(api.imagesRemoved) != 1 || !api.imagesRemoved[0].force {
		t.Fatalf("image:remove 的 force 未透传: %+v", api.imagesRemoved)
	}
	if len(api.volumesRemoved) != 1 || !api.volumesRemoved[0].force {
		t.Fatalf("volume:remove 的 force 未透传: %+v", api.volumesRemoved)
	}
}

// 「文件名」概念上就是文件名：任何带路径分隔符或 .. 的输入都必须在拼接**之前**被拒。
// 协议层已按正则校验过，这里是纵深防御 —— agent 是最后一道。
func TestWriteExecutorRejectsPathInFilename(t *testing.T) {
	api := &stubAPI{}
	e := newWriteExecutor(api, "", t.TempDir())
	bad := []string{
		"../evil.tar", "a/b.tar", "/etc/passwd.tar", `..\evil.tar`,
		"x.tar/../y.tar", "sub/../../x.tar", "..", ".",
	}
	for _, filename := range bad {
		for _, c := range []struct {
			action string
			opts   agentproto.DockerCmdOptions
		}{
			{agentproto.DockerActionImageSave, agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: filename}},
			{agentproto.DockerActionImageLoad, agentproto.DockerCmdOptions{Filename: filename}},
		} {
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{Action: c.action, Options: c.opts})
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("文件名 %q 必须被拒，实际 err=%v", filename, err)
			}
			if strings.Contains(ee.Msg, "filename") || strings.ContainsAny(ee.Msg, `/\`) {
				t.Fatalf("结论句不得出现字段名或路径: %q", ee.Msg)
			}
		}
	}
	if len(api.saved)+len(api.loaded) != 0 {
		t.Fatal("非法文件名绝不能触达 Docker API / 文件系统")
	}
}

// save 的默认动作是**不覆盖**：目标已存在时回 already_exists 语义（失败结论 + 标志位），
// 用户确认后带 overwrite=true 重发才允许截断。标志位必须一路走到 result（core 靠它
// 让前端问「覆盖吗」——两段确认复用 cmd/result）。
func TestWriteExecutorSaveAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	api := &stubAPI{saveAlreadyExists: true}
	e := newWriteExecutor(api, "", dir)
	ctx := context.Background()
	wantPath := filepath.Join(dir, "mysql.tar")

	_, err := e.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("已存在时必须拒绝覆盖，实际 %v", err)
	}
	if !ee.AlreadyExists {
		t.Fatalf("拒绝必须带 already_exists 语义: %+v", ee)
	}
	if len(api.saved) != 1 || api.saved[0].overwrite {
		t.Fatalf("首轮必须走独占创建（overwrite=false）: %+v", api.saved)
	}
	if api.saved[0].path != wantPath {
		t.Fatalf("路径必须由 transferDir+文件名拼出，实际 %q", api.saved[0].path)
	}

	// 覆盖重发：成功，载荷带产物路径（spec §7.5：save/load 的 result 都带路径与大小）。
	payload, err := e.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar", Overwrite: true}})
	if err != nil {
		t.Fatalf("确认覆盖后必须放行: %v", err)
	}
	if len(api.saved) != 2 || !api.saved[1].overwrite {
		t.Fatalf("重发必须带 overwrite=true: %+v", api.saved)
	}
	var tp transferPayload
	if err := json.Unmarshal(payload, &tp); err != nil || tp.Path != wantPath {
		t.Fatalf("产物载荷不符: err=%v payload=%+v", err, tp)
	}

	// 结果层：dispatcher 必须把 already_exists 搬到 result。
	sink := &collectSink{}
	d := NewDispatcher(e, sink.send, testLogger())
	d.Handle(ctx, &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"},
		Confirm: "mysql.tar"})
	d.ExecuteNow(ctx, "1")
	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("必须回一条结果，实际 %d", len(got))
	}
	if got[0].OK || !got[0].AlreadyExists || got[0].Error == "" {
		t.Fatalf("结果必须透出 already_exists 且是失败结论: %+v", got[0])
	}
	if err := got[0].Validate(); err != nil {
		t.Fatalf("结果必须能过协议校验: %v", err)
	}
}

// dst 不带 tag 时补 :latest（与 docker tag 一致）；注册表主机段里的端口号不是 tag。
func TestWriteExecutorImageTagDefaultsToLatest(t *testing.T) {
	cases := []struct{ dst, want string }{
		{"myimg", "myimg:latest"},
		{"team/myimg", "team/myimg:latest"},
		{"registry.local:5000/team/myimg", "registry.local:5000/team/myimg:latest"},
		{"myimg:v1", "myimg:v1"},
		{"registry.local:5000/team/myimg:v2", "registry.local:5000/team/myimg:v2"},
	}
	for _, c := range cases {
		t.Run(c.dst, func(t *testing.T) {
			api := &stubAPI{}
			e := newWriteExecutor(api, "", t.TempDir())
			if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Action: agentproto.DockerActionImageTag,
				Options: agentproto.DockerCmdOptions{Src: "mysql:8.0.22", Dst: c.dst}}); err != nil {
				t.Fatal(err)
			}
			if len(api.tagged) != 1 || api.tagged[0].dst != c.want || api.tagged[0].src != "mysql:8.0.22" {
				t.Fatalf("tag 调用不符: %+v, want dst=%q", api.tagged, c.want)
			}
		})
	}
}

// 路径纪律三件事：配置目录不可用 → 回退 /tmp 且成功结果的 detail 留痕；
// 目录可用 → 落在配置目录；load 侧符号链接逃逸被堵住、目录内真实文件放行。
func TestWriteExecutorTransferPathDiscipline(t *testing.T) {
	ctx := context.Background()

	// ① 配置目录不存在：回退 /tmp，不报错中断，detail 注明。
	api := &stubAPI{}
	missing := filepath.Join(t.TempDir(), "not-there")
	e := newWriteExecutor(api, "", missing)
	if _, err := e.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}}); err != nil {
		t.Fatalf("目录不可用必须回退而不是失败: %v", err)
	}
	if want := filepath.Join(os.TempDir(), "mysql.tar"); api.saved[0].path != want {
		t.Fatalf("应回退到 /tmp，实际 %q", api.saved[0].path)
	}
	if note := e.takeNote(); !strings.Contains(note, "回退") {
		t.Fatalf("回退必须在成功结果的 detail 里注明，实际 %q", note)
	}

	// ② 目录可用：落在配置目录，且没有回退留痕。
	dir := t.TempDir()
	api2 := &stubAPI{}
	e2 := newWriteExecutor(api2, "", dir)
	if _, err := e2.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"}}); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "mysql.tar"); api2.saved[0].path != want {
		t.Fatalf("应落在配置目录，实际 %q", api2.saved[0].path)
	}
	if note := e2.takeNote(); note != "" {
		t.Fatalf("目录可用时不该有回退留痕: %q", note)
	}

	// ③ load 的符号链接逃逸：目录里的链接指向目录外 → 必须在读之前拒绝。
	dir3 := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.tar")
	if err := os.WriteFile(outside, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir3, "escape.tar")); err != nil {
		t.Fatal(err)
	}
	api3 := &stubAPI{}
	e3 := newWriteExecutor(api3, "", dir3)
	_, err := e3.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageLoad,
		Options: agentproto.DockerCmdOptions{Filename: "escape.tar"}})
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "不在允许的目录内") {
		t.Fatalf("符号链接逃逸必须被拒，实际 %v", err)
	}
	if len(api3.loaded) != 0 {
		t.Fatal("被拒的 load 绝不能触达 Docker API")
	}

	// ④ 目录内的真实文件放行（实际交出去的是解析后的真实路径）。
	inside := filepath.Join(dir3, "ok.tar")
	if err := os.WriteFile(inside, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e3.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageLoad,
		Options: agentproto.DockerCmdOptions{Filename: "ok.tar"}}); err != nil {
		t.Fatalf("目录内的产物必须放行: %v", err)
	}
	realDir, err := filepath.EvalSymlinks(dir3)
	if err != nil {
		t.Fatal(err)
	}
	if len(api3.loaded) != 1 || api3.loaded[0] != filepath.Join(realDir, "ok.tar") {
		t.Fatalf("load 路径不符: %v", api3.loaded)
	}
}

// prune 的释放量是「成功但必须留痕」的信息：结构化载荷给前端，detail 留痕给审计与
// 二期真机验收（D1 的期望是 detail 里能看到 space_reclaimed）。
func TestWriteExecutorPruneReportsFreedSpace(t *testing.T) {
	api := &stubAPI{imagePruneFreed: 48 << 20, volumePruneFreed: 7 << 20}
	e := newWriteExecutor(api, "", t.TempDir())
	ctx := context.Background()

	payload, err := e.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImagePrune,
		Options: agentproto.DockerCmdOptions{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	var p writeSpacePayload
	if err := json.Unmarshal(payload, &p); err != nil || p.SpaceReclaimedBytes != 48<<20 {
		t.Fatalf("释放量载荷不符: err=%v %+v", err, p)
	}
	if len(api.imagePrunedAll) != 1 || !api.imagePrunedAll[0] {
		t.Fatalf("all 标志必须原样传给 daemon（all=true = 清全部未使用）: %v", api.imagePrunedAll)
	}
	if note := e.takeNote(); !strings.Contains(note, "space_reclaimed=50331648") {
		t.Fatalf("detail 必须能看到 space_reclaimed，实际 %q", note)
	}

	if _, err := e.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionVolumePrune}); err != nil {
		t.Fatal(err)
	}
	if api.volumePruneCalls != 1 {
		t.Fatalf("volume:prune 未执行: %d", api.volumePruneCalls)
	}
	if note := e.takeNote(); !strings.Contains(note, "space_reclaimed=7340032") {
		t.Fatalf("卷清理同样要留痕，实际 %q", note)
	}
}

// writeTimeouts 是 spec §4.3.1 的二期行（含四期三条）**逐行镜像**：少一行会让那条
// 指令被 5 分钟兜底提前判死，值漂移会让 agent 与 core 的 sweep 对「超时」给出两个答案。
func TestWriteTimeoutsMirrorSpec(t *testing.T) {
	spec := map[string]time.Duration{
		agentproto.DockerActionContainerStart:   30 * time.Second,
		agentproto.DockerActionContainerStop:    30 * time.Second,
		agentproto.DockerActionContainerRestart: 60 * time.Second,
		agentproto.DockerActionContainerRemove:  60 * time.Second,

		agentproto.DockerActionImageRemove: 60 * time.Second,
		agentproto.DockerActionImagePrune:  120 * time.Second,
		agentproto.DockerActionImagePull:   15 * time.Minute,
		agentproto.DockerActionImageTag:    30 * time.Second,
		agentproto.DockerActionImageSave:   15 * time.Minute,
		agentproto.DockerActionImageLoad:   15 * time.Minute,

		agentproto.DockerActionVolumeRemove: 60 * time.Second,
		agentproto.DockerActionVolumePrune:  120 * time.Second,

		agentproto.DockerActionNetworkRemove: 60 * time.Second,

		agentproto.DockerActionComposeUp:      120 * time.Second,
		agentproto.DockerActionComposeStop:    120 * time.Second,
		agentproto.DockerActionComposeStart:   120 * time.Second,
		agentproto.DockerActionComposeRestart: 120 * time.Second,
		agentproto.DockerActionComposePull:    15 * time.Minute,
		agentproto.DockerActionComposeDown:    120 * time.Second,

		agentproto.DockerActionComposeServiceScale:            120 * time.Second,
		agentproto.DockerActionComposeServiceRemoveContainers: 60 * time.Second,

		// 四期：配置编辑（写入是本地文件操作 + 一次 config -q 预检）。
		agentproto.DockerActionComposeFileValidate: 30 * time.Second,
		agentproto.DockerActionComposeFileWrite:    30 * time.Second,
		agentproto.DockerActionComposeFilePatch:    30 * time.Second,
	}
	if len(writeTimeouts) != len(spec) {
		t.Fatalf("时限镜像 %d 行，spec %d 行（逐行对齐是这张表存在的意义）", len(writeTimeouts), len(spec))
	}
	for action, want := range spec {
		got, ok := writeTimeouts[action]
		if !ok {
			t.Errorf("%s 不在时限镜像里：它会被 %v 的兜底提前判死", action, dispatchTimeoutCap)
			continue
		}
		if got != want {
			t.Errorf("%s 时限 = %v，spec = %v", action, got, want)
		}
	}
	// 15 分钟那四条（pull/save/load + compose:pull）正是这张表存在的理由：
	// 兜底 5 分钟会把「其实还在拉」显示成超时。
	for _, action := range []string{
		agentproto.DockerActionImagePull,
		agentproto.DockerActionImageSave,
		agentproto.DockerActionImageLoad,
		agentproto.DockerActionComposePull,
	} {
		if got := actionTimeout(action); got != 15*time.Minute || got <= dispatchTimeoutCap {
			t.Fatalf("%s 时限 = %v，必须等于 15 分钟且大于兜底 %v", action, got, dispatchTimeoutCap)
		}
	}
}

// B1 的写 action 必须已登记为「已实现」，否则控制台上的按钮点了只会得到
// 「该操作尚未开放」（那是 B2/升级的语义，不是本版的能力）。
func TestImplementedActionsIncludesPhase2Writes(t *testing.T) {
	want := []string{
		agentproto.DockerActionContainerStart,
		agentproto.DockerActionContainerStop,
		agentproto.DockerActionContainerRestart,
		agentproto.DockerActionContainerRemove,
		agentproto.DockerActionImageRemove,
		agentproto.DockerActionImagePrune,
		agentproto.DockerActionImagePull,
		agentproto.DockerActionImageTag,
		agentproto.DockerActionImageSave,
		agentproto.DockerActionImageLoad,
		agentproto.DockerActionVolumeRemove,
		agentproto.DockerActionVolumePrune,
		agentproto.DockerActionNetworkRemove,
	}
	for _, a := range want {
		if !implementedActions[a] {
			t.Errorf("%s 已由 WriteExecutor 实现，必须登记为已实现", a)
		}
	}
}

// Runtime 的单一执行器入口必须按 action 分流：写 action 送写执行器，读 action 仍走
// 只读执行器；且 hello_ack 下发的保护清单要**立刻**送达写执行器（构造时的旧清单失效
// 是本模块最不可逆的失败模式）。
func TestRuntimeRoutesAndInjectsWriteConfig(t *testing.T) {
	api := &stubAPI{
		containers:  []ContainerInfo{{ID: "c1", Name: "mysql"}},
		imageDetail: ImageDetail{ID: "sha256:aaa", RepoTags: []string{"mysql:8.0.22"}},
	}
	r := New(Deps{
		StateDir:   t.TempDir(),
		SendState:  func(*agentproto.DockerState) error { return nil },
		SendResult: func(*agentproto.DockerCmdResult) error { return nil },
		Log:        testLogger(), API: api,
		Now: func() time.Time { return time.Unix(1790000000, 0) },
	})
	r.OnConfig(&agentproto.DockerConfig{ConfigVersion: 1, Protected: "mysql"})

	ctx := context.Background()
	_, err := r.exec.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionContainerStop,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "这是受保护的目标，需要显式的强制确认才能操作" {
		t.Fatalf("配置下发后写执行器必须按新清单拒绝，实际 %v", err)
	}
	if len(api.stopped) != 0 {
		t.Fatal("被保护档拒绝的停止绝不能触达 Docker API")
	}

	// 读 action 仍走只读执行器（不得落进写执行器的「尚未开放」分支）。
	payload, err := r.exec.Do(ctx, &agentproto.DockerCmd{Action: agentproto.DockerActionImageInspect,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22"}})
	if err != nil {
		t.Fatalf("读 action 必须仍走只读执行器: %v", err)
	}
	var detail agentproto.DockerImageInspectPayload
	if err := json.Unmarshal(payload, &detail); err != nil || detail.ID != "sha256:aaa" {
		t.Fatalf("只读载荷不符: err=%v %+v", err, detail)
	}
}

// 附注（目录回退 / space_reclaimed）不得串台：失败路径上设置的附注若留在执行器里，
// 会挂到**下一条**指令的结果上；读 action 不清附注，串台只会在读结果上现形。
func TestDispatcherConsumesNoteEvenOnFailure(t *testing.T) {
	// save 会先走 transferPath：目录不存在 → 回退 /tmp 并留下附注；随后 already_exists
	// 让这条指令以失败收场 —— 正是「失败路径留下附注」的场景。
	api := &stubAPI{saveAlreadyExists: true}
	e := newWriteExecutor(api, "", filepath.Join(t.TempDir(), "not-there"))
	sink := &collectSink{}
	d := NewDispatcher(e, sink.send, testLogger())
	ctx := context.Background()

	d.Handle(ctx, &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionImageSave,
		Options: agentproto.DockerCmdOptions{Target: "mysql:8.0.22", Filename: "mysql.tar"},
		Confirm: "mysql.tar"})
	d.ExecuteNow(ctx, "1")
	d.Handle(ctx, &agentproto.DockerCmd{Ref: "2", Action: agentproto.DockerActionContainerInspect,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	d.ExecuteNow(ctx, "2")

	got := sink.all()
	if len(got) != 2 {
		t.Fatalf("两条都应有结果，实际 %d", len(got))
	}
	if got[0].OK || !got[0].AlreadyExists {
		t.Fatalf("第一条应是 already_exists 的失败结论: %+v", got[0])
	}
	if got[1].Detail != "" {
		t.Fatalf("上一条失败的附注串到了下一条读结果: %q", got[1].Detail)
	}
}
