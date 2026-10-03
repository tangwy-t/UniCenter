package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerpolicy"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// taskDeviceLookup / taskUserLookup 是任务面联查的替身：未命中返回**仓储哨兵**
// （repository.ErrNotFound）而不是裸 error —— 真仓储就是用它分辨「删了（降级空名）」
// 与「读库故障（告警）」的（与 handler 测试的 dockerDeviceReader 同语义）。
type taskDeviceLookup struct {
	devs map[uint64]*entity.Device
	err  error
}

func (d taskDeviceLookup) FindByID(_ context.Context, id uint64) (*entity.Device, error) {
	if d.err != nil {
		return nil, d.err
	}
	if dev, ok := d.devs[id]; ok {
		return dev, nil
	}
	return nil, repository.ErrNotFound
}

type taskUserLookup struct {
	users map[uint64]*entity.SysUser
	err   error
}

func (u taskUserLookup) FindByID(_ context.Context, id uint64) (*entity.SysUser, error) {
	if u.err != nil {
		return nil, u.err
	}
	if user, ok := u.users[id]; ok {
		return user, nil
	}
	return nil, repository.ErrNotFound
}

// newTaskSvc 构造任务面：miniredis 上的真 CmdStore + 可编程的联查替身。
func newTaskSvc(t *testing.T, devices taskDeviceLookup, users taskUserLookup) (*DockerTaskService, *dockerstate.CmdStore, *goredis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cmds := dockerstate.NewCmdStore(rdb)
	return NewDockerTaskService(cmds, devices, users, logger.NewNop()), cmds, rdb, mr
}

// seedTask 直接经数据面造一条记录（不经下发服务）：任务面是只读面，测试种子
// 走 CmdStore.Create 与真实写入同形态（含最近索引），终态用 Complete 完成。
func seedTask(t *testing.T, cs *dockerstate.CmdStore, rec *dockerstate.CmdRecord, ok bool, errMsg string) {
	t.Helper()
	if err := cs.Create(context.Background(), rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok {
		if err := cs.Complete(context.Background(), rec, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: true}); err != nil {
			t.Fatal(err)
		}
	} else if errMsg != "" {
		if err := cs.Complete(context.Background(), rec, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: false, Error: errMsg}); err != nil {
			t.Fatal(err)
		}
	}
}

func taskRec(ref string, deviceID, userID uint64, action, target string, created int64) *dockerstate.CmdRecord {
	return &dockerstate.CmdRecord{Ref: ref, DeviceID: deviceID, UserID: userID,
		Action: action, Target: target, Status: dockerstate.StatusPending, CreatedAt: created}
}

// TestTasksCrossHostOrderAndPaging 钉住核心契约：跨主机聚合、受理时刻降序、
// 分页（8d 起取代 6b 的「上限 100 条」—— 上限由页大小承载，Total 报真实全量）。
func TestTasksCrossHostOrderAndPaging(t *testing.T) {
	dev7 := &entity.Device{Hostname: "h7"}
	dev7.ID = 7
	dev9 := &entity.Device{Hostname: "h9"}
	dev9.ID = 9
	svc, cs, _, _ := newTaskSvc(t,
		taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev7, 9: dev9}},
		taskUserLookup{users: map[uint64]*entity.SysUser{1: {Username: "alice"}, 2: {Username: "bob"}}})

	seedTask(t, cs, taskRec("r1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000), true, "")
	seedTask(t, cs, taskRec("r2", 9, 2, agentproto.DockerActionContainerRestart, "mysql", 2000), false, "执行失败")
	// 同一毫秒两条（ref 降序兜底次序：后受理在前）
	seedTask(t, cs, taskRec("r3", 7, 1, agentproto.DockerActionComposeUp, "web", 3000), true, "")
	seedTask(t, cs, taskRec("r4", 9, 2, agentproto.DockerActionImageRemove, "alpine", 3000), false, "被保护，拒绝删除")

	resp, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 4 {
		t.Fatalf("跨主机全量 = 4, got %d (%+v)", len(resp.Items), resp.Items)
	}
	if resp.Items[0].Ref != "r4" || resp.Items[0].Hostname != "h9" || resp.Items[0].Username != "bob" {
		t.Fatalf("最近在前 + 联查归属: got %+v", resp.Items[0])
	}
	if resp.Items[1].Ref != "r3" || resp.Items[2].Ref != "r2" || resp.Items[3].Ref != "r1" {
		t.Fatalf("受理时刻降序 + 同毫秒 ref 降序: got %s,%s,%s",
			resp.Items[1].Ref, resp.Items[2].Ref, resp.Items[3].Ref)
	}
	if resp.Items[1].HostID != 7 || resp.Items[1].Username != "alice" || resp.Items[1].Target != "web" {
		t.Fatalf("条目的 hostId/username/target 必须逐列正确: got %+v", resp.Items[1])
	}
	if resp.Items[0].Summary != "被保护，拒绝删除" || resp.Items[1].Summary != dockerTaskSummarySucceeded {
		t.Fatalf("终态结论句（失败原文/成功兜底）: got %q / %q",
			resp.Items[0].Summary, resp.Items[1].Summary)
	}

	// 分页（8d 起取代「上限 100 条」）：补到 124 条在飞窗口内，逐页取回。
	// 用 image:pull 播种（可见动作）—— container:inspect 是读面，从 P2 打磨批起
	// 不进任务列表（守卫见 TestTasksHidesReadOnlyActions）。
	for i := 0; i < 120; i++ {
		seedTask(t, cs, taskRec(fmt.Sprintf("cap%03d", i), 7, 1, agentproto.DockerActionImagePull, "x", int64(5000+i)), true, "")
	}
	resp, err = svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 10 || resp.Total != 124 || resp.Page != 1 || resp.PageSize != 10 {
		t.Fatalf("缺省分页 = 第 1 页 10 条 / 共 124: items=%d total=%d page=%d pageSize=%d",
			len(resp.Items), resp.Total, resp.Page, resp.PageSize)
	}
	if resp.Items[0].CreatedAt != 5119 || resp.Items[9].CreatedAt != 5110 {
		t.Fatalf("第 1 页必须是最新的 10 条（受理时刻降序）, got %d..%d",
			resp.Items[0].CreatedAt, resp.Items[9].CreatedAt)
	}
	// 页大小 100（绑定层上限）：第 1 页 100 条、第 2 页 24 条，尾巴上仍是最旧的 r1。
	big, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{PageRequest: pageReq(1, 100)})
	if err != nil {
		t.Fatal(err)
	}
	if len(big.Items) != 100 || big.Total != 124 || big.Items[0].CreatedAt != 5119 {
		t.Fatalf("页大小 100 的第 1 页: items=%d total=%d head=%d",
			len(big.Items), big.Total, big.Items[0].CreatedAt)
	}
	tail, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{PageRequest: pageReq(2, 100)})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Items) != 24 || tail.Items[len(tail.Items)-1].Ref != "r1" {
		t.Fatalf("第 2 页 = 剩余 24 条且以最旧结尾: %d 条, tail=%s",
			len(tail.Items), tail.Items[len(tail.Items)-1].Ref)
	}
}

// TestTasksFilters 钉住三档过滤与组合：hostId/status/action 各自独立收窄。
func TestTasksFilters(t *testing.T) {
	dev7 := &entity.Device{Hostname: "h7"}
	dev7.ID = 7
	dev9 := &entity.Device{Hostname: "h9"}
	dev9.ID = 9
	svc, cs, _, _ := newTaskSvc(t,
		taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev7, 9: dev9}},
		taskUserLookup{})

	seedTask(t, cs, taskRec("a1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000), true, "")
	seedTask(t, cs, taskRec("a2", 9, 1, agentproto.DockerActionImagePull, "alpine", 2000), false, "拉取已取消")
	seedTask(t, cs, taskRec("a3", 7, 1, agentproto.DockerActionContainerRestart, "mysql", 3000), true, "")
	seedTask(t, cs, taskRec("a4", 7, 1, agentproto.DockerActionImagePull, "nginx", 4000), false, "") // pending

	cases := []struct {
		name string
		q    request.DockerTasksQuery
		want []string
	}{
		{"hostId=7", request.DockerTasksQuery{HostID: 7}, []string{"a4", "a3", "a1"}},
		{"status=pending", request.DockerTasksQuery{Status: "pending"}, []string{"a4"}},
		{"status=done", request.DockerTasksQuery{Status: "done"}, []string{"a3", "a2", "a1"}},
		{"action=image:pull", request.DockerTasksQuery{Action: agentproto.DockerActionImagePull}, []string{"a4", "a2", "a1"}},
		{"host+done 组合", request.DockerTasksQuery{HostID: 7, Status: "done"}, []string{"a3", "a1"}},
		{"host+action+pending 组合", request.DockerTasksQuery{HostID: 7, Action: agentproto.DockerActionImagePull, Status: "pending"}, []string{"a4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := svc.Tasks(context.Background(), &tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Items) != len(tc.want) {
				t.Fatalf("want %v, got %d (%+v)", tc.want, len(resp.Items), resp.Items)
			}
			for i, ref := range tc.want {
				if resp.Items[i].Ref != ref {
					t.Fatalf("顺序不符: [%d] = %s, want %s", i, resp.Items[i].Ref, ref)
				}
			}
		})
	}

	// pending 条目的结论句为空、终态投影照实；取消语义由 failed + 结论句承载
	// （a2 在 9 号机 —— 必须跨主机查 action 才拿得到它）。
	resp, _ := svc.Tasks(context.Background(), &request.DockerTasksQuery{Action: agentproto.DockerActionImagePull})
	if resp.Items[0].Ref != "a4" || resp.Items[0].Summary != "" || resp.Items[0].Status != "pending" {
		t.Fatalf("pending 条目: status=pending、summary 空, got %+v", resp.Items[0])
	}
	if resp.Items[1].Ref != "a2" || resp.Items[1].Status != "failed" || resp.Items[1].Summary != "拉取已取消" {
		t.Fatalf("取消语义由 failed + 结论句承载: got %+v", resp.Items[1])
	}
}

// TestTasksInvalidFilters 钉住 400 结论句（status/action 非法不得静默当不过滤）。
func TestTasksInvalidFilters(t *testing.T) {
	svc, _, _, _ := newTaskSvc(t, taskDeviceLookup{}, taskUserLookup{})
	ctx := context.Background()

	if _, err := svc.Tasks(ctx, &request.DockerTasksQuery{Status: "finished"}); err == nil {
		t.Fatal("status=finished 必须 400（silent filter 会让用户以为筛了没生效）")
	}
	if _, err := svc.Tasks(ctx, &request.DockerTasksQuery{Action: "not:an:action"}); err == nil {
		t.Fatal("未登记 action 必须 400（动作码唯一事实源是策略表）")
	}
	// docker:events 不在策略表（也不是用户任务），过滤它同样 400
	if _, err := svc.Tasks(ctx, &request.DockerTasksQuery{Action: agentproto.DockerActionEvents}); err == nil {
		t.Fatal("docker:events 不在策略表，必须 400")
	}
}

// TestTasksSkipsSystemAndExpired 钉住两类剔除：常驻订阅记录（UserID=0）不列、
// 已过期记录跳过并自我清报名。
func TestTasksSkipsSystemAndExpired(t *testing.T) {
	dev7 := &entity.Device{Hostname: "h7"}
	dev7.ID = 7
	svc, cs, rdb, _ := newTaskSvc(t, taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev7}}, taskUserLookup{})
	ctx := context.Background()

	seedTask(t, cs, taskRec("u1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000), true, "")
	// docker:events 常驻订阅形态：UserID=0（dockerevents.Manager 的真实记录形态）
	seedTask(t, cs, taskRec("sys1", 7, 0, agentproto.DockerActionEvents, "", 2000), true, "")
	// 已过期记录：记录键直接删掉（TTL 到期的同形态），报名还在
	seedTask(t, cs, taskRec("gone1", 7, 1, agentproto.DockerActionImagePull, "redis", 3000), true, "")
	if err := rdb.Del(ctx, dockerstate.CmdKeyPrefix+"gone1").Err(); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Tasks(ctx, &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Ref != "u1" {
		t.Fatalf("系统记录与已过期记录必须剔除, got %+v", resp.Items)
	}
	// 自愈：报名里的过期 ref 已被顺手剔除（下一次枚举不再撞墙）
	if n, _ := rdb.ZCard(ctx, dockerstate.RecentCmdKey).Result(); n != 2 {
		t.Fatalf("过期报名应被自愈剔除（剩 u1+sys1）, got %d", n)
	}
}

// TestTasksLookupDegrade 钉住联查降级口径：设备/用户已删（ErrNotFound）与读库故障
// 都不让条目标消失 —— 行还在，只是名字留空（hostId/userId 仍可导航）。
func TestTasksLookupDegrade(t *testing.T) {
	devMissing := &entity.Device{Hostname: "h7"}
	devMissing.ID = 8 // 联查表里只有 8，记录在 7
	svc, cs, _, _ := newTaskSvc(t,
		taskDeviceLookup{devs: map[uint64]*entity.Device{9: devMissing}, err: nil},
		taskUserLookup{err: repository.ErrNotFound})
	seedTask(t, cs, taskRec("d1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000), true, "")

	resp, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("设备/用户联查不到时条目必须保留, got %d", len(resp.Items))
	}
	if resp.Items[0].HostID != 7 || resp.Items[0].Hostname != "" || resp.Items[0].Username != "" {
		t.Fatalf("降级 = hostId 保留 + 名字留空, got %+v", resp.Items[0])
	}
}

// TestTasksTimeoutStatusReported 钉住 timeout 照实暴露（sweep 推断 ≠ 执行失败）：
// 服务端把到期指令终结成 timeout 后，任务中心条目 status=timeout、结论句照录。
func TestTasksTimeoutStatusReported(t *testing.T) {
	dev7 := &entity.Device{Hostname: "h7"}
	dev7.ID = 7
	svc, cs, _, _ := newTaskSvc(t, taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev7}}, taskUserLookup{})
	rec := taskRec("t1", 7, 1, agentproto.DockerActionImagePull, "alpine", 1000)
	if err := cs.Create(context.Background(), rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Timeout(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Status != dockerstate.StatusTimeout || resp.Items[0].Summary != "指令超时未完成" {
		t.Fatalf("timeout 必须照实（done 过滤会命中它）: got %+v", resp.Items)
	}
	if got, _ := svc.Tasks(context.Background(), &request.DockerTasksQuery{Status: "pending"}); len(got.Items) != 0 {
		t.Fatalf("timeout 是终态：pending 过滤不得命中, got %+v", got.Items)
	}
	if got, _ := svc.Tasks(context.Background(), &request.DockerTasksQuery{Status: "done"}); len(got.Items) != 1 {
		t.Fatalf("timeout 是终态：done 过滤必须命中, got %+v", got.Items)
	}
}

// TestTasksHidesReadOnlyActions 钉住 P2 打磨批的读面剔除（QA 路 1 P2）：只读动作
// 不进任务列表（「任务」的语义是变更/长任务），判定依据 = 策略表的 inspect 档。
//
// 守卫同时钉住「读面 == inspect 档」这条不变量：写死名单与策略表现状互为对照 ——
// 将来给某个动作换档位（或新增 inspect 档动作）时这里必有一边红灯，逼一次
// 「它还算不算任务」的重新裁定，而不是让列表被悄悄刷屏或悄悄吞掉一条变更。
func TestTasksHidesReadOnlyActions(t *testing.T) {
	// 实现单元直检：inspect 档剔除、manage 档的 image:scan 放行（长任务）、
	// 未知动作放行（fail-open：多显示一行好过静默吞掉历史记录）。
	if !dockerTaskReadOnly(agentproto.DockerActionContainerLogs) {
		t.Fatal("container:logs 属于读面，必须剔除")
	}
	if dockerTaskReadOnly(agentproto.DockerActionImageScan) {
		t.Fatal("image:scan 是长任务（manage 档），必须留在任务列表")
	}
	if dockerTaskReadOnly("not:an:action") {
		t.Fatal("策略表查不到的动作不得静默剔除")
	}

	// 读面名单（写死）与策略表 inspect 档（现状）必须恰好相等。
	readOnly := []string{
		agentproto.DockerActionContainerInspect,
		agentproto.DockerActionContainerLogs,
		agentproto.DockerActionContainerStats,
		agentproto.DockerActionImageInspect,
		agentproto.DockerActionComposeLogs,
		agentproto.DockerActionComposeFileRead,
	}
	inspectInPolicy := []string{}
	for _, p := range dockerpolicy.All() {
		if p.Perm == permission.PermDockerInspect {
			inspectInPolicy = append(inspectInPolicy, p.Action)
		}
	}
	inspectSet := map[string]struct{}{}
	for _, a := range inspectInPolicy {
		inspectSet[a] = struct{}{}
	}
	if len(inspectInPolicy) != len(readOnly) {
		t.Fatalf("读面名单与策略表 inspect 档条数不一致: 名单 %v, 策略表 %v", readOnly, inspectInPolicy)
	}
	for _, a := range readOnly {
		if _, ok := inspectSet[a]; !ok {
			t.Fatalf("动作 %s 不在策略表 inspect 档（名单与实现已漂移）: %v", a, inspectInPolicy)
		}
	}

	dev7 := &entity.Device{Hostname: "h7"}
	dev7.ID = 7
	svc, cs, _, _ := newTaskSvc(t, taskDeviceLookup{devs: map[uint64]*entity.Device{7: dev7}}, taskUserLookup{})

	created := int64(1000)
	for _, a := range readOnly {
		seedTask(t, cs, taskRec("ro-"+a, 7, 1, a, "x", created), true, "")
		created++
	}
	// 变更/长任务代表：pull（长任务）、restart（变更）、scan（读语义但 manage 档，
	// P3 设计要求它在任务中心留痕）、compose.file:write（配置编辑）。
	wantOrder := []string{
		agentproto.DockerActionComposeFileWrite,
		agentproto.DockerActionImageScan,
		agentproto.DockerActionContainerRestart,
		agentproto.DockerActionImagePull,
	}
	for _, a := range []string{
		agentproto.DockerActionImagePull,
		agentproto.DockerActionContainerRestart,
		agentproto.DockerActionImageScan,
		agentproto.DockerActionComposeFileWrite,
	} {
		seedTask(t, cs, taskRec("vi-"+a, 7, 1, a, "x", created), true, "")
		created++
	}

	resp, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != len(wantOrder) {
		t.Fatalf("任务列表应只含变更/长任务 %d 条, got %d (%+v)",
			len(wantOrder), len(resp.Items), resp.Items)
	}
	for i, want := range wantOrder {
		if resp.Items[i].Action != want {
			t.Fatalf("可见动作[%d] = %s, want %s（读面剔除 + 降序）", i, resp.Items[i].Action, want)
		}
	}
	// action 过滤对读面同样收口：显式查只读动作得空列表，而不是把它漏出来。
	for _, a := range readOnly {
		got, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{Action: a})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 0 {
			t.Fatalf("action=%s 必须被读面剔除, got %+v", a, got.Items)
		}
	}
}
