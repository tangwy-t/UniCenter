package service

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 镜像版本漂移视图（GET /docker/image-drift）的测试。
//
// 夹具沿用 docker_resources_test.go 的 resFixture（miniredis + 确定「现在」+ 四台
// 主机 3/7/8/9 已登记进设备表，其中主机 7 预置分组 "prod"，见 newDockerOverviewSvc）：
// 本视图与四资源页同源（hostRecords 枚举 + 同一次快照读），测试也共用同一套夹具。

// driftImage 造一个带指定内容摘要与标签的镜像条目。
func driftImage(digest string, tags ...string) agentproto.DockerImage {
	return agentproto.DockerImage{ID: digest, RepoTags: tags, SizeMB: 100}
}

// mustDrift 跑一次 ImageDrift 并断言无错。
func mustDrift(t *testing.T, f *resFixture, q *request.DockerImageDriftQuery) *response.DockerImageDriftResp {
	t.Helper()
	resp, err := f.svc.ImageDrift(f.ctx, q)
	if err != nil {
		t.Fatalf("ImageDrift: %v", err)
	}
	return resp
}

// driftRow 从漂移行里按 repoTag 取一行（默认排序下位置不固定，按 tag 定位）。
func driftRow(t *testing.T, resp *response.DockerImageDriftResp, tag string) response.DockerImageDriftItem {
	t.Helper()
	for _, it := range resp.Items {
		if it.RepoTag == tag {
			return it
		}
	}
	t.Fatalf("漂移行里没有 tag=%q: %+v", tag, resp.Items)
	return response.DockerImageDriftItem{}
}

// TestDockerImageDriftAggregatesDistribution 多主机同 tag 不同摘要 → 聚合出正确的
// distinctDigests / 多数派 / 少数派主机清单（少数派主机按 hostId 升序）。
//
// 两个 tag 各验一种形态：app:1 有清晰多数派；web:1 是 2:2 并列（钉住并列时
// 「多数派取摘要字典序最小者」的确定性锚）。
func TestDockerImageDriftAggregatesDistribution(t *testing.T) {
	f := newResFixture(t)
	// 主机 3/7/8/9 各带 app:1 与 web:1 两个标签，摘要分布刻意不同。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:aaa", "app:1"), driftImage("sha256:bbb", "web:1"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:aaa", "app:1"), driftImage("sha256:aaa", "web:1"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:aaa", "app:1"), driftImage("sha256:bbb", "web:1"),
	}})
	f.saveState(t, 9, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:bbb", "app:1"), driftImage("sha256:aaa", "web:1"),
	}})

	resp := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("应恰有两行（app:1 / web:1）: total=%d %+v", resp.Total, resp.Items)
	}

	// app:1：aaa 在 3/7/8（3 台）= 多数派；bbb 在 9（1 台）= 少数派。
	app := driftRow(t, resp, "app:1")
	if app.HostCount != 4 || app.DistinctDigests != 2 {
		t.Fatalf("app:1 分布不符: %+v", app)
	}
	if app.DominantDigest != "sha256:aaa" || app.DominantHostCount != 3 {
		t.Fatalf("app:1 多数派不符（应为 aaa×3）: %+v", app)
	}
	if len(app.Outliers) != 1 || app.Outliers[0].Digest != "sha256:bbb" || app.Outliers[0].HostCount != 1 {
		t.Fatalf("app:1 少数派不符: %+v", app.Outliers)
	}
	if len(app.Outliers[0].Hosts) != 1 || app.Outliers[0].Hosts[0].HostID != 9 ||
		app.Outliers[0].Hosts[0].Hostname != "uni-109" {
		t.Fatalf("app:1 少数派主机清单不符: %+v", app.Outliers[0].Hosts)
	}

	// web:1：aaa 在 7/9、bbb 在 3/8 —— 2:2 并列，多数派取字典序最小者 aaa；
	// 少数派 bbb 的主机清单必须按 hostId 升序（[3,8]）。
	web := driftRow(t, resp, "web:1")
	if web.HostCount != 4 || web.DistinctDigests != 2 {
		t.Fatalf("web:1 分布不符: %+v", web)
	}
	if web.DominantDigest != "sha256:aaa" || web.DominantHostCount != 2 {
		t.Fatalf("web:1 并列时多数派须取字典序最小摘要: %+v", web)
	}
	if len(web.Outliers) != 1 || web.Outliers[0].Digest != "sha256:bbb" || web.Outliers[0].HostCount != 2 {
		t.Fatalf("web:1 少数派不符: %+v", web.Outliers)
	}
	hosts := web.Outliers[0].Hosts
	if len(hosts) != 2 || hosts[0].HostID != 3 || hosts[1].HostID != 8 {
		t.Fatalf("少数派主机清单必须按 hostId 升序: %+v", hosts)
	}

	// 默认排序：distinctDigests 降序（两行同值）→ 同值按 repoTag 升序。
	if resp.Items[0].RepoTag != "app:1" || resp.Items[1].RepoTag != "web:1" {
		t.Fatalf("默认排序应为 repoTag 升序兜底: %+v", resp.Items)
	}
}

// TestDockerImageDriftMultiTagImage 一个镜像带多个 tag → 每个 tag 各自成一行
//（同一个摘要出现在它挂的每一个 tag 行里），且一致标签无少数派（空数组非 null）。
func TestDockerImageDriftMultiTagImage(t *testing.T) {
	f := newResFixture(t)
	// 同一个镜像（sha256:x）挂两个标签 alpha:1 / beta:2。
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:x", "alpha:1", "beta:2"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:x", "alpha:1"),
	}})

	resp := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("一个多标签镜像应展平成两行: total=%d %+v", resp.Total, resp.Items)
	}
	alpha := driftRow(t, resp, "alpha:1")
	if alpha.HostCount != 2 || alpha.DistinctDigests != 1 || alpha.DominantDigest != "sha256:x" ||
		alpha.DominantHostCount != 2 {
		t.Fatalf("alpha:1 不符（7/8 同摘要）: %+v", alpha)
	}
	beta := driftRow(t, resp, "beta:2")
	if beta.HostCount != 1 || beta.DistinctDigests != 1 || beta.DominantHostCount != 1 {
		t.Fatalf("beta:2 不符（只有 7 有）: %+v", beta)
	}
	// 一致标签的少数派必须是空数组而非 null（本模块既有纪律）。
	if alpha.Outliers == nil || beta.Outliers == nil {
		t.Fatalf("few 一致标签的 outliers 必须是空数组而非 null: alpha=%v beta=%v", alpha.Outliers, beta.Outliers)
	}
	if len(alpha.Outliers) != 0 || len(beta.Outliers) != 0 {
		t.Fatalf("一致标签不应有少数派: %+v", resp.Items)
	}
}

// TestDockerImageDriftGroupScopeAndUnavailable 钉住统计范围口径（本波的核心裁决）：
//   - group 收窄**主机范围**：组外主机既不聚合，也**不进** unavailableHosts
//     （它是「没在看」，不是「不可用」）；
//   - 主机范围内读失败 / 从未上报的主机 → 进 unavailableHosts，且**不计入**任何
//     tag 的 hostCount（不编造「零镜像」的假象）；
//   - keyword 收窄标签行，与主机范围无关（不把任何主机移出/移入 unavailableHosts）。
func TestDockerImageDriftGroupScopeAndUnavailable(t *testing.T) {
	f := newResFixture(t)
	// 主机 7（组 prod）：app:1 = a7。
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a7", "app:1"),
	}})
	// 主机 8（无组）：app:1 = a8（与 7 不同摘要）。
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a8", "app:1"),
	}})
	// 主机 3（无组）：app:1 = a7（与 7 同摘要）。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a7", "app:1"),
	}})
	// 主机 9（无组）：先登记进 docker:hosts 集合，再覆盖成坏 JSON → 读失败（而非「从未上报」）。
	f.saveState(t, 9, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a9", "app:1"),
	}})
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(9), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	// group=prod：范围只有主机 7。组外主机（8/3 可用、9 读失败）一律不在范围内 ——
	// 既不聚合，也不进 unavailableHosts。
	only := mustDrift(t, f, &request.DockerImageDriftQuery{Group: "prod"})
	if only.Total != 1 || len(only.Items) != 1 {
		t.Fatalf("group=prod 只应统计主机 7: %+v", only)
	}
	if r := only.Items[0]; r.HostCount != 1 || r.DistinctDigests != 1 || r.DominantDigest != "sha256:a7" {
		t.Fatalf("group=prod 应只含主机 7 的 a7: %+v", r)
	}
	if len(only.UnavailableHosts) != 0 {
		t.Fatalf("组外主机（含读失败者）不得进 unavailableHosts: %+v", only.UnavailableHosts)
	}

	// group=""（全范围）：读失败的 9 进不可用清单，且**不计入** hostCount（=3 不是 4）。
	all := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if all.Total != 1 || len(all.Items) != 1 {
		t.Fatalf("全范围应有一行 app:1: %+v", all)
	}
	r := all.Items[0]
	if r.HostCount != 3 || r.DistinctDigests != 2 || r.DominantDigest != "sha256:a7" || r.DominantHostCount != 2 {
		t.Fatalf("读失败主机不得计入 hostCount（应为 7/3/8 三台、a7×2）: %+v", r)
	}
	if len(r.Outliers) != 1 || r.Outliers[0].Digest != "sha256:a8" ||
		len(r.Outliers[0].Hosts) != 1 || r.Outliers[0].Hosts[0].HostID != 8 {
		t.Fatalf("少数派 a8 只在主机 8: %+v", r.Outliers)
	}
	if len(all.UnavailableHosts) != 1 || all.UnavailableHosts[0].HostID != 9 ||
		all.UnavailableHosts[0].Reason != dockerStateReadFailed {
		t.Fatalf("读失败主机必须进 unavailableHosts 并带结论句: %+v", all.UnavailableHosts)
	}

	// keyword 收窄标签行、与主机范围无关：不匹配的关键字 → 空表，但读失败主机
	// 仍在 unavailableHosts（keyword 不会把它移出范围）。
	kw := mustDrift(t, f, &request.DockerImageDriftQuery{Keyword: "nomatch"})
	if kw.Total != 0 || len(kw.Items) != 0 {
		t.Fatalf("不匹配的 keyword 应回空表: %+v", kw)
	}
	if len(kw.UnavailableHosts) != 1 || kw.UnavailableHosts[0].HostID != 9 {
		t.Fatalf("keyword 不得把读失败主机移出 unavailableHosts: %+v", kw.UnavailableHosts)
	}

	// 组内读失败：主机 7（prod）读失败、主机 8（无组）可用 → group=prod 时 7 进
	// 不可用清单、8 被组过滤排除、无任何行。
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{bad", 0).Err(); err != nil {
		t.Fatal(err)
	}
	inGroup := mustDrift(t, f, &request.DockerImageDriftQuery{Group: "prod"})
	if inGroup.Total != 0 || len(inGroup.Items) != 0 {
		t.Fatalf("组内唯一主机读失败时不应有行: %+v", inGroup)
	}
	if len(inGroup.UnavailableHosts) != 1 || inGroup.UnavailableHosts[0].HostID != 7 ||
		inGroup.UnavailableHosts[0].Reason != dockerStateReadFailed {
		t.Fatalf("组内读失败主机必须进不可用清单: %+v", inGroup.UnavailableHosts)
	}
}

// TestDockerImageDriftSortWhitelistAndFallback 默认排序（distinctDigests 降序、
// repoTag 升序作锚）+ 白名单键 + 非法键静默回落（不 400）。
func TestDockerImageDriftSortWhitelistAndFallback(t *testing.T) {
	f := newResFixture(t)
	// 四个标签：aaa 只 3 有；bbb/ccc 在 3/7/8；ddd 在 3/9 且**摘要不同**（distinct=2）。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:1", "aaa:1"), driftImage("sha256:1", "bbb:1"),
		driftImage("sha256:1", "ccc:1"), driftImage("sha256:1", "ddd:1"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:1", "bbb:1"), driftImage("sha256:1", "ccc:1"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:1", "bbb:1"), driftImage("sha256:1", "ccc:1"),
	}})
	f.saveState(t, 9, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:9", "ddd:1"), // ddd:1 的第二个摘要 → ddd:1 distinctDigests=2
	}})

	// 默认排序：distinctDigests 降序 → ddd:1(2) 在前，其余 distinct=1 按 repoTag 升序。
	def := mustDrift(t, f, &request.DockerImageDriftQuery{})
	gotTags := make([]string, len(def.Items))
	for i, it := range def.Items {
		gotTags[i] = it.RepoTag
	}
	want := []string{"ddd:1", "aaa:1", "bbb:1", "ccc:1"}
	for i := range want {
		if gotTags[i] != want[i] {
			t.Fatalf("默认排序 = %v, want %v", gotTags, want)
		}
	}

	// 白名单 sortBy=hostCount asc：aaa(1) ddd(2) bbb(3) ccc(3) —— 同值 repoTag 升序。
	hc := mustDrift(t, f, &request.DockerImageDriftQuery{SortBy: "hostCount", SortOrder: "asc"})
	gotHC := []string{}
	for _, it := range hc.Items {
		gotHC = append(gotHC, it.RepoTag)
	}
	wantHC := []string{"aaa:1", "ddd:1", "bbb:1", "ccc:1"}
	for i := range wantHC {
		if gotHC[i] != wantHC[i] {
			t.Fatalf("hostCount asc = %v, want %v", gotHC, wantHC)
		}
	}

	// 白名单 sortBy=repoTag desc。
	rd := mustDrift(t, f, &request.DockerImageDriftQuery{SortBy: "repoTag", SortOrder: "desc"})
	if rd.Items[0].RepoTag != "ddd:1" || rd.Items[len(rd.Items)-1].RepoTag != "aaa:1" {
		t.Fatalf("repoTag desc 首尾不符: %+v", rd.Items)
	}

	// 非法键静默回落默认（不 400）。
	bad := mustDrift(t, f, &request.DockerImageDriftQuery{SortBy: "evil; drop", SortOrder: "asc"})
	if len(bad.Items) != len(def.Items) || bad.Items[0].RepoTag != def.Items[0].RepoTag {
		t.Fatalf("非法 sortBy 必须静默回落默认次序: %+v", bad.Items)
	}
}

// TestDockerImageDriftPagination 分页：Total 是行数（分页前全量）、pageSize 缺省 50 /
// 上限 100（dockerPageSlice 同口径）、越界页空 items 但 Total 不变。
func TestDockerImageDriftPagination(t *testing.T) {
	f := newResFixture(t)
	// 三个标签，distinctDigests 分别 3/2/1（默认排序确定：p:1, q:1, r:1）。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("d1", "p:1"), driftImage("d1", "q:1"), driftImage("d1", "r:1"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("d2", "p:1"), driftImage("d2", "q:1"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("d3", "p:1"),
	}})

	// pageSize=2 第 1 页：2 条，Total 3。
	p1 := mustDrift(t, f, &request.DockerImageDriftQuery{PageRequest: app.PageRequest{Page: 1, PageSize: 2}})
	if p1.Total != 3 || p1.Page != 1 || p1.PageSize != 2 || len(p1.Items) != 2 {
		t.Fatalf("第 1 页不符: %+v", p1)
	}
	if p1.Items[0].RepoTag != "p:1" || p1.Items[1].RepoTag != "q:1" {
		t.Fatalf("第 1 页排序不符: %+v", p1.Items)
	}
	// 第 2 页：1 条。
	p2 := mustDrift(t, f, &request.DockerImageDriftQuery{PageRequest: app.PageRequest{Page: 2, PageSize: 2}})
	if p2.Total != 3 || len(p2.Items) != 1 || p2.Items[0].RepoTag != "r:1" {
		t.Fatalf("第 2 页不符: %+v", p2)
	}
	// pageSize 缺省 → 50（不是 app.PageRequest 的 10）。
	def := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if def.PageSize != dockerDefaultPageSize {
		t.Fatalf("缺省 pageSize 应为 %d: %d", dockerDefaultPageSize, def.PageSize)
	}
	// pageSize 超上限 → 夹到 100（服务端直调路径）。
	capd := mustDrift(t, f, &request.DockerImageDriftQuery{PageRequest: app.PageRequest{PageSize: 1000}})
	if capd.PageSize != dockerPageSizeMax || len(capd.Items) != 3 {
		t.Fatalf("pageSize 超上限应夹到 %d: %+v", dockerPageSizeMax, capd)
	}
	// 越界页：空 items 而非错误，Total 不变。
	over := mustDrift(t, f, &request.DockerImageDriftQuery{PageRequest: app.PageRequest{Page: 9, PageSize: 10}})
	if over.Total != 3 || len(over.Items) != 0 || over.Items == nil {
		t.Fatalf("越界页应回空 items 且 Total 不变: %+v", over)
	}
}

// TestDockerImageDriftEmptyFleetNonNil 零行时不返回 null：items / outliers /
// unavailableHosts 一律空数组（本模块既有纪律）。
func TestDockerImageDriftEmptyFleetNonNil(t *testing.T) {
	f := newResFixture(t)
	// 没有主机登记即可管（无快照）→ 零行。
	empty := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if empty.Items == nil || empty.UnavailableHosts == nil {
		t.Fatalf("零行时 items/unavailableHosts 必须是空数组而非 null: %+v", empty)
	}
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("零行盘点: %+v", empty)
	}

	// 有主机、但快照里没有镜像 → 同样零行（不是「一台空主机一行」）。
	f.saveState(t, 7, &agentproto.DockerState{})
	noImg := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if noImg.Items == nil || noImg.Total != 0 || len(noImg.Items) != 0 {
		t.Fatalf("无镜像时零行且非 null: %+v", noImg)
	}
}