package dockerops

import (
	"context"
	"errors"
	"strconv"
	"strings"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── container:create（四支柱·创建面，4a）────────────────────────────────────
//
// 两条本切片定下的取舍：
//
//  1. **镜像本地不存在不自动拉取**：拉取是独立 action（image:pull，15 分钟超时档），
//     混进 create 会让 create 的执行时限与结论语义都要为「最慢 15 分钟」兜底 ——
//     用户看到的也不再是「创建好了吗」而是漫长的无响应。create 保持 30 秒本地
//     操作档，镜像缺失给「先拉取」结论句：拉完再建，动作边界与超时纪律各自成立。
//  2. **名字冲突对照 daemon 的 409 翻译**：结论句是中文，不进用户可见面的
//     daemon 英文原文只落 detail —— 与模块「结论句 + 排障细节」的既有纪律一致。
//
// 保护守卫（guard）不适用：保护清单管的是**现存**容器/卷的停删重建，create 的
// 对象还不存在，谈不上受保护 —— 新容器诞生后保护与否，由它后续的 compose 标签
// 与运维配置决定（下一帧快照自会算好 Protected）。

// createPayload 是 create 成功的结果数据。
type createPayload struct {
	// ID 是完整容器 ID：后续容器级操作（container:start 等）可以直接引用它，
	// 或按它在下一次快照里定位新增条目。
	ID string `json:"id"`
	// ShortID 是 12 位短 id（docker CLI/ps 的展示惯例），供页面在结论里显示。
	ShortID string `json:"short_id"`
	// Started 表示是否已按 start 启动（false = 只创建，配合创建抽屉的「仅创建」）。
	Started bool `json:"started"`
}

// shortDockerID 取 12 位短 id（不足 12 位原样）。
func shortDockerID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// createContainer 执行 container:create：解析 options → 创建 →（按需）启动。
//
// 写路径的路径纪律在挂载源上有一个**有意为之**的例外（见协议侧 validateDockerMount
// 的注释）：bind 挂载源是用户要挂的真实目录，属于 docker:manage 权限覆盖的运维
// 决定 —— 这一面的护栏是协议白名单（必须绝对路径、无 NUL/换行），而非 transferDir
// 式的「agent 只拼不接」。
func (e *WriteExecutor) createContainer(ctx context.Context, o *agentproto.DockerCmdOptions) ([]byte, error) {
	// 再验一遍（纵深防御）：dispatcher 受理前已经跑过同一函数，但协议校验是纯函数、
	// 代价可忽略 —— 与 guard 的「core 可被绕过，agent 是最后一道」同一理由，
	// 执行器不信任上游的校验。结论与 dispatcher 的参数档同款（400 与执行拒绝
	// 说同一句话）。
	if err := agentproto.ValidateDockerCmdOptions(agentproto.DockerActionContainerCreate, o); err != nil {
		return nil, &ExecError{Msg: "指令参数不合法", Detail: agentproto.NormalizeDockerDetail(err.Error())}
	}
	spec, err := buildCreateSpec(o)
	if err != nil {
		// 走到这里 = 协议校验与解析口径裂开了（开发期错误，正常路径不可达）。
		return nil, err
	}
	id, err := e.api.ContainerCreate(ctx, spec)
	switch {
	case errors.Is(err, errImageNotFound):
		return nil, &ExecError{
			Msg:    "本机没有这个镜像，请先拉取镜像后再创建容器",
			Detail: err.Error(),
		}
	case errors.Is(err, errContainerNameConflict):
		return nil, &ExecError{
			Msg:    "同名容器已存在，请换一个容器名",
			Detail: err.Error(),
		}
	case err != nil:
		return nil, wrapDocker("创建容器失败", err)
	}

	payload := &createPayload{ID: id, ShortID: shortDockerID(id)}
	start := o.Start == nil || *o.Start
	payload.Started = start
	if start {
		if err := e.api.ContainerStart(ctx, id); err != nil {
			// 启动失败但容器已建成：算失败，结论句**必须带上短 id** —— 否则
			// 用户会在列表里看到一个刚多出来的容器，却不知道它是从哪来的。
			return nil, &ExecError{
				Msg:    "容器已创建但启动失败（" + payload.ShortID + "），可稍后在容器页手动启动",
				Detail: err.Error(),
			}
		}
	}
	// 成功附注带短 id：result.detail 是成功路径唯一的自由文本槽（payload 是结构化面）。
	e.setNote("已创建容器 " + payload.ShortID)
	return marshalWritePayload(payload)
}

// buildCreateSpec 把协议 options 解析成已定型的参数。协议校验在前一道闸跑过，
// 这里的失败 = 校验与解析口径不一致的开发期红灯 —— 拒绝结论与参数档同款，
// detail 指明具体字段（detail 不带用户可见的义务，但它是排查「为什么两处不一致」
// 的唯一线索）。
func buildCreateSpec(o *agentproto.DockerCmdOptions) (ContainerCreateSpec, error) {
	spec := ContainerCreateSpec{
		Image:         o.Image,
		Name:          o.Name,
		Env:           o.Env,
		RestartPolicy: o.RestartPolicy,
		CPULimit:      o.CPULimit,
		MemLimitMB:    o.MemLimitMB,
		Network:       o.Network,
	}
	ports, err := parseCreatePorts(o.Ports)
	if err != nil {
		return ContainerCreateSpec{}, err
	}
	spec.Ports = ports
	mounts, err := parseCreateMounts(o.Mounts)
	if err != nil {
		return ContainerCreateSpec{}, err
	}
	spec.Mounts = mounts
	return spec, nil
}

// parseCreatePorts 解析 `宿主:容器[/协议]` 列表（协议缺省 tcp）。
func parseCreatePorts(in []string) ([]CreatePortBinding, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]CreatePortBinding, 0, len(in))
	for _, s := range in {
		body, proto := s, "tcp"
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			body, proto = s[:i], s[i+1:]
		}
		h, c, ok := strings.Cut(body, ":")
		if !ok {
			return nil, badCreateField("ports")
		}
		host, err1 := strconv.Atoi(h)
		cont, err2 := strconv.Atoi(c)
		if err1 != nil || err2 != nil {
			return nil, badCreateField("ports")
		}
		out = append(out, CreatePortBinding{Host: host, Container: cont, Proto: proto})
	}
	return out, nil
}

// parseCreateMounts 解析 `源:目的地[:ro]` 列表。源以 '/' 开头 = bind（宿主路径），
// 否则按命名卷名给出（与协议校验的判断规则一致 —— 两处必须同源，见注释）。
func parseCreateMounts(in []string) ([]CreateMount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]CreateMount, 0, len(in))
	for _, s := range in {
		parts := strings.SplitN(s, ":", 3)
		if len(parts) < 2 {
			return nil, badCreateField("mounts")
		}
		ro := len(parts) == 3 // 协议校验保证第三段只可能是 ro
		m := CreateMount{Source: parts[0], Dest: parts[1], ReadOnly: ro}
		m.Bind = strings.HasPrefix(parts[0], "/")
		out = append(out, m)
	}
	return out, nil
}

// badCreateField 是「协议已放行、解析却失败」的结论句 + 排障线索。
func badCreateField(field string) error {
	return &ExecError{Msg: "指令参数不合法", Detail: "container:create 的 " + field + " 无法解析"}
}
