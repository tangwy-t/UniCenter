package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 镜像目标版本（Wave 4c：GET/POST/DELETE /docker/image-targets）的 service 测试。
//
// 用内存替身实现 DockerImageTargetRepo 而不是真库：本层要钉的是**服务语义**
//（upsert 覆盖 / 删除幂等 / 校验 400 / 批量取一次），真 SQL 的形状由 repository
// 层测试（docker_image_target_test.go）负责。

// fakeImageTargetRepo 是内存版目标存储面，并记录各方法的调用次数（N+1 断言用）。
type fakeImageTargetRepo struct {
	rows        map[string]entity.DockerImageTarget
	upsertCalls int
	deleteCalls int
	listCalls   int
	batchCalls  int
	// batchTagCnt 记每次 FindByRepoTags 的入参标签数（钉住「一次取全量」）。
	batchTagCnt []int
}

func newFakeImageTargetRepo() *fakeImageTargetRepo {
	return &fakeImageTargetRepo{rows: map[string]entity.DockerImageTarget{}}
}

func (f *fakeImageTargetRepo) Upsert(_ context.Context, row *entity.DockerImageTarget) error {
	f.upsertCalls++
	f.rows[row.RepoTag] = *row
	return nil
}

func (f *fakeImageTargetRepo) Delete(_ context.Context, repoTag string) error {
	f.deleteCalls++
	delete(f.rows, repoTag)
	return nil
}

func (f *fakeImageTargetRepo) List(_ context.Context) ([]entity.DockerImageTarget, error) {
	f.listCalls++
	out := make([]entity.DockerImageTarget, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoTag < out[j].RepoTag })
	return out, nil
}

func (f *fakeImageTargetRepo) FindByRepoTags(_ context.Context, repoTags []string) (map[string]entity.DockerImageTarget, error) {
	f.batchCalls++
	f.batchTagCnt = append(f.batchTagCnt, len(repoTags))
	out := map[string]entity.DockerImageTarget{}
	for _, t := range repoTags {
		if r, ok := f.rows[t]; ok {
			out[t] = r
		}
	}
	return out, nil
}

// newTargetSvc 造一个只挂目标存储面的读面（Set/Clear/List 不碰 store/devices）。
func newTargetSvc(repo DockerImageTargetRepo) *DockerService {
	return NewDockerService(nil, nil, nil, nil).WithImageTargets(repo)
}

// wantBadRequest 断言错误是一条 400 结论句。
func wantBadRequest(t *testing.T, err error, what string) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.CodeBadRequest {
		t.Fatalf("%s 应为 400 结论句: %v", what, err)
	}
	if strings.TrimSpace(ae.Message) == "" {
		t.Fatalf("%s 的 400 必须带结论句", what)
	}
}

// TestDockerImageTargetSetUpsertOverwrites 声明是 upsert：同 repoTag 覆盖、只留一行、
// 覆盖调用两次 Upsert（无「创建冲突」）。
func TestDockerImageTargetSetUpsertOverwrites(t *testing.T) {
	repo := newFakeImageTargetRepo()
	svc := newTargetSvc(repo)
	ctx := context.Background()

	item, err := svc.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{
		RepoTag: "app:1", TargetDigest: "sha256:aaa"})
	if err != nil {
		t.Fatalf("Set #1: %v", err)
	}
	if item.RepoTag != "app:1" || item.TargetDigest != "sha256:aaa" {
		t.Fatalf("声明回显不符: %+v", item)
	}

	// 同 repoTag 覆盖。
	if _, err := svc.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{
		RepoTag: "app:1", TargetDigest: "sha256:bbb"}); err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	if repo.upsertCalls != 2 {
		t.Fatalf("覆盖应走第二次 Upsert: %d", repo.upsertCalls)
	}
	if len(repo.rows) != 1 || repo.rows["app:1"].TargetDigest != "sha256:bbb" {
		t.Fatalf("同 repoTag 覆盖后应恰一行且值被刷新: %+v", repo.rows)
	}
}

// TestDockerImageTargetSetValidation 钉住写端点的字段级校验：非法一律 400 结论句
//（用户输入校验报错，不走白名单静默回落）。
func TestDockerImageTargetSetValidation(t *testing.T) {
	svc := newTargetSvc(newFakeImageTargetRepo())
	ctx := context.Background()

	cases := []struct {
		name string
		req  request.DockerImageTargetSaveReq
	}{
		{"repoTag 空", request.DockerImageTargetSaveReq{RepoTag: "", TargetDigest: "sha256:aaa"}},
		{"repoTag 空白", request.DockerImageTargetSaveReq{RepoTag: "   ", TargetDigest: "sha256:aaa"}},
		{"repoTag 超长", request.DockerImageTargetSaveReq{RepoTag: strings.Repeat("a", 256), TargetDigest: "sha256:aaa"}},
		{"digest 空", request.DockerImageTargetSaveReq{RepoTag: "app:1", TargetDigest: ""}},
		{"digest 无 sha256 前缀", request.DockerImageTargetSaveReq{RepoTag: "app:1", TargetDigest: "abc123"}},
		{"digest 超长", request.DockerImageTargetSaveReq{RepoTag: "app:1", TargetDigest: "sha256:" + strings.Repeat("a", 200)}},
	}
	for _, tc := range cases {
		if _, err := svc.SetImageTarget(ctx, &tc.req); err != nil {
			wantBadRequest(t, err, tc.name)
		} else {
			t.Fatalf("%s 应报 400", tc.name)
		}
	}

	// 合法（含首尾空白，trim 后通过）。
	item, err := svc.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{
		RepoTag: "  app:1  ", TargetDigest: "  sha256:aaa  "})
	if err != nil {
		t.Fatalf("合法声明被拒: %v", err)
	}
	if item.RepoTag != "app:1" || item.TargetDigest != "sha256:aaa" {
		t.Fatalf("声明应 trim 后落库/回显: %+v", item)
	}

	// 未装配存储 → 500（装配错误早暴露）。
	bare := NewDockerService(nil, nil, nil, nil)
	if _, err := bare.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{RepoTag: "app:1", TargetDigest: "sha256:aaa"}); err == nil {
		t.Fatal("未装配目标存储应报错")
	}
}

// TestDockerImageTargetClearIdempotent 清除幂等：设了再清成功、再清仍成功、
// 清一个没设过的也成功；只有 repoTag 为空才是 400。
func TestDockerImageTargetClearIdempotent(t *testing.T) {
	repo := newFakeImageTargetRepo()
	svc := newTargetSvc(repo)
	ctx := context.Background()

	if _, err := svc.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{RepoTag: "app:1", TargetDigest: "sha256:aaa"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := svc.ClearImageTarget(ctx, "app:1"); err != nil {
		t.Fatalf("Clear 存在行: %v", err)
	}
	if err := svc.ClearImageTarget(ctx, "app:1"); err != nil {
		t.Fatalf("重复 Clear 必须幂等成功: %v", err)
	}
	if err := svc.ClearImageTarget(ctx, "never:set"); err != nil {
		t.Fatalf("清不存在的目标必须幂等成功: %v", err)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("清除后应无残留: %+v", repo.rows)
	}

	if err := svc.ClearImageTarget(ctx, "   "); err != nil {
		wantBadRequest(t, err, "repoTag 空")
	} else {
		t.Fatal("repoTag 为空应报 400")
	}
}

// TestDockerImageTargetList 清单：按 repoTag 升序、空时为空数组非 null、total 一致。
func TestDockerImageTargetList(t *testing.T) {
	repo := newFakeImageTargetRepo()
	svc := newTargetSvc(repo)
	ctx := context.Background()

	// 先验空清单形态。
	resp, err := svc.ListImageTargets(ctx)
	if err != nil {
		t.Fatalf("List 空: %v", err)
	}
	if resp.Total != 0 || resp.Items == nil {
		t.Fatalf("空清单应为空数组而非 null: %+v", resp)
	}

	for _, r := range []struct{ tag, dig string }{{"web:1", "sha256:b"}, {"app:1", "sha256:a"}} {
		if _, err := svc.SetImageTarget(ctx, &request.DockerImageTargetSaveReq{RepoTag: r.tag, TargetDigest: r.dig}); err != nil {
			t.Fatalf("Set %s: %v", r.tag, err)
		}
	}
	resp, err = svc.ListImageTargets(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("应恰 2 条: %+v", resp)
	}
	if resp.Items[0].RepoTag != "app:1" || resp.Items[1].RepoTag != "web:1" {
		t.Fatalf("清单应按 repoTag 升序: %+v", resp.Items)
	}
}

// driftRowByTag 按 repoTag 取漂移行（默认排序位置不固定）。
func driftRowByTag(t *testing.T, resp *response.DockerImageDriftResp, tag string) response.DockerImageDriftItem {
	t.Helper()
	for _, it := range resp.Items {
		if it.RepoTag == tag {
			return it
		}
	}
	t.Fatalf("漂移行里没有 tag=%q: %+v", tag, resp.Items)
	return response.DockerImageDriftItem{}
}

// TestImageDriftTargetAlignment 漂移视图的目标扩展：有目标 → 填 targetDigest/
// alignedCount/pendingHosts（含「摘要不符」与「该主机没有这个 tag」两类、按 hostId 升序）；
// 无目标 → 三枚字段都不产出（绝不用多数派臆造目标）。
func TestImageDriftTargetAlignment(t *testing.T) {
	f := newResFixture(t)
	repo := newFakeImageTargetRepo()
	f.svc.WithImageTargets(repo)

	// 主机 3/7 的 app:1 = 目标摘要（对齐）；主机 8 的 app:1 摘要不符（待对齐）；
	// 主机 9 根本没有 app:1（待对齐）。另加一个无目标的 web:1。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:target", "app:1"), driftImage("sha256:w", "web:1"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:target", "app:1"),
	}})
	f.saveState(t, 8, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:other", "app:1"),
	}})
	f.saveState(t, 9, &agentproto.DockerState{})

	if _, err := f.svc.SetImageTarget(f.ctx, &request.DockerImageTargetSaveReq{
		RepoTag: "app:1", TargetDigest: "sha256:target"}); err != nil {
		t.Fatalf("SetImageTarget: %v", err)
	}

	resp := mustDrift(t, f, &request.DockerImageDriftQuery{})

	app := driftRowByTag(t, resp, "app:1")
	if app.TargetDigest != "sha256:target" {
		t.Fatalf("app:1 应有目标摘要: %+v", app)
	}
	if app.AlignedCount != 2 {
		t.Fatalf("app:1 已对齐应 2 台（主机 3/7）: %+v", app)
	}
	// 待对齐 = 摘要不符（8）+ 该主机没有这个 tag（9），按 hostId 升序。
	if len(app.PendingHosts) != 2 || app.PendingHosts[0].HostID != 8 || app.PendingHosts[1].HostID != 9 {
		t.Fatalf("app:1 待对齐应为 [8,9] 升序: %+v", app.PendingHosts)
	}
	if app.PendingHosts[0].Hostname != "uni-106" || app.PendingHosts[1].Hostname != "uni-109" {
		t.Fatalf("待对齐主机引用应带主机名: %+v", app.PendingHosts)
	}

	// 无目标的 web:1：三枚字段一律不产出（不凭多数派臆造目标）。
	web := driftRowByTag(t, resp, "web:1")
	if web.TargetDigest != "" || len(web.PendingHosts) != 0 || web.AlignedCount != 0 {
		t.Fatalf("无目标的标签不该产出目标相关字段: %+v", web)
	}
}

// TestImageDriftTargetGroupScopesPending 待对齐只在**主机范围内**（group 过滤后）计算：
// group=prod 只留主机 7（它已对齐）→ alignedCount=1、pending 为空（不把组外主机算进来）。
func TestImageDriftTargetGroupScopesPending(t *testing.T) {
	f := newResFixture(t)
	repo := newFakeImageTargetRepo()
	f.svc.WithImageTargets(repo)

	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{driftImage("sha256:other", "app:1")}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{driftImage("sha256:target", "app:1")}}) // prod，且对齐

	if _, err := f.svc.SetImageTarget(f.ctx, &request.DockerImageTargetSaveReq{
		RepoTag: "app:1", TargetDigest: "sha256:target"}); err != nil {
		t.Fatalf("SetImageTarget: %v", err)
	}

	resp := mustDrift(t, f, &request.DockerImageDriftQuery{Group: "prod"})
	app := driftRowByTag(t, resp, "app:1")
	if app.AlignedCount != 1 || len(app.PendingHosts) != 0 {
		t.Fatalf("group=prod 下应只统计主机 7（已对齐、无待对齐）: %+v", app)
	}
}

// TestImageDriftTargetBatchNoNPlusOne 目标一次批量取：多标签漂移只触发**一次**
// FindByRepoTags（覆盖全部标签），不逐行查询。
func TestImageDriftTargetBatchNoNPlusOne(t *testing.T) {
	f := newResFixture(t)
	repo := newFakeImageTargetRepo()
	f.svc.WithImageTargets(repo)

	// 造 4 个不同标签，跨多台主机。
	f.saveState(t, 3, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a", "a:1"), driftImage("sha256:b", "b:1"), driftImage("sha256:c", "c:1"),
	}})
	f.saveState(t, 7, &agentproto.DockerState{Images: []agentproto.DockerImage{
		driftImage("sha256:a", "a:1"), driftImage("sha256:b", "b:1"), driftImage("sha256:d", "d:1"),
	}})

	resp := mustDrift(t, f, &request.DockerImageDriftQuery{})
	if resp.Total != 4 {
		t.Fatalf("应恰 4 个标签行: %d", resp.Total)
	}
	if repo.batchCalls != 1 {
		t.Fatalf("目标必须一次批量取（无 N+1）: batchCalls=%d", repo.batchCalls)
	}
	if len(repo.batchTagCnt) != 1 || repo.batchTagCnt[0] != 4 {
		t.Fatalf("批量取应一次覆盖全部 4 个标签: %+v", repo.batchTagCnt)
	}
}