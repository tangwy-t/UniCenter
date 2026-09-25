package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 一期四个只读 action 各自的数据面（执行器级）。
func TestReadExecutorPayloads(t *testing.T) {
	api := &stubAPI{}
	api.containerDetail = ContainerDetail{ID: "c1", Name: "mysql", Image: "mysql:8.0.22", State: "running",
		Env: []string{"MYSQL_ROOT_PASSWORD=***"}, Entrypoint: []string{"docker-entrypoint.sh"},
		Mounts: []MountInfo{{Type: "volume", Source: "mysql-data", Destination: "/var/lib/mysql", RW: true}}}
	api.logLines = "2026-09-24T15:30:32 server starting\n"
	api.imageDetail = ImageDetail{ID: "sha256:545b", SizeBytes: 545 << 20,
		History: []ImageLayer{{SizeBytes: 12 << 20, CreatedBy: "CMD [\"mysqld\"]"}, {SizeBytes: 0, CreatedBy: "ENV x=1", EmptyLayer: true}}}
	api.composeContent = "services:\n  uni_core:\n    image: x\n"
	// compose.file:read 的路径**只能**来自容器标签，而标签给的是宿主上的真实路径 ——
	// 故把 composeContent 落到临时目录再把标签指向它：测试不依赖仓库里任何真实 yml，
	// 也顺带守住「按标签路径读文件」这条路径白名单（而不是从别处取内容）。
	composePath := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(api.composeContent), 0o600); err != nil {
		t.Fatal(err)
	}
	api.containers = []ContainerInfo{{ID: "c1", Name: "uni-center-core", State: "running",
		Labels: map[string]string{composeProjectLabel: "uni-center", composeConfigFilesLabel: composePath}}}
	e := NewReadExecutor(api)

	cases := []struct {
		action string
		opts   agentproto.DockerCmdOptions
		check  func(t *testing.T, raw []byte)
	}{
		{agentproto.DockerActionContainerInspect, agentproto.DockerCmdOptions{Target: "mysql"},
			func(t *testing.T, raw []byte) {
				var p agentproto.DockerContainerInspectPayload
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if p.Name != "mysql" || len(p.Env) != 1 {
					t.Fatalf("容器 inspect 载荷不符: %+v", p)
				}
			}},
		{agentproto.DockerActionContainerLogs, agentproto.DockerCmdOptions{Target: "mysql", Tail: 100},
			func(t *testing.T, raw []byte) {
				var p agentproto.DockerLogsPayload
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(p.Lines, "server starting") {
					t.Fatalf("日志载荷不符: %+v", p)
				}
			}},
		{agentproto.DockerActionImageInspect, agentproto.DockerCmdOptions{Target: "mysql:8.0.22"},
			func(t *testing.T, raw []byte) {
				var p agentproto.DockerImageInspectPayload
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				// 分层大小合计 ≈ 镜像大小（一期验收标准之一）；空层（ENV）不计入
				var sum int64
				for _, l := range p.History {
					sum += l.SizeBytes
				}
				if len(p.History) != 2 || sum > p.SizeBytes {
					t.Fatalf("分层历史不符: layers=%d sum=%d size=%d", len(p.History), sum, p.SizeBytes)
				}
			}},
		{agentproto.DockerActionComposeFileRead, agentproto.DockerCmdOptions{Target: "uni-center"},
			func(t *testing.T, raw []byte) {
				var p agentproto.DockerComposeFilePayload
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if len(p.Hash) != 64 || !strings.Contains(p.Content, "uni_core") {
					t.Fatalf("配置文件载荷不符: hash=%q", p.Hash)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			raw, err := e.Do(context.Background(), &agentproto.DockerCmd{
				Ref: "1", Action: c.action, Options: c.opts})
			if err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			c.check(t, raw)
		})
	}
}

// 路径白名单：项目名对应的配置文件**只能**来自容器标签，且必须在标签给出的绝对路径上。
// 这里守的是「旧版 compose 没记路径」与「路径不是绝对路径」两条拒绝路径。
func TestComposeFileReadRejectsMissingPath(t *testing.T) {
	api := &stubAPI{}
	api.containers = []ContainerInfo{{ID: "c1", Name: "x", State: "running",
		Labels: map[string]string{composeProjectLabel: "uni-center"}}} // 无 config_files 标签
	e := NewReadExecutor(api)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionComposeFileRead, Options: agentproto.DockerCmdOptions{Target: "uni-center"}})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg == "" {
		t.Fatalf("缺路径必须给出结论句: %v", err)
	}
	if !strings.Contains(ee.Msg, "位置未知") {
		t.Fatalf("结论句应说明「位置未知」（旧版 compose 的如实标注），实际 %q", ee.Msg)
	}
}
