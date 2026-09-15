package permission

import (
	"sort"
	"testing"
)

func TestDevicePermsAreRegisteredInAll(t *testing.T) {
	want := []string{PermDeviceDelete, PermDeviceDisable, PermDeviceEnable, PermDeviceList, PermDeviceQuery}
	got := All()

	set := make(map[string]bool, len(got))
	for _, p := range got {
		if set[p] {
			t.Fatalf("All() 含重复权限码 %q", p)
		}
		set[p] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("权限码 %q 未加入 All()（新增权限必须三步：定义常量 → 加入 All() → 种子内引用）", w)
		}
	}
}

func TestAllIsSortedAscending(t *testing.T) {
	if got := All(); !sort.StringsAreSorted(got) {
		t.Fatalf("All() 必须升序（apigen 生成与漂移守卫依赖该顺序）")
	}
}

func TestDevicePermsUseDevicePrefix(t *testing.T) {
	for _, p := range []string{PermDeviceList, PermDeviceQuery, PermDeviceDelete, PermDeviceEnable, PermDeviceDisable} {
		if len(p) < len("device:") || p[:len("device:")] != "device:" {
			t.Fatalf("权限码 %q 必须使用 device: 前缀", p)
		}
	}
}
