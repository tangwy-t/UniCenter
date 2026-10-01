package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/errdefs"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── container:create（四支柱·创建面，4a）────────────────────────────────────
//
// 全部走替身（CI 没有 docker daemon）：断言「执行器把协议 options 解析/映射成了
// 什么」与「失败时给出哪句结论」。daemon 的分布式事实（name conflict、镜像缺失）
// 由哨兵注入，验证结论句翻译不透传英文。

// boolP 是 start 的测试工具（显式 false 与缺席不同义）。
func boolP(v bool) *bool { return &v }

// 全参数创建：每一项 options 都必须映射进 spec（执行器的协议→SDK 参数解析面），
// start 缺省 true → 创建后必须启动；成功载荷带完整 id + 12 位短 id。
func TestCreateContainerFullMappingAndStart(t *testing.T) {
	api := &stubAPI{createReply: "a1b2c3d4e5f6a7b8c9d0e1f"}
	e := newWriteExecutor(api, "", t.TempDir())
	payload, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{
			Image: "nginx:1.27", Name: "web-1",
			Ports:         []string{"8080:80", "53:53/udp"},
			Env:           []string{"MODE=prod", "EMPTY="},
			Mounts:        []string{"data:/var/lib/app", "/srv/app:/etc/app:ro"},
			RestartPolicy: "unless-stopped",
			CPULimit:      1.5, MemLimitMB: 1024, Network: "app-net",
		},
	})
	if err != nil {
		t.Fatalf("全参数创建应成功，实际 %v", err)
	}
	if len(api.created) != 1 {
		t.Fatalf("ContainerCreate 应恰好一次，实际 %d 次", len(api.created))
	}
	got := api.created[0].spec
	if got.Image != "nginx:1.27" || got.Name != "web-1" {
		t.Fatalf("image/name 映射不符: %+v", got)
	}
	if len(got.Env) != 2 || got.Env[1] != "EMPTY=" {
		t.Fatalf("env 必须原样照抄（空值也合法）: %+v", got.Env)
	}
	if len(got.Ports) != 2 ||
		got.Ports[0] != (CreatePortBinding{Host: 8080, Container: 80, Proto: "tcp"}) ||
		got.Ports[1] != (CreatePortBinding{Host: 53, Container: 53, Proto: "udp"}) {
		t.Fatalf("端口映射不符（缺省协议必须默认 tcp）: %+v", got.Ports)
	}
	if len(got.Mounts) != 2 ||
		got.Mounts[0] != (CreateMount{Bind: false, Source: "data", Dest: "/var/lib/app"}) ||
		got.Mounts[1] != (CreateMount{Bind: true, Source: "/srv/app", Dest: "/etc/app", ReadOnly: true}) {
		t.Fatalf("挂载映射不符（源定性 + ro）: %+v", got.Mounts)
	}
	if got.RestartPolicy != "unless-stopped" || got.CPULimit != 1.5 || got.MemLimitMB != 1024 || got.Network != "app-net" {
		t.Fatalf("重启策略/限额/网络映射不符: %+v", got)
	}
	// start 缺省 = true：必须启动新容器，且用的是 create 回的 id。
	if len(api.started) != 1 || api.started[0] != api.createReply {
		t.Fatalf("缺省应创建后启动（用 create 回的 id）: started=%v reply=%s", api.started, api.createReply)
	}
	var p createPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("成功载荷应可解析: %v", err)
	}
	if p.ID != api.createReply || p.ShortID != "a1b2c3d4e5f6" || !p.Started {
		t.Fatalf("成功载荷应带完整 id + 12 位短 id + started: %+v", p)
	}
	if note := e.takeNote(); !strings.Contains(note, "a1b2c3d4e5f6") {
		t.Fatalf("成功附注应带短 id: %q", note)
	}
}

// start=false：只创建不启动（创建抽屉「仅创建」档）。
func TestCreateContainerStartFalseOnlyCreates(t *testing.T) {
	api := &stubAPI{}
	e := newWriteExecutor(api, "", t.TempDir())
	payload, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{Image: "nginx:1.27", Start: boolP(false)},
	})
	if err != nil {
		t.Fatalf("应成功: %v", err)
	}
	if len(api.created) != 1 {
		t.Fatalf("只创建：ContainerCreate 应恰好一次")
	}
	if len(api.started) != 0 {
		t.Fatalf("start=false 时绝不能 ContainerStart: %v", api.started)
	}
	var p createPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Started {
		t.Fatal("载荷的 started 应为 false")
	}
}

// 镜像缺失：哨兵 → 「先拉取」结论句，**不**自动拉（拉取是独立长耗时 action，
// 混进 create 会让超时纪律失控 —— 见 container_create.go 顶部取舍说明）。
func TestCreateContainerImageMissingGuidesPull(t *testing.T) {
	api := &stubAPI{createErr: &createSentinelError{
		sentinel: errImageNotFound, cause: errors.New("Error response from daemon: No such image: nginx:1.99"),
	}}
	e := newWriteExecutor(api, "", t.TempDir())
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{Image: "nginx:1.99"},
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("应返回 ExecError，实际 %v", err)
	}
	if ee.Msg != "本机没有这个镜像，请先拉取镜像后再创建容器" {
		t.Fatalf("结论句不符: %q", ee.Msg)
	}
	if !strings.Contains(ee.Detail, "No such image") {
		t.Fatalf("detail 应带 daemon 原文: %q", ee.Detail)
	}
	if len(api.started) != 0 {
		t.Fatal("镜像缺失时绝不能启动")
	}
}

// 名字冲突：对照 docker 原生 409 翻译结论句，英文原文只落 detail。
func TestCreateContainerNameConflict(t *testing.T) {
	api := &stubAPI{createErr: &createSentinelError{
		sentinel: errContainerNameConflict,
		cause:    errors.New(`Error response from daemon: Conflict. The container name "/web-1" is already in use by container "abc"`),
	}}
	e := newWriteExecutor(api, "", t.TempDir())
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{Image: "nginx:1.27", Name: "web-1"},
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("应返回 ExecError，实际 %v", err)
	}
	if ee.Msg != "同名容器已存在，请换一个容器名" {
		t.Fatalf("结论句不符: %q", ee.Msg)
	}
	if !strings.Contains(ee.Detail, "already in use") {
		t.Fatalf("detail 应带 daemon 原文: %q", ee.Detail)
	}
}

// 通用失败：走 wrapDocker 的通用结论句（不分类的错误不编造结论）。
func TestCreateContainerGenericFailure(t *testing.T) {
	api := &stubAPI{createErr: errors.New("daemon exploded")}
	e := newWriteExecutor(api, "", t.TempDir())
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{Image: "nginx:1.27"},
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("应返回 ExecError，实际 %v", err)
	}
	if ee.Msg != "创建容器失败" || !strings.Contains(ee.Detail, "daemon exploded") {
		t.Fatalf("通用失败结论句不符: %+v", ee)
	}
}

// 启动失败：容器已建成，结论句必须带上短 id（用户会在列表里看到这个新容器，
// 需要知道它从哪来、怎么收尾）。
func TestCreateContainerStartFailureKeepsID(t *testing.T) {
	api := &stubAPI{createReply: "abcdef123456abcdef", startErr: errors.New("start refused")}
	e := newWriteExecutor(api, "", t.TempDir())
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionContainerCreate,
		Options: agentproto.DockerCmdOptions{Image: "nginx:1.27", Name: "web-1"},
	})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("应返回 ExecError，实际 %v", err)
	}
	if !strings.Contains(ee.Msg, "容器已创建但启动失败") || !strings.Contains(ee.Msg, "abcdef123456") {
		t.Fatalf("启动失败结论句应带短 id: %q", ee.Msg)
	}
	if !strings.Contains(ee.Detail, "start refused") {
		t.Fatalf("detail 应带 daemon 原文: %q", ee.Detail)
	}
}

// 执行器侧的纵深防御：**绕过 dispatcher**直接调用 Do 时，非法 options 仍被拒
// （Do 重跑协议校验 —— 与 guard「agent 是最后一道」同一理由）。结论与
// dispatcher 的参数档同款（「指令参数不合法」），字段线索在 detail。
func TestCreateContainerRejectsBadOptionsAtExecutor(t *testing.T) {
	for _, c := range []struct {
		name string
		opts agentproto.DockerCmdOptions
	}{
		{"端口宿主为 0", agentproto.DockerCmdOptions{Image: "nginx", Ports: []string{"0:80"}}},
		{"env 缺等号", agentproto.DockerCmdOptions{Image: "nginx", Env: []string{"FOO"}}},
		{"挂载四段", agentproto.DockerCmdOptions{Image: "nginx", Mounts: []string{"a:b:c:d"}}},
		{"cpu_limit 越界", agentproto.DockerCmdOptions{Image: "nginx", CPULimit: 99}},
		{"mem_limit_mb 负", agentproto.DockerCmdOptions{Image: "nginx", MemLimitMB: -1}},
		{"restart_policy 非枚举", agentproto.DockerCmdOptions{Image: "nginx", RestartPolicy: "on-success"}},
		{"name 含斜杠", agentproto.DockerCmdOptions{Image: "nginx", Name: "a/b"}},
		{"带 target（create 无 target）", agentproto.DockerCmdOptions{Image: "nginx", Target: "web-1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := &stubAPI{}
			e := newWriteExecutor(api, "", t.TempDir())
			_, err := e.Do(context.Background(), &agentproto.DockerCmd{
				Action: agentproto.DockerActionContainerCreate, Options: c.opts,
			})
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("非法 options 必须被拒，实际 %v", err)
			}
			if ee.Msg != "指令参数不合法" {
				t.Fatalf("结论句应与参数档同款: %q", ee.Msg)
			}
			if len(api.created) != 0 || len(api.started) != 0 {
				t.Fatal("被拒的指令绝不能触达 Docker API")
			}
		})
	}
	// 缺 image 的结论同样是参数档（必填在协议校验的第一道）。
	api := &stubAPI{}
	e := newWriteExecutor(api, "", t.TempDir())
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionContainerCreate, Options: agentproto.DockerCmdOptions{},
	})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "指令参数不合法" {
		t.Fatalf("缺 image 必须被拒: %v", err)
	}
}

// classifier 的翻译边界：404 未必是镜像（network not found 也是 404），只有原文
// 对得上才翻镜像哨兵 —— 把两种处置糊成一句「先拉取」会让运维朝错误方向排查。
// 分类基于 errdefs 的判定（真实 SDK 错误形态），测试用 errdefs.NotFound/Conflict 构造。
func TestClassifyCreateError(t *testing.T) {
	netErr := errdefs.NotFound(errors.New("Error response from daemon: network mynet not found"))
	if got := classifyCreateError(netErr); errors.Is(got, errImageNotFound) {
		t.Fatal("network not found 不该被翻成镜像缺失")
	} else if got.Error() == "" {
		t.Fatal("非哨兵错误必须原样上抛")
	}
	conflict := errdefs.Conflict(errors.New(`Conflict. The container name "/x" is already in use`))
	if got := classifyCreateError(conflict); !errors.Is(got, errContainerNameConflict) {
		t.Fatal("409 必须翻成名字冲突哨兵")
	}
	noImage := errdefs.NotFound(errors.New("No such image: nginx:1.99"))
	if got := classifyCreateError(noImage); !errors.Is(got, errImageNotFound) {
		t.Fatal("No such image 必须翻成镜像缺失哨兵")
	}
}
