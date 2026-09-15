package permission

import "testing"

func TestDevicePermsAreRegisteredInAll(t *testing.T) {
	want := []string{PermDeviceDelete, PermDeviceDisable, PermDeviceEnable, PermDeviceList, PermDeviceQuery}

	set := make(map[string]bool, len(All()))
	for _, p := range All() {
		set[p] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("权限码 %q 未加入 All()（新增权限必须三步：定义常量 → 加入 All() → 种子内引用）", w)
		}
	}
}

func TestDevicePermsUseDevicePrefix(t *testing.T) {
	const prefix = "device:"
	for _, p := range []string{PermDeviceList, PermDeviceQuery, PermDeviceDelete, PermDeviceEnable, PermDeviceDisable} {
		if len(p) < len(prefix) || p[:len(prefix)] != prefix {
			t.Fatalf("权限码 %q 必须使用 %s 前缀", p, prefix)
		}
	}
}

// 注意：此处刻意**不**断言 All() 的值升序 —— 基线自身并不满足该性质
// （见本计划文末《评审修正记录》§1）。重复项由 v002 的
// TestPermissionAllNoDuplicates 守卫，这里不重复。
