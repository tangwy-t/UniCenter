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

// TestDockerWorkloadsCap 上限 500 条且**截断先报全量**：Total 是截断前的真数，
// Items 只留排序后的前 500（砍掉的是尾部，不是运气）。
func TestDockerWorkloadsCap(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	store := dockerstate.NewStore(rdb)
	ctx := context.Background()
	now := time.Now()
	svc := newDockerOverviewSvc(t, store, now)

	// 主机 7 报 600 个容器（超上限），主机 8 报 1 个（排在 7 之后，截断后不在场）。
	big := make([]agentproto.DockerContainer, 0, 600)
	for i := 0; i < 600; i++ {
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

	resp, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 601 {
		t.Fatalf("Total 必须是截断前全量 = 601, got %d", resp.Total)
	}
	if len(resp.Items) != 500 {
		t.Fatalf("Items 必须截到 500 条, got %d", len(resp.Items))
	}
	// 截断必须发生在排序之后：留下的是 dead-000..dead-499（主机 7 的前 500），
	// 而不是枚举运气给出的任意 500 行。
	if resp.Items[0].Name != "dead-000" || resp.Items[499].Name != "dead-499" {
		t.Fatalf("截断必须砍排序后的尾部: 首=%s 尾=%s", resp.Items[0].Name, resp.Items[499].Name)
	}
	for i, it := range resp.Items {
		if it.HostID != 7 {
			t.Fatalf("前 500 行必须都来自主机 7（8 排在其后）: 第 %d 行 hostId=%d", i, it.HostID)
		}
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
	// hostId 指向读失败主机：空表而非错误（与「未上报」同一句「没有容器」）。
	only, err := svc.Workloads(ctx, &request.DockerWorkloadQuery{HostID: 7})
	if err != nil {
		t.Fatalf("指向读失败主机不得报错: %v", err)
	}
	if only.Total != 0 || len(only.Items) != 0 {
		t.Fatalf("读失败主机应给出空表: %+v", only)
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
