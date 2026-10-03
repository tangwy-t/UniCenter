package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// newTaskSvcWithHistory 构造**完整形态**的任务面（8d）：真 CmdStore（miniredis）
// + 真历史仓储（sqlite）+ 终态钩子（recorder），端到端钉住
// 「终态 → 落库 → 合并读面」这条链（不是替身拼出来的形状）。
func newTaskSvcWithHistory(t *testing.T, devices taskDeviceLookup, users taskUserLookup) (*DockerTaskService, *dockerstate.CmdStore, *goredis.Client, *miniredis.Miniredis, *gorm.DB) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.DockerTaskHistory{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})

	repo := repository.NewDockerTaskHistoryRepo(db)
	cmds := dockerstate.NewCmdStore(rdb).
		WithTerminalHook(NewDockerTaskHistoryRecorder(repo, logger.NewNop()).Record)
	svc := NewDockerTaskService(cmds, devices, users, logger.NewNop()).WithHistory(repo)
	return svc, cmds, rdb, mr, db
}

func taskDevices(ids ...uint64) taskDeviceLookup {
	m := map[uint64]*entity.Device{}
	for _, id := range ids {
		d := &entity.Device{Hostname: fmt.Sprintf("h%d", id)}
		d.ID = id
		m[id] = d
	}
	return taskDeviceLookup{devs: m}
}

// pageReq 构造分页查询（合并分页用例的便捷形态）。
func pageReq(page, size int) app.PageRequest {
	return app.PageRequest{Page: page, PageSize: size}
}

// TestTasksHistorySurvivesRedisExpiry 是持久化的**直接证明**（单测形态）：
// 终态落库后把 Redis 记录与报名一并抹掉（= TTL 到期 / core 重启后 Redis 被清），
// 任务中心仍能从历史面查出这条任务，且字段与实时面逐字一致。
func TestTasksHistorySurvivesRedisExpiry(t *testing.T) {
	svc, cs, rdb, _, db := newTaskSvcWithHistory(t, taskDevices(7),
		taskUserLookup{users: map[uint64]*entity.SysUser{1: {Username: "alice"}}})
	ctx := context.Background()

	rec := taskRec("r1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000)
	if err := cs.Create(ctx, rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: "r1", OK: false, Error: "拉取失败"}); err != nil {
		t.Fatal(err)
	}

	// 落库的直接证据：表里有这一行，且终态事实齐全。
	var n int64
	if err := db.Model(&entity.DockerTaskHistory{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("终态必须落库一行, got %d (err=%v)", n, err)
	}

	// 模拟易失窗口关闭：记录键与最近索引一并清掉（TTL 到期的同形态）。
	if err := rdb.Del(ctx, dockerstate.CmdKeyPrefix+"r1").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.ZRem(ctx, dockerstate.RecentCmdKey, "r1").Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Tasks(ctx, &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Ref != "r1" {
		t.Fatalf("Redis 记录消失后必须由历史面兜底: %+v", resp.Items)
	}
	got := resp.Items[0]
	if got.Status != dockerstate.StatusFailed || got.Summary != "拉取失败" ||
		got.HostID != 7 || got.Hostname != "h7" || got.Username != "alice" ||
		got.Action != agentproto.DockerActionImagePull || got.Target != "alpine" || got.CreatedAt != 1000 {
		t.Fatalf("历史行的投影必须与实时面逐字一致: %+v", got)
	}
	if resp.Total != 1 || resp.Page != 1 || resp.PageSize != 10 {
		t.Fatalf("合计口径: total=%d page=%d pageSize=%d, want 1/1/10", resp.Total, resp.Page, resp.PageSize)
	}
}

// TestTasksMergeDedupeLiveWins 钉住合并去重：同一 ref 同时在两条腿上时只出一行，
// 且**实时胜**（活的记录可能刚被迟到的事实覆盖过）；历史独有的行照常出现。
func TestTasksMergeDedupeLiveWins(t *testing.T) {
	svc, cs, _, _, db := newTaskSvcWithHistory(t, taskDevices(7), taskUserLookup{})
	ctx := context.Background()
	repo := repository.NewDockerTaskHistoryRepo(db)

	// live1：终态 + 已落库（两条腿都有 —— 去重后只出一行）。
	live1 := taskRec("live1", 7, 1, agentproto.DockerActionImagePull, "alpine", 2000)
	if err := cs.Create(ctx, live1, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, live1, &agentproto.DockerCmdResult{Ref: "live1", OK: false, Error: "拉取失败"}); err != nil {
		t.Fatal(err)
	}
	// 再把历史行改成一个**陈旧**的结论（模拟「库里落后于 Redis」的窗口）：
	// 合并读面必须以实时面为准。
	stale := &entity.DockerTaskHistory{Ref: "live1", DeviceID: 7, Action: agentproto.DockerActionImagePull,
		Target: "alpine", UserID: 1, Status: "timeout", Summary: "指令超时未完成",
		AcceptedAt: 2000, FinishedAt: 2100}
	if err := repo.Upsert(ctx, stale); err != nil {
		t.Fatal(err)
	}

	// live2：在途（只在实时面）。
	live2 := taskRec("live2", 7, 1, agentproto.DockerActionContainerRestart, "mysql", 3000)
	if err := cs.Create(ctx, live2, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	// hist1：历史独有（Redis 里已经没有它了）。
	if err := repo.Upsert(ctx, &entity.DockerTaskHistory{Ref: "hist1", DeviceID: 7,
		Action: agentproto.DockerActionComposeUp, Target: "web", UserID: 1,
		Status: "succeeded", Summary: "执行成功", AcceptedAt: 1000, FinishedAt: 1500}); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Tasks(ctx, &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]mergedRow{}
	for _, it := range resp.Items {
		if _, dup := refs[it.Ref]; dup {
			t.Fatalf("去重失效：%s 出现了两次", it.Ref)
		}
		refs[it.Ref] = mergedRow{status: it.Status, summary: it.Summary}
	}
	if len(resp.Items) != 3 || resp.Total != 3 {
		t.Fatalf("合并后 3 条（live1 去重 + live2 + hist1）: items=%d total=%d", len(resp.Items), resp.Total)
	}
	if got := refs["live1"]; got.status != dockerstate.StatusFailed || got.summary != "拉取失败" {
		t.Fatalf("同 ref 必须实时胜（库里那条陈旧结论不得胜出）: %+v", got)
	}
	if refs["live2"].status != dockerstate.StatusPending || refs["hist1"].status != dockerstate.StatusSucceeded {
		t.Fatalf("两腿的条目都要在场: %+v", refs)
	}
	// 受理时刻降序：live2(3000) → live1(2000) → hist1(1000)。
	if resp.Items[0].Ref != "live2" || resp.Items[1].Ref != "live1" || resp.Items[2].Ref != "hist1" {
		t.Fatalf("合并后必须整体重排（实时在前只是巧合，排序才是契约）: %+v", resp.Items)
	}
}

// mergedRow 是合并断言的极简投影（只取要用到的两列）。
type mergedRow struct {
	status  string
	summary string
}

// TestTasksMergedPagination 钉住合并分页：页大小/页码/合计，且**历史侧的取数上界
// 由页偏移 + 页大小 + 实时条数推导**（实时重叠不会把某页掏空）。
func TestTasksMergedPagination(t *testing.T) {
	svc, cs, _, _, db := newTaskSvcWithHistory(t, taskDevices(7), taskUserLookup{})
	ctx := context.Background()
	repo := repository.NewDockerTaskHistoryRepo(db)

	// 25 条历史（受理时刻 1000..2500）+ 2 条实时终态（也是这两条历史的重叠源）。
	for i := 0; i < 25; i++ {
		if err := repo.Upsert(ctx, &entity.DockerTaskHistory{Ref: refName(i), DeviceID: 7,
			Action: agentproto.DockerActionImagePull, Target: "alpine", UserID: 1,
			Status: "succeeded", Summary: "执行成功",
			AcceptedAt: int64(1000 + i*100), FinishedAt: int64(1050 + i*100)}); err != nil {
			t.Fatal(err)
		}
	}
	// 实时面的两条（恰是历史里最新的两条：重叠去重后总数仍是 25）。
	for _, i := range []int{24, 23} {
		rec := taskRec(refName(i), 7, 1, agentproto.DockerActionImagePull, "alpine", int64(1000+i*100))
		if err := cs.Create(ctx, rec, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := cs.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: refName(i), OK: true}); err != nil {
			t.Fatal(err)
		}
	}

	// 第 1 页（默认 10 条）：最新的 10 条 = 2400..1500。
	p1, err := svc.Tasks(ctx, &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 10 || p1.Total != 25 || p1.Page != 1 || p1.PageSize != 10 {
		t.Fatalf("第 1 页: items=%d total=%d page=%d pageSize=%d", len(p1.Items), p1.Total, p1.Page, p1.PageSize)
	}
	if p1.Items[0].Ref != refName(24) || p1.Items[9].Ref != refName(15) {
		t.Fatalf("第 1 页窗口: %s..%s", p1.Items[0].Ref, p1.Items[9].Ref)
	}

	// 第 2 页：1400..500（页偏移 10 起）。
	p2, err := svc.Tasks(ctx, &request.DockerTasksQuery{PageRequest: pageReq(2, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 10 || p2.Total != 25 || p2.Page != 2 {
		t.Fatalf("第 2 页: items=%d total=%d page=%d", len(p2.Items), p2.Total, p2.Page)
	}
	if p2.Items[0].Ref != refName(14) || p2.Items[9].Ref != refName(5) {
		t.Fatalf("第 2 页窗口: %s..%s", p2.Items[0].Ref, p2.Items[9].Ref)
	}

	// 第 3 页：剩下的 5 条。
	p3, err := svc.Tasks(ctx, &request.DockerTasksQuery{PageRequest: pageReq(3, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p3.Items) != 5 || p3.Items[0].Ref != refName(4) || p3.Items[4].Ref != refName(0) {
		t.Fatalf("第 3 页窗口: %+v", p3.Items)
	}

	// 越界页：空 items、Total 不变（前端据 Total 把页码拉回第一页）。
	p4, err := svc.Tasks(ctx, &request.DockerTasksQuery{PageRequest: pageReq(9, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p4.Items) != 0 || p4.Total != 25 {
		t.Fatalf("越界页: items=%d total=%d, want 0/25", len(p4.Items), p4.Total)
	}

	// 页大小上限兜底（HTTP 绑定已封顶；服务侧再夹一次，防直调放大取数上界）。
	big, err := svc.Tasks(ctx, &request.DockerTasksQuery{PageRequest: pageReq(1, 100000)})
	if err != nil {
		t.Fatal(err)
	}
	if big.PageSize != 100 {
		t.Fatalf("页大小必须夹到 %d, got %d", dockerTaskPageSizeMax, big.PageSize)
	}
}

// TestTasksHistoryFiltersUnion 钉住过滤的并集语义：pending 档只有实时面有内容
// （历史表只存终态 → 不查询、也不拉低合计），done 档两条腿都参与合并。
func TestTasksHistoryFiltersUnion(t *testing.T) {
	svc, cs, _, _, db := newTaskSvcWithHistory(t, taskDevices(7), taskUserLookup{})
	ctx := context.Background()
	repo := repository.NewDockerTaskHistoryRepo(db)
	_ = repo.Upsert(ctx, &entity.DockerTaskHistory{Ref: "h1", DeviceID: 7,
		Action: agentproto.DockerActionImagePull, Target: "alpine", UserID: 1,
		Status: "succeeded", Summary: "执行成功", AcceptedAt: 1000, FinishedAt: 1100})

	pend := taskRec("p1", 7, 1, agentproto.DockerActionImagePull, "alpine", 2000)
	if err := cs.Create(ctx, pend, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	done := taskRec("d1", 7, 1, agentproto.DockerActionContainerRestart, "mysql", 1500)
	if err := cs.Create(ctx, done, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, done, &agentproto.DockerCmdResult{Ref: "d1", OK: true}); err != nil {
		t.Fatal(err)
	}

	// pending：只有实时在途那一条；历史面不参与（合计也只有 1）。
	got, err := svc.Tasks(ctx, &request.DockerTasksQuery{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Ref != "p1" || got.Total != 1 {
		t.Fatalf("pending 档 = 实时的在途集合: %+v (total=%d)", got.Items, got.Total)
	}

	// done：实时终态（d1）+ 历史（h1 + d1 的重叠去重）= 2。
	got, err = svc.Tasks(ctx, &request.DockerTasksQuery{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Total != 2 {
		t.Fatalf("done 档 = 两条腿的并集: %+v (total=%d)", got.Items, got.Total)
	}
	if got.Items[0].Ref != "d1" || got.Items[1].Ref != "h1" {
		t.Fatalf("done 档顺序（时刻降序）: %+v", got.Items)
	}

	// all：三者都在（p1 在途 + d1 + h1）。
	got, err = svc.Tasks(ctx, &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 || got.Total != 3 {
		t.Fatalf("全部档 = 3: %+v (total=%d)", got.Items, got.Total)
	}
}

// refName 生成 r00..r24 形态的 ref（定长，排序与字典序一致）。
func refName(i int) string {
	return "r" + string([]byte{'0' + byte(i/10), '0' + byte(i%10)})
}
