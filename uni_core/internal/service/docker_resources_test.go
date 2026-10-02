package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 跨主机资源清单（9a：Images/Volumes/Networks/Projects）的测试。
//
// 夹具与断言风格沿用 docker_workloads_test.go：这四个端点与容器统一表共用
// hostRecords 枚举，且四页共用 aggregateResourceRows 的排序/截断/跳过骨架，
// 测试也共用同一套「确定现在」。每页固定六类用例：全量聚合逐字段、主机/关键字
// 过滤、资源专属开关、上限截断、读失败跳过、排序稳定（跨主机同名不交织）。

// resFixture 是四页资源清单测试的公共夹具：miniredis + 确定「现在」的读面
// （四台主机 3/7/8/9 已登记进设备表，见 newDockerOverviewSvc）。
type resFixture struct {
	svc   *DockerService
	store *dockerstate.Store
	rdb   *goredis.Client
	ctx   context.Context
	now   time.Time
}

func newResFixture(t *testing.T) *resFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	now := time.Now()
	return &resFixture{
		svc:   newDockerOverviewSvc(t, store, now),
		store: store,
		rdb:   rdb,
		ctx:   context.Background(),
		now:   now,
	}
}

// saveState 保存一台主机的快照：T 与 DockerOK 由夹具统一补齐（测试关心的是
// 资源清单本身，不是这两枚样板字段）。
func (f *resFixture) saveState(t *testing.T, id uint64, st *agentproto.DockerState) {
	t.Helper()
	st.T = f.now.UnixMilli()
	st.DockerOK = true
	if err := f.store.Save(f.ctx, id, st, f.now); err != nil {
		t.Fatal(err)
	}
}

// 四类资源条目的紧凑构造（对齐 overviewContainer 的角色）。
func resImage(id, tag string, inUse, dangling bool) agentproto.DockerImage {
	i := agentproto.DockerImage{ID: id, SizeMB: 100, InUse: inUse, Dangling: dangling}
	if tag != "" {
		i.RepoTags = []string{tag}
	}
	return i
}

func resVolume(name string, inUse bool) agentproto.DockerVolume {
	return agentproto.DockerVolume{Name: name, Driver: "local", InUse: inUse}
}

func resNetwork(name string, internal bool) agentproto.DockerNetwork {
	return agentproto.DockerNetwork{Name: name, Driver: "bridge", Scope: "local", Internal: internal, ContainersCount: 1}
}

func resProject(name, state string) agentproto.DockerProject {
	return agentproto.DockerProject{Name: name, State: state, Services: 1, ContainersCount: 1}
}

// requireEmptyNonNil 钉住「空结果 = 空数组而非 null」的约定（前端少一层判空；
// Go 切片为 nil 时 JSON 序列化成 null，这条断言就是防线的第一半）。
func requireEmptyNonNil[T any](t *testing.T, items []T, total int, label string) {
	t.Helper()
	if total != 0 || len(items) != 0 {
		t.Fatalf("%s 应为空表: total=%d items=%d", label, total, len(items))
	}
	if items == nil {
		t.Fatalf("%s 必须是空数组而非 null（前端少一层判空）", label)
	}
}

// ── 镜像 ────────────────────────────────────────────────────────────────

// TestDockerImagesAggregatesAllHosts 无过滤时的全量聚合：全部可管主机的镜像
// 并成一张表，按主机 id 升序（同机按排序名 —— 第一个 repoTag，悬空回落 ID）
// 排序，且**逐字段**断言条目映射（含归属两列）。
func TestDockerImagesAggregatesAllHosts(t *testing.T) {
	f := newResFixture(t)

	// 主机 8 先写、主机 7 后写（故意与 id 升序相反）；主机 7 的三张也按非排序名
	// 序写入 —— 若排序依赖枚举/写入顺序，7 会排在 8 后、zeta 会排在 alpha 前。
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-solo", "solo:latest", true, false),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-zeta", "zeta:9", false, false),
		{
			// alpha 用全字段构造：统一表条目 = 单主机条目 + 归属两列，
			// 漏一个字段就是页面上少一列。
			ID: "sha256:alpha", RepoTags: []string{"alpha:1.0", "alpha:latest"},
			SizeMB: 545.5, Created: 1789000000, InUse: true, Dangling: false,
			InUseBy: []string{"core", "console"},
		},
		// 悬空无标签镜像：排序名回落 ID（"sha256:none" 恰在 alpha 与 zeta 之间）。
		resImage("sha256:none", "", false, true),
	}})

	// nil 查询与空查询同形（全量、无过滤）—— 钉住 service 对 nil 的防御语义。
	resp, err := f.svc.Images(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 4 || len(resp.Items) != 4 {
		t.Fatalf("统一表应收全量 4 条: total=%d items=%d", resp.Total, len(resp.Items))
	}
	wantOrder := []struct {
		host uint64
		id   string
	}{{7, "sha256:alpha"}, {7, "sha256:none"}, {7, "img-zeta"}, {8, "img-solo"}}
	for i, want := range wantOrder {
		got := resp.Items[i]
		if got.HostID != want.host || got.ID != want.id {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（须按主机 id 升序、同机按排序名）",
				i, got.HostID, got.ID, want.host, want.id)
		}
	}

	// 归属两列 + 内嵌镜像条目逐字段（alpha 是全字段样本）。
	a := resp.Items[0]
	if a.HostID != 7 || a.Hostname != "uni-105" {
		t.Fatalf("归属两列未映射: hostId=%d hostname=%s", a.HostID, a.Hostname)
	}
	if a.ID != "sha256:alpha" || len(a.RepoTags) != 2 ||
		a.RepoTags[0] != "alpha:1.0" || a.RepoTags[1] != "alpha:latest" {
		t.Fatalf("镜像身份/标签未逐字段映射: %+v", a.DockerImageItem)
	}
	if a.SizeMB != 545.5 || a.Created != 1789000000 || !a.InUse || a.Dangling {
		t.Fatalf("镜像尺寸/时刻/结论未逐字段映射: %+v", a.DockerImageItem)
	}
	if len(a.InUseBy) != 2 || a.InUseBy[0] != "core" || a.InUseBy[1] != "console" {
		t.Fatalf("InUseBy 未带入统一表条目: %+v", a.InUseBy)
	}
	// 悬空条目与 compact 构造钉住同一条换算路径。
	if d := resp.Items[1]; !d.Dangling || d.InUse || len(d.RepoTags) != 0 {
		t.Fatalf("悬空镜像条目未映射: %+v", d.DockerImageItem)
	}
	if z := resp.Items[2]; z.ID != "img-zeta" || z.SizeMB != 100 || z.InUse {
		t.Fatalf("compact 镜像条目未映射: %+v", z.DockerImageItem)
	}
}

// TestDockerImagesHostAndKeywordFilter hostId 限定单主机 + keyword 对 repoTag
// 子串（大小写不敏感，多标签 join(" ") 后匹配）。
func TestDockerImagesHostAndKeywordFilter(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		{ID: "id-gw", RepoTags: []string{"Api-Gateway:latest"}},
		{ID: "id-db", RepoTags: []string{"mysql:8.0.22", "db:latest"}},
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		{ID: "id-web", RepoTags: []string{"API-GATEWAY:v2"}},
	}})

	// 大小写两种敲法是同一个意图：命中集不得改变（标签命中跨两台各一）。
	for _, kw := range []string{"api-gateway", "API-GATEWAY"} {
		resp, err := f.svc.Images(f.ctx, &request.DockerImageQuery{Keyword: kw})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != 2 {
			t.Fatalf("keyword=%q 必须命中两台各一（大小写不敏感）: %+v", kw, resp)
		}
	}
	// 多标签 join(" ") 后匹配：第二枚标签也要能命中（与前端同一句）。
	byTag, err := f.svc.Images(f.ctx, &request.DockerImageQuery{Keyword: "db:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if byTag.Total != 1 || byTag.Items[0].ID != "id-db" {
		t.Fatalf("第二枚标签的子串必须命中: %+v", byTag)
	}
	// hostId 限定单主机：只收那台的镜像，归属两列逐行一致。
	only7, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if only7.Total != 2 || len(only7.Items) != 2 {
		t.Fatalf("hostId=7 应只收主机 7 的 2 张镜像: %+v", only7)
	}
	for _, it := range only7.Items {
		if it.HostID != 7 || it.Hostname != "uni-105" {
			t.Fatalf("过滤后不得混入别的主机的镜像: %+v", it)
		}
	}
	// 组合过滤逐层收窄：hostId + keyword。
	combo, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 7, Keyword: "MYSQL"})
	if err != nil {
		t.Fatal(err)
	}
	if combo.Total != 1 || combo.Items[0].ID != "id-db" {
		t.Fatalf("hostId+keyword 组合过滤失效: %+v", combo)
	}
	// 未命中 → 空表（不是错误）；从未上报的主机 → 空表（不是 404）。
	none, err := f.svc.Images(f.ctx, &request.DockerImageQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, none.Items, none.Total, "未命中")
	empty, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 9})
	if err != nil {
		t.Fatalf("指向未上报主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, empty.Items, empty.Total, "未上报主机")
}

// TestDockerImagesDanglingUnusedFilters dangling/unused 的三值语义（nil=不过滤、
// true=取该侧、false=取其反侧，与设备列表 Online 过滤同一句）：四张镜像构成
// 2×2 正交矩阵，逐格断言。
func TestDockerImagesDanglingUnusedFilters(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("both", "both:1", false, true), // 悬空且未用
		resImage("dang", "dang:1", true, true),  // 悬空但在用（罕见但合法）
		resImage("unused", "unused:1", false, false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("live", "live:1", true, false), // 在用且非悬空
	}})

	// 三值语义的每一侧都要可表达：nil 不过滤、true 取该侧、false 取其反侧。
	cases := []struct {
		name    string
		q       request.DockerImageQuery
		wantIDs []string
	}{
		{"dangling=true", request.DockerImageQuery{Dangling: boolPtr(true)}, []string{"both", "dang"}},
		{"dangling=false", request.DockerImageQuery{Dangling: boolPtr(false)}, []string{"live", "unused"}},
		{"unused=true", request.DockerImageQuery{Unused: boolPtr(true)}, []string{"both", "unused"}},
		{"unused=false", request.DockerImageQuery{Unused: boolPtr(false)}, []string{"dang", "live"}},
		{"dangling=true+unused=true", request.DockerImageQuery{Dangling: boolPtr(true), Unused: boolPtr(true)}, []string{"both"}},
		{"dangling=true+hostId=8", request.DockerImageQuery{HostID: 8, Dangling: boolPtr(true)}, []string{}},
	}
	for _, tc := range cases {
		resp, err := f.svc.Images(f.ctx, &tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != len(tc.wantIDs) || len(resp.Items) != len(tc.wantIDs) {
			t.Fatalf("%s 应命中 %d 张: %+v", tc.name, len(tc.wantIDs), resp)
		}
		got := map[string]bool{}
		for _, it := range resp.Items {
			got[it.ID] = true
		}
		for _, id := range tc.wantIDs {
			if !got[id] {
				t.Fatalf("%s 丢了 %s: %+v", tc.name, id, resp)
			}
		}
	}
}

// TestDockerImagesCap 上限 500 条且**截断先报全量**：Total 是截断前的真数，
// Items 只留排序后的前 500（砍掉的是尾部，不是运气）。
func TestDockerImagesCap(t *testing.T) {
	f := newResFixture(t)
	big := make([]agentproto.DockerImage, 0, 600)
	for i := 0; i < 600; i++ {
		big = append(big, resImage(fmt.Sprintf("img-%03d", i), fmt.Sprintf("cap-%03d:latest", i), false, false))
	}
	// 主机 7 带 df：账目在截断**之前**按整机收，600 条截到 500 也影响不到它。
	f.saveState(t, 7, &agentproto.DockerState{
		Images:    big,
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 600, ImagesDanglingMB: 3},
	})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-ok", "zzz:latest", true, false),
	}})

	resp, err := f.svc.Images(f.ctx, &request.DockerImageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是截断前全量 = 601, got %d", resp.Total)
	}
	if len(resp.Items) != 500 {
		t.Fatalf("Items 必须截到 500 条, got %d", len(resp.Items))
	}
	// 截断必须发生在排序之后：留下的是 cap-000..cap-499（主机 7 的前 500）。
	if resp.Items[0].RepoTags[0] != "cap-000:latest" || resp.Items[499].RepoTags[0] != "cap-499:latest" {
		t.Fatalf("截断必须砍排序后的尾部: 首=%v 尾=%v", resp.Items[0].RepoTags, resp.Items[499].RepoTags)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("前 500 行必须都来自主机 7（8 排在其后）: 第 %d 行 hostId=%d", i, it.HostID)
		}
	}
	// 500 条截断不得影响账目：有 df 的主机照常整机收（主机 8 无 df → 缺席）。
	if len(resp.Disk) != 1 || resp.Disk[0].HostID != 7 ||
		resp.Disk[0].DanglingCount != 0 || resp.Disk[0].DanglingMB != 3 {
		t.Fatalf("截断与账目是两条口径（账目按整机收）: %+v", resp.Disk)
	}
}

// TestDockerImagesReadFailureSkipsHost 单台快照读失败（键值损坏）不炸整表：
// 记 warn 后跳过该主机，其余主机照常聚合（与 Workloads 同款取舍）。
func TestDockerImagesReadFailureSkipsHost(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-a", "a:1", false, false),
		resImage("img-b", "b:1", false, false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-ok", "ok:1", true, false),
	}})
	// 把主机 7 的快照键覆盖成坏 JSON：store.Get 返回错误而不是「从未上报」。
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Images(f.ctx, &request.DockerImageQuery{})
	if err != nil {
		t.Fatalf("单台读失败不得让整表失败: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].HostID != 8 {
		t.Fatalf("读失败主机必须整台跳过（不编造零条目假象）: %+v", resp)
	}
	// hostId 指向读失败主机：空表而非错误（与「未上报」同一句「没有条目」）。
	only, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, only.Items, only.Total, "读失败主机")
}

// TestDockerImagesSortStable 排序稳定：两台主机有同名的镜像引用（同名镜像在
// 两台机上都有完全现实），排序必须先按 hostId 再按名字 —— 同名不得把两台的
// 行交织在一起，同主机内部再按名字重排。
func TestDockerImagesSortStable(t *testing.T) {
	f := newResFixture(t)
	// 主机 8 先写（枚举顺序不保证）。
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-s8", "shared:latest", false, false),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		resImage("img-z7", "zzz:1", false, false),
		resImage("img-sh7", "shared:latest", false, false),
		resImage("img-a7", "aaa:1", false, false),
	}})

	resp, err := f.svc.Images(f.ctx, &request.DockerImageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		host uint64
		id   string
	}{{7, "img-a7"}, {7, "img-sh7"}, {7, "img-z7"}, {8, "img-s8"}}
	if len(resp.Items) != len(want) {
		t.Fatalf("应恰有 %d 行: %+v", len(want), resp)
	}
	for i, w := range want {
		if resp.Items[i].HostID != w.host || resp.Items[i].ID != w.id {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（同名不得跨主机交织）",
				i, resp.Items[i].HostID, resp.Items[i].ID, w.host, w.id)
		}
	}
}

// TestDockerImagesDiskAccount 可回收账目（disk）的口径（底栏「N 个可回收 · X」
// 的数据面）：
//   - 只收「账目完整」的主机（快照可读且该帧有 df）：没有 df 的主机**缺席**而不是
//     记零（「缺席 = 不知道」，与总览主机行 disk=nil 同一条纪律）；
//   - DanglingMB 照抄 agent 的 df 对账数，不是 Σ 悬空条目的 SizeMB（共享层不算）；
//   - DanglingCount 与总览 DanglingImages 同一判据（含「悬空但在用」的条目）；
//   - hostId 收窄账目范围；keyword/dangling/unused 只过筛行、碰不到账目；
//   - 账目按 hostId 升序（枚举顺序不保证，与行同一排序纪律）。
func TestDockerImagesDiskAccount(t *testing.T) {
	f := newResFixture(t)

	// 主机 8 先写（枚举顺序不保证）：有 df，2 悬空（含 1 张悬空但在用）+ 1 在用。
	f.saveState(t, 8, &agentproto.DockerState{
		Images: []agentproto.DockerImage{
			resImage("img-live8", "live:8", true, false),
			resImage("img-dang8", "", false, true),
			resImage("img-dang-use8", "", true, true), // 悬空但在用（罕见但合法）
		},
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 300, ImagesDanglingMB: 12.5},
	})
	// 主机 7 后写：有 df，1 悬空。df 值故意远小于条目 SizeMB：账目必须照抄 df
	//（悬空独占层），Σ 条目 Size 是「从没存在过的空间」。
	f.saveState(t, 7, &agentproto.DockerState{
		Images: []agentproto.DockerImage{
			resImage("img-a7", "aaa:1", true, false),
			resImage("img-dang7", "", false, true),
		},
		DiskUsage: &agentproto.DockerDiskUsage{ImagesTotalMB: 100, ImagesDanglingMB: 0.002},
	})
	// 主机 9：快照可读但没有 df 数据（旧版 agent 形态）——账目缺席，不是零。
	f.saveState(t, 9, &agentproto.DockerState{
		Images: []agentproto.DockerImage{resImage("img-9", "nine:1", false, true)},
	})

	// 全量：账目恰含有 df 的两台，按 hostId 升序（9 缺席），逐字段断言。
	resp, err := f.svc.Images(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Disk) != 2 || resp.Disk[0].HostID != 7 || resp.Disk[1].HostID != 8 {
		t.Fatalf("账目应恰含有 df 的两台并按 hostId 升序: %+v", resp.Disk)
	}
	if d := resp.Disk[0]; d.DanglingCount != 1 || d.DanglingMB != 0.002 {
		t.Fatalf("主机 7 的账目不符（字节必须照抄 df，不是 Σ 悬空条目 SizeMB）: %+v", d)
	}
	if d := resp.Disk[1]; d.DanglingCount != 2 || d.DanglingMB != 12.5 {
		t.Fatalf("主机 8 的账目不符（悬空计数须含悬空但在用的条目）: %+v", d)
	}

	// hostId 收窄：账目只剩那台；指向无 df 的主机 → 空账目（空数组而非 null）。
	only7, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(only7.Disk) != 1 || only7.Disk[0].HostID != 7 {
		t.Fatalf("hostId=7 的账目应只剩主机 7: %+v", only7.Disk)
	}
	only9, err := f.svc.Images(f.ctx, &request.DockerImageQuery{HostID: 9})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, only9.Disk, 0, "无 df 主机的账目")

	// 过滤只过筛行、碰不到账目：keyword 把行筛空、dangling=false 把悬空行筛掉，
	// 账目照常是整个主机范围的两台（账目 = 这台主机 prune 能回收的批）。
	none, err := f.svc.Images(f.ctx, &request.DockerImageQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, none.Items, none.Total, "未命中行")
	if len(none.Disk) != 2 || none.Disk[1].DanglingCount != 2 {
		t.Fatalf("行被 keyword 筛空不影响账目: %+v", none.Disk)
	}
	dang, err := f.svc.Images(f.ctx, &request.DockerImageQuery{Dangling: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(dang.Disk) != 2 || dang.Disk[1].DanglingCount != 2 {
		t.Fatalf("dangling=false 只过滤行，账目须保持整机口径: %+v", dang.Disk)
	}

	// 单台读取失败：账目同样整台缺席（没有账目可给，不编造零值）。
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	broken, err := f.svc.Images(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken.Disk) != 1 || broken.Disk[0].HostID != 8 {
		t.Fatalf("读失败主机的账目必须缺席（其余照常）: %+v", broken.Disk)
	}
}

// ── 数据卷 ──────────────────────────────────────────────────────────────

// TestDockerVolumesAggregatesAllHosts 全量聚合：排序按卷名、逐字段断言映射。
func TestDockerVolumesAggregatesAllHosts(t *testing.T) {
	f := newResFixture(t)
	size := 512.25
	f.saveState(t, 8, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("backup", false),
	}})
	// 主机 7 的两卷按非字母序写入（pgdata 先、cache 后）。
	f.saveState(t, 7, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		{
			// 全字段样本：尺寸指针、挂载者、保护结论都要逐字段带出来。
			Name: "pgdata", Driver: "local", SizeMB: &size,
			InUse: true, MountedBy: []string{"db"}, Protected: true,
		},
		resVolume("cache", false),
	}})

	resp, err := f.svc.Volumes(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Fatalf("统一表应收全量 3 条: total=%d items=%d", resp.Total, len(resp.Items))
	}
	wantOrder := []struct {
		host uint64
		name string
	}{{7, "cache"}, {7, "pgdata"}, {8, "backup"}}
	for i, want := range wantOrder {
		got := resp.Items[i]
		if got.HostID != want.host || got.Name != want.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（须按主机 id 升序、同机按卷名）",
				i, got.HostID, got.Name, want.host, want.name)
		}
	}
	p := resp.Items[1]
	if p.HostID != 7 || p.Hostname != "uni-105" {
		t.Fatalf("归属两列未映射: hostId=%d hostname=%s", p.HostID, p.Hostname)
	}
	if p.Driver != "local" || p.SizeMB == nil || *p.SizeMB != 512.25 || !p.InUse || !p.Protected {
		t.Fatalf("卷字段未逐字段映射: %+v", p.DockerVolumeItem)
	}
	if len(p.MountedBy) != 1 || p.MountedBy[0] != "db" {
		t.Fatalf("MountedBy 未带入统一表条目: %+v", p.MountedBy)
	}
	// compact 构造（无尺寸）钉住 nil 指针路径：不知道 ≠ 零。
	if c := resp.Items[0]; c.SizeMB != nil || c.Driver != "local" || c.InUse {
		t.Fatalf("无尺寸卷必须保持 SizeMB=nil: %+v", c.DockerVolumeItem)
	}
}

// TestDockerVolumesHostKeywordAndUnusedFilter hostId/keyword/unused 三层过滤。
func TestDockerVolumesHostKeywordAndUnusedFilter(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("Data-Vol", true),
		resVolume("logs", false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("data-shared", false),
	}})

	// keyword 大小写不敏感：两台各命中一枚。
	for _, kw := range []string{"data", "DATA"} {
		resp, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{Keyword: kw})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != 2 {
			t.Fatalf("keyword=%q 必须命中两台各一枚: %+v", kw, resp)
		}
	}
	// unused 三值语义：true 只留未挂载、false 只留挂载中。
	unused, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{Unused: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if unused.Total != 2 {
		t.Fatalf("unused=true 应恰收 2 枚（logs/data-shared）: %+v", unused)
	}
	for _, it := range unused.Items {
		if it.InUse {
			t.Fatalf("unused=true 混进了挂载中的卷: %+v", it)
		}
	}
	inUse, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{Unused: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if inUse.Total != 1 || inUse.Items[0].Name != "Data-Vol" {
		t.Fatalf("unused=false 应恰收挂载中的 1 枚: %+v", inUse)
	}
	// hostId + keyword 组合。
	combo, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{HostID: 7, Keyword: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	if combo.Total != 1 || combo.Items[0].HostID != 7 || combo.Items[0].Hostname != "uni-105" {
		t.Fatalf("hostId+keyword 组合过滤失效: %+v", combo)
	}
	// 未命中/未上报主机 → 空表而非错误。
	none, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, none.Items, none.Total, "未命中")
	empty, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{HostID: 9})
	if err != nil {
		t.Fatalf("指向未上报主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, empty.Items, empty.Total, "未上报主机")
}

// TestDockerVolumesCap 上限 500 条且截断先报全量。
func TestDockerVolumesCap(t *testing.T) {
	f := newResFixture(t)
	big := make([]agentproto.DockerVolume, 0, 600)
	for i := 0; i < 600; i++ {
		big = append(big, resVolume(fmt.Sprintf("cap-%03d", i), false))
	}
	f.saveState(t, 7, &agentproto.DockerState{Volumes: big})
	f.saveState(t, 8, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{resVolume("zzz-pad", false)}})

	resp, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是截断前全量 = 601, got %d", resp.Total)
	}
	if len(resp.Items) != 500 {
		t.Fatalf("Items 必须截到 500 条, got %d", len(resp.Items))
	}
	if resp.Items[0].Name != "cap-000" || resp.Items[499].Name != "cap-499" {
		t.Fatalf("截断必须砍排序后的尾部: 首=%s 尾=%s", resp.Items[0].Name, resp.Items[499].Name)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("前 500 行必须都来自主机 7: 第 %d 行 hostId=%d", i, it.HostID)
		}
	}
}

// TestDockerVolumesReadFailureSkipsHost 单台读失败跳过该主机，其余照常聚合。
func TestDockerVolumesReadFailureSkipsHost(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("seven-a", false), resVolume("seven-b", false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{resVolume("eight", true)}})
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{})
	if err != nil {
		t.Fatalf("单台读失败不得让整表失败: %v", err)
	}
	if resp.Total != 1 || resp.Items[0].HostID != 8 || resp.Items[0].Name != "eight" {
		t.Fatalf("读失败主机必须整台跳过: %+v", resp)
	}
	only, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, only.Items, only.Total, "读失败主机")
}

// TestDockerVolumesSortStable 排序稳定：两台都有 shared 卷（跨主机同名卷完全
// 现实），先按 hostId 分组、组内按名称。
func TestDockerVolumesSortStable(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 8, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("shared", false), resVolume("zeta", false),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Volumes: []agentproto.DockerVolume{
		resVolume("zeta", false), resVolume("shared", false), resVolume("alpha", false),
	}})

	resp, err := f.svc.Volumes(f.ctx, &request.DockerVolumeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		host uint64
		name string
	}{{7, "alpha"}, {7, "shared"}, {7, "zeta"}, {8, "shared"}, {8, "zeta"}}
	if len(resp.Items) != len(want) {
		t.Fatalf("应恰有 %d 行: %+v", len(want), resp)
	}
	for i, w := range want {
		if resp.Items[i].HostID != w.host || resp.Items[i].Name != w.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（同名不得跨主机交织）",
				i, resp.Items[i].HostID, resp.Items[i].Name, w.host, w.name)
		}
	}
}

// ── 网络 ────────────────────────────────────────────────────────────────

// TestDockerNetworksAggregatesAllHosts 全量聚合：排序按网络名、逐字段断言映射。
func TestDockerNetworksAggregatesAllHosts(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 8, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("bridge", false),
	}})
	// 主机 7 的两个网络按非字母序写入（zeta 先、app-net 后）。
	f.saveState(t, 7, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("zeta-net", false),
		{
			// 全字段样本。
			Name: "app-net", Driver: "bridge", Scope: "local", Internal: true, ContainersCount: 3,
		},
	}})

	resp, err := f.svc.Networks(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Fatalf("统一表应收全量 3 条: total=%d items=%d", resp.Total, len(resp.Items))
	}
	wantOrder := []struct {
		host uint64
		name string
	}{{7, "app-net"}, {7, "zeta-net"}, {8, "bridge"}}
	for i, want := range wantOrder {
		got := resp.Items[i]
		if got.HostID != want.host || got.Name != want.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（须按主机 id 升序、同机按网络名）",
				i, got.HostID, got.Name, want.host, want.name)
		}
	}
	a := resp.Items[0]
	if a.HostID != 7 || a.Hostname != "uni-105" {
		t.Fatalf("归属两列未映射: hostId=%d hostname=%s", a.HostID, a.Hostname)
	}
	if a.Driver != "bridge" || a.Scope != "local" || !a.Internal || a.ContainersCount != 3 {
		t.Fatalf("网络字段未逐字段映射: %+v", a.DockerNetworkItem)
	}
	if z := resp.Items[1]; z.Internal || z.ContainersCount != 1 {
		t.Fatalf("compact 网络条目未映射: %+v", z.DockerNetworkItem)
	}
}

// TestDockerNetworksHostKeywordAndInternalFilter hostId/keyword/internal 三层过滤。
func TestDockerNetworksHostKeywordAndInternalFilter(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("App-Net", true),
		resNetwork("egress", false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("app-shared", false),
	}})

	for _, kw := range []string{"app", "APP"} {
		resp, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{Keyword: kw})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != 2 {
			t.Fatalf("keyword=%q 必须命中两台各一枚: %+v", kw, resp)
		}
	}
	// internal 三值语义：true 只留隔离网络、false 只留非隔离。
	inner, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{Internal: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if inner.Total != 1 || inner.Items[0].Name != "App-Net" {
		t.Fatalf("internal=true 应恰收隔离网络 1 枚: %+v", inner)
	}
	outer, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{Internal: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if outer.Total != 2 {
		t.Fatalf("internal=false 应恰收 2 枚非隔离网络: %+v", outer)
	}
	// hostId + keyword 组合。
	combo, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{HostID: 7, Keyword: "egress"})
	if err != nil {
		t.Fatal(err)
	}
	if combo.Total != 1 || combo.Items[0].HostID != 7 || combo.Items[0].Hostname != "uni-105" {
		t.Fatalf("hostId+keyword 组合过滤失效: %+v", combo)
	}
	none, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, none.Items, none.Total, "未命中")
	empty, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{HostID: 9})
	if err != nil {
		t.Fatalf("指向未上报主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, empty.Items, empty.Total, "未上报主机")
}

// TestDockerNetworksCap 上限 500 条且截断先报全量。
func TestDockerNetworksCap(t *testing.T) {
	f := newResFixture(t)
	big := make([]agentproto.DockerNetwork, 0, 600)
	for i := 0; i < 600; i++ {
		big = append(big, resNetwork(fmt.Sprintf("cap-%03d", i), false))
	}
	f.saveState(t, 7, &agentproto.DockerState{Networks: big})
	f.saveState(t, 8, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{resNetwork("zzz-pad", false)}})

	resp, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是截断前全量 = 601, got %d", resp.Total)
	}
	if len(resp.Items) != 500 {
		t.Fatalf("Items 必须截到 500 条, got %d", len(resp.Items))
	}
	if resp.Items[0].Name != "cap-000" || resp.Items[499].Name != "cap-499" {
		t.Fatalf("截断必须砍排序后的尾部: 首=%s 尾=%s", resp.Items[0].Name, resp.Items[499].Name)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("前 500 行必须都来自主机 7: 第 %d 行 hostId=%d", i, it.HostID)
		}
	}
}

// TestDockerNetworksReadFailureSkipsHost 单台读失败跳过该主机，其余照常聚合。
func TestDockerNetworksReadFailureSkipsHost(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("seven-a", false), resNetwork("seven-b", false),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{resNetwork("eight", false)}})
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{})
	if err != nil {
		t.Fatalf("单台读失败不得让整表失败: %v", err)
	}
	if resp.Total != 1 || resp.Items[0].HostID != 8 || resp.Items[0].Name != "eight" {
		t.Fatalf("读失败主机必须整台跳过: %+v", resp)
	}
	only, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, only.Items, only.Total, "读失败主机")
}

// TestDockerNetworksSortStable 排序稳定：两台都有 shared 网络，先按 hostId
// 分组、组内按名称。
func TestDockerNetworksSortStable(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 8, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("shared", false), resNetwork("zeta", false),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Networks: []agentproto.DockerNetwork{
		resNetwork("zeta", false), resNetwork("shared", false), resNetwork("alpha", false),
	}})

	resp, err := f.svc.Networks(f.ctx, &request.DockerNetworkQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		host uint64
		name string
	}{{7, "alpha"}, {7, "shared"}, {7, "zeta"}, {8, "shared"}, {8, "zeta"}}
	if len(resp.Items) != len(want) {
		t.Fatalf("应恰有 %d 行: %+v", len(want), resp)
	}
	for i, w := range want {
		if resp.Items[i].HostID != w.host || resp.Items[i].Name != w.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（同名不得跨主机交织）",
				i, resp.Items[i].HostID, resp.Items[i].Name, w.host, w.name)
		}
	}
}

// ── 编排项目 ────────────────────────────────────────────────────────────

// TestDockerProjectsAggregatesAllHosts 全量聚合：排序按项目名、逐字段断言映射。
func TestDockerProjectsAggregatesAllHosts(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 8, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("blog", "running"),
	}})
	// 主机 7 的两个项目按非字母序写入（uni-center 先、aaa-project 后）。
	f.saveState(t, 7, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		{
			// 全字段样本。
			Name: "uni-center", ConfigFiles: []string{"/opt/uni/docker-compose.yml"},
			State: "running", Services: 3, ContainersCount: 5, Protected: true,
		},
		resProject("aaa-project", "partial"),
	}})

	resp, err := f.svc.Projects(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Fatalf("统一表应收全量 3 条: total=%d items=%d", resp.Total, len(resp.Items))
	}
	wantOrder := []struct {
		host uint64
		name string
	}{{7, "aaa-project"}, {7, "uni-center"}, {8, "blog"}}
	for i, want := range wantOrder {
		got := resp.Items[i]
		if got.HostID != want.host || got.Name != want.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（须按主机 id 升序、同机按项目名）",
				i, got.HostID, got.Name, want.host, want.name)
		}
	}
	p := resp.Items[1]
	if p.HostID != 7 || p.Hostname != "uni-105" {
		t.Fatalf("归属两列未映射: hostId=%d hostname=%s", p.HostID, p.Hostname)
	}
	if len(p.ConfigFiles) != 1 || p.ConfigFiles[0] != "/opt/uni/docker-compose.yml" ||
		p.State != "running" || p.Services != 3 || p.ContainersCount != 5 || !p.Protected {
		t.Fatalf("项目字段未逐字段映射: %+v", p.DockerProjectItem)
	}
	// compact 构造钉住同一条换算路径。
	if a := resp.Items[0]; a.State != "partial" || a.Services != 1 || a.ContainersCount != 1 || a.Protected {
		t.Fatalf("compact 项目条目未映射: %+v", a.DockerProjectItem)
	}
}

// TestDockerProjectsHostKeywordAndStateFilter hostId/keyword/state 三层过滤，
// 且 state 口径与容器表同一句：stopped = 一切非 running（partial 归入）。
func TestDockerProjectsHostKeywordAndStateFilter(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("Uni-Center", "running"),
		resProject("uni-backup", "stopped"),
		resProject("uni-lab", "partial"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("uni-web", "running"),
	}})

	// keyword 大小写不敏感：三枚 uni-* 命中。
	for _, kw := range []string{"uni-", "UNI-"} {
		resp, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{Keyword: kw})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != 4 {
			t.Fatalf("keyword=%q 必须命中四个项目: %+v", kw, resp)
		}
	}
	// running 与 stopped 互斥且并集 = 全量（与容器表/KPI 同一数学口径）。
	all, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{})
	if err != nil {
		t.Fatal(err)
	}
	running, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{State: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 4 || running.Total != 2 || stopped.Total != 2 {
		t.Fatalf("running/stopped 划分失真: all=%d running=%d stopped=%d", all.Total, running.Total, stopped.Total)
	}
	if running.Total+stopped.Total != all.Total {
		t.Fatalf("running+stopped 必须等于全量: %d+%d != %d", running.Total, stopped.Total, all.Total)
	}
	for _, it := range stopped.Items {
		if it.State == "running" {
			t.Fatalf("stopped 里混进了 running: %+v", it)
		}
	}
	// hostId + keyword 组合。
	combo, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{HostID: 7, Keyword: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	if combo.Total != 1 || combo.Items[0].HostID != 7 || combo.Items[0].Hostname != "uni-105" {
		t.Fatalf("hostId+keyword 组合过滤失效: %+v", combo)
	}
	none, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyNonNil(t, none.Items, none.Total, "未命中")
	empty, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{HostID: 9})
	if err != nil {
		t.Fatalf("指向未上报主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, empty.Items, empty.Total, "未上报主机")
}

// TestDockerProjectsRejectsInvalidState state 非法值 → 400 结论句（而不是静默
// 当不过滤 —— 那会让用户以为「筛了但没生效」；合法值大小写敏感，与 Workloads
// 同一句话、同一词表）。
func TestDockerProjectsRejectsInvalidState(t *testing.T) {
	f := newResFixture(t)
	for _, bad := range []string{"partial", "RUNNING", "paused"} {
		_, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{State: bad})
		if err == nil {
			t.Fatalf("state=%q 必须拒绝（静默忽略=筛选失效的假象）", bad)
		}
		var appErr *apperror.AppError
		if !errors.As(err, &appErr) || appErr.Code != apperror.CodeBadRequest {
			t.Fatalf("state=%q 必须是 400, got %v", bad, err)
		}
		if appErr.Message != "参数错误: state 仅支持 running 或 stopped" {
			t.Fatalf("400 必须给结论句, got %q", appErr.Message)
		}
	}
}

// TestDockerProjectsCap 上限 500 条且截断先报全量。
func TestDockerProjectsCap(t *testing.T) {
	f := newResFixture(t)
	big := make([]agentproto.DockerProject, 0, 600)
	for i := 0; i < 600; i++ {
		big = append(big, resProject(fmt.Sprintf("cap-%03d", i), "running"))
	}
	f.saveState(t, 7, &agentproto.DockerState{Projects: big})
	f.saveState(t, 8, &agentproto.DockerState{Projects: []agentproto.DockerProject{resProject("zzz-pad", "running")}})

	resp, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是截断前全量 = 601, got %d", resp.Total)
	}
	if len(resp.Items) != 500 {
		t.Fatalf("Items 必须截到 500 条, got %d", len(resp.Items))
	}
	if resp.Items[0].Name != "cap-000" || resp.Items[499].Name != "cap-499" {
		t.Fatalf("截断必须砍排序后的尾部: 首=%s 尾=%s", resp.Items[0].Name, resp.Items[499].Name)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("前 500 行必须都来自主机 7: 第 %d 行 hostId=%d", i, it.HostID)
		}
	}
}

// TestDockerProjectsReadFailureSkipsHost 单台读失败跳过该主机，其余照常聚合。
func TestDockerProjectsReadFailureSkipsHost(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 7, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("seven-a", "running"), resProject("seven-b", "stopped"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Projects: []agentproto.DockerProject{resProject("eight", "running")}})
	if err := f.rdb.Set(f.ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{})
	if err != nil {
		t.Fatalf("单台读失败不得让整表失败: %v", err)
	}
	if resp.Total != 1 || resp.Items[0].HostID != 8 || resp.Items[0].Name != "eight" {
		t.Fatalf("读失败主机必须整台跳过: %+v", resp)
	}
	only, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	requireEmptyNonNil(t, only.Items, only.Total, "读失败主机")
}

// TestDockerProjectsSortStable 排序稳定：两台都有 shared 项目，先按 hostId
// 分组、组内按名称。
func TestDockerProjectsSortStable(t *testing.T) {
	f := newResFixture(t)
	f.saveState(t, 8, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("shared", "running"), resProject("zeta", "stopped"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Projects: []agentproto.DockerProject{
		resProject("zeta", "running"), resProject("shared", "stopped"), resProject("alpha", "running"),
	}})

	resp, err := f.svc.Projects(f.ctx, &request.DockerProjectQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		host uint64
		name string
	}{{7, "alpha"}, {7, "shared"}, {7, "zeta"}, {8, "shared"}, {8, "zeta"}}
	if len(resp.Items) != len(want) {
		t.Fatalf("应恰有 %d 行: %+v", len(want), resp)
	}
	for i, w := range want {
		if resp.Items[i].HostID != w.host || resp.Items[i].Name != w.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（同名不得跨主机交织）",
				i, resp.Items[i].HostID, resp.Items[i].Name, w.host, w.name)
		}
	}
}

// boolPtr 是查询参数三值布尔的取址助手（request 包的口径是 *bool）。
func boolPtr(v bool) *bool { return &v }
