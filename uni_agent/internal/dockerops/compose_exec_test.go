package dockerops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// intPtr 是「指针 + 零值有意义」字段的小工具（compose.service:scale 的 n=0 是合法且
// 高危的值，不能用零值表示「没填」）。
func intPtr(v int) *int { return &v }

// ── 注入替身：记录 argv 的固定 argv 执行入口 ──────────────────────────────

// composeExecStub 是注入 WriteExecutor 的 exec 替身。
//
// 它做两件事：把收到的 (name, args) 逐字记下来供断言；返回一个**宿主无关**的假 compose
// 进程（测试二进制自身 + TestComposeExecHelperProcess 入口，不真跑 docker），
// 由 exitCode/stderr 控制成败 —— 这样 argv 断言与失败路径都不依赖 CI 里有 docker。
type composeExecStub struct {
	mu       sync.Mutex
	calls    []composeCall
	exitCode int
	stderr   string
}

type composeCall struct {
	name string
	args []string
}

func (s *composeExecStub) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	s.mu.Lock()
	s.calls = append(s.calls, composeCall{name: name, args: append([]string(nil), args...)})
	code, stderr := s.exitCode, s.stderr
	s.mu.Unlock()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestComposeExecHelperProcess$")
	cmd.Env = append(os.Environ(),
		"UNI_TEST_COMPOSE_HELPER=1",
		"UNI_TEST_COMPOSE_HELPER_EXIT="+strconv.Itoa(code),
		"UNI_TEST_COMPOSE_HELPER_STDERR="+stderr,
	)
	return cmd
}

func (s *composeExecStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *composeExecStub) last(t *testing.T) composeCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("compose CLI 没有被执行")
	}
	return s.calls[len(s.calls)-1]
}

// TestComposeExecHelperProcess 不是测试：它是 composeExecStub 启动的假 compose 进程。
func TestComposeExecHelperProcess(t *testing.T) {
	if os.Getenv("UNI_TEST_COMPOSE_HELPER") != "1" {
		return
	}
	if s := os.Getenv("UNI_TEST_COMPOSE_HELPER_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code, _ := strconv.Atoi(os.Getenv("UNI_TEST_COMPOSE_HELPER_EXIT"))
	os.Exit(code)
}

// composeFlavorCases 是三种 compose 形态的 argv 形态差异：plugin 是 `docker compose`
// （二进制 docker + 子命令 compose），独立二进制的 v2/v1 是 `docker-compose`
// （**没有 compose 子命令**）。两种形态都必须逐字钉住 —— 混用会执行成
// `docker-compose compose up`（命令不存在）。
var composeFlavorCases = []struct {
	flavor string
	bin    string
	prefix []string
}{
	{agentproto.DockerComposeFlavorPlugin, "docker", []string{"compose"}},
	{agentproto.DockerComposeFlavorStandaloneV2, "docker-compose", nil},
	{agentproto.DockerComposeFlavorV1, "docker-compose", nil},
}

// testComposeConfig 是 argv 断言里那个「来自容器标签」的配置文件路径。
const testComposeConfig = "/data/UniCenter/docker-compose.yml"

func composeOpts(target string) agentproto.DockerCmdOptions {
	return agentproto.DockerCmdOptions{Target: target}
}

// writeComposeFixture 落一个真实存在的 compose 文件（composeConfigFilesOf 要求标签路径
// 指向的文件必须存在）。
func writeComposeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// composeProjectFixture 造一个「项目 uni-center + 服务 uni_core」的容器（标签给出配置
// 文件路径），并可另加一个不受保护的项目。
func composeProjectFixture(configFile string) *stubAPI {
	labels := map[string]string{
		composeProjectLabel:     "uni-center",
		composeServiceLabel:     "uni_core",
		composeConfigFilesLabel: configFile,
	}
	return &stubAPI{containers: []ContainerInfo{
		{ID: "c1", Name: "uni-center-core", State: "running", Labels: labels},
		// 另一个项目：用来证明「不受保护的目标照常放行」，也让服务粒度用例有落点。
		{ID: "c2", Name: "other-svc-1", State: "running", Labels: map[string]string{
			composeProjectLabel:     "other",
			composeServiceLabel:     "other_svc",
			composeConfigFilesLabel: configFile,
		}},
	}}
}

// ── Step 1：argv 逐字断言（防注入的关键证据）─────────────────────────────

// argv 必须逐字可预期：compose 写路径一旦经过 shell 或让参数串上 flag，就等于把
// 宿主机 root 交给请求方。这里把「公共操作子集」的 argv 逐条钉死，且三种 flavor
// 形态（plugin 带 compose 子命令 / 独立二进制不带）都覆盖。
func TestComposeArgv(t *testing.T) {
	cases := []struct {
		name   string
		action string
		opts   agentproto.DockerCmdOptions
		// want 是 **flavor 无关** 的尾部（plugin 的 "compose" 前缀由用例拼上）。
		want []string
	}{
		{"up 默认回收孤儿", agentproto.DockerActionComposeUp, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "up", "-d", "--remove-orphans"}},
		{"up 显式 remove_orphans 同形（默认档无开关可关）", agentproto.DockerActionComposeUp,
			agentproto.DockerCmdOptions{Target: "uni-center", RemoveOrphans: true},
			[]string{"-p", "uni-center", "-f", testComposeConfig, "up", "-d", "--remove-orphans"}},
		{"stop", agentproto.DockerActionComposeStop, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "stop"}},
		{"start", agentproto.DockerActionComposeStart, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "start"}},
		{"restart", agentproto.DockerActionComposeRestart, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "restart"}},
		{"pull", agentproto.DockerActionComposePull, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "pull"}},
		{"down", agentproto.DockerActionComposeDown, composeOpts("uni-center"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "down"}},
		{"down 带卷", agentproto.DockerActionComposeDown,
			agentproto.DockerCmdOptions{Target: "uni-center", Volumes: true},
			[]string{"-p", "uni-center", "-f", testComposeConfig, "down", "-v"}},
		{"scale 扩到 3", agentproto.DockerActionComposeServiceScale,
			agentproto.DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(3)},
			[]string{"-p", "uni-center", "-f", testComposeConfig, "up", "-d", "--scale", "uni_core=3"}},
		{"scale 缩到 0（保护档，argv 仍是普通缩容）", agentproto.DockerActionComposeServiceScale,
			agentproto.DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(0)},
			[]string{"-p", "uni-center", "-f", testComposeConfig, "up", "-d", "--scale", "uni_core=0"}},
		{"仅删容器（保留定义）", agentproto.DockerActionComposeServiceRemoveContainers,
			composeOpts("uni-center/uni_core"),
			[]string{"-p", "uni-center", "-f", testComposeConfig, "rm", "-f", "-s", "uni_core"}},
	}
	for _, fc := range composeFlavorCases {
		t.Run(fc.flavor, func(t *testing.T) {
			e := NewWriteExecutor(&stubAPI{}, ParseProtected(""), t.TempDir(), fc.flavor, nil)
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					argv, err := e.composeArgv(c.action, &c.opts, []string{testComposeConfig})
					if err != nil {
						t.Fatalf("argv 构建失败: %v", err)
					}
					want := append(append([]string(nil), fc.prefix...), c.want...)
					if !slices.Equal(argv, want) {
						t.Fatalf("argv 必须逐字相等:\n got %q\nwant %q", argv, want)
					}
					// 公共操作子集纪律：v1/独立版没有 --wait，出现即违约。
					if slices.Contains(argv, "--wait") {
						t.Fatalf("argv 不得出现 --wait（公共子集纪律）: %q", argv)
					}
					if slices.Contains(argv, "sh") || slices.Contains(argv, "-c") {
						t.Fatalf("argv 不得引入 shell: %q", argv)
					}
				})
			}
		})
	}
}

// 二进制名与 flavor 一一对应：plugin → docker（argv 带 compose），
// standalone-v2 / v1 → docker-compose（argv 不带 compose）。执行路径上整体再断言一次
// （不只 composeArgv 单元）。
func TestComposeExecutorRunsFixedArgvPerFlavor(t *testing.T) {
	configFile := writeComposeFixture(t)
	for _, fc := range composeFlavorCases {
		t.Run(fc.flavor, func(t *testing.T) {
			stub := &composeExecStub{}
			e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(), fc.flavor, stub.command)
			if _, err := e.Do(context.Background(), &agentproto.DockerCmd{
				Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
			}); err != nil {
				t.Fatalf("up 应放行: %v", err)
			}
			call := stub.last(t)
			if call.name != fc.bin {
				t.Fatalf("可执行文件必须是 %q，实际 %q", fc.bin, call.name)
			}
			want := append(append([]string(nil), fc.prefix...),
				"-p", "uni-center", "-f", configFile, "up", "-d", "--remove-orphans")
			if !slices.Equal(call.args, want) {
				t.Fatalf("实际 argv 与期望逐字不符:\n got %q\nwant %q", call.args, want)
			}
		})
	}
}

// 目标名在拼 argv 之前再走一遍协议白名单（纵深防御）：注入形态、缺 n、空项目名
// 一律在**执行之前**被结论句拒绝，绝不能触达 CLI。
func TestComposeExecutorRejectsInvalidTargetsBeforeExec(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeExecStub{}
	e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	cases := []struct {
		name   string
		action string
		target string
	}{
		{"项目名带 shell 元字符", agentproto.DockerActionComposeUp, "uni-center; rm -rf /"},
		{"项目名后接 flag", agentproto.DockerActionComposeUp, "uni-center --wait"},
		{"项目名带斜杠", agentproto.DockerActionComposeUp, "uni-center/uni_core"},
		{"项目名以短横开头", agentproto.DockerActionComposeUp, "-p"},
		{"项目名为空", agentproto.DockerActionComposeUp, ""},
		{"服务 target 不是「项目/服务」", agentproto.DockerActionComposeServiceRemoveContainers, "uni-center"},
		{"服务名带路径穿越", agentproto.DockerActionComposeServiceRemoveContainers, "uni-center/../evil"},
		{"服务名带 shell 元字符", agentproto.DockerActionComposeServiceRemoveContainers, "uni-center/uni_core;x"},
		{"服务名为空", agentproto.DockerActionComposeServiceRemoveContainers, "uni-center/"},
		{"项目段为空", agentproto.DockerActionComposeServiceRemoveContainers, "/uni_core"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{
				Action: c.action, Options: composeOpts(c.target),
			})
			var ee *ExecError
			if !errors.As(err, &ee) || ee.Msg == "" {
				t.Fatalf("非法目标必须被结论句拒绝，实际 %v", err)
			}
			if stub.count() != 0 {
				t.Fatal("被拒的目标绝不能触达 compose CLI")
			}
		})
	}
}

// scale 缺 n 是伪造/旧 core 才会有的输入（协议必填）：不猜测、不用默认值。
// 这里直接打 argv 构建函数（执行路径上配置文件解析会先返回另一条错误）。
func TestComposeArgvScaleRequiresN(t *testing.T) {
	e := NewWriteExecutor(&stubAPI{}, ParseProtected(""), t.TempDir(), agentproto.DockerComposeFlavorPlugin, nil)
	_, err := e.composeArgv(agentproto.DockerActionComposeServiceScale,
		&agentproto.DockerCmdOptions{Target: "uni-center/uni_core"}, []string{testComposeConfig})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg == "" {
		t.Fatalf("缺 n 必须被拒绝，实际 %v", err)
	}
}

// 没有探测到 compose（flavor 为空）时不得猜一个形态去执行：猜错只会得到
// 「docker: 'compose' is not a docker command」这类噪音。
func TestComposeExecutorRequiresDetectedFlavor(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeExecStub{}
	e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(), "", stub.command)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	})
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "没有检测到可用的 compose") {
		t.Fatalf("未探测到 compose 必须给结论句，实际 %v", err)
	}
	if stub.count() != 0 {
		t.Fatal("形态未知时绝不能执行 CLI")
	}
}

// 配置文件路径只能来自容器标签：缺标签（旧版 compose）与项目不存在都要给出结论句，
// 且在拼 argv 之前就失败（不执行 CLI）。
func TestComposeExecutorConfigFileFromLabelsOnly(t *testing.T) {
	t.Run("缺 config_files 标签", func(t *testing.T) {
		api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "x",
			Labels: map[string]string{composeProjectLabel: "uni-center"}}}}
		stub := &composeExecStub{}
		e := NewWriteExecutor(api, ParseProtected(""), t.TempDir(), agentproto.DockerComposeFlavorPlugin, stub.command)
		_, err := e.Do(context.Background(), &agentproto.DockerCmd{
			Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
		})
		var ee *ExecError
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "位置未知") {
			t.Fatalf("缺标签必须如实说「位置未知」，实际 %v", err)
		}
		if stub.count() != 0 {
			t.Fatal("路径不可得时绝不能执行 CLI")
		}
	})
	t.Run("从未见过（没有容器）", func(t *testing.T) {
		stub := &composeExecStub{}
		e := NewWriteExecutor(&stubAPI{}, ParseProtected(""), t.TempDir(), agentproto.DockerComposeFlavorPlugin, stub.command)
		_, err := e.Do(context.Background(), &agentproto.DockerCmd{
			Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
		})
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Msg == "" {
			t.Fatalf("从未见过的项目必须给结论句，实际 %v", err)
		}
		// 结论句必须可操作：告诉用户「先在主机上起一次」，而不是笼统的
		// 「没有找到这个项目（它可能已经被删除）」——后者把路径未知误报成项目不存在。
		if !strings.Contains(ee.Msg, "先在主机上执行一次") {
			t.Fatalf("结论句必须给可操作指引，实际 %q", ee.Msg)
		}
		if stub.count() != 0 {
			t.Fatal("解析不到路径时绝不能执行 CLI")
		}
	})
}

// 解析顺序的第一条：容器标签是**最新真相**。索引里存着旧位置时（项目被移动过），
// 标签必须赢 —— 否则 down 一次再 up 会用回过期路径。
func TestComposeExecutorLabelWinsOverIndex(t *testing.T) {
	labelFile := writeComposeFixture(t)
	staleFile := writeComposeFixture(t)
	idx := newProjectIndex(t.TempDir(), testLogger())
	idx.Learn("uni-center", staleFile)

	stub := &composeExecStub{}
	e := NewWriteExecutor(withProjectIndex(composeProjectFixture(labelFile), idx), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	}); err != nil {
		t.Fatalf("up 应放行: %v", err)
	}
	call := stub.last(t)
	if !slices.Contains(call.args, labelFile) || slices.Contains(call.args, staleFile) {
		t.Fatalf("标签路径必须优先于索引里的旧位置: %q", call.args)
	}
}

// 容器在、但旧版 compose 没有 config_files 标签：回落到索引里的历史观测
// （没有历史时才是「位置未知（旧版 compose 未记录）」）。
func TestComposeExecutorFallsBackToIndexWhenLabelMissing(t *testing.T) {
	configFile := writeComposeFixture(t)
	idx := newProjectIndex(t.TempDir(), testLogger())
	idx.Learn("uni-center", configFile)
	api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "x",
		Labels: map[string]string{composeProjectLabel: "uni-center"}}}}

	stub := &composeExecStub{}
	e := NewWriteExecutor(withProjectIndex(api, idx), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	}); err != nil {
		t.Fatalf("缺标签但索引有历史观测时必须放行: %v", err)
	}
	if call := stub.last(t); !slices.Contains(call.args, configFile) {
		t.Fatalf("argv 必须用索引路径: %q", call.args)
	}
}

// ── 多文件项目（override）：argv 必须保留全部 -f ─────────────────────────

// `-f a.yml -f b.yml` 起的项目，写路径必须带着**全部** -f：compose 的 merge 语义
// （后者覆盖前者）正是 override 本身，只带主文件会让 override 定义静默失效——更何况
// up 默认带 --remove-orphans，override 独有的服务会被当孤儿删掉。三种 flavor 都支持
// 重复 -f（自 v1 时代起），逐字钉死。
func TestComposeExecutorKeepsOverrideFiles(t *testing.T) {
	main := writeComposeFixture(t)
	override := writeComposeFixture(t)
	// 标签里带空格与空项：拆分必须去空白、丢空项，仍然只有这两个文件。
	api := composeProjectFixture(main)
	api.containers[0].Labels[composeConfigFilesLabel] = " " + main + " , ," + override + " "
	for _, fc := range composeFlavorCases {
		t.Run(fc.flavor, func(t *testing.T) {
			stub := &composeExecStub{}
			e := NewWriteExecutor(api, ParseProtected(""), t.TempDir(), fc.flavor, stub.command)
			if _, err := e.Do(context.Background(), &agentproto.DockerCmd{
				Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
			}); err != nil {
				t.Fatalf("up 应放行: %v", err)
			}
			call := stub.last(t)
			want := append(append([]string(nil), fc.prefix...),
				"-p", "uni-center", "-f", main, "-f", override, "up", "-d", "--remove-orphans")
			if !slices.Equal(call.args, want) {
				t.Fatalf("override 的 -f 必须按标签原序全部保留:\n got %q\nwant %q", call.args, want)
			}
		})
	}
}

// 清单里有文件缺失（override 被移走）：结论句**指明是哪个文件**，且绝不执行 CLI
// —— 带着残缺清单执行等于执行一个与快照不符的项目。
func TestComposeExecutorRejectsMissingOverrideFile(t *testing.T) {
	main := writeComposeFixture(t)
	missing := filepath.Join(t.TempDir(), "override.yml") // 故意不存在
	api := composeProjectFixture(main)
	api.containers[0].Labels[composeConfigFilesLabel] = main + "," + missing
	stub := &composeExecStub{}
	e := NewWriteExecutor(api, ParseProtected(""), t.TempDir(), agentproto.DockerComposeFlavorPlugin, stub.command)

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeUp, Options: composeOpts("uni-center"),
	})
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, missing) {
		t.Fatalf("缺失的 override 必须被结论句拒绝并指明文件，实际 %v", err)
	}
	if stub.count() != 0 {
		t.Fatal("清单残缺时绝不能执行 CLI")
	}
}

// ── 解析层：标签清单的拆分与逐项校验 ────────────────────────────────────

// 标签拆出全部文件（原序、Trim、丢空项）；任何一项不是绝对路径/不存在/是目录都
// 拒绝，结论句指明是哪个文件 —— 多文件项目里只说「有个文件坏了」无从定位。
func TestComposeConfigFilesLabelValidation(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "base.yml")
	b := filepath.Join(dir, "override.yml")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("全部文件按原序返回（去空白、丢空项）", func(t *testing.T) {
		files, err := validatedLabelConfigFiles(" " + a + " , ," + b + " , ")
		if err != nil {
			t.Fatalf("标签应解析成功: %v", err)
		}
		if want := []string{a, b}; !slices.Equal(files, want) {
			t.Fatalf("必须按标签原序返回全部文件:\n got %q\nwant %q", files, want)
		}
	})
	t.Run("单文件标签回归护栏", func(t *testing.T) {
		files, err := validatedLabelConfigFiles(a)
		if err != nil {
			t.Fatalf("单文件标签应解析成功: %v", err)
		}
		if !slices.Equal(files, []string{a}) {
			t.Fatalf("单文件标签必须原样返回单元素列表: %q", files)
		}
	})
	t.Run("不是绝对路径：结论句指明文件", func(t *testing.T) {
		_, err := validatedLabelConfigFiles(a + ",relative.yml")
		var ee *ExecError
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "不是绝对路径") ||
			!strings.Contains(ee.Msg, "relative.yml") {
			t.Fatalf("相对路径必须被拒并指明是哪个文件: %v", err)
		}
	})
	t.Run("文件不存在：结论句指明文件", func(t *testing.T) {
		missing := filepath.Join(dir, "no-such.yml")
		_, err := validatedLabelConfigFiles(a + "," + missing)
		var ee *ExecError
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, missing) {
			t.Fatalf("缺失文件必须被拒并指明是哪个: %v", err)
		}
	})
	t.Run("是目录：结论句指明文件", func(t *testing.T) {
		_, err := validatedLabelConfigFiles(a + "," + dir)
		var ee *ExecError
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "目录") ||
			!strings.Contains(ee.Msg, dir) {
			t.Fatalf("目录必须被拒并指明是哪个: %v", err)
		}
	})
}

// 索引兜底是单元素列表：索引只学主文件（快照学到的单一路径），多文件知识只在标签里
// —— 有容器的项目走标签（全部文件），down 之后走索引（只剩主文件，如实）。
func TestComposeConfigFilesIndexFallbackSingleFile(t *testing.T) {
	configFile := writeComposeFixture(t)
	idx := newProjectIndex(t.TempDir(), testLogger())
	idx.Learn("uni-center", configFile)
	api := &stubAPI{containers: []ContainerInfo{{ID: "c1", Name: "x",
		Labels: map[string]string{composeProjectLabel: "uni-center"}}}}

	files, err := composeConfigFilesOf(context.Background(), withProjectIndex(api, idx), "uni-center")
	if err != nil {
		t.Fatalf("索引兜底应解析成功: %v", err)
	}
	if !slices.Equal(files, []string{configFile}) {
		t.Fatalf("索引兜底必须是单元素列表: %q", files)
	}
}

// ── 保护强制（compose 项目/服务粒度）────────────────────────────────────

// 保护目标是拒绝依据：项目粒度（up/down 等）与服务粒度（scale/rm 等）都必须命中，
// 未带 force 时在触达 CLI 之前拒绝；带 force 才放行。
func TestComposeExecutorProtectedRequiresForce(t *testing.T) {
	configFile := writeComposeFixture(t)
	const wantMsg = "这是受保护的目标，需要显式的强制确认才能操作"
	protected := "project:uni-center,project:other/other_svc"

	cases := []struct {
		name    string
		action  string
		opts    agentproto.DockerCmdOptions
		wantErr bool
	}{
		{"项目受保护：up 无 force 拒绝", agentproto.DockerActionComposeUp,
			composeOpts("uni-center"), true},
		{"项目受保护：down 无 force 拒绝", agentproto.DockerActionComposeDown,
			composeOpts("uni-center"), true},
		{"项目受保护：带 force 放行", agentproto.DockerActionComposeUp,
			agentproto.DockerCmdOptions{Target: "uni-center", Force: true}, false},
		{"不受保护的项目：放行", agentproto.DockerActionComposeUp,
			composeOpts("other"), false},
		{"服务受保护：scale 无 force 拒绝", agentproto.DockerActionComposeServiceScale,
			agentproto.DockerCmdOptions{Target: "other/other_svc", N: intPtr(0)}, true},
		{"服务受保护：remove-containers 无 force 拒绝", agentproto.DockerActionComposeServiceRemoveContainers,
			composeOpts("other/other_svc"), true},
		{"服务受保护：带 force 放行", agentproto.DockerActionComposeServiceScale,
			agentproto.DockerCmdOptions{Target: "other/other_svc", N: intPtr(2), Force: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &composeExecStub{}
			e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(protected), t.TempDir(),
				agentproto.DockerComposeFlavorPlugin, stub.command)
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{Action: c.action, Options: c.opts})
			if !c.wantErr {
				if err != nil {
					t.Fatalf("应当放行，实际 %v", err)
				}
				if stub.count() != 1 {
					t.Fatalf("放行必须真的执行一次 CLI，实际 %d 次", stub.count())
				}
				return
			}
			var ee *ExecError
			if !errors.As(err, &ee) || ee.Msg != wantMsg {
				t.Fatalf("必须被保护档拒绝，实际 %v", err)
			}
			if !strings.Contains(ee.Detail, "protected target:") {
				t.Fatalf("detail 必须能看出命中的目标: %q", ee.Detail)
			}
			if stub.count() != 0 {
				t.Fatal("被保护档拒绝的指令绝不能触达 compose CLI")
			}
		})
	}
}

// ── CLI 失败路径：结论句 + detail ───────────────────────────────────────

// CLI 失败时面向用户的是结论句，stderr 原文只进 detail；结果必须能过协议校验。
func TestComposeExecutorCLIFailureIsConclusion(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeExecStub{exitCode: 1, stderr: "no such service: uni_core"}
	e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeServiceRemoveContainers, Options: composeOpts("uni-center/uni_core"),
	})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "删除服务容器失败" {
		t.Fatalf("失败必须是结论句，实际 %v", err)
	}
	if !strings.Contains(ee.Detail, "no such service") {
		t.Fatalf("detail 必须保留 CLI 原文供排障: %q", ee.Detail)
	}
	// 走一遍 dispatcher，确认 detail 被规范化、结果能过协议校验。
	sink := &collectSink{}
	d := NewDispatcher(e, sink.send, testLogger())
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionComposeServiceRemoveContainers,
		Options: composeOpts("uni-center/uni_core"), Confirm: "uni_core"})
	d.ExecuteNow(context.Background(), "1")
	got := sink.all()
	if len(got) != 1 || got[0].OK || got[0].Error != "删除服务容器失败" {
		t.Fatalf("结果不符: %+v", got)
	}
	if !strings.Contains(got[0].Detail, "no such service") {
		t.Fatalf("结果的 detail 必须保留排障原文: %q", got[0].Detail)
	}
	if err := got[0].Validate(); err != nil {
		t.Fatalf("结果必须能过协议校验: %v", err)
	}
}

// ── dispatcher 集成：放行清单 + 确认档 ───────────────────────────────────

// 8 条 compose 写 action 必须登记为「已实现」；四期的 compose.file:write/validate/patch
// 在四期交付后同样必须放行（执行分支在 compose_file_write.go —— 漏登记只会让按钮点了
// 得到「该操作尚未开放」）。
func TestImplementedActionsIncludesComposeOpsAndPhase4(t *testing.T) {
	mustImplement := []string{
		agentproto.DockerActionComposeUp,
		agentproto.DockerActionComposeStop,
		agentproto.DockerActionComposeStart,
		agentproto.DockerActionComposeRestart,
		agentproto.DockerActionComposePull,
		agentproto.DockerActionComposeDown,
		agentproto.DockerActionComposeServiceScale,
		agentproto.DockerActionComposeServiceRemoveContainers,
		agentproto.DockerActionComposeFileWrite,
		agentproto.DockerActionComposeFileValidate,
		agentproto.DockerActionComposeFilePatch,
	}
	for _, a := range mustImplement {
		if !implementedActions[a] {
			t.Errorf("%s 已由 WriteExecutor 实现，必须登记为已实现", a)
		}
	}
}

// 端到端：up 需要照抄项目名；scale 缩到 0 需要照抄服务名；确认后 argv 仍逐字正确。
func TestDispatcherComposeConfirmAndExecution(t *testing.T) {
	configFile := writeComposeFixture(t)
	stub := &composeExecStub{}
	e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	sink := &collectSink{}
	d := NewDispatcher(e, sink.send, testLogger())
	ctx := context.Background()

	d.Handle(ctx, &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionComposeUp,
		Options: composeOpts("uni-center")})
	d.ExecuteNow(ctx, "1")
	d.Handle(ctx, &agentproto.DockerCmd{Ref: "2", Action: agentproto.DockerActionComposeUp,
		Options: composeOpts("uni-center"), Confirm: "uni-center"})
	d.ExecuteNow(ctx, "2")
	d.Handle(ctx, &agentproto.DockerCmd{Ref: "3", Action: agentproto.DockerActionComposeServiceScale,
		Options: agentproto.DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(0)}})
	d.ExecuteNow(ctx, "3")
	d.Handle(ctx, &agentproto.DockerCmd{Ref: "4", Action: agentproto.DockerActionComposeServiceScale,
		Options: agentproto.DockerCmdOptions{Target: "uni-center/uni_core", N: intPtr(0)}, Confirm: "uni_core"})
	d.ExecuteNow(ctx, "4")

	got := sink.all()
	if len(got) != 4 {
		t.Fatalf("四条都应有结果，实际 %d", len(got))
	}
	if got[0].OK || got[0].Error != "缺少确认信息" {
		t.Fatalf("up 缺确认必须被拒: %+v", got[0])
	}
	if !got[1].OK {
		t.Fatalf("up 带项目名确认应成功: %+v", got[1])
	}
	if got[2].OK || got[2].Error != "缺少确认信息" {
		t.Fatalf("scale 到 0 缺确认必须被拒: %+v", got[2])
	}
	if !got[3].OK {
		t.Fatalf("scale 到 0 带服务名确认应成功: %+v", got[3])
	}
	call := stub.last(t)
	if !slices.Contains(call.args, "--scale") || !slices.Contains(call.args, "uni_core=0") {
		t.Fatalf("n=0 的 argv 必须原样是 --scale uni_core=0: %q", call.args)
	}
	for _, r := range got {
		if err := r.Validate(); err != nil {
			t.Fatalf("结果必须能过协议校验: %v", err)
		}
	}
}

// Runtime 探测到 flavor 后必须注入写执行器：compose argv 的形态由它决定，
// 没注进去的话执行器会一直回「没有检测到可用的 compose」。
func TestRuntimeInjectsProbedFlavorIntoWriteExecutor(t *testing.T) {
	r := New(Deps{
		StateDir:   t.TempDir(),
		SendState:  func(*agentproto.DockerState) error { return nil },
		SendResult: func(*agentproto.DockerCmdResult) error { return nil },
		Log:        testLogger(),
		API:        &stubAPI{flavor: agentproto.DockerComposeFlavorStandaloneV2, flavorVer: "v2.26.1"},
	})
	r.probeComposeOnce(context.Background())
	if got := r.write.flavorValue(); got != agentproto.DockerComposeFlavorStandaloneV2 {
		t.Fatalf("flavor 必须注入写执行器，实际 %q", got)
	}
}
