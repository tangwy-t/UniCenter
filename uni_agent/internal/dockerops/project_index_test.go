package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 索引本身：持久化 / 只在变化时写盘 / 损坏与写失败的自愈取向 ────────────

// 学习 → 落盘 → 重新构造实例能读回（模拟 agent 重启）。没有这条，down 之后
// up 依然解析不到路径 —— 索引的全部价值就在「重启后还记得」。
func TestProjectIndexPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	idx := newProjectIndex(dir, testLogger())

	// 空值不学：缺项目名/缺路径都是「没有观测」，学了就是编造。
	idx.Learn("", "/data/a/docker-compose.yml")
	idx.Learn("uni-center", "")
	if _, ok := idx.Lookup("uni-center"); ok {
		t.Fatal("空值不得学习")
	}

	idx.Learn("uni-center", "/data/UniCenter/docker-compose.yml")
	idx.Learn("other", "/srv/other/compose.yml")

	path := filepath.Join(dir, projectIndexFileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("索引必须落盘: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("索引落盘权限必须 0600（只有 agent 能读写），实际 %o", perm)
	}

	// 模拟 agent 重启：从同一状态目录重新构造。
	reloaded := newProjectIndex(dir, testLogger())
	if got, ok := reloaded.Lookup("uni-center"); !ok || got != "/data/UniCenter/docker-compose.yml" {
		t.Fatalf("重启后必须读回索引，got %q ok=%v", got, ok)
	}
	if got, ok := reloaded.Lookup("other"); !ok || got != "/srv/other/compose.yml" {
		t.Fatalf("第二个项目也必须读回，got %q ok=%v", got, ok)
	}
}

// 值没变不写盘：快照 30s 一次，同值反复落盘没有意义。用哨兵文件证明
// 「同值 Learn 不写盘、变值 Learn 必写盘」。
func TestProjectIndexWritesOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	idx := newProjectIndex(dir, testLogger())
	idx.Learn("uni-center", "/data/UniCenter/docker-compose.yml")

	path := filepath.Join(dir, projectIndexFileName)
	if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx.Learn("uni-center", "/data/UniCenter/docker-compose.yml")
	if b, _ := os.ReadFile(path); string(b) != "sentinel" {
		t.Fatalf("同值学习不得写盘，实际落盘内容 %q", b)
	}

	idx.Learn("uni-center", "/data/UniCenter/compose.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "sentinel" {
		t.Fatal("值变化必须写盘")
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["uni-center"] != "/data/UniCenter/compose.yml" {
		t.Fatalf("落盘内容不符: %v", m)
	}
}

// 落盘文件损坏：只 Warn、从空开始（不得 panic、不得影响启动），并且还能继续自愈。
func TestProjectIndexCorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectIndexFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := testLogger()
	idx := newProjectIndex(dir, log) // 不得 panic
	if _, ok := idx.Lookup("uni-center"); ok {
		t.Fatal("损坏的索引必须从空开始")
	}
	if len(log.warns) == 0 {
		t.Fatal("损坏必须留下 Warn（而不是静默）")
	}
	idx.Learn("uni-center", "/data/UniCenter/docker-compose.yml")
	if got, ok := idx.Lookup("uni-center"); !ok || got != "/data/UniCenter/docker-compose.yml" {
		t.Fatalf("损坏后索引必须能自愈（下一次快照重建），got %q ok=%v", got, ok)
	}
}

// 索引写入失败（状态目录不存在 = 任何进程都写不进）：只 Warn，内存优先 ——
// Lookup 照常可用，不阻断当前进程的任何操作。
func TestProjectIndexPersistenceFailureIsWarnOnly(t *testing.T) {
	unwritable := filepath.Join(t.TempDir(), "missing", "state")
	log := testLogger()
	idx := newProjectIndex(unwritable, log)
	idx.Learn("uni-center", "/data/UniCenter/docker-compose.yml")

	if got, ok := idx.Lookup("uni-center"); !ok || got != "/data/UniCenter/docker-compose.yml" {
		t.Fatalf("落盘失败不得影响内存索引（内存优先），got %q ok=%v", got, ok)
	}
	if len(log.warns) == 0 {
		t.Fatal("落盘失败必须留下 Warn（可见但不阻断）")
	}
}

// ── 解析路径：标签 → 索引 → 可操作结论句 ────────────────────────────────

// down 之后的项目（没有任何容器）：标签解析不到 → 索引命中 → argv 里的 -f 是
// 索引记下的绝对路径。用「从同一目录重新加载的索引」驱动，连重启一起模拟。
func TestComposeExecutorResolvesConfigFileFromIndexWithoutContainers(t *testing.T) {
	configFile := writeComposeFixture(t)
	dir := t.TempDir()
	newProjectIndex(dir, testLogger()).Learn("uni-center", configFile)
	// 模拟 agent 重启：执行器只认识从状态目录读回的索引。
	reloaded := newProjectIndex(dir, testLogger())

	stub := &composeExecStub{}
	e := NewWriteExecutor(withProjectIndex(&stubAPI{}, reloaded), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	}); err != nil {
		t.Fatalf("没有容器的项目必须能靠索引解析并放行: %v", err)
	}
	call := stub.last(t)
	want := []string{"compose", "-p", "uni-center", "-f", configFile, "up", "-d", "--remove-orphans"}
	if !slices.Equal(call.args, want) {
		t.Fatalf("argv 的 -f 必须是索引记下的绝对路径:\n got %q\nwant %q", call.args, want)
	}
}

// 只读路径（compose.file:read）与写路径共用同一份解析：没有容器时同样靠索引读到文件。
func TestComposeFileReadResolvesFromIndexWithoutContainers(t *testing.T) {
	configFile := writeComposeFixture(t)
	idx := newProjectIndex(t.TempDir(), testLogger())
	idx.Learn("uni-center", configFile)

	e := NewReadExecutor(withProjectIndex(&stubAPI{}, idx))
	raw, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileRead, Options: composeOpts("uni-center"),
	})
	if err != nil {
		t.Fatalf("没有容器的项目必须能靠索引读到配置文件: %v", err)
	}
	var p agentproto.DockerComposeFilePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Path != configFile || !strings.Contains(p.Content, "services") {
		t.Fatalf("载荷必须来自索引记下的路径: %+v", p)
	}
}

// 从未见过的项目：结论句必须是**可操作的指引**（告诉用户先在主机上把项目起一次），
// 而不是旧的笼统文案「没有找到这个项目」；且绝不触达 CLI。
func TestComposeExecutorUnknownProjectConclusionIsActionable(t *testing.T) {
	stub := &composeExecStub{}
	e := NewWriteExecutor(withProjectIndex(&stubAPI{}, newProjectIndex(t.TempDir(), testLogger())),
		ParseProtected(""), t.TempDir(), agentproto.DockerComposeFlavorPlugin, stub.command)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("从未见过的项目必须给结论句，实际 %v", err)
	}
	if !strings.Contains(ee.Msg, "先在主机上执行一次") {
		t.Fatalf("结论句必须给可操作指引，实际 %q", ee.Msg)
	}
	if strings.Contains(ee.Msg, "没有找到这个项目") {
		t.Fatalf("不得再用笼统的旧文案（它把「路径未知」误报成「项目不存在」），实际 %q", ee.Msg)
	}
	if stub.count() != 0 {
		t.Fatal("解析不到路径绝不能执行 CLI")
	}
}

// 索引里的文件已被删除：结论句说明「配置文件已不存在」，且**不**把失效路径塞进 argv。
func TestComposeExecutorIndexedConfigFileDeleted(t *testing.T) {
	configFile := writeComposeFixture(t)
	idx := newProjectIndex(t.TempDir(), testLogger())
	idx.Learn("uni-center", configFile)
	if err := os.Remove(configFile); err != nil {
		t.Fatal(err)
	}

	stub := &composeExecStub{}
	e := NewWriteExecutor(withProjectIndex(&stubAPI{}, idx), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("失效的索引条目必须给结论句，实际 %v", err)
	}
	if !strings.Contains(ee.Msg, "不存在") {
		t.Fatalf("结论句必须说明配置文件已不存在，实际 %q", ee.Msg)
	}
	if strings.Contains(ee.Msg, configFile) {
		t.Fatalf("失效路径不得出现在面向用户的结论句里: %q", ee.Msg)
	}
	if stub.count() != 0 {
		t.Fatal("失效路径绝不能进入 argv / 触达 CLI")
	}
}

// ── Runtime：索引的持有与注入（快照学习 + 执行器解析 + 重启读回）─────────

// Runtime 必须持有索引、注入快照器与两个执行器；快照学到的东西要落盘，
// 重启后的新实例（包括它的执行器）要能继续用。
func TestRuntimeInjectsProjectIndex(t *testing.T) {
	configFile := writeComposeFixture(t)
	api := &stubAPI{containers: []ContainerInfo{{
		ID: "c1", Name: "uni-center-core", State: "running",
		Labels: map[string]string{
			composeProjectLabel:     "uni-center",
			composeServiceLabel:     "uni_core",
			composeConfigFilesLabel: configFile,
		},
	}}}
	sink := &stateSink{}
	dir := t.TempDir()
	deps := func() Deps {
		return Deps{StateDir: dir, SendState: sink.state, SendResult: sink.result,
			Log: testLogger(), API: api, Now: func() time.Time { return time.Unix(1790000000, 0) }}
	}

	r := New(deps())
	if r.projects == nil {
		t.Fatal("Runtime 必须持有项目索引")
	}
	if r.snapshotter.projects != r.projects {
		t.Fatal("索引必须注入快照器（Collect 据此学习）")
	}
	if projectIndexOfAPI(r.write.api) != r.projects {
		t.Fatal("写执行器的 API 句柄必须携带索引（compose 写路径据此回落）")
	}
	ne, ok := r.exec.(*nodeExecutor)
	if !ok || projectIndexOfAPI(ne.read.api) != r.projects {
		t.Fatal("读执行器的 API 句柄必须携带索引（compose.file:read 同理）")
	}

	r.snapshotter.Collect(context.Background())
	if got, ok := r.projects.Lookup("uni-center"); !ok || got != configFile {
		t.Fatalf("一帧快照必须把标签学进索引，got %q ok=%v", got, ok)
	}

	// 模拟 agent 重启：新实例必须从状态目录读回索引。
	r2 := New(deps())
	if got, ok := r2.projects.Lookup("uni-center"); !ok || got != configFile {
		t.Fatalf("重启后的 Runtime 必须读回索引，got %q ok=%v", got, ok)
	}
}
