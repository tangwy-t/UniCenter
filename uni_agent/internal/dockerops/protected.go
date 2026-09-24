package dockerops

import "strings"

// 保护清单的粒度前缀（§10 第 3 条四种粒度）。
const (
	protectedProjectPrefix = "project:"
	protectedVolumePrefix  = "volume:"
)

// ProtectedList 是 sys.docker.protected 的解析结果。
//
// 四种粒度：`<容器名>` / `project:<项目名>` / `project:<项目名>/<服务>` / `volume:<卷名>`。
// 为什么卷要单列一种粒度：.105 的 mysql 数据卷与 uni-center-uploads 不在任何容器粒度内，
// 而「先停容器、再删卷」是两步就能绕开的路径 —— 卷粒度是堵这条路的唯一办法。
type ProtectedList struct {
	containers map[string]bool
	projects   map[string]bool
	// services 的键是 `项目/服务`：服务粒度必须**成对**命中，单独的 ⟨服务名⟩ 会跨项目误伤。
	services map[string]bool
	volumes  map[string]bool
}

// ParseProtected 解析清单原文（csv，容忍换行/空格/多余分隔符与前后空白）。
//
// 容忍脏输入不是「顺手」而是必需：运维在配置页里手写这份清单，一个多余的逗号若让
// 整条清单静默失效，等于底座容器失去保护，而失败是无声的。
func ParseProtected(raw string) *ProtectedList {
	p := &ProtectedList{
		containers: map[string]bool{},
		projects:   map[string]bool{},
		services:   map[string]bool{},
		volumes:    map[string]bool{},
	}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		switch {
		case strings.HasPrefix(item, protectedProjectPrefix):
			body := strings.TrimPrefix(item, protectedProjectPrefix)
			if body == "" {
				continue
			}
			if i := strings.IndexByte(body, '/'); i > 0 && i < len(body)-1 {
				p.services[body[:i]+"/"+body[i+1:]] = true
				continue
			}
			p.projects[body] = true
		case strings.HasPrefix(item, protectedVolumePrefix):
			if name := strings.TrimPrefix(item, protectedVolumePrefix); name != "" {
				p.volumes[name] = true
			}
		default:
			// 带未知前缀（拼错的粒度）的条目**忽略**而不是当成容器名：当成容器名会写入
			// 一个永不命中的幽灵条目，而真正想保护的目标失去保护，且没有任何提示。
			if strings.ContainsRune(item, ':') {
				continue
			}
			p.containers[item] = true
		}
	}
	return p
}

// Container 报告容器名是否在清单内。
func (p *ProtectedList) Container(name string) bool { return p != nil && p.containers[name] }

// Project 报告项目名是否在清单内。
func (p *ProtectedList) Project(name string) bool { return p != nil && p.projects[name] }

// Service 报告「项目/服务」是否在清单内。
func (p *ProtectedList) Service(project, service string) bool {
	return p != nil && p.services[project+"/"+service]
}

// Volume 报告卷名是否在清单内。
func (p *ProtectedList) Volume(name string) bool { return p != nil && p.volumes[name] }

// ContainerProtected 计算一个容器是否受保护：容器名 / 所属项目 / 项目+服务，任一命中即受保护。
//
// compose 元数据为空的裸容器只按名字判定（.105 上 8+ 个裸容器属于这种）。
func (p *ProtectedList) ContainerProtected(name, project, service string) bool {
	if p == nil {
		return false
	}
	if p.containers[name] {
		return true
	}
	if project == "" {
		return false
	}
	if p.projects[project] {
		return true
	}
	return p.services[project+"/"+service]
}

// IsEmpty 报告清单是否为空（空清单 = 没有任何目标受保护）。
func (p *ProtectedList) IsEmpty() bool {
	return p == nil || p.Size() == 0
}

// Size 返回清单条目数（日志与诊断用；**不**用于安全判断）。
func (p *ProtectedList) Size() int {
	if p == nil {
		return 0
	}
	return len(p.containers) + len(p.projects) + len(p.services) + len(p.volumes)
}
