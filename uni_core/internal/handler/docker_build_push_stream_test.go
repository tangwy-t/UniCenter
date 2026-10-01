package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建/推送进度流端点（P2）──────────────────────────────────────────────
//
// 接入纪律与拉取进度流**逐条相同**（权限按记录的 docker:manage、发起人归属、
// 派生句柄、断开即 cancel —— 数据形态不同不能带来任何鉴权差别）。夹具照
// seedPullRecord/seedPullProgressSession/pullStreamContext 同款各建一套。

const buildRecRef = "1790000000000000888"

func seedBuildRecord(t *testing.T, env *dockerTestEnv, device, user uint64) *dockerstate.CmdRecord {
	t.Helper()
	rec := &dockerstate.CmdRecord{Ref: buildRecRef, DeviceID: device, Action: agentproto.DockerActionImageBuild,
		Target: "app:1", UserID: user, Perm: "docker:manage"}
	if err := env.cmds.Create(context.Background(), rec, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	return rec
}

func seedBuildProgressSession(t *testing.T, env *dockerTestEnv, device, user uint64) *dockerstream.Session {
	t.Helper()
	if err := env.sessions.Register(dockerstream.Meta{
		SessionID: agentproto.DockerBuildSessionID(buildRecRef), DeviceID: device, UserID: user,
		Action: agentproto.DockerActionImageBuild, Ref: buildRecRef, Kind: dockerstream.KindBuild,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return env.sessions.Get(agentproto.DockerBuildSessionID(buildRecRef))
}

func buildStreamContext(rec *dockerstate.CmdRecord, uid uint64) (*httptest.ResponseRecorder, *gin.Context, context.CancelFunc) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+rec.Ref+"/build", nil).WithContext(ctx)
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: rec.Ref}}
	c.Set(middleware.CtxClaims, testClaims(uid))
	return w, c, cancel
}

const pushRecRef = "1790000000000000889"

func seedPushRecord(t *testing.T, env *dockerTestEnv, device, user uint64) *dockerstate.CmdRecord {
	t.Helper()
	rec := &dockerstate.CmdRecord{Ref: pushRecRef, DeviceID: device, Action: agentproto.DockerActionImagePush,
		Target: "app:1", UserID: user, Perm: "docker:manage"}
	if err := env.cmds.Create(context.Background(), rec, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	return rec
}

func seedPushProgressSession(t *testing.T, env *dockerTestEnv, device, user uint64) *dockerstream.Session {
	t.Helper()
	if err := env.sessions.Register(dockerstream.Meta{
		SessionID: agentproto.DockerPushSessionID(pushRecRef), DeviceID: device, UserID: user,
		Action: agentproto.DockerActionImagePush, Ref: pushRecRef, Kind: dockerstream.KindPush,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return env.sessions.Get(agentproto.DockerPushSessionID(pushRecRef))
}

func pushStreamContext(rec *dockerstate.CmdRecord, uid uint64) (*httptest.ResponseRecorder, *gin.Context, context.CancelFunc) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+rec.Ref+"/push", nil).WithContext(ctx)
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: rec.Ref}}
	c.Set(middleware.CtxClaims, testClaims(uid))
	return w, c, cancel
}

func buildLineOf(t *testing.T, p agentproto.DockerBuildProgressItem) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func pushLineOf(t *testing.T, p agentproto.DockerPushProgressItem) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

// TestBuildStreamRejects：权限/归属/action 不匹配/会话不存在四条拒绝路径。
func TestBuildStreamRejects(t *testing.T) {
	t.Run("无权限 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:list"}) // 缺 docker:manage
		rec := seedBuildRecord(t, env, 7, 1)
		w, c, cancel := buildStreamContext(rec, 1)
		defer cancel()
		env.handler.BuildStream(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("无权限必须 403: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("非发起人 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedBuildRecord(t, env, 7, 1)
		w, c, cancel := buildStreamContext(rec, 2)
		defer cancel()
		env.handler.BuildStream(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("非发起人必须 403: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("非构建指令 400", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPullRecord(t, env, 7, 1) // 拉取指令走构建端点
		w, c, cancel := buildStreamContext(rec, 1)
		defer cancel()
		env.handler.BuildStream(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "支持构建") {
			t.Fatalf("非构建指令必须 400: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("会话不存在 404", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedBuildRecord(t, env, 7, 1) // 注册表未预登记
		w, c, cancel := buildStreamContext(rec, 1)
		defer cancel()
		env.handler.BuildStream(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("会话不存在必须 404: %d %s", w.Code, w.Body.String())
		}
	})
}

// TestBuildStreamForwardsNDJSON：构建进度记录逐行转成打平字段的 NDJSON 行
// （文本行与步骤行、终态 done 与 eof 挂最后一行、eof 转发完清理会话、正常收尾
// 不发 cancel）—— 与 pull/stats 同一条管线纪律。
func TestBuildStreamForwardsNDJSON(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedBuildRecord(t, env, 7, 1)
	seedBuildProgressSession(t, env, 7, 1)

	sid := agentproto.DockerBuildSessionID(buildRecRef)
	double := append(
		buildLineOf(t, agentproto.DockerBuildProgressItem{T: 1790000000000, Stream: "#1 load build definition"}),
		buildLineOf(t, agentproto.DockerBuildProgressItem{T: 1790000000000, ID: "Step 1/2", Status: "FROM node:20"})...)
	for _, f := range []*agentproto.DockerFrame{
		{SessionID: sid, Seq: 1, Data: double},
		{SessionID: sid, Seq: 2, Data: buildLineOf(t, agentproto.DockerBuildProgressItem{T: 1790000000100, Done: true}), EOF: true},
	} {
		if err := env.sessions.DeliverDockerFrame(ctx, 7, f); err != nil {
			t.Fatal(err)
		}
	}

	w, c, cancel := buildStreamContext(rec, 1)
	defer cancel()
	env.handler.BuildStream(c)

	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("Content-Type 必须是 application/x-ndjson，实际 %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 行 NDJSON（2 进度行 + 终态行），实际 %d: %q", len(lines), lines)
	}
	type line struct {
		Seq    uint64 `json:"seq"`
		ID     string `json:"id"`
		Status string `json:"status"`
		Stream string `json:"stream"`
		Done   bool   `json:"done"`
		EOF    bool   `json:"eof"`
	}
	var got []line
	for i, raw := range lines {
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v (%q)", i, err, raw)
		}
		got = append(got, l)
	}
	if got[0].Seq != 1 || got[0].Stream != "#1 load build definition" {
		t.Fatalf("文本行不符: %+v", got[0])
	}
	if got[1].Seq != 1 || got[1].ID != "Step 1/2" || got[1].Status != "FROM node:20" {
		t.Fatalf("步骤行不符: %+v", got[1])
	}
	if got[2].Seq != 2 || !got[2].Done || !got[2].EOF {
		t.Fatalf("终态行必须带 done 与 eof: %+v", got[2])
	}
	if env.sessions.Get(sid) != nil {
		t.Fatal("eof 转发完必须清理会话")
	}
	for _, fr := range env.sender.frameOps() {
		if fr.Op == agentproto.DockerFrameOpCancel {
			t.Fatalf("正常 eof 收尾不得下发 cancel: %+v", fr)
		}
	}
}

// TestBuildStreamDisconnectCancelsAgent：客户端断开 → 下发 cancel（对构建的语义
// 重量与拉取同款：它**终止这场构建**）、清理会话。
func TestBuildStreamDisconnectCancelsAgent(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedBuildRecord(t, env, 7, 1)
	sess := seedBuildProgressSession(t, env, 7, 1)

	_, c, cancel := buildStreamContext(rec, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.BuildStream(c)
	}()
	waitStream(t, "接入完成", func() bool { return sess.Attached() })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后处理器必须返回")
	}
	if env.sessions.Get(agentproto.DockerBuildSessionID(buildRecRef)) != nil {
		t.Fatal("断开后必须清理会话")
	}
	frames := env.sender.frameOps()
	if len(frames) != 1 || frames[0].Op != agentproto.DockerFrameOpCancel ||
		frames[0].SessionID != agentproto.DockerBuildSessionID(buildRecRef) {
		t.Fatalf("断开必须下发 cancel（语义 = 放弃构建）: %+v", frames)
	}
}

// TestBuildStreamSkipsInvalidLine：解不开的构建行跳过留痕，合法行照常 ——
// 丢一眼构建输出不致命，把整条流转成错误才是（与 pull 同纪律）。
func TestBuildStreamSkipsInvalidLine(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedBuildRecord(t, env, 7, 1)
	seedBuildProgressSession(t, env, 7, 1)

	sid := agentproto.DockerBuildSessionID(buildRecRef)
	if err := env.sessions.DeliverDockerFrame(ctx, 7, &agentproto.DockerFrame{
		SessionID: sid, Seq: 1,
		Data: append([]byte("not-a-line\n"), buildLineOf(t, agentproto.DockerBuildProgressItem{
			T: 1790000000000, Stream: "#2 RUN npm install"})...)}); err != nil {
		t.Fatal(err)
	}
	if err := env.sessions.DeliverDockerFrame(ctx, 7, &agentproto.DockerFrame{
		SessionID: sid, Seq: 2, EOF: true}); err != nil {
		t.Fatal(err)
	}

	w, c, cancel := buildStreamContext(rec, 1)
	defer cancel()
	env.handler.BuildStream(c)
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("坏行应被跳过：应有 2 行（好记录 + eof），实际 %d: %q", len(lines), lines)
	}
	var l struct {
		Stream string `json:"stream"`
		EOF    bool   `json:"eof"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &l); err != nil || l.Stream != "#2 RUN npm install" {
		t.Fatalf("好记录必须照常转发: %v %+v", err, l)
	}
	if err := json.Unmarshal([]byte(lines[1]), &l); err != nil || !l.EOF {
		t.Fatalf("收尾行必须是 eof: %v %+v", err, l)
	}
}

// TestPushStreamRejectsAndForwards：推送端点四条拒绝路径 + 转发形状
// （与 pull 同字段集 —— 同形不同 item 类型）。
func TestPushStreamRejectsAndForwards(t *testing.T) {
	t.Run("无权限 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:list"})
		rec := seedPushRecord(t, env, 7, 1)
		w, c, cancel := pushStreamContext(rec, 1)
		defer cancel()
		env.handler.PushStream(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("无权限必须 403: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("非发起人 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPushRecord(t, env, 7, 1)
		w, c, cancel := pushStreamContext(rec, 2)
		defer cancel()
		env.handler.PushStream(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("非发起人必须 403: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("非推送指令 400", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPullRecord(t, env, 7, 1)
		w, c, cancel := pushStreamContext(rec, 1)
		defer cancel()
		env.handler.PushStream(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "支持推送") {
			t.Fatalf("非推送指令必须 400: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("会话不存在 404", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPushRecord(t, env, 7, 1)
		w, c, cancel := pushStreamContext(rec, 1)
		defer cancel()
		env.handler.PushStream(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("会话不存在必须 404: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("转发 NDJSON", func(t *testing.T) {
		ctx := context.Background()
		env := newTestDockerHandler(t, []string{"docker:manage"})
		rec := seedPushRecord(t, env, 7, 1)
		seedPushProgressSession(t, env, 7, 1)

		sid := agentproto.DockerPushSessionID(pushRecRef)
		for _, f := range []*agentproto.DockerFrame{
			{SessionID: sid, Seq: 1, Data: pushLineOf(t, agentproto.DockerPushProgressItem{
				T: 1790000000000, ID: "aaa", Status: "Pushing", Current: 4096, Total: 8192})},
			{SessionID: sid, Seq: 2, Data: pushLineOf(t, agentproto.DockerPushProgressItem{
				T: 1790000000100, Done: true}), EOF: true},
		} {
			if err := env.sessions.DeliverDockerFrame(ctx, 7, f); err != nil {
				t.Fatal(err)
			}
		}
		w, c, cancel := pushStreamContext(rec, 1)
		defer cancel()
		env.handler.PushStream(c)

		lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("应有 2 行 NDJSON，实际 %d: %q", len(lines), lines)
		}
		var first struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Current int64  `json:"current"`
			Total   int64  `json:"total"`
			EOF     bool   `json:"eof"`
		}
		if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.ID != "aaa" ||
			first.Status != "Pushing" || first.Current != 4096 || first.Total != 8192 || first.EOF {
			t.Fatalf("推送进度行不符: %v %+v", err, first)
		}
		var last struct {
			Done bool `json:"done"`
			EOF  bool `json:"eof"`
		}
		if err := json.Unmarshal([]byte(lines[1]), &last); err != nil || !last.Done || !last.EOF {
			t.Fatalf("终态行必须带 done 与 eof: %v %+v", err, last)
		}
		if env.sessions.Get(sid) != nil {
			t.Fatal("eof 转发完必须清理会话")
		}
	})
}

// TestPushStreamDisconnectCancelsAgent：断开 → 下发 cancel（终止推送）并清理。
func TestPushStreamDisconnectCancelsAgent(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:manage"})
	rec := seedPushRecord(t, env, 7, 1)
	sess := seedPushProgressSession(t, env, 7, 1)

	_, c, cancel := pushStreamContext(rec, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.PushStream(c)
	}()
	waitStream(t, "接入完成", func() bool { return sess.Attached() })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后处理器必须返回")
	}
	if env.sessions.Get(agentproto.DockerPushSessionID(pushRecRef)) != nil {
		t.Fatal("断开后必须清理会话")
	}
	frames := env.sender.frameOps()
	if len(frames) != 1 || frames[0].Op != agentproto.DockerFrameOpCancel ||
		frames[0].SessionID != agentproto.DockerPushSessionID(pushRecRef) {
		t.Fatalf("断开必须下发 cancel（语义 = 放弃推送）: %+v", frames)
	}
}
