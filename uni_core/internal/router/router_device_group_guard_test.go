package router

import (
	"os"
	"strings"
	"testing"
)

// TestDeviceGroupPatchRouteMountedWithGroupPerm 是 PATCH /devices/:id 的
// **挂载 + 权限**守卫。
//
// 为什么需要源码级断言（与 TestStatsHistoryRouteMountedWithInspectPerm 同型）：
// 路由级 perm 是中间件层的事 —— handler 层的测试挂在裸引擎上、不带权限也照旧全绿。
// 这条写端点的权限档是独立的 device:index:group；若被误挂到读面（device:query）
// 或直接丢了 perm，就等于「任何能看设备列表的人都能改设备的分组归属」，而这类
// 破坏平时完全静默（端点照常工作）。
func TestDeviceGroupPatchRouteMountedWithGroupPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const want = `devices.PATCH("/:id", perm(permission.PermDeviceGroup), deps.Device.DeviceHdl.UpdateGroup)`
	if !strings.Contains(text, want) {
		t.Fatalf("写分组端点必须以 device:index:group 挂在 devices 组上（当前缺失或权限档漂移），期待:\n%s", want)
	}
	// 不得降档/错档到别的 device 权限：失败形态就是一行之差，逐字盯住。
	for _, degraded := range []string{
		`devices.PATCH("/:id", perm(permission.PermDeviceQuery)`,
		`devices.PATCH("/:id", perm(permission.PermDeviceList)`,
		`devices.PATCH("/:id", perm(permission.PermDeviceEnable)`,
		`devices.PATCH("/:id", perm(permission.PermDeviceDisable)`,
		`devices.PATCH("/:id", perm(permission.PermDeviceDelete)`,
	} {
		if strings.Contains(text, degraded) {
			t.Fatalf("写分组端点不得降档到别的 device 权限（分组写是独立能力）: %s", degraded)
		}
	}
}