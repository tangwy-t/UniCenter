package dockerops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ── 项目 → 配置文件索引（持久化的可自愈推断量）──────────────────────────
//
// 为什么需要它：compose 的配置文件路径**只能**从容器标签解析（spec §8 路径白名单：
// 路径永不来自客户端），而没有容器的项目（`compose:down` 之后、或刚建好还没起过容器）
// 根本没有标签可看 —— 最自然的「down → up」流程会永远解析不到文件。
//
// 于是把快照里学到的「项目 → 主配置文件」落盘成索引。三条定位纪律：
//  1. **推断量而非凭据**：读不到/写不进都只记 Warn，从不阻断任何操作 ——
//     最坏情况只是退回一句可操作的结论句（见 compose_exec.go）；
//  2. **用途上仍受同一套路径纪律**：索引里的值交给 compose CLI 之前照样要过
//     「绝对路径 + 存在 + 普通文件」，而索引里的值最初也只来自容器标签 ——
//     客户端从头到尾不参与路径；
//  3. **不过期**：`compose:down` 成功后条目不删 —— 那正是为了让随后的 up 还能解析；
//     位置真的变了，由下一次快照的 Learn 覆盖。

// projectIndexFileName 是索引的落盘文件名（在 agent 状态目录下，与 docker_state.json 并列）。
const projectIndexFileName = "docker_projects.json"

// projectIndex 是持久化的「项目 → 配置文件」映射。
//
// 并发：Learn 在快照循环里被调用，Lookup 在分派器 worker 里被调用，故用互斥锁守护。
type projectIndex struct {
	path string
	log  Logger

	mu sync.Mutex
	m  map[string]string
}

// newProjectIndex 从 stateDir 载入索引。
//
// 文件不存在是正常情形（首次启动）；**损坏只记 Warn 并从空开始** —— 索引是可自愈的
// 推断量（下一次快照就会重建），没有任何理由让它挡住启动。
func newProjectIndex(stateDir string, log Logger) *projectIndex {
	if log == nil {
		log = nopLogger{}
	}
	idx := &projectIndex{path: filepath.Join(stateDir, projectIndexFileName), log: log, m: map[string]string{}}
	b, err := os.ReadFile(idx.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			idx.log.Warn("read docker project index failed", "err", err.Error())
		}
		return idx
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		idx.log.Warn("docker project index corrupt, starting empty", "err", err.Error())
		return idx
	}
	// 空键/空值不是有效的观测（Learn 也不接受）：加载时顺手清掉，免得它们被当成路径。
	for project, file := range m {
		if strings.TrimSpace(project) != "" && strings.TrimSpace(file) != "" {
			idx.m[project] = file
		}
	}
	return idx
}

// Learn 记录一次「项目 → 主配置文件」观测（来自容器标签的快照学习）。
//
// 空值不学（学了就是编造）；值没变不写盘（快照 30s 一次，同值反复写盘没有意义）。
// 落盘失败只记 Warn，不阻断 —— 内存里已经生效，下一次快照还会再学一遍。
func (p *projectIndex) Learn(project, configFile string) {
	if p == nil {
		return
	}
	project, configFile = strings.TrimSpace(project), strings.TrimSpace(configFile)
	if project == "" || configFile == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.m[project]; ok && old == configFile {
		return
	}
	p.m[project] = configFile
	p.persistLocked()
}

// Lookup 查一个项目的最近一次已知配置文件路径。
//
// 返回值**尚未**经过「存在 + 普通文件」校验：校验属于使用它的解析路径
// （compose_exec.go），索引只负责记住观测。
func (p *projectIndex) Lookup(project string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.m[project]
	return f, ok
}

// persistLocked 原子落盘（临时文件 + rename，与 Runtime.persist 及 agent token 的
// 落盘同款：进程在写到一半时被杀掉不会留下一份半截 JSON）；权限 0600，只有 agent 能读写。
// 调用方持锁，保证并发 Learn 的写盘顺序与内存顺序一致。
func (p *projectIndex) persistLocked() {
	b, err := json.MarshalIndent(p.m, "", "  ")
	if err != nil {
		p.log.Warn("marshal docker project index failed", "err", err.Error())
		return
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		p.log.Warn("write docker project index failed", "path", p.path, "err", err.Error())
		return
	}
	if err := os.Rename(tmp, p.path); err != nil {
		p.log.Warn("rename docker project index failed", "path", p.path, "err", err.Error())
	}
}

// learnProjects 把一帧容器里的 compose 项目 → 主配置文件写进索引。
//
// 只在两个标签都在时学习：缺 config_files 是旧版 compose 的如实状态，学了就是编造路径。
// 多文件（`-f a.yml -f b.yml`）取第一个（主文件）—— 与解析路径同一口径。
func learnProjects(idx *projectIndex, cs []ContainerInfo) {
	if idx == nil {
		return
	}
	for _, c := range cs {
		project := c.Labels[composeProjectLabel]
		raw := strings.TrimSpace(c.Labels[composeConfigFilesLabel])
		if project == "" || raw == "" {
			continue
		}
		idx.Learn(project, strings.TrimSpace(strings.Split(raw, ",")[0]))
	}
}

// ── 索引随 API 句柄进入解析 ─────────────────────────────────────────────

// projectIndexCarrier 是「API 句柄携带项目索引」的标记接口。
//
// 为什么挂在 API 句柄上而不是给每个执行器加字段：读（compose.file:read）与写
// （compose 写操作）两条路径共用同一个 composeConfigFileOf，而它们唯一共同的依赖
// 就是 DockerAPI —— 索引挂在同一个句柄上，两条解析自动都拿到它，不存在「某个执行器
// 忘了注入」的暗角。生产上由 Runtime 在 New 里包一层（见 runtime.go）。
type projectIndexCarrier interface {
	composeProjectIndex() *projectIndex
}

// withProjectIndex 把索引挂到 API 句柄上；api 或 idx 为 nil 时原样返回
// （测试替身与降级路径因此自然退回「只有标签」的旧行为）。
func withProjectIndex(api DockerAPI, idx *projectIndex) DockerAPI {
	if api == nil || idx == nil {
		return api
	}
	return &indexedAPI{DockerAPI: api, projects: idx}
}

// indexedAPI 是携带项目索引的 DockerAPI 装饰器：能力面全部透传（嵌入），
// 唯一新增的是索引的读取口。
type indexedAPI struct {
	DockerAPI
	projects *projectIndex
}

func (a *indexedAPI) composeProjectIndex() *projectIndex { return a.projects }

// projectIndexOfAPI 取 API 句柄上的索引（没有则 nil = 解析只有标签这一条路）。
func projectIndexOfAPI(api DockerAPI) *projectIndex {
	if c, ok := api.(projectIndexCarrier); ok {
		return c.composeProjectIndex()
	}
	return nil
}
