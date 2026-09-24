package migrations

import (
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
)

// 菜单与配置键的**存在性**守卫（DB 无关的部分）：定义列表本身的完整性。
//
// 真正落库的验证在既有 v002_seed_menu_perm_test（权限双向）与集成测试里；
// 这里守的是「定义写对了」这件事，让错误在单测阶段就暴露。
func TestDockerMenuDefinitionsAreComplete(t *testing.T) {
	// 五个列表页 + 一个目录
	var menus, dirs, btns int
	keys := map[string]bool{}
	for _, d := range dockerMenuDefinitions {
		if keys[d.Key] {
			t.Fatalf("重复的菜单键 %q（同键会让后一条覆盖前一条的 id 解析）", d.Key)
		}
		keys[d.Key] = true
		switch d.Type {
		case "dir":
			dirs++
		case "menu":
			menus++
			if !strings.HasPrefix(d.Path, "/docker/") {
				t.Fatalf("菜单 %s 的 path 必须是 /docker/ 前缀（与前端路由逐字一致）", d.Key)
			}
			if d.Component == "" || d.Component != strings.TrimPrefix(d.Path, "/") {
				t.Fatalf("菜单 %s 的 Component(%q) 必须等于去掉前导 / 的 Path(%q)", d.Key, d.Component, d.Path)
			}
		case "btn":
			btns++
			if d.Perms == "" {
				t.Fatalf("按钮 %s 必须承载一个权限码（否则它是死节点）", d.Key)
			}
		default:
			t.Fatalf("未知菜单类型 %q", d.Type)
		}
	}
	if dirs != 1 || menus != 5 {
		t.Fatalf("应为 1 个目录 + 5 个列表页，实际 %d + %d", dirs, menus)
	}
	if btns != 5 {
		t.Fatalf("应为 5 个按钮权限节点，实际 %d", btns)
	}
	// 六个权限码必须全部被引用（与 v002 守卫同口径，这里做一次快速自检）
	used := map[string]bool{}
	for _, d := range dockerMenuDefinitions {
		if d.Perms != "" {
			used[d.Perms] = true
		}
	}
	for _, code := range []string{
		permission.PermDockerList, permission.PermDockerInspect, permission.PermDockerManage,
		permission.PermDockerDelete, permission.PermDockerConfig, permission.PermDockerExec,
	} {
		if !used[code] {
			t.Fatalf("权限码 %s 没有菜单/按钮承载（v002 双向守卫会判为死常量）", code)
		}
	}
}

// 默认保护清单必须包含底座（它一旦为空，uni-center 自己的容器就成了可删对象）。
func TestDockerProtectedDefaultCoversBase(t *testing.T) {
	var v string
	for _, d := range dockerConfigDefinitions {
		if d.Key == "sys.docker.protected" {
			v = d.Value
		}
	}
	for _, must := range []string{"uni-center-core", "uni-center-console", "mysql", "redis",
		"project:uni-center", "volume:uni-center-uploads"} {
		if !strings.Contains(v, must) {
			t.Fatalf("默认保护清单缺 %q：%q", must, v)
		}
	}
	// 快照周期的默认值必须在协议允许区间内（<10 会被协议层拒）
	for _, d := range dockerConfigDefinitions {
		if d.Key == "sys.docker.snapshotInterval" && d.Value != "30" {
			t.Fatalf("快照周期默认值应为 30，实际 %q", d.Value)
		}
	}
}
