package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 参数原文留存（方案 D）的守卫：存什么 / 门怎么判 / 超限截断 / 不泄漏 / 持久化往返。
//
// 权限门的钥匙是**这笔任务自己的权限码**（受理时校验过的那个码，见 dockerstate.CmdRecord.Perm）
// —— 三种调用者形态（持码 / 不持码 / admin 通配）各钉一遍，实时面与历史面两条投影路径
// 都要走同一句判定。

// taskOptionsSecret 是一个**只在参数原文里出现**的字面量：不持有权限时它必须在
// 响应体里逐字消失（不泄漏断言就搜它）。
const taskOptionsSecret = "SECRET-ARGV-DO-NOT-LEAK-42"

// taskOptionsTruncatedSuffix 是超限截断的尾标（与 middleware.TruncateString 同款文案）。
const taskOptionsTruncatedSuffix = "...(truncated)"

// optsRec 造一条带参数原文的记录（门 = 受理时校验的权限码）。
func optsRec(ref, action, target, perm, options string) *dockerstate.CmdRecord {
	rec := taskRec(ref, 7, 1, action, target, 1000)
	rec.Perm = perm
	rec.Options = options
	return rec
}

// taskItems 取一页条目（三种调用者形态共用一条取数路径）。
func taskItems(t *testing.T, svc *DockerTaskService, callerPerms []string) []response.DockerTaskItem {
	t.Helper()
	resp, err := svc.Tasks(context.Background(), &request.DockerTasksQuery{}, callerPerms)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Items
}

// TestTaskOptionsPermissionGate 钉住权限门的三态（实时面）。
func TestTaskOptionsPermissionGate(t *testing.T) {
	svc, cs, _, _ := newTaskSvc(t, taskDevices(7),
		taskUserLookup{users: map[uint64]*entity.SysUser{1: {Username: "alice"}}})
	rec := optsRec("r1", agentproto.DockerActionContainerExec, "web", permission.PermDockerExec,
		`{"target":"web","command":["sh","-c","`+taskOptionsSecret+`"]}`)
	seedTask(t, cs, rec, true, "")

	// 持任务自身的权限码 → 拿到原文。
	items := taskItems(t, svc, []string{permission.PermDockerExec})
	if len(items) != 1 || items[0].Options != rec.Options {
		t.Fatalf("持码调用者必须拿到参数原文: %+v", items)
	}
	// admin 通配 → 拿到原文。
	if items = taskItems(t, svc, []string{"admin"}); len(items) != 1 || items[0].Options != rec.Options {
		t.Fatalf("admin 必须通配拿到参数原文: %+v", items)
	}
	// 持别的码（这里是最低档 docker:list）→ 字段缺席（空串即 omitempty 下的缺席）。
	items = taskItems(t, svc, []string{permission.PermDockerList})
	if len(items) != 1 || items[0].Options != "" {
		t.Fatalf("不持码时参数原文必须缺席: %+v", items)
	}
	// 空权限集（未登录形态的直调）→ 同样缺席（门缺省即关闭）。
	if items = taskItems(t, svc, nil); len(items) != 1 || items[0].Options != "" {
		t.Fatalf("空权限集时参数原文必须缺席: %+v", items)
	}

	// **不泄漏**：无权限时把整页序列化成响应体，参数原文逐字不得出现。
	body, err := json.Marshal(taskItems(t, svc, []string{permission.PermDockerList}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), taskOptionsSecret) {
		t.Fatalf("无权限时响应体不得出现参数原文: %s", body)
	}
	if strings.Contains(string(body), `"options"`) {
		t.Fatalf("无权限时字段必须**缺席**而不是空串/掩码: %s", body)
	}
	// 对照：持码时它必须在（证明上面的「缺席」是门在起作用，不是压根没存）。
	body, err = json.Marshal(taskItems(t, svc, []string{permission.PermDockerExec}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), taskOptionsSecret) {
		t.Fatalf("持码时响应体必须带上参数原文: %s", body)
	}
}

// TestEncodeCmdOptionsTruncation 钉住截断口径：超 16 KiB 截断且**显式标注**，
// 未超限原样（仍可解回协议选项）。
func TestEncodeCmdOptionsTruncation(t *testing.T) {
	// 超限：compose 的 YAML 正文没有协议上限，是最容易撑爆的一份。
	big := &agentproto.DockerCmdOptions{Target: "web", Content: strings.Repeat("x", 32*1024)}
	got := encodeCmdOptions(big)
	if len(got) != dockerstate.CmdOptionsMaxBytes+len(taskOptionsTruncatedSuffix) {
		t.Fatalf("超限必须截到上限并带尾标: len=%d want=%d",
			len(got), dockerstate.CmdOptionsMaxBytes+len(taskOptionsTruncatedSuffix))
	}
	if !strings.HasSuffix(got, taskOptionsTruncatedSuffix) {
		t.Fatalf("截断必须显式标注（尾标）: %q...", got[len(got)-32:])
	}

	// 未超限：原样，且是**合法 JSON**（可解回协议选项）。
	small := &agentproto.DockerCmdOptions{Target: "web", Command: []string{"sh", "-c", taskOptionsSecret}}
	s := encodeCmdOptions(small)
	if len(s) > dockerstate.CmdOptionsMaxBytes || strings.HasSuffix(s, taskOptionsTruncatedSuffix) {
		t.Fatalf("未超限不得截断: %q", s)
	}
	var back agentproto.DockerCmdOptions
	if err := json.Unmarshal([]byte(s), &back); err != nil {
		t.Fatalf("未截断的原文必须是合法 JSON: %v (%q)", err, s)
	}
	if back.Target != "web" || len(back.Command) != 3 || back.Command[2] != taskOptionsSecret {
		t.Fatalf("原文必须逐字段保真: %+v", back)
	}
}

// TestTaskOptionsHistoryRoundTrip 钉住持久化往返：受理 → 终态落库 → Redis 易失
// （TTL 到期/重启）→ **换一个服务实例**（同 DB）仍能从历史面取回原文，且权限门在
// 历史面上同样生效。
func TestTaskOptionsHistoryRoundTrip(t *testing.T) {
	devices := taskDevices(7)
	users := taskUserLookup{users: map[uint64]*entity.SysUser{1: {Username: "alice"}}}
	_, cs, rdb, _, db := newTaskSvcWithHistory(t, devices, users)
	ctx := context.Background()

	rec := optsRec("r1", agentproto.DockerActionComposeUp, "web", permission.PermDockerManage,
		`{"target":"web","content":"`+taskOptionsSecret+`"}`)
	if err := cs.Create(ctx, rec, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cs.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: "r1", OK: true}); err != nil {
		t.Fatal(err)
	}

	// 落库的直接证据：历史行带上权限门与参数原文两列。
	var row entity.DockerTaskHistory
	if err := db.Where("ref = ?", "r1").First(&row).Error; err != nil {
		t.Fatalf("终态必须落库: %v", err)
	}
	if row.Perm != permission.PermDockerManage || row.Options != rec.Options {
		t.Fatalf("历史行必须带上权限门与参数原文: %+v", row)
	}

	// 模拟易失窗口关闭：记录键与最近索引一并清掉（TTL 到期 / 重启后 Redis 被清）。
	if err := rdb.Del(ctx, dockerstate.CmdKeyPrefix+"r1").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.ZRem(ctx, dockerstate.RecentCmdKey, "r1").Err(); err != nil {
		t.Fatal(err)
	}

	// 「换进程」：同一个 DB 上重建一套（实时面为空的真 CmdStore + 真历史仓储）。
	repo := repository.NewDockerTaskHistoryRepo(db)
	svc2 := NewDockerTaskService(dockerstate.NewCmdStore(rdb), devices, users, logger.NewNop()).
		WithHistory(repo)

	got := taskItems(t, svc2, []string{permission.PermDockerManage})
	if len(got) != 1 || got[0].Options != rec.Options {
		t.Fatalf("历史面必须取回参数原文: %+v", got)
	}
	denied := taskItems(t, svc2, []string{permission.PermDockerList})
	if len(denied) != 1 || denied[0].Options != "" {
		t.Fatalf("历史面的权限门必须与实时面同源: %+v", denied)
	}
	body, err := json.Marshal(denied)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), taskOptionsSecret) {
		t.Fatalf("历史面在无权限时不得泄漏参数原文: %s", body)
	}
}
