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
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 跨主机统一工作负载表（Workloads）的测试。夹具与断言风格沿用
// docker_overview_test.go（同一套 newDockerOverviewSvc + overviewContainer）：
// 两张聚合页共用 hostRecords 枚举与排序纪律，测试也共用同一套「确定现在」。

// TestDockerWorkloadsAggregatesAllHosts 无过滤时的全量聚合：全部可管主机的
// 容器并成一张表，按主机 id 升序（同机按容器名）排序，且**逐字段**断言条目
// 映射（含归属两列）—— 这类映射的错误形态是「漏写一个字段」，抽查两三个字段
// 恰好漏掉时测试照样绿（与 TestDockerServiceStateMapsFullSnapshot 同一纪律）。
func TestDockerWorkloadsAggregatesAllHosts(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 8 先写、主机 7 后写（故意与 id 升序相反）；主机 7 的两个容器也按
	// 非字母序写入 —— 若排序依赖枚举/写入顺序，7 会排在 8 后、beta 排在 alpha 前。
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("solo", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("beta", "running", true),
			{
				// alpha 用全字段构造：统一表条目 = 单主机容器条目 + 归属两列，
				// 漏一个字段就是页面上少一列。
				ID: "c1", Name: "alpha", Image: "uni-center-core:latest", State: "exited",
				StatusText: "Exited (137) 3 days ago", Created: 1789000000, StartedAt: 1789000100,
				CPUPercent: 0.6, MemUsageMB: 91, MemLimitMB: 1024,
				NetRXBytesSec: 2400, NetTXBytesSec: 1100,
				ComposeProject: "uni-center", ComposeService: "uni_core", Protected: true,
				Ports: []agentproto.DockerPort{{IP: "0.0.0.0", PrivatePort: 8088, PublicPort: 20080, Type: "tcp"}},
			},
		}}, now); err != nil {
		t.Fatal(err)
	}

	// nil 查询与空查询同形（全量、无过滤）—— handler 永远传非 nil，这里钉住
	// service 对 nil 的防御语义，避免调用方多写一层判空。
	resp, err := svc.Workloads(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Fatalf("统一表应收全量 3 条: total=%d items=%d", resp.Total, len(resp.Items))
	}
	wantOrder := []struct {
		host uint64
		name string
	}{{7, "alpha"}, {7, "beta"}, {8, "solo"}}
	for i, want := range wantOrder {
		got := resp.Items[i]
		if got.HostID != want.host || got.Name != want.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（须按主机 id 升序、同机按容器名）",
				i, got.HostID, got.Name, want.host, want.name)
		}
	}

	// 归属两列 + 内嵌容器条目逐字段（alpha 是全字段样本）。
	a := resp.Items[0]
	if a.HostID != 7 || a.Hostname != "uni-105" {
		t.Fatalf("归属两列未映射: hostId=%d hostname=%s", a.HostID, a.Hostname)
	}
	if a.ID != "c1" || a.Image != "uni-center-core:latest" || a.State != "exited" ||
		a.StatusText != "Exited (137) 3 days ago" || a.Created != 1789000000 || a.StartedAt != 1789000100 {
		t.Fatalf("容器身份/状态字段未逐字段映射: %+v", a.DockerContainerItem)
	}
	if a.CPUPercent != 0.6 || a.MemUsageMB != 91 || a.MemLimitMB != 1024 ||
		a.NetRXBytesSec != 2400 || a.NetTXBytesSec != 1100 {
		t.Fatalf("资源字段未逐字段映射: %+v", a.DockerContainerItem)
	}
	if a.ComposeProject != "uni-center" || a.ComposeService != "uni_core" || !a.Protected {
		t.Fatalf("compose/保护结论未逐字段映射: %+v", a.DockerContainerItem)
	}
	if len(a.Ports) != 1 || a.Ports[0].IP != "0.0.0.0" || a.Ports[0].PrivatePort != 8088 ||
		a.Ports[0].PublicPort != 20080 || a.Ports[0].Type != "tcp" {
		t.Fatalf("端口映射未带入统一表条目: %+v", a.Ports)
	}
	// beta 钉住 compact 构造的字段也走了同一条换算路径。
	if b := resp.Items[1]; b.ID != "id-beta" || b.Image != "beta:latest" || !b.Protected {
		t.Fatalf("compact 容器条目未映射: %+v", b.DockerContainerItem)
	}
}

// TestDockerWorkloadsHostFilter hostId 限定单主机（兼容旧容器页的 host 语义）：
// 只收那台的容器；指向一台从未上报的主机 → 空表（不是 404 —— 那是主机粒度
// 的结论，容器表只说「没有容器」）。
func TestDockerWorkloadsHostFilter(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("core", "running", false),
			overviewContainer("backup", "exited", false),
		}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("console", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{HostID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("hostId=7 应只收主机 7 的 2 个容器: %+v", resp)
	}
	for _, it := range resp.Items {
		if it.HostID != 7 || it.Hostname != "uni-105" {
			t.Fatalf("过滤后不得混入别的主机的容器: %+v", it)
		}
	}
	// 从未上报的主机（集合里没有它）：空表而非错误。
	empty, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{HostID: 9})
	if err != nil {
		t.Fatalf("指向未上报主机不得报错: %v", err)
	}
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("未上报主机应给出空表: %+v", empty)
	}
}

// TestDockerWorkloadsStateFilter state 过滤的数学口径：running 与 stopped
// 互斥且并集 = 全量（stopped 是「一切非 running」的统称，与总览 KPI 的
// Stopped=Total-Running 同一句 —— 两边各写一套就出现口径分裂）。
func TestDockerWorkloadsStateFilter(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 5 个容器：2 running（core/console），3 非 running（exited/paused/created
	// —— 泛指三种非 running 形态都得被 stopped 收进）。
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("core", "running", false),
			overviewContainer("backup", "exited", false),
			overviewContainer("hold", "paused", false),
		}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("console", "running", false),
			overviewContainer("init", "created", false),
		}}, now); err != nil {
		t.Fatal(err)
	}

	all, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	running, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{State: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 {
		t.Fatalf("无过滤应收全量 5: %+v", all)
	}
	if running.Total != 2 || len(running.Items) != 2 {
		t.Fatalf("state=running 应恰收 2 个: %+v", running)
	}
	if stopped.Total != 3 || len(stopped.Items) != 3 {
		t.Fatalf("state=stopped 应恰收 3 个（exited/paused/created 同口径）: %+v", stopped)
	}
	if running.Total+stopped.Total != all.Total {
		t.Fatalf("running+stopped 必须等于全量（口径分裂即失真）: %d+%d != %d",
			running.Total, stopped.Total, all.Total)
	}
	for _, it := range stopped.Items {
		if it.State == "running" {
			t.Fatalf("stopped 里混进了 running: %+v", it)
		}
	}
}

// TestDockerWorkloadsKeywordCaseInsensitive keyword 对容器名**或**镜像名做
// 子串匹配，大小写不敏感：用户在搜索框里敲什么大小写都不该改变命中集。
func TestDockerWorkloadsKeywordCaseInsensitive(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 名字命中（Api-Gateway）与镜像命中（API-GATEWAY:v2）各一个。
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			{ID: "id-gw", Name: "Api-Gateway", Image: "nginx:latest", State: "running"},
		}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			{ID: "id-web", Name: "web", Image: "API-GATEWAY:v2", State: "running"},
		}}, now); err != nil {
		t.Fatal(err)
	}

	// 全小写关键词：名字命中（Api-Gateway）+ 镜像命中（API-GATEWAY:v2）。
	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{Keyword: "api-gateway"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("小写关键词应命中名字与镜像各一（大小写不敏感）: %+v", resp)
	}
	// 大写关键词：命中集不得改变（同一关键词的两种敲法是同一个意图）。
	upper, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{Keyword: "API-GATEWAY"})
	if err != nil {
		t.Fatal(err)
	}
	if upper.Total != 2 {
		t.Fatalf("大写关键词的命中集必须与小写一致: %+v", upper)
	}
	// 只碰镜像名（nginx）也要命中。
	byImage, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{Keyword: "NGINX"})
	if err != nil {
		t.Fatal(err)
	}
	if byImage.Total != 1 || byImage.Items[0].Name != "Api-Gateway" {
		t.Fatalf("镜像名子串必须命中（大小写不敏感）: %+v", byImage)
	}
	// 未命中 → 空表（不是错误）。
	none, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{Keyword: "no-such-thing"})
	if err != nil {
		t.Fatal(err)
	}
	if none.Total != 0 {
		t.Fatalf("未命中应是空表: %+v", none)
	}
}

// seedWorkloadsBig 造「主机 7 报 n 个 dead-XXX（排序在前）+ 主机 8 报 1 个 ok
// （排序在其后）」的数据 —— 分页与默认页大小用例共用同一副形状。
func seedWorkloadsBig(t *testing.T, store *dockerstate.Store, ctx context.Context, now time.Time, n int) {
	t.Helper()
	big := make([]agentproto.DockerContainer, 0, n)
	for i := 0; i < n; i++ {
		big = append(big, overviewContainer(fmt.Sprintf("dead-%03d", i), "exited", false))
	}
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: big}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("ok", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
}

// TestDockerWorkloadsPagination 真分页：601 条数据、page=2&pageSize=100 →
// Total=601（过滤后全量）、len(Items)=100、Items[0] 是**排序后的第 101 条**
// （钉住「先排序后切片」—— 先切片会拿到枚举运气的前 100，页边界就抖）。
func TestDockerWorkloadsPagination(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)
	seedWorkloadsBig(t, store, ctx, now, 600)

	q := &request.DockerWorkloadQuery{}
	q.Page, q.PageSize = 2, 100
	resp, err := svc.Workloads(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是过滤后全量 = 601, got %d", resp.Total)
	}
	if resp.Page != 2 || resp.PageSize != 100 {
		t.Fatalf("生效页参数必须回显: page=%d pageSize=%d", resp.Page, resp.PageSize)
	}
	if len(resp.Items) != 100 {
		t.Fatalf("第 2 页应收 100 条, got %d", len(resp.Items))
	}
	// 排序后索引 100..199 = dead-100..dead-199（主机 7 的 dead-000..dead-599 在前 600）。
	if resp.Items[0].Name != "dead-100" || resp.Items[99].Name != "dead-199" {
		t.Fatalf("第 2 页必须是排序后的第 101..200 条: 首=%s 尾=%s", resp.Items[0].Name, resp.Items[99].Name)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("第 2 页应全来自主机 7: 第 %d 行 hostId=%d", i, it.HostID)
		}
	}
}

// TestDockerWorkloadsDefaultPageSize 不传 pageSize 时默认 50（不是「全部返回」，
// 也不是 app.PageRequest 的 10）：601 条数据默认只回 50 条、Total 仍报 601。
func TestDockerWorkloadsDefaultPageSize(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)
	seedWorkloadsBig(t, store, ctx, now, 600)

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是全量 = 601, got %d", resp.Total)
	}
	if resp.Page != 1 || resp.PageSize != dockerDefaultPageSize {
		t.Fatalf("默认页参数应为 1/%d, got %d/%d", dockerDefaultPageSize, resp.Page, resp.PageSize)
	}
	if len(resp.Items) != dockerDefaultPageSize {
		t.Fatalf("默认应回 %d 条（不是全部 601）, got %d", dockerDefaultPageSize, len(resp.Items))
	}
	if resp.Items[0].Name != "dead-000" {
		t.Fatalf("默认页首条应为排序后的第一条: %s", resp.Items[0].Name)
	}
}

// TestDockerWorkloadsDefaultOrderUnchanged 评审回归点：不传 sortBy 时的次序与
// 改造前逐字一致（hostId 升序、同主机按容器名升序）——数据量小于默认页大小时
// 结果整份等同「排序后的全量」。
func TestDockerWorkloadsDefaultOrderUnchanged(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 8 先写、主机 7 后写；主机 7 内部按非字母序写入（beta 先、alpha 后）。
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("solo", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("beta", "running", false),
			overviewContainer("alpha", "exited", false),
		}}, now); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		host uint64
		name string
	}{{7, "alpha"}, {7, "beta"}, {8, "solo"}}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Fatalf("应回全量 3 条（< 默认页大小）: total=%d items=%d", resp.Total, len(resp.Items))
	}
	for i, w := range want {
		if resp.Items[i].HostID != w.host || resp.Items[i].Name != w.name {
			t.Fatalf("第 %d 行 = %d/%s, want %d/%s（默认次序不得变）",
				i, resp.Items[i].HostID, resp.Items[i].Name, w.host, w.name)
		}
	}
}

// TestDockerWorkloadsSortWhitelistAndFallback 排序白名单（照抄
// repository/device_test.go 的形状）：
//   - 白名单键（image）asc/desc 真的改变顺序；
//   - 非法 sortBy（含注入串）静默回落默认（不 400）；
//   - 非法 sortOrder（带空格）回落 desc。
func TestDockerWorkloadsSortWhitelistAndFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 命名与镜像名故意错位：默认按 name 排得 [alpha,beta,gamma]，
	// 按 image 排得完全不同的顺序 —— 才能区分「sortBy 真的生效」。
	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			{ID: "a", Name: "alpha", Image: "zzz:1", State: "running"},
			{ID: "b", Name: "beta", Image: "aaa:1", State: "running"},
		}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			{ID: "g", Name: "gamma", Image: "mmm:1", State: "running"},
		}}, now); err != nil {
		t.Fatal(err)
	}
	order := func(resp *response.DockerWorkloadListResp) string {
		s := ""
		for _, it := range resp.Items {
			s += fmt.Sprintf("%d/%s ", it.HostID, it.Name)
		}
		return s
	}

	// 默认（不传 sortBy）：现状次序 hostId 升序、同机按 name。
	def, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := order(def); got != "7/alpha 7/beta 8/gamma " {
		t.Fatalf("默认次序 = %q", got)
	}

	// 白名单键 image asc：aaa(beta)/mmm(gamma)/zzz(alpha)。
	asc, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{SortBy: "image", SortOrder: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	if got := order(asc); got != "7/beta 8/gamma 7/alpha " {
		t.Fatalf("sortBy=image asc 次序 = %q", got)
	}
	// image desc：完全反向。
	desc, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{SortBy: "image", SortOrder: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if got := order(desc); got != "7/alpha 8/gamma 7/beta " {
		t.Fatalf("sortBy=image desc 次序 = %q", got)
	}

	// 非法 sortBy（注入尝试）→ 静默回落默认，不报错。
	inj, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{SortBy: "hostId; DROP TABLE x", SortOrder: "asc"})
	if err != nil {
		t.Fatalf("非法 sortBy 不得报错（静默回落）: %v", err)
	}
	if got := order(inj); got != "7/alpha 7/beta 8/gamma " {
		t.Fatalf("非法 sortBy 必须回落默认, got %q", got)
	}

	// 非法 sortOrder（带空格）→ 回落 desc（不是 asc）。
	badOrder, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{SortBy: "image", SortOrder: "DESC "})
	if err != nil {
		t.Fatal(err)
	}
	if got := order(badOrder); got != "7/alpha 8/gamma 7/beta " {
		t.Fatalf("非法 sortOrder 必须回落 desc, got %q", got)
	}
}

// TestDockerWorkloadsOutOfRangePage 越界页（page 超总页数）→ 空 items + Total
// 不变，且 items 为空数组而非 null（与 /docker/tasks 的越界语义同句）。
func TestDockerWorkloadsOutOfRangePage(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)
	seedWorkloadsBig(t, store, ctx, now, 3)

	q := &request.DockerWorkloadQuery{}
	q.Page, q.PageSize = 99, 10
	resp, err := svc.Workloads(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 4 {
		t.Fatalf("Total 不随越界页改变 = 4, got %d", resp.Total)
	}
	if len(resp.Items) != 0 || resp.Items == nil {
		t.Fatalf("越界页应为空数组而非 null: %+v", resp.Items)
	}
}

// TestDockerWorkloadsReadFailureSkipsHost 单台快照读失败（键值损坏）不炸整表：
// 记 warn 后跳过该主机（其故障由总览页 error 字段如实呈现），其余主机照常聚合
// —— 部分聚合优于整体 500（与 Hosts/Overview 同款取舍）。
func TestDockerWorkloadsReadFailureSkipsHost(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	if err := store.Save(ctx, 7, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{
			overviewContainer("core", "running", false),
			overviewContainer("backup", "exited", false),
		}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("console", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
	// 把主机 7 的快照键覆盖成**坏 JSON**（模拟键值损坏/反序列化失败）：
	// store.Get 返回错误而不是「从未上报」的 (nil, nil)。
	if err := rdb.Set(ctx, dockerstate.StateKey(7), "{not json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatalf("单台读失败不得让整表失败: %v", err)
	}
	// 只收主机 8 的 1 个容器：失败主机**不编造**零容器假象（少几行，总览页见 error）。
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("读失败主机必须整台跳过: %+v", resp)
	}
	if resp.Items[0].HostID != 8 || resp.Items[0].Name != "console" {
		t.Fatalf("留下的必须是读成功主机的那行: %+v", resp.Items[0])
	}
	// 行被跳过**且**该主机出现在不可用清单（Reason 非空）——「少了几行、少的是谁、
	// 为什么」显式告知读者。
	if len(resp.UnavailableHosts) != 1 {
		t.Fatalf("读失败主机必须列进 UnavailableHosts: %+v", resp.UnavailableHosts)
	}
	if u := resp.UnavailableHosts[0]; u.HostID != 7 || u.Hostname != "uni-105" || u.Reason != dockerStateReadFailed {
		t.Fatalf("UnavailableHosts 条目不符: %+v", u)
	}
	// hostId 指向读失败主机：空表而非错误（与「未上报」同一句「没有容器」），
	// 且该主机同样列进不可用清单。
	only, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	if only.Total != 0 || len(only.Items) != 0 {
		t.Fatalf("读失败主机应给出空表: %+v", only)
	}
	if len(only.UnavailableHosts) != 1 || only.UnavailableHosts[0].HostID != 7 {
		t.Fatalf("hostId 收窄后该主机仍须在不可用清单里: %+v", only.UnavailableHosts)
	}
}

// TestDockerWorkloadsNeverReportedUnavailable 「从未上报」主机（集合里有它、快照键
// 却没写过）也列进不可用清单，且 Reason 与「读失败」是两句不同的话。
func TestDockerWorkloadsNeverReportedUnavailable(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	if err := store.Save(ctx, 8, &agentproto.DockerState{T: now.UnixMilli(), DockerOK: true,
		Containers: []agentproto.DockerContainer{overviewContainer("console", "running", false)}}, now); err != nil {
		t.Fatal(err)
	}
	// 主机 7 只在 docker:hosts 集合里、没有快照键（SAdd 直接登记，不写 state）。
	if err := rdb.SAdd(ctx, dockerstate.HostsKey, 7).Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].HostID != 8 {
		t.Fatalf("从未上报主机不得贡献行: %+v", resp)
	}
	if len(resp.UnavailableHosts) != 1 {
		t.Fatalf("从未上报主机必须列进不可用清单: %+v", resp.UnavailableHosts)
	}
	if u := resp.UnavailableHosts[0]; u.HostID != 7 || u.Hostname != "uni-105" || u.Reason != dockerStateNeverReported {
		t.Fatalf("从未上报的 Reason 应是另一句结论: %+v", u)
	}
}

// TestDockerWorkloadsRejectsInvalidState state 非法值 → 400 结论句（而不是
// 静默当不过滤 —— 那会让用户以为「筛了但没生效」）。合法值大小写敏感
// （协议的 state 是小写字面量，RUNNING 不在词表内）。
func TestDockerWorkloadsRejectsInvalidState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	for _, bad := range []string{"paused", "RUNNING", "exited"} {
		_, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{State: bad})
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
