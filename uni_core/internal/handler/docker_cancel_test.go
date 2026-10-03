package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 显式取消端点（本波语义收口：取消的唯一入口）────────────────────────────
//
// 钉住的核心不变量：**cancel 帧只由这条端点发出**（断流只解除接入，见
// docker_stream_test.go 的三个 DisconnectOnlyDetaches），且它的权限包络与原来的
// 隐式路径（断流即取消 = 进度流接入）逐字相同 —— 记录存在且设备匹配、记录的权限码、
// 发起人归属，三闸缺一不可。

// cancelCmdContext 造一个 POST /docker/hosts/:id/cmds/:ref/cancel 的测试上下文。
func cancelCmdContext(rec *dockerstate.CmdRecord, uid uint64) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/docker/hosts/7/cmds/"+rec.Ref+"/cancel", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: rec.Ref}}
	c.Set(middleware.CtxClaims, testClaims(uid))
	return w, c
}

// TestCancelCmdSendsCancelFrameAndLeavesRecordAlone：受理一条进行中的拉取 → 取消 →
// 202 + 一帧 cancel（发往会话设备、句柄 = 派生的 pull_<ref>）+ 会话从注册表移除；
// **记录保持 pending 且分毫未动**（取消不改写记录：终态只有 agent 的 result 一个
// 写入者，202 说的是「请求已下发」而不是「已取消」）。
func TestCancelCmdSendsCancelFrameAndLeavesRecordAlone(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedPullRecord(t, env, 7, 1)
	seedPullProgressSession(t, env, 7, 1)

	w, c := cancelCmdContext(rec, 1)
	env.handler.CancelCmd(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("取消请求必须 202（已下发，不是已取消）: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), rec.Ref) {
		t.Fatalf("202 载荷应回指令号: %s", w.Body.String())
	}
	frames := env.sender.frameOps()
	if len(frames) != 1 || frames[0].Op != agentproto.DockerFrameOpCancel ||
		frames[0].SessionID != agentproto.DockerPullSessionID(pullRecRef) {
		t.Fatalf("必须下发一帧 cancel（句柄取协议派生）: %+v", frames)
	}
	if ids := env.sender.deviceIDs(); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("cancel 必须发往会话设备: %v", ids)
	}
	if env.sessions.Get(agentproto.DockerPullSessionID(pullRecRef)) != nil {
		t.Fatal("取消后会话必须从注册表移除")
	}

	got, err := env.cmds.Get(context.Background(), rec.Ref)
	if err != nil || got == nil {
		t.Fatalf("记录必须还在: %v %+v", err, got)
	}
	if got.Status != dockerstate.StatusPending {
		t.Fatalf("取消不得改写记录（终态只有 agent 的 result 一个写入者）: %s", got.Status)
	}
	if got.FinishedAt != 0 || got.Error != "" {
		t.Fatalf("取消不得往记录里写结论: %+v", got)
	}
}

// TestCancelCmdWithoutLocalSessionStillSendsFrame：注册表里没有这条会话（预登记失败/
// 已被清理/多实例下的另一实例持有）时**照样把帧发出去** —— 帧只需要设备与会话句柄，
// 而 agent 对未知会话的 cancel 是幂等忽略（迟到的取消本就无副作用）。
func TestCancelCmdWithoutLocalSessionStillSendsFrame(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedPullRecord(t, env, 7, 1) // 不预登记会话
	if env.sessions.Len() != 0 {
		t.Fatal("前置：注册表应为空")
	}

	w, c := cancelCmdContext(rec, 1)
	env.handler.CancelCmd(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("无本地会话也应受理取消: %d %s", w.Code, w.Body.String())
	}
	frames := env.sender.frameOps()
	if len(frames) != 1 || frames[0].Op != agentproto.DockerFrameOpCancel ||
		frames[0].SessionID != agentproto.DockerPullSessionID(pullRecRef) {
		t.Fatalf("无本地会话必须走设备级发送: %+v", frames)
	}
}

// TestCancelCmdRejects：五条拒绝路径（无权限 / 非发起人 / 记录不存在 / 已终态 /
// 非进度族的 action），且四条中一条都不得下发帧。
func TestCancelCmdRejects(t *testing.T) {
	t.Run("无权限 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:list"}) // 缺 docker:manage
		rec := seedPullRecord(t, env, 7, 1)
		seedPullProgressSession(t, env, 7, 1)
		w, c := cancelCmdContext(rec, 1)
		env.handler.CancelCmd(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("无权限必须 403: %d %s", w.Code, w.Body.String())
		}
		if len(env.sender.frameOps()) != 0 {
			t.Fatal("被拒的取消不得下发任何帧")
		}
		if env.sessions.Get(agentproto.DockerPullSessionID(pullRecRef)) == nil {
			t.Fatal("被拒的取消不得动会话")
		}
	})

	t.Run("非发起人 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPullRecord(t, env, 7, 1) // 发起人 = 1
		seedPullProgressSession(t, env, 7, 1)
		w, c := cancelCmdContext(rec, 2)
		env.handler.CancelCmd(c)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "无权取消该指令") {
			t.Fatalf("非发起人必须 403: %d %s", w.Code, w.Body.String())
		}
		if len(env.sender.frameOps()) != 0 {
			t.Fatal("非发起人不得下发任何帧")
		}
	})

	t.Run("记录不存在 404", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := &dockerstate.CmdRecord{Ref: "1790000000000000123", DeviceID: 7,
			Action: agentproto.DockerActionImagePull, Target: "x", UserID: 1, Perm: "docker:manage"}
		w, c := cancelCmdContext(rec, 1)
		env.handler.CancelCmd(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("记录不存在必须 404: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("已终态 409", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPullRecord(t, env, 7, 1)
		if err := env.cmds.Complete(context.Background(), rec,
			&agentproto.DockerCmdResult{Ref: rec.Ref, OK: true}); err != nil {
			t.Fatal(err)
		}
		seedPullProgressSession(t, env, 7, 1)
		w, c := cancelCmdContext(rec, 1)
		env.handler.CancelCmd(c)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "该任务已结束") {
			t.Fatalf("已终态必须 409: %d %s", w.Code, w.Body.String())
		}
		if len(env.sender.frameOps()) != 0 {
			t.Fatal("已终态不得下发任何帧（那只是噪声）")
		}
	})

	t.Run("非进度族 400", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := &dockerstate.CmdRecord{Ref: "1790000000000000456", DeviceID: 7,
			Action: agentproto.DockerActionContainerStop, Target: "mysql",
			UserID: 1, Perm: "docker:manage"}
		if err := env.cmds.Create(context.Background(), rec, time.Minute); err != nil {
			t.Fatal(err)
		}
		w, c := cancelCmdContext(rec, 1)
		env.handler.CancelCmd(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不支持取消") {
			t.Fatalf("非进度族必须 400: %d %s", w.Code, w.Body.String())
		}
		if len(env.sender.frameOps()) != 0 {
			t.Fatal("不支持取消的任务不得下发任何帧")
		}
	})
}

// TestCancelCmdDeviceOfflineConclusion：帧未送达（设备离线）如实回 500 结论句 ——
// 「点了取消就一定会截止」是假承诺，送不出去就必须说出口。
func TestCancelCmdDeviceOfflineConclusion(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedPullRecord(t, env, 7, 1) // 无本地会话 → 走设备级发送
	env.sender.offline = true

	w, c := cancelCmdContext(rec, 1)
	env.handler.CancelCmd(c)

	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "取消未送达") {
		t.Fatalf("未送达必须如实回 500 结论句: %d %s", w.Code, w.Body.String())
	}
}

// TestCancelCmdIsTheOnlyCancelTrigger（守卫）：源码级核对 —— 进度三族的**观看端点**
// 里不得出现 CancelSession，断开路径必须以 ReleaseSession 收尾。与三个
// DisconnectOnlyDetaches 的行为断言互为表里：行为断言跑一次断开只盖住正常断开分支，
// 源码断言把写失败分支（enc.Encode 失败那两处）也盖住。
//
// 断言的是函数体而不是整份文件：同一个文件里 CancelSession 仍然合法存在（日志/stats
// 的断开即停流、终端的 in-band 取消、sweep 的超时/离线，以及 CancelProgress 本身）。
func TestCancelCmdIsTheOnlyCancelTrigger(t *testing.T) {
	src, err := os.ReadFile("docker_stream.go")
	if err != nil {
		t.Fatalf("读取 docker_stream.go 失败: %v", err)
	}
	text := string(src)
	for _, fn := range []string{"servePullNDJSON", "serveBuildNDJSON", "servePushNDJSON"} {
		body := dockerFuncBody(t, text, fn)
		if strings.Contains(body, "CancelSession") {
			t.Fatalf("%s 不得出现 CancelSession（断流只是停止观看；取消走显式端点）", fn)
		}
		if !strings.Contains(body, "ReleaseSession") {
			t.Fatalf("%s 的断开路径必须以 ReleaseSession 收尾（只解除接入）", fn)
		}
	}
}

// dockerFuncBody 截出一个 DockerHandler 方法的函数体（花括号配对；找不到即判死）——
// 本文件顶层的「只扫这段」守卫用，避免整文件级断言被同文件里的合法用法误伤。
func dockerFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	idx := strings.Index(src, "func (h *DockerHandler) "+name+"(")
	if idx < 0 {
		t.Fatalf("源码里找不到 %s", name)
	}
	open := strings.IndexByte(src[idx:], '{')
	if open < 0 {
		t.Fatalf("%s 签名后找不到函数体", name)
	}
	depth := 0
	for i := idx + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[idx : i+1]
			}
		}
	}
	t.Fatalf("%s 的函数体不闭合", name)
	return ""
}
