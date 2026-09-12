package migrations

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/permission"
)

// TestSeedPermsMatchPermissionRegistry 是权限码漂移守卫:种子菜单(menuDefinitions)
// 是权限码的数据落点,permission.All() 是唯一枚举。二者必须一致 ——
//  1. 每个非空种子 Perms 值都注册在 All() 内(防止有人绕开常量直接写裸串);
//  2. All() 内每个常量都至少被种子引用一次(防止定义了死常量)。
//
// 路由层 router.go 引用同一批常量(编译器保证),故此测试即保证
// 种子 ↔ 路由 ↔ 注册表 三者始终一致。
func TestSeedPermsMatchPermissionRegistry(t *testing.T) {
	allowed := make(map[string]bool, len(permission.All()))
	for _, code := range permission.All() {
		allowed[code] = true
	}

	used := make(map[string]int)
	for _, m := range menuDefinitions {
		if m.Perms == "" {
			continue
		}
		if !allowed[m.Perms] {
			t.Errorf("seed Perms %q 未注册进 permission.All():需定义常量并加入 All()", m.Perms)
		}
		used[m.Perms]++
	}

	for _, code := range permission.All() {
		if used[code] == 0 {
			t.Errorf("permission.All() 中的 %q 未被任何种子菜单引用(死常量)", code)
		}
	}
}

// TestPermissionAllNoDuplicates 保证注册表本身不含重复项。
func TestPermissionAllNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(permission.All()))
	for _, code := range permission.All() {
		if seen[code] {
			t.Errorf("permission.All() 含重复权限码: %q", code)
		}
		seen[code] = true
	}
}
