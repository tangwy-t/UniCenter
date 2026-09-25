package dockerops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// defaultLogTail 是 container:logs 未指定 tail 时的默认行数（与 UI 默认一致）。
const defaultLogTail = 100

// ReadExecutor 执行一期的四个只读 action。
type ReadExecutor struct {
	api DockerAPI
}

// NewReadExecutor 构造只读执行器。
func NewReadExecutor(api DockerAPI) *ReadExecutor { return &ReadExecutor{api: api} }

// Do 分发到具体的只读读取。
func (e *ReadExecutor) Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	switch cmd.Action {
	case agentproto.DockerActionContainerInspect:
		d, err := e.api.ContainerInspect(ctx, cmd.Options.Target)
		if err != nil {
			return nil, &ExecError{Msg: "读取容器信息失败", Detail: err.Error()}
		}
		return json.Marshal(containerInspectPayload(d))

	case agentproto.DockerActionContainerLogs:
		tail := cmd.Options.Tail
		if tail <= 0 {
			tail = defaultLogTail
		}
		lines, truncated, err := e.api.ContainerLogs(ctx, cmd.Options.Target, tail, cmd.Options.Since)
		if err != nil {
			return nil, &ExecError{Msg: "读取容器日志失败", Detail: err.Error()}
		}
		return json.Marshal(&agentproto.DockerLogsPayload{Lines: lines, Truncated: truncated})

	case agentproto.DockerActionImageInspect:
		d, err := e.api.ImageInspect(ctx, cmd.Options.Target)
		if err != nil {
			return nil, &ExecError{Msg: "读取镜像信息失败", Detail: err.Error()}
		}
		return json.Marshal(imageInspectPayload(d))

	case agentproto.DockerActionComposeFileRead:
		p, err := e.composeFile(ctx, cmd.Options.Target)
		if err != nil {
			return nil, err
		}
		return json.Marshal(p)

	default:
		return nil, &ExecError{Msg: "该操作尚未开放"}
	}
}

// composeFile 读一个 compose 项目的配置文件。
//
// 路径**只能**来自容器标签（com.docker.compose.project.config_files），协议上从不接受
// 路径入参 —— 路径白名单纪律（§8 四条防护之一）：UI 只传项目名，路径逃逸面因此不存在。
func (e *ReadExecutor) composeFile(ctx context.Context, project string) (*agentproto.DockerComposeFilePayload, error) {
	cs, err := e.api.Containers(ctx)
	if err != nil {
		return nil, &ExecError{Msg: "读取项目信息失败", Detail: err.Error()}
	}
	for _, c := range cs {
		if c.Labels[composeProjectLabel] != project {
			continue
		}
		raw := c.Labels[composeConfigFilesLabel]
		if strings.TrimSpace(raw) == "" {
			// 旧版 compose 不写这个标签：如实说「位置未知」，不猜、不去扫目录。
			return nil, &ExecError{Msg: "这个项目的配置文件位置未知（旧版 compose 未记录）"}
		}
		// 可能是多个文件（-f a.yml -f b.yml）：取第一个（主文件）。
		path := strings.TrimSpace(strings.Split(raw, ",")[0])
		if !filepath.IsAbs(path) {
			return nil, &ExecError{Msg: "配置文件位置异常（不是绝对路径）"}
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
		}
		if fi.IsDir() {
			return nil, &ExecError{Msg: "配置文件位置异常（它是一个目录）"}
		}
		if fi.Size() > agentproto.MaxDockerComposeFileBytes {
			return nil, &ExecError{Msg: "配置文件过大（超过 1MB）"}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
		}
		sum := sha256.Sum256(b)
		return &agentproto.DockerComposeFilePayload{
			Content: string(b),
			Hash:    hex.EncodeToString(sum[:]),
			Path:    path,
		}, nil
	}
	return nil, &ExecError{Msg: "没有找到这个项目（它可能已经被删除）"}
}

// containerInspectPayload 把 inspect 结果映射成协议载荷（导出的结构在协议侧，
// agent 侧用一个未导出函数做映射，避免两处字段名各写一遍）。
func containerInspectPayload(d ContainerDetail) *agentproto.DockerContainerInspectPayload {
	return &agentproto.DockerContainerInspectPayload{
		ID: d.ID, Name: d.Name, Image: d.Image, ImageID: d.ImageID, Created: d.Created,
		State: d.State, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, ExitCode: d.ExitCode,
		RestartPolicy: d.RestartPolicy, Env: d.Env, Labels: d.Labels, Ports: toProtoPorts(d.Ports),
		Mounts:     toProtoMounts(d.Mounts),
		Entrypoint: d.Entrypoint, Cmd: d.Cmd, Health: d.Health, Networks: d.Networks,
	}
}

func imageInspectPayload(d ImageDetail) *agentproto.DockerImageInspectPayload {
	out := &agentproto.DockerImageInspectPayload{
		ID: d.ID, RepoTags: d.RepoTags, SizeBytes: d.SizeBytes, Created: d.Created,
		Architecture: d.Architecture, OS: d.OS, Labels: d.Labels, Env: d.Env,
		ExposedPorts: d.ExposedPorts, Entrypoint: d.Entrypoint, Cmd: d.Cmd,
	}
	for _, l := range d.History {
		out.History = append(out.History, agentproto.DockerImageLayer{
			SizeBytes: l.SizeBytes, Created: l.Created, CreatedBy: l.CreatedBy,
			EmptyLayer: l.EmptyLayer, Comment: l.Comment,
		})
	}
	return out
}
