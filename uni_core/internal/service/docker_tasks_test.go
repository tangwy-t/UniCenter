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
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
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

// TestTasksCrossHostOrderAndCap 钉住核心契约：跨主机聚合、受理时刻降序、上限 100 条。
func TestTasksCrossHostOrderAndCap(t *testing.T) {
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

	// 上限：补到超过 100 条，端点只回最新 100（索引容量 300 内）。
	for i := 0; i < 120; i++ {
		seedTask(t, cs, taskRec(fmt.Sprintf("cap%03d", i), 7, 1, agentproto.DockerActionContainerInspect, "x", int64(5000+i)), true, "")
	}
	resp, err = svc.Tasks(context.Background(), &request.DockerTasksQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != dockerTaskCap {
		t.Fatalf("上限必须恰为 %d, got %d", dockerTaskCap, len(resp.Items))
	}
	if resp.Items[0].CreatedAt < 5119 {
		t.Fatalf("截断后端必须保留最新（受理时刻最大在前）, got %d", resp.Items[0].CreatedAt)
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
