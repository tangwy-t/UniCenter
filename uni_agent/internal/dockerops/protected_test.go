package dockerops

import "testing"

// 默认清单是**种子里的真实值**（v015 配置键），故用逐字原文做用例：
// 它是「uni-center 底座删不动」这条承诺在 agent 侧的落点。
const defaultProtected = "uni-center-core,uni-center-console,mysql,redis,project:uni-center,volume:uni-center-uploads"

func TestProtectedListMatchesDefaultSeed(t *testing.T) {
	p := ParseProtected(defaultProtected)
	cases := []struct {
		name                    string
		container, project, svc string
		want                    bool
	}{
		{"底座容器按名字受保护", "uni-center-core", "uni-center", "uni_core", true},
		{"数据库按名字受保护", "mysql", "", "", true},
		{"项目内任意服务受保护", "uni-center-console", "uni-center", "uni_console", true},
		{"项目内新加的服务也受保护", "any-new-svc", "uni-center", "new", true},
		{"无关容器不受保护", "zentao", "", "", false},
		{"同名前缀但不同名不受保护", "uni-center-core-2", "uni-center-x", "core", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.ContainerProtected(c.container, c.project, c.svc); got != c.want {
				t.Fatalf("ContainerProtected(%q,%q,%q) = %v, want %v", c.container, c.project, c.svc, got, c.want)
			}
		})
	}
	// 卷是独立粒度：**停止容器后卷即无主**，「先停再删」两步绕过必须被 volume: 堵住
	if !p.Volume("uni-center-uploads") {
		t.Fatal("volume:uni-center-uploads 必须受保护（否则可先停容器再删卷）")
	}
	if p.Volume("some-other-volume") {
		t.Fatal("未列出的卷不受保护")
	}
}

// 服务粒度必须**成对**命中：只写 project:名/服务 时，同项目的**其它**服务不受保护。
func TestProtectedServiceGranularity(t *testing.T) {
	p := ParseProtected("project:shop/db")
	if !p.ContainerProtected("shop-db-1", "shop", "db") {
		t.Fatal("project:shop/db 应命中 shop 项目下的 db 服务")
	}
	if p.ContainerProtected("shop-web-1", "shop", "web") {
		t.Fatal("project:shop/db 不应命中同项目的 web 服务（否则服务粒度没有意义）")
	}
	if p.ContainerProtected("other-db-1", "other", "db") {
		t.Fatal("project:shop/db 不应命中其它项目的同名服务")
	}
}

// 解析必须容忍运维手写的真实形态：换行/空格/多余逗号/前后空白。
// 一个多余的逗号让整条清单静默失效，等于底座失去保护 —— 这是不可接受的失败模式。
func TestParseProtectedToleratesMessyInput(t *testing.T) {
	p := ParseProtected(" mysql ,\n uni-center-core ,, \nproject:uni-center , volume:x-vol \n")
	for _, name := range []string{"mysql", "uni-center-core"} {
		if !p.Container(name) {
			t.Errorf("%q 应被解析出来（分隔符容忍失败）", name)
		}
	}
	if !p.Project("uni-center") {
		t.Error("project:uni-center 应被解析出来")
	}
	if !p.Volume("x-vol") {
		t.Error("volume:x-vol 应被解析出来")
	}
	if p.IsEmpty() {
		t.Error("非空清单不应报空")
	}
	if got := ParseProtected("").Size(); got != 0 {
		t.Fatalf("空清单的条目数应为 0，实际 %d", got)
	}
	if !ParseProtected("").IsEmpty() {
		t.Error("空输入必须是空清单（而不是 nil 崩溃）")
	}
	// 未知形态（拼错的粒度前缀）忽略而不是当成容器名：把 "projet:x" 当成容器名
	// 会让保护清单里出现一个永不命中的幽灵条目，而真正想保护的目标失去保护。
	if ParseProtected("projet:uni-center").Container("projet:uni-center") {
		t.Error("未知粒度前缀必须被忽略，不得降级成容器名")
	}
}
