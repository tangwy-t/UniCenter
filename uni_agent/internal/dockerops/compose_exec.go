package dockerops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── compose 写路径（B2）：固定 argv + flavor 纪律 ────────────────────────
//
// 三条硬纪律（spec §6.1 / §6.3、§10 执行纪律）：
//  1. **永远显式 -p <项目名>**：项目名是协议校验过的字段，而 CLI 按目录名推断可能得到
//     与快照不一致的名字（v1/v2 的项目名规范化规则不同，§6.2），upsert 会变成
//     「另起一个项目」；
//  2. **永不经 shell**：参数以列表交给 exec，且 argv 里的可变片段（项目名/服务名/
//     配置文件路径）在拼接前全部再过一遍白名单（纵深防御 —— 协议层已校验）；
//  3. **只用公共操作子集**：up -d / stop / start / restart / pull / down / rm /
//     --scale / --remove-orphans 在 v2 插件、v2 独立与 v1 上都存在；
//     `--wait` 之类不许出现（§6.1 矩阵）。

// composeConfigFileOf 解析一个 compose 项目的配置文件绝对路径。
//
// 路径**只能**来自本机事实（容器标签 / agent 自己落盘的索引），协议上从不接受
// 路径入参 —— 路径白名单纪律（§8）：UI 只传项目名，路径逃逸面因此不存在。
// read.go 的 compose.file:read 与本文件的 compose 写路径共用这一份解析。
//
// 解析顺序：
//  1. **容器标签**（com.docker.compose.project.config_files）——最新真相，优先；
//  2. **项目索引**（project_index.go）——没有容器的项目（down 之后）没有标签可看，
//     靠它解析出上次快照学到的位置；这是「down → up」能走通的关键；
//  3. 两者都没有 → **可操作的结论句**（请在主机上先把项目起一次），而不是笼统的
//     「没有找到这个项目」——后者把「路径未知」误报成「项目不存在」，用户无从下手。
func composeConfigFileOf(ctx context.Context, api DockerAPI, project string) (string, error) {
	cs, err := api.Containers(ctx)
	if err != nil {
		return "", &ExecError{Msg: "读取项目信息失败", Detail: err.Error()}
	}
	hasContainer := false
	for _, c := range cs {
		if c.Labels[composeProjectLabel] != project {
			continue
		}
		hasContainer = true
		raw := strings.TrimSpace(c.Labels[composeConfigFilesLabel])
		if raw == "" {
			// 旧版 compose 不写这个标签：这个容器给不出路径；也许索引里存着上一次学到的。
			continue
		}
		// 可能是多个文件（-f a.yml -f b.yml）：取第一个（主文件）。
		path := strings.TrimSpace(strings.Split(raw, ",")[0])
		if !filepath.IsAbs(path) {
			return "", &ExecError{Msg: "配置文件位置异常（不是绝对路径）"}
		}
		fi, err := os.Stat(path)
		if err != nil {
			return "", &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
		}
		if fi.IsDir() {
			return "", &ExecError{Msg: "配置文件位置异常（它是一个目录）"}
		}
		return path, nil
	}
	// 标签给不出路径：回落到持久化索引（最近一次快照学到的位置）。
	if path, ok := projectIndexOfAPI(api).Lookup(project); ok {
		return validatedIndexedConfigFile(project, path)
	}
	if hasContainer {
		// 容器在、但路径无从得知（旧版 compose 不记标签，索引里也没有历史）：如实标注。
		return "", &ExecError{Msg: "这个项目的配置文件位置未知（旧版 compose 未记录）"}
	}
	return "", &ExecError{Msg: "这个项目的配置文件位置未知：本机还没有它的容器。请先在主机上执行一次 `docker compose up -d`，之后控制台就能管理它"}
}

// validatedIndexedConfigFile 校验索引给出的路径，纪律与标签路径**完全一致**
// （绝对路径 + 存在 + 是普通文件）——索引只是推断量，没有资格绕过路径检查。
//
// 文件已被移动/删除时，给出的结论句说明「配置文件已不存在」，且绝不把这条失效路径
// 交给 argv（调用方在错误分支不会执行 CLI）。
func validatedIndexedConfigFile(project, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", &ExecError{Msg: "配置文件位置异常（不是绝对路径）"}
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &ExecError{
				Msg:    "这个项目的配置文件已不存在（它可能在主机上被移动或删除了）。请先在主机上执行一次 `docker compose up -d`，控制台会重新记住它的位置",
				Detail: "project index: " + project + " -> " + path,
			}
		}
		return "", &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
	}
	if fi.IsDir() {
		return "", &ExecError{Msg: "配置文件位置异常（它是一个目录）"}
	}
	return path, nil
}

// composeNames 在拼 argv 之前把 target 再解析一次。
//
// 协议层（ValidateDockerCmdOptions）已按 action 校验过 target，这里再走一遍同一组
// 白名单是**纵深防御**：agent 是最后一道；且这样一来「argv 里的每个可变片段都经过
// 白名单」这件事集中在一处可见，不会出现「某个分支漏校验」的暗角。
func composeNames(action, target string) (project, service string, err error) {
	switch action {
	case agentproto.DockerActionComposeServiceScale,
		agentproto.DockerActionComposeServiceRemoveContainers:
		p, s, ok := agentproto.SplitDockerProjectService(target)
		if !ok {
			return "", "", &ExecError{Msg: "服务目标不合法（应为「项目名/服务名」）"}
		}
		return p, s, nil
	default:
		if !agentproto.IsDockerProjectName(target) {
			return "", "", &ExecError{Msg: "项目名不合法"}
		}
		return target, "", nil
	}
}

// composeBinary 返回 flavor 对应的可执行文件名。
//
// plugin 形态是 docker 的一个**子命令**（argv 里要带 "compose"），独立二进制/v1 则是
// 命令本身 —— 没有 compose 子命令。把两种形态混起来会执行成
// `docker-compose compose up`（命令不存在）。
func composeBinary(flavor string) (string, error) {
	switch flavor {
	case agentproto.DockerComposeFlavorPlugin:
		return "docker", nil
	case agentproto.DockerComposeFlavorStandaloneV2, agentproto.DockerComposeFlavorV1:
		return "docker-compose", nil
	default:
		// flavor 是探测后落盘的事实（§6.2 单一 flavor 纪律）。没有它就无法判断 CLI
		// 形态，而猜一个只会得到「docker: 'compose' is not a docker command」这类噪音。
		return "", &ExecError{Msg: "这台主机上没有检测到可用的 compose"}
	}
}

// composeArgv 把 action+options 折成 compose CLI 的固定 argv。
//
// 返回值不含二进制名；plugin 形态的首元素是 "compose"（`docker compose …` 的子命令），
// 独立二进制/v1 形态没有这个前缀（`docker-compose …`）。
func (e *WriteExecutor) composeArgv(action string, o *agentproto.DockerCmdOptions, configFile string) ([]string, error) {
	project, service, err := composeNames(action, o.Target)
	if err != nil {
		return nil, err
	}
	// 配置文件路径同样只接受绝对路径：它来自容器标签，这里是「所有进入 argv 的可变片段
	// 都过一遍检查」的收口；相对路径会让 CLI 按 agent 的工作目录去猜，结果不可预期。
	if !filepath.IsAbs(configFile) {
		return nil, &ExecError{Msg: "配置文件位置异常（不是绝对路径）"}
	}
	flavor := e.flavorValue()
	if _, err := composeBinary(flavor); err != nil {
		return nil, err
	}
	argv := make([]string, 0, 9)
	if flavor == agentproto.DockerComposeFlavorPlugin {
		argv = append(argv, "compose")
	}
	argv = append(argv, "-p", project, "-f", configFile)
	switch action {
	case agentproto.DockerActionComposeUp:
		// --remove-orphans **默认带**（§6.3：被移除的定义不回收就会留下孤儿容器，
		// 「删不干净」）。协议上 remove_orphans 的 false 与缺席同义，故没有分支。
		argv = append(argv, "up", "-d", "--remove-orphans")
	case agentproto.DockerActionComposeStop:
		argv = append(argv, "stop")
	case agentproto.DockerActionComposeStart:
		argv = append(argv, "start")
	case agentproto.DockerActionComposeRestart:
		argv = append(argv, "restart")
	case agentproto.DockerActionComposePull:
		argv = append(argv, "pull")
	case agentproto.DockerActionComposeDown:
		argv = append(argv, "down")
		if o.Volumes {
			// 连带删卷是显式选项（默认不删）：卷里是数据，删卷的确认档独立成立。
			argv = append(argv, "-v")
		}
	case agentproto.DockerActionComposeServiceScale:
		if o.N == nil {
			// 协议里 n 是必填；走到这里是伪造/旧 core。扩缩容没有安全的默认值，不猜。
			return nil, &ExecError{Msg: "缺少实例数，无法扩缩容"}
		}
		// N 经 strconv 折成十进制：不可能携带 flag 分隔符或 shell 元字符。
		// n=0 的确认档由协议层承担（ExpectedDockerConfirm），argv 与普通缩容同形。
		argv = append(argv, "up", "-d", "--scale", service+"="+strconv.Itoa(*o.N))
	case agentproto.DockerActionComposeServiceRemoveContainers:
		// 「仅删容器、保留定义」= rm -f -s（-s 先停再删；定义还在，下次 up 重建）。
		argv = append(argv, "rm", "-f", "-s", service)
	default:
		return nil, &ExecError{Msg: "该操作尚未开放"}
	}
	return argv, nil
}

// composeFailureMsg 按 action 给出**结论句**（页面直接显示）。
//
// CLI/daemon 的英文原文只进 detail —— 把 stderr 原样显示给用户是另一类问题。
func composeFailureMsg(action string) string {
	switch action {
	case agentproto.DockerActionComposeUp, agentproto.DockerActionComposeStart:
		return "启动项目失败"
	case agentproto.DockerActionComposeStop:
		return "停止项目失败"
	case agentproto.DockerActionComposeRestart:
		return "重启项目失败"
	case agentproto.DockerActionComposePull:
		return "拉取项目镜像失败"
	case agentproto.DockerActionComposeDown:
		return "下线项目失败"
	case agentproto.DockerActionComposeServiceScale:
		return "调整服务实例数失败"
	case agentproto.DockerActionComposeServiceRemoveContainers:
		return "删除服务容器失败"
	default:
		return "执行项目操作失败"
	}
}

// doCompose 执行一条 compose 类写操作（write.go 的 Do 在 compose 分支上调用）。
//
// 顺序：目标名再校验 → 保护强制 → flavor 与配置文件解析 → 固定 argv 执行。
// 保护强制排在配置文件解析之前：拒绝不该先付一次 daemon 往返，也不该在拒绝前
// 暴露「这个项目存不存在」。
func (e *WriteExecutor) doCompose(ctx context.Context, cmd *agentproto.DockerCmd) error {
	o := &cmd.Options
	project, service, err := composeNames(cmd.Action, o.Target)
	if err != nil {
		return err
	}
	if err := e.guard(ctx, protectedTarget{project: project, service: service}, o.Force); err != nil {
		return err
	}
	if _, err := composeBinary(e.flavorValue()); err != nil {
		return err
	}
	configFile, err := composeConfigFileOf(ctx, e.api, project)
	if err != nil {
		return err
	}
	return e.composeRun(ctx, cmd.Action, o, configFile)
}

// composeRun 执行一条 compose 写操作：固定 argv、零 shell。
//
// 失败时取 CLI 的合并输出（stderr 是 compose 的错误出口）折进 detail；成功不产生
// 载荷 —— 项目类操作的结论在快照里（下一帧即反映），不需要数据面。
func (e *WriteExecutor) composeRun(ctx context.Context, action string, o *agentproto.DockerCmdOptions, configFile string) error {
	argv, err := e.composeArgv(action, o, configFile)
	if err != nil {
		return err
	}
	bin, err := composeBinary(e.flavorValue())
	if err != nil {
		return err
	}
	out, err := e.exec(ctx, bin, argv...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return &ExecError{Msg: composeFailureMsg(action), Detail: detail}
	}
	return nil
}
