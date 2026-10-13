package service

import (
	"fmt"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 全局跨资源检索（Wave 4d：GET /docker/search）的 service 测试。
//
// 夹具沿用 resFixture（miniredis + 确定「现在」的读面，四台主机 3/7/8/9 已登记）。

// searchGroup 按 kind 取一个分组（groups 顺序固定，但按 kind 取更稳）。
func searchGroup(t *testing.T, resp *response.DockerSearchResp, kind string) response.DockerSearchGroup {
	t.Helper()
	for _, g := range resp.Groups {
		if g.Kind == kind {
			return g
		}
	}
	t.Fatalf("响应里没有 %q 分组: %+v", kind, resp.Groups)
	return response.DockerSearchGroup{}
}

// searchKinds 是 groups 的固定顺序（响应契约）。
var searchKinds = []string{"containers", "images", "volumes", "networks", "projects"}

// TestDockerSearchCrossTypeAndCaseInsensitive 跨类型命中 + 大小写不敏感 + 只匹配名称/镜像名：
//   - 五类资源按名称子串各命中一枚；
//   - 大写/小写两种敲法命中集一致；
//   - 容器**额外**按镜像名命中（与 Workloads 的 keyword 同一句）；
//   - 其它字段（如容器 statusText）不参与匹配。
func TestDockerSearchCrossTypeAndCaseInsensitive(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{
		Containers: []agentproto.DockerContainer{
			{ID: "c1", Name: "mysql", Image: "mysql:8.0.22", State: "running"},
			{ID: "c2", Name: "web", Image: "Redis:7", State: "running"}, // 名称不含，镜像含
			{ID: "c3", Name: "foo", Image: "bar:1", State: "running", StatusText: "zzztoken"},
		},
		Images:   []agentproto.DockerImage{{ID: "i-mysql", RepoTags: []string{"mysql:8.0.22"}}},
		Volumes:  []agentproto.DockerVolume{{Name: "mysql-data", Driver: "local"}},
		Networks: []agentproto.DockerNetwork{{Name: "MySQL-Net", Driver: "bridge"}},
		Projects: []agentproto.DockerProject{{Name: "mysql-stack", Services: 2}},
	})

	for _, kw := range []string{"mysql", "MYSQL"} {
		resp, err := f.svc.Search(f.ctx, kw, 0)
		if err != nil {
			t.Fatal(err)
		}
		if g := searchGroup(t, resp, "containers"); g.Total != 1 || g.Items[0].Name != "mysql" {
			t.Fatalf("keyword=%q 容器组应恰命中 mysql: %+v", kw, g)
		}
		if g := searchGroup(t, resp, "images"); g.Total != 1 || g.Items[0].Name != "mysql:8.0.22" {
			t.Fatalf("keyword=%q 镜像组应恰命中 mysql:8.0.22: %+v", kw, g)
		}
		if g := searchGroup(t, resp, "volumes"); g.Total != 1 || g.Items[0].Name != "mysql-data" {
			t.Fatalf("keyword=%q 卷组应恰命中 mysql-data: %+v", kw, g)
		}
		if g := searchGroup(t, resp, "networks"); g.Total != 1 || g.Items[0].Name != "MySQL-Net" {
			t.Fatalf("keyword=%q 网络组应恰命中 MySQL-Net（大小写不敏感）: %+v", kw, g)
		}
		if g := searchGroup(t, resp, "projects"); g.Total != 1 || g.Items[0].Name != "mysql-stack" {
			t.Fatalf("keyword=%q 项目组应恰命中 mysql-stack: %+v", kw, g)
		}
	}

	// 容器额外匹配镜像名：redis 只命中容器的**镜像**（没有镜像资源）。
	resp, err := f.svc.Search(f.ctx, "redis", 0)
	if err != nil {
		t.Fatal(err)
	}
	if g := searchGroup(t, resp, "containers"); g.Total != 1 || g.Items[0].Name != "web" {
		t.Fatalf("容器必须按镜像名命中（与 Workloads 同一句）: %+v", g)
	}
	if g := searchGroup(t, resp, "images"); g.Total != 0 {
		t.Fatalf("redis 不应命中镜像资源: %+v", g)
	}

	// 只匹配名称/镜像名：容器 statusText 里的 token 不参与匹配。
	resp, err = f.svc.Search(f.ctx, "zzztoken", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range resp.Groups {
		if g.Total != 0 || len(g.Items) != 0 {
			t.Fatalf("statusText 不应参与匹配: %+v", g)
		}
	}
}

// TestDockerSearchHintsAndAttribution 逐类型断言条目形状：下钻标识 id、归属三列
// （hostId/hostname/group）与每类各自的 hint（容器=镜像名、镜像=镜像 ID、
// 卷/网络=驱动、项目=服务数）。
//
// id 的取值口径以**既有详情页路由能吃下的参数**为准（前端下钻靠它）：容器=容器 ID
// （`/docker/containers/:id`）、镜像=镜像 ID/摘要（`/docker/image-detail/:id`）、
// 卷/网络=名称（列表页吃 `?keyword=`）、项目=项目名（`/docker/projects/:name`）。
func TestDockerSearchHintsAndAttribution(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{
		Containers: []agentproto.DockerContainer{{ID: "c1", Name: "app", Image: "app:1.0", State: "running"}},
		Images:     []agentproto.DockerImage{{ID: "sha256:abc", RepoTags: []string{"app:1.0"}}},
		Volumes:    []agentproto.DockerVolume{{Name: "app-vol", Driver: "local"}},
		Networks:   []agentproto.DockerNetwork{{Name: "app-net", Driver: "bridge"}},
		Projects:   []agentproto.DockerProject{{Name: "app-stack", Services: 3}},
	})
	resp, err := f.svc.Search(f.ctx, "app", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 主机 7 的平台级分组 = prod（见 newDockerOverviewSvc 夹具）——归属三列带出。
	check := func(kind, wantName, wantID, wantHint string) {
		t.Helper()
		g := searchGroup(t, resp, kind)
		if g.Total != 1 || len(g.Items) != 1 {
			t.Fatalf("%s 组应恰命中 1 条: %+v", kind, g)
		}
		it := g.Items[0]
		if it.Name != wantName || it.Hint != wantHint {
			t.Fatalf("%s 条目 name/hint 不符: got %q/%q want %q/%q", kind, it.Name, it.Hint, wantName, wantHint)
		}
		if it.ID != wantID {
			t.Fatalf("%s 条目 id 不符（下钻标识）: got %q want %q", kind, it.ID, wantID)
		}
		if it.HostID != 7 || it.Hostname != "uni-105" || it.Group != "prod" {
			t.Fatalf("%s 条目归属三列不符: %+v", kind, it)
		}
	}
	check("containers", "app", "c1", "app:1.0")            // id = 容器 ID, hint = 镜像名
	check("images", "app:1.0", "sha256:abc", "sha256:abc") // id = hint = 镜像 ID, name = 首个 tag
	check("volumes", "app-vol", "app-vol", "local")        // id = 卷名, hint = 驱动
	check("networks", "app-net", "app-net", "bridge")      // id = 网络名, hint = 驱动
	check("projects", "app-stack", "app-stack", "3")       // id = 项目名, hint = 服务数
}

// TestDockerSearchGroupLimitAndTruncated 每组限量与 truncated：
//   - 缺省每组 20；limit 显式生效；limit 超上限被夹到 100（不报错）；
//   - total 报截断前全量；truncated = total > 生效限量；
//   - 限量只砍该组（其余空组不受影响，truncated=false）。
func TestDockerSearchGroupLimitAndTruncated(t *testing.T) {
	f := newResFixture(t)
	big := make([]agentproto.DockerContainer, 0, 150)
	for i := 0; i < 150; i++ {
		big = append(big, agentproto.DockerContainer{
			ID: fmt.Sprintf("c%03d", i), Name: fmt.Sprintf("cap-%03d", i), Image: "img:1", State: "running",
		})
	}
	f.saveState(t, 7, &agentproto.DockerState{Containers: big})

	cases := []struct {
		name      string
		limit     int
		wantItems int
		wantTrunc bool
	}{
		{"缺省/limit=0=20", 0, 20, true},
		{"limit=5", 5, 5, true},
		{"limit=100", 100, 100, true},
		{"limit=999 夹到 100", 999, 100, true},
		{"limit=-3 回落缺省", -3, 20, true},
	}
	for _, tc := range cases {
		resp, err := f.svc.Search(f.ctx, "cap", tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		g := searchGroup(t, resp, "containers")
		if g.Total != 150 {
			t.Fatalf("%s: total 必须是截断前全量 = 150, got %d", tc.name, g.Total)
		}
		if len(g.Items) != tc.wantItems || g.Truncated != tc.wantTrunc {
			t.Fatalf("%s: items=%d truncated=%v, want %d/%v", tc.name, len(g.Items), g.Truncated, tc.wantItems, tc.wantTrunc)
		}
		// 别的组不受该组限量的影响：空组 total=0/truncated=false。
		if eg := searchGroup(t, resp, "images"); eg.Total != 0 || eg.Truncated || len(eg.Items) != 0 {
			t.Fatalf("%s: 空组不该被别的组的限量波及: %+v", tc.name, eg)
		}
	}

	// 命中数恰好等于生效限量 → 不截断（子串 cap-0 命中 100 条，limit=100）。
	resp, err := f.svc.Search(f.ctx, "cap-0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if g := searchGroup(t, resp, "containers"); g.Truncated || g.Total != 100 || len(g.Items) != 100 {
		t.Fatalf("命中数 == 限量时应不截断: total=%d items=%d truncated=%v", g.Total, len(g.Items), g.Truncated)
	}

	// limit 超上限被夹到 100：cap 命中 150 条、limit=200 → 夹到 100，仍截断。
	resp, err = f.svc.Search(f.ctx, "cap", 200)
	if err != nil {
		t.Fatal(err)
	}
	if g := searchGroup(t, resp, "containers"); !g.Truncated || len(g.Items) != 100 {
		t.Fatalf("limit 超上限应夹到 100 且仍截断: items=%d truncated=%v", len(g.Items), g.Truncated)
	}
}

// TestDockerSearchUnavailableHosts 不可用主机口径：
//   - 读失败 / 从未上报的主机**不进任何组**（不编造「零条目」假象）；
//   - 它们进 unavailableHosts，且带与其余读面同源的结论句（两句不同的话）。
func TestDockerSearchUnavailableHosts(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Containers: []agentproto.DockerContainer{{ID: "c7", Name: "match-7", Image: "x", State: "running"}}})
	f.saveState(t, 8, &agentproto.DockerState{Containers: []agentproto.DockerContainer{{ID: "c8", Name: "match-8", Image: "x", State: "running"}}})
	// 主机 7：坏 JSON（读失败）。主机 8：删快照键（从未上报）。
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.rdb.Del(f.ctx, dockerstate.StateKey(8)).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Search(f.ctx, "match", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range resp.Groups {
		if g.Total != 0 || len(g.Items) != 0 {
			t.Fatalf("不可用主机的命中不得进组: %+v", g)
		}
	}
	if len(resp.UnavailableHosts) != 2 {
		t.Fatalf("两台不可用主机都应列出: %+v", resp.UnavailableHosts)
	}
	reasons := map[uint64]string{}
	for _, u := range resp.UnavailableHosts {
		reasons[u.HostID] = u.Reason
	}
	if reasons[7] != dockerStateReadFailed {
		t.Fatalf("读失败主机结论句不符: %q", reasons[7])
	}
	if reasons[8] != dockerStateNeverReported {
		t.Fatalf("从未上报主机结论句不符: %q", reasons[8])
	}
}

// TestDockerSearchEmptyQuery 空 q 的裁决：返回 5 个空组（200、不报错、不枚举）——
// 输入过程中的空查询是常态，报 400 会把检索框打成一片红。
func TestDockerSearchEmptyQuery(t *testing.T) {
	f := newResFixture(t)
	// 底下有能命中的数据：空 q 仍必须返回空结果（证明它短路、没做检索）。
	f.saveState(t, 7, &agentproto.DockerState{Containers: []agentproto.DockerContainer{{ID: "c1", Name: "anything", Image: "x", State: "running"}}})

	for _, q := range []string{"", "   ", "\t\n"} {
		resp, err := f.svc.Search(f.ctx, q, 0)
		if err != nil {
			t.Fatalf("空 q 不得报错（q=%q）: %v", q, err)
		}
		// groups 固定 5 组、顺序固定。
		if len(resp.Groups) != len(searchKinds) {
			t.Fatalf("空 q 应返回固定 5 组: %+v", resp.Groups)
		}
		for i, kind := range searchKinds {
			g := resp.Groups[i]
			if g.Kind != kind {
				t.Fatalf("groups 顺序必须固定: 第 %d 组 = %q, want %q", i, g.Kind, kind)
			}
			if g.Total != 0 || g.Truncated || g.Items == nil || len(g.Items) != 0 {
				t.Fatalf("空 q 每组应为空数组（total=0/truncated=false）: %+v", g)
			}
		}
		if resp.UnavailableHosts == nil || len(resp.UnavailableHosts) != 0 {
			t.Fatalf("空 q 的 unavailableHosts 应为空数组而非 null: %+v", resp.UnavailableHosts)
		}
	}
}

// TestDockerSearchNoMatch 非空关键词未命中：5 组皆空（空数组而非 null）、无不可用主机。
func TestDockerSearchNoMatch(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Containers: []agentproto.DockerContainer{{ID: "c1", Name: "web", Image: "x", State: "running"}}})
	resp, err := f.svc.Search(f.ctx, "no-such-thing", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range resp.Groups {
		if g.Total != 0 || g.Items == nil || len(g.Items) != 0 {
			t.Fatalf("未命中每组都应为空数组: %+v", g)
		}
	}
	if len(resp.UnavailableHosts) != 0 {
		t.Fatalf("主机都可用时不应有不可用清单: %+v", resp.UnavailableHosts)
	}
}
