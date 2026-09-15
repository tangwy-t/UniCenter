package migrations

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
)

func TestV008IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 8 {
			found = true
			if m.Up == nil {
				t.Fatal("v008 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v008 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

func TestV008MenuPermsExistInPermissionRegistry(t *testing.T) {
	all := map[string]bool{}
	for _, p := range permission.All() {
		all[p] = true
	}
	for _, m := range deviceMenuDefinitions {
		if m.Perms == "" {
			continue
		}
		if !all[m.Perms] {
			t.Fatalf("菜单 %q 引用了未在 permission.All() 注册的权限码 %q", m.Key, m.Perms)
		}
	}
}

func TestV008MenuParentsResolve(t *testing.T) {
	defined := map[string]bool{}
	for _, m := range deviceMenuDefinitions {
		defined[m.Key] = true
	}
	for _, m := range deviceMenuDefinitions {
		if m.Parent == "" {
			continue
		}
		if !defined[m.Parent] {
			t.Fatalf("菜单 %q 的父亲 %q 不在本批定义内（父必须先于子、且不能是外部 key）", m.Key, m.Parent)
		}
	}
}

func TestV008ConfigKeysAreUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range deviceConfigDefinitions {
		if seen[c.Key] {
			t.Fatalf("配置键重复：%s", c.Key)
		}
		seen[c.Key] = true
		if len(c.Key) < len("sys.agent.") || c.Key[:len("sys.agent.")] != "sys.agent." {
			t.Fatalf("配置键 %q 必须使用 sys.agent. 前缀", c.Key)
		}
	}
}
