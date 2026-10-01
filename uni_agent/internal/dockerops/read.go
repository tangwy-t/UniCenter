package dockerops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

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
// 路径解析（**只能**来自容器标签）与 compose 写路径共用 composeConfigFilesOf
// （见 compose_exec.go）—— 复制两份会让「路径白名单」有两个实现，而它只需要一个。
// 它返回项目的全部文件（主文件 + override），而编辑器只编辑**主文件**：取列表
// 首元素（标签原序的第一项），对外行为与修复前一致。
func (e *ReadExecutor) composeFile(ctx context.Context, project string) (*agentproto.DockerComposeFilePayload, error) {
	files, err := composeConfigFilesOf(ctx, e.api, project)
	if err != nil {
		return nil, err
	}
	path := files[0]
	// 先看大小再读：超限的文件不该被读进内存（上限见 §13）。
	fi, err := os.Stat(path)
	if err != nil {
		return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
	}
	if fi.Size() > agentproto.MaxDockerComposeFileBytes {
		return nil, &ExecError{Msg: "配置文件过大（超过 1MB）"}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
	}
	sum := sha256.Sum256(b)
	// 备份历史（v1.2.4）：与写路径的列表共用同一个扫描（最多 10 份、时间倒序、
	// 含各自 hash/size/at）——「读到的备份」与「回滚用的令牌」同源。
	backups, err := listComposeBackups(path)
	if err != nil {
		return nil, &ExecError{Msg: "读取备份列表失败", Detail: err.Error()}
	}
	return &agentproto.DockerComposeFilePayload{
		Content: string(b),
		Hash:    hex.EncodeToString(sum[:]),
		Path:    path,
		Backups: backups,
	}, nil
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
